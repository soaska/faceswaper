package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
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

func getOrCreateUser(tgUserID int, tgUsername string) (string, error) {
	// Search in pocketbase
	searchURL := fmt.Sprintf("%s/api/collections/users/records?filter=tgid=%d", pocketBaseUrl, tgUserID)
	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка при отправке запроса на поиск пользователя: %v", err)
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return "", fmt.Errorf("ошибка разбора ответа: %v, ответ: %s", err, string(resp))
	}

	// Check search results
	if items, ok := searchResult["items"].([]interface{}); ok && len(items) > 0 {
		if user, ok := items[0].(map[string]interface{}); ok {
			if userID, ok := user["id"]; ok {
				if idStr, ok := userID.(string); ok && idStr != "" {
					// Пользователь найден, возвращаем его ID
					return idStr, nil
				}
			}
		}
	}

	// New user creation
	userData := map[string]interface{}{
		"tgid":               tgUserID,
		"username":           tgUsername,
		"circle_count":       0,
		"face_replace_count": 0,
		"coins":              200,
	}
	userDataJson, _ := json.Marshal(userData)

	createUserURL := fmt.Sprintf("%s/api/collections/users/records", pocketBaseUrl)
	createResp, err := sendAuthorizedRequest("POST", createUserURL, userDataJson)
	if err != nil {
		return "", fmt.Errorf("ошибка при отправке запроса на создание пользователя: %v", err)
	}

	var createdUser map[string]interface{}
	if err := json.Unmarshal(createResp, &createdUser); err != nil {
		return "", fmt.Errorf("ошибка разбора ответа на создание пользователя: %v, ответ: %s", err, string(createResp))
	}

	// User creation recheck
	if userID, ok := createdUser["id"].(string); ok && userID != "" {
		return userID, nil
	}

	return "", fmt.Errorf("не удалось получить ID созданного пользователя из ответа: %s", string(createResp))
}

// Face replacement job creation
func createFaceJob(bot *tgbotapi.BotAPI, userID, inputMediaFileID, inputFaceFileID string) (error, string) {
	// file dir creation
	userDir := fmt.Sprintf("data/%s", userID)
	err := os.MkdirAll(userDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("не удалось создать директорию пользователя: %v", err), ""
	}

	// file download
	inputMediaPath := fmt.Sprintf("%s/%s.mp4", userDir, inputMediaFileID)
	err = downloadTelegramFile(bot, inputMediaFileID, inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось скачать видеофайл: %v", err), ""
	}

	inputFacePath := fmt.Sprintf("%s/%s.jpeg", userDir, inputFaceFileID)
	err = downloadTelegramFile(bot, inputFaceFileID, inputFacePath)
	if err != nil {
		return fmt.Errorf("не удалось скачать файл лица: %v", err), ""
	}

	// file checker
	inputMediaFile, err := os.Open(inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось открыть видеофайл: %v", err), ""
	}
	defer inputMediaFile.Close()

	inputFaceFile, err := os.Open(inputFacePath)
	if err != nil {
		return fmt.Errorf("не удалось открыть файл лица: %v", err), ""
	}
	defer inputFaceFile.Close()

	// Check size
	// needed for testing. will be removed.
	fileInfo, err := inputMediaFile.Stat()
	if err != nil {
		return fmt.Errorf("не удалось получить информацию о видеофайле: %v", err), ""
	}
	if fileInfo.Size() > 50*1024*1024 {
		return fmt.Errorf("размер видео превышает 50 МБ"), ""
	}

	faceFileInfo, err := inputFaceFile.Stat()
	if err != nil {
		return fmt.Errorf("не удалось получить информацию о файле лица: %v", err), ""
	}
	if faceFileInfo.Size() > 50*1024*1024 {
		return fmt.Errorf("размер файла лица превышает 50 МБ"), ""
	}

	// body for multipart/form-data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// metadata
	_ = writer.WriteField("owner", userID)
	_ = writer.WriteField("status", "queued") // Статус задачи по умолчанию

	// Добавляем файлы в request
	mediaPart, err := writer.CreateFormFile("input_media", fileInfo.Name())
	if err != nil {
		return fmt.Errorf("не удалось создать часть для видеофайла: %v", err), ""
	}
	_, err = io.Copy(mediaPart, inputMediaFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить видеофайл: %v", err), ""
	}

	facePart, err := writer.CreateFormFile("input_face", faceFileInfo.Name())
	if err != nil {
		return fmt.Errorf("не удалось создать часть для файла лица: %v", err), ""
	}
	_, err = io.Copy(facePart, inputFaceFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить файл лица: %v", err), ""
	}

	// Закрываем writer, чтобы завершить формирование multipart
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("не удалось завершить формирование multipart: %v", err), ""
	}

	// Формируем запрос
	createJobURL := fmt.Sprintf("%s/api/collections/face_jobs/records", pocketBaseUrl)
	req, err := http.NewRequest("POST", createJobURL, body)
	if err != nil {
		return fmt.Errorf("не удалось создать HTTP-запрос для создания задачи: %v", err), ""
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken)) // Добавляем токен авторизации

	// Выполняем запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса на создание face job: %v", err), ""
	}
	defer resp.Body.Close()

	// Обрабатываем ответ
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ошибка создания face job, код: %d, ответ: %s", resp.StatusCode, string(respBody)), ""
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("ошибка парсинга ответа JSON: %v", err), ""
	}

	jobID, ok := result["id"].(string)
	if !ok || jobID == "" {
		return fmt.Errorf("не удалось получить ID новой задачи, ответ: %s", string(respBody)), ""
	}

	log.Printf("Задача Circle Job успешно создана с ID: %s", jobID)
	return nil, jobID
}

