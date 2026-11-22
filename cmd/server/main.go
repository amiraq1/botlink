// Command server runs BOTLINK as a single Go binary: REST API + SQLite
// storage + the built React frontend, ready for Railway / Docker.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/amiraq1/botlink/internal/server"
	"github.com/amiraq1/botlink/internal/store"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	logger := log.New(os.Stdout, "botlink: ", log.LstdFlags)

	port := envOr("PORT", "8080")
	dbPath := envOr("DB_PATH", "./data/botlink.db")
	frontendDir := envOr("FRONTEND_DIST_DIR", "./src/frontend/dist")
	adminPassword := os.Getenv("ADMIN_PASSWORD")

	secretStr := os.Getenv("SESSION_SECRET")
	ephemeral := false
	var secret []byte
	production := os.Getenv("FLY_APP_NAME") != "" || os.Getenv("APP_ENV") == "production"
	if secretStr == "" {
		generated, err := server.GenerateEphemeralSecret()
		if err != nil {
			logger.Fatalf("generate session secret: %v", err)
		}
		if production {
			logger.Fatal("SESSION_SECRET is required in production")
		}
		secret = generated
		ephemeral = true
		logger.Println("WARNING: SESSION_SECRET is not set — using an ephemeral random secret; admin sessions will not survive restarts. Set SESSION_SECRET in production.")
	} else {
		if production && len(secretStr) < 32 {
			logger.Fatal("SESSION_SECRET must be at least 32 characters in production")
		}
		secret = []byte(secretStr)
	}
	if adminPassword == "" {
		if production {
			logger.Fatal("ADMIN_PASSWORD is required in production")
		}
		logger.Println("WARNING: ADMIN_PASSWORD is not set — admin login is disabled until it is set.")
	}

	if raw := os.Getenv("MAX_TURNS_LIMIT"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 1 {
			store.SetMaxTurnsLimit(n)
		} else {
			logger.Fatal("MAX_TURNS_LIMIT must be a positive integer")
		}
	}
	if raw := os.Getenv("MESSAGE_RETENTION"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 100 {
			logger.Fatal("MESSAGE_RETENTION must be at least 100")
		}
		store.SetMessageRetention(n)
	}
	turnTimeoutSec := int64(store.DefaultTurnTimeoutSec)
	if raw := os.Getenv("TURN_TIMEOUT_SEC"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			logger.Fatal("TURN_TIMEOUT_SEC must be a positive integer")
		}
		turnTimeoutSec = n
	}

	st, err := store.Open(dbPath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer st.Close()

	trustedHeader := os.Getenv("TRUSTED_PROXY_HEADER")
	trustAlways := false
	if os.Getenv("FLY_APP_NAME") != "" && trustedHeader == "" {
		trustedHeader = "Fly-Client-IP"
		// Fly always terminates TLS in front of the app, so its header can
		// be trusted even when the proxy peer is outside our CIDR list.
		trustAlways = true
	}
	var trustedProxies []*net.IPNet
	if raw := os.Getenv("TRUSTED_PROXIES"); raw != "" {
		trustedProxies = server.ParseCIDRs(strings.Split(raw, ","))
	}

	srv := server.New(server.Config{
		Addr:                   ":" + port,
		DBPath:                 dbPath,
		FrontendDistDir:        frontendDir,
		AdminPassword:          adminPassword,
		SessionSecret:          secret,
		SessionSecretEphemeral: ephemeral,
		TrustedProxyHeader:     trustedHeader,
		TrustedProxies:         trustedProxies,
		TrustHeaderAlways:      trustAlways,
		TurnTimeoutSec:         turnTimeoutSec,
	}, st)
	st.SetTurnTimeout(turnTimeoutSec)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.StartTurnSweeper(ctx)

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Printf("listening on :%s (db=%s, frontend=%s)", port, dbPath, frontendDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}
