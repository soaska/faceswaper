package main

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

var (
	GitCommit  = "unknown"
	GitMessage = "unknown"
)

var (
	userSessions    = newSessionStore(sessionTTL)
	compressionSlot = make(chan struct{}, 2)
)

func handleCompressImage(bot *tgbotapi.BotAPI, chatID, tgUserID int64, userID, fileID string, quality int) {
	user, err := getUserInfo(tgUserID)
	if err != nil {
		log.Printf("Ошибка проверки баланса пользователя %d: %v", chatID, err)
		sendText(bot, chatID, "Ошибка при проверке баланса.")
		return
	}
	if user.Coins <= 0 {
		sendText(bot, chatID, "Функция сжатия доступна только пользователям с положительным балансом.")
		return
	}

	inputPath, err := getTelegramFile(bot, fileID)
	if err != nil {
		log.Printf("Ошибка получения изображения: %v", err)
		sendText(bot, chatID, fmt.Sprintf("Ошибка получения изображения: %v", err))
		return
	}
	defer removeDownloadedTelegramFile(inputPath)

	if err := os.MkdirAll(botCacheDirectory(), 0o755); err != nil {
		log.Printf("Ошибка создания временной директории: %v", err)
		sendText(bot, chatID, "Ошибка создания временной директории.")
		return
	}
	outputFile, err := os.CreateTemp(botCacheDirectory(), "compress-*.jpg")
	if err != nil {
		log.Printf("Ошибка создания сжатого файла: %v", err)
		sendText(bot, chatID, "Ошибка создания сжатого файла.")
		return
	}
	outputPath := outputFile.Name()
	defer os.Remove(outputPath)

	inputFile, err := os.Open(inputPath)
	if err != nil {
		_ = outputFile.Close()
		log.Printf("Ошибка открытия изображения: %v", err)
		sendText(bot, chatID, "Ошибка открытия изображения.")
		return
	}
	img, _, decodeErr := image.Decode(inputFile)
	closeErr := inputFile.Close()
	if decodeErr != nil {
		_ = outputFile.Close()
		log.Printf("Ошибка декодирования изображения: %v", decodeErr)
		sendText(bot, chatID, "Ошибка чтения изображения. Убедитесь, что это JPG или PNG файл.")
		return
	}
	if closeErr != nil {
		_ = outputFile.Close()
		log.Printf("Ошибка закрытия изображения: %v", closeErr)
		return
	}

	if err := jpeg.Encode(outputFile, img, &jpeg.Options{Quality: quality}); err != nil {
		_ = outputFile.Close()
		log.Printf("Ошибка сжатия изображения: %v", err)
		sendText(bot, chatID, "Ошибка сжатия изображения.")
		return
	}
	if err := outputFile.Close(); err != nil {
		log.Printf("Ошибка закрытия сжатого файла: %v", err)
		sendText(bot, chatID, "Ошибка подготовки сжатого изображения.")
		return
	}

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(outputPath))
	photo.Caption = fmt.Sprintf("Изображение сжато до %d%% качества.", quality)
	if _, err := bot.Send(photo); err != nil {
		log.Printf("Ошибка отправки сжатого изображения: %v", err)
		sendText(bot, chatID, "Ошибка отправки сжатого изображения.")
		return
	}
	log.Printf("Изображение успешно сжато для пользователя %s", userID)
}

func handleStatusCommand(bot *tgbotapi.BotAPI, message *tgbotapi.Message) error {
	user, err := getUserInfo(message.From.ID)
	if err != nil {
		return fmt.Errorf("ошибка получения пользователя: %v", err)
	}
	circleJobs, err := getActiveJobs(user.ID, "circle_jobs")
	if err != nil {
		return fmt.Errorf("ошибка получения задач создания кружков: %v", err)
	}
	faceJobs, err := getActiveJobs(user.ID, "face_jobs")
	if err != nil {
		return fmt.Errorf("ошибка получения задач замены лиц: %v", err)
	}

	chunks := splitTelegramMessage(formatStatus(user, circleJobs, faceJobs), 3500)
	for index, chunk := range chunks {
		msg := tgbotapi.NewMessage(message.Chat.ID, chunk)
		if index == len(chunks)-1 {
			msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("Сбросить ошибки", "reset_errors"),
				),
			)
		}
		if _, err := bot.Send(msg); err != nil {
			return fmt.Errorf("ошибка отправки статуса: %v", err)
		}
	}
	return nil
}

