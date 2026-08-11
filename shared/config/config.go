package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

const defaultTelegramAPI = "https://api.telegram.org"

type Common struct {
	TelegramToken string
	TelegramDebug bool
	TelegramAPI   string
	PocketBaseURL string
	PocketBaseID  string
	PocketBaseKey string
}

// LoadCommon preserves the environment names, validation order and public
// error messages used by the original main bot.
func LoadCommon() (Common, error) {
	if os.Getenv("DOCKER_BUILD") == "" {
		if err := godotenv.Load(); err != nil {
			return Common{}, fmt.Errorf("Не удалось загрузить .env")
		}
	}

	result := Common{
		TelegramToken: strings.TrimSpace(os.Getenv("TELEGRAM_APITOKEN")),
		TelegramDebug: os.Getenv("BOT_DEBUG") == "true",
		TelegramAPI:   strings.TrimRight(strings.TrimSpace(os.Getenv("TELEGRAM_API")), "/"),
		PocketBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("POCKETBASE_URL")), "/"),
		PocketBaseID:  strings.TrimSpace(os.Getenv("POCKETBASE_LOGIN")),
		PocketBaseKey: os.Getenv("POCKETBASE_PASSWORD"),
	}
	if result.TelegramToken == "" {
		return Common{}, fmt.Errorf("TELEGRAM_APITOKEN не задан")
	}
	if result.TelegramAPI == "" {
		result.TelegramAPI = defaultTelegramAPI
	}
	if result.PocketBaseURL == "" {
		return Common{}, fmt.Errorf("POCKETBASE_URL не задан")
	}
	if result.PocketBaseID == "" {
		return Common{}, fmt.Errorf("POCKETBASE_LOGIN не задан")
	}
	if result.PocketBaseKey == "" {
		return Common{}, fmt.Errorf("POCKETBASE_PASSWORD не задан")
	}
	return result, nil
}
