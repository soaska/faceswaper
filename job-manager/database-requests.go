package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/soaska/faceswaper/shared/multipartstream"
)

// uploadOutputMedia stores the result but deliberately leaves task status
// unchanged. A task becomes completed only after Telegram confirms delivery.
func uploadOutputMedia(ctx context.Context, collection, taskID, filePath string) error {
	url := fmt.Sprintf("%s/api/collections/%s/records/%s", pocketBaseUrl, collection, taskID)
	responseBody, statusCode, err := pocketBaseClient.DoAuthenticated(func(token string) ([]byte, int, error) {
		return uploadOutputMediaOnce(ctx, url, filePath, token)
	})
	if err != nil {
		return err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("ошибка загрузки файла, код %d: %s", statusCode, limitedBody(responseBody))
	}
	return nil
}

func uploadOutputMediaOnce(ctx context.Context, url, filePath, token string) ([]byte, int, error) {
	headers := make(http.Header)
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	resp, err := multipartstream.Do(
		ctx,
		mediaHTTPClient,
		http.MethodPatch,
		url,
		headers,
		nil,
		[]multipartstream.File{{Field: "output_media", Path: filePath}},
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
