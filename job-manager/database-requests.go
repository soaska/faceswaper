package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	url := fmt.Sprintf("%s/api/collections/%s/records/%s", pocketBaseUrl, collection, taskID)
	responseBody, statusCode, err := uploadOutputMediaOnce(url, filePath)
	if err != nil {
		return err
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		refreshMutex.Lock()
		refreshErr := authenticatePocketBase()
		if refreshErr == nil {
			responseBody, statusCode, err = uploadOutputMediaOnce(url, filePath)
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

func uploadOutputMediaOnce(url, filePath string) ([]byte, int, error) {
	headers := make(http.Header)
	if token := currentAuthToken(); token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	resp, err := doStreamingMultipartFileRequest(
		mediaHTTPClient,
		http.MethodPatch,
		url,
		headers,
		nil,
		"output_media",
		filePath,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("ошибка загрузки файла: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка чтения ответа загрузки: %v", err)
	}
	return responseBody, resp.StatusCode, nil
}

func getOwnerTGID(ownerID string) (string, error) {
	ownerData, err := getOwnerData(ownerID)
	if err != nil {
		return "", err
	}
	if ownerData.TGID == 0 {
		return "", fmt.Errorf("Telegram ID владельца %s не найден", ownerID)
	}
	return strconv.FormatInt(ownerData.TGID, 10), nil
}

func getOwnerBalance(ownerID string) (int, error) {
	ownerData, err := getOwnerData(ownerID)
	if err != nil {
		return 0, err
	}
	return ownerData.Coins, nil
}

type ownerData struct {
	TGID  int64 `json:"tgid"`
	Coins int   `json:"coins"`
}

func getOwnerData(ownerID string) (ownerData, error) {
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, ownerID),
		nil,
	)
	if err != nil {
		return ownerData{}, fmt.Errorf("ошибка получения данных о владельце: %v", err)
	}

	var result ownerData
	if err := json.Unmarshal(body, &result); err != nil {
		return ownerData{}, fmt.Errorf("ошибка разбора данных о владельце: %v", err)
	}
	return result, nil
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
