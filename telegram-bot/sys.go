package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/joho/godotenv"
)

// 403 error tracking
var (
	forbidden403Mutex    sync.Mutex
	forbidden403MaxCount = 1
	forbidden403Window   = 5 * time.Minute
	forbidden403Times    []time.Time
)

var Err403TooMany = fmt.Errorf("слишком много ошибок 403 при доступе к базе данных")

func track403Error() {
	forbidden403Mutex.Lock()
	defer forbidden403Mutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-forbidden403Window)
	var recent []time.Time
	for _, t := range forbidden403Times {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	forbidden403Times = recent

	log.Printf("Ошибка 403 при доступе к базе данных. Количество за последние %v: %d/%d",
		forbidden403Window, len(forbidden403Times), forbidden403MaxCount)

	if len(forbidden403Times) >= forbidden403MaxCount {
		log.Printf("КРИТИЧЕСКАЯ ОШИБКА: Слишком много ошибок 403 (%d за %v). Завершение программы.",
			len(forbidden403Times), forbidden403Window)
		os.Exit(3)
	}
}

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
	client := &http.Client{}
	req, err := http.NewRequest(method, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	if authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Проверка на 403 ошибку
	if resp.StatusCode == http.StatusForbidden {
		track403Error()
		return nil, fmt.Errorf("ошибка 403 Forbidden при доступе к базе данных: %s", string(body))
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
	resp, err := http.Get(callURL)
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
	fileResp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("ошибка скачивания файла: %v", err)
	}
	defer fileResp.Body.Close()

	if fileResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ошибка скачивания файла, статус: %d", fileResp.StatusCode)
	}

	// 3. Save to temporary file
	tempFile, err := os.CreateTemp("", "tg_file_*"+filepath.Ext(filePath))
	if err != nil {
		return "", fmt.Errorf("ошибка создания временного файла: %v", err)
	}
	defer tempFile.Close()

	_, err = io.Copy(tempFile, fileResp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка сохранения файла: %v", err)
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
