package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
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
		"identity": email,    // Email администратора/пользователя
		"password": password, // Пароль администратора/пользователя
	}

	authDataJson, _ := json.Marshal(authData)

	// Используем правильный URL для аутентификации
	authURL := fmt.Sprintf("%s/api/admins/auth-with-password", pocketBaseUrl) // Используем для администратора

	resp, err := http.Post(authURL, "application/json", bytes.NewBuffer(authDataJson))
	if err != nil {
		return fmt.Errorf("не удалось отправить запрос на авторизацию: %v", err)
	}
	defer resp.Body.Close()

	// Обрабатываем статус ошибки на уровне HTTP
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("авторизация не удалась, код %d, ответ: %s", resp.StatusCode, string(body))
	}

	// Обрабатываем тело ответа и извлекаем JWT токен
	body, _ := io.ReadAll(resp.Body)
	var authResponse map[string]interface{}
	if err := json.Unmarshal(body, &authResponse); err != nil {
		return fmt.Errorf("ошибка разбора ответа: %v, ответ: %s", err, string(body))
	}

	// Извлекаем поле "token" с более надежной проверкой
	token, ok := authResponse["token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("не удалось получить токен из ответа: %s", string(body))
	}

	// Сохраняем токен в глобальную переменную
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// Функция для регистрации нового пользователя или получения существующего
func getOrCreateUser(tgUserID int, tgUsername string) (string, error) {
	// Поиск пользователя по ID в PocketBase
	searchURL := fmt.Sprintf("%s/api/collections/users/records?filter=tgid=%d", pocketBaseUrl, tgUserID)
	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка при отправке запроса на поиск пользователя: %v", err)
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return "", fmt.Errorf("ошибка разбора ответа: %v, ответ: %s", err, string(resp))
	}

	// Проверка наличия records в ответе
	if items, ok := searchResult["items"].([]interface{}); ok && len(items) > 0 {
		if user, ok := items[0].(map[string]interface{}); ok {
			if userID, ok := user["id"]; ok {
				if idStr, ok := userID.(string); ok && idStr != "" {
					// Пользователь найден, возвращаем его ID
					return idStr, nil
				}
			}
		}
	}

	// Если пользователь не найден, создаем нового пользователя
	userData := map[string]interface{}{
		"tgid":               tgUserID,
		"username":           tgUsername,
		"circle_count":       0,
		"face_replace_count": 0,
		"coins":              200,
	}
	userDataJson, _ := json.Marshal(userData)

	createUserURL := fmt.Sprintf("%s/api/collections/users/records", pocketBaseUrl)
	createResp, err := sendAuthorizedRequest("POST", createUserURL, userDataJson)
	if err != nil {
		return "", fmt.Errorf("ошибка при отправке запроса на создание пользователя: %v", err)
	}

	var createdUser map[string]interface{}
	if err := json.Unmarshal(createResp, &createdUser); err != nil {
		return "", fmt.Errorf("ошибка разбора ответа на создание пользователя: %v, ответ: %s", err, string(createResp))
	}

	// Проверка правильности записи созданного пользователя
	if userID, ok := createdUser["id"].(string); ok && userID != "" {
		// Новый пользователь создан, возвращаем его ID
		return userID, nil
	}

	return "", fmt.Errorf("не удалось получить ID созданного пользователя из ответа: %s", string(createResp))
}

// Функция для скачивания файла из Telegram
func downloadTelegramFile(bot *tgbotapi.BotAPI, fileID, destinationPath string) error {
	// Получить информацию о файле
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return fmt.Errorf("не удалось получить файл по ID: %v", err)
	}

	// Сформировать URL для скачивания
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, file.FilePath)

	// Скачиваем файл
	resp, err := http.Get(fileURL)
	if err != nil {
		return fmt.Errorf("не удалось скачать файл: %v", err)
	}
	defer resp.Body.Close()

	// Проверяем успешность ответа
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("не удалось скачать файл, статус: %s", resp.Status)
	}

	// Создаём файл на диске
	out, err := os.Create(destinationPath)
	if err != nil {
		return fmt.Errorf("не удалось создать файл: %v", err)
	}
	defer out.Close()

	// Сохраняем содержимое ответа в файл
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("не удалось сохранить содержимое файла: %v", err)
	}

	return nil
}

