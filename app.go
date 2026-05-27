package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type app struct {
	cfg    config
	log    *slog.Logger
	store  appStore
	broker *broker
}

func newApp(cfg config, logger *slog.Logger, store appStore, broker *broker) *app {
	return &app{
		cfg:    cfg,
		log:    logger,
		store:  store,
		broker: broker,
	}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/readyz", a.handleReady)
	mux.HandleFunc("/api/auth/csrf", a.handleCSRF)
	mux.HandleFunc("/api/auth/me", a.handleMe)
	mux.HandleFunc("/api/auth/login", a.handleLogin)
	mux.HandleFunc("/api/auth/totp/verify", a.handleTOTPVerify)
	mux.HandleFunc("/api/auth/logout", a.authAPI(a.handleLogout))
	mux.HandleFunc("/api/auth/logout-all", a.authAPI(a.handleLogoutAll))
	mux.HandleFunc("/api/cameras", a.authAPI(a.handleCamerasAPI))
	mux.HandleFunc("/api/cameras/", a.authAPI(a.handleCameraAPI))
	mux.HandleFunc("/ws/host/", a.handleHostWS)
	mux.HandleFunc("/ws/view/", a.handleViewWS)
	return a.middleware(mux)
}

func (a *app) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.setSecurityHeaders(w)
		if a.setCORSHeaders(w, r) && r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; media-src 'self' blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if a.cfg.SecureCookies {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func (a *app) setCORSHeaders(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !strings.HasPrefix(r.URL.Path, "/api/") || !a.cfg.allowedOrigin(origin) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", strings.TrimRight(origin, "/"))
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Add("Vary", "Origin")
	return true
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
	if err != nil {
		return session{}, false
	}
	if time.Now().After(s.ExpiresAt) {
		a.store.DeleteSession(r.Context(), s.ID)
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
	return a.cfg.allowedOrigin(r.Header.Get("Origin"))
}

func (a *app) signToken(kind, subject string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := kind + "|" + subject + "|" + strconv.FormatInt(exp, 10)
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
	username := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10) + ":" + userID
	mac := hmac.New(sha1.New, []byte(a.cfg.TURNSecret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return turnResponse{TTLSeconds: int(ttl.Seconds()), IceServers: []iceServer{{URLs: a.cfg.TURNURLs, Username: username, Credential: credential}}}
}

func (a *app) audit(ctx context.Context, userID, cameraID, event string) {
	err := a.store.AddAudit(ctx, auditEvent{ID: randomID(), UserID: userID, CameraID: cameraID, Event: event, CreatedAt: time.Now()})
	if err != nil {
		a.log.Warn("audit write failed", "error", err)
	}
	a.log.Info("audit", "user", userID, "camera", cameraID, "event", event)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
