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

	"encoding/json"
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
			updateTaskStatus(task.ID, fmt.Sprintf("error: %v", err)[:min(len(fmt.Sprintf("error: %v", err)), 255)])
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

// Получение задачи в статусе "queued"
func fetchQueuedCircleJob(collection string) (*Task, error) {
	filter := "status='queued'"
	url := fmt.Sprintf("%s/api/collections/%s/records?filter=%s&perPage=1", pocketBaseUrl, collection, filter)

	body, err := sendAuthorizedRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при запросе задач: %v", err)
	}

	var response struct {
		Items []Task `json:"items"`
	}

	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("Ошибка разбора JSON: %v", err)
	}

	if len(response.Items) > 0 {
		return &response.Items[0], nil
	}

	return nil, nil // Нет задач в статусе "queued"
}

// Обновление статуса задачи
func updateTaskStatus(taskID, status string) error {
	url := fmt.Sprintf("%s/api/collections/circle_jobs/records/%s", pocketBaseUrl, taskID)

	data := map[string]string{
		"status": status,
	}
	jsonData, _ := json.Marshal(data)

	_, err := sendAuthorizedRequest("PATCH", url, jsonData)
	if err != nil {
		return fmt.Errorf("Ошибка обновления статуса задачи: %v", err)
	}

	return nil
}

// Обработка задачи
func processTask(task *Task) error {
	if task.InputMedia == "" {
		return fmt.Errorf("Задача с ID %s не содержит ссылки на input_media", task.ID)
	}

	cacheDir := "cache"
	err := os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("Ошибка создания кэша: %v", err)
	}

	inputFilePath := filepath.Join(cacheDir, fmt.Sprintf("%s_input.mp4", task.ID))
	outputFilePath := filepath.Join(cacheDir, fmt.Sprintf("%s_output.mp4", task.ID))
	mediaUrl := fmt.Sprintf("%s/api/files/circle_jobs/%s/%s", pocketBaseUrl, task.ID, task.InputMedia)

	err = downloadFile(mediaUrl, inputFilePath)
	if err != nil {
		return fmt.Errorf("Ошибка скачивания файла: %v", err)
	}

	err = processVideo(inputFilePath, outputFilePath)
	if err != nil {
		return fmt.Errorf("Ошибка обработки видео: %v", err)
	}

	err = uploadOutputMedia(task.ID, outputFilePath)
	if err != nil {
		return fmt.Errorf("Ошибка загрузки кружка в бд: %v", err)
	}

	return nil
}

// Скачивание файла
func downloadFile(url, destination string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("Ошибка скачивания: %v", err)
	}
	defer resp.Body.Close()

	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("Ошибка создания файла: %v", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("Ошибка сохранения файла: %v", err)
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
		return fmt.Errorf("Ошибка ffmpeg: %v, вывод: %s", err, string(output))
	}

	return nil
}

// Загрузка обработанного файла в output_media
func uploadOutputMedia(taskID, filePath string) error {
	url := fmt.Sprintf("%s/api/collections/circle_jobs/records/%s", pocketBaseUrl, taskID)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Ошибка открытия файла: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем файл
	filePart, err := writer.CreateFormFile("output_media", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("Ошибка добавления файла в запрос: %v", err)
	}
	_, err = io.Copy(filePart, file)
	if err != nil {
		return fmt.Errorf("Ошибка копирования содержимого файла: %v", err)
	}

	// Завершаем формирование multipart
	writer.WriteField("status", "completed")
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("Ошибка завершения multipart: %v", err)
	}

	request, err := http.NewRequest("PATCH", url, body)
	if err != nil {
		return fmt.Errorf("Ошибка создания запроса: %v", err)
	}

	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{}

	resp, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Ошибка отправки запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Ошибка загрузки файла: статус %d, ответ: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Увеличение circle_count на 1 для владельца задачи