// Функция для создания Face Job
func createFaceJob(bot *tgbotapi.BotAPI, userID, inputMediaFileID, inputFaceFileID string) error {
	// Открываем файлы перед отправкой
	// Создаём папку для пользователя
	userDir := fmt.Sprintf("data/%s", userID)
	err := os.MkdirAll(userDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("не удалось создать директорию пользователя: %v", err)
	}

	// Скачиваем видеофайл
	inputMediaPath := fmt.Sprintf("%s/%s.mp4", userDir, inputMediaFileID)
	err = downloadTelegramFile(bot, inputMediaFileID, inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось скачать видеофайл: %v", err)
	}

	// Скачиваем файл лица
	inputFacePath := fmt.Sprintf("%s/%s.jpeg", userDir, inputFaceFileID)
	err = downloadTelegramFile(bot, inputFaceFileID, inputFacePath)
	if err != nil {
		return fmt.Errorf("не удалось скачать файл лица: %v", err)
	}

	inputMediaFile, err := os.Open(inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось открыть видеофайл: %v", err)
	}
	defer inputMediaFile.Close()

	inputFaceFile, err := os.Open(inputFacePath)
	if err != nil {
		return fmt.Errorf("не удалось открыть файл лица: %v", err)
	}
	defer inputFaceFile.Close()

	// Проверяем размер видео
	fileInfo, err := inputMediaFile.Stat()
	if err != nil {
		return fmt.Errorf("не удалось получить информацию о видеофайле: %v", err)
	}
	if fileInfo.Size() > 50*1024*1024 {
		return fmt.Errorf("размер видео превышает 50 МБ")
	}

	// Проверяем размер файла лица
	faceFileInfo, err := inputFaceFile.Stat()
	if err != nil {
		return fmt.Errorf("не удалось получить информацию о файле лица: %v", err)
	}
	if faceFileInfo.Size() > 50*1024*1024 {
		return fmt.Errorf("размер файла лица превышает 50 МБ")
	}

	// Создаем body для multipart/form-data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем метаданные (например, владелец и статус)
	_ = writer.WriteField("owner", userID)
	_ = writer.WriteField("status", "queued") // Статус задачи по умолчанию

	// Добавляем файлы в request
	mediaPart, err := writer.CreateFormFile("input_media", fileInfo.Name())
	if err != nil {
		return fmt.Errorf("не удалось создать часть для видеофайла: %v", err)
	}
	_, err = io.Copy(mediaPart, inputMediaFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить видеофайл: %v", err)
	}

	facePart, err := writer.CreateFormFile("input_face", faceFileInfo.Name())
	if err != nil {
		return fmt.Errorf("не удалось создать часть для файла лица: %v", err)
	}
	_, err = io.Copy(facePart, inputFaceFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить файл лица: %v", err)
	}

	// Закрываем writer, чтобы завершить формирование multipart
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("не удалось завершить формирование multipart: %v", err)
	}

	// Формируем запрос
	createJobURL := fmt.Sprintf("%s/api/collections/face_jobs/records", pocketBaseUrl)
	req, err := http.NewRequest("POST", createJobURL, body)
	if err != nil {
		return fmt.Errorf("не удалось создать HTTP-запрос для создания задачи: %v", err)
	}

	// Устанавливаем заголовки
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken)) // Добавляем токен авторизации

	// Выполняем запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса на создание face job: %v", err)
	}
	defer resp.Body.Close()

	// Обрабатываем статус ответа
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ошибка создания face job, код: %d, ответ: %s", resp.StatusCode, string(respBody))
	}

	fmt.Println("Задача Face Job успешно создана.")
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
				err := createFaceJob(bot, pbUserID, videoFileID, tempFaceFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на замену лица: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Произошла ошибка при создании задания: %v", err))
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
