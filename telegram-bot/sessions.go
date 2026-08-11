package main

import (
	"sync"
	"time"
)

const sessionTTL = 30 * time.Minute

type UserSession struct {
	FaceFileID         string
	WaitingForCompress bool
	CompressQuality    int
	UpdatedAt          time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[int64]UserSession
	ttl      time.Duration
	now      func() time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{
		sessions: make(map[int64]UserSession),
		ttl:      ttl,
		now:      time.Now,
	}
}

func (store *sessionStore) Get(userID int64) UserSession {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	session := store.sessions[userID]
	if !session.UpdatedAt.IsZero() && now.Sub(session.UpdatedAt) >= store.ttl {
		session = UserSession{}
	}
	session.UpdatedAt = now
	store.sessions[userID] = session
	return session
}

func (store *sessionStore) SetFace(userID int64, fileID string) {
	store.update(userID, func(session *UserSession) {
		session.FaceFileID = fileID
	})
}

func (store *sessionStore) StartCompression(userID int64, quality int) {
	store.update(userID, func(session *UserSession) {
		session.WaitingForCompress = true
		session.CompressQuality = quality
	})
}

func (store *sessionStore) Reset(userID int64) {
	store.mu.Lock()
	delete(store.sessions, userID)
	store.mu.Unlock()
}

func (store *sessionStore) update(userID int64, change func(*UserSession)) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session := store.sessions[userID]
	change(&session)
	session.UpdatedAt = store.now()
	store.sessions[userID] = session
}
