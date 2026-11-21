// Command server runs BOTLINK as a single Go binary: REST API + SQLite
// storage + the built React frontend, ready for Railway / Docker.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	if secretStr == "" {
		secret, _ = server.GenerateEphemeralSecret()
		ephemeral = true
		logger.Println("WARNING: SESSION_SECRET is not set — using an ephemeral random secret; admin sessions will not survive restarts. Set SESSION_SECRET in production.")
	} else {
		secret = []byte(secretStr)
	}
	if adminPassword == "" {
		logger.Println("WARNING: ADMIN_PASSWORD is not set — admin login is disabled until it is set.")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		logger.Fatalf("open database: %v", err)
	}
	defer st.Close()

	srv := server.New(server.Config{
		Addr:                   ":" + port,
		DBPath:                 dbPath,
		FrontendDistDir:        frontendDir,
		AdminPassword:          adminPassword,
		SessionSecret:          secret,
		SessionSecretEphemeral: ephemeral,
	}, st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.StartMockSweeper(ctx)

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
