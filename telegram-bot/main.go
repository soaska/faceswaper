package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// для обработки команды /status
func handleStatusCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) error {
	tgUserID := int(update.Message.From.ID)
	tgChatID := update.Message.Chat.ID

	userData, err := getUserInfo(tgUserID)
	if err != nil {
		return fmt.Errorf("ошибка при получении данных о пользователе: %v", err)
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

	// Получаем активные задачи создания кружков
	activeCircleJobs, err := getActiveJobs(userData["id"].(string), "circle_jobs")
	if err != nil {
		return fmt.Errorf("ошибка при получении активных задач создания кружков: %v", err)
	}
	if len(activeCircleJobs) > 0 {
		response += "📋 Активные задачи создания кружков:\n"
		for _, job := range activeCircleJobs {
			// Пропускаем задачи со статусом "err cleared"
			if status, ok := job["status"].(string); ok && strings.Contains(status, "err cleared") {
				continue
			}
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
		response += "У вас нет активных задач создания кружков.\n\n"
	}

	// Получаем активные задачи замены лиц
	activeFaceJobs, err := getActiveJobs(userData["id"].(string), "face_jobs")
	if err != nil {
		return fmt.Errorf("ошибка при получении активных задач замены лиц: %v", err)
	}
	if len(activeFaceJobs) > 0 {
		response += "📋 Активные задачи замены лиц:\n"
		for _, job := range activeFaceJobs {
			// Пропускаем задачи со статусом "err cleared"
			if status, ok := job["status"].(string); ok && strings.Contains(status, "err cleared") {
				continue
			}
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

	// Создаем сообщение с кнопкой
	msg := tgbotapi.NewMessage(tgChatID, response)

	// Добавляем кнопку сброса ошибок
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Сбросить ошибки", "reset_errors"),
		),
	)
	msg.ReplyMarkup = keyboard

	bot.Send(msg)
	return nil
}

// Обработка нажатия на кнопку сброса ошибок
func handleResetErrors(bot *tgbotapi.BotAPI, update tgbotapi.Update) error {
	tgUserID := int(update.CallbackQuery.From.ID)
	tgChatID := update.CallbackQuery.Message.Chat.ID

	userData, err := getUserInfo(tgUserID)
	if err != nil {
		return fmt.Errorf("ошибка при получении данных о пользователе: %v", err)
	}

	// Получаем все задачи пользователя
	faceJobs, err := getActiveJobs(userData["id"].(string), "face_jobs")
	if err != nil {
		return fmt.Errorf("ошибка при получении задач замены лиц: %v", err)
	}

	circleJobs, err := getActiveJobs(userData["id"].(string), "circle_jobs")
	if err != nil {
		return fmt.Errorf("ошибка при получении задач создания кружков: %v", err)
	}

	// Сбрасываем статусы ошибок
	for _, job := range faceJobs {
		if status, ok := job["status"].(string); ok && strings.HasPrefix(status, "error") {
			newStatus := fmt.Sprintf("err cleared: %s", strings.TrimPrefix(status, "error"))
			err = updateStatus("face_jobs", job["id"].(string), newStatus)
			if err != nil {
				log.Printf("Ошибка обновления статуса задачи %s: %v", job["id"], err)
			}
		}
	}

	for _, job := range circleJobs {
		if status, ok := job["status"].(string); ok && strings.HasPrefix(status, "error:") {
			newStatus := fmt.Sprintf("err cleared: %s", strings.TrimPrefix(status, "error:"))
			err = updateStatus("circle_jobs", job["id"].(string), newStatus)
			if err != nil {
				log.Printf("Ошибка обновления статуса задачи %s: %v", job["id"], err)
			}
		}
	}

	// Отправляем подтверждение
	msg := tgbotapi.NewMessage(tgChatID, "Статусы ошибок сброшены. Используйте /status для проверки.")
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

func initializeBot(BOT_TOKEN, BOT_ENDPOINT string) (*tgbotapi.BotAPI, error) {
	var bot *tgbotapi.BotAPI
	var err error
	for retries := 0; retries < 5; retries++ {
		// auth pocketbase
		err = authenticatePocketBase()
		if err != nil {
			log.Printf("PocketBase auth failed (attempt %d/5): %v", retries+1, err)
			time.Sleep(time.Duration(retries+1) * 5 * time.Second)
			continue
		}

		// start the bot
		bot, err = tgbotapi.NewBotAPIWithAPIEndpoint(BOT_TOKEN, BOT_ENDPOINT+`/bot%s/%s`)
		if err != nil {
			log.Printf("Bot init failed (attempt %d/5): %v", retries+1, err)
			time.Sleep(time.Duration(retries+1) * 5 * time.Second)
			continue
		}
		log.Printf("Authorized on account %s", bot.Self.UserName)
		return bot, nil
	}
	return nil, fmt.Errorf("failed to initialize after 5 attempts: %v", err)
}

func main() {
	// load variables
	BOT_TOKEN, BOT_DEBUG, BOT_ENDPOINT := LoadEnvironment()

	// Initialize bot
	bot, err := initializeBot(BOT_TOKEN, BOT_ENDPOINT)
	if err != nil {
		log.Fatalf("Bot initialization failed: %v", err)
	}

	if BOT_DEBUG {
		bot.Debug = true
		log.Print("bot in DEBUG mode")
	}

	// Clean up any leftover user sessions on restart
	userSessions = make(map[int]*UserSession)
	log.Println("User sessions cleared on restart")

	// updates on telegram API with recovery
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Bot panic recovered: %v", r)
				}
			}()

			u := tgbotapi.NewUpdate(0)
			u.Timeout = 60

			// Основной обработчик
			updates := bot.GetUpdatesChan(u)
			for update := range updates {
		// Обработка нажатия на кнопку
		if update.CallbackQuery != nil {
			if update.CallbackQuery.Data == "reset_errors" {
				err = handleResetErrors(bot, update)
				if err != nil {
					log.Printf("Ошибка при сбросе ошибок: %v", err)
					msg := tgbotapi.NewMessage(update.CallbackQuery.Message.Chat.ID, "Произошла ошибка при сбросе статусов. Попробуйте позже.")
					bot.Send(msg)
				}
				continue
			}
		}

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
			greeting := fmt.Sprintf(
				"👋 Привет, %s! Добро пожаловать в бот для создания кружков и замены лиц!\n\n"+
					"🎯 Что я умею:\n"+
					"• Создавать кружки из видео\n"+
					"• Заменять лица на видео (временно недоступно)\n\n"+
					"📚 Подробнее о командах: /help\n"+
					"📊 Проверить статус: /status\n"+
					"📢 Новости: https://t.me/+HGQVwMhFzIExZDNi",
				userName)
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, greeting)
			bot.Send(msg)
			continue
		}

		// help
		if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "help") {
			helpMessage := "📚 Список доступных команд:\n\n" +
				"🎥 Создание кружка:\n" +
				"• Отправьте видео\n" +
				"• Дождитесь обработки\n\n" +
				"👤 Замена лица (временно недоступно):\n" +
				"• Отправьте фото лица\n" +
				"• Отправьте видео\n" +
				"• Дождитесь обработки\n\n" +
				"📊 /status - проверить статус и баланс\n" +
				"❓ /help - показать это сообщение\n\n" +
				"📢 Новости и обновления: https://t.me/+HGQVwMhFzIExZDNi"
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
				jobID, err := createFaceJob(bot, pbUserID, videoFileID, session.FaceFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на замену лица: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Произошла ошибка при создании задания. Если ситуация повторяется, обратитесь в поддержку.")
					bot.Send(msg)
					continue
				}

				msg := tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Ваше видео поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
				bot.Send(msg)

				// Сбрасываем данные сессии
				session.FaceFileID = ""
				continue
			} else {
				jobID, err := createCircleJob(bot, pbUserID, videoFileID)
				if err != nil {
					log.Printf("Не удалось создать задание на создание кружочка: %v", err)
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Произошла ошибка при создании задания. Если ситуация повторяется, обратитесь в поддержку.")
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
		}()
		log.Println("Bot connection lost, attempting restart...")
		time.Sleep(30 * time.Second)
		
		// Reinitialize bot
		bot, err = initializeBot(BOT_TOKEN, BOT_ENDPOINT)
		if err != nil {
			log.Printf("Bot restart failed: %v", err)
			time.Sleep(60 * time.Second)
		}
	}
}
