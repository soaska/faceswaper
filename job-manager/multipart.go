package main

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// doStreamingMultipartFileRequest sends one file without buffering it in RAM.
// A new request must be created for every retry because the pipe is not
// rewindable.
func doStreamingMultipartFileRequest(
	client *http.Client,
	method string,
	requestURL string,
	headers http.Header,
	fields map[string]string,
	fileField string,
	filePath string,
) (*http.Response, error) {
	reader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	contentType := multipartWriter.FormDataContentType()
	writeResult := make(chan error, 1)

	go func() {
		err := writeMultipartFile(multipartWriter, fields, fileField, filePath)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = pipeWriter.CloseWithError(err)
		writeResult <- err
	}()

	request, err := http.NewRequest(method, requestURL, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return nil, fmt.Errorf("ошибка создания потокового запроса: %v", err)
	}
	request.Header.Set("Content-Type", contentType)
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}

	response, err := client.Do(request)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return nil, err
	}
	if writeErr := <-writeResult; writeErr != nil {
		_ = response.Body.Close()
		return nil, fmt.Errorf("ошибка потоковой отправки файла: %v", writeErr)
	}
	return response, nil
}

func writeMultipartFile(
	writer *multipart.Writer,
	fields map[string]string,
	fileField string,
	filePath string,
) error {
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return err
		}
	}

	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	part, createErr := writer.CreateFormFile(fileField, filepath.Base(filePath))
	if createErr == nil {
		_, createErr = io.Copy(part, file)
	}
	closeErr := file.Close()
	if createErr != nil {
		return createErr
	}
	return closeErr
}
