package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie = "print_cam_session"
	csrfCookie    = "print_cam_csrf"
	maxSignalSize = 32 << 10
)

var (
	errNotFound     = errors.New("not found")
	errUnauthorized = errors.New("unauthorized")
)

func main() {
	err := loadDotEnv(".env")
	if err != nil {
		slog.Warn("load .env failed", "error", err)
	}
	cfg := loadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if cfg.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database pool failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	err = pool.Ping(ctx)
	if err != nil {
		logger.Error("database ping failed", "error", err)
		os.Exit(1)
	}
	store := newPostgresStore(pool)

	err = initSchema(ctx, pool)
	if err != nil {
		logger.Error("schema init failed", "error", err)
		os.Exit(1)
	}
	err = bootstrap(ctx, store, cfg)
	if err != nil {
		logger.Error("bootstrap failed", "error", err)
		os.Exit(1)
	}

	app := newApp(cfg, logger, store, newBroker(logger), newLimiter())
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("listening", "addr", cfg.Addr)
	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

type config struct {
	Addr           string
	PublicOrigin   string
	DatabaseURL    string
	SessionSecret  []byte
	TURNSecret     string
	TURNRealm      string
	TURNURLs       []string
	SecureCookies  bool
	BootstrapEmail string
	BootstrapPass  string
	BootstrapTOTP  string
}

func loadConfig() config {
	origin := getenv("PUBLIC_ORIGIN", "http://localhost:8080")
	secret := []byte(getenv("SESSION_SECRET", "dev-session-secret-change-me"))
	if len(secret) < 32 {
		sum := sha256.Sum256(secret)
		secret = sum[:]
	}

	parsed, _ := url.Parse(origin)
	return config{
		Addr:           getenv("ADDR", ":8080"),
		PublicOrigin:   strings.TrimRight(origin, "/"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		SessionSecret:  secret,
		TURNSecret:     os.Getenv("TURN_SHARED_SECRET"),
		TURNRealm:      getenv("TURN_REALM", "print-cam"),
		TURNURLs:       splitCSV(os.Getenv("TURN_URLS")),
		SecureCookies:  parsed != nil && parsed.Scheme == "https",
		BootstrapEmail: getenv("BOOTSTRAP_EMAIL", "admin@example.com"),
		BootstrapPass:  getenv("BOOTSTRAP_PASSWORD", "change-me-now"),
		BootstrapTOTP:  getenv("BOOTSTRAP_TOTP_SECRET", "JBSWY3DPEHPK3PXP"),
	}
}

func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		err = os.Setenv(key, value)
		if err != nil {
			return err
		}
	}
	return nil
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return key, value, true
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"stun:stun.l.google.com:19302"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func bootstrap(ctx context.Context, store appStore, cfg config) error {
	email := strings.ToLower(cfg.BootstrapEmail)
	existing, err := store.UserByEmail(ctx, email)
	if err == nil {
		return seedDefaultCamera(ctx, store, existing.ID)
	}
	if !errors.Is(err, errNotFound) {
		return err
	}
	passwordHash, err := hashPassword(cfg.BootstrapPass)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	user := user{
		ID:           randomID(),
		Email:        email,
		PasswordHash: passwordHash,
		TOTPSecret:   normalizeBase32(cfg.BootstrapTOTP),
		CreatedAt:    time.Now(),
	}
	err = store.CreateUser(ctx, user)
	if err != nil {
		return err
	}
	return seedDefaultCamera(ctx, store, user.ID)
}

func seedDefaultCamera(ctx context.Context, store appStore, userID string) error {
	cameras, err := store.CamerasByUser(ctx, userID)
	if err != nil {
		return err
	}
	if len(cameras) > 0 {
		return nil
	}
	return store.CreateCamera(ctx, userID, "Printer camera")
}

type app struct {
	cfg    config
	log    *slog.Logger
	store  appStore
	broker *broker
	limit  *limiter
	pages  *template.Template
}

func newApp(cfg config, logger *slog.Logger, store appStore, broker *broker, limit *limiter) *app {
	return &app{
		cfg:    cfg,
		log:    logger,
		store:  store,
		broker: broker,
		limit:  limit,
		pages:  template.Must(template.New("pages").Parse(pageTemplates)),
	}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/login", a.handleLoginPage)
	mux.HandleFunc("/cameras", a.authPage(a.handleCamerasPage))
	mux.HandleFunc("/cameras/", a.authPage(a.handleCameraPage))
	mux.HandleFunc("/api/auth/login", a.handleLogin)
	mux.HandleFunc("/api/auth/totp/verify", a.handleTOTPVerify)
	mux.HandleFunc("/api/auth/logout", a.authAPI(a.handleLogout))
	mux.HandleFunc("/api/cameras", a.authAPI(a.handleCamerasAPI))
	mux.HandleFunc("/api/cameras/", a.authAPI(a.handleCameraAPI))
	mux.HandleFunc("/ws/host/", a.handleHostWS)
	mux.HandleFunc("/ws/view/", a.handleViewWS)
	return a.securityHeaders(mux)
}

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; media-src 'self' blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if a.cfg.SecureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, ok := a.currentSession(r); ok {
		http.Redirect(w, r, "/cameras", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *app) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.render(w, "login", map[string]any{"CSRF": a.ensureCSRF(w, r)})
}

func (a *app) handleCamerasPage(w http.ResponseWriter, r *http.Request, s session) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cameras, err := a.store.CamerasByUser(r.Context(), s.UserID)
	if err != nil {
		a.log.Error("load cameras failed", "error", err)
		http.Error(w, "load cameras failed", http.StatusInternalServerError)
		return
	}
	a.render(w, "cameras", map[string]any{"CSRF": a.ensureCSRF(w, r), "Cameras": cameras})
}

