package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func testApp(store appStore) *app {
	return newApp(config{
		PublicOrigin:    "http://localhost:8080",
		FrontendOrigins: []string{"http://localhost:5173"},
		SessionSecret:   []byte("01234567890123456789012345678901"),
		TURNURLs:        []string{"stun:stun.l.google.com:19302"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), store, newBroker(nil))
}

func dialSignal(t *testing.T, ctx context.Context, serverURL, path, origin, sessionToken string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + path
	headers := http.Header{}
	headers.Set("Origin", origin)
	headers.Set("Cookie", sessionCookie+"="+sessionToken)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("websocket dial %s: %v", path, err)
	}
	return conn
}

func readSignal(t *testing.T, ctx context.Context, conn *websocket.Conn) signalMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read signal: %v", err)
	}
	var msg signalMessage
	err = json.Unmarshal(data, &msg)
	if err != nil {
		t.Fatalf("decode signal: %v", err)
	}
	return msg
}

func writeSignal(t *testing.T, ctx context.Context, conn *websocket.Conn, msg signalMessage) {
	t.Helper()
	err := conn.Write(ctx, websocket.MessageText, mustJSON(msg))
	if err != nil {
		t.Fatalf("write signal: %v", err)
	}
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

func TestCameraRenameAndDeleteRequireOwner(t *testing.T) {
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
	cameraID := cameras[0].ID
	err = store.UpdateCameraName(ctx, "user-b", cameraID, "Other")
	if err == nil {
		t.Fatal("non-owner rename succeeded")
	}
	err = store.UpdateCameraName(ctx, "user-a", cameraID, "Updated printer")
	if err != nil {
		t.Fatalf("owner rename failed: %v", err)
	}
	cam, err := store.Camera(ctx, "user-a", cameraID)
	if err != nil {
		t.Fatalf("Camera: %v", err)
	}
	if cam.Name != "Updated printer" {
		t.Fatalf("camera name = %q", cam.Name)
	}
	err = store.DeleteCamera(ctx, "user-b", cameraID)
	if err == nil {
		t.Fatal("non-owner delete succeeded")
	}
	err = store.DeleteCamera(ctx, "user-a", cameraID)
	if err != nil {
		t.Fatalf("owner delete failed: %v", err)
	}
	_, err = store.Camera(ctx, "user-a", cameraID)
	if err == nil {
		t.Fatal("deleted camera still exists")
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

func TestWebSocketSignalingRelayAndHostDisconnect(t *testing.T) {
	store := newMemoryStore()
	app := testApp(store)
	ctx := context.Background()
	err := store.CreateCamera(ctx, "user-a", "Printer")
	if err != nil {
		t.Fatalf("CreateCamera: %v", err)
	}
	cameras, err := store.CamerasByUser(ctx, "user-a")
	if err != nil {
		t.Fatalf("CamerasByUser: %v", err)
	}
	sess := session{ID: "ws-session", UserID: "user-a", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	err = store.CreateSession(ctx, sess)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(app.routes())
	server.Listener = listener
	server.Start()
	defer server.Close()
	app.cfg.PublicOrigin = server.URL

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token := app.signToken("session", sess.ID, time.Hour)
	host := dialSignal(t, readCtx, server.URL, "/ws/host/"+cameras[0].ID, server.URL, token)
	defer host.Close(websocket.StatusNormalClosure, "test done")
	msg := readSignal(t, readCtx, host)
	if msg.Type != "host-ready" {
		t.Fatalf("host ready msg = %#v", msg)
	}

	secondHost := dialSignal(t, readCtx, server.URL, "/ws/host/"+cameras[0].ID, server.URL, token)
	msg = readSignal(t, readCtx, secondHost)
	if msg.Type != "error" || msg.Error == "" {
		t.Fatalf("second host rejection msg = %#v", msg)
	}
	secondHost.Close(websocket.StatusNormalClosure, "test done")

	viewer := dialSignal(t, readCtx, server.URL, "/ws/view/"+cameras[0].ID, server.URL, token)
	defer viewer.Close(websocket.StatusNormalClosure, "test done")
	msg = readSignal(t, readCtx, host)
	if msg.Type != "viewer-join" || msg.ViewerID == "" {
		t.Fatalf("viewer join msg = %#v", msg)
	}
	viewerID := msg.ViewerID

	offer := json.RawMessage(`{"type":"offer","sdp":"offer-sdp"}`)
	writeSignal(t, readCtx, host, signalMessage{Type: "offer", ViewerID: viewerID, SDP: offer})
	msg = readSignal(t, readCtx, viewer)
	if msg.Type != "offer" || msg.ViewerID != viewerID || string(msg.SDP) != string(offer) {
		t.Fatalf("offer relay msg = %#v", msg)
	}

	answer := json.RawMessage(`{"type":"answer","sdp":"answer-sdp"}`)
	writeSignal(t, readCtx, viewer, signalMessage{Type: "answer", SDP: answer})
	msg = readSignal(t, readCtx, host)
	if msg.Type != "answer" || msg.ViewerID != viewerID || string(msg.SDP) != string(answer) {
		t.Fatalf("answer relay msg = %#v", msg)
	}

	candidate := json.RawMessage(`{"candidate":"candidate-a"}`)
	writeSignal(t, readCtx, viewer, signalMessage{Type: "ice-candidate", Candidate: candidate})
	msg = readSignal(t, readCtx, host)
	if msg.Type != "ice-candidate" || msg.ViewerID != viewerID || string(msg.Candidate) != string(candidate) {
		t.Fatalf("candidate relay msg = %#v", msg)
	}

	err = host.Close(websocket.StatusNormalClosure, "host left")
	if err != nil {
		t.Fatalf("close host: %v", err)
	}
	msg = readSignal(t, readCtx, viewer)
	if msg.Type != "host-left" {
		t.Fatalf("host left msg = %#v", msg)
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

func TestFrontendDistServing(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<div id="app"></div>`), 0o600)
	if err != nil {
		t.Fatalf("write index: %v", err)
	}
	err = os.Mkdir(filepath.Join(dir, "assets"), 0o700)
	if err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	err = os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte(`console.log("ok")`), 0o600)
	if err != nil {
		t.Fatalf("write asset: %v", err)
	}
	app := testApp(newMemoryStore())
	app.cfg.FrontendDistDir = dir
	handler := app.routes()

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login fallback status = %d", rec.Code)
	}
	if rec.Body.String() != `<div id="app"></div>` {
		t.Fatalf("login fallback body = %q", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d", rec.Code)
	}
	if rec.Body.String() != `console.log("ok")` {
		t.Fatalf("asset body = %q", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/missing", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("api missing status = %d", rec.Code)
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

func TestCameraAPIRenameAndDelete(t *testing.T) {
	store := newMemoryStore()
	app := testApp(store)
	handler := app.routes()
	ctx := context.Background()
	u := user{ID: "user-a", Email: "user@example.com", PasswordHash: "hash", CreatedAt: time.Now()}
	err := store.CreateUser(ctx, u)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	err = store.CreateCamera(ctx, u.ID, "Printer")
	if err != nil {
		t.Fatalf("CreateCamera: %v", err)
	}
	cameras, err := store.CamerasByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CamerasByUser: %v", err)
	}
	sess := session{ID: "session-a", UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	err = store.CreateSession(ctx, sess)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	csrf := "csrf-token"
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/"+cameras[0].ID, bytes.NewBufferString(`{"name":"Updated"}`))
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: app.signToken("session", sess.ID, time.Hour)})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d", rec.Code)
	}
	cam, err := store.Camera(ctx, u.ID, cameras[0].ID)
	if err != nil {
		t.Fatalf("Camera: %v", err)
	}
	if cam.Name != "Updated" {
		t.Fatalf("camera name = %q", cam.Name)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/cameras/"+cameras[0].ID, nil)
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: app.signToken("session", sess.ID, time.Hour)})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	_, err = store.Camera(ctx, u.ID, cameras[0].ID)
	if err == nil {
		t.Fatal("deleted camera still exists")
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

func TestValidateHTTPSRequiresProductionSecrets(t *testing.T) {
	base := config{
		DatabaseURL:      "postgres://example",
		PublicOrigin:     "https://app.example.com",
		SessionSecret:    []byte("01234567890123456789012345678901"),
		SessionSecretSet: true,
		SessionSecretLen: 32,
		SecureCookies:    true,
		BootstrapEmail:   "owner@example.com",
		BootstrapPass:    "changed-password",
		BootstrapTOTP:    "GEZDGNBVGY3TQOJQ",
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
	cases := []struct {
		name   string
		change func(*config)
	}{
		{
			name: "session secret unset",
			change: func(cfg *config) {
				cfg.SessionSecretSet = false
			},
		},
		{
			name: "default bootstrap email",
			change: func(cfg *config) {
				cfg.BootstrapEmail = defaultBootstrapEmail
			},
		},
		{
			name: "default bootstrap password",
			change: func(cfg *config) {
				cfg.BootstrapPass = defaultBootstrapPass
			},
		},
		{
			name: "default bootstrap totp",
			change: func(cfg *config) {
				cfg.BootstrapTOTP = defaultBootstrapTOTP
			},
		},
	}
	for _, tc := range cases {
		cfg := base
		tc.change(&cfg)
		if err := cfg.validate(); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
}
