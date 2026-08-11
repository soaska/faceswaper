package tempcleanup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveContentsKeepsDirectory(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, "file"))
	if err := os.Mkdir(filepath.Join(directory, "session"), 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveContents(directory)
	if err != nil || removed != 2 {
		t.Fatalf("RemoveContents() = %d, %v", removed, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory after cleanup = %v, %v", entries, err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
}
