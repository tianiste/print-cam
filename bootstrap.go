package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
	user := user{ID: randomID(), Email: email, PasswordHash: passwordHash, TOTPSecret: normalizeBase32(cfg.BootstrapTOTP), CreatedAt: time.Now()}
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