func (a *app) handleCameraPage(w http.ResponseWriter, r *http.Request, s session) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id, role, ok := parseCameraRole(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cam, err := a.store.Camera(r.Context(), s.UserID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view := "view"
	if role == "host" {
		view = "host"
	}
	a.render(w, view, map[string]any{"Camera": cam, "CSRF": a.ensureCSRF(w, r)})
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.limit.allow("login:"+clientIP(r), 8, time.Minute) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var req loginRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	user, err := a.store.UserByEmail(r.Context(), strings.ToLower(req.Email))
	if err != nil || !verifyPassword(req.Password, user.PasswordHash) {
		time.Sleep(300 * time.Millisecond)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	pending := a.signToken("pending", user.ID, 10*time.Minute)
	setCookie(w, "print_cam_pending", pending, 10*time.Minute, a.cfg.SecureCookies, true)
	a.audit(r.Context(), user.ID, "", "password_login_ok")
	writeJSON(w, http.StatusOK, map[string]string{"status": "totp_required"})
}

func (a *app) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	pending, err := r.Cookie("print_cam_pending")
	if err != nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	userID, ok := a.verifyToken("pending", pending.Value)
	if !ok {
		http.Error(w, "login expired", http.StatusUnauthorized)
		return
	}
	var req totpRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	user, err := a.store.User(r.Context(), userID)
	if err != nil || !verifyTOTP(user.TOTPSecret, req.Code, time.Now()) {
		http.Error(w, "invalid totp", http.StatusUnauthorized)
		return
	}
	s := session{ID: randomID(), UserID: user.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)}
	err = a.store.CreateSession(r.Context(), s)
	if err != nil {
		a.log.Error("create session failed", "error", err)
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	setCookie(w, sessionCookie, a.signToken("session", s.ID, 24*time.Hour), 24*time.Hour, a.cfg.SecureCookies, true)
	setCookie(w, "print_cam_pending", "", -time.Hour, a.cfg.SecureCookies, true)
	a.ensureCSRF(w, r)
	a.audit(r.Context(), user.ID, "", "totp_login_ok")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request, s session) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "csrf rejected", http.StatusForbidden)
		return
	}
	err := a.store.DeleteSession(r.Context(), s.ID)
	if err != nil {
		a.log.Error("delete session failed", "error", err)
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}
	setCookie(w, sessionCookie, "", -time.Hour, a.cfg.SecureCookies, true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleCamerasAPI(w http.ResponseWriter, r *http.Request, s session) {
	switch r.Method {
	case http.MethodGet:
		cameras, err := a.store.CamerasByUser(r.Context(), s.UserID)
		if err != nil {
			a.log.Error("load cameras failed", "error", err)
			http.Error(w, "load cameras failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, cameras)
	case http.MethodPost:
		if !a.validCSRF(r) {
			http.Error(w, "csrf rejected", http.StatusForbidden)
			return
		}
		var req createCameraRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil || strings.TrimSpace(req.Name) == "" {
			http.Error(w, "invalid camera", http.StatusBadRequest)
			return
		}
		err = a.store.CreateCamera(r.Context(), s.UserID, req.Name)
		if err != nil {
			http.Error(w, "create camera failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
	default:
		methodNotAllowed(w)
	}
}

func (a *app) handleCameraAPI(w http.ResponseWriter, r *http.Request, s session) {
	cameraID, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/cameras/"), "/turn-credentials")
	if !ok || cameraID == "" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "csrf rejected", http.StatusForbidden)
		return
	}
	_, err := a.store.Camera(r.Context(), s.UserID, cameraID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, a.turnCredentials(s.UserID))
}

func (a *app) handleHostWS(w http.ResponseWriter, r *http.Request) {
	a.handleWS(w, r, "host", strings.TrimPrefix(r.URL.Path, "/ws/host/"))
}

func (a *app) handleViewWS(w http.ResponseWriter, r *http.Request) {
	a.handleWS(w, r, "viewer", strings.TrimPrefix(r.URL.Path, "/ws/view/"))
}

func (a *app) handleWS(w http.ResponseWriter, r *http.Request, role, cameraID string) {
	if !a.checkOrigin(r) {
		http.Error(w, "bad origin", http.StatusForbidden)
		return
	}
	if !a.limit.allow("ws:"+clientIP(r), 30, time.Minute) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	s, ok := a.currentSession(r)
	if !ok {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	_, err := a.store.Camera(r.Context(), s.UserID, cameraID)
	if err != nil {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}
	conn, err := acceptWebSocket(w, r)
	if err != nil {
		a.log.Warn("websocket accept failed", "error", err)
		return
	}
	client := newSignalClient(randomID(), s.UserID, cameraID, role, conn)
	if role == "host" {
		err = a.broker.addHost(client)
	} else {
		err = a.broker.addViewer(client)
	}
	if err != nil {
		client.send(signalMessage{Type: "error", Error: err.Error()})
		client.close()
		return
	}
	a.audit(r.Context(), s.UserID, cameraID, role+"_connected")
	a.broker.readLoop(client)
	a.audit(context.Background(), s.UserID, cameraID, role+"_disconnected")
}

func (a *app) authPage(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.currentSession(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r, s)
	}
}

func (a *app) authAPI(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.currentSession(r)
		if !ok {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		next(w, r, s)
	}
}

func (a *app) currentSession(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	id, ok := a.verifyToken("session", c.Value)
	if !ok {
		return session{}, false
	}
	s, err := a.store.Session(r.Context(), id)
	if err != nil || time.Now().After(s.ExpiresAt) {
		return session{}, false
	}
	return s, true
}

func (a *app) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(csrfCookie)
	if err == nil && c.Value != "" {
		return c.Value
	}
	token := randomID()
	setCookie(w, csrfCookie, token, 24*time.Hour, a.cfg.SecureCookies, false)
	return token
}

func (a *app) validCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

func (a *app) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.TrimRight(origin, "/") == a.cfg.PublicOrigin
}

func (a *app) signToken(kind, subject string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%s|%d", kind, subject, exp)
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

func (a *app) verifyToken(kind, token string) (string, bool) {
	payload64, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payload64)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	mac.Write(payload)
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", false
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || parts[0] != kind {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return parts[1], true
}

func (a *app) turnCredentials(userID string) turnResponse {
	ttl := time.Hour
	if a.cfg.TURNSecret == "" {
		return turnResponse{TTLSeconds: int(ttl.Seconds()), IceServers: []iceServer{{URLs: a.cfg.TURNURLs}}}
	}
	username := fmt.Sprintf("%d:%s", time.Now().Add(ttl).Unix(), userID)
	mac := hmac.New(sha1.New, []byte(a.cfg.TURNSecret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return turnResponse{
		TTLSeconds: int(ttl.Seconds()),
		IceServers: []iceServer{{
			URLs:       a.cfg.TURNURLs,
			Username:   username,
			Credential: credential,
		}},
	}
}

func (a *app) audit(ctx context.Context, userID, cameraID, event string) {
	err := a.store.AddAudit(ctx, auditEvent{ID: randomID(), UserID: userID, CameraID: cameraID, Event: event, CreatedAt: time.Now()})
	if err != nil {
		a.log.Warn("audit write failed", "error", err)
	}
	a.log.Info("audit", "user", userID, "camera", cameraID, "event", event)
}

func (a *app) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := a.pages.ExecuteTemplate(w, name, data)
	if err != nil {
		a.log.Error("render failed", "template", name, "error", err)
	}
}

type user struct {
	ID           string
	Email        string
	PasswordHash string
	TOTPSecret   string
	CreatedAt    time.Time
}

type camera struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type session struct {
	ID        string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type auditEvent struct {
	ID        string
	UserID    string
	CameraID  string
	Event     string
	CreatedAt time.Time
}

type appStore interface {
	CreateUser(context.Context, user) error
	UserByEmail(context.Context, string) (user, error)
	User(context.Context, string) (user, error)
	CreateCamera(context.Context, string, string) error
	CamerasByUser(context.Context, string) ([]camera, error)
	Camera(context.Context, string, string) (camera, error)
	CreateSession(context.Context, session) error
	Session(context.Context, string) (session, error)
	DeleteSession(context.Context, string) error
	AddAudit(context.Context, auditEvent) error
}

type postgresStore struct {
	db *pgxpool.Pool
}

func newPostgresStore(db *pgxpool.Pool) *postgresStore {
	return &postgresStore{db: db}
}

func initSchema(ctx context.Context, db *pgxpool.Pool) error {
	for _, stmt := range schemaStatements {
		_, err := db.Exec(ctx, stmt)
		if err != nil {
			return fmt.Errorf("execute schema statement: %w", err)
		}
	}
	return nil
}

func (s *postgresStore) CreateUser(ctx context.Context, u user) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO NOTHING
	`, u.ID, u.Email, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO user_totp_secrets (user_id, secret, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO NOTHING
	`, u.ID, u.TOTPSecret, u.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) UserByEmail(ctx context.Context, email string) (user, error) {
	return s.scanUser(ctx, `
		SELECT u.id, u.email, u.password_hash, COALESCE(t.secret, ''), u.created_at
		FROM users u
		LEFT JOIN user_totp_secrets t ON t.user_id = u.id
		WHERE u.email = $1
	`, email)
}

func (s *postgresStore) User(ctx context.Context, id string) (user, error) {
	return s.scanUser(ctx, `
		SELECT u.id, u.email, u.password_hash, COALESCE(t.secret, ''), u.created_at
		FROM users u
		LEFT JOIN user_totp_secrets t ON t.user_id = u.id
		WHERE u.id = $1
	`, id)
}

func (s *postgresStore) scanUser(ctx context.Context, query string, arg string) (user, error) {
	var u user
	err := s.db.QueryRow(ctx, query, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.TOTPSecret, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user{}, errNotFound
	}
	if err != nil {
		return user{}, err
	}
	return u, nil
}

func (s *postgresStore) CreateCamera(ctx context.Context, userID, name string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO cameras (id, user_id, name, created_at)
		VALUES ($1, $2, $3, $4)
	`, randomID(), userID, strings.TrimSpace(name), time.Now())
	return err
}

func (s *postgresStore) CamerasByUser(ctx context.Context, userID string) ([]camera, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, name, created_at
		FROM cameras
		WHERE user_id = $1
		ORDER BY created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cameras := make([]camera, 0)
	for rows.Next() {
		var cam camera
		err = rows.Scan(&cam.ID, &cam.UserID, &cam.Name, &cam.CreatedAt)
		if err != nil {
			return nil, err
		}
		cameras = append(cameras, cam)
	}
	err = rows.Err()
	if err != nil {
		return nil, err
	}
	return cameras, nil
}

func (s *postgresStore) Camera(ctx context.Context, userID, cameraID string) (camera, error) {
	var cam camera
	err := s.db.QueryRow(ctx, `
		SELECT id, user_id, name, created_at
		FROM cameras
		WHERE id = $1
	`, cameraID).Scan(&cam.ID, &cam.UserID, &cam.Name, &cam.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return camera{}, errNotFound
	}
	if err != nil {
		return camera{}, err
	}
	if cam.UserID != userID {
		return camera{}, errUnauthorized
	}
	return cam, nil
}

func (s *postgresStore) CreateSession(ctx context.Context, sess session) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
	`, sess.ID, sess.UserID, sess.CreatedAt, sess.ExpiresAt)
	return err
}

func (s *postgresStore) Session(ctx context.Context, id string) (session, error) {
	var sess session
	err := s.db.QueryRow(ctx, `
		SELECT id, user_id, created_at, expires_at
		FROM sessions
		WHERE id = $1
	`, id).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return session{}, errNotFound
	}
	if err != nil {
		return session{}, err
	}
	return sess, nil
}

func (s *postgresStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

func (s *postgresStore) AddAudit(ctx context.Context, event auditEvent) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO audit_events (id, user_id, camera_id, event, created_at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5)
	`, event.ID, event.UserID, event.CameraID, event.Event, event.CreatedAt)
	return err
}

type memoryStore struct {
	mu       sync.RWMutex
	users    map[string]user
	email    map[string]string
	cameras  map[string]camera
	sessions map[string]session
	audits   []auditEvent
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		users:    make(map[string]user),
		email:    make(map[string]string),
		cameras:  make(map[string]camera),
		sessions: make(map[string]session),
	}
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

func (s *memoryStore) AddAudit(_ context.Context, event auditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, event)
	return nil
}

type broker struct {
	mu      sync.Mutex
	log     *slog.Logger
	hosts   map[string]*signalClient
	viewers map[string]map[string]*signalClient
}

func newBroker(logger *slog.Logger) *broker {
	return &broker{
		log:     logger,
		hosts:   make(map[string]*signalClient),
		viewers: make(map[string]map[string]*signalClient),
	}
}

func (b *broker) addHost(c *signalClient) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing := b.hosts[c.cameraID]; existing != nil {
		return errors.New("camera already has an active host")
	}
	b.hosts[c.cameraID] = c
	c.send(signalMessage{Type: "host-ready"})
	return nil
}

func (b *broker) addViewer(c *signalClient) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hosts[c.cameraID] == nil {
		return errors.New("host tab is offline")
	}
	if b.viewers[c.cameraID] == nil {
		b.viewers[c.cameraID] = make(map[string]*signalClient)
	}
	b.viewers[c.cameraID][c.id] = c
	b.hosts[c.cameraID].send(signalMessage{Type: "viewer-join", ViewerID: c.id})
	return nil
}

func (b *broker) readLoop(c *signalClient) {
	defer b.remove(c)
	for {
		msg, err := c.conn.readJSON()
		if err != nil {
			return
		}
		if !validSignalType(msg.Type) {
			c.send(signalMessage{Type: "error", Error: "unknown signal type"})
			continue
		}
		b.relay(c, msg)
	}
}

func (b *broker) relay(c *signalClient, msg signalMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	msg.ViewerID = firstNonEmpty(msg.ViewerID, c.id)
	if c.role == "host" {
		viewers := b.viewers[c.cameraID]
		if viewers == nil {
			return
		}
		viewer := viewers[msg.ViewerID]
		if viewer != nil {
			viewer.send(msg)
		}
		return
	}
	host := b.hosts[c.cameraID]
	if host != nil {
		host.send(msg)
	}
}

func (b *broker) remove(c *signalClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c.role == "host" && b.hosts[c.cameraID] == c {
		delete(b.hosts, c.cameraID)
		for _, viewer := range b.viewers[c.cameraID] {
			viewer.send(signalMessage{Type: "host-left"})
			viewer.close()
		}
		delete(b.viewers, c.cameraID)
		return
	}
	if c.role == "viewer" {
		delete(b.viewers[c.cameraID], c.id)
		if b.hosts[c.cameraID] != nil {
			b.hosts[c.cameraID].send(signalMessage{Type: "viewer-left", ViewerID: c.id})
		}
	}
}

type signalClient struct {
	id       string
	userID   string
	cameraID string
	role     string
	conn     *wsConn
	sendMu   sync.Mutex
}

func newSignalClient(id, userID, cameraID, role string, conn *wsConn) *signalClient {
	return &signalClient{id: id, userID: userID, cameraID: cameraID, role: role, conn: conn}
}

func (c *signalClient) send(msg signalMessage) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.conn.writeJSON(msg)
}

func (c *signalClient) close() {
	c.conn.close()
}

type signalMessage struct {
	Type      string          `json:"type"`
	ViewerID  string          `json:"viewerId,omitempty"`
	SDP       json.RawMessage `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func validSignalType(t string) bool {
	switch t {
	case "viewer-join", "offer", "answer", "ice-candidate", "viewer-left", "host-left", "host-ready":
		return true
	default:
		return false
	}
}

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
}

func acceptWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if strings.ToLower(r.Header.Get("Upgrade")) != "websocket" || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return nil, errors.New("not a websocket upgrade")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing websocket key", http.StatusBadRequest)
		return nil, errors.New("missing websocket key")
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return nil, errors.New("hijack unsupported")
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, err
	}
	accept := websocketAccept(key)
	_, err = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, r: rw.Reader}, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (c *wsConn) readJSON() (signalMessage, error) {
	payload, err := c.readFrame()
	if err != nil {
		return signalMessage{}, err
	}
	var msg signalMessage
	err = json.Unmarshal(payload, &msg)
	if err != nil {
		return signalMessage{}, err
	}
	return msg, nil
}

