package main

import "testing"

func TestCalculateFaceSwapPrice(t *testing.T) {
	tests := []struct {
		name     string
		isPhoto  bool
		duration int
		workers  int
		want     int
	}{
		{name: "photo has a fixed price", isPhoto: true, duration: 999, workers: 8, want: 1},
		{name: "video includes base price", duration: 0, workers: 1, want: 2},
		{name: "video rounds worker seconds to nearest coin", duration: 10, workers: 1, want: 3},
		{name: "video uses actual workers", duration: 20, workers: 2, want: 4},
		{name: "invalid worker count falls back to one", duration: 20, workers: 0, want: 3},
		{name: "negative duration is treated as zero", duration: -20, workers: 4, want: 2},
		{name: "video price is capped", duration: 3600, workers: 8, want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calculateFaceSwapPrice(tt.isPhoto, tt.duration, tt.workers); got != tt.want {
				t.Fatalf("calculateFaceSwapPrice() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCanAffordNeverAllowsNegativeBalance(t *testing.T) {
	tests := []struct {
		balance int
		price   int
		want    bool
	}{
		{balance: 10, price: 10, want: true},
		{balance: 10, price: 11, want: false},
		{balance: 0, price: 1, want: false},
		{balance: 10, price: -1, want: false},
	}

	for _, tt := range tests {
		if got := canAfford(tt.balance, tt.price); got != tt.want {
			t.Errorf("canAfford(%d, %d) = %v, want %v", tt.balance, tt.price, got, tt.want)
		}
	}
}

func TestTaskStatusLifecycle(t *testing.T) {
	allowed := [][2]string{
		{statusQueued, statusProcessing},
		{statusProcessing, statusSending},
		{statusSending, statusCompleted},
		{statusQueued, "error: недостаточно монет"},
		{statusProcessing, "error: обработка не удалась"},
		{statusSending, "error: отправка не удалась"},
	}
	for _, transition := range allowed {
		if !isAllowedStatusTransition(transition[0], transition[1]) {
			t.Errorf("transition %q -> %q must be allowed", transition[0], transition[1])
		}
	}

	forbidden := [][2]string{
		{statusQueued, statusCompleted},
		{statusProcessing, statusCompleted},
		{statusCompleted, statusQueued},
		{statusCompleted, "error: поздняя ошибка"},
		{statusQueued, "broken"},
	}
	for _, transition := range forbidden {
		if isAllowedStatusTransition(transition[0], transition[1]) {
			t.Errorf("transition %q -> %q must be forbidden", transition[0], transition[1])
		}
	}
}
