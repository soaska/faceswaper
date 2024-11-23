package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

// PocketBase global credentials
var pocketBaseUrl string
var email string
var password string
var authToken string


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

	return body, nil
}



func downloadTelegramFile(bot *tgbotapi.BotAPI, fileID, destinationPath string) error {
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return fmt.Errorf("не удалось получить файл по ID: %v", err)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, file.FilePath)

	// Download request
	resp, err := http.Get(fileURL)
	if err != nil {
		return fmt.Errorf("не удалось скачать файл: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("не удалось скачать файл, статус: %s", resp.Status)
	}

	// Save on disk
	out, err := os.Create(destinationPath)
	if err != nil {
		return fmt.Errorf("не удалось создать файл: %v", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("не удалось сохранить содержимое файла: %v", err)
	}

	return nil
}



// loading env variables from .env or system environment
func LoadEnvironment() (string, bool) {
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

	// pocketbase
	pocketBaseUrl = os.Getenv("POCKETBASE_URL")
	if pocketBaseUrl == `` {
		log.Fatal("empty pocketbase url loaded, check POCKETBASE_URL value")
	} else {
		// validate db url
		_, err := url.ParseRequestURI(pocketBaseUrl)
		if err != nil {
			log.Fatal(err)
		}
	}
	email = os.Getenv("POCKETBASE_LOGIN")
	if email == `` {
		log.Fatal("empty pocketbase login loaded. env is not correct or configuration is insecure")
	}

	password = os.Getenv("POCKETBASE_PASSWORD")
	if password == `` {
		log.Fatal("empty pocketbase password loaded. env is not correct or configuration is insecure")
	}

	return bot_token, bot_debug
}
