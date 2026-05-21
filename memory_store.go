package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

type memoryStore struct {
	mu       sync.RWMutex
	users    map[string]user
	email    map[string]string
	cameras  map[string]camera
	sessions map[string]session
	audits   []auditEvent
	limits   map[string][]time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		users:    make(map[string]user),
		email:    make(map[string]string),
		cameras:  make(map[string]camera),
		sessions: make(map[string]session),
		limits:   make(map[string][]time.Time),
	}
}

func (s *memoryStore) Ping(context.Context) error {
	return nil
}

func (s *memoryStore) CreateUser(_ context.Context, u user) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.email[u.Email]; exists {
		return nil
	}
	s.users[u.ID] = u
	s.email[u.Email] = u.ID
	return nil
}

func (s *memoryStore) UserByEmail(_ context.Context, email string) (user, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.email[email]
	if !ok {
		return user{}, errNotFound
	}
	return s.users[id], nil
}

func (s *memoryStore) User(_ context.Context, id string) (user, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return user{}, errNotFound
	}
	return u, nil
}

func (s *memoryStore) CreateCamera(_ context.Context, userID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := randomID()
	s.cameras[id] = camera{ID: id, UserID: userID, Name: strings.TrimSpace(name), CreatedAt: time.Now()}
	return nil
}

func (s *memoryStore) CamerasByUser(_ context.Context, userID string) ([]camera, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]camera, 0)
	for _, cam := range s.cameras {
		if cam.UserID == userID {
			out = append(out, cam)
		}
	}
	return out, nil
}

func (s *memoryStore) Camera(_ context.Context, userID, cameraID string) (camera, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cam, ok := s.cameras[cameraID]
	if !ok {
		return camera{}, errNotFound
	}
	if cam.UserID != userID {
		return camera{}, errUnauthorized
	}
	return cam, nil
}

func (s *memoryStore) CreateSession(_ context.Context, sess session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *memoryStore) Session(_ context.Context, id string) (session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return session{}, errNotFound
	}
	return sess, nil
}

func (s *memoryStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *memoryStore) DeleteUserSessions(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, id)
		}
	}
	return nil
}

func (s *memoryStore) DeleteExpiredSessions(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if !sess.ExpiresAt.After(now) {
			delete(s.sessions, id)
		}
	}
	return nil
}

func (s *memoryStore) AddAudit(_ context.Context, event auditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, event)
	return nil
}

func (s *memoryStore) AllowRateLimit(_ context.Context, key string, max int, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	events := s.limits[key]
	kept := events[:0]
	for _, event := range events {
		if event.After(cutoff) {
			kept = append(kept, event)
		}
	}
	if len(kept) >= max {
		s.limits[key] = kept
		return false, nil
	}
	s.limits[key] = append(kept, now)
	return true, nil
}
