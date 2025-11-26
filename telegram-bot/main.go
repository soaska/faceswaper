package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Version info set at build time
var (
	GitCommit  = "unknown"
	GitMessage = "unknown"
)

// Handle /compress command - compress JPG to 12% quality (FREE for users with positive balance)
func handleCompressImage(bot *tgbotapi.BotAPI, chatID int64, userID string, fileID string) {
	// Check user has positive balance (feature is free but requires account with coins)
	userData, err := getUserInfo(int(chatID))
	if err != nil {
		log.Printf("Ошибка получения данных пользователя: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка при проверке баланса.")
		bot.Send(msg)
		return
	}

	coins := int(userData["coins"].(float64))
	if coins <= 0 {
		msg := tgbotapi.NewMessage(chatID, "Функция сжатия доступна только пользователям с положительным балансом.")
		bot.Send(msg)
		return
	}

	// Get file path from Telegram
	inputPath, err := getTelegramFile(bot, fileID)
	if err != nil {
		log.Printf("Ошибка получения файла: %v", err)
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка получения изображения: %v", err))
		bot.Send(msg)
		return
	}

	// Create cache directory for output
	cacheDir := "cache"
	err = os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		log.Printf("Ошибка создания кэша: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка создания временной директории.")
		bot.Send(msg)
		return
	}

	outputPath := filepath.Join(cacheDir, fmt.Sprintf("compress_output_%s.jpg", fileID))
	defer os.Remove(outputPath)

	// Open and decode image
	imgFile, err := os.Open(inputPath)
	if err != nil {
		log.Printf("Ошибка открытия файла: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка открытия изображения.")
		bot.Send(msg)
		return
	}
	defer imgFile.Close()

	img, _, err := image.Decode(imgFile)
	if err != nil {
		log.Printf("Ошибка декодирования изображения: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка чтения изображения. Убедитесь, что это JPG файл.")
		bot.Send(msg)
		return
	}

	// Create output file
	outputFile, err := os.Create(outputPath)
	if err != nil {
		log.Printf("Ошибка создания выходного файла: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка создания сжатого файла.")
		bot.Send(msg)
		return
	}
	defer outputFile.Close()

	// Encode with 12% quality
	options := &jpeg.Options{Quality: 12}
	err = jpeg.Encode(outputFile, img, options)
	if err != nil {
		log.Printf("Ошибка кодирования изображения: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка сжатия изображения.")
		bot.Send(msg)
		return
	}

	// Send compressed image back
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(outputPath))
	photo.Caption = "Изображение сжато до 12% качества."
	_, err = bot.Send(photo)
	if err != nil {
		log.Printf("Ошибка отправки сжатого изображения: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка отправки сжатого изображения.")
		bot.Send(msg)
		return
	}

	log.Printf("Изображение успешно сжато для пользователя %s", userID)
}

// Handle /status command
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
			// Skip tasks with "err cleared" status
			if status, ok := job["status"].(string); ok && strings.Contains(status, "err cleared") {
				continue
			}

			// Build response text, including duration and price if available
			responseText := fmt.Sprintf(
				"🔹 Задача ID: %s\n"+
					"   Статус: %s\n"+
					"   Время: %s\n"+
					"   Обновлена: %s\n",
				job["id"],
				job["status"],
				job["created"],
				job["updated"],
			)

			// Add duration and price if available (for completed jobs)
			if duration, ok := job["duration"].(float64); ok && duration > 0 {
				responseText += fmt.Sprintf("   Длительность: %d сек\n", int(duration))
			}
			if price, ok := job["price"].(float64); ok && price > 0 {
				responseText += fmt.Sprintf("   Цена: %d монет\n", int(price))
			}

			response += responseText + "\n"
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
	FaceFileID         string // временное хранение ID файла фотографии
	WaitingForCompress bool   // ожидание фото для сжатия
}

// Function to get or create user session
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
			log.Printf("Ошибка авторизации PocketBase (попытка %d/5): %v", retries+1, err)
			time.Sleep(time.Duration(retries+1) * 5 * time.Second)
			continue
		}

		// start the bot
		bot, err = tgbotapi.NewBotAPIWithAPIEndpoint(BOT_TOKEN, BOT_ENDPOINT+`/bot%s/%s`)
		if err != nil {
			log.Printf("Ошибка инициализации бота (попытка %d/5): %v", retries+1, err)
			time.Sleep(time.Duration(retries+1) * 5 * time.Second)
			continue
		}
		log.Printf("Authorized on account %s", bot.Self.UserName)
		return bot, nil
	}
	return nil, fmt.Errorf("не удалось инициализировать после 5 попыток: %v", err)
}

