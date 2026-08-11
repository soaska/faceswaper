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
	"strings"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

type UserRecord struct {
	ID               string `json:"id"`
	TGID             int64  `json:"tgid"`
	Username         string `json:"username"`
	Coins            int    `json:"coins"`
	CircleCount      int    `json:"circle_count"`
	FaceReplaceCount int    `json:"face_replace_count"`
	PendingFaceFile  string `json:"pending_face_file_id"`
	PendingFaceDate  string `json:"pending_face_updated"`
}

type JobRecord struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Created  string `json:"created"`
	Updated  string `json:"updated"`
	Duration int    `json:"duration"`
	Price    int    `json:"price"`
}

type recordList[T any] struct {
	Items []T `json:"items"`
}

func authenticatePocketBase() error {
	payload, err := json.Marshal(map[string]string{
		"identity": email,
		"password": password,
	})
	if err != nil {
		return fmt.Errorf("ошибка сериализации данных авторизации: %v", err)
	}
	resp, err := apiHTTPClient.Post(
		pocketBaseUrl+"/api/admins/auth-with-password",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("ошибка запроса авторизации PocketBase: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа авторизации: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("авторизация не удалась, код %d: %s", resp.StatusCode, limitedResponse(body))
	}

	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("ошибка разбора ответа авторизации: %v", err)
	}
	if response.Token == "" {
		return fmt.Errorf("PocketBase не вернул токен")
	}
	setAuthToken(response.Token)
	log.Println("PocketBase: авторизация прошла успешно")
	return nil
}

func getOrCreateUser(tgUserID int64, tgUsername string) (string, error) {
	user, err := findUser(tgUserID)
	if err != nil {
		return "", err
	}
	if user != nil {
		if user.Username != tgUsername {
			if err := updateUsername(user.ID, tgUsername); err != nil {
				log.Printf("Не удалось обновить username пользователя %d: %v", tgUserID, err)
			}
		}
		return user.ID, nil
	}

	// Keep the original main data contract explicit. PocketBase owns record IDs
	// and the persistent session fields remain empty until a face is received.
	payload, err := json.Marshal(map[string]interface{}{
		"tgid":               tgUserID,
		"username":           tgUsername,
		"coins":              200,
		"circle_count":       0,
		"face_replace_count": 0,
	})
	if err != nil {
		return "", fmt.Errorf("ошибка сериализации пользователя: %v", err)
	}
	body, err := sendAuthorizedRequest(
		http.MethodPost,
		pocketBaseUrl+"/api/collections/users/records",
		payload,
	)
	if err != nil {
		// Another bot instance may have created the same Telegram user.
		if existing, findErr := findUser(tgUserID); findErr == nil && existing != nil {
			return existing.ID, nil
		}
		return "", fmt.Errorf("ошибка создания пользователя: %v", err)
	}

	var created UserRecord
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("ошибка разбора созданного пользователя: %v", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("PocketBase не вернул ID созданного пользователя")
	}
	return created.ID, nil
}

func findUser(tgUserID int64) (*UserRecord, error) {
	query := url.Values{}
	query.Set("filter", fmt.Sprintf("tgid=%d", tgUserID))
	query.Set("perPage", "1")
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		pocketBaseUrl+"/api/collections/users/records?"+query.Encode(),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ошибка поиска пользователя: %v", err)
	}
	var result recordList[UserRecord]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ошибка разбора пользователя: %v", err)
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	return &result.Items[0], nil
}

func updateUsername(userID, username string) error {
	payload, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	_, err = sendAuthorizedRequest(
		http.MethodPatch,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID),
		payload,
	)
	return err
}

func getPendingFace(userID string) (string, error) {
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID),
		nil,
	)
	if err != nil {
		return "", err
	}
	var user UserRecord
	if err := json.Unmarshal(body, &user); err != nil {
		return "", fmt.Errorf("ошибка разбора сессии пользователя: %v", err)
	}
	if user.PendingFaceFile == "" || user.PendingFaceDate == "" {
		return "", nil
	}
	updated, err := parsePocketBaseTime(user.PendingFaceDate)
	if err != nil || time.Since(updated) >= sessionTTL {
		if clearErr := clearPendingFace(userID); clearErr != nil {
			log.Printf("Не удалось очистить просроченную сессию пользователя %s: %v", userID, clearErr)
		}
		return "", nil
	}
	return user.PendingFaceFile, nil
}

