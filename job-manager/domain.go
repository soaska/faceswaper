package main

import "strings"

const (
	statusQueued     = "queued"
	statusProcessing = "processing"
	statusSending    = "sending"
	statusCompleted  = "completed"
	statusError      = "error"

	circlePrice        = 1
	photoFaceSwapPrice = 1
	videoBasePrice     = 2
	videoMaxPrice      = 30
)

// calculateFaceSwapPrice returns the full price of a completed face swap.
// Video processing is billed by actual worker-seconds and never exceeds the
// product price cap.
func calculateFaceSwapPrice(isPhoto bool, durationSeconds, workers int) int {
	if isPhoto {
		return photoFaceSwapPrice
	}

	if durationSeconds < 0 {
		durationSeconds = 0
	}
	if workers < 1 {
		workers = 1
	}

	billableSeconds := durationSeconds * workers
	additionalPrice := (billableSeconds + 10) / 20
	totalPrice := videoBasePrice + additionalPrice
	if totalPrice > videoMaxPrice {
		return videoMaxPrice
	}

	return totalPrice
}

// canAfford prevents processing from knowingly taking a user below zero.
func canAfford(balance, price int) bool {
	return price >= 0 && balance >= price
}

// isAllowedStatusTransition documents the normal task lifecycle. Returning a
// task to queued is reserved for the lease recovery path and is therefore not
// a normal status transition.
func isAllowedStatusTransition(from, to string) bool {
	if strings.HasPrefix(to, statusError+":") {
		return from == statusQueued || from == statusProcessing || from == statusSending
	}

	switch from {
	case statusQueued:
		return to == statusProcessing
	case statusProcessing:
		return to == statusSending
	case statusSending:
		return to == statusCompleted
	default:
		return false
	}
}