func formatStatus(user UserRecord, circleJobs, faceJobs []JobRecord) string {
	var response strings.Builder
	fmt.Fprintf(
		&response,
		"📊 Статус пользователя:\n👤 Имя пользователя: %s\n🔑 Telegram ID: %d\n💰 Монеты: %d\n🌀 Кружков создано: %d\n💼 Замены лиц: %d\n\n",
		user.Username,
		user.TGID,
		user.Coins,
		user.CircleCount,
		user.FaceReplaceCount,
	)
	appendJobs(&response, "📋 Активные задачи создания кружков:", "У вас нет активных задач создания кружков.", circleJobs)
	response.WriteString("\n")
	appendJobs(&response, "📋 Активные задачи замены лиц:", "У вас нет активных задач замены лиц.", faceJobs)
	return response.String()
}

func appendJobs(response *strings.Builder, title, emptyMessage string, jobs []JobRecord) {
	visible := make([]JobRecord, 0, len(jobs))
	for _, job := range jobs {
		if !strings.HasPrefix(job.Status, "err cleared:") {
			visible = append(visible, job)
		}
	}
	if len(visible) == 0 {
		response.WriteString(emptyMessage + "\n")
		return
	}
	response.WriteString(title + "\n")
	for _, job := range visible {
		fmt.Fprintf(
			response,
			"🔹 Задача ID: %s\n   Статус: %s\n   Время: %s\n   Обновлена: %s\n",
			job.ID,
			job.Status,
			job.Created,
			job.Updated,
		)
		if job.Duration > 0 {
			fmt.Fprintf(response, "   Длительность: %d сек\n", job.Duration)
		}
		if job.Price > 0 {
			fmt.Fprintf(response, "   Цена: %d монет\n", job.Price)
		}
		response.WriteString("\n")
	}
}

