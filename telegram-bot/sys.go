package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/joho/godotenv"
)

var (
	pocketBaseUrl string
	email         string
	password      string
	authToken     string
	apiEndpoint   string
	tokenMutex    sync.RWMutex
	refreshMutex  sync.Mutex
)

var (
	apiHTTPClient = &http.Client{Timeout: 30 * time.Second}
	mediaClient   = &http.Client{Timeout: 30 * time.Minute}
	botHTTPClient = &http.Client{Timeout: 75 * time.Second}
)

func sendAuthorizedRequest(method, requestURL string, payload []byte) ([]byte, error) {
	body, statusCode, err := doAuthorizedRequest(method, requestURL, payload)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		refreshMutex.Lock()
		refreshErr := authenticatePocketBase()
		if refreshErr == nil {
			body, statusCode, err = doAuthorizedRequest(method, requestURL, payload)
		}
		refreshMutex.Unlock()
		if refreshErr != nil {
			return nil, fmt.Errorf("ошибка обновления авторизации PocketBase: %v", refreshErr)
		}
		if err != nil {
			return nil, err
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("PocketBase вернул код %d: %s", statusCode, limitedResponse(body))
	}
	return body, nil
}

func doAuthorizedRequest(method, requestURL string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка создания запроса PocketBase: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token := currentAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка запроса PocketBase: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка чтения ответа PocketBase: %v", err)
	}
	return body, resp.StatusCode, nil
}

func currentAuthToken() string {
	tokenMutex.RLock()
	defer tokenMutex.RUnlock()
	return authToken
}

func setAuthToken(token string) {
	tokenMutex.Lock()
	authToken = token
	tokenMutex.Unlock()
}

func limitedResponse(body []byte) string {
	const maxLength = 1000
	if len(body) <= maxLength {
		return string(body)
	}
	return string(body[:maxLength]) + "…"
}

type fileResponse struct {
	OK          bool       `json:"ok"`
	Description string     `json:"description"`
	Result      fileResult `json:"result"`
}

type fileResult struct {
	FilePath string `json:"file_path"`
}

func getTelegramFile(bot *tgbotapi.BotAPI, fileID string) (string, error) {
	requestURL := fmt.Sprintf(
		"%s/bot%s/getFile?file_id=%s",
		apiEndpoint,
		bot.Token,
		url.QueryEscape(fileID),
	)
	resp, err := apiHTTPClient.Get(requestURL)
	if err != nil {
		return "", fmt.Errorf("ошибка запроса файла Telegram: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка чтения ответа Telegram: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Telegram API вернул код %d: %s", resp.StatusCode, limitedResponse(body))
	}

	var response fileResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("ошибка разбора ответа Telegram: %v", err)
	}
	if !response.OK || response.Result.FilePath == "" {
		return "", fmt.Errorf("Telegram API не вернул путь к файлу: %s", response.Description)
	}
	if filepath.IsAbs(response.Result.FilePath) {
		return filepath.Clean(response.Result.FilePath), nil
	}

	return downloadTelegramFile(bot.Token, response.Result.FilePath)
}

func downloadTelegramFile(token, remotePath string) (string, error) {
	cacheDir := botCacheDirectory()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("ошибка создания временной директории: %v", err)
	}
	extension := strings.ToLower(filepath.Ext(remotePath))
	if len(extension) > 10 || strings.ContainsAny(extension, `/\\`) {
		extension = ""
	}
	file, err := os.CreateTemp(cacheDir, "telegram-*"+extension)
	if err != nil {
		return "", fmt.Errorf("ошибка создания временного файла: %v", err)
	}
	filePath := file.Name()
	removeFile := true
	defer func() {
		_ = file.Close()
		if removeFile {
			_ = os.Remove(filePath)
		}
	}()

	requestURL := fmt.Sprintf("%s/file/bot%s/%s", apiEndpoint, token, strings.TrimLeft(remotePath, "/"))
	resp, err := mediaClient.Get(requestURL)
	if err != nil {
		return "", fmt.Errorf("ошибка скачивания файла Telegram: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return "", fmt.Errorf("Telegram API вернул код %d при скачивании: %s", resp.StatusCode, string(body))
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", fmt.Errorf("ошибка сохранения файла Telegram: %v", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("ошибка закрытия файла Telegram: %v", err)
	}
	removeFile = false
	return filePath, nil
}

func botCacheDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("BOT_CACHE_DIR")); configured != "" {
		return configured
	}
	if os.Getenv("DOCKER_BUILD") != "" {
		return "/tmp/faceswaper-bot"
	}
	return "cache"
}

func LoadEnvironment() (string, bool, string) {
	if os.Getenv("DOCKER_BUILD") == "" {
		if err := godotenv.Load(); err != nil {
			log.Fatal("Не удалось загрузить .env")
		}
	}

	botToken := strings.TrimSpace(os.Getenv("TELEGRAM_APITOKEN"))
	if botToken == "" {
		log.Fatal("TELEGRAM_APITOKEN не задан")
	}
	botDebug := os.Getenv("BOT_DEBUG") == "true"

	apiEndpoint = strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API")), "/")
	if apiEndpoint == "" {
		apiEndpoint = "https://api.telegram.org"
	}
	pocketBaseUrl = strings.TrimRight(strings.TrimSpace(os.Getenv("POCKETBASE_URL")), "/")
	if pocketBaseUrl == "" {
		log.Fatal("POCKETBASE_URL не задан")
	}
	email = strings.TrimSpace(os.Getenv("POCKETBASE_LOGIN"))
	if email == "" {
		log.Fatal("POCKETBASE_LOGIN не задан")
	}
	password = os.Getenv("POCKETBASE_PASSWORD")
	if password == "" {
		log.Fatal("POCKETBASE_PASSWORD не задан")
	}

	return botToken, botDebug, apiEndpoint
}
