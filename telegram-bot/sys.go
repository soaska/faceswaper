package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/joho/godotenv"
)

var (
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
	}
)

// PocketBase global credentials
var (
	pocketBaseUrl string
	email         string
	password      string
	authToken     string
	apiEndpoint   string
)

// just for sending search requests to pocketbase
func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
	return sendAuthorizedRequestWithRetry(method, url, payload, true)
}

func sendAuthorizedRequestWithRetry(method, url string, payload []byte, allowRetry bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Check for auth errors and attempt token refresh
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if allowRetry {
			log.Printf("Получена ошибка %d, пробуем обновить токен...", resp.StatusCode)
			if err := authenticatePocketBase(); err != nil {
				log.Printf("Не удалось обновить токен: %v", err)
				return nil, fmt.Errorf("ошибка авторизации при доступе к базе данных: %s", string(body))
			}
			log.Printf("Токен успешно обновлен, повторяем запрос")
			return sendAuthorizedRequestWithRetry(method, url, payload, false)
		}
		return nil, fmt.Errorf("ошибка %d при доступе к базе данных: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

type FileResponse struct {
	Ok     bool                   `json:"ok"`
	Result map[string]interface{} `json:"result"`
}

func getTelegramFile(bot *tgbotapi.BotAPI, fileID string) (string, error) {
	// 1. Get file path info from Telegram API
	callURL := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", apiEndpoint, bot.Token, fileID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", callURL, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка подготовки запроса: %v", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка http запроса: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка чтения ответа сервера: %v", err)
	}

	fileResponse := &FileResponse{}
	err = json.Unmarshal(responseBody, fileResponse)
	if err != nil {
		return "", fmt.Errorf("ошибка расшифровки ответа JSON: %v", err)
	}

	if !fileResponse.Ok {
		return "", fmt.Errorf("ошибка получения информации о файле: %v", resp.StatusCode)
	}

	filePath, ok := fileResponse.Result["file_path"].(string)
	if !ok || filePath == "" {
		return "", fmt.Errorf("не найден путь в ответе сервера")
	}

	// 2. Download the actual file
	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", apiEndpoint, bot.Token, filePath)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err = http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка подготовки запроса скачивания: %v", err)
	}

	fileResp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка скачивания файла: %v", err)
	}
	defer fileResp.Body.Close()

	if fileResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ошибка скачивания файла, статус: %d", fileResp.StatusCode)
	}

	if cl := fileResp.ContentLength; cl > 0 && cl > 200*1024*1024 {
		return "", fmt.Errorf("размер файла превышает лимит 200MB")
	}

	// 3. Save to temporary file
	tempFile, err := os.CreateTemp("", "tg_file_*"+filepath.Ext(filePath))
	if err != nil {
		return "", fmt.Errorf("ошибка создания временного файла: %v", err)
	}
	defer tempFile.Close()

	const maxDownloadSize = int64(200 * 1024 * 1024)
	limited := io.LimitReader(fileResp.Body, maxDownloadSize+1)
	written, err := io.Copy(tempFile, limited)
	if err != nil {
		return "", fmt.Errorf("ошибка сохранения файла: %v", err)
	}
	if written > maxDownloadSize {
		return "", fmt.Errorf("размер файла превышает лимит 200MB")
	}

	return tempFile.Name(), nil
}

// loading env variables from .env or system environment
func LoadEnvironment() (string, bool, string) {
	if os.Getenv("DOCKER_BUILD") == `` {
		err := godotenv.Load()
		if err != nil {
			log.Fatalf("Error loading .env file")
		}
	}

	// load telegram token
	bot_token := os.Getenv("TELEGRAM_APITOKEN")
	if bot_token == `` {
		log.Fatal("empty telegram api token loaded, check TELEGRAM_APITOKEN value")
	}

	// telegram bot debug mode
	var bot_debug bool
	if os.Getenv("BOT_DEBUG") == `true` {
		bot_debug = true
	} else {
		bot_debug = false
	}

	apiEndpoint = os.Getenv("TELEGRAM_API")
	if apiEndpoint == `` {
		apiEndpoint = "https://api.telegram.org"
	}

	// pocketbase
	pocketBaseUrl = os.Getenv("POCKETBASE_URL")
	if pocketBaseUrl == `` {
		log.Fatal("empty pocketbase url loaded, check POCKETBASE_URL value")
	}

	email = os.Getenv("POCKETBASE_LOGIN")
	if email == `` {
		log.Fatal("empty pocketbase login loaded. env is not correct or configuration is insecure")
	}

	password = os.Getenv("POCKETBASE_PASSWORD")
	if password == `` {
		log.Fatal("empty pocketbase password loaded. env is not correct or configuration is insecure")
	}

	return bot_token, bot_debug, apiEndpoint
}
