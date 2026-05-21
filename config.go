package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type config struct {
	Addr             string
	PublicOrigin     string
	FrontendOrigins  []string
	DatabaseURL      string
	SessionSecret    []byte
	SessionSecretSet bool
	SessionSecretLen int
	TURNSecret       string
	TURNRealm        string
	TURNURLs         []string
	SecureCookies    bool
	BootstrapEmail   string
	BootstrapPass    string
	BootstrapTOTP    string
}

func loadConfig() config {
	origin := getenv("PUBLIC_ORIGIN", "http://localhost:8080")
	secretRaw, secretSet := os.LookupEnv("SESSION_SECRET")
	if strings.TrimSpace(secretRaw) == "" {
		secretRaw = "dev-session-secret-change-me"
		secretSet = false
	}
	secret := []byte(secretRaw)
	if len(secret) < 32 {
		sum := sha256.Sum256(secret)
		secret = sum[:]
	}

	parsed, _ := url.Parse(origin)
	return config{
		Addr:             getenv("ADDR", ":8080"),
		PublicOrigin:     strings.TrimRight(origin, "/"),
		FrontendOrigins:  splitCSV(getenv("FRONTEND_ORIGINS", "http://localhost:5173")),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		SessionSecret:    secret,
		SessionSecretSet: secretSet,
		SessionSecretLen: len(secretRaw),
		TURNSecret:       os.Getenv("TURN_SHARED_SECRET"),
		TURNRealm:        getenv("TURN_REALM", "print-cam"),
		TURNURLs:         splitCSV(os.Getenv("TURN_URLS")),
		SecureCookies:    parsed != nil && parsed.Scheme == "https",
		BootstrapEmail:   getenv("BOOTSTRAP_EMAIL", "admin@example.com"),
		BootstrapPass:    getenv("BOOTSTRAP_PASSWORD", "change-me-now"),
		BootstrapTOTP:    getenv("BOOTSTRAP_TOTP_SECRET", "JBSWY3DPEHPK3PXP"),
	}
}

func (c config) validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.SecureCookies && (!c.SessionSecretSet || c.SessionSecretLen < 32) {
		return errors.New("SESSION_SECRET must be set to at least 32 bytes for HTTPS origins")
	}
	if c.SecureCookies && c.BootstrapPass == "change-me-now" {
		return errors.New("BOOTSTRAP_PASSWORD must be changed for HTTPS origins")
	}
	if c.PublicOrigin == "" {
		return errors.New("PUBLIC_ORIGIN is required")
	}
	_, err := url.Parse(c.PublicOrigin)
	if err != nil {
		return fmt.Errorf("parse PUBLIC_ORIGIN: %w", err)
	}
	return nil
}

func (c config) allowedOrigin(origin string) bool {
	origin = strings.TrimRight(origin, "/")
	if origin == "" || origin == c.PublicOrigin {
		return true
	}
	for _, allowed := range c.FrontendOrigins {
		if origin == strings.TrimRight(allowed, "/") {
			return true
		}
	}
	return false
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
