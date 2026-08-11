package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	sharedconfig "github.com/soaska/faceswaper/shared/config"
	sharedpb "github.com/soaska/faceswaper/shared/pocketbase"
)

var (
	pocketBaseUrl    string
	pocketBaseClient *sharedpb.Client
	apiEndpoint      string
)

var (
	apiHTTPClient = &http.Client{Timeout: 30 * time.Second}
	mediaClient   = &http.Client{Timeout: 30 * time.Minute}
	botHTTPClient = &http.Client{Timeout: 75 * time.Second}
)

func sendAuthorizedRequest(method, requestURL string, payload []byte) ([]byte, error) {
	if pocketBaseClient == nil {
		return nil, fmt.Errorf("клиент PocketBase не настроен")
	}
	return pocketBaseClient.Do(method, requestURL, payload)
}

func authenticatePocketBase() error {
	if pocketBaseClient == nil {
		return fmt.Errorf("клиент PocketBase не настроен")
	}
	if err := pocketBaseClient.Authenticate(); err != nil {
		return err
	}
	log.Println("PocketBase: авторизация прошла успешно")
	return nil
}

func configurePocketBase(baseURL, identity, password string, httpClient *http.Client) {
	pocketBaseUrl = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	pocketBaseClient = sharedpb.New(pocketBaseUrl, identity, password, httpClient)
}

func limitedResponse(body []byte) string {
	return sharedpb.LimitedBody(body)
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
	common, err := sharedconfig.LoadCommon()
	if err != nil {
		log.Fatal(err)
	}
	apiEndpoint = common.TelegramAPI
	configurePocketBase(common.PocketBaseURL, common.PocketBaseID, common.PocketBaseKey, apiHTTPClient)
	return common.TelegramToken, common.TelegramDebug, common.TelegramAPI
}
