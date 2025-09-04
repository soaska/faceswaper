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
	"os/exec"
	"path/filepath"
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
	resp, err := http.Get(FaceSwapComponent_URL + "/health")
	if err != nil {
		return fmt.Errorf("ошибка проверки состояния сервера замены лиц: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер замены лиц недоступен (статус %d)", resp.StatusCode)
	}

	return nil
}

// Process face swap task - returns (realDuration, workerCount, error)
func processFaceSwapTask(task *Task) (int, int, error) {
	if task.InputMedia == "" || task.SourceImage == "" {
		return 0, 0, fmt.Errorf("задача с ID %s не содержит ссылок на input_media или source_image", task.ID)
	}

	// Check face swap server health
	if err := checkFaceSwapHealth(); err != nil {
		return 0, 0, fmt.Errorf("сервер замены лиц занят: %v", err)
	}

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

// Face swap response structure
type FaceSwapResponse struct {
	VideoPath       string `json:"video_path"`
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
	req, err := http.NewRequest("POST", FaceSwapComponent_URL+"/swap", body)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания запроса к FaceSwapComponent: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
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

	// Copy video file from temp path to our path
	sourceVideoFile, err := os.Open(swapResponse.VideoPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка открытия видео из ответа: %v", err)
	}
	defer sourceVideoFile.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания файла результата: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, sourceVideoFile)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка копирования результата: %v", err)
	}

	// Check that file is created and has size
	fileInfo, err := outFile.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка получения информации о файле: %v", err)
	}
	if fileInfo.Size() == 0 {
		return 0, 0, fmt.Errorf("получен пустой файл результата")
	}

	return swapResponse.DurationSeconds, swapResponse.WorkersUsed, nil
}

// Process circle video file
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
		return fmt.Errorf("ошибка ffmpeg: %v, вывод: %s", err, string(output))
	}

	return nil
}
