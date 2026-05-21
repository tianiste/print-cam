package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok\n"))
}

func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	ctx := r.Context()
	err := a.store.Ping(ctx)
	if err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok\n"))
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
	allowed, err := a.store.AllowRateLimit(r.Context(), "login:"+clientIP(r), 8, time.Minute)
	if err != nil {
		a.log.Error("rate limit failed", "error", err)
		http.Error(w, "rate limit failed", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var req loginRequest
	err = json.NewDecoder(r.Body).Decode(&req)
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

func (a *app) handleLogoutAll(w http.ResponseWriter, r *http.Request, s session) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "csrf rejected", http.StatusForbidden)
		return
	}
	err := a.store.DeleteUserSessions(r.Context(), s.UserID)
	if err != nil {
		a.log.Error("delete user sessions failed", "error", err)
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
