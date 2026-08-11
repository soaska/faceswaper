package multipartstream

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

type File struct {
	Field string
	Path  string
}

// Do streams fields and files directly from disk. A new call is required for
// every retry because the request body is backed by a non-rewindable pipe.
func Do(
	ctx context.Context,
	client *http.Client,
	method string,
	requestURL string,
	headers http.Header,
	fields map[string]string,
	files []File,
) (*http.Response, error) {
	reader, pipeWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(pipeWriter)
	contentType := multipartWriter.FormDataContentType()
	writeResult := make(chan error, 1)

	go func() {
		err := writeBody(multipartWriter, fields, files)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = pipeWriter.CloseWithError(err)
		writeResult <- err
	}()

	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
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
		return nil, fmt.Errorf("ошибка потоковой отправки файлов: %v", writeErr)
	}
	return response, nil
}

func writeBody(writer *multipart.Writer, fields map[string]string, files []File) error {
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return err
		}
	}
	for _, upload := range files {
		file, err := os.Open(upload.Path)
		if err != nil {
			return err
		}
		part, writeErr := writer.CreateFormFile(upload.Field, filepath.Base(upload.Path))
		if writeErr == nil {
			_, writeErr = io.Copy(part, file)
		}
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
