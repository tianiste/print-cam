package main

import (
	"errors"
	"time"
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