// Функция для создания Circle Job
func createCircleJob(bot *tgbotapi.BotAPI, userID, inputMediaFileID string) (error, string) {
	// file dir creation
	userDir := fmt.Sprintf("data/%s", userID)
	err := os.MkdirAll(userDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("не удалось создать директорию пользователя: %v", err), ""
	}

	// file download
	inputMediaPath := fmt.Sprintf("%s/%s.mp4", userDir, inputMediaFileID)
	err = downloadTelegramFile(bot, inputMediaFileID, inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось скачать видеофайл: %v", err), ""
	}

	// file check
	inputMediaFile, err := os.Open(inputMediaPath)
	if err != nil {
		return fmt.Errorf("не удалось открыть видеофайл: %v", err), ""
	}
	defer inputMediaFile.Close()

	// Check size
	// needed for testing. will be removed.
	fileInfo, err := inputMediaFile.Stat()
	if err != nil {
		return fmt.Errorf("не удалось получить информацию о видеофайле: %v", err), ""
	}
	if fileInfo.Size() > 50*1024*1024 {
		return fmt.Errorf("размер видео превышает 50 МБ"), ""
	}

	// Создаем body для multipart/form-data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Добавляем метаданные (например, владелец и статус)
	_ = writer.WriteField("owner", userID)
	_ = writer.WriteField("status", "queued") // Статус задачи по умолчанию

	// Добавляем файлы в request
	mediaPart, err := writer.CreateFormFile("input_media", fileInfo.Name())
	if err != nil {
		return fmt.Errorf("не удалось создать часть для видеофайла: %v", err), ""
	}
	_, err = io.Copy(mediaPart, inputMediaFile)
	if err != nil {
		return fmt.Errorf("не удалось загрузить видеофайл: %v", err), ""
	}

	// Закрываем writer, чтобы завершить формирование multipart
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("не удалось завершить формирование multipart: %v", err), ""
	}

	// Формируем запрос
	createJobURL := fmt.Sprintf("%s/api/collections/circle_jobs/records", pocketBaseUrl)
	req, err := http.NewRequest("POST", createJobURL, body)
	if err != nil {
		return fmt.Errorf("не удалось создать HTTP-запрос для создания задачи: %v", err), ""
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authToken)) // Добавляем токен авторизации

	// Выполняем запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка выполнения запроса на создание circle job: %v", err), ""
	}
	defer resp.Body.Close()

	// Обрабатываем ответ
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ошибка создания face job, код: %d, ответ: %s", resp.StatusCode, string(respBody)), ""
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("ошибка парсинга ответа JSON: %v", err), ""
	}

	jobID, ok := result["id"].(string)
	if !ok || jobID == "" {
		return fmt.Errorf("не удалось получить ID новой задачи, ответ: %s", string(respBody)), ""
	}

	log.Printf("Задача Circle Job успешно создана с ID: %s", jobID)
	return nil, jobID
}

func getUserInfo(tgUserID int) (map[string]interface{}, error) {
	// Поиск пользователя в PocketBase по tgid
	searchURL := fmt.Sprintf("%s/api/collections/users/records?filter=tgid=%d", pocketBaseUrl, tgUserID)
	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при запросе пользователя: %v", err)
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return nil, fmt.Errorf("Ошибка разбора ответа при получении пользователя: %v", err)
	}

	if items, ok := searchResult["items"].([]interface{}); ok && len(items) > 0 {
		if user, ok := items[0].(map[string]interface{}); ok {
			return user, nil
		}
	}

	return nil, fmt.Errorf("Пользователь с Telegram ID %d не найден", tgUserID)
}

func getActiveJobs(userID, collection string) ([]map[string]interface{}, error) {
	filter := fmt.Sprintf("owner=\"%s\" && status!=\"completed\"", userID)
	encodedFilter := url.QueryEscape(filter) // Кодируем фильтр для передачи в URL

	searchURL := fmt.Sprintf("%s/api/collections/%s/records?filter=%s", pocketBaseUrl, collection, encodedFilter)

	resp, err := sendAuthorizedRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Ошибка при запросе задач: %v", err)
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(resp, &searchResult); err != nil {
		return nil, fmt.Errorf("Ошибка разбора ответа при получении задач: %v, ответ: %s", err, string(resp))
	}

	if items, ok := searchResult["items"].([]interface{}); ok {
		jobs := make([]map[string]interface{}, len(items))
		for i, item := range items {
			if job, ok := item.(map[string]interface{}); ok {
				jobs[i] = job
			}
		}
		return jobs, nil
	}

	return nil, nil
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