func savePendingFace(userID, fileID string) error {
	payload, err := json.Marshal(map[string]string{
		"pending_face_file_id": fileID,
		"pending_face_updated": time.Now().UTC().Format("2006-01-02 15:04:05.000Z"),
	})
	if err != nil {
		return err
	}
	_, err = sendAuthorizedRequest(
		http.MethodPatch,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID),
		payload,
	)
	return err
}

func clearPendingFace(userID string) error {
	payload, err := json.Marshal(map[string]string{
		"pending_face_file_id": "",
		"pending_face_updated": "",
	})
	if err != nil {
		return err
	}
	_, err = sendAuthorizedRequest(
		http.MethodPatch,
		fmt.Sprintf("%s/api/collections/users/records/%s", pocketBaseUrl, userID),
		payload,
	)
	return err
}

func parsePocketBaseTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000Z"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("неизвестный формат времени %q", value)
}

func createFaceJob(bot *tgbotapi.BotAPI, userID, inputMediaFileID, inputFaceFileID, requestKey string) (string, error) {
	inputMediaPath, err := getTelegramFile(bot, inputMediaFileID)
	if err != nil {
		return "", fmt.Errorf("не удалось получить медиафайл: %v", err)
	}
	defer removeDownloadedTelegramFile(inputMediaPath)

	inputFacePath, err := getTelegramFile(bot, inputFaceFileID)
	if err != nil {
		return "", fmt.Errorf("не удалось получить файл лица: %v", err)
	}
	defer removeDownloadedTelegramFile(inputFacePath)

	return createJobRecord("face_jobs", userID, requestKey, []uploadFile{
		{Field: "input_media", Path: inputMediaPath},
		{Field: "input_face", Path: inputFacePath},
	})
}

func createCircleJob(bot *tgbotapi.BotAPI, userID, inputMediaFileID, requestKey string) (string, error) {
	inputMediaPath, err := getTelegramFile(bot, inputMediaFileID)
	if err != nil {
		return "", fmt.Errorf("не удалось получить видеофайл: %v", err)
	}
	defer removeDownloadedTelegramFile(inputMediaPath)
	return createJobRecord("circle_jobs", userID, requestKey, []uploadFile{
		{Field: "input_media", Path: inputMediaPath},
	})
}

type uploadFile struct {
	Field string
	Path  string
}

