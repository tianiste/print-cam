package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type appStore interface {
	CreateUser(context.Context, user) error
	UserByEmail(context.Context, string) (user, error)
	User(context.Context, string) (user, error)
	CreateCamera(context.Context, string, string) error
	CamerasByUser(context.Context, string) ([]camera, error)
	Camera(context.Context, string, string) (camera, error)
	UpdateCameraName(context.Context, string, string, string) error
	DeleteCamera(context.Context, string, string) error
	CreateSession(context.Context, session) error
	Session(context.Context, string) (session, error)
	DeleteSession(context.Context, string) error
	DeleteUserSessions(context.Context, string) error
	DeleteExpiredSessions(context.Context, time.Time) error
	AddAudit(context.Context, auditEvent) error
	AllowRateLimit(context.Context, string, int, time.Duration) (bool, error)
	Ping(context.Context) error
}

type postgresStore struct {
	db *pgxpool.Pool
}

func newPostgresStore(db *pgxpool.Pool) *postgresStore {
	return &postgresStore{db: db}
}

func (s *postgresStore) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
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
	return cameras, rows.Err()
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

func (s *postgresStore) UpdateCameraName(ctx context.Context, userID, cameraID, name string) error {
	_, err := s.Camera(ctx, userID, cameraID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		UPDATE cameras
		SET name = $1
		WHERE id = $2 AND user_id = $3
	`, strings.TrimSpace(name), cameraID, userID)
	return err
}

func (s *postgresStore) DeleteCamera(ctx context.Context, userID, cameraID string) error {
	_, err := s.Camera(ctx, userID, cameraID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		DELETE FROM cameras
		WHERE id = $1 AND user_id = $2
	`, cameraID, userID)
	return err
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

func (s *postgresStore) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

func (s *postgresStore) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now)
	return err
}

func (s *postgresStore) AddAudit(ctx context.Context, event auditEvent) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO audit_events (id, user_id, camera_id, event, created_at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5)
	`, event.ID, event.UserID, event.CameraID, event.Event, event.CreatedAt)
	return err
}

func (s *postgresStore) AllowRateLimit(ctx context.Context, key string, max int, window time.Duration) (bool, error) {
	now := time.Now()
	cutoff := now.Add(-window)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `DELETE FROM rate_limit_events WHERE created_at < $1`, cutoff)
	if err != nil {
		return false, err
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM rate_limit_events WHERE key = $1 AND created_at >= $2`, key, cutoff).Scan(&count)
	if err != nil {
		return false, err
	}
	if count >= max {
		return false, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `INSERT INTO rate_limit_events (id, key, created_at) VALUES ($1, $2, $3)`, randomID(), key, now)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