func splitTelegramMessage(message string, limit int) []string {
	if utf8.RuneCountInString(message) <= limit {
		return []string{message}
	}
	var chunks []string
	var current strings.Builder
	for _, line := range strings.SplitAfter(message, "\n") {
		if utf8.RuneCountInString(current.String())+utf8.RuneCountInString(line) > limit && current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		for utf8.RuneCountInString(line) > limit {
			runes := []rune(line)
			chunks = append(chunks, string(runes[:limit]))
			line = string(runes[limit:])
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

func handleResetErrors(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) error {
	user, err := getUserInfo(query.From.ID)
	if err != nil {
		return fmt.Errorf("ошибка получения пользователя: %v", err)
	}
	for _, collection := range []string{"face_jobs", "circle_jobs"} {
		jobs, err := getActiveJobs(user.ID, collection)
		if err != nil {
			return fmt.Errorf("ошибка получения задач из %s: %v", collection, err)
		}
		for _, job := range jobs {
			if !strings.HasPrefix(job.Status, "error:") {
				continue
			}
			status := "err cleared: " + strings.TrimSpace(strings.TrimPrefix(job.Status, "error:"))
			if err := updateStatus(collection, job.ID, status); err != nil {
				log.Printf("Ошибка сброса статуса задачи %s: %v", job.ID, err)
			}
		}
	}
	if query.Message != nil {
		sendText(bot, query.Message.Chat.ID, "Статусы ошибок сброшены. Используйте /status для проверки.")
	}
	return nil
}

func parseCommand(text string) (string, []string, bool) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return "", nil, false
	}
	command := strings.TrimPrefix(strings.ToLower(parts[0]), "/")
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	if command == "" {
		return "", nil, false
	}
	return command, parts[1:], true
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		handleCallback(bot, update.CallbackQuery)
		return
	}
	message := update.Message
	if message == nil || message.From == nil {
		return
	}

	userID := message.From.ID
	username := message.From.UserName
	pbUserID, err := getOrCreateUser(userID, username)
	if err != nil {
		log.Printf("Ошибка получения пользователя %d: %v", userID, err)
		sendText(bot, message.Chat.ID, "Не удалось загрузить профиль. Попробуйте позже.")
		return
	}

	if command, args, ok := parseCommand(message.Text); ok {
		switch command {
		case "start":
			handleStart(bot, message)
		case "help":
			handleHelp(bot, message.Chat.ID)
		case "status":
			if err := handleStatusCommand(bot, message); err != nil {
				log.Printf("Не удалось получить статус пользователя: %v", err)
				sendText(bot, message.Chat.ID, "Произошла ошибка при получении статуса. Попробуйте позже.")
			}
		case "compress":
			handleCompressCommand(bot, message, args)
		case "cancel":
			cancelSession(bot, message.Chat.ID, userID, pbUserID)
		}
		return
	}
	if strings.EqualFold(strings.TrimSpace(message.Text), "Отменить") {
		cancelSession(bot, message.Chat.ID, userID, pbUserID)
		return
	}

	requestKey := fmt.Sprintf("telegram:%d", update.UpdateID)
	if (len(message.Photo) > 0 || message.Video != nil) && !userSessions.Get(userID).WaitingForCompress {
		if existingID := findExistingJobByRequestKey(requestKey); existingID != "" {
			userSessions.Reset(userID)
			sendText(bot, message.Chat.ID, fmt.Sprintf("Эта задача уже поставлена в очередь. ID: %s.", existingID))
			return
		}
	}

	session := userSessions.Get(userID)
	if session.FaceFileID == "" && !session.WaitingForCompress {
		pendingFace, err := getPendingFace(pbUserID)
		if err != nil {
			log.Printf("Не удалось восстановить сессию пользователя %d: %v", userID, err)
		} else if pendingFace != "" {
			userSessions.SetFace(userID, pendingFace)
			session = userSessions.Get(userID)
		}
	}
	if len(message.Photo) > 0 {
		handlePhoto(bot, message, pbUserID, session, requestKey)
		return
	}
	if message.Video != nil {
		handleVideo(bot, message, pbUserID, session, requestKey)
	}
}

func handleCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	if _, err := bot.Request(tgbotapi.NewCallback(query.ID, "")); err != nil {
		log.Printf("Не удалось подтвердить callback: %v", err)
	}
	if query.Data != "reset_errors" {
		return
	}
	if err := handleResetErrors(bot, query); err != nil {
		log.Printf("Ошибка при сбросе ошибок: %v", err)
		if query.Message != nil {
			sendText(bot, query.Message.Chat.ID, "Произошла ошибка при сбросе статусов. Попробуйте позже.")
		}
	}
}

func handleStart(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	name := message.From.UserName
	if name == "" {
		name = message.From.FirstName
	}
	sendText(bot, message.Chat.ID, startMessage(name))
}

func startMessage(name string) string {
	return fmt.Sprintf(
		"👋 Привет, %s! Добро пожаловать в бот для создания кружков и замены лиц!\n\n"+
			"🎯 Что я умею:\n"+
			"• Создавать кружки из видео (1 монета)\n"+
			"• Сжимать фото по команде /compress (бесплатно)\n"+
			"• Заменять лица на фото (1 монета)\n"+
			"• Заменять лица на видео (2+ монет)\n\n"+
			"📚 Подробнее о командах: /help\n"+
			"📊 Проверить статус: /status\n"+
			"📢 Новости: https://t.me/+HGQVwMhFzIExZDNi",
		name,
	)
}

func handleHelp(bot *tgbotapi.BotAPI, chatID int64) {
	sendText(bot, chatID, helpMessage())
}

