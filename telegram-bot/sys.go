package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/joho/godotenv"
)

// Token refresh mutex to prevent concurrent refresh attempts
var tokenRefreshMutex sync.Mutex

// PocketBase global credentials
var (
	pocketBaseUrl string
	email         string
	password      string
	authToken     string
	api_endpint   string
)

// refreshToken re-authenticates with PocketBase and updates the authToken
func refreshToken() error {
	tokenRefreshMutex.Lock()
	defer tokenRefreshMutex.Unlock()

	log.Println("PocketBase: Обновление JWT токена...")
	err := authenticatePocketBase()
	if err != nil {
		return fmt.Errorf("не удалось обновить JWT токен: %v", err)
	}
	log.Println("PocketBase: JWT токен успешно обновлён")
	return nil
}

// sendAuthorizedRequest sends a request to PocketBase with JWT authentication
// If a 401/403 error occurs, it will refresh the token and retry once
func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
	body, statusCode, err := doAuthorizedRequest(method, url, payload)
	if err != nil {
		return nil, err
	}

	// Check for auth errors (401 Unauthorized or 403 Forbidden)
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		log.Printf("PocketBase: Получен код %d, пробуем обновить токен...", statusCode)

		// Refresh the token
		if refreshErr := refreshToken(); refreshErr != nil {
			return nil, fmt.Errorf("ошибка обновления токена после %d: %v", statusCode, refreshErr)
		}

		// Retry the request with the new token
		body, statusCode, err = doAuthorizedRequest(method, url, payload)
		if err != nil {
			return nil, err
		}

		// If still getting auth error after refresh, return error
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			return nil, fmt.Errorf("ошибка авторизации после обновления токена, код: %d, ответ: %s", statusCode, string(body))
		}
	}

	return body, nil
}

// doAuthorizedRequest performs the actual HTTP request
func doAuthorizedRequest(method, url string, payload []byte) ([]byte, int, error) {
	client := &http.Client{}
	req, err := http.NewRequest(method, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	if authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	return body, resp.StatusCode, nil
}

type FileResponse struct {
	Ok     bool                   `json:"ok"`
	Result map[string]interface{} `json:"result"`
}

func getTelegramFile(bot *tgbotapi.BotAPI, fileID string) (string, error) {
	// file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	CallUrl := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", api_endpint, bot.Token, fileID)
	resp, err := http.Get(CallUrl)
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

	if fileResponse.Ok {
		filePath, ok := fileResponse.Result["file_path"]
		if !ok || filePath == "" {
			return "", fmt.Errorf("не найден путь в ответе сервера")
		}
		// serverPath := fmt.Sprintf("/storage/%s/%s", bot.Token, filePath.(string))
		return filePath.(string), nil
	} else {
		return "", fmt.Errorf("ошибка получения пути: %v", resp.StatusCode)
	}
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

	api_endpint = os.Getenv("TELEGRAM_API")
	if api_endpint == `` {
		api_endpint = "https://api.telegram.org"
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

	return bot_token, bot_debug, api_endpint
}
