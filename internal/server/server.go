// Package server is the unified Go HTTP server: REST API + static React
// frontend + health check, in one binary.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// TrustedProxyHeader names a header set by the edge (e.g. Fly-Client-IP).
	TrustedProxyHeader string
	// TrustedProxies are CIDRs allowed to forward X-Forwarded-For.
	TrustedProxies []*net.IPNet
	// TrustHeaderAlways skips the peer check; only enable when every
	// request is guaranteed to pass through the trusted edge.
	TrustHeaderAlways bool
	TurnTimeoutSec    int64
}

type Server struct {
	cfg           Config
	store         *store.Store
	mux           *http.ServeMux
	log           *log.Logger
	adminPassword string
	sessionSecret []byte

	trustedProxyHeader string
	trustedProxies     []*net.IPNet
	trustHeaderAlways  bool

	loginMu      sync.Mutex
	loginAttempt map[string][]time.Time

	roomGETLimiter  *windowLimiter
	roomPOSTLimiter *windowLimiter
	roomIPLimiter   *windowLimiter

	broker       *eventBroker
	sched        *turnScheduler
	streamCtx    context.Context
	streamCancel context.CancelFunc
}

const (
	maxEventSubscribers         = 512
	maxEventSubscribersPerGroup = 128
	streamHeartbeat             = 20 * time.Second
	streamLifetime              = 2 * time.Minute
	streamWriteDeadline         = 30 * time.Second
	streamRetryMs               = 3000
)

func New(cfg Config, st *store.Store) *Server {
	s := &Server{
		cfg:                cfg,
		store:              st,
		mux:                http.NewServeMux(),
		log:                log.New(os.Stdout, "botlink: ", log.LstdFlags),
		adminPassword:      cfg.AdminPassword,
		sessionSecret:      cfg.SessionSecret,
		trustedProxyHeader: cfg.TrustedProxyHeader,
		trustedProxies:     cfg.TrustedProxies,
		trustHeaderAlways:  cfg.TrustHeaderAlways,
		loginAttempt:       map[string][]time.Time{},
		roomGETLimiter:     newWindowLimiter(60, time.Minute, 8192),
		roomPOSTLimiter:    newWindowLimiter(10, time.Minute, 8192),
		roomIPLimiter:      newWindowLimiter(180, time.Minute, 8192),
	}
	s.streamCtx, s.streamCancel = context.WithCancel(context.Background())
	s.broker = newEventBroker(maxEventSubscribers, maxEventSubscribersPerGroup)
	s.sched = newTurnScheduler(st, s.turnTimeoutMs, s.log)
	st.SetChangeHook(s.onGroupChange)
	if len(s.trustedProxies) == 0 {
		s.trustedProxies = parseCIDRs(DefaultTrustedProxies)
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.gzipMiddleware(s.securityHeaders(s.mux))
}

// Close stops live event streams and releases their subscriptions. Call it
// before http.Server.Shutdown so graceful shutdown is not held open.
func (s *Server) Close() {
	if s.streamCancel != nil {
		s.streamCancel()
	}
	if s.broker != nil {
		s.broker.closeAll()
	}
}

func (s *Server) onGroupChange(groupID int64) {
	s.broker.publish(groupID)
	s.sched.wake()
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
	s.mux.HandleFunc("GET /api/room/{token}/events", s.handleRoomEvents)

	// Admin API: cookie auth enforced on every route.
	s.mux.HandleFunc("GET /api/admin/groups", s.requireAdmin(s.handleListGroups))
	s.mux.HandleFunc("POST /api/admin/groups", s.requireAdmin(s.handleCreateGroup))
	s.mux.HandleFunc("GET /api/admin/groups/{id}", s.requireAdmin(s.handleGetGroup))
	s.mux.HandleFunc("GET /api/admin/groups/{id}/events", s.requireAdmin(s.handleGroupEvents))
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
	s.mux.HandleFunc("POST /api/admin/sessions/revoke-all", s.requireAdmin(s.handleRevokeAllSessions))

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

// tokenBucketKey never stores the raw agent token in the limiter map.
func tokenBucketKey(ip, token string) string {
	sum := sha256.Sum256([]byte(token))
	return ip + "|" + hex.EncodeToString(sum[:8])
}

// roomRateOK applies the per-(IP, token) limits and a global per-IP cap, and
// writes a 429 with Retry-After when any of them is exhausted.
func (s *Server) roomRateOK(w http.ResponseWriter, r *http.Request, token string, post bool) bool {
	ip := s.clientIP(r)
	now := time.Now()
	key := tokenBucketKey(ip, token)
	checks := []*windowLimiter{s.roomIPLimiter}
	if post {
		checks = append(checks, s.roomPOSTLimiter)
	} else {
		checks = append(checks, s.roomGETLimiter)
	}
	for _, limiter := range checks {
		limitKey := key
		if limiter == s.roomIPLimiter {
			limitKey = ip
		}
		if ok, retry := limiter.allow(limitKey, now); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
			return false
		}
	}
	return true
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
	if !s.loginAllowed(s.clientIP(r), time.Now()) {
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
	sessionID, err := store.NewSessionID()
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	now := time.Now()
	if err := s.store.CreateAdminSession(r.Context(), sessionID,
		now.UnixMilli(), now.Add(sessionTTL).UnixMilli()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.setSessionCookie(w, r, newSessionToken(s.sessionSecret, sessionID, now))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if id, ok := verifySessionToken(s.sessionSecret, c.Value, time.Now()); ok {
			_ = s.store.RevokeAdminSession(r.Context(), id, nowMs())
		}
	}
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": s.isAuthenticated(r)})
}

// ---------------------------------------------------------------------------
// Public room
// ---------------------------------------------------------------------------

func (s *Server) handleRoomState(w http.ResponseWriter, r *http.Request) {
	if !s.roomRateOK(w, r, r.PathValue("token"), false) {
		return
	}
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
	writeJSONConditional(w, r, roomETagSeed(state), state)
}

// roomETagSeed fingerprints every visible field of a room response, so a 304
// is only returned when nothing the agent can see has changed.
func roomETagSeed(state *store.RoomState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%d|%s|%s|%s|%s|%s|%t|%d|",
		state.Status, state.TurnSeq, state.MaxTurns, state.BotID, state.BotName,
		state.RoleTitle, state.GroupName, state.Topic, state.IsYourTurn, len(state.Messages))
	for _, name := range state.OnlineNames {
		b.WriteString(name)
		b.WriteByte(0)
	}
	if n := len(state.Messages); n > 0 {
		fmt.Fprintf(&b, "%d|%d", state.Messages[0].ID, state.Messages[n-1].ID)
	}
	return b.String()
}

func (s *Server) handleRoomReply(w http.ResponseWriter, r *http.Request) {
	if !s.roomRateOK(w, r, r.PathValue("token"), true) {
		return
	}
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
	beforeID, afterID, limit := int64(0), int64(0), int64(100)
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		beforeID, _ = strconv.ParseInt(raw, 10, 64)
	}
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		afterID, _ = strconv.ParseInt(raw, 10, 64)
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 100
	}
	detail, err := s.store.GetGroupDetailPaged(r.Context(), id, nowMs(), beforeID, afterID, limit)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSONConditional(w, r, groupDetailETagSeed(id, beforeID, afterID, detail), detail)
}

