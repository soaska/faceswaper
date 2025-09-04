package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"context"
	"os/signal"
	"syscall"
)

// Task - структура для хранения данных задачи
type Task struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	InputMedia  string `json:"input_media"` // видео
	SourceImage string `json:"input_face"`  // фото для замены лица
	OutputMedia string `json:"output_media"`
	Status      string `json:"status"`
}

// Основной цикл обработки задач создания кружков
func processCircleJobs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Процессор задач создания кружков завершает работу...")
			return
		default:
		}
		
		task, err := fetchQueuedJobs("circle_jobs")
		if err != nil {
			log.Printf("Ошибка при получении задачи: %v", err)
			continue
		}
		if task == nil {
			wait()
			continue
		}

		// Получаем Telegram ID владельца
		ownerTGID, err := getOwnerTGID(task.Owner)
		if err != nil {
			log.Printf("Ошибка получения Telegram ID владельца для задачи %s: %v", task.ID, err)
			continue
		}

		// Проверяем монеты
		tgid, err := strconv.Atoi(ownerTGID)
		if err != nil {
			log.Printf("Ошибка преобразования telegram id в int: %v", err)
			continue
		}

		userInfo, err := getUserInfo(tgid)
		if err != nil {
			log.Printf("Ошибка получения информации о пользователе: %v", err)
			continue
		}

		currentCoins, ok := userInfo["coins"].(float64)
		if !ok {
			currentCoins = 0
		}

		if int(currentCoins) < 1 {
			log.Printf("Недостаточно монет для задачи %s. Требуется: 1, доступно: %d", task.ID, int(currentCoins))
			updateStatus("circle_jobs", task.ID, fmt.Sprintf("error: недостаточно монет. Требуется: 1, доступно: %d", int(currentCoins)))
			sendErrorNotification(task.Owner, task.ID)
			continue
		}

		// Списываем монеты
		deductedAmount, err := checkAndDeductCoins(tgid, 1)
		if err != nil {
			log.Printf("Ошибка списания монет для задачи %s: %v", task.ID, err)
			updateStatus("circle_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			continue
		}

		err = updateStatus("circle_jobs", task.ID, "processing")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'processing' для задачи %s: %v", task.ID, err)
			refundCoins(tgid, deductedAmount)
			continue
		}

		err = processCircleTask(task)
		if err != nil {
			log.Printf("Ошибка обработки задачи %s: %v", task.ID, err)
			updateStatus("circle_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			refundCoins(tgid, deductedAmount)
			continue
		}

		err = updateStatus("circle_jobs", task.ID, "sending")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'sending' для задачи %s: %v", task.ID, err)
			refundCoins(tgid, deductedAmount)
			continue
		}

		err = notifyCircleOwner(task)
		if err != nil {
			log.Printf("Ошибка отправки для задачи %s: %v", task.ID, err)
			updateStatus("circle_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			refundCoins(tgid, deductedAmount)
			continue
		}

		err = updateStatus("circle_jobs", task.ID, "completed")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'completed' для задачи %s: %v", task.ID, err)
		}

		// Увеличиваем счетчик кружков
		err = incrementCircleCount(tgid)
		if err != nil {
			log.Printf("Ошибка обновления circle_count для владельца задачи %s: %v", task.ID, err)
		}
	}
}

