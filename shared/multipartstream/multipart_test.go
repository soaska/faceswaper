package multipartstream

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestDoStreamsMultipleFilesAndFields(t *testing.T) {
	first := writeTempFile(t, "first")
	second := writeTempFile(t, "second")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.ContentLength > 0 {
			t.Fatalf("ContentLength = %d, want streaming body", request.ContentLength)
		}
		mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("Content-Type = %q, %v", mediaType, err)
		}
		reader := multipart.NewReader(request.Body, parameters["boundary"])
		values := make(map[string]string)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			values[part.FormName()] = string(body)
		}
		if values["owner"] != "user1" || values["source"] != "first" || values["target"] != "second" {
			t.Fatalf("multipart values = %#v", values)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&emptyReader{}), Header: make(http.Header)}, nil
	})}

	response, err := Do(
		context.Background(),
		client,
		http.MethodPost,
		"http://service.test/upload",
		nil,
		map[string]string{"owner": "user1"},
		[]File{{Field: "source", Path: first}, {Field: "target", Path: second}},
	)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	response.Body.Close()
}

type emptyReader struct{}

func (*emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }
func (*emptyReader) Close() error               { return nil }

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "multipart-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}
