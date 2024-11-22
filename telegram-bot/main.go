package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

// PocketBase global credentials
var pocketBaseUrl string
var email string
var password string
var authToken string

// getiing JWT for pocketbase
func authenticatePocketBase() error {
	authData := map[string]string{
		"identity": email,
		"password": password,
	}

	authDataJson, _ := json.Marshal(authData)
	authURL := fmt.Sprintf("%s/api/admins/auth-with-password", pocketBaseUrl)

	resp, err := http.Post(authURL, "application/json", bytes.NewBuffer(authDataJson))
	if err != nil {
		return fmt.Errorf("не удалось отправить запрос на авторизацию: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("авторизация не удалась, код %d", resp.StatusCode)
	}

	// Getting jwt
	body, _ := ioutil.ReadAll(resp.Body)
	var authResponse map[string]interface{}
	json.Unmarshal(body, &authResponse)

	token, ok := authResponse["token"].(string)
	if !ok {
		return fmt.Errorf("не удалось получить токен из ответа: %s", body)
	}

	// global jwt
	authToken = token

	log.Println("PocketBase: Авторизация прошла успешно. Получен токен от PocketBase.")
	return nil
}

// Функция для отправки HTTP-запросов с заголовком Authorization
func sendAuthorizedRequest(method, url string, payload []byte) ([]byte, error) {
	client := &http.Client{}
	req, err := http.NewRequest(method, url, bytes.NewBuffer(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Добавляем Bearer токен в заголовок
	if authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// Функция для регистрации нового пользователя или получения существующего
func getOrCreateUser(tgUserID int, tgUsername string) (string, error) {
	// Поиск пользователя по ID в PocketBase
	searchURL := fmt.Sprintf("%s/api/collections/users/records?filter=id=%s", pocketBaseUrl, strconv.Itoa(tgUserID))
	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return "", err
	}

	var searchResult map[string]interface{}
	json.Unmarshal(resp, &searchResult)

	if searchResult["code"] == nil {
		records := searchResult["items"].([]interface{})
		if len(records) > 0 {
			userId := records[0].(map[string]interface{})["id"].(string)
			return userId, nil
		}
	}

	// Если не найден — нужно создать
	userData := map[string]interface{}{
		"id":                 tgUserID,
		"username":           tgUsername,
		"circle_count":       0,
		"face_replace_count": 0,
		"coins":              200,
	}
	userDataJson, _ := json.Marshal(userData)

	createUserURL := fmt.Sprintf("%s/api/collections/users/records", pocketBaseUrl)
	createResp, err := sendAuthorizedRequest("POST", createUserURL, userDataJson)
	if err != nil {
		return "", err
	}

	var createdUser map[string]interface{}
	json.Unmarshal(createResp, &createdUser)

	return createdUser["id"].(string), nil
}

// Function for creating face_swap job
func createFaceJob(userID, mediaFileID, faceFileID string) error {
	jobData := map[string]interface{}{
		"owner":       userID,
		"input_media": mediaFileID,
		"input_face":  faceFileID,
		"status":      "queued",
	}

	jobDataJson, _ := json.Marshal(jobData)
	createJobURL := fmt.Sprintf("%s/api/collections/face_jobs/records", pocketBaseUrl)

	_, err := sendAuthorizedRequest("POST", createJobURL, jobDataJson)
	if err != nil {
		return err
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

func main() {
	// load variables
	BOT_TOKEN, BOT_DEBUG := LoadEnvironment()

	// auth pocketbase
	err := authenticatePocketBase()
	if err != nil {
		panic(err)
	}

	// start the bot
	bot, err := tgbotapi.NewBotAPI(BOT_TOKEN)
	if err != nil {
		panic(err)
	} else {
		log.Printf("Authorized on account %s", bot.Self.UserName)
	}
	if BOT_DEBUG {
		bot.Debug = true
		log.Print("bot in DEBUG mode")
	}

	// updates on telegram API
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	var tempFaceFileID string // media tmp

	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message == nil {
			continue
		}

		userID := update.Message.From.ID
		userName := update.Message.From.UserName

		// Получение или создание пользователя
		pbUserID, err := getOrCreateUser(int(userID), userName)
		if err != nil {
			log.Printf("Ошибка при получении/создании пользователя: %v", err)
			continue
		}

		// Приветственное сообщение
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "start") {
			greeting := fmt.Sprintf("Привет, %s! Добро пожаловать!", userName)
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, greeting)
			bot.Send(msg)
			continue
		}

		// Обработка текстовых команд
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "help") {
			helpMessage := "Напиши мне видео или фото для выполнения задачи по замене лица."
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, helpMessage)
			bot.Send(msg)
			continue
		}

		// Обработка получения фотографии
		// Обработка получения фотографии
		if update.Message.Photo != nil {
			// Получаем последний элемент массива Photo, так как это самое большое фото
			fileID := update.Message.Photo[len(update.Message.Photo)-1].FileID
			tempFaceFileID = fileID // сохраняем ID для будущего использования с видео

			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Получена фотография. Пожалуйста, отправьте видео для замены лица.")
			// Кнопка отмены
			cancelMarkup := tgbotapi.NewReplyKeyboard(
				tgbotapi.NewKeyboardButtonRow(
					tgbotapi.NewKeyboardButton("Отменить"),
				),
			)
			msg.ReplyMarkup = cancelMarkup
			bot.Send(msg)
			continue
		}

		// Обработка получения видео
		if update.Message.Video != nil {
			// Получаем видеофайл
			videoFileID := update.Message.Video.FileID

			// Если фото уже было отправлено до этого
			if tempFaceFileID != "" {
				err := createFaceJob(pbUserID, videoFileID, tempFaceFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на замену лица: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Произошла ошибка при создании задания.")
					bot.Send(msg)
					continue
				}

				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ваше видео поставлено в очередь для обработки. Статус: В очереди.")
				bot.Send(msg)

				// Сбрасываем временные переменные после создания задания
				tempFaceFileID = ""
				continue
			} else {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Видео получено, пожалуйста, отправьте фото для замены лица.")
				bot.Send(msg)
			}
			continue
		}

		// Отмена операции
		if update.Message.Text == "Отменить" {
			tempFaceFileID = "" // Обнуляем данные
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Операция отменена.")
			bot.Send(msg)
			continue
		}
	}
}
