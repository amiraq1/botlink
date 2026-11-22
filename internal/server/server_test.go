package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestServer(t *testing.T, adminPassword string) (*Server, *store.Store) {
	t.Helper()
	st := openTestStore(t)
	secret, err := GenerateEphemeralSecret()
	if err != nil {
		t.Fatalf("GenerateEphemeralSecret: %v", err)
	}
	srv := New(Config{
		Addr: ":0", DBPath: filepath.Join(t.TempDir(), "x.db"),
		FrontendDistDir: t.TempDir(), AdminPassword: adminPassword,
		SessionSecret: secret,
	}, st)
	return srv, st
}

func doReq(t *testing.T, srv *Server, method, target string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, rdr)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	rec := doReq(t, srv, "POST", "/api/auth/login", map[string]string{"password": "secret123"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login response set no session cookie")
	return nil
}

func TestHealthzAndSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	rec := doReq(t, srv, "GET", "/healthz", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q; want 200 ok", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", h.Get("Referrer-Policy"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", h.Get("X-Content-Type-Options"))
	}
	if h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q", h.Get("X-Frame-Options"))
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "http") {
		t.Errorf("CSP = %q; want strict self-only policy", csp)
	}
}

func TestAdminAuthEnforcement(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	// No cookie -> 401 on every admin route.
	for _, target := range []string{
		"GET /api/admin/groups",
		"POST /api/admin/groups",
	} {
		parts := strings.SplitN(target, " ", 2)
		rec := doReq(t, srv, parts[0], parts[1], nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d; want 401", target, rec.Code)
		}
	}
	// Wrong password -> 401, no cookie.
	rec := doReq(t, srv, "POST", "/api/auth/login", map[string]string{"password": "wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong login = %d; want 401", rec.Code)
	}
	// Correct password -> cookie, then admin routes work.
	cookie := login(t, srv)
	rec = doReq(t, srv, "GET", "/api/admin/groups", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed list = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	// Tampered cookie -> 401.
	bad := *cookie
	bad.Value = bad.Value + "x"
	rec = doReq(t, srv, "GET", "/api/admin/groups", nil, &bad)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered cookie = %d; want 401", rec.Code)
	}
	// Logout clears the session.
	rec = doReq(t, srv, "POST", "/api/auth/logout", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d; want 200", rec.Code)
	}
}

