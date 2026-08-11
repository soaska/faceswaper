package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseCommandMatchesOnlyExactCommand(t *testing.T) {
	tests := []struct {
		text    string
		command string
		args    []string
		ok      bool
	}{
		{text: "/start", command: "start", ok: true},
		{text: "/STATUS@NoKnAbThBo_Bot", command: "status", ok: true},
		{text: "/compress 80", command: "compress", args: []string{"80"}, ok: true},
		{text: "restart", ok: false},
		{text: "please help", ok: false},
		{text: "status report", ok: false},
	}

	for _, tt := range tests {
		command, args, ok := parseCommand(tt.text)
		if ok != tt.ok || command != tt.command || strings.Join(args, "|") != strings.Join(tt.args, "|") {
			t.Errorf("parseCommand(%q) = %q, %v, %v", tt.text, command, args, ok)
		}
	}
}

func TestSplitTelegramMessageRespectsLimit(t *testing.T) {
	message := strings.Repeat("строка с emoji 🌀\n", 100)
	chunks := splitTelegramMessage(message, 120)
	if len(chunks) < 2 {
		t.Fatalf("splitTelegramMessage() returned %d chunk", len(chunks))
	}
	if strings.Join(chunks, "") != message {
		t.Fatal("splitTelegramMessage() changed message content")
	}
	for _, chunk := range chunks {
		if length := utf8.RuneCountInString(chunk); length > 120 {
			t.Errorf("chunk length = %d, want <= 120", length)
		}
	}
}

func TestFormatStatusUsesTypedValuesAndHidesClearedErrors(t *testing.T) {
	status := formatStatus(
		UserRecord{TGID: 42, Username: "tester", Coins: 7, CircleCount: 2, FaceReplaceCount: 3},
		[]JobRecord{{ID: "hidden", Status: "err cleared: old"}},
		[]JobRecord{{ID: "active", Status: "processing", Duration: 10, Price: 4}},
	)
	for _, expected := range []string{"tester", "Монеты: 7", "У вас нет активных задач создания кружков", "active", "Цена: 4 монет"} {
		if !strings.Contains(status, expected) {
			t.Errorf("status does not contain %q: %s", expected, status)
		}
	}
	if strings.Contains(status, "hidden") || strings.Contains(status, "%!") {
		t.Fatalf("status contains invalid data: %s", status)
	}
}
