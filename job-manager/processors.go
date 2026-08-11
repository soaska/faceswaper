package main

import (
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Process circle creation task
func processCircleTask(task *Task) error {
	if task.InputMedia == "" {
		return fmt.Errorf("задача с ID %s не содержит ссылки на input_media", task.ID)
	}

	cacheDir := jobCacheDirectory()
	err := os.MkdirAll(cacheDir, 0o755)
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
	resp, err := apiHTTPClient.Get(FaceSwapComponent_URL + "/health")
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
	cacheDir := jobCacheDirectory()
	err := os.MkdirAll(cacheDir, 0o755)
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
	cacheDir := jobCacheDirectory()
	err := os.MkdirAll(cacheDir, 0o755)
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

func processFaceSwapComponent(sourceImage, targetVideo, outputPath string) (int, int, error) {
	return requestFaceSwap(
		"/swap",
		sourceImage,
		targetVideo,
		"target_video",
		outputPath,
		"video/",
	)
}

func processPhotoSwapComponent(sourceImage, targetImage, outputPath string) (int, int, error) {
	return requestFaceSwap(
		"/swap-photo",
		sourceImage,
		targetImage,
		"target_image",
		outputPath,
		"image/",
	)
}

func requestFaceSwap(endpoint, sourcePath, targetPath, targetField, outputPath, expectedMedia string) (int, int, error) {
	reader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	contentType := multipartWriter.FormDataContentType()
	writeResult := make(chan error, 1)
	go func() {
		err := writeFaceSwapMultipart(multipartWriter, sourcePath, targetPath, targetField)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = pipeWriter.CloseWithError(err)
		writeResult <- err
	}()

	req, err := http.NewRequest(http.MethodPost, FaceSwapComponent_URL+endpoint, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return 0, 0, fmt.Errorf("ошибка создания запроса FaceSwap: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", FaceSwapAPIKey)

	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return 0, 0, fmt.Errorf("ошибка запроса FaceSwap: %v", err)
	}
	defer resp.Body.Close()
	writeErr := <-writeResult
	if writeErr != nil {
		return 0, 0, fmt.Errorf("ошибка загрузки файлов в FaceSwap: %v", writeErr)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return 0, 0, fmt.Errorf("FaceSwap вернул код %d: %s", resp.StatusCode, string(body))
	}
	if mediaType := resp.Header.Get("Content-Type"); !strings.HasPrefix(mediaType, expectedMedia) {
		return 0, 0, fmt.Errorf("FaceSwap вернул неожиданный Content-Type %q", mediaType)
	}

	partialPath := outputPath + ".partial"
	output, err := os.Create(partialPath)
	if err != nil {
		return 0, 0, fmt.Errorf("ошибка создания файла результата: %v", err)
	}
	written, copyErr := io.Copy(output, resp.Body)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(partialPath)
		return 0, 0, fmt.Errorf("ошибка скачивания результата FaceSwap: %v", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(partialPath)
		return 0, 0, fmt.Errorf("ошибка закрытия результата FaceSwap: %v", closeErr)
	}
	if written == 0 {
		_ = os.Remove(partialPath)
		return 0, 0, fmt.Errorf("FaceSwap вернул пустой файл")
	}
	if err := os.Rename(partialPath, outputPath); err != nil {
		_ = os.Remove(partialPath)
		return 0, 0, fmt.Errorf("ошибка сохранения результата FaceSwap: %v", err)
	}

	duration := positiveHeaderInt(resp.Header, "X-Processing-Duration", 1)
	workers := positiveHeaderInt(resp.Header, "X-Workers-Used", 1)
	log.Printf(
		"FaceSwap завершён: сессия %s, устройство %s, потоков %d, время %d сек",
		resp.Header.Get("X-Session-ID"),
		resp.Header.Get("X-Device-Type"),
		workers,
		duration,
	)
	return duration, workers, nil
}

func writeFaceSwapMultipart(writer *multipart.Writer, sourcePath, targetPath, targetField string) error {
	for _, file := range []struct {
		field string
		path  string
	}{
		{field: "source_image", path: sourcePath},
		{field: targetField, path: targetPath},
	} {
		input, err := os.Open(file.path)
		if err != nil {
			return err
		}
		part, err := writer.CreateFormFile(file.field, filepath.Base(file.path))
		if err == nil {
			_, err = io.Copy(part, input)
		}
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func positiveHeaderInt(header http.Header, name string, fallback int) int {
	value, err := strconv.Atoi(header.Get(name))
	if err != nil || value < 1 {
		return fallback
	}
	return value
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
