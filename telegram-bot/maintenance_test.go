package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupBotCacheOnStartupRemovesAllEntries(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("BOT_CACHE_DIR", cacheDir)
	if err := os.WriteFile(filepath.Join(cacheDir, "compress-leftover.jpg"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cacheDir, "abandoned-session"), 0o700); err != nil {
		t.Fatal(err)
	}

	cleanupBotCacheOnStartup()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("startup cleanup retained %d entries", len(entries))
	}
}