func (c *wsConn) readFrame() ([]byte, error) {
	header := make([]byte, 2)
	_, err := io.ReadFull(c.r, header)
	if err != nil {
		return nil, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	size := int64(header[1] & 0x7f)
	if size == 126 {
		ext := make([]byte, 2)
		_, err = io.ReadFull(c.r, ext)
		if err != nil {
			return nil, err
		}
		size = int64(binary.BigEndian.Uint16(ext))
	} else if size == 127 {
		ext := make([]byte, 8)
		_, err = io.ReadFull(c.r, ext)
		if err != nil {
			return nil, err
		}
		size = int64(binary.BigEndian.Uint64(ext))
	}
	if size > maxSignalSize {
		return nil, errors.New("websocket message too large")
	}
	mask := make([]byte, 4)
	if masked {
		_, err = io.ReadFull(c.r, mask)
		if err != nil {
			return nil, err
		}
	}
	payload := make([]byte, size)
	_, err = io.ReadFull(c.r, payload)
	if err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode == 8 {
		return nil, io.EOF
	}
	if opcode != 1 {
		return nil, errors.New("only text frames are supported")
	}
	return payload, nil
}

func (c *wsConn) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(payload)
}

func (c *wsConn) writeFrame(payload []byte) error {
	var buf bytes.Buffer
	buf.WriteByte(0x81)
	switch {
	case len(payload) < 126:
		buf.WriteByte(byte(len(payload)))
	case len(payload) <= math.MaxUint16:
		buf.WriteByte(126)
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(len(payload)))
		buf.Write(ext)
	default:
		buf.WriteByte(127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(len(payload)))
		buf.Write(ext)
	}
	buf.Write(payload)
	_, err := c.conn.Write(buf.Bytes())
	return err
}

