package main

import (
	"bytes"
	"context"
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
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
	}
	// optional download cap (0 = unlimited, preserves previous behavior)
	maxDownloadSize int64 = 0
)

// 403 error tracking
var (
	forbidden403Mutex    sync.Mutex
	forbidden403MaxCount = 5
	forbidden403Window   = 5 * time.Minute
	forbidden403Times    []time.Time
)

var Err403TooMany = fmt.Errorf("слишком много ошибок 403 при доступе к базе данных")

func track403Error() error {
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
		log.Printf("КРИТИЧЕСКАЯ ОШИБКА: Слишком много ошибок 403 (%d за %v). Требуется бэкофф и повторная авторизация.",
			len(forbidden403Times), forbidden403Window)
		return Err403TooMany
	}
	return nil
}

// PocketBase global credentials
var (
	pocketBaseUrl string
	email         string
	password      string
	authToken     string
)

// tgbot globals
var (
	BOT_TOKEN    string
	BOT_ENDPOINT string
)

/**
 * FaceSwapComponent
 */
var (
	FaceSwapComponent_URL   string
	FaceSwapComponentSecret string
)

// just for sending search requests to pocketbase
func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
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

	// Проверка на 403 ошибку
	if resp.StatusCode == http.StatusForbidden {
		if err := track403Error(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("ошибка 403 Forbidden при доступе к базе данных: %s", string(body))
	}

	return body, nil
}

// loading env variables from .env or system environment
func LoadEnvironment() (string, bool, string, string) {
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

	bot_endpoint := os.Getenv("TELEGRAM_API")
	if bot_endpoint == `` {
		bot_endpoint = "https://api.telegram.org"
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

	// FaceSwapComponent
	FaceSwapComponentUrl := os.Getenv("FaceSwapComponent_URL")
	if FaceSwapComponentUrl == "" {
		log.Fatalf("переменная окружения FaceSwapComponent_URL не установлена")
	}
	FaceSwapComponentSecret = os.Getenv("FaceSwapComponent_SECRET")

	return bot_token, bot_debug, bot_endpoint, FaceSwapComponentUrl
}

// Скачивание файла
func downloadFile(url, destination string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("ошибка подготовки запроса: %v", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка скачивания: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("ошибка скачивания: статус %d, ответ: %s", resp.StatusCode, string(body))
	}

	// Basic content-type guard
	ext := strings.ToLower(filepath.Ext(destination))
	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		if ext == ".mp4" && !strings.HasPrefix(ct, "video") {
			return fmt.Errorf("недопустимый тип содержимого для видео: %s", ct)
		}
		if (ext == ".jpg" || ext == ".jpeg" || ext == ".png") && !strings.HasPrefix(ct, "image") {
			return fmt.Errorf("недопустимый тип содержимого для изображения: %s", ct)
		}
	}

	var limitedReader io.Reader = resp.Body
	if maxDownloadSize > 0 {
		limitedReader = io.LimitReader(resp.Body, maxDownloadSize+1)
	}

	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("ошибка создания файла: %v", err)
	}
	defer file.Close()

	written, err := io.Copy(file, limitedReader)
	if err != nil {
		return fmt.Errorf("ошибка сохранения файла: %v", err)
	}
	if maxDownloadSize > 0 && written > maxDownloadSize {
		return fmt.Errorf("файл превышает лимит %d байт", maxDownloadSize)
	}

	return nil
}

// Отправка сообщения в Telegram
func sendTelegramMessage(chatID string, message string) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", BOT_ENDPOINT, BOT_TOKEN)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	err := writer.WriteField("chat_id", chatID)
	if err != nil {
		return fmt.Errorf("ошибка добавления поля chat_id: %v", err)
	}

	err = writer.WriteField("text", message)
	if err != nil {
		return fmt.Errorf("ошибка добавления поля text: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка закрытия записи multipart данных: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания HTTP-запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса Telegram API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("ошибка чтения ответа Telegram API: %v", err)
		}
		return fmt.Errorf("ошибка в Telegram API. Код %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Отправка видео через Telegram
func sendTelegramVideo(chatID string, filePath string) error {
	// Открываем файл
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	// Создаем multipart запрос
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем chat_id
	err = writer.WriteField("chat_id", chatID)
	if err != nil {
		return fmt.Errorf("ошибка добавления поля chat_id: %v", err)
	}

	// Добавляем видео
	filePart, err := writer.CreateFormFile("video", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("ошибка создания части файла: %v", err)
	}
	_, err = io.Copy(filePart, file)
	if err != nil {
		return fmt.Errorf("ошибка копирования файла: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	// Отправляем запрос
	url := fmt.Sprintf("%s/bot%s/sendVideo", BOT_ENDPOINT, BOT_TOKEN)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("ошибка чтения ответа при ошибке отправки видео: %v", err)
		}
		return fmt.Errorf("ошибка отправки видео: статус %d, ответ: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Отправка фото пользователю
func sendTelegramPhoto(chatID string, filePath string) error {
	// Открываем файл
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	// Создаем multipart запрос
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем chat_id
	err = writer.WriteField("chat_id", chatID)
	if err != nil {
		return fmt.Errorf("ошибка добавления поля chat_id: %v", err)
	}

	// Добавляем фото
	filePart, err := writer.CreateFormFile("photo", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("ошибка создания части файла: %v", err)
	}
	_, err = io.Copy(filePart, file)
	if err != nil {
		return fmt.Errorf("ошибка копирования файла: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	// Отправляем запрос
	url := fmt.Sprintf("%s/bot%s/sendPhoto", BOT_ENDPOINT, BOT_TOKEN)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("ошибка чтения ответа при ошибке отправки фото: %v", err)
		}
		return fmt.Errorf("ошибка отправки фото: статус %d, ответ: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Отправка уведомления об ошибке пользователю
func sendErrorNotification(ownerID, taskID string) error {
	// Получаем Telegram ID владельца
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}

	// Формируем сообщение об ошибке
	message := fmt.Sprintf("Ваша задача %s завершилась неудачей. Проверьте /status", taskID)

	return sendTelegramMessage(ownerTGID, message)
}

// Отправка видео пользователю
func sendVideoToUser(ownerID, filePath string) error {
	// Получаем Telegram ID владельца
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}

	return sendTelegramVideo(ownerTGID, filePath)
}

// Отправка фото пользователю
func sendPhotoToUser(ownerID, filePath string) error {
	// Получаем Telegram ID владельца
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}

	return sendTelegramPhoto(ownerTGID, filePath)
}
