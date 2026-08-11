package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

var (
	pocketBaseUrl string
	email         string
	password      string
	authToken     string
	tokenMutex    sync.RWMutex
	refreshMutex  sync.Mutex
)

var (
	BOT_TOKEN    string
	BOT_ENDPOINT string
)

var FaceSwapComponent_URL string

var (
	apiHTTPClient = &http.Client{
		Timeout: 30 * time.Second,
	}
	mediaHTTPClient = &http.Client{
		Timeout: 30 * time.Minute,
	}
)

func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
	body, statusCode, err := doAuthorizedRequest(method, url, payload)
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		refreshMutex.Lock()
		refreshErr := authenticatePocketBase()
		if refreshErr == nil {
			body, statusCode, err = doAuthorizedRequest(method, url, payload)
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
		return nil, fmt.Errorf("PocketBase вернул код %d: %s", statusCode, limitedBody(body))
	}
	return body, nil
}

func doAuthorizedRequest(method, url string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
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

func limitedBody(body []byte) string {
	const maxLength = 1000
	if len(body) <= maxLength {
		return string(body)
	}
	return string(body[:maxLength]) + "…"
}

func jobCacheDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("JOB_CACHE_DIR")); configured != "" {
		return configured
	}
	if os.Getenv("DOCKER_BUILD") != "" {
		return "/temp/job-manager"
	}
	return "cache"
}

func LoadEnvironment() (string, bool, string, string) {
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

	botEndpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API")), "/")
	if botEndpoint == "" {
		botEndpoint = "https://api.telegram.org"
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

	faceSwapURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FaceSwapComponent_URL")), "/")
	if faceSwapURL == "" {
		log.Fatal("FaceSwapComponent_URL не задан")
	}

	return botToken, botDebug, botEndpoint, faceSwapURL
}

func downloadFile(url, destination string) error {
	resp, err := mediaHTTPClient.Get(url)
	if err != nil {
		return fmt.Errorf("ошибка скачивания: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return fmt.Errorf("сервер вернул код %d при скачивании: %s", resp.StatusCode, string(body))
	}

	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("ошибка создания файла: %v", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("ошибка сохранения файла: %v", err)
	}
	return nil
}

func sendTelegramMessage(chatID, message string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return fmt.Errorf("ошибка добавления chat_id: %v", err)
	}
	if err := writer.WriteField("text", message); err != nil {
		return fmt.Errorf("ошибка добавления текста: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("ошибка завершения запроса Telegram: %v", err)
	}
	return sendTelegramRequest("sendMessage", writer.FormDataContentType(), body)
}

func sendTelegramVideo(chatID, filePath string) error {
	return sendTelegramFile("sendVideo", "video", chatID, filePath)
}

func sendTelegramPhoto(chatID, filePath string) error {
	return sendTelegramFile("sendPhoto", "photo", chatID, filePath)
}

func sendTelegramFile(method, field, chatID, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return fmt.Errorf("ошибка добавления chat_id: %v", err)
	}
	filePart, err := writer.CreateFormFile(field, filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("ошибка добавления файла в запрос: %v", err)
	}
	if _, err := io.Copy(filePart, file); err != nil {
		return fmt.Errorf("ошибка чтения файла: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("ошибка завершения запроса Telegram: %v", err)
	}

	return sendTelegramRequest(method, writer.FormDataContentType(), body)
}

func sendTelegramRequest(method, contentType string, body *bytes.Buffer) error {
	url := fmt.Sprintf("%s/bot%s/%s", BOT_ENDPOINT, BOT_TOKEN, method)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса Telegram: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка запроса Telegram: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа Telegram: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API вернул код %d: %s", resp.StatusCode, limitedBody(responseBody))
	}
	return nil
}

func sendErrorNotification(ownerID, taskID string) error {
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}
	message := fmt.Sprintf("Ваша задача %s завершилась неудачей. Проверьте /status", taskID)
	return sendTelegramMessage(ownerTGID, message)
}

func sendVideoToUser(ownerID, filePath string) error {
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}
	return sendTelegramVideo(ownerTGID, filePath)
}

func sendPhotoToUser(ownerID, filePath string) error {
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}
	return sendTelegramPhoto(ownerTGID, filePath)
}
