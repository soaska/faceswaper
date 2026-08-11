package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
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
	url := fmt.Sprintf("%s/bot%s/sendVideoNote", BOT_ENDPOINT, BOT_TOKEN)
	resp, err := doStreamingMultipartFileRequest(
		mediaHTTPClient,
		http.MethodPost,
		url,
		nil,
		map[string]string{"chat_id": ownerTGID},
		"video_note",
		outputFilePath,
	)
	if err != nil {
		return fmt.Errorf("ошибка отправки кружка: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа Telegram: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ошибка Telegram API, код %d: %s", resp.StatusCode, string(responseBody))
	}

	log.Printf("Видеосообщение отправлено владельцу задачи %s (Telegram ID: %s)", task.ID, ownerTGID)
	return nil
}
