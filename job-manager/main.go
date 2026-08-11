package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Version info set at build time.
var (
	GitCommit  = "unknown"
	GitMessage = "unknown"
)

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
	if err := ensureTaskBalance(task, circlePrice); err != nil {
		failTask("circle_jobs", task, err)
		return
	}

	if err := runWithHeartbeat(ctx, "circle_jobs", task, func() error {
		return processCircleTask(task)
	}); err != nil {
		failTask("circle_jobs", task, err)
		return
	}
	if err := chargeTaskCoins(task, "circle", "circle_charge", circlePrice); err != nil {
		failTask("circle_jobs", task, err)
		return
	}

	if err := updateClaimedStatus("circle_jobs", task.ID, statusSending); err != nil {
		refundTaskCoins(task, "circle", "circle_refund", circlePrice)
		failTask("circle_jobs", task, err)
		return
	}

	if err := notifyCircleOwner(task); err != nil {
		refundTaskCoins(task, "circle", "circle_refund", circlePrice)
		failTask("circle_jobs", task, err)
		return
	}

	if err := updateClaimedStatus("circle_jobs", task.ID, statusCompleted); err != nil {
		log.Printf("Кружок по задаче %s отправлен, но статус completed не сохранён: %v", task.ID, err)
		return
	}

	if _, err := applyUserOperation(userOperation{
		UserID:       task.Owner,
		JobID:        task.ID,
		Kind:         "circle_complete",
		OperationKey: completionOperationKey(task, "circle"),
		CircleDelta:  1,
	}); err != nil {
		log.Printf("Кружок по задаче %s отправлен, но circle_count не обновлён: %v", task.ID, err)
	}

	log.Printf("Задача создания кружка %s успешно обработана", task.ID)
}

func handleFaceSwapTask(ctx context.Context, task *Task) {
	isPhoto := isPhotoTask(task)
	basePrice := videoBasePrice
	if isPhoto {
		basePrice = photoFaceSwapPrice
	}

	if err := ensureTaskBalance(task, basePrice); err != nil {
		failTask("face_jobs", task, err)
		return
	}

	var duration int
	var workers int
	processErr := runWithHeartbeat(ctx, "face_jobs", task, func() error {
		var err error
		duration, workers, err = processFaceSwapTask(task)
		return err
	})
	if processErr != nil {
		failTask("face_jobs", task, processErr)
		return
	}

	totalPrice := calculateFaceSwapPrice(isPhoto, duration, workers)
	if err := updateTaskDurationPriceAndThreads(task.ID, duration, totalPrice, workers); err != nil {
		log.Printf("Не удалось сохранить цену задачи %s: %v", task.ID, err)
	}
	if err := chargeTaskCoins(task, "face", "face_charge", totalPrice); err != nil {
		failTask("face_jobs", task, err)
		return
	}

	if err := updateClaimedStatus("face_jobs", task.ID, statusSending); err != nil {
		refundTaskCoins(task, "face", "face_refund", totalPrice)
		failTask("face_jobs", task, err)
		return
	}

	outputExtension := ".mp4"
	if isPhoto {
		outputExtension = ".jpg"
	}
	outputPath := filepath.Join(jobCacheDirectory(), task.ID+"_output"+outputExtension)
	var sendErr error
	if isPhoto {
		sendErr = sendPhotoToUser(task.Owner, outputPath)
	} else {
		sendErr = sendVideoToUser(task.Owner, outputPath)
	}
	if sendErr != nil {
		refundTaskCoins(task, "face", "face_refund", totalPrice)
		failTask("face_jobs", task, sendErr)
		return
	}

	if err := updateClaimedStatus("face_jobs", task.ID, statusCompleted); err != nil {
		log.Printf("Результат задачи %s отправлен, но статус completed не сохранён: %v", task.ID, err)
		return
	}

	if _, err := applyUserOperation(userOperation{
		UserID:       task.Owner,
		JobID:        task.ID,
		Kind:         "face_complete",
		OperationKey: completionOperationKey(task, "face"),
		FaceDelta:    1,
	}); err != nil {
		log.Printf("Результат задачи %s отправлен, но face_replace_count не обновлён: %v", task.ID, err)
	}

	log.Printf(
		"Задача замены лиц %s успешно обработана за %d секунд (%d потоков), списано %d монет",
		task.ID,
		duration,
		workers,
		totalPrice,
	)
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

func chargeTaskCoins(task *Task, scope, kind string, amount int) error {
	_, err := applyUserOperation(userOperation{
		UserID:       task.Owner,
		JobID:        task.ID,
		Kind:         kind,
		OperationKey: billingOperationKey(task, scope, "charge"),
		CoinsDelta:   -amount,
	})
	if err != nil {
		return fmt.Errorf("операция с балансом не выполнена: %v", err)
	}
	return nil
}

func refundTaskCoins(task *Task, scope, kind string, amount int) {
	if amount <= 0 {
		return
	}
	_, err := applyUserOperation(userOperation{
		UserID:       task.Owner,
		JobID:        task.ID,
		Kind:         kind,
		OperationKey: billingOperationKey(task, scope, "refund"),
		CoinsDelta:   amount,
	})
	if err != nil {
		log.Printf("Не удалось вернуть %d монет по задаче %s: %v", amount, task.ID, err)
	}
}

func failTask(collection string, task *Task, cause error) {
	log.Printf("Задача %s завершилась ошибкой: %v", task.ID, cause)
	if err := updateClaimedStatus(collection, task.ID, taskErrorStatus(cause)); err != nil {
		log.Printf("Не удалось сохранить ошибку задачи %s: %v", task.ID, err)
	}
	if err := sendErrorNotification(task.Owner, task.ID); err != nil {
		log.Printf("Не удалось уведомить пользователя об ошибке задачи %s: %v", task.ID, err)
	}
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

func cleanupTempFiles() {
	cacheDir := jobCacheDirectory()
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return
	}

	files, err := filepath.Glob(filepath.Join(cacheDir, "*"))
	if err != nil {
		log.Printf("Ошибка поиска файлов кэша: %v", err)
		return
	}

	for _, file := range files {
		if info, err := os.Stat(file); err == nil && time.Since(info.ModTime()) > time.Hour {
			if err := os.Remove(file); err != nil {
				log.Printf("Не удалось удалить старый файл кэша %s: %v", file, err)
			} else {
				log.Printf("Удалён старый файл кэша: %s", file)
			}
		}
	}
}

func initializeServices() error {
	for retries := 0; retries < 5; retries++ {
		if err := authenticatePocketBase(); err == nil {
			log.Println("Авторизация PocketBase успешна")
			return nil
		} else {
			log.Printf("Ошибка авторизации PocketBase (попытка %d/5): %v", retries+1, err)
		}
		time.Sleep(time.Duration(retries+1) * 5 * time.Second)
	}
	return fmt.Errorf("не удалось авторизоваться после 5 попыток")
}

func main() {
	commitShort := GitCommit
	if len(GitCommit) > 8 {
		commitShort = GitCommit[:8]
	}
	log.Println("🚀 Job Manager запущен")
	log.Printf("📦 Версия: %s — %s", commitShort, GitMessage)

	BOT_TOKEN, _, BOT_ENDPOINT, FaceSwapComponent_URL = LoadEnvironment()
	workerID = initializeWorkerID()
	log.Printf("Воркер: %s", workerID)

	cleanupTempFiles()
	if err := initializeServices(); err != nil {
		log.Fatalf("Ошибка инициализации сервисов: %v", err)
	}

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
