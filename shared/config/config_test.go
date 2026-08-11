package config

import "testing"

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_BUILD", "1")
	t.Setenv("TELEGRAM_APITOKEN", " token ")
	t.Setenv("POCKETBASE_URL", " http://pocketbase:8080/ ")
	t.Setenv("POCKETBASE_LOGIN", " admin@example.test ")
	t.Setenv("POCKETBASE_PASSWORD", "secret")
}

func TestLoadCommonPreservesMainDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("TELEGRAM_API", "")
	t.Setenv("BOT_DEBUG", "true")

	result, err := LoadCommon()
	if err != nil {
		t.Fatalf("LoadCommon() error = %v", err)
	}
	if result.TelegramToken != "token" || !result.TelegramDebug {
		t.Fatalf("Telegram config = %+v", result)
	}
	if result.TelegramAPI != defaultTelegramAPI {
		t.Fatalf("Telegram API = %q", result.TelegramAPI)
	}
	if result.PocketBaseURL != "http://pocketbase:8080" || result.PocketBaseID != "admin@example.test" {
		t.Fatalf("PocketBase config = %+v", result)
	}
}

func TestLoadCommonUsesMainValidationOrder(t *testing.T) {
	t.Setenv("DOCKER_BUILD", "1")
	t.Setenv("TELEGRAM_APITOKEN", "")
	t.Setenv("POCKETBASE_URL", "")

	_, err := LoadCommon()
	if err == nil || err.Error() != "TELEGRAM_APITOKEN не задан" {
		t.Fatalf("LoadCommon() error = %v", err)
	}
}
