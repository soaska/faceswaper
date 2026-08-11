package main

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestFaceSwapStreamsHTTPResult(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.jpg")
	targetPath := filepath.Join(t.TempDir(), "target.mp4")
	outputPath := filepath.Join(t.TempDir(), "output.mp4")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldClient := mediaHTTPClient
	oldURL := FaceSwapComponent_URL
	oldKey := FaceSwapAPIKey
	FaceSwapComponent_URL = "http://face-swap.test"
	FaceSwapAPIKey = "secret-key"
	mediaHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-API-Key") != "secret-key" {
			t.Errorf("X-API-Key = %q", request.Header.Get("X-API-Key"))
		}
		mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("invalid multipart content type: %q, %v", mediaType, err)
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
		if fields["source_image"] != "source" || fields["target_video"] != "target" {
			t.Fatalf("multipart fields = %#v", fields)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("processed-video")),
			Header: http.Header{
				"Content-Type":          []string{"video/mp4"},
				"X-Processing-Duration": []string{"12"},
				"X-Workers-Used":        []string{"1"},
				"X-Session-ID":          []string{"session1"},
				"X-Device-Type":         []string{"nvidia"},
			},
		}, nil
	})}
	defer func() {
		mediaHTTPClient = oldClient
		FaceSwapComponent_URL = oldURL
		FaceSwapAPIKey = oldKey
	}()

	duration, workers, err := requestFaceSwap(
		context.Background(),
		"/swap",
		sourcePath,
		targetPath,
		"target_video",
		outputPath,
		"video/",
	)
	if err != nil {
		t.Fatalf("requestFaceSwap() error = %v", err)
	}
	if duration != 12 || workers != 1 {
		t.Fatalf("requestFaceSwap() = %d, %d", duration, workers)
	}
	result, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "processed-video" {
		t.Fatalf("output = %q", result)
	}
}
