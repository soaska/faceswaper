package main

import (
	"context"
	"encoding/json"
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

var workerID string

type claimResponse struct {
	Task *Task `json:"task"`
}

type userOperation struct {
	UserID       string `json:"user_id"`
	JobID        string `json:"job_id"`
	Kind         string `json:"kind"`
	OperationKey string `json:"operation_key"`
	CoinsDelta   int    `json:"coins_delta,omitempty"`
	CircleDelta  int    `json:"circle_delta,omitempty"`
	FaceDelta    int    `json:"face_delta,omitempty"`
}

type userOperationResult struct {
	Applied bool `json:"applied"`
	Balance int  `json:"balance"`
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

func updateClaimedStatus(collection, taskID, status string) error {
	return sendJobCommand("/api/faceswaper/jobs/status", map[string]string{
		"collection": collection,
		"task_id":    taskID,
		"worker_id":  workerID,
		"status":     status,
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

func applyUserOperation(operation userOperation) (userOperationResult, error) {
	payload, err := json.Marshal(operation)
	if err != nil {
		return userOperationResult{}, fmt.Errorf("ошибка сериализации операции пользователя: %v", err)
	}

	var body []byte
	for attempt := 1; attempt <= 3; attempt++ {
		body, err = sendAuthorizedRequest(
			"POST",
			pocketBaseUrl+"/api/faceswaper/users/apply-operation",
			payload,
		)
		if err == nil {
			break
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
		}
	}
	if err != nil {
		return userOperationResult{}, fmt.Errorf("операция не выполнена после трёх попыток: %v", err)
	}

	var result userOperationResult
	if err := json.Unmarshal(body, &result); err != nil {
		return userOperationResult{}, fmt.Errorf("ошибка разбора операции пользователя: %v", err)
	}
	return result, nil
}

func billingOperationKey(task *Task, scope, action string) string {
	return fmt.Sprintf("%s:%s:%s", scope, task.ID, action)
}

func completionOperationKey(task *Task, scope string) string {
	return scope + ":" + task.ID + ":complete"
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
	message := strings.TrimSpace(err.Error())
	const maxErrorLength = 450
	runes := []rune(message)
	if len(runes) > maxErrorLength {
		message = string(runes[:maxErrorLength]) + "…"
	}
	return statusError + ": " + message
}