func (c *wsConn) close() {
	c.conn.Close()
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: make(map[string][]time.Time)}
}

func (l *limiter) allow(key string, max int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	events := l.buckets[key]
	kept := events[:0]
	for _, event := range events {
		if event.After(cutoff) {
			kept = append(kept, event)
		}
	}
	if len(kept) >= max {
		l.buckets[key] = kept
		return false
	}
	l.buckets[key] = append(kept, now)
	return true
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type totpRequest struct {
	Code string `json:"code"`
}

type createCameraRequest struct {
	Name string `json:"name"`
}

type turnResponse struct {
	TTLSeconds int         `json:"ttlSeconds"`
	IceServers []iceServer `json:"iceServers"`
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	if err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=4$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func verifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	for offset := -1; offset <= 1; offset++ {
		counter := now.Unix()/30 + int64(offset)
		if counter < 0 {
			continue
		}
		if hotp(secret, uint64(counter)) == code {
			return true
		}
	}
	return false
}

func hotp(secret string, counter uint64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalizeBase32(secret))
	if err != nil {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (int(sum[offset])&0x7f)<<24 | (int(sum[offset+1])&0xff)<<16 | (int(sum[offset+2])&0xff)<<8 | (int(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", value%1000000)
}

func normalizeBase32(value string) string {
	return strings.ToUpper(strings.TrimRight(strings.ReplaceAll(value, " ", ""), "="))
}

func randomID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		n, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
		return strconv.FormatInt(n.Int64(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func parseCameraRole(path string) (string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "cameras" {
		return "", "", false
	}
	if parts[2] != "host" && parts[2] != "view" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration, secure, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		Secure:   secure,
		HttpOnly: httpOnly,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const pageTemplates = `
{{define "login"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Print Cam Login</title><style>{{template "css"}}</style></head>
<body><main class="shell narrow"><h1>Print Cam</h1><section class="panel"><form id="login"><label>Email<input name="email" type="email" value="admin@example.com" autocomplete="username" required></label><label>Password<input name="password" type="password" autocomplete="current-password" required></label><button>Continue</button></form><form id="totp" hidden><label>Authenticator code<input name="code" inputmode="numeric" autocomplete="one-time-code" required></label><button>Sign in</button></form><p id="status"></p></section></main><script>
const status = document.querySelector("#status");
document.querySelector("#login").onsubmit = async e => { e.preventDefault(); const body = Object.fromEntries(new FormData(e.target)); const res = await fetch("/api/auth/login", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(body)}); if (res.ok) { e.target.hidden = true; document.querySelector("#totp").hidden = false; status.textContent = "Enter the mandatory TOTP code."; } else status.textContent = await res.text(); };
document.querySelector("#totp").onsubmit = async e => { e.preventDefault(); const body = Object.fromEntries(new FormData(e.target)); const res = await fetch("/api/auth/totp/verify", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(body)}); if (res.ok) location.href = "/cameras"; else status.textContent = await res.text(); };
</script></body></html>{{end}}
{{define "cameras"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Cameras</title><style>{{template "css"}}</style></head>
<body><main class="shell"><header><h1>Cameras</h1><button id="logout">Log out</button></header><section class="toolbar"><form id="newCamera"><input name="name" placeholder="Camera name" required><button>Add camera</button></form></section><section class="grid">{{range .Cameras}}<article class="panel"><h2>{{.Name}}</h2><p class="muted">{{.ID}}</p><div class="actions"><a href="/cameras/{{.ID}}/host">Host</a><a href="/cameras/{{.ID}}/view">View</a></div></article>{{else}}<p>No cameras yet.</p>{{end}}</section></main><script>
const csrf = "{{.CSRF}}";
document.querySelector("#logout").onclick = async () => { await fetch("/api/auth/logout", {method:"POST", headers:{"X-CSRF-Token":csrf}}); location.href="/login"; };
document.querySelector("#newCamera").onsubmit = async e => { e.preventDefault(); const body = Object.fromEntries(new FormData(e.target)); const res = await fetch("/api/cameras", {method:"POST", headers:{"Content-Type":"application/json","X-CSRF-Token":csrf}, body:JSON.stringify(body)}); if (res.ok) location.reload(); };
</script></body></html>{{end}}
{{define "host"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Host {{.Camera.Name}}</title><style>{{template "css"}}</style></head>
<body><main class="shell"><header><h1>{{.Camera.Name}}</h1><a href="/cameras">Cameras</a></header><section class="video"><video id="local" autoplay muted playsinline></video></section><p id="status">Starting camera...</p></main><script>
const cameraID = "{{.Camera.ID}}"; const status = document.querySelector("#status"); const peers = new Map(); let stream; let iceServers = [];
async function turn(){ const r = await fetch("/api/cameras/" + cameraID + "/turn-credentials", {method:"POST", headers:{"X-CSRF-Token":"{{.CSRF}}"}}); if(r.ok) iceServers = (await r.json()).iceServers; }
async function start(){ await turn(); stream = await navigator.mediaDevices.getUserMedia({video:true,audio:false}); document.querySelector("#local").srcObject = stream; const ws = new WebSocket((location.protocol === "https:" ? "wss" : "ws") + "://" + location.host + "/ws/host/" + cameraID); ws.onmessage = async e => { const m = JSON.parse(e.data); if(m.type==="host-ready") status.textContent="Host online. Waiting for viewers."; if(m.type==="viewer-join") await join(ws,m.viewerId); if(m.type==="answer") await peers.get(m.viewerId)?.setRemoteDescription(m.sdp); if(m.type==="ice-candidate" && m.candidate) await peers.get(m.viewerId)?.addIceCandidate(m.candidate); if(m.type==="viewer-left") { peers.get(m.viewerId)?.close(); peers.delete(m.viewerId); status.textContent=peers.size + " viewer(s) connected"; } if(m.type==="error") status.textContent=m.error; }; ws.onclose=()=>status.textContent="Host disconnected."; }
async function join(ws,id){ const pc = new RTCPeerConnection({iceServers}); peers.set(id, pc); stream.getTracks().forEach(t=>pc.addTrack(t, stream)); pc.onicecandidate = e => { if(e.candidate) ws.send(JSON.stringify({type:"ice-candidate", viewerId:id, candidate:e.candidate})); }; const offer = await pc.createOffer(); await pc.setLocalDescription(offer); ws.send(JSON.stringify({type:"offer", viewerId:id, sdp:pc.localDescription})); status.textContent=peers.size + " viewer(s) connected"; }
start().catch(err => status.textContent = err.message);
</script></body></html>{{end}}
{{define "view"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>View {{.Camera.Name}}</title><style>{{template "css"}}</style></head>
<body><main class="shell"><header><h1>{{.Camera.Name}}</h1><a href="/cameras">Cameras</a></header><section class="video"><video id="remote" autoplay playsinline controls></video></section><p id="status">Connecting...</p></main><script>
const cameraID = "{{.Camera.ID}}"; const status = document.querySelector("#status"); let pc; let iceServers = [];
async function turn(){ const r = await fetch("/api/cameras/" + cameraID + "/turn-credentials", {method:"POST", headers:{"X-CSRF-Token":"{{.CSRF}}"}}); if(r.ok) iceServers = (await r.json()).iceServers; }
async function start(){ await turn(); const ws = new WebSocket((location.protocol === "https:" ? "wss" : "ws") + "://" + location.host + "/ws/view/" + cameraID); pc = new RTCPeerConnection({iceServers}); pc.ontrack = e => { document.querySelector("#remote").srcObject = e.streams[0]; status.textContent = "Live"; }; pc.onicecandidate = e => { if(e.candidate) ws.send(JSON.stringify({type:"ice-candidate", candidate:e.candidate})); }; pc.onconnectionstatechange = () => { if(pc.connectionState) status.textContent = pc.connectionState; }; ws.onmessage = async e => { const m = JSON.parse(e.data); if(m.type==="offer") { await pc.setRemoteDescription(m.sdp); const answer = await pc.createAnswer(); await pc.setLocalDescription(answer); ws.send(JSON.stringify({type:"answer", sdp:pc.localDescription})); } if(m.type==="ice-candidate" && m.candidate) await pc.addIceCandidate(m.candidate); if(m.type==="host-left") status.textContent="Host tab is offline."; if(m.type==="error") status.textContent=m.error; }; ws.onclose=()=>{ if(status.textContent !== "Live") status.textContent="Disconnected."; }; }
start().catch(err => status.textContent = err.message);
</script></body></html>{{end}}
{{define "css"}}body{margin:0;font-family:Inter,system-ui,-apple-system,Segoe UI,sans-serif;background:#f4f7f5;color:#18211d}a,button{font:inherit}button,a{border:1px solid #18211d;background:#18211d;color:white;text-decoration:none;padding:.7rem 1rem;border-radius:6px;cursor:pointer}input{display:block;width:100%;box-sizing:border-box;margin-top:.35rem;padding:.75rem;border:1px solid #9aa79f;border-radius:6px;background:white}label{display:block;margin:0 0 1rem}.shell{max-width:1040px;margin:0 auto;padding:32px}.narrow{max-width:420px}.panel{background:white;border:1px solid #d8e0db;border-radius:8px;padding:20px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px}.actions,.toolbar,header{display:flex;gap:12px;align-items:center;justify-content:space-between}.muted{color:#5c6b63;font-size:.9rem;overflow-wrap:anywhere}.video{background:#111;border-radius:8px;overflow:hidden;aspect-ratio:16/9}video{width:100%;height:100%;object-fit:contain}#status{min-height:1.5rem;color:#334139}@media(max-width:640px){.shell{padding:18px}.actions,.toolbar,header{align-items:stretch;flex-direction:column}} {{end}}
`

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS user_totp_secrets (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		secret TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS cameras (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS cameras_user_id_idx ON cameras(user_id)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		expires_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions(user_id)`,
	`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at)`,
	`CREATE TABLE IF NOT EXISTS audit_events (
		id TEXT PRIMARY KEY,
		user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
		camera_id TEXT REFERENCES cameras(id) ON DELETE SET NULL,
		event TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS audit_events_user_id_idx ON audit_events(user_id)`,
	`CREATE INDEX IF NOT EXISTS audit_events_camera_id_idx ON audit_events(camera_id)`,
	`CREATE INDEX IF NOT EXISTS audit_events_created_at_idx ON audit_events(created_at)`,
}
