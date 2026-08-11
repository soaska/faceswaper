package main

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOperationKeysAreJobScoped(t *testing.T) {
	task := &Task{ID: "job123", Attempts: 2}
	if got, want := billingOperationKey(task, "face", "charge"), "face:job123:charge"; got != want {
		t.Fatalf("billingOperationKey() = %q, want %q", got, want)
	}
	if got, want := completionOperationKey(task, "face"), "face:job123:complete"; got != want {
		t.Fatalf("completionOperationKey() = %q, want %q", got, want)
	}
}

func TestSanitizeWorkerID(t *testing.T) {
	if got, want := sanitizeWorkerID(" gpu worker/01 "), "gpu-worker-01"; got != want {
		t.Fatalf("sanitizeWorkerID() = %q, want %q", got, want)
	}
	if got := sanitizeWorkerID(strings.Repeat("a", 101)); len(got) != 100 {
		t.Fatalf("worker ID length = %d, want 100", len(got))
	}
}

func TestSendAuthorizedRequestRejectsNonSuccessStatus(t *testing.T) {
	oldClient := apiHTTPClient
	apiHTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("database unavailable")),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() { apiHTTPClient = oldClient }()

	_, err := sendAuthorizedRequest(http.MethodGet, "http://pocketbase.test/fail", nil)
	if err == nil || !strings.Contains(err.Error(), "код 500") {
		t.Fatalf("sendAuthorizedRequest() error = %v, want status error", err)
	}
}

func TestUploadOutputMediaDoesNotCompleteTask(t *testing.T) {
	var statusField string
	var uploadedFile string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.ContentLength > 0 {
			t.Errorf("upload buffered %d bytes instead of streaming", r.ContentLength)
		}
		mediaType, params, err := mimeParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("invalid content type: %q, %v", mediaType, err)
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader("invalid multipart")),
				Header:     make(http.Header),
			}, nil
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read multipart: %v", err)
				break
			}
			content, _ := io.ReadAll(part)
			switch part.FormName() {
			case "status":
				statusField = string(content)
			case "output_media":
				uploadedFile = string(content)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"task1"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	oldURL := pocketBaseUrl
	oldClient := mediaHTTPClient
	pocketBaseUrl = "http://pocketbase.test"
	mediaHTTPClient = &http.Client{Transport: transport, Timeout: time.Second}
	defer func() {
		pocketBaseUrl = oldURL
		mediaHTTPClient = oldClient
	}()

	tempFile, err := os.CreateTemp(t.TempDir(), "result-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tempFile.WriteString("result"); err != nil {
		t.Fatal(err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatal(err)
	}

	if err := uploadOutputMedia("circle_jobs", "task1", tempFile.Name()); err != nil {
		t.Fatalf("uploadOutputMedia() error = %v", err)
	}
	if statusField != "" {
		t.Fatalf("upload changed status to %q", statusField)
	}
	if uploadedFile != "result" {
		t.Fatalf("uploaded file = %q, want result", uploadedFile)
	}
}

// Kept behind a small wrapper so the test remains readable while still using
// the standard library parser.
func mimeParseMediaType(value string) (string, map[string]string, error) {
	return mime.ParseMediaType(value)
}
