package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sharedcleanup "github.com/soaska/faceswaper/shared/tempcleanup"
)

// Version info set at build time.
var GitCommit = "unknown"

// Task contains the PocketBase fields used by the worker.
type Task struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	InputMedia  string `json:"input_media"`
	SourceImage string `json:"input_face"`
	OutputMedia string `json:"output_media"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
}

func processCircleJobs(ctx context.Context) {
	processJobs(ctx, "circle_jobs", handleCircleTask)
}

func processFaceSwapJobs(ctx context.Context) {
	processJobs(ctx, "face_jobs", handleFaceSwapTask)
}

func processJobs(ctx context.Context, collection string, handler func(context.Context, *Task)) {
	for {
		select {
		case <-ctx.Done():
			log.Printf("Процессор %s завершает работу...", collection)
			return
		default:
		}

		task, err := claimJob(collection)
		if err != nil {
			log.Printf("Ошибка при получении задачи из %s: %v", collection, err)
			if !waitForNextAttempt(ctx, 3*time.Second) {
				return
			}
			continue
		}
		if task == nil {
			if !waitForNextAttempt(ctx, 5*time.Second) {
				return
			}
			continue
		}

		log.Printf("Задача %s получена воркером %s, попытка %d", task.ID, workerID, task.Attempts)
		handler(ctx, task)
	}
}

func handleCircleTask(ctx context.Context, task *Task) {
	defer cleanupTaskFiles(task.ID)
	if err := runWithHeartbeat(ctx, "circle_jobs", task, func(taskCtx context.Context) error {
		return handleCircleTaskWithLease(taskCtx, task)
	}); err != nil {
		log.Printf("Обработка задачи %s прервана для безопасного повтора: %v", task.ID, err)
	}
}

func handleCircleTaskWithLease(ctx context.Context, task *Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureTaskBalance(task, circlePrice); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("circle_jobs", task, err)
	}

	if err := processCircleTask(ctx, task); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("circle_jobs", task, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	settlement, err := settleJob(settlementRequest{
		Collection: "circle_jobs",
		TaskID:     task.ID,
		Action:     "start_sending",
		Price:      circlePrice,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isRejectedSettlement(err) {
			return failTaskAtomically("circle_jobs", task, err)
		}
		return fmt.Errorf("не удалось подтвердить списание и отправку задачи: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := notifyCircleOwner(ctx, task); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("circle_jobs", task, err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := settleJob(settlementRequest{
		Collection: "circle_jobs",
		TaskID:     task.ID,
		Action:     "complete",
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("кружок отправлен, но атомарное завершение задачи не подтверждено: %w", err)
	}

	log.Printf("Задача создания кружка %s успешно обработана, списано %d монет", task.ID, settlement.Price)
	return nil
}

func handleFaceSwapTask(ctx context.Context, task *Task) {
	defer cleanupTaskFiles(task.ID)
	if err := runWithHeartbeat(ctx, "face_jobs", task, func(taskCtx context.Context) error {
		return handleFaceSwapTaskWithLease(taskCtx, task)
	}); err != nil {
		log.Printf("Обработка задачи %s прервана для безопасного повтора: %v", task.ID, err)
	}
}

func handleFaceSwapTaskWithLease(ctx context.Context, task *Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	isPhoto := isPhotoTask(task)
	basePrice := videoBasePrice
	if isPhoto {
		basePrice = photoFaceSwapPrice
	}

	if err := ensureTaskBalance(task, basePrice); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("face_jobs", task, err)
	}

	var duration int
	var workers int
	duration, workers, processErr := processFaceSwapTask(ctx, task)
	if processErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("face_jobs", task, processErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	totalPrice := calculateFaceSwapPrice(isPhoto, duration, workers)
	settlement, err := settleJob(settlementRequest{
		Collection: "face_jobs",
		TaskID:     task.ID,
		Action:     "start_sending",
		Price:      totalPrice,
		Duration:   duration,
		Threads:    workers,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isRejectedSettlement(err) {
			return failTaskAtomically("face_jobs", task, err)
		}
		return fmt.Errorf("не удалось подтвердить списание и отправку задачи: %w", err)
	}

	outputExtension := ".mp4"
	if isPhoto {
		outputExtension = ".jpg"
	}
	outputPath := filepath.Join(jobCacheDirectory(), task.ID+"_output"+outputExtension)
	if err := ctx.Err(); err != nil {
		return err
	}
	var sendErr error
	if isPhoto {
		sendErr = sendPhotoToUser(ctx, task.Owner, outputPath)
	} else {
		sendErr = sendVideoToUser(ctx, task.Owner, outputPath)
	}
	if sendErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failTaskAtomically("face_jobs", task, sendErr)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := settleJob(settlementRequest{
		Collection: "face_jobs",
		TaskID:     task.ID,
		Action:     "complete",
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("результат отправлен, но атомарное завершение задачи не подтверждено: %w", err)
	}

	log.Printf(
		"Задача замены лиц %s успешно обработана за %d секунд (%d потоков), списано %d монет",
		task.ID,
		duration,
		workers,
		settlement.Price,
	)
	return nil
}

func isPhotoTask(task *Task) bool {
	switch strings.ToLower(filepath.Ext(task.InputMedia)) {
	case ".jpg", ".jpeg", ".png":
		return true
	default:
		return false
	}
}

func ensureTaskBalance(task *Task, minimum int) error {
	balance, err := getOwnerBalance(task.Owner)
	if err != nil {
		return fmt.Errorf("не удалось проверить баланс: %v", err)
	}
	if !canAfford(balance, minimum) {
		return fmt.Errorf("недостаточно монет. Требуется минимум %d, доступно %d", minimum, balance)
	}
	return nil
}

func failTaskAtomically(collection string, task *Task, cause error) error {
	log.Printf("Задача %s завершилась ошибкой: %v", task.ID, cause)
	if _, err := settleJob(settlementRequest{
		Collection: collection,
		TaskID:     task.ID,
		Action:     "fail",
		Error:      taskErrorMessage(cause),
	}); err != nil {
		return fmt.Errorf("не удалось атомарно завершить задачу %s с ошибкой: %w", task.ID, err)
	}
	if err := sendErrorNotification(task.Owner, task.ID); err != nil {
		log.Printf("Не удалось уведомить пользователя об ошибке задачи %s: %v", task.ID, err)
	}
	return nil
}

func isRejectedSettlement(err error) bool {
	var statusErr *pocketBaseStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == 400
}

func waitForNextAttempt(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func cleanupTempFilesOnStartup() {
	removed, err := sharedcleanup.RemoveContents(jobCacheDirectory())
	if err != nil {
		log.Printf("Не удалось очистить временные файлы менеджера задач при запуске: %v", err)
		return
	}
	if removed > 0 {
		log.Printf("Удалено временных файлов менеджера задач при запуске: %d", removed)
	}
}

func cleanupTaskFiles(taskID string) {
	if taskID == "" {
		return
	}
	cacheDir := jobCacheDirectory()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("Ошибка поиска временных файлов задачи %s: %v", taskID, err)
		return
	}
	prefix := taskID + "_"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		file := filepath.Join(cacheDir, entry.Name())
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			log.Printf("Не удалось удалить временный файл задачи %s: %v", taskID, err)
		}
	}
}

func initializeServices() error {
	for retries := 0; retries < 5; retries++ {
		if err := authenticatePocketBase(); err == nil {
			return nil
		} else {
			log.Printf("Ошибка авторизации PocketBase (попытка %d/5): %v", retries+1, err)
		}
		time.Sleep(time.Duration(retries+1) * 5 * time.Second)
	}
	return fmt.Errorf("не удалось авторизоваться после 5 попыток")
}

func main() {
	BOT_TOKEN, _, BOT_ENDPOINT, FaceSwapComponent_URL = LoadEnvironment()
	workerID = initializeWorkerID()

	cleanupTempFilesOnStartup()
	if err := initializeServices(); err != nil {
		log.Fatalf("Ошибка инициализации сервисов: %v", err)
	}

	commitShort := GitCommit
	if len(GitCommit) > 8 {
		commitShort = GitCommit[:8]
	}
	log.Println("Job Manager запущен")
	log.Printf("Версия: %s", commitShort)
	log.Printf("Воркер: %s", workerID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		log.Println("Получен сигнал остановки, корректно завершаем работу...")
		cancel()
	}()

	go processCircleJobs(ctx)
	go processFaceSwapJobs(ctx)

	<-ctx.Done()
	log.Println("Менеджер задач завершил работу")
}
