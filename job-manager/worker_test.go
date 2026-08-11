package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func TestTaskErrorStatusTruncatesValidUTF8(t *testing.T) {
	status := taskErrorStatus(errors.New(strings.Repeat("я", 500)))
	if !utf8.ValidString(status) {
		t.Fatalf("taskErrorStatus() returned invalid UTF-8: %q", status)
	}
	if got, want := utf8.RuneCountInString(status), utf8.RuneCountInString("error: ")+450+1; got != want {
		t.Fatalf("taskErrorStatus() rune count = %d, want %d", got, want)
	}
	if !strings.HasSuffix(status, "…") {
		t.Fatalf("taskErrorStatus() = %q, want ellipsis", status)
	}
}

func TestRunWithHeartbeatCancelsAfterRepeatedLeaseFailures(t *testing.T) {
	oldInterval := heartbeatInterval
	oldHeartbeat := heartbeatJobFunc
	heartbeatInterval = time.Millisecond
	var calls atomic.Int32
	heartbeatJobFunc = func(_, _ string) error {
		calls.Add(1)
		return errors.New("PocketBase недоступен")
	}
	defer func() {
		heartbeatInterval = oldInterval
		heartbeatJobFunc = oldHeartbeat
	}()

	err := runWithHeartbeat(
		context.Background(),
		"face_jobs",
		&Task{ID: "job1"},
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)
	var leaseErr *leaseUncertainError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("runWithHeartbeat() error = %v, want leaseUncertainError", err)
	}
	if got := calls.Load(); got != heartbeatFailureLimit {
		t.Fatalf("heartbeat calls = %d, want %d", got, heartbeatFailureLimit)
	}
}

func TestRunWithHeartbeatLeavesShutdownForLeaseRecovery(t *testing.T) {
	oldInterval := heartbeatInterval
	heartbeatInterval = time.Hour
	defer func() { heartbeatInterval = oldInterval }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runWithHeartbeat(ctx, "circle_jobs", &Task{ID: "job1"}, func(ctx context.Context) error {
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runWithHeartbeat() error = %v, want context.Canceled", err)
	}
	var leaseErr *leaseUncertainError
	if errors.As(err, &leaseErr) {
		t.Fatalf("shutdown was misclassified as lease failure: %v", err)
	}
}

func TestTaskHandlersDoNotStartWorkAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &Task{ID: "job1", Owner: "user1"}
	if err := handleCircleTaskWithLease(ctx, task); !errors.Is(err, context.Canceled) {
		t.Fatalf("handleCircleTaskWithLease() error = %v, want context.Canceled", err)
	}
	if err := handleFaceSwapTaskWithLease(ctx, task); !errors.Is(err, context.Canceled) {
		t.Fatalf("handleFaceSwapTaskWithLease() error = %v, want context.Canceled", err)
	}
}

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestSanitizeWorkerID(t *testing.T) {
	if got, want := sanitizeWorkerID(" gpu worker/01 "), "gpu-worker-01"; got != want {
		t.Fatalf("sanitizeWorkerID() = %q, want %q", got, want)
	}
	if got := sanitizeWorkerID(strings.Repeat("a", 101)); len(got) != 100 {
		t.Fatalf("worker ID length = %d, want 100", len(got))
	}
}

func TestSettleJobRetriesIdenticalIdempotentRequest(t *testing.T) {
	oldClient := apiHTTPClient
	oldURL := pocketBaseUrl
	oldPocketBaseClient := pocketBaseClient
	oldWorkerID := workerID
	oldRetryDelay := settlementRetryBaseDelay
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseUrl = oldURL
		pocketBaseClient = oldPocketBaseClient
		workerID = oldWorkerID
		settlementRetryBaseDelay = oldRetryDelay
	}()

	pocketBaseUrl = "http://pocketbase.test"
	workerID = "worker-1"
	settlementRetryBaseDelay = 0
	var bodies [][]byte
	apiHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		status := http.StatusInternalServerError
		response := `{"message":"temporary"}`
		if len(bodies) == 2 {
			status = http.StatusOK
			response = `{"ok":true,"already":true,"price":3,"balance":197}`
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
		}, nil
	})}
	configurePocketBase(pocketBaseUrl, "admin", "secret", apiHTTPClient)

	result, err := settleJob(settlementRequest{
		Collection: "face_jobs",
		TaskID:     "job123",
		Action:     "start_sending",
		Price:      3,
		Duration:   12,
		Threads:    1,
	})
	if err != nil {
		t.Fatalf("settleJob() error = %v", err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("settlement retry bodies = %q, want two identical requests", bodies)
	}
	var request settlementRequest
	if err := json.Unmarshal(bodies[0], &request); err != nil {
		t.Fatal(err)
	}
	if request.WorkerID != "worker-1" || request.TaskID != "job123" {
		t.Fatalf("settlement request = %+v", request)
	}
	if !result.OK || !result.Already || result.Price != 3 || result.Balance != 197 {
		t.Fatalf("settleJob() = %+v", result)
	}
}

