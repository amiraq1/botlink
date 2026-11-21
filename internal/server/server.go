// Package server is the unified Go HTTP server: REST API + static React
// frontend + health check, in one binary.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

type Config struct {
	Addr            string // e.g. ":8080"
	DBPath          string
	FrontendDistDir string
	AdminPassword   string
	SessionSecret   []byte
	// SessionSecretEphemeral is true when the secret was generated at
	// startup because SESSION_SECRET was unset (dev only).
	SessionSecretEphemeral bool
}

type Server struct {
	cfg           Config
	store         *store.Store
	mux           *http.ServeMux
	log           *log.Logger
	adminPassword string
	sessionSecret []byte

	loginMu      sync.Mutex
	loginAttempt map[string][]time.Time
}

func New(cfg Config, st *store.Store) *Server {
	s := &Server{
		cfg:           cfg,
		store:         st,
		mux:           http.NewServeMux(),
		log:           log.New(os.Stdout, "botlink: ", log.LstdFlags),
		adminPassword: cfg.AdminPassword,
		sessionSecret: cfg.SessionSecret,
		loginAttempt:  map[string][]time.Time{},
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)

	// Auth (public).
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.handleMe)

	// Public agent room: never requires auth, never leaks tokens.
	s.mux.HandleFunc("GET /api/room/{token}", s.handleRoomState)
	s.mux.HandleFunc("POST /api/room/{token}/reply", s.handleRoomReply)

	// Admin API: cookie auth enforced on every route.
	s.mux.HandleFunc("GET /api/admin/groups", s.requireAdmin(s.handleListGroups))
	s.mux.HandleFunc("POST /api/admin/groups", s.requireAdmin(s.handleCreateGroup))
	s.mux.HandleFunc("GET /api/admin/groups/{id}", s.requireAdmin(s.handleGetGroup))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/leader-message", s.requireAdmin(s.handleLeaderMessage))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/skip", s.requireAdmin(s.handleSkip))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/pause", s.requireAdmin(s.handlePause))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/resume", s.requireAdmin(s.handleResume))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/stop", s.requireAdmin(s.handleStop))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/turn-mode", s.requireAdmin(s.handleSetTurnMode))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/max-turns", s.requireAdmin(s.handleSetMaxTurns))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/members", s.requireAdmin(s.handleAddMember))
	s.mux.HandleFunc("DELETE /api/admin/groups/{id}/members/{botId}", s.requireAdmin(s.handleRemoveMember))
	s.mux.HandleFunc("POST /api/admin/groups/{id}/members/{botId}/rotate-token", s.requireAdmin(s.handleRotateToken))

	// Static frontend + SPA fallback (everything else).
	s.mux.HandleFunc("/", s.handleStatic)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func nowMs() int64 { return time.Now().UnixMilli() }

func (s *Server) groupID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func (s *Server) handleStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrValidation):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "validation error: "))
	default:
		s.log.Printf("store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// securityHeaders applies the headers required on every response.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self'; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthenticated(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Health + auth
// ---------------------------------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginAllowed is a basic per-IP rate limit: 10 attempts per minute.
func (s *Server) loginAllowed(ip string, now time.Time) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	windowStart := now.Add(-time.Minute)
	attempts := s.loginAttempt[ip]
	kept := attempts[:0]
	for _, t := range attempts {
		if t.After(windowStart) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 10 {
		s.loginAttempt[ip] = kept
		return false
	}
	s.loginAttempt[ip] = append(kept, now)
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminPassword == "" {
		writeError(w, http.StatusServiceUnavailable, "admin login is not configured: set ADMIN_PASSWORD")
		return
	}
	if !s.loginAllowed(clientIP(r), time.Now()) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !constantTimeEqual(body.Password, s.adminPassword) {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	setSessionCookie(w, r, s.sessionSecret, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": s.isAuthenticated(r)})
}

// ---------------------------------------------------------------------------
// Public room
// ---------------------------------------------------------------------------

func (s *Server) handleRoomState(w http.ResponseWriter, r *http.Request) {
	state, err := s.store.GetRoomState(r.Context(), r.PathValue("token"), nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if state == nil {
		// Unknown token: JSON null, mirroring the old ?RoomState.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("null"))
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleRoomReply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedTurnSeq int64  `json:"expectedTurnSeq"`
		Content         string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	accepted, err := s.store.SubmitBotReply(r.Context(), r.PathValue("token"), body.ExpectedTurnSeq, body.Content, nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	// Stale/duplicate submits return {accepted:false} with HTTP 200, never an error.
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

// ---------------------------------------------------------------------------
// Admin groups
// ---------------------------------------------------------------------------

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context(), nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var in store.CreateGroupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	id, err := s.store.CreateGroup(r.Context(), in, nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	detail, err := s.store.GetGroupDetail(r.Context(), id, nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleLeaderMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.SendLeaderMessage(r.Context(), id, body.Text, nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) simpleGroupAction(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, id int64, now int64) error) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if err := fn(r.Context(), id, nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	s.simpleGroupAction(w, r, s.store.SkipCurrentTurn)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.simpleGroupAction(w, r, s.store.PauseGroup)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.simpleGroupAction(w, r, s.store.ResumeGroup)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.simpleGroupAction(w, r, s.store.StopGroup)
}

func (s *Server) handleSetTurnMode(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.SetTurnMode(r.Context(), id, body.Mode, nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetMaxTurns(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var body struct {
		MaxTurns int64 `json:"maxTurns"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.SetMaxTurns(r.Context(), id, body.MaxTurns, nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var in store.MemberInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.AddMember(r.Context(), id, in, nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if err := s.store.RemoveMember(r.Context(), id, r.PathValue("botId"), nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	token, err := s.store.RotateToken(r.Context(), id, r.PathValue("botId"), nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agentToken": token})
}

// ---------------------------------------------------------------------------
// Static frontend (SPA fallback)
// ---------------------------------------------------------------------------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dist := s.cfg.FrontendDistDir
	upath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	// Never serve outside the dist dir.
	candidate := filepath.Join(dist, filepath.FromSlash(upath))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		http.ServeFile(w, r, candidate)
		return
	}
	index := filepath.Join(dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeError(w, http.StatusServiceUnavailable, "frontend not built: run pnpm build first")
		return
	}
	http.ServeFile(w, r, index)
}

// StartMockSweeper runs the 15-second mock-reply safety net until ctx ends.
func (s *Server) StartMockSweeper(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.SweepDueMocks(ctx, nowMs()); err != nil {
				s.log.Printf("mock sweep error: %v", err)
			}
		}
	}
}
