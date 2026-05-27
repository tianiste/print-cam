package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPostgresStore(t *testing.T) (*postgresStore, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	err = pool.Ping(ctx)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("Ping: %v", err)
	}
	err = runMigrations(ctx, pool)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("runMigrations: %v", err)
	}
	return newPostgresStore(pool), func() {
		pool.Close()
		cancel()
	}
}

func TestPostgresMigrationsAndStoreLifecycle(t *testing.T) {
	store, cleanup := testPostgresStore(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	u := user{
		ID:           "test-user-" + randomID(),
		Email:        "test-" + randomID() + "@example.com",
		PasswordHash: "hash",
		TOTPSecret:   "JBSWY3DPEHPK3PXP",
		CreatedAt:    now,
	}

	err := store.CreateUser(ctx, u)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := store.UserByEmail(ctx, u.Email)
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if got.ID != u.ID || got.TOTPSecret != u.TOTPSecret {
		t.Fatalf("user = %#v", got)
	}
	err = store.CreateCamera(ctx, u.ID, "  Printer  ")
	if err != nil {
		t.Fatalf("CreateCamera: %v", err)
	}
	cameras, err := store.CamerasByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CamerasByUser: %v", err)
	}
	if len(cameras) != 1 || cameras[0].Name != "Printer" {
		t.Fatalf("cameras = %#v", cameras)
	}
	cameraID := cameras[0].ID
	_, err = store.Camera(ctx, "other-user", cameraID)
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("non-owner camera err = %v", err)
	}
	err = store.UpdateCameraName(ctx, u.ID, cameraID, "Updated printer")
	if err != nil {
		t.Fatalf("UpdateCameraName: %v", err)
	}
	camera, err := store.Camera(ctx, u.ID, cameraID)
	if err != nil {
		t.Fatalf("Camera: %v", err)
	}
	if camera.Name != "Updated printer" {
		t.Fatalf("camera name = %q", camera.Name)
	}

	sess := session{ID: "test-session-" + randomID(), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	err = store.CreateSession(ctx, sess)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = store.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	err = store.AddAudit(ctx, auditEvent{ID: "test-audit-" + randomID(), UserID: u.ID, CameraID: cameraID, Event: "camera_checked", CreatedAt: now})
	if err != nil {
		t.Fatalf("AddAudit: %v", err)
	}
	err = store.DeleteCamera(ctx, u.ID, cameraID)
	if err != nil {
		t.Fatalf("DeleteCamera: %v", err)
	}
	_, err = store.Camera(ctx, u.ID, cameraID)
	if !errors.Is(err, errNotFound) {
		t.Fatalf("deleted camera err = %v", err)
	}
	err = store.DeleteUserSessions(ctx, u.ID)
	if err != nil {
		t.Fatalf("DeleteUserSessions: %v", err)
	}
	_, err = store.Session(ctx, sess.ID)
	if !errors.Is(err, errNotFound) {
		t.Fatalf("deleted session err = %v", err)
	}
}

func TestPostgresRateLimit(t *testing.T) {
	store, cleanup := testPostgresStore(t)
	defer cleanup()
	ctx := context.Background()
	key := "test-rate-" + randomID()
	for i := 0; i < 2; i++ {
		allowed, err := store.AllowRateLimit(ctx, key, 2, time.Minute)
		if err != nil {
			t.Fatalf("AllowRateLimit: %v", err)
		}
		if !allowed {
			t.Fatal("expected request to be allowed")
		}
	}
	allowed, err := store.AllowRateLimit(ctx, key, 2, time.Minute)
	if err != nil {
		t.Fatalf("AllowRateLimit: %v", err)
	}
	if allowed {
		t.Fatal("expected rate limit denial")
	}
}
