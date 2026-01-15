package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Process circle creation task
func processCircleTask(task *Task) error {
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

	err = uploadOutputMedia("circle_jobs", task.ID, outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки кружка в бд: %v", err)
	}

	return nil
}

// Check face swap server health
func checkFaceSwapHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", FaceSwapComponent_URL+"/health", nil)
	if err != nil {
		return fmt.Errorf("ошибка подготовки запроса проверки состояния: %v", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка проверки состояния сервера замены лиц: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер замены лиц недоступен (статус %d)", resp.StatusCode)
	}

	return nil
}

// Process face swap task - determines media type and routes to appropriate handler
func processFaceSwapTask(task *Task) (int, int, error) {
	if task.InputMedia == "" || task.SourceImage == "" {
		return 0, 0, fmt.Errorf("задача с ID %s не содержит ссылок на input_media или source_image", task.ID)
	}

	// Check face swap server health
	if err := checkFaceSwapHealth(); err != nil {
		return 0, 0, fmt.Errorf("сервер замены лиц занят: %v", err)
	}

	// Determine media type by extension
	ext := filepath.Ext(task.InputMedia)
	isPhoto := ext == ".jpg" || ext == ".jpeg" || ext == ".png"

	if isPhoto {
		return processPhotoSwap(task)
	} else {
		return processVideoSwap(task)
	}
}

// Process video face swap - returns (realDuration, workerCount, error)
func processVideoSwap(task *Task) (int, int, error) {
	cacheDir := "cache"
	err := os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания кэша: %v", err)
	}

	// Download source files
	videoPath := filepath.Join(cacheDir, fmt.Sprintf("%s_input.mp4", task.ID))
	imagePath := filepath.Join(cacheDir, fmt.Sprintf("%s_source.jpg", task.ID))
	outputPath := filepath.Join(cacheDir, fmt.Sprintf("%s_output.mp4", task.ID))

	videoUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.InputMedia)
	imageUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.SourceImage)

	err = downloadFile(videoUrl, videoPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка скачивания видео: %v", err)
	}

	err = downloadFile(imageUrl, imagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка скачивания изображения: %v", err)
	}

	// Get both duration and worker count from face swap component
	realDuration, workerCount, err := processFaceSwapComponent(imagePath, videoPath, outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка обработки FaceSwapComponent: %v", err)
	}

	// Upload result back
	err = uploadOutputMedia("face_jobs", task.ID, outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка загрузки результата в бд: %v", err)
	}

	return realDuration, workerCount, nil
}

// Process photo face swap - returns (processingTime, workerCount, error)
func processPhotoSwap(task *Task) (int, int, error) {
	cacheDir := "cache"
	err := os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания кэша: %v", err)
	}

	// Download source files
	sourceImagePath := filepath.Join(cacheDir, fmt.Sprintf("%s_source.jpg", task.ID))
	targetImagePath := filepath.Join(cacheDir, fmt.Sprintf("%s_target.jpg", task.ID))
	outputPath := filepath.Join(cacheDir, fmt.Sprintf("%s_output.jpg", task.ID))

	sourceImageUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.SourceImage)
	targetImageUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.InputMedia)

	err = downloadFile(sourceImageUrl, sourceImagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка скачивания исходного изображения: %v", err)
	}

	err = downloadFile(targetImageUrl, targetImagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка скачивания целевого изображения: %v", err)
	}

	// Process photo swap
	processingTime, workerCount, err := processPhotoSwapComponent(sourceImagePath, targetImagePath, outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка обработки FaceSwapComponent (фото): %v", err)
	}

	// Upload result back
	err = uploadOutputMedia("face_jobs", task.ID, outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка загрузки результата в бд: %v", err)
	}

	return processingTime, workerCount, nil
}

// Face swap response structure
type FaceSwapResponse struct {
	VideoPath       string `json:"video_path"`
	DownloadURL     string `json:"download_url"`
	DurationSeconds int    `json:"duration_seconds"`
	Filename        string `json:"filename"`
	MediaType       string `json:"media_type"`
	SessionID       string `json:"session_id"`
	ProcessingTime  int    `json:"processing_time"`
	WorkersUsed     int    `json:"workers_used"`
	DeviceType      string `json:"device_type"`
}

// Photo swap response structure
type PhotoSwapResponse struct {
	ImagePath       string `json:"image_path"`
	DownloadURL     string `json:"download_url"`
	DurationSeconds int    `json:"duration_seconds"`
	Filename        string `json:"filename"`
	MediaType       string `json:"media_type"`
	SessionID       string `json:"session_id"`
	ProcessingTime  int    `json:"processing_time"`
	WorkersUsed     int    `json:"workers_used"`
	DeviceType      string `json:"device_type"`
}