// processFaceSwapJobs основной цикл обработки задач замены лиц
func processFaceSwapJobs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Процессор задач замены лиц завершает работу...")
			return
		default:
		}
		
		task, err := fetchQueuedJobs("face_jobs")
		if err != nil {
			log.Printf("Ошибка при получении задачи замены лиц: %v", err)
			continue
		}
		if task == nil {
			wait()
			continue
		}

		// Получаем Telegram ID владельца
		ownerTGID, err := getOwnerTGID(task.Owner)
		if err != nil {
			log.Printf("Ошибка получения Telegram ID владельца для задачи %s: %v", task.ID, err)
			continue
		}

		// Проверяем монеты
		tgid, err := strconv.Atoi(ownerTGID)
		if err != nil {
			log.Printf("Ошибка преобразования telegram id в int: %v", err)
			continue
		}

		userInfo, err := getUserInfo(tgid)
		if err != nil {
			log.Printf("Ошибка получения информации о пользователе: %v", err)
			continue
		}

		currentCoins, ok := userInfo["coins"].(float64)
		if !ok {
			currentCoins = 0
		}

		if int(currentCoins) < 2 {
			log.Printf("Недостаточно монет для задачи %s. Минимум требуется: 2, доступно: %d", task.ID, int(currentCoins))
			updateStatus("face_jobs", task.ID, fmt.Sprintf("error: недостаточно монет. Минимум требуется: 2, доступно: %d", int(currentCoins)))
			sendErrorNotification(task.Owner, task.ID)
			continue
		}

		// Списываем базовые 2 монеты
		baseAmount, err := checkAndDeductCoins(tgid, 2)
		if err != nil {
			log.Printf("Ошибка списания базовых монет для задачи %s: %v", task.ID, err)
			updateStatus("face_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			continue
		}

		err = updateStatus("face_jobs", task.ID, "processing")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'processing' для задачи %s: %v", task.ID, err)
			refundCoins(tgid, baseAmount)
			continue
		}

		duration, err := processFaceSwapTask(task)
		if err != nil {
			log.Printf("Ошибка обработки задачи замены лиц %s: %v", task.ID, err)
			updateStatus("face_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			refundCoins(tgid, baseAmount)
			continue
		}

		// Calculate additional cost based on processing time
		additionalCost := duration / 20  // floor division
		totalCost := 2 + additionalCost
		
		if totalCost > 30 {
			totalCost = 30  // cap at 30 coins
		}
		
		// Handle additional coins
		additionalAmount := 0
		finalAdditionalCost := additionalCost
		if additionalCost > 0 {
			// Try to deduct additional coins
			additionalAmount, err = checkAndDeductCoins(tgid, additionalCost)
			if err != nil {
				// User can't pay additional cost - double it and force negative balance
				finalAdditionalCost = additionalCost * 2
				log.Printf("Пользователь не может доплатить %d монет за задачу %s, удваиваем до %d и разрешаем отрицательный баланс", additionalCost, task.ID, finalAdditionalCost)
				
				err = forceDeductCoins(tgid, finalAdditionalCost)
				if err != nil {
					log.Printf("Ошибка принудительного списания монет для задачи %s: %v", task.ID, err)
					updateStatus("face_jobs", task.ID, fmt.Sprintf("error: %v", err))
					sendErrorNotification(task.Owner, task.ID)
					refundCoins(tgid, baseAmount)
					continue
				}
				additionalAmount = finalAdditionalCost
			}
		}
		
		totalDeducted := baseAmount + additionalAmount
		log.Printf("Задача %s обработана за %d секунд, списано %d монет (базовые: %d, за время: %d)", task.ID, duration, totalDeducted, baseAmount, additionalAmount)

		// Update duration and price in database
		err = updateTaskDurationAndPrice(task.ID, duration, totalDeducted)
		if err != nil {
			log.Printf("Предупреждение: Не удалось обновить длительность и цену для задачи %s: %v", task.ID, err)
		}

		// Устанавливаем статус sending перед отправкой
		err = updateStatus("face_jobs", task.ID, "sending")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'sending' для задачи %s: %v", task.ID, err)
			refundCoins(tgid, totalDeducted)
			continue
		}

		// Отправляем результат пользователю
		outputPath := filepath.Join("cache", fmt.Sprintf("%s_output.mp4", task.ID))
		err = sendVideoToUser(task.Owner, outputPath)
		if err != nil {
			log.Printf("Ошибка отправки видео пользователю для задачи %s: %v", task.ID, err)
			updateStatus("face_jobs", task.ID, fmt.Sprintf("error: %v", err))
			sendErrorNotification(task.Owner, task.ID)
			refundCoins(tgid, totalDeducted)
			continue
		}

		err = updateStatus("face_jobs", task.ID, "completed")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'completed' для задачи %s: %v", task.ID, err)
		}

		// Увеличиваем счетчик замен лиц
		err = incrementFaceReplaceCount(tgid)
		if err != nil {
			log.Printf("Ошибка обновления face_replace_count для владельца задачи %s: %v", task.ID, err)
		}

		log.Printf("Задача замены лиц %s успешно обработана", task.ID)
	}
}

