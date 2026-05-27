package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
		http.NotFound(w, r)
		return
	}
	if a.serveFrontend(w, r) {
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"service": "print-cam", "status": "ok"})
}

func (a *app) serveFrontend(w http.ResponseWriter, r *http.Request) bool {
	if a.cfg.FrontendDistDir == "" {
		return false
	}
	indexPath := filepath.Join(a.cfg.FrontendDistDir, "index.html")
	_, err := os.Stat(indexPath)
	if err != nil {
		return false
	}
	requestPath := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
	if requestPath != "" {
		filePath := filepath.Join(a.cfg.FrontendDistDir, requestPath)
		info, err := os.Stat(filePath)
		if err == nil && !info.IsDir() {
			http.ServeFile(w, r, filePath)
			return true
		}
	}
	http.ServeFile(w, r, indexPath)
	return true
}

func (a *app) handleCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": a.ensureCSRF(w, r)})
}

func (a *app) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	s, ok := a.currentSession(r)
	if !ok {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	user, err := a.store.User(r.Context(), s.UserID)
	if err != nil {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": user.ID, "email": user.Email})
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
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/cameras/"), "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	cameraID, action, _ := strings.Cut(path, "/")
	if cameraID == "" {
		http.NotFound(w, r)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "csrf rejected", http.StatusForbidden)
		return
	}
	if action == "turn-credentials" && r.Method == http.MethodPost {
		_, err := a.store.Camera(r.Context(), s.UserID, cameraID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, a.turnCredentials(s.UserID))
		return
	}
	if action != "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		a.handleUpdateCamera(w, r, s, cameraID)
	case http.MethodDelete:
		a.handleDeleteCamera(w, r, s, cameraID)
	default:
		methodNotAllowed(w)
	}
}

func (a *app) handleUpdateCamera(w http.ResponseWriter, r *http.Request, s session, cameraID string) {
	var req updateCameraRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "invalid camera", http.StatusBadRequest)
		return
	}
	err = a.store.UpdateCameraName(r.Context(), s.UserID, cameraID, req.Name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.audit(r.Context(), s.UserID, cameraID, "camera_renamed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (a *app) handleDeleteCamera(w http.ResponseWriter, r *http.Request, s session, cameraID string) {
	_, err := a.store.Camera(r.Context(), s.UserID, cameraID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.audit(r.Context(), s.UserID, cameraID, "camera_deleted")
	err = a.store.DeleteCamera(r.Context(), s.UserID, cameraID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
