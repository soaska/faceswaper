package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

// Обработка задачи создания кружка
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

	err = uploadOutputMedia(task.ID, outputFilePath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки кружка в бд: %v", err)
	}

	return nil
}

// Обработка задачи замены лиц
func processFaceSwapTask(task *Task) error {
	if task.InputMedia == "" || task.SourceImage == "" {
		return fmt.Errorf("задача с ID %s не содержит ссылок на input_media или source_image", task.ID)
	}

	cacheDir := "cache"
	err := os.MkdirAll(cacheDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("ошибка создания кэша: %v", err)
	}

	// Скачиваем исходные файлы
	videoPath := filepath.Join(cacheDir, fmt.Sprintf("%s_input.mp4", task.ID))
	imagePath := filepath.Join(cacheDir, fmt.Sprintf("%s_source.jpg", task.ID))
	outputPath := filepath.Join(cacheDir, fmt.Sprintf("%s_output.mp4", task.ID))

	videoUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.InputMedia)
	imageUrl := fmt.Sprintf("%s/api/files/face_jobs/%s/%s", pocketBaseUrl, task.ID, task.SourceImage)

	err = downloadFile(videoUrl, videoPath)
	if err != nil {
		return fmt.Errorf("ошибка скачивания видео: %v", err)
	}

	err = downloadFile(imageUrl, imagePath)
	if err != nil {
		return fmt.Errorf("ошибка скачивания изображения: %v", err)
	}

	// Обрабатываем через FaceFusion
	err = processFaceFusion(imagePath, videoPath, outputPath)
	if err != nil {
		return fmt.Errorf("ошибка обработки FaceFusion: %v", err)
	}

	// Загружаем результат обратно
	err = uploadOutputMedia(task.ID, outputPath)
	if err != nil {
		return fmt.Errorf("ошибка загрузки результата в бд: %v", err)
	}

	return nil
}

// отправляем файлы в FaceFusion и получаем результат
func processFaceFusion(sourceImage, targetVideo, outputPath string) error {
	// Открываем файлы
	imageFile, err := os.Open(sourceImage)
	if err != nil {
		return fmt.Errorf("ошибка открытия исходного изображения: %v", err)
	}
	defer imageFile.Close()

	videoFile, err := os.Open(targetVideo)
	if err != nil {
		return fmt.Errorf("ошибка открытия целевого видео: %v", err)
	}
	defer videoFile.Close()

	// Создаем multipart запрос
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	imagePart, err := writer.CreateFormFile("source_image", filepath.Base(sourceImage))
	if err != nil {
		return fmt.Errorf("ошибка создания части изображения: %v", err)
	}
	_, err = io.Copy(imagePart, imageFile)
	if err != nil {
		return fmt.Errorf("ошибка копирования изображения: %v", err)
	}

	videoPart, err := writer.CreateFormFile("target_video", filepath.Base(targetVideo))
	if err != nil {
		return fmt.Errorf("ошибка создания части видео: %v", err)
	}
	_, err = io.Copy(videoPart, videoFile)
	if err != nil {
		return fmt.Errorf("ошибка копирования видео: %v", err)
	}

	err = writer.Close()
	if err != nil {
		return fmt.Errorf("ошибка завершения multipart: %v", err)
	}

	// Отправляем запрос
	req, err := http.NewRequest("POST", FACEFUSION_URL+"/swap", body)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса к FaceFusion: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка отправки запроса к FaceFusion: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ошибка FaceFusion, статус %d: %s", resp.StatusCode, string(body))
	}

	// Сохраняем результат
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("ошибка создания файла результата: %v", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("ошибка сохранения результата: %v", err)
	}

	return nil
}

// Обработка файла кружка
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