// groupDetailETagSeed fingerprints the admin group view. lastSeenAt is
// deliberately excluded: the UI renders the boolean online flag, so presence
// write churn must not invalidate the cache, while a change in online state
// does.
func groupDetailETagSeed(id, beforeID, afterID int64, d *store.GroupDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%d|%d|%d|%s|%s|%s|%s|%s|%d|%d|",
		id, beforeID, afterID, d.Group.ID, d.Group.Name, d.Group.Description,
		d.Group.Topic, d.Group.Status, d.Group.TurnMode, d.Group.MaxTurns, d.Group.TurnSeq)
	if d.Group.CurrentTurnBotID != nil {
		b.WriteString(*d.Group.CurrentTurnBotID)
	}
	b.WriteByte('|')
	for _, queued := range d.Group.TurnQueue {
		b.WriteString(queued)
		b.WriteByte(',')
	}
	fmt.Fprintf(&b, "|%d|%d|%d|%d|", d.Group.MockDelaySec, d.Group.TurnStartedAt,
		d.Group.SkipStreak, d.Group.UpdatedAt)
	for _, m := range d.Members {
		fmt.Fprintf(&b, "%s:%s:%s:%s:%t:%t:%s|", m.BotID, m.Name, m.RoleTitle,
			m.AccentColor, m.IsMock, m.Online, m.AgentToken)
	}
	fmt.Fprintf(&b, "%t|%d|", d.HasMore, len(d.Messages))
	if n := len(d.Messages); n > 0 {
		fmt.Fprintf(&b, "%d|%d", d.Messages[0].ID, d.Messages[n-1].ID)
	}
	return b.String()
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
	if strings.HasPrefix(upath, "/assets/") {
		// Vite content-hashes every asset file name.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
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

func (s *Server) handleRevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeAllAdminSessions(r.Context(), nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// StartTurnSweeper starts the precise turn scheduler (mock replies and
// silent-bot timeouts at their exact due time) and the 15-second safety net
// for sessions, retention and any event the scheduler missed.
func (s *Server) StartTurnSweeper(ctx context.Context) {
	go s.sched.run(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.SweepTurns(ctx, nowMs(), s.turnTimeoutMs()); err != nil {
				s.log.Printf("turn sweep error: %v", err)
			}
		}
	}
}

func (s *Server) turnTimeoutMs() int64 { return s.cfg.TurnTimeoutSec * 1000 }
