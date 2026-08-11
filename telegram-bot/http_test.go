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
	var createdFields map[string]interface{}
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
		if err := json.NewDecoder(request.Body).Decode(&createdFields); err != nil {
			t.Fatalf("decode created user: %v", err)
		}
		encoded, err := json.Marshal(createdFields)
		if err != nil {
			t.Fatalf("encode created user: %v", err)
		}
		if err := json.Unmarshal(encoded, &created); err != nil {
			t.Fatalf("decode typed created user: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"user1"}`)),
			Header:     make(http.Header),
		}, nil
	})

	oldClient := apiHTTPClient
	oldURL := pocketBaseUrl
	oldPocketBaseClient := pocketBaseClient
	apiHTTPClient = &http.Client{Transport: transport}
	configurePocketBase("http://pocketbase.test", "admin", "secret", apiHTTPClient)
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseUrl = oldURL
		pocketBaseClient = oldPocketBaseClient
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
	if len(createdFields) != 5 {
		t.Fatalf("main-compatible fields = %#v", createdFields)
	}
	for _, field := range []string{"tgid", "username", "coins", "circle_count", "face_replace_count"} {
		if _, ok := createdFields[field]; !ok {
			t.Errorf("main-compatible field %q is missing: %#v", field, createdFields)
		}
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
	oldPocketBaseClient := pocketBaseClient
	configurePocketBase("http://pocketbase.test", "admin", "secret", apiHTTPClient)
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseClient = oldPocketBaseClient
	}()

	_, err := sendAuthorizedRequest(http.MethodGet, "http://pocketbase.test/fail", nil)
	if err == nil || !strings.Contains(err.Error(), "код 500") {
		t.Fatalf("sendAuthorizedRequest() error = %v, want code 500", err)
	}
}

func TestPendingFacePersistenceIncludesRequestIdentity(t *testing.T) {
	var payloads []map[string]string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/auth-with-password") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"token":"test-token"}`)),
				Header:     make(http.Header),
			}, nil
		}
		if request.Method != http.MethodPatch || request.URL.Path != "/api/collections/users/records/user1" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode pending face payload: %v", err)
		}
		payloads = append(payloads, payload)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	oldClient := apiHTTPClient
	oldURL := pocketBaseUrl
	oldPocketBaseClient := pocketBaseClient
	apiHTTPClient = &http.Client{Transport: transport}
	configurePocketBase("http://pocketbase.test", "admin", "secret", apiHTTPClient)
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseUrl = oldURL
		pocketBaseClient = oldPocketBaseClient
	}()

	if err := savePendingFace("user1", "face-file", "telegram:42"); err != nil {
		t.Fatalf("savePendingFace() error = %v", err)
	}
	if err := clearPendingFace("user1"); err != nil {
		t.Fatalf("clearPendingFace() error = %v", err)
	}
	if len(payloads) != 2 {
		t.Fatalf("pending face writes = %d, want 2", len(payloads))
	}
	if payloads[0]["pending_face_file_id"] != "face-file" || payloads[0]["pending_face_request_key"] != "telegram:42" || payloads[0]["pending_face_updated"] == "" {
		t.Fatalf("save payload = %#v", payloads[0])
	}
	if payloads[1]["pending_face_file_id"] != "" || payloads[1]["pending_face_request_key"] != "" || payloads[1]["pending_face_updated"] != "" {
		t.Fatalf("clear payload = %#v", payloads[1])
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
		"",
	)
	if err != nil || statusCode != http.StatusOK || !strings.Contains(string(body), "job1") {
		t.Fatalf("uploadJobOnce() = %s, %d, %v", body, statusCode, err)
	}
}