func incrementCircleCount(tgUserID int) error {
	userInfo, err := getUserInfo(tgUserID)
	if err != nil {
		return fmt.Errorf("Ошибка получения информации о пользователе с Telegram ID %d: %v", tgUserID, err)
	}
	currentCircleCount, ok := userInfo["circle_count"].(float64) // JSON numbers в Go парсятся в float64
	if !ok {
		currentCircleCount = 0
	}

	newCircleCount := int(currentCircleCount) + 1

	updateData := map[string]interface{}{
		"circle_count": newCircleCount,
	}

	userID, ok := userInfo["id"].(string)
	if !ok {
		return fmt.Errorf("Не удалось извлечь ID пользователя с Telegram ID %d", tgUserID)
	}

	updateURL := fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID)

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("Ошибка сериализации данных для обновления: %v", err)
	}

	_, err = sendAuthorizedRequest("PATCH", updateURL, jsonData)
	if err != nil {
		return fmt.Errorf("Ошибка обновления circle_count для пользователя %s: %v", userID, err)
	}

	// log.Printf("circle_count для пользователя с Telegram ID %d успешно обновлен. Новое значение: %d", tgUserID, newCircleCount)
	return nil
}

// Получение Telegram ID владельца
func getOwnerTGID(ownerID string) (string, error) {
	url := fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, ownerID)
	body, err := sendAuthorizedRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("Ошибка получения данных о владельце: %v", err)
	}

	var ownerData struct {
		TGID int `json:"tgid"`
	}

	err = json.Unmarshal(body, &ownerData)
	if err != nil {
		return "", fmt.Errorf("Ошибка разбора данных о владельце: %v", err)
	}

	if ownerData.TGID == 0 {
		return "", fmt.Errorf("Telegram ID владельца %s не найден", ownerID)
	}

	return strconv.Itoa(ownerData.TGID), nil
}

// Отправка готового видеосообщения владельцу через Telegram API
func notifyOwner(task *Task) error {
	if task.Owner == "" {
		return fmt.Errorf("Задача с ID %s не содержит корректного owner", task.ID)
	}

	ownerTGID, err := getOwnerTGID(task.Owner)
	if err != nil {
		return fmt.Errorf("Ошибка получения Telegram ID владельца задачи %s: %v", task.ID, err)
	}

	outputFilePath := filepath.Join("cache", fmt.Sprintf("%s_output.mp4", task.ID))
	if _, err := os.Stat(outputFilePath); err != nil {
		return fmt.Errorf("Файл для отправки не найден: %v", err)
	}

	file, err := os.Open(outputFilePath)
	if err != nil {
		return fmt.Errorf("Ошибка открытия файла: %v", err)
	}
	defer file.Close()

	var fileBuffer bytes.Buffer
	_, err = io.Copy(&fileBuffer, file)
	if err != nil {
		return fmt.Errorf("Ошибка чтения содержимого файла: %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	err = writer.WriteField("chat_id", ownerTGID)
	if err != nil {
		return fmt.Errorf("Ошибка добавления поля chat_id: %v", err)
	}

	filePart, err := writer.CreateFormFile("video_note", filepath.Base(outputFilePath))
	if err != nil {
		return fmt.Errorf("Ошибка добавления файла в запрос: %v", err)
	}
	_, err = fileBuffer.WriteTo(filePart)
	if err != nil {
		return fmt.Errorf("Ошибка записи видео в multipart форму: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("Ошибка закрытия записи multipart данных: %v", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendVideoNote", BOT_ENDPOINT, os.Getenv("TELEGRAM_APITOKEN"))

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("Ошибка создания HTTP-запроса: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Отправляем запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Ошибка отправки запроса Telegram API: %v", err)
	}
	defer resp.Body.Close()

	// Проверяем ответ от Telegram API
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Ошибка чтения ответа Telegram API: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ошибка в Telegram API. Код %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("Видеосообщение отправлено владельцу задачи %s (Telegram ID: %s).", task.ID, ownerTGID)

	// Увеличиваем счетчик кружков (circle_count) для владельца
	tgid, err := strconv.Atoi(ownerTGID)
	if err != nil {
		return fmt.Errorf("Ошибка преобразования telegram id в int: %s", err)
	}
	err = incrementCircleCount(tgid)
	if err != nil {
		log.Printf("Ошибка обновления circle_count для владельца задачи %s: %v", task.ID, err)
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
