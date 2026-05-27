package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testApp(store appStore) *app {
	return newApp(config{
		PublicOrigin:    "http://localhost:8080",
		FrontendOrigins: []string{"http://localhost:5173"},
		SessionSecret:   []byte("01234567890123456789012345678901"),
		TURNURLs:        []string{"stun:stun.l.google.com:19302"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), store, newBroker(nil))
}

func TestPasswordHashVerifiesOnlyOriginalPassword(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Fatal("expected original password to verify")
	}
	if verifyPassword("wrong password", hash) {
		t.Fatal("wrong password verified")
	}
}

func TestVerifyTOTPUsesStandardVector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(59, 0)
	if !verifyTOTP(secret, "287082", now) {
		t.Fatal("expected RFC 6238 test vector to verify")
	}
	if verifyTOTP(secret, "000000", now) {
		t.Fatal("unexpected invalid TOTP verification")
	}
}

func TestCameraLookupRequiresOwner(t *testing.T) {
	store := newMemoryStore()
	ctx := context.Background()
	err := store.CreateCamera(ctx, "user-a", "Printer")
	if err != nil {
		t.Fatalf("CreateCamera: %v", err)
	}
	cameras, err := store.CamerasByUser(ctx, "user-a")
	if err != nil {
		t.Fatalf("CamerasByUser: %v", err)
	}
	if len(cameras) != 1 {
		t.Fatalf("expected one camera, got %d", len(cameras))
	}
	_, err = store.Camera(ctx, "user-a", cameras[0].ID)
	if err != nil {
		t.Fatalf("owner lookup failed: %v", err)
	}
	_, err = store.Camera(ctx, "user-b", cameras[0].ID)
	if err == nil {
		t.Fatal("non-owner lookup succeeded")
	}
}

func TestTURNSharedSecretCredentials(t *testing.T) {
	app := newApp(config{
		SessionSecret: []byte("01234567890123456789012345678901"),
		TURNSecret:    "turn-secret",
		TURNURLs:      []string{"turns:turn.example.com:5349"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), newMemoryStore(), newBroker(nil))
	creds := app.turnCredentials("user-a")
	if len(creds.IceServers) != 1 {
		t.Fatalf("expected one ICE server, got %d", len(creds.IceServers))
	}
	if creds.IceServers[0].Username == "" || creds.IceServers[0].Credential == "" {
		t.Fatal("expected ephemeral TURN username and credential")
	}
}

func TestRateLimitAllowDeny(t *testing.T) {
	store := newMemoryStore()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		allowed, err := store.AllowRateLimit(ctx, "login:127.0.0.1", 2, time.Minute)
		if err != nil {
			t.Fatalf("AllowRateLimit: %v", err)
		}
		if !allowed {
			t.Fatal("expected request to be allowed")
		}
	}
	allowed, err := store.AllowRateLimit(ctx, "login:127.0.0.1", 2, time.Minute)
	if err != nil {
		t.Fatalf("AllowRateLimit: %v", err)
	}
	if allowed {
		t.Fatal("expected rate limit denial")
	}
}

func TestDeleteExpiredAndUserSessions(t *testing.T) {
	store := newMemoryStore()
	ctx := context.Background()
	active := session{ID: "active", UserID: "user-a", ExpiresAt: time.Now().Add(time.Hour)}
	expired := session{ID: "expired", UserID: "user-a", ExpiresAt: time.Now().Add(-time.Hour)}
	store.CreateSession(ctx, active)
	store.CreateSession(ctx, expired)
	err := store.DeleteExpiredSessions(ctx, time.Now())
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	_, err = store.Session(ctx, expired.ID)
	if err == nil {
		t.Fatal("expired session still exists")
	}
	_, err = store.Session(ctx, active.ID)
	if err != nil {
		t.Fatalf("active session missing: %v", err)
	}
	err = store.DeleteUserSessions(ctx, "user-a")
	if err != nil {
		t.Fatalf("DeleteUserSessions: %v", err)
	}
	_, err = store.Session(ctx, active.ID)
	if err == nil {
		t.Fatal("user session still exists")
	}
}

func TestHealthReadyAndCORS(t *testing.T) {
	app := testApp(newMemoryStore())
	handler := app.routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/cameras", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allow origin = %q", got)
	}
}

func TestRootAndRemovedPageRoutes(t *testing.T) {
	app := testApp(newMemoryStore())
	handler := app.routes()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login page status = %d", rec.Code)
	}
}

func TestCSRFAndMe(t *testing.T) {
	store := newMemoryStore()
	app := testApp(store)
	handler := app.routes()

	req := httptest.NewRequest(http.MethodGet, "/api/auth/csrf", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("csrf status = %d", rec.Code)
	}
	if rec.Result().Cookies()[0].Name != csrfCookie {
		t.Fatalf("expected csrf cookie, got %q", rec.Result().Cookies()[0].Name)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without session status = %d", rec.Code)
	}
}

func TestAllowedOrigin(t *testing.T) {
	cfg := config{PublicOrigin: "https://app.example.com", FrontendOrigins: []string{"http://localhost:5173"}}
	if !cfg.allowedOrigin("https://app.example.com") {
		t.Fatal("public origin rejected")
	}
	if !cfg.allowedOrigin("http://localhost:5173") {
		t.Fatal("frontend origin rejected")
	}
	if cfg.allowedOrigin("https://evil.example.com") {
		t.Fatal("unexpected origin allowed")
	}
}