func main() {
	// Log version information
	commitShort := GitCommit
	if len(GitCommit) > 8 {
		commitShort = GitCommit[:8]
	}
	log.Printf("🤖 Telegram Bot started")
	log.Printf("📦 Version: %s - %s", commitShort, GitMessage)

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

				// Get session for current user
				session := getUserSession(int(userID))

				// Приветственное сообщение
				if update.Message.Text != "" && strings.Contains(strings.ToLower(update.Message.Text), "start") {
					greeting := fmt.Sprintf(
						"👋 Привет, %s! Добро пожаловать в бот для создания кружков и замены лиц!\n\n"+
							"🎯 Что я умею:\n"+
							"• Создавать кружки из видео (1 монета)\n"+
							"• Сжимать фото по команде /compress (бесплатно)\n"+
							"• Заменять лица на фото (1 монета)\n"+
							"• Заменять лица на видео (2+ монет)\n\n"+
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
						"🎥 Создание кружка (1 монета):\n" +
						"• Отправьте видео\n" +
						"• Дождитесь обработки\n\n" +
						"👤 Замена лица на фото (1 монета):\n" +
						"• Отправьте фото лица\n" +
						"• Отправьте второе фото\n" +
						"• Дождитесь обработки\n\n" +
						"🎬 Замена лица на видео (2+ монет):\n" +
						"• Отправьте фото лица\n" +
						"• Отправьте видео\n" +
						"• Дождитесь обработки\n\n" +
						"🗜 /compress - сжать JPG изображение до 12% качества (бесплатно)\n\n" +
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

				// compress command
				if update.Message.Text != "" && strings.HasPrefix(strings.ToLower(update.Message.Text), "/compress") {
					session.WaitingForCompress = true
					msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Отправьте JPG изображение для сжатия до 12% качества.")
					cancelMarkup := tgbotapi.NewReplyKeyboard(
						tgbotapi.NewKeyboardButtonRow(
							tgbotapi.NewKeyboardButton("Отменить"),
						),
					)
					msg.ReplyMarkup = cancelMarkup
					bot.Send(msg)
					continue
				}

				// Обработка получения фотографии
				if update.Message.Photo != nil {
					fileID := update.Message.Photo[len(update.Message.Photo)-1].FileID

					// Проверяем режим ожидания сжатия
					if session.WaitingForCompress {
						msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Обрабатываю...")
						bot.Send(msg)

						// Запускаем обработку в горутине
						go handleCompressImage(bot, update.Message.Chat.ID, pbUserID, fileID)

						// Сбрасываем режим ожидания
						session.WaitingForCompress = false
						continue
					}

					// Проверяем, есть ли уже сохраненное фото в сессии
					if session.FaceFileID != "" {
						// Второе фото получено - создаем задачу замены лица на фото
						msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Ловлю!")
						bot.Send(msg)

						jobID, err := createFaceJob(bot, pbUserID, fileID, session.FaceFileID)
						if err != nil {
							log.Printf("Не удалось создать задание на замену лица: %v", err)
							msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Произошла ошибка при создании задания. Если ситуация повторяется, обратитесь в поддержку.")
							bot.Send(msg)
							continue
						}

						msg = tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Ваше фото поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
						bot.Send(msg)

						// Сбрасываем данные сессии
						session.FaceFileID = ""
						continue
					} else {
						// Первое фото получено - сохраняем и просим второе фото или видео
						session.FaceFileID = fileID

						msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Получена фотография. Пожалуйста, отправьте видео или второе фото для замены лица.")
						cancelMarkup := tgbotapi.NewReplyKeyboard(
							tgbotapi.NewKeyboardButtonRow(
								tgbotapi.NewKeyboardButton("Отменить"),
							),
						)
						msg.ReplyMarkup = cancelMarkup
						bot.Send(msg)
						continue
					}
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
					session.FaceFileID = ""            // Сбрасываем временные данные в сессии
					session.WaitingForCompress = false // Сбрасываем режим сжатия
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
			log.Printf("Ошибка перезапуска бота: %v", err)
			time.Sleep(60 * time.Second)
		}
	}
}
