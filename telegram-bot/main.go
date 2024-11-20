package main

import (
	"log"
	"net/url"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"github.com/r--w/pocketbase"
)

func LoadEnvinment() (string, bool, string, string, string) {
	if os.Getenv("DOCKER_BUILD") == `` {
		err := godotenv.Load()
		if err != nil {
			log.Fatalf("Error loading .env file")
		}
	}

	// load telegram token
	bot_token := os.Getenv("TELEGRAM_APITOKEN")
	if bot_token == `` {
		log.Fatal("empty telegram api token loaded, check TELEGRAM_APITOKEN value")
	}

	// telegram bot debug mode
	var bot_debug bool
	if os.Getenv("BOT_DEBUG") == `true` {
		bot_debug = true
	} else {
		bot_debug = false
	}

	// pocketbase
	db_url := os.Getenv("POCKETBASE_URL")
	if bot_token == `` {
		log.Fatal("empty pocketbase url loaded, check POCKETBASE_URL value")
	} else {
		// validate db url
		_, err := url.ParseRequestURI(db_url)
		if err != nil {
			log.Fatal(err)
		}
	}
	db_login := os.Getenv("POCKETBASE_LOGIN")
	if bot_token == `` {
		log.Fatal("empty pocketbase login loaded. env is not correct or configuration is insecure")
	}

	db_password := os.Getenv("POCKETBASE_PASSWORD")
	if bot_token == `` {
		log.Fatal("empty pocketbase password loaded. env is not correct or configuration is insecure")
	}
	return bot_token, bot_debug, db_url, db_login, db_password
}

func main() {
	// load variables
	BOT_TOKEN, BOT_DEBUG, DB_URL, DB_LOGIN, DB_PASSWORD := LoadEnvinment()

	// pocketbase client
	client := pocketbase.NewClient(DB_URL, pocketbase.WithAdminEmailPassword(DB_LOGIN, DB_PASSWORD))
	response, err := client.List("posts_public", pocketbase.ParamsList{
		Page: 1, Size: 10, Sort: "-created", Filters: "field~'test'",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Print(response.TotalItems)

	// start the bot
	bot, err := tgbotapi.NewBotAPI(BOT_TOKEN)
	if err != nil {
		panic(err)
	} else {
		log.Printf("Authorized on account %s", bot.Self.UserName)
	}
	if BOT_DEBUG {
		bot.Debug = true
		log.Print("bot in DEBUG mode")
	}

	// updates on telegram API
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message != nil { // If we got a message
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

			msg := tgbotapi.NewMessage(update.Message.Chat.ID, update.Message.Text)
			msg.ReplyToMessageID = update.Message.MessageID

			bot.Send(msg)
		}
	}
}
