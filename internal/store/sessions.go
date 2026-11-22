package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// NewSessionID returns a fresh 128-bit random id for a signed admin cookie.
func NewSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateAdminSession persists a session so logout can revoke it server-side.
func (s *Store) CreateAdminSession(ctx context.Context, id string, createdAt, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO admin_sessions (id, created_at, expires_at, revoked_at) VALUES (?, ?, ?, NULL)",
		id, createdAt, expiresAt)
	return err
}

// RevokeAdminSession marks one session invalid without deleting the audit row.
func (s *Store) RevokeAdminSession(ctx context.Context, id string, nowMs int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE admin_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", nowMs, id)
	return err
}

// RevokeAllAdminSessions invalidates every live session at once.
func (s *Store) RevokeAllAdminSessions(ctx context.Context, nowMs int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE admin_sessions SET revoked_at = ? WHERE revoked_at IS NULL", nowMs)
	return err
}

// IsAdminSessionValid fails closed on any database error.
func (s *Store) IsAdminSessionValid(ctx context.Context, id string, nowMs int64) bool {
	var revoked any
	var expiresAt int64
	err := s.ro.QueryRowContext(ctx,
		"SELECT expires_at, revoked_at FROM admin_sessions WHERE id = ?", id).
		Scan(&expiresAt, &revoked)
	if err != nil {
		return false
	}
	return revoked == nil && expiresAt > nowMs
}

// DeleteExpiredSessions keeps the session table bounded.
func (s *Store) DeleteExpiredSessions(ctx context.Context, nowMs int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM admin_sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL", nowMs)
	return err
}
