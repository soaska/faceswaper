package main

import (
	"log"

	sharedcleanup "github.com/soaska/faceswaper/shared/tempcleanup"
)

func cleanupBotCacheOnStartup() {
	removed, err := sharedcleanup.RemoveContents(botCacheDirectory())
	if err != nil {
		log.Printf("Не удалось очистить временные файлы бота при запуске: %v", err)
		return
	}
	if removed > 0 {
		log.Printf("Удалено временных файлов бота при запуске: %d", removed)
	}
}
