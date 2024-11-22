package main

import (
	"fmt"
	"log"
	"strings"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// для обработки команды /status
func handleStatusCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) error {
	tgUserID := int(update.Message.From.ID)
	tgChatID := update.Message.Chat.ID

	userData, err := getUserInfo(tgUserID)
	if err != nil {
		return fmt.Errorf("Ошибка при получении данных о пользователе: %v", err)
	}

	response := fmt.Sprintf(
		"📊 Статус пользователя:\n"+
			"👤 Имя пользователя: %s\n"+
			"🔑 Telegram ID: %d\n"+
			"💰 Монеты: %d\n"+
			"🌀 Кружков создано: %d\n"+
			"💼 Замены лиц: %d\n\n",
		userData["username"],
		tgUserID,
		int(userData["coins"].(float64)),
		int(userData["circle_count"].(float64)),
		int(userData["face_replace_count"].(float64)),
	)

	activeJobs, err := getActiveJobs(userData["id"].(string), "face_jobs")
	if err != nil {
		return fmt.Errorf("Ошибка при получении активных задач: %v", err)
	}
	if len(activeJobs) > 0 {
		response += "📋 Активные задачи замены лиц:\n"
		for _, job := range activeJobs {
			response += fmt.Sprintf(
				"🔹 Задача ID: %s\n"+
					"   Статус: %s\n"+
					"   Время: %s\n"+
					"   Обновлена: %s\n\n",
				job["id"],
				job["status"],
				job["created"],
				job["updated"],
			)
		}
	} else {
		response += "У вас нет активных задач замены лиц.\n"
	}

	activeJobs, err = getActiveJobs(userData["id"].(string), "circle_jobs")
	if err != nil {
		return fmt.Errorf("Ошибка при получении активных задач: %v", err)
	}
	if len(activeJobs) > 0 {
		response += "📋 Активные задачи создания кружков:\n"
		for _, job := range activeJobs {
			response += fmt.Sprintf(
				"🔹 Задача ID: %s\n"+
					"   Статус: %s\n"+
					"   Время: %s\n"+
					"   Обновлена: %s\n\n",
				job["id"],
				job["status"],
				job["created"],
				job["updated"],
			)
		}
	}

	msg := tgbotapi.NewMessage(tgChatID, response)
	bot.Send(msg)
	return nil
}

type UserSession struct {
	FaceFileID string // временное хранение ID файла фотографии
}

// Функция для получения или создания сессии пользователя
func getUserSession(userID int) *UserSession {
	if session, ok := userSessions[userID]; ok {
		return session
	}
	// Создаем новую сессию, если её еще нет
	userSessions[userID] = &UserSession{}
	return userSessions[userID]
}

// Хранилище сессий пользователей
var userSessions = make(map[int]*UserSession)

func main() {
	// load variables
	BOT_TOKEN, BOT_DEBUG, BOT_ENDPOINT := LoadEnvironment()

	// auth pocketbase
	err := authenticatePocketBase()
	if err != nil {
		panic(err)
	}

	// start the bot
	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint(BOT_TOKEN, BOT_ENDPOINT+`/bot%s/%s`)
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

	// Основной обработчик
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message == nil {
			continue
		}

		userID := update.Message.From.ID
		userName := update.Message.From.UserName

		pbUserID, err := getOrCreateUser(int(userID), userName)
		if err != nil {
			log.Printf("Ошибка при получении/создании пользователя: %v", err)
			continue
		}

		// Получаем сессию для текущего пользователя
		session := getUserSession(int(userID))

		// Приветственное сообщение
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "start") {
			greeting := fmt.Sprintf("Привет, %s! Добро пожаловать! Справка: /help", userName)
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, greeting)
			bot.Send(msg)
			continue
		}

		// help
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "help") {
			helpMessage := "Напиши мне фото для создания задачи по замене лица (временно недоступно). Пришли видео для создания кружочка."
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, helpMessage)
			bot.Send(msg)
			continue
		}

		// status
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "status") {
			err = handleStatusCommand(bot, update)
			if err != nil {
				log.Printf("Не удалось получить статус пользователя: %v", err)
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Произошла ошибка при получении статуса: %v", err))
				bot.Send(msg)
				continue
			}
			continue
		}

		// Обработка получения фотографии
		if update.Message.Photo != nil {
			fileID := update.Message.Photo[len(update.Message.Photo)-1].FileID
			session.FaceFileID = fileID // сохраняем ID фото для текущего пользователя

			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Получена фотография. Пожалуйста, отправьте видео для замены лица.")
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
			videoFileID := update.Message.Video.FileID
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ловлю!")
			bot.Send(msg)

			// Проверяем, есть ли фото в сессии пользователя
			if session.FaceFileID != "" {
				err, jobID := createFaceJob(bot, pbUserID, videoFileID, session.FaceFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на замену лица: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Произошла ошибка при создании задания: %v", err))
					bot.Send(msg)
					continue
				}

				msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Ваше видео поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
				bot.Send(msg)

				// Сбрасываем данные сессии
				session.FaceFileID = ""
				continue
			} else {
				err, jobID := createCircleJob(bot, pbUserID, videoFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на создание кружочка: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Произошла ошибка при создании задания: %v", err))
					bot.Send(msg)
					continue
				}

				msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Ваше видео поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
				bot.Send(msg)

				// Сбрасываем временные данные
				continue
			}
		}

		// Обработка команды отмены
		if update.Message.Text == "Отменить" {
			session.FaceFileID = "" // Сбрасываем временные данные в сессии
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Операция отменена.")
			bot.Send(msg)
			continue
		}
	}
}
