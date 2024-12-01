package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"mime/multipart"
)

// Task - структура для хранения данных задачи
type Task struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	InputMedia  string `json:"input_media"`
	OutputMedia string `json:"output_media"`
	Status      string `json:"status"`
}

// Основная функция обработки задачи
func processCircleJobs() {
	for {
		task, err := fetchQueuedCircleJob("circle_jobs")
		if err != nil {
			log.Printf("Ошибка при получении задачи: %v", err)
			continue
		}
		if task == nil {
			wait()
			continue
		}

		err = updateTaskStatus(task.ID, "processing")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'processing' для задачи %s: %v", task.ID, err)
			continue
		}

		err = processTask(task)
		if err != nil {
			log.Printf("Ошибка обработки задачи %s: %v", task.ID, err)
			updateTaskStatus(task.ID, fmt.Sprintf("error. time: %v", time.Now()))
			continue
		}

		err = updateTaskStatus(task.ID, "sending")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'sending' для задачи %s: %v", task.ID, err)
		}

		err = notifyOwner(task)
		if err != nil {
			log.Printf("Ошибка отправки для задачи %s: %v", task.ID, err)
		}

		err = updateTaskStatus(task.ID, "completed")
		if err != nil {
			log.Printf("Ошибка смены статуса на 'completed' для задачи %s: %v", task.ID, err)
		}

	}
}

// Обработка задачи
func processTask(task *Task) error {
	if task.InputMedia == "" {
		return fmt.Errorf("задача с ID %s не содержит ссылки на input_media", task.ID)
	}

	cacheDir := "cache"
	err := os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("ошибка создания кэша: %v", err)
	}

	inputFilePath := filepath.Join(cacheDir, fmt.Sprintf("%s_input.mp4", task.ID))
	outputFilePath := filepath.Join(cacheDir, fmt.Sprintf("%s_output.mp4", task.ID))
	mediaUrl := fmt.Sprintf("%s/api/files/circle_jobs/%s/%s", pocketBaseUrl, task.ID, task.InputMedia)

	err = downloadFile(mediaUrl, inputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка скачивания файла: %v", err)
	}

	err = processVideo(inputFilePath, outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка обработки видео: %v", err)
	}

	err = uploadOutputMedia(task.ID, outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки кружка в бд: %v", err)
	}

	return nil
}

// Скачивание файла
func downloadFile(url, destination string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("ошибка скачивания: %v", err)
	}
	defer resp.Body.Close()

	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("ошибка создания файла: %v", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка сохранения файла: %v", err)
	}

	return nil
}

// Обработка файла
func processVideo(inputPath, outputPath string) error {
	cmd := exec.Command(
		"ffmpeg",
		"-i", inputPath,
		"-vf", "crop=min(iw\\,ih):min(iw\\,ih):(iw-min(iw\\,ih))/2:(ih-min(iw\\,ih))/2,scale=512:512",
		"-r", "30",
		"-t", "60",
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		outputPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ошибка ffmpeg: %v, вывод: %s", err, string(output))
	}

	return nil
}

// Отправка готового видеосообщения владельцу через Telegram API
func notifyOwner(task *Task) error {
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
	BOT_TOKEN, _, BOT_ENDPOINT = LoadEnvironment()

	err := authenticatePocketBase()
	if err != nil {
		log.Fatalf("Ошибка аутентификации: %v", err)
	}

	processCircleJobs()
}
