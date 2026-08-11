package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const heartbeatFailureLimit = 3

var heartbeatInterval = time.Minute
var heartbeatJobFunc = heartbeatJob
var settlementRetryBaseDelay = 250 * time.Millisecond

var workerID string

type claimResponse struct {
	Task *Task `json:"task"`
}

type settlementRequest struct {
	Collection string `json:"collection"`
	TaskID     string `json:"task_id"`
	WorkerID   string `json:"worker_id"`
	Action     string `json:"action"`
	Error      string `json:"error,omitempty"`
	Price      int    `json:"price,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Threads    int    `json:"threads,omitempty"`
}

type settlementResult struct {
	OK       bool `json:"ok"`
	Already  bool `json:"already"`
	Refunded bool `json:"refunded"`
	Price    int  `json:"price"`
	Balance  int  `json:"balance"`
}

func initializeWorkerID() string {
	if configured := strings.TrimSpace(os.Getenv("WORKER_ID")); configured != "" {
		return sanitizeWorkerID(configured)
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "job-manager"
	}
	return sanitizeWorkerID(fmt.Sprintf("%s-%d", hostname, os.Getpid()))
}

func sanitizeWorkerID(value string) string {
	invalid := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	value = invalid.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		value = "job-manager"
	}
	if len(value) > 100 {
		value = value[:100]
	}
	return value
}

func claimJob(collection string) (*Task, error) {
	payload, err := json.Marshal(map[string]string{
		"collection": collection,
		"worker_id":  workerID,
	})
	if err != nil {
		return nil, fmt.Errorf("ошибка сериализации запроса на получение задачи: %v", err)
	}

	body, err := sendAuthorizedRequest("POST", pocketBaseUrl+"/api/faceswaper/jobs/claim", payload)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения задачи: %v", err)
	}

	var response claimResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("ошибка разбора ответа с задачей: %v", err)
	}
	return response.Task, nil
}

func heartbeatJob(collection, taskID string) error {
	return sendJobCommand("/api/faceswaper/jobs/heartbeat", map[string]string{
		"collection": collection,
		"task_id":    taskID,
		"worker_id":  workerID,
	})
}

func sendJobCommand(path string, data map[string]string) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("ошибка сериализации команды задачи: %v", err)
	}
	if _, err := sendAuthorizedRequest("POST", pocketBaseUrl+path, payload); err != nil {
		return err
	}
	return nil
}

func settleJob(request settlementRequest) (settlementResult, error) {
	request.WorkerID = workerID
	payload, err := json.Marshal(request)
	if err != nil {
		return settlementResult{}, fmt.Errorf("ошибка сериализации расчёта задачи: %v", err)
	}

	var body []byte
	for attempt := 1; attempt <= 3; attempt++ {
		body, err = sendAuthorizedRequest(
			"POST",
			pocketBaseUrl+"/api/faceswaper/jobs/settle",
			payload,
		)
		if err == nil {
			break
		}
		var statusErr *pocketBaseStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 {
			break
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * settlementRetryBaseDelay)
		}
	}
	if err != nil {
		return settlementResult{}, fmt.Errorf("расчёт задачи не выполнен после трёх попыток: %w", err)
	}

	var result settlementResult
	if err := json.Unmarshal(body, &result); err != nil {
		return settlementResult{}, fmt.Errorf("ошибка разбора расчёта задачи: %v", err)
	}
	if !result.OK {
		return settlementResult{}, fmt.Errorf("PocketBase не подтвердил расчёт задачи")
	}
	return result, nil
}

type leaseUncertainError struct {
	cause error
}

func (err *leaseUncertainError) Error() string {
	return fmt.Sprintf("lease задачи не подтверждён: %v", err.cause)
}

func runWithHeartbeat(
	ctx context.Context,
	collection string,
	task *Task,
	process func(context.Context) error,
) error {
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	leaseErrors := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-processCtx.Done():
				return
			case <-ticker.C:
				if err := heartbeatJobFunc(collection, task.ID); err != nil {
					failures++
					log.Printf("Не удалось продлить обработку задачи %s: %v", task.ID, err)
					if failures >= heartbeatFailureLimit {
						leaseErrors <- &leaseUncertainError{cause: err}
						cancel()
						return
					}
				} else {
					failures = 0
				}
			}
		}
	}()

	err := process(processCtx)
	cancel()
	wg.Wait()
	select {
	case leaseErr := <-leaseErrors:
		return leaseErr
	default:
	}
	return err
}

func taskErrorStatus(err error) string {
	return statusError + ": " + taskErrorMessage(err)
}

func taskErrorMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	const maxErrorLength = 450
	runes := []rune(message)
	if len(runes) > maxErrorLength {
		message = string(runes[:maxErrorLength]) + "…"
	}
	return message
}
