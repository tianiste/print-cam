package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

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