func TestSettleJobDoesNotRetryRejectedRequest(t *testing.T) {
	oldClient := apiHTTPClient
	oldURL := pocketBaseUrl
	oldPocketBaseClient := pocketBaseClient
	oldWorkerID := workerID
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseUrl = oldURL
		pocketBaseClient = oldPocketBaseClient
		workerID = oldWorkerID
	}()

	pocketBaseUrl = "http://pocketbase.test"
	workerID = "worker-1"
	var calls int
	apiHTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"message":"insufficient balance"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	configurePocketBase(pocketBaseUrl, "admin", "secret", apiHTTPClient)

	_, err := settleJob(settlementRequest{Collection: "circle_jobs", TaskID: "job123", Action: "start_sending", Price: 1})
	if err == nil || !isRejectedSettlement(err) {
		t.Fatalf("settleJob() error = %v, want rejected settlement", err)
	}
	if calls != 1 {
		t.Fatalf("settlement calls = %d, want 1", calls)
	}
}

func TestConfiguredFaceSwapURLPrefersMainStyleName(t *testing.T) {
	t.Setenv("FACE_SWAP_URL", " http://preferred:7860/ ")
	t.Setenv("FaceSwapComponent_URL", "http://legacy:7860")
	if got, want := configuredFaceSwapURL(), "http://preferred:7860"; got != want {
		t.Fatalf("configuredFaceSwapURL() = %q, want %q", got, want)
	}
}

func TestConfiguredFaceSwapURLSupportsLegacyName(t *testing.T) {
	t.Setenv("FACE_SWAP_URL", "")
	t.Setenv("FaceSwapComponent_URL", "http://legacy:7860/")
	if got, want := configuredFaceSwapURL(), "http://legacy:7860"; got != want {
		t.Fatalf("configuredFaceSwapURL() = %q, want %q", got, want)
	}
}

func TestCleanupTaskFilesOnlyRemovesRequestedTask(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("JOB_CACHE_DIR", cacheDir)
	requested := cacheDir + "/job1_output.mp4"
	other := cacheDir + "/job10_output.mp4"
	for _, path := range []string{requested, other} {
		if err := os.WriteFile(path, []byte("result"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupTaskFiles("job1")
	if _, err := os.Stat(requested); !os.IsNotExist(err) {
		t.Fatalf("requested task file still exists: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other task file was removed: %v", err)
	}
}

func TestSendAuthorizedRequestRejectsNonSuccessStatus(t *testing.T) {
	oldClient := apiHTTPClient
	oldPocketBaseClient := pocketBaseClient
	apiHTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("database unavailable")),
			Header:     make(http.Header),
		}, nil
	})}
	configurePocketBase("http://pocketbase.test", "admin", "secret", apiHTTPClient)
	defer func() {
		apiHTTPClient = oldClient
		pocketBaseClient = oldPocketBaseClient
	}()

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
	oldPocketBaseClient := pocketBaseClient
	configurePocketBase("http://pocketbase.test", "admin", "secret", apiHTTPClient)
	mediaHTTPClient = &http.Client{Transport: transport, Timeout: time.Second}
	defer func() {
		pocketBaseUrl = oldURL
		mediaHTTPClient = oldClient
		pocketBaseClient = oldPocketBaseClient
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

	if err := uploadOutputMedia(context.Background(), "circle_jobs", "task1", tempFile.Name()); err != nil {
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