func TestLoginRequiresAdminPassword(t *testing.T) {
	srv, _ := newTestServer(t, "")
	rec := doReq(t, srv, "POST", "/api/auth/login", map[string]string{"password": "anything"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("login without ADMIN_PASSWORD = %d; want 503", rec.Code)
	}
}

func TestRoomNeverLeaksTokensOverHTTP(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "G", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: 20,
		Members: []store.MemberInput{
			{Name: "Alpha", AccentColor: "#3B82F6"},
			{Name: "Beta", AccentColor: "#A855F7"},
		},
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("get group = %d", rec.Code)
	}
	var detail store.GroupDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	tokenA := detail.Members[0].AgentToken
	tokenB := detail.Members[1].AgentToken
	// Public room for Alpha must not contain Beta's (or Alpha's own) token.
	rec = doReq(t, srv, "GET", "/api/room/"+tokenA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("room = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, tokenA) || strings.Contains(body, tokenB) {
		t.Fatalf("room response leaks a token: %s", body)
	}
	if !strings.Contains(body, "Alpha") {
		t.Fatalf("room response missing bot name: %s", body)
	}
	// Unknown token -> JSON null, HTTP 200.
	rec = doReq(t, srv, "GET", "/api/room/deadbeefdeadbeefdeadbeefdeadbeef", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("unknown room = %d %q; want 200 null", rec.Code, rec.Body.String())
	}
	// Rotate token via admin; old token dies.
	rec = doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id)+"/members/bot_1/rotate-token", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d, %s", rec.Code, rec.Body.String())
	}
	var rotated struct {
		AgentToken string `json:"agentToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("unmarshal rotate: %v", err)
	}
	if rotated.AgentToken == tokenA || len(rotated.AgentToken) != 32 {
		t.Fatalf("rotated token = %q; want fresh 32-hex", rotated.AgentToken)
	}
	rec = doReq(t, srv, "GET", "/api/room/"+tokenA, nil)
	if strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("old token still works after rotation: %s", rec.Body.String())
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestClientIPTrustBoundaries(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs([]string{"127.0.0.1/32"})

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := srv.clientIP(req); got != "203.0.113.10" {
		t.Fatalf("untrusted peer clientIP = %q; want RemoteAddr", got)
	}

	req = httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := srv.clientIP(req); got != "5.6.7.8" {
		t.Fatalf("trusted peer clientIP = %q; want rightmost untrusted hop", got)
	}

	srv.trustedProxyHeader = "Fly-Client-IP"
	req.Header.Set("Fly-Client-IP", "198.51.100.7")
	if got := srv.clientIP(req); got != "198.51.100.7" {
		t.Fatalf("trusted header clientIP = %q; want Fly-Client-IP", got)
	}
}

func TestSecureCookieRequiresTrustedProxy(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs([]string{"127.0.0.1/32"})

	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	srv.setSessionCookie(w, req, "value")
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("untrusted peer cookie Secure = %v; want false", cookies)
	}

	// On Fly the edge is always trusted, so Fly-Client-IP plus XFP must keep Secure.
	srv.trustedProxyHeader = "Fly-Client-IP"
	srv.trustHeaderAlways = true
	req.Header.Set("Fly-Client-IP", "198.51.100.7")
	w = httptest.NewRecorder()
	srv.setSessionCookie(w, req, "value")
	if cookies := w.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("trusted Fly edge cookie Secure = %v; want true", cookies)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !constantTimeEqual("same", "same") {
		t.Fatal("equal passwords must match")
	}
	if constantTimeEqual("a", "a longer password") {
		t.Fatal("different-length passwords must not match")
	}
	if constantTimeEqual("abc", "abd") {
		t.Fatal("same-length different passwords must not match")
	}
	if !constantTimeEqual("", "") {
		t.Fatal("empty passwords must match")
	}
}

func TestRoomRateLimitAndRetryAfter(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "G", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "A"}, {Name: "B"}},
	}, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	token := detail.Members[0].AgentToken

	statuses := make([]int, 0, 61)
	var retry string
	for i := 0; i < 61; i++ {
		req := httptest.NewRequest("GET", "/api/room/"+token, nil)
		req.RemoteAddr = "198.51.100.20:1234"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		statuses = append(statuses, w.Code)
		retry = w.Header().Get("Retry-After")
	}
	if statuses[0] != 200 || statuses[59] != 200 || statuses[60] != 429 {
		t.Fatalf("room GET statuses: first=%d 60th=%d 61st=%d", statuses[0], statuses[59], statuses[60])
	}
	if retry == "" {
		t.Fatal("429 response missing Retry-After")
	}
}

func TestSessionRevocation(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	a := login(t, srv)
	b := login(t, srv)

	if rec := doReq(t, srv, "GET", "/api/admin/groups", nil, a); rec.Code != 200 {
		t.Fatalf("session A before logout = %d", rec.Code)
	}
	if rec := doReq(t, srv, "POST", "/api/auth/logout", nil, a); rec.Code != 200 {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := doReq(t, srv, "GET", "/api/admin/groups", nil, a); rec.Code != 401 {
		t.Fatalf("replayed session after logout = %d; want 401", rec.Code)
	}
	if rec := doReq(t, srv, "POST", "/api/admin/sessions/revoke-all", nil, b); rec.Code != 200 {
		t.Fatalf("revoke-all = %d", rec.Code)
	}
	if rec := doReq(t, srv, "GET", "/api/admin/groups", nil, b); rec.Code != 401 {
		t.Fatalf("session B after revoke-all = %d; want 401", rec.Code)
	}
}