// Отправка готового видеосообщения владельцу через Telegram API и обновление баланса
func notifyCircleOwner(task *Task) error {
	if task.Owner == "" {
		return fmt.Errorf("задача с ID %s не содержит корректного owner", task.ID)
	}

	ownerTGID, err := getOwnerTGID(task.Owner)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца задачи %s: %v", task.ID, err)
	}

	outputFilePath := filepath.Join("cache", fmt.Sprintf("%s_output.mp4", task.ID))
	if _, err := os.Stat(outputFilePath); err != nil {
		return fmt.Errorf("файл для отправки не найден: %v", err)
	}

	file, err := os.Open(outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	var fileBuffer bytes.Buffer
	_, err = io.Copy(&fileBuffer, file)
	if err != nil {
		return fmt.Errorf("ошибка чтения содержимого файла: %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	err = writer.WriteField("chat_id", ownerTGID)
	if err != nil {
		return fmt.Errorf("ошибка добавления поля chat_id: %v", err)
	}

	filePart, err := writer.CreateFormFile("video_note", filepath.Base(outputFilePath))
	if err != nil {
		return fmt.Errorf("ошибка добавления файла в запрос: %v", err)
	}
	_, err = fileBuffer.WriteTo(filePart)
	if err != nil {
		return fmt.Errorf("ошибка записи видео в multipart форму: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка закрытия записи multipart данных: %v", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendVideoNote", BOT_ENDPOINT, BOT_TOKEN)

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания HTTP-запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса Telegram API: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа Telegram API: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ошибка в Telegram API. Код %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("Видеосообщение отправлено владельцу задачи %s (Telegram ID: %s).", task.ID, ownerTGID)

	// Увеличиваем счетчик кружков (circle_count) для владельца
	tgid, err := strconv.Atoi(ownerTGID)
	if err != nil {
		return fmt.Errorf("ошибка преобразования telegram id в int: %s", err)
	}
	err = incrementCircleCount(tgid)
	if err != nil {
		return fmt.Errorf("ошибка обновления circle_count для владельца задачи %s: %v", task.ID, err)
	}

	return nil
}

func wait() {
	<-time.After(10 * time.Second)
}

func cleanupTempFiles() {
	cacheDir := "cache"
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return
	}
	
	files, err := filepath.Glob(filepath.Join(cacheDir, "*"))
	if err != nil {
		log.Printf("Ошибка поиска файлов кэша: %v", err)
		return
	}
	
	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			if time.Since(info.ModTime()) > time.Hour {
				if err := os.Remove(file); err != nil {
					log.Printf("Не удалось удалить старый файл кэша %s: %v", file, err)
				} else {
					log.Printf("Удален старый файл кэша: %s", file)
				}
			}
		}
	}
}

func initializeServices() error {
	for retries := 0; retries < 5; retries++ {
		err := authenticatePocketBase()
		if err == nil {
			log.Println("Авторизация PocketBase успешна")
			return nil
		}
		log.Printf("Ошибка авторизации PocketBase (попытка %d/5): %v", retries+1, err)
		time.Sleep(time.Duration(retries+1) * 5 * time.Second)
	}
	return fmt.Errorf("не удалось авторизоваться после 5 попыток")
}

func main() {
	BOT_TOKEN, _, BOT_ENDPOINT, FaceSwapComponent_URL = LoadEnvironment()

	// Cleanup old temp files on startup
	cleanupTempFiles()

	// Initialize services with retry
	if err := initializeServices(); err != nil {
		log.Fatalf("Ошибка инициализации сервисов: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Println("Получен сигнал остановки, корректно завершаем работу...")
		cancel()
	}()

	// Start processors with context
	go processCircleJobs(ctx)
	go processFaceSwapJobs(ctx)

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Менеджер задач завершил работу")
}
