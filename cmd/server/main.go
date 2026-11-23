// Command server runs BOTLINK as a single Go binary: REST API + SQLite
// storage + the built React frontend, ready for Railway / Docker.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
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
	tokenKeyStr := os.Getenv("TOKEN_ENCRYPTION_KEY")
	devMode := os.Getenv("BOTLINK_DEV") == "1"
	ephemeral := false
	var secret []byte
	production := os.Getenv("FLY_APP_NAME") != "" || os.Getenv("APP_ENV") == "production" || os.Getenv("RAILWAY_ENVIRONMENT") != ""
	secret, tokenKey, ephemeral, err := resolveConfig(secretStr, tokenKeyStr, adminPassword, production, devMode)
	if err != nil {
		logger.Fatalf("configuration error: %v", err)
	}
	if ephemeral {
		logger.Println("WARNING: SESSION_SECRET is not set — using an ephemeral random secret; admin sessions will not survive restarts. Set SESSION_SECRET in production.")
	}
	if tokenKeyStr == "" {
		logger.Println("WARNING: BOTLINK_DEV=1 enabled — deriving development TOKEN_ENCRYPTION_KEY.")
	}
	if adminPassword == "" {
		logger.Println("WARNING: ADMIN_PASSWORD is not set — admin login is disabled until it is set.")
	}

	clientIPHeader := os.Getenv("CLIENT_IP_HEADER")
	if os.Getenv("RAILWAY_ENVIRONMENT") != "" && clientIPHeader == "" {
		logger.Println("WARNING: running on Railway (RAILWAY_ENVIRONMENT is set) but CLIENT_IP_HEADER is not set. Set CLIENT_IP_HEADER=X-Real-IP so real client IPs can be determined accurately.")
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

	st, err := store.Open(dbPath, tokenKey)
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

	cookieSecure := os.Getenv("COOKIE_SECURE") == "1"

	srv := server.New(server.Config{
		Addr:                   ":" + port,
		DBPath:                 dbPath,
		FrontendDistDir:        frontendDir,
		AdminPassword:          adminPassword,
		SessionSecret:          secret,
		SessionSecretEphemeral: ephemeral,
		ClientIPHeader:         clientIPHeader,
		TrustedProxyHeader:     trustedHeader,
		TrustedProxies:         trustedProxies,
		TrustHeaderAlways:      trustAlways,
		TurnTimeoutSec:         turnTimeoutSec,
		Production:             production,
		CookieSecure:           cookieSecure,
	}, st)
	st.SetTurnTimeout(turnTimeoutSec)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.StartTurnSweeper(ctx)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Printf("listening on :%s (db=%s, frontend=%s)", port, dbPath, frontendDir)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	// End live event streams first so graceful shutdown is not held open.
	srv.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}

func deriveDevTokenKey(secret []byte) []byte {
	salt := []byte("botlink-dev-token-encryption-key-salt")
	info := []byte("botlink:token-encryption-key:v1")
	// HKDF-Extract: PRK = HMAC-SHA256(salt, secret)
	hExtract := hmac.New(sha256.New, salt)
	hExtract.Write(secret)
	prk := hExtract.Sum(nil)

	// HKDF-Expand: OKM = HMAC-SHA256(PRK, info || 0x01)
	hExpand := hmac.New(sha256.New, prk)
	hExpand.Write(info)
	hExpand.Write([]byte{0x01})
	return hExpand.Sum(nil)
}

func resolveConfig(secretStr, tokenKeyStr, adminPassword string, production, devMode bool) (secret []byte, tokenKey []byte, ephemeral bool, err error) {
	if production && devMode {
		return nil, nil, false, errors.New("BOTLINK_DEV cannot be enabled in production environment")
	}

	if secretStr == "" {
		if production {
			return nil, nil, false, errors.New("SESSION_SECRET is required in production")
		}
		generated, gerr := server.GenerateEphemeralSecret()
		if gerr != nil {
			return nil, nil, false, fmt.Errorf("generate session secret: %w", gerr)
		}
		secret = generated
		ephemeral = true
	} else {
		if production && len(secretStr) < 32 {
			return nil, nil, false, errors.New("SESSION_SECRET must be at least 32 characters in production")
		}
		secret = []byte(secretStr)
	}

	if tokenKeyStr == "" {
		if !devMode {
			return nil, nil, false, errors.New("TOKEN_ENCRYPTION_KEY is required (at least 32 characters). Set BOTLINK_DEV=1 to allow dev key derivation.")
		}
		tokenKey = deriveDevTokenKey(secret)
	} else {
		if len(tokenKeyStr) < 32 {
			return nil, nil, false, errors.New("TOKEN_ENCRYPTION_KEY must be at least 32 characters")
		}
		tokenKey = []byte(tokenKeyStr)
	}

	if adminPassword == "" {
		if production {
			return nil, nil, false, errors.New("ADMIN_PASSWORD is required in production")
		}
	} else if production && len(adminPassword) < 16 {
		return nil, nil, false, errors.New("ADMIN_PASSWORD must be at least 16 characters in production")
	}

	return secret, tokenKey, ephemeral, nil
}
