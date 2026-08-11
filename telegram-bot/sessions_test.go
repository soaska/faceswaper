package main

import (
	"testing"
	"time"
)

func TestSessionExpires(t *testing.T) {
	current := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	store := newSessionStore(30 * time.Minute)
	store.now = func() time.Time { return current }
	store.SetFace(42, "face-file", "telegram:100")

	current = current.Add(29 * time.Minute)
	if got := store.Get(42).FaceFileID; got != "face-file" {
		t.Fatalf("session expired early: %q", got)
	}

	current = current.Add(30 * time.Minute)
	if got := store.Get(42).FaceFileID; got != "" {
		t.Fatalf("expired session kept face file: %q", got)
	}
}

func TestSessionResetClearsAllModes(t *testing.T) {
	store := newSessionStore(time.Hour)
	store.SetFace(42, "face-file", "telegram:100")
	store.StartCompression(42, 80)
	store.Reset(42)

	session := store.Get(42)
	if session.FaceFileID != "" || session.WaitingForCompress || session.CompressQuality != 0 {
		t.Fatalf("session was not reset: %+v", session)
	}
}

func TestSessionStoreDoesNotRetainEmptyReads(t *testing.T) {
	store := newSessionStore(time.Hour)
	if session := store.Get(42); session != (UserSession{}) {
		t.Fatalf("missing session = %+v", session)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("empty read retained %d sessions", len(store.sessions))
	}
}

func TestRepeatedFaceSourceUsesUpdateIdentity(t *testing.T) {
	session := UserSession{FaceFileID: "face-file", FaceRequestKey: "telegram:100"}
	if !isRepeatedFaceSource(session, "telegram:100") {
		t.Fatal("same Telegram update was not recognized")
	}
	if isRepeatedFaceSource(session, "telegram:101") {
		t.Fatal("a new update with the same file must remain a valid target")
	}
}
