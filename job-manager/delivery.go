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
)

func notifyCircleOwner(task *Task) error {
	if task.Owner == "" {
		return fmt.Errorf("задача с ID %s не содержит корректного owner", task.ID)
	}

	ownerTGID, err := getOwnerTGID(task.Owner)
	if err != nil {
		return fmt.Errorf("ошибка получения Telegram ID владельца задачи %s: %v", task.ID, err)
	}

	outputFilePath := filepath.Join(jobCacheDirectory(), task.ID+"_output.mp4")
	file, err := os.Open(outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия кружка для отправки: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", ownerTGID); err != nil {
		return fmt.Errorf("ошибка добавления chat_id: %v", err)
	}
	filePart, err := writer.CreateFormFile("video_note", filepath.Base(outputFilePath))
	if err != nil {
		return fmt.Errorf("ошибка добавления кружка в запрос: %v", err)
	}
	if _, err := io.Copy(filePart, file); err != nil {
		return fmt.Errorf("ошибка чтения кружка: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("ошибка завершения запроса Telegram: %v", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendVideoNote", BOT_ENDPOINT, BOT_TOKEN)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса Telegram: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки кружка: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа Telegram: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ошибка Telegram API, код %d: %s", resp.StatusCode, string(responseBody))
	}

	log.Printf("Видеосообщение отправлено владельцу задачи %s (Telegram ID: %s)", task.ID, ownerTGID)
	return nil
}
