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

// Отправка сообщения в Telegram
func sendTelegramMessage(chatID string, message string) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", BOT_ENDPOINT, os.Getenv("TELEGRAM_APITOKEN"))

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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ошибка в Telegram API. Код %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// Основной цикл обработки задач создания кружков
func processCircleJobs() {
	for {
		task, err := fetchQueuedJobs("circle_jobs")
		if err != nil {
			log.Printf("Ошибка при получении задачи: %v", err)
			continue
		}
		if task == nil {
			wait()
			continue
		}

		err = updateStatus("circle_jobs", task.ID, "processing")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'processing' для задачи %s: %v", task.ID, err)
			continue
		}

		err = processCircleTask(task)
		if err != nil {
			log.Printf("Ошибка обработки задачи %s: %v", task.ID, err)
			updateStatus("circle_jobs", task.ID, fmt.Sprintf("error. time: %v", time.Now()))
			continue
		}

		err = updateStatus("circle_jobs", task.ID, "sending")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'sending' для задачи %s: %v", task.ID, err)
		}

		err = notifyCircleOwner(task)
		if err != nil {
			log.Printf("Ошибка отправки для задачи %s: %v", task.ID, err)
		}

		err = updateStatus("circle_jobs", task.ID, "completed")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'completed' для задачи %s: %v", task.ID, err)
		}

	}
}

// processFaceSwapJobs основной цикл обработки задач замены лиц
func processFaceSwapJobs() {
	for {
		task, err := fetchQueuedJobs("face_jobs")
		if err != nil {
			log.Printf("Ошибка при получении задачи замены лиц: %v", err)
			continue
		}
		if task == nil {
			wait()
			continue
		}

		err = updateStatus("face_jobs", task.ID, "processing")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'processing' для задачи %s: %v", task.ID, err)
			continue
		}

		err = processFaceSwapTask(task)
		if err != nil {
			log.Printf("Ошибка обработки задачи замены лиц %s: %v", task.ID, err)
			updateStatus("face_jobs", task.ID, fmt.Sprintf("error. time: %v", time.Now()))
			continue
		}

		err = updateStatus("face_jobs", task.ID, "completed")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'completed' для задачи %s: %v", task.ID, err)
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

	url := fmt.Sprintf("%s/bot%s/sendVideoNote", BOT_ENDPOINT, os.Getenv("TELEGRAM_APITOKEN"))

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания HTTP-запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Отправляем запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса Telegram API: %v", err)
	}
	defer resp.Body.Close()

	// Проверяем ответ от Telegram API
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

func main() {
	BOT_TOKEN, _, BOT_ENDPOINT, FaceSwapComponent_URL = LoadEnvironment()

	err := authenticatePocketBase()
	if err != nil {
		log.Fatalf("Ошибка аутентификации: %v", err)
	}

	go processCircleJobs()
	go processFaceSwapJobs()

	select {}
}
