package main

import (
	"context"
	"testing"
	"time"
)

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
	}, nil, newMemoryStore(), nil, nil)
	creds := app.turnCredentials("user-a")
	if len(creds.IceServers) != 1 {
		t.Fatalf("expected one ICE server, got %d", len(creds.IceServers))
	}
	if creds.IceServers[0].Username == "" || creds.IceServers[0].Credential == "" {
		t.Fatal("expected ephemeral TURN username and credential")
	}
}
