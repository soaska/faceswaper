package main

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGetOrCreateUserPreservesMainDefaults(t *testing.T) {
	var created UserRecord
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"items":[]}`)),
				Header:     make(http.Header),
			}, nil
		}
		if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
			t.Fatalf("decode created user: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"user1"}`)),
			Header:     make(http.Header),
		}, nil
	})

	oldClient := apiHTTPClient
	oldURL := pocketBaseUrl
	apiHTTPClient = &http.Client{Transport: transport}
	pocketBaseUrl = "http://pocketbase.test"
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseUrl = oldURL
	}()

	userID, err := getOrCreateUser(123456, "tester")
	if err != nil {
		t.Fatalf("getOrCreateUser() error = %v", err)
	}
	if userID != "user1" || requests != 2 {
		t.Fatalf("getOrCreateUser() = %q after %d requests", userID, requests)
	}
	if created.TGID != 123456 || created.Username != "tester" {
		t.Fatalf("identity fields = %#v", created)
	}
	if created.Coins != 200 || created.CircleCount != 0 || created.FaceReplaceCount != 0 {
		t.Fatalf("main-compatible defaults = %#v", created)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestSendAuthorizedRequestRejectsServerError(t *testing.T) {
	oldClient := apiHTTPClient
	apiHTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("unavailable")),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() { apiHTTPClient = oldClient }()

	_, err := sendAuthorizedRequest(http.MethodGet, "http://pocketbase.test/fail", nil)
	if err == nil || !strings.Contains(err.Error(), "код 500") {
		t.Fatalf("sendAuthorizedRequest() error = %v, want code 500", err)
	}
}

func TestUploadJobWritesFieldsAndFiles(t *testing.T) {
	tempFile, err := os.CreateTemp(t.TempDir(), "photo-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tempFile.WriteString("image-data"); err != nil {
		t.Fatal(err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatal(err)
	}

	oldClient := mediaClient
	mediaClient = &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" {
				t.Fatalf("invalid content type: %q, %v", mediaType, err)
			}
			reader := multipart.NewReader(request.Body, params["boundary"])
			fields := map[string]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read multipart: %v", err)
				}
				content, _ := io.ReadAll(part)
				fields[part.FormName()] = string(content)
			}
			if fields["owner"] != "user1" || fields["status"] != "queued" || fields["request_key"] != "telegram:42" || fields["input_face"] != "image-data" {
				t.Fatalf("unexpected multipart fields: %#v", fields)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"job1"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	defer func() { mediaClient = oldClient }()

	body, statusCode, err := uploadJobOnce(
		"http://pocketbase.test/jobs",
		"user1",
		"telegram:42",
		[]uploadFile{{Field: "input_face", Path: tempFile.Name()}},
	)
	if err != nil || statusCode != http.StatusOK || !strings.Contains(string(body), "job1") {
		t.Fatalf("uploadJobOnce() = %s, %d, %v", body, statusCode, err)
	}
}