// Send files to FaceSwapComponent and get result
func processFaceSwapComponent(sourceImage, targetVideo, outputPath string) (int, int, error) {
	// Open files
	imageFile, err := os.Open(sourceImage)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка открытия исходного изображения: %v", err)
	}
	defer imageFile.Close()

	videoFile, err := os.Open(targetVideo)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка открытия целевого видео: %v", err)
	}
	defer videoFile.Close()

	// Create multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	imagePart, err := writer.CreateFormFile("source_image", filepath.Base(sourceImage))
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания части изображения: %v", err)
	}
	_, err = io.Copy(imagePart, imageFile)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка копирования изображения: %v", err)
	}

	videoPart, err := writer.CreateFormFile("target_video", filepath.Base(targetVideo))
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания части видео: %v", err)
	}
	_, err = io.Copy(videoPart, videoFile)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка копирования видео: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	// Send request
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", FaceSwapComponent_URL+"/swap", body)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания запроса к FaceSwapComponent: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if FaceSwapComponentSecret != "" {
		req.Header.Set("X-API-KEY", FaceSwapComponentSecret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка отправки запроса к FaceSwapComponent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, 0, fmt.Errorf("ошибка чтения ответа при ошибке FaceSwapComponent: %v", err)
		}
		return 0, 0, fmt.Errorf("ошибка FaceSwapComponent, статус %d: %s", resp.StatusCode, string(body))
	}

	// Read JSON response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка чтения ответа: %v", err)
	}

	var swapResponse FaceSwapResponse
	err = json.Unmarshal(responseBody, &swapResponse)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка парсинга JSON ответа: %v", err)
	}

	log.Printf("FaceSwap обработка завершена: сессия %s, устройство %s, потоков %d, время %d сек",
		swapResponse.SessionID, swapResponse.DeviceType, swapResponse.WorkersUsed, swapResponse.ProcessingTime)

	downloadURL := swapResponse.DownloadURL
	if downloadURL == "" {
		downloadURL = swapResponse.VideoPath
	}
	if !strings.HasPrefix(downloadURL, "http") {
		downloadURL = strings.TrimRight(FaceSwapComponent_URL, "/") + "/" + strings.TrimLeft(downloadURL, "/")
	}

	if err := downloadFaceSwapResult(downloadURL, outputPath); err != nil {
		return 0, 0, err
	}

	// Validate result size
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка получения информации о файле: %v", err)
	}
	if fileInfo.Size() == 0 {
		return 0, 0, fmt.Errorf("получен пустой файл результата")
	}

	return swapResponse.DurationSeconds, swapResponse.WorkersUsed, nil
}

// Send files to FaceSwapComponent for photo processing
func processPhotoSwapComponent(sourceImage, targetImage, outputPath string) (int, int, error) {
	// Open files
	sourceImageFile, err := os.Open(sourceImage)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка открытия исходного изображения: %v", err)
	}
	defer sourceImageFile.Close()

	targetImageFile, err := os.Open(targetImage)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка открытия целевого изображения: %v", err)
	}
	defer targetImageFile.Close()

	// Create multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	sourceImagePart, err := writer.CreateFormFile("source_image", filepath.Base(sourceImage))
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания части исходного изображения: %v", err)
	}
	_, err = io.Copy(sourceImagePart, sourceImageFile)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка копирования исходного изображения: %v", err)
	}

	targetImagePart, err := writer.CreateFormFile("target_image", filepath.Base(targetImage))
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания части целевого изображения: %v", err)
	}
	_, err = io.Copy(targetImagePart, targetImageFile)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка копирования целевого изображения: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	// Send request
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", FaceSwapComponent_URL+"/swap-photo", body)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания запроса к FaceSwapComponent: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if FaceSwapComponentSecret != "" {
		req.Header.Set("X-API-KEY", FaceSwapComponentSecret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка отправки запроса к FaceSwapComponent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, 0, fmt.Errorf("ошибка чтения ответа при ошибке FaceSwapComponent: %v", err)
		}
		return 0, 0, fmt.Errorf("ошибка FaceSwapComponent, статус %d: %s", resp.StatusCode, string(body))
	}

	// Read JSON response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка чтения ответа: %v", err)
	}

	var swapResponse PhotoSwapResponse
	err = json.Unmarshal(responseBody, &swapResponse)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка парсинга JSON ответа: %v", err)
	}

	log.Printf("FaceSwap (фото) обработка завершена: сессия %s, устройство %s, потоков %d, время %d сек",
		swapResponse.SessionID, swapResponse.DeviceType, swapResponse.WorkersUsed, swapResponse.ProcessingTime)

	downloadURL := swapResponse.DownloadURL
	if downloadURL == "" {
		downloadURL = swapResponse.ImagePath
	}
	if !strings.HasPrefix(downloadURL, "http") {
		downloadURL = strings.TrimRight(FaceSwapComponent_URL, "/") + "/" + strings.TrimLeft(downloadURL, "/")
	}

	if err := downloadFaceSwapResult(downloadURL, outputPath); err != nil {
		return 0, 0, err
	}

	// Validate result size
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка получения информации о файле: %v", err)
	}
	if fileInfo.Size() == 0 {
		return 0, 0, fmt.Errorf("получен пустой файл результата")
	}

	return swapResponse.ProcessingTime, swapResponse.WorkersUsed, nil
}

// Process circle video file
func processVideo(inputPath, outputPath string) error {
	cmd := exec.Command(
		"ffmpeg",
		"-i", inputPath,
		"-vf", "crop=min(iw,ih):min(iw,ih):(iw-min(iw,ih))/2:(ih-min(iw,ih))/2,scale=512:512",
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

// Download face-swap result from component with auth and limits
func downloadFaceSwapResult(downloadURL string, outputPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("ошибка подготовки запроса для загрузки результата: %v", err)
	}
	if FaceSwapComponentSecret != "" {
		req.Header.Set("X-API-KEY", FaceSwapComponentSecret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка загрузки результата face-swap: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ошибка загрузки результата face-swap: статус %d, ответ %s", resp.StatusCode, string(body))
	}

	var maxResultSize int64 = 0 // 0 = unlimited (previous behavior)
	var limited io.Reader = resp.Body
	if maxResultSize > 0 {
		limited = io.LimitReader(resp.Body, maxResultSize+1)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("ошибка создания файла результата: %v", err)
	}
	defer outFile.Close()

	written, err := io.Copy(outFile, limited)
	if err != nil {
		return fmt.Errorf("ошибка записи результата: %v", err)
	}
	if maxResultSize > 0 && written > maxResultSize {
		return fmt.Errorf("результат превышает лимит %d байт", maxResultSize)
	}

	return nil
}
