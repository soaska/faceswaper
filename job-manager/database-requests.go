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

func authenticatePocketBase() error {
	authData, err := json.Marshal(map[string]string{
		"identity": email,
		"password": password,
	})
	if err != nil {
		return fmt.Errorf("ошибка сериализации данных авторизации: %v", err)
	}

	resp, err := apiHTTPClient.Post(
		pocketBaseUrl+"/api/admins/auth-with-password",
		"application/json",
		bytes.NewReader(authData),
	)
	if err != nil {
		return fmt.Errorf("не удалось отправить запрос на авторизацию: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа авторизации: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("авторизация не удалась, код %d: %s", resp.StatusCode, limitedBody(body))
	}

	var authResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &authResponse); err != nil {
		return fmt.Errorf("ошибка разбора ответа авторизации: %v", err)
	}
	if authResponse.Token == "" {
		return fmt.Errorf("PocketBase не вернул токен")
	}

	setAuthToken(authResponse.Token)
	log.Println("PocketBase: авторизация прошла успешно")
	return nil
}

// uploadOutputMedia stores the result but deliberately leaves task status
// unchanged. A task becomes completed only after Telegram confirms delivery.
func uploadOutputMedia(collection, taskID, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	filePart, err := writer.CreateFormFile("output_media", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("ошибка добавления файла в запрос: %v", err)
	}
	if _, err := io.Copy(filePart, file); err != nil {
		return fmt.Errorf("ошибка чтения файла результата: %v", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("ошибка завершения multipart-запроса: %v", err)
	}

	url := fmt.Sprintf("%s/api/collections/%s/records/%s", pocketBaseUrl, collection, taskID)
	contentType := writer.FormDataContentType()
	responseBody, statusCode, err := doMultipartAuthorizedRequest(http.MethodPatch, url, contentType, body.Bytes())
	if err != nil {
		return err
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		refreshMutex.Lock()
		refreshErr := authenticatePocketBase()
		if refreshErr == nil {
			responseBody, statusCode, err = doMultipartAuthorizedRequest(
				http.MethodPatch,
				url,
				contentType,
				body.Bytes(),
			)
		}
		refreshMutex.Unlock()
		if refreshErr != nil {
			return fmt.Errorf("ошибка обновления авторизации PocketBase: %v", refreshErr)
		}
		if err != nil {
			return err
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("ошибка загрузки файла, код %d: %s", statusCode, limitedBody(responseBody))
	}
	return nil
}

func doMultipartAuthorizedRequest(method, url, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка создания запроса загрузки: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token := currentAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка загрузки файла: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка чтения ответа загрузки: %v", err)
	}
	return responseBody, resp.StatusCode, nil
}

func getOwnerTGID(ownerID string) (string, error) {
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, ownerID),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("ошибка получения данных о владельце: %v", err)
	}

	var ownerData struct {
		TGID int `json:"tgid"`
	}
	if err := json.Unmarshal(body, &ownerData); err != nil {
		return "", fmt.Errorf("ошибка разбора данных о владельце: %v", err)
	}
	if ownerData.TGID == 0 {
		return "", fmt.Errorf("Telegram ID владельца %s не найден", ownerID)
	}
	return strconv.Itoa(ownerData.TGID), nil
}

func updateTaskDurationPriceAndThreads(taskID string, duration, price, threads int) error {
	payload, err := json.Marshal(map[string]int{
		"duration": duration,
		"price":    price,
		"threads":  threads,
	})
	if err != nil {
		return fmt.Errorf("ошибка сериализации данных задачи: %v", err)
	}
	_, err = sendAuthorizedRequest(
		http.MethodPatch,
		fmt.Sprintf("%s/api/collections/face_jobs/records/%s", pocketBaseUrl, taskID),
		payload,
	)
	if err != nil {
		return fmt.Errorf("ошибка обновления данных задачи: %v", err)
	}
	return nil
}
