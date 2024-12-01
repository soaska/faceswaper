package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// getting JWT for pocketbase
func authenticatePocketBase() error {
	authData := map[string]string{
		"identity": email,
		"password": password,
	}

	authDataJson, _ := json.Marshal(authData)

	authURL := fmt.Sprintf("%s/api/admins/auth-with-password", pocketBaseUrl)

	resp, err := http.Post(authURL, "application/json", bytes.NewBuffer(authDataJson))
	if err != nil {
		return fmt.Errorf("не удалось отправить запрос на авторизацию: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("авторизация не удалась, код %d, ответ: %s", resp.StatusCode, string(body))
	}

	// getting jwt
	body, _ := io.ReadAll(resp.Body)
	var authResponse map[string]interface{}
	if err := json.Unmarshal(body, &authResponse); err != nil {
		return fmt.Errorf("ошибка разбора ответа: %v, ответ: %s", err, string(body))
	}

	token, ok := authResponse["token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("не удалось получить токен из ответа: %s", string(body))
	}

	authToken = token
	log.Println("PocketBase: Авторизация прошла успешно. Получен токен от PocketBase.")
	return nil
}

func getUserInfo(tgUserID int) (map[string]interface{}, error) {
	// Поиск пользователя в PocketBase по tgid
	searchURL := fmt.Sprintf("%s/api/collections/users/records?filter=tgid=%d", pocketBaseUrl, tgUserID)
	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка при запросе пользователя: %v", err)
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return nil, fmt.Errorf("ошибка разбора ответа при получении пользователя: %v", err)
	}

	if items, ok := searchResult["items"].([]interface{}); ok && len(items) > 0 {
		if user, ok := items[0].(map[string]interface{}); ok {
			return user, nil
		}
	}

	return nil, fmt.Errorf("пользователь с Telegram ID %d не найден", tgUserID)
}

// Увеличение circle_count на 1 для владельца задачи
func incrementCircleCount(tgUserID int) error {
	userInfo, err := getUserInfo(tgUserID)
	if err != nil {
		return fmt.Errorf("ошибка получения информации о пользователе с Telegram ID %d: %v", tgUserID, err)
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
		return fmt.Errorf("не удалось извлечь ID пользователя с Telegram ID %d", tgUserID)
	}

	updateURL := fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID)

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("ошибка сериализации данных для обновления: %v", err)
	}

	_, err = sendAuthorizedRequest("PATCH", updateURL, jsonData)
	if err != nil {
		return fmt.Errorf("ошибка обновления circle_count для пользователя %s: %v", userID, err)
	}

	// log.Printf("circle_count для пользователя с Telegram ID %d успешно обновлен. Новое значение: %d", tgUserID, newCircleCount)
	return nil
}

// Загрузка обработанного файла в output_media
func uploadOutputMedia(taskID, filePath string) error {
	url := fmt.Sprintf("%s/api/collections/circle_jobs/records/%s", pocketBaseUrl, taskID)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем файл
	filePart, err := writer.CreateFormFile("output_media", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("ошибка добавления файла в запрос: %v", err)
	}
	_, err = io.Copy(filePart, file)
	if err != nil {
		return fmt.Errorf("ошибка копирования содержимого файла: %v", err)
	}

	// Завершаем формирование multipart
	writer.WriteField("status", "completed")
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	request, err := http.NewRequest("PATCH", url, body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %v", err)
	}

	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{}

	resp, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ошибка загрузки файла: статус %d, ответ: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Получение Telegram ID владельца
func getOwnerTGID(ownerID string) (string, error) {
	url := fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, ownerID)
	body, err := sendAuthorizedRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка получения данных о владельце: %v", err)
	}

	var ownerData struct {
		TGID int `json:"tgid"`
	}

	err = json.Unmarshal(body, &ownerData)
	if err != nil {
		return "", fmt.Errorf("ошибка разбора данных о владельце: %v", err)
	}

	if ownerData.TGID == 0 {
		return "", fmt.Errorf("telegram id владельца %s не найден", ownerID)
	}

	return strconv.Itoa(ownerData.TGID), nil
}

// Получение задачи в статусе "queued"
func fetchQueuedCircleJob(collection string) (*Task, error) {
	filter := "status='queued'"
	url := fmt.Sprintf("%s/api/collections/%s/records?filter=%s&perPage=1", pocketBaseUrl, collection, filter)

	body, err := sendAuthorizedRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка при запросе задач: %v", err)
	}

	var response struct {
		Items []Task `json:"items"`
	}

	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("ошибка разбора JSON: %v", err)
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
		return fmt.Errorf("ошибка обновления статуса задачи: %v", err)
	}

	return nil
}