func createJobRecord(collection, userID, requestKey string, files []uploadFile) (string, error) {
	for _, upload := range files {
		info, err := os.Stat(upload.Path)
		if err != nil {
			return "", fmt.Errorf("не удалось получить информацию о файле %s: %v", upload.Field, err)
		}
		if info.Size() <= 0 {
			return "", fmt.Errorf("файл %s пуст", upload.Field)
		}
		if info.Size() > 500*1024*1024 {
			return "", fmt.Errorf("размер файла %s превышает 500 МБ", upload.Field)
		}
	}

	requestURL := fmt.Sprintf("%s/api/collections/%s/records", pocketBaseUrl, collection)
	body, statusCode, err := uploadJobOnce(requestURL, userID, requestKey, files)
	if err != nil {
		if existingID := findJobByRequestKey(collection, requestKey); existingID != "" {
			return existingID, nil
		}
		return "", err
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		refreshMutex.Lock()
		refreshErr := authenticatePocketBase()
		if refreshErr == nil {
			body, statusCode, err = uploadJobOnce(requestURL, userID, requestKey, files)
		}
		refreshMutex.Unlock()
		if refreshErr != nil {
			return "", fmt.Errorf("ошибка обновления авторизации PocketBase: %v", refreshErr)
		}
		if err != nil {
			return "", err
		}
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		if existingID := findJobByRequestKey(collection, requestKey); existingID != "" {
			return existingID, nil
		}
		return "", fmt.Errorf("PocketBase вернул код %d при создании задачи: %s", statusCode, limitedResponse(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("ошибка разбора созданной задачи: %v", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("PocketBase не вернул ID созданной задачи")
	}
	log.Printf("Задача %s успешно создана с ID %s", collection, result.ID)
	return result.ID, nil
}

func uploadJobOnce(requestURL, userID, requestKey string, files []uploadFile) ([]byte, int, error) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	writeResult := make(chan error, 1)
	go func() {
		err := writeJobMultipart(multipartWriter, userID, requestKey, files)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
		writeResult <- err
	}()

	req, err := http.NewRequest(http.MethodPost, requestURL, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return nil, 0, fmt.Errorf("ошибка создания запроса задачи: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token := currentAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := mediaClient.Do(req)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return nil, 0, fmt.Errorf("ошибка загрузки задачи: %v", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	writeErr := <-writeResult
	if writeErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка формирования задачи: %v", writeErr)
	}
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка чтения ответа создания задачи: %v", readErr)
	}
	return body, resp.StatusCode, nil
}

func writeJobMultipart(writer *multipart.Writer, userID, requestKey string, files []uploadFile) error {
	if err := writer.WriteField("owner", userID); err != nil {
		return err
	}
	if err := writer.WriteField("status", "queued"); err != nil {
		return err
	}
	if err := writer.WriteField("request_key", requestKey); err != nil {
		return err
	}
	for _, upload := range files {
		file, err := os.Open(upload.Path)
		if err != nil {
			return err
		}
		part, err := writer.CreateFormFile(upload.Field, filepath.Base(upload.Path))
		if err == nil {
			_, err = io.Copy(part, file)
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func findJobByRequestKey(collection, requestKey string) string {
	if requestKey == "" {
		return ""
	}
	query := url.Values{}
	query.Set("filter", fmt.Sprintf("request_key=\"%s\"", requestKey))
	query.Set("perPage", "1")
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/collections/%s/records?%s", pocketBaseUrl, collection, query.Encode()),
		nil,
	)
	if err != nil {
		return ""
	}
	var result struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Items) == 0 {
		return ""
	}
	return result.Items[0].ID
}

func findExistingJobByRequestKey(requestKey string) string {
	for _, collection := range []string{"circle_jobs", "face_jobs"} {
		if jobID := findJobByRequestKey(collection, requestKey); jobID != "" {
			return jobID
		}
	}
	return ""
}

func removeDownloadedTelegramFile(path string) {
	cacheDir, err := filepath.Abs(botCacheDirectory())
	if err != nil {
		return
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	if strings.HasPrefix(absolutePath, cacheDir+string(os.PathSeparator)) {
		if err := os.Remove(absolutePath); err != nil && !os.IsNotExist(err) {
			log.Printf("Не удалось удалить временный файл %s: %v", absolutePath, err)
		}
	}
}

func getUserInfo(tgUserID int64) (UserRecord, error) {
	user, err := findUser(tgUserID)
	if err != nil {
		return UserRecord{}, err
	}
	if user == nil {
		return UserRecord{}, fmt.Errorf("пользователь с Telegram ID %d не найден", tgUserID)
	}
	return *user, nil
}

func getActiveJobs(userID, collection string) ([]JobRecord, error) {
	if collection != "circle_jobs" && collection != "face_jobs" {
		return nil, fmt.Errorf("неизвестная коллекция задач %q", collection)
	}
	query := url.Values{}
	query.Set("filter", fmt.Sprintf("owner=\"%s\" && status!=\"completed\"", userID))
	query.Set("sort", "-created")
	query.Set("perPage", "100")
	body, err := sendAuthorizedRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/collections/%s/records?%s", pocketBaseUrl, collection, query.Encode()),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения задач: %v", err)
	}
	var result recordList[JobRecord]
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ошибка разбора задач: %v", err)
	}
	return result.Items, nil
}

func updateStatus(collection, taskID, status string) error {
	if collection != "circle_jobs" && collection != "face_jobs" {
		return fmt.Errorf("неизвестная коллекция задач %q", collection)
	}
	payload, err := json.Marshal(map[string]string{"status": status})
	if err != nil {
		return fmt.Errorf("ошибка сериализации статуса: %v", err)
	}
	_, err = sendAuthorizedRequest(
		http.MethodPatch,
		fmt.Sprintf("%s/api/collections/%s/records/%s", pocketBaseUrl, collection, taskID),
		payload,
	)
	if err != nil {
		return fmt.Errorf("ошибка обновления статуса задачи: %v", err)
	}
	return nil
}
