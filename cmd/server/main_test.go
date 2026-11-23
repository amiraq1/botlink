package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/server"
	"github.com/amiraq1/botlink/internal/store"
)

func TestStartupConfigValidation(t *testing.T) {
	validSecret := strings.Repeat("s", 32)
	validKey := strings.Repeat("k", 32)
	validPass := strings.Repeat("p", 16)

	// 1. Missing TOKEN_ENCRYPTION_KEY without BOTLINK_DEV fails
	_, _, _, err := resolveConfig(validSecret, "", validPass, false, false)
	if err == nil || !strings.Contains(err.Error(), "TOKEN_ENCRYPTION_KEY is required") {
		t.Fatalf("expected missing key error, got %v", err)
	}

	// 2. Short TOKEN_ENCRYPTION_KEY (< 32 chars) fails
	_, _, _, err = resolveConfig(validSecret, "short-key-less-than-32", validPass, false, true)
	if err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Fatalf("expected short key error, got %v", err)
	}

	// 3. BOTLINK_DEV combined with production must be strictly rejected
	_, _, _, err = resolveConfig(validSecret, validKey, validPass, true, true)
	if err == nil || !strings.Contains(err.Error(), "BOTLINK_DEV cannot be enabled in production") {
		t.Fatalf("expected error when BOTLINK_DEV is enabled in production, got %v", err)
	}

	// 4. Missing TOKEN_ENCRYPTION_KEY with BOTLINK_DEV=1 in development succeeds (derives key via HKDF)
	_, key, _, err := resolveConfig(validSecret, "", validPass, false, true)
	if err != nil {
		t.Fatalf("expected dev key derivation to succeed, got %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32-byte derived key, got %d bytes", len(key))
	}
	// HKDF derivation must be deterministic and domain separated
	key2 := deriveDevTokenKey([]byte(validSecret))
	if string(key) != string(key2) {
		t.Fatalf("expected deterministic HKDF key, got mismatch")
	}

	// 5. Production requires ADMIN_PASSWORD >= 16 characters
	_, _, _, err = resolveConfig(validSecret, validKey, "shortPass123", true, false)
	if err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD must be at least 16 characters in production") {
		t.Fatalf("expected short admin password error in production, got %v", err)
	}

	// 6. Production requires SESSION_SECRET >= 32 characters
	_, _, _, err = resolveConfig("shortSecret", validKey, validPass, true, false)
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET must be at least 32 characters in production") {
		t.Fatalf("expected short session secret error in production, got %v", err)
	}

	// 7. Valid production configuration succeeds
	sec, key, eph, err := resolveConfig(validSecret, validKey, validPass, true, false)
	if err != nil {
		t.Fatalf("expected valid config to succeed, got %v", err)
	}
	if eph || len(sec) < 32 || len(key) < 32 {
		t.Fatalf("unexpected config output: eph=%v, secLen=%d, keyLen=%d", eph, len(sec), len(key))
	}
}

func TestGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	dbPath := filepath.Join(t.TempDir(), "graceful.db")
	tokenKey := []byte(strings.Repeat("k", 32))
	st, err := store.Open(dbPath, tokenKey)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	secret, err := server.GenerateEphemeralSecret()
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New(server.Config{
		Addr:            ":0",
		DBPath:          dbPath,
		FrontendDistDir: t.TempDir(),
		AdminPassword:   "secret1234567890",
		SessionSecret:   secret,
		TurnTimeoutSec:  90,
	}, st)
	defer srv.Close()

	sweeperDone := make(chan struct{})
	go func() {
		srv.StartTurnSweeper(ctx)
		close(sweeperDone)
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{
		Handler: srv.Handler(),
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.Serve(ln)
	}()

	// Signal shutdown
	cancel()
	srv.Close()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("httpServer.Shutdown: %v", err)
	}

	select {
	case <-sweeperDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper did not shut down within 2 seconds")
	}

	err = <-serverErr
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("unexpected server error: %v", err)
	}
}
