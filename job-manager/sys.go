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
	"strings"
	"time"

	"github.com/joho/godotenv"
	sharedpb "github.com/soaska/faceswaper/shared/pocketbase"
)

var (
	pocketBaseUrl    string
	pocketBaseClient *sharedpb.Client
)

var (
	BOT_TOKEN    string
	BOT_ENDPOINT string
)

var (
	FaceSwapComponent_URL string
	FaceSwapAPIKey        string
)

var (
	apiHTTPClient = &http.Client{
		Timeout: 30 * time.Second,
	}
	mediaHTTPClient = &http.Client{
		Timeout: 30 * time.Minute,
	}
)

func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
	if pocketBaseClient == nil {
		return nil, fmt.Errorf("клиент PocketBase не настроен")
	}
	return pocketBaseClient.Do(method, url, payload)
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

type pocketBaseStatusError = sharedpb.StatusError

func limitedBody(body []byte) string {
	return sharedpb.LimitedBody(body)
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
	email := strings.TrimSpace(os.Getenv("POCKETBASE_LOGIN"))
	if email == "" {
		log.Fatal("POCKETBASE_LOGIN не задан")
	}
	password := os.Getenv("POCKETBASE_PASSWORD")
	if password == "" {
		log.Fatal("POCKETBASE_PASSWORD не задан")
	}
	configurePocketBase(pocketBaseUrl, email, password, apiHTTPClient)

	faceSwapURL := configuredFaceSwapURL()
	if faceSwapURL == "" {
		log.Fatal("FACE_SWAP_URL не задан")
	}
	FaceSwapAPIKey = strings.TrimSpace(os.Getenv("FACE_SWAP_API_KEY"))
	if FaceSwapAPIKey == "" {
		log.Fatal("FACE_SWAP_API_KEY не задан")
	}

	return botToken, botDebug, botEndpoint, faceSwapURL
}

func configuredFaceSwapURL() string {
	faceSwapURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FACE_SWAP_URL")), "/")
	if faceSwapURL == "" {
		// Backward compatibility for deployments created by the old AI branch.
		faceSwapURL = strings.TrimRight(strings.TrimSpace(os.Getenv("FaceSwapComponent_URL")), "/")
	}
	return faceSwapURL
}

func downloadFile(ctx context.Context, url, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса скачивания: %v", err)
	}
	resp, err := mediaHTTPClient.Do(request)
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

func sendTelegramVideo(ctx context.Context, chatID, filePath string) error {
	return sendTelegramFile(ctx, "sendVideo", "video", chatID, filePath)
}

func sendTelegramPhoto(ctx context.Context, chatID, filePath string) error {
	return sendTelegramFile(ctx, "sendPhoto", "photo", chatID, filePath)
}

func sendTelegramFile(ctx context.Context, method, field, chatID, filePath string) error {
	url := fmt.Sprintf("%s/bot%s/%s", BOT_ENDPOINT, BOT_TOKEN, method)
	resp, err := doStreamingMultipartFileRequest(
		ctx,
		mediaHTTPClient,
		http.MethodPost,
		url,
		nil,
		map[string]string{"chat_id": chatID},
		field,
		filePath,
	)
	if err != nil {
		return fmt.Errorf("ошибка запроса Telegram: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа Telegram: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram API вернул код %d: %s", resp.StatusCode, limitedBody(responseBody))
	}
	return nil
}

func sendTelegramRequest(method, contentType string, body *bytes.Buffer) error {
	url := fmt.Sprintf("%s/bot%s/%s", BOT_ENDPOINT, BOT_TOKEN, method)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса Telegram: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := apiHTTPClient.Do(req)
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

func sendVideoToUser(ctx context.Context, ownerID, filePath string) error {
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}
	return sendTelegramVideo(ctx, ownerTGID, filePath)
}

func sendPhotoToUser(ctx context.Context, ownerID, filePath string) error {
	ownerTGID, err := getOwnerTGID(ownerID)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца: %v", err)
	}
	return sendTelegramPhoto(ctx, ownerTGID, filePath)
}