func helpMessage() string {
	return "📚 Список доступных команд:\n\n" +
		"🎥 Создание кружка (1 монета):\n• Отправьте видео\n• Дождитесь обработки\n\n" +
		"👤 Замена лица на фото (1 монета):\n• Отправьте фото лица\n• Отправьте второе фото\n• Дождитесь обработки\n\n" +
		"🎬 Замена лица на видео (2+ монет):\n• Отправьте фото лица\n• Отправьте видео\n• Дождитесь обработки\n\n" +
		"🗜 /compress [качество] — сжать фото в JPEG (бесплатно):\n" +
		"• Введите команду с нужным качеством\n• Отправляйте фото для сжатия (можно несколько)\n" +
		"• Нажмите «Отменить» или /cancel для выхода\n• Примеры: /compress или /compress 80\n" +
		"• Качество: 1–100 (по умолчанию 12)\n• Требуется положительный баланс монет\n\n" +
		"📊 /status — проверить статус и баланс\n❓ /help — показать это сообщение\n\n" +
		"📢 Новости и обновления: https://t.me/+HGQVwMhFzIExZDNi"
}

func handleCompressCommand(bot *tgbotapi.BotAPI, message *tgbotapi.Message, args []string) {
	quality := 12
	if len(args) > 1 {
		sendText(bot, message.Chat.ID, "Неверный формат. Пример: /compress 80")
		return
	}
	if len(args) == 1 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed < 1 || parsed > 100 {
			sendText(bot, message.Chat.ID, "Неверное значение качества. Используйте число от 1 до 100.\nПример: /compress 80")
			return
		}
		quality = parsed
	}
	userSessions.StartCompression(message.From.ID, quality)
	msg := tgbotapi.NewMessage(
		message.Chat.ID,
		fmt.Sprintf("Отправляйте изображения для сжатия до %d%% качества.\nНажмите «Отменить» или /cancel, когда закончите.", quality),
	)
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("Отменить")),
	)
	sendConfig(bot, msg)
}

func handlePhoto(bot *tgbotapi.BotAPI, message *tgbotapi.Message, pbUserID string, session UserSession, requestKey string) {
	fileID := message.Photo[len(message.Photo)-1].FileID
	if session.WaitingForCompress {
		select {
		case compressionSlot <- struct{}{}:
			sendText(bot, message.Chat.ID, "Обрабатываю...")
			go func(quality int) {
				defer func() { <-compressionSlot }()
				handleCompressImage(bot, message.Chat.ID, message.From.ID, pbUserID, fileID, quality)
			}(session.CompressQuality)
		default:
			sendText(bot, message.Chat.ID, "Сейчас уже обрабатываются другие изображения. Попробуйте через несколько секунд.")
		}
		return
	}

	if session.FaceFileID == "" {
		if err := savePendingFace(pbUserID, fileID); err != nil {
			log.Printf("Не удалось сохранить фотографию лица: %v", err)
			sendText(bot, message.Chat.ID, "Не удалось сохранить фотографию. Попробуйте ещё раз.")
			return
		}
		userSessions.SetFace(message.From.ID, fileID)
		sendTextWithCancelKeyboard(bot, message.Chat.ID, "Получена фотография. Пожалуйста, отправьте видео или второе фото для замены лица.")
		return
	}

	sendText(bot, message.Chat.ID, "Ловлю!")
	jobID, err := createFaceJob(bot, pbUserID, fileID, session.FaceFileID, requestKey)
	if err != nil {
		log.Printf("Не удалось создать задачу замены лица: %v", err)
		sendText(bot, message.Chat.ID, "Произошла ошибка при создании задания. Если ситуация повторяется, обратитесь в поддержку.")
		return
	}
	userSessions.Reset(message.From.ID)
	if err := clearPendingFace(pbUserID); err != nil {
		log.Printf("Не удалось очистить сессию после создания задачи %s: %v", jobID, err)
	}
	sendText(bot, message.Chat.ID, fmt.Sprintf("Ваше фото поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
}

func handleVideo(bot *tgbotapi.BotAPI, message *tgbotapi.Message, pbUserID string, session UserSession, requestKey string) {
	sendText(bot, message.Chat.ID, "Ловлю!")
	var jobID string
	var err error
	if session.FaceFileID != "" {
		jobID, err = createFaceJob(bot, pbUserID, message.Video.FileID, session.FaceFileID, requestKey)
	} else {
		jobID, err = createCircleJob(bot, pbUserID, message.Video.FileID, requestKey)
	}
	if err != nil {
		log.Printf("Не удалось создать задачу по видео: %v", err)
		sendText(bot, message.Chat.ID, "Произошла ошибка при создании задания. Если ситуация повторяется, обратитесь в поддержку.")
		return
	}
	userSessions.Reset(message.From.ID)
	if err := clearPendingFace(pbUserID); err != nil {
		log.Printf("Не удалось очистить сессию после создания задачи %s: %v", jobID, err)
	}
	sendText(bot, message.Chat.ID, fmt.Sprintf("Ваше видео поставлено в очередь для обработки. Статус: В очереди. ID: %s.", jobID))
}

func cancelSession(bot *tgbotapi.BotAPI, chatID, userID int64, pbUserID string) {
	userSessions.Reset(userID)
	if err := clearPendingFace(pbUserID); err != nil {
		log.Printf("Не удалось очистить сессию пользователя %d: %v", userID, err)
	}
	msg := tgbotapi.NewMessage(chatID, "Операция отменена.")
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	sendConfig(bot, msg)
}

func sendTextWithCancelKeyboard(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("Отменить")),
	)
	sendConfig(bot, msg)
}

func sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	sendConfig(bot, tgbotapi.NewMessage(chatID, text))
}

func sendConfig(bot *tgbotapi.BotAPI, config tgbotapi.Chattable) {
	if _, err := bot.Send(config); err != nil {
		log.Printf("Ошибка отправки сообщения в Telegram: %v", err)
	}
}

func initializeBot(ctx context.Context, token, endpoint string) (*tgbotapi.BotAPI, error) {
	for attempt := 1; ; attempt++ {
		if err := authenticatePocketBase(); err != nil {
			log.Printf("Ошибка авторизации PocketBase (попытка %d): %v", attempt, err)
		} else {
			bot, err := tgbotapi.NewBotAPIWithClient(token, endpoint+"/bot%s/%s", botHTTPClient)
			if err == nil {
				log.Printf("Авторизация Telegram выполнена для @%s", bot.Self.UserName)
				return bot, nil
			}
			log.Printf("Ошибка авторизации Telegram (попытка %d): %v", attempt, err)
		}

		delay := time.Duration(attempt) * 3 * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		if !waitContext(ctx, delay) {
			return nil, ctx.Err()
		}
	}
}

func pollUpdates(ctx context.Context, bot *tgbotapi.BotAPI) {
	config := tgbotapi.NewUpdate(0)
	config.Timeout = 60
	backoff := time.Second
	for ctx.Err() == nil {
		updates, err := bot.GetUpdatesWithContext(ctx, config)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Ошибка получения обновлений Telegram: %v", err)
			if !waitContext(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, update := range updates {
			if update.UpdateID >= config.Offset {
				config.Offset = update.UpdateID + 1
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						log.Printf("Паника при обработке update %d: %v", update.UpdateID, recovered)
					}
				}()
				handleUpdate(bot, update)
			}()
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func main() {
	commitShort := GitCommit
	if len(GitCommit) > 8 {
		commitShort = GitCommit[:8]
	}
	log.Println("Telegram Bot запущен")
	log.Printf("Версия: %s — %s", commitShort, GitMessage)

	token, debug, endpoint := LoadEnvironment()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	bot, err := initializeBot(ctx, token, endpoint)
	if err != nil {
		log.Printf("Инициализация бота прервана: %v", err)
		return
	}
	bot.Debug = debug
	if debug {
		log.Println("Бот работает в режиме DEBUG")
	}

	pollUpdates(ctx, bot)
	log.Println("Telegram Bot завершил работу")
}
