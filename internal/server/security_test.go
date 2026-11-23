package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
	"go.uber.org/goleak"
)

// TestAllAdminEndpointsRejectNonAdmin asserts that every single /api/admin/*
// endpoint returns 401 Unauthorized when requested without an authenticated cookie,
// and includes no-store headers.
func TestAllAdminEndpointsRejectNonAdmin(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/api/admin/groups"},
		{"POST", "/api/admin/groups"},
		{"GET", "/api/admin/groups/1"},
		{"GET", "/api/admin/groups/1/events"},
		{"POST", "/api/admin/groups/1/leader-message"},
		{"POST", "/api/admin/groups/1/skip"},
		{"POST", "/api/admin/groups/1/pause"},
		{"POST", "/api/admin/groups/1/resume"},
		{"POST", "/api/admin/groups/1/stop"},
		{"POST", "/api/admin/groups/1/turn-mode"},
		{"POST", "/api/admin/groups/1/max-turns"},
		{"POST", "/api/admin/groups/1/members"},
		{"DELETE", "/api/admin/groups/1/members/bot_1"},
		{"POST", "/api/admin/groups/1/members/bot_1/rotate-token"},
		{"POST", "/api/admin/sessions/revoke-all"},
	}

	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d; want 401 Unauthorized", r.method, r.path, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Errorf("%s %s Cache-Control = %q; want no-store", r.method, r.path, cc)
		}
	}
}

// TestBadTokenRateLimiting verifies that scanning non-existent tokens quickly
// triggers the badTokenLimiter (429 Too Many Requests + Retry-After).
func TestBadTokenRateLimiting(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")

	ip := "198.51.100.99"
	// The badTokenLimiter limit is 15. The first 15 requests return 200 with null.
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", "/api/room/badtoken"+strconv.Itoa(i), nil)
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
			t.Fatalf("request %d: code=%d body=%s; want 200 null", i, rec.Code, rec.Body.String())
		}
	}

	// 16th request must be rejected with 429
	req := httptest.NewRequest("GET", "/api/room/badtoken15", nil)
	req.RemoteAddr = ip + ":12345"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("16th bad token status = %d; want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}

	// Subsequent SSE stream connection on bad token is also rejected with 429
	eventReq := httptest.NewRequest("GET", "/api/room/badtoken15/events", nil)
	eventReq.RemoteAddr = ip + ":12345"
	eventRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(eventRec, eventReq)

	if eventRec.Code != http.StatusTooManyRequests {
		t.Fatalf("bad token SSE stream status = %d; want 429", eventRec.Code)
	}
}

// TestForgedXForwardedForRejected verifies that an untrusted client IP attempting
// to forge X-Forwarded-For cannot spoof another IP address.
func TestForgedXForwardedForRejected(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	srv.trustedProxies = parseCIDRs([]string{"127.0.0.1/32"})

	// An attacker at 203.0.113.50 claims to be 10.0.0.1 via X-Forwarded-For
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "203.0.113.50:54321"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")

	extracted := srv.clientIP(req)
	if extracted != "203.0.113.50" {
		t.Fatalf("clientIP extracted %q from untrusted peer; want 203.0.113.50", extracted)
	}
}

// TestCSRFProtection verifies that cross-site and cross-origin requests
// on mutating endpoints are rejected with 403 Forbidden.
func TestCSRFProtection(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "CSRF Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)

	// 1. Sec-Fetch-Site: cross-site is rejected
	req := httptest.NewRequest("POST", "/api/admin/groups/"+itoa(id)+"/skip", nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site: cross-site = %d; want 403 Forbidden", rec.Code)
	}

	// 2. Mismatched Origin is rejected
	req = httptest.NewRequest("POST", "/api/admin/groups/"+itoa(id)+"/skip", nil)
	req.Host = "botlink.example.com"
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://evil-attacker.com")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Mismatched Origin = %d; want 403 Forbidden", rec.Code)
	}

	// 3. Same-origin Origin is accepted
	req = httptest.NewRequest("POST", "/api/admin/groups/"+itoa(id)+"/skip", nil)
	req.Host = "botlink.example.com"
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://botlink.example.com")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Same-origin request = %d; want 200 OK", rec.Code)
	}

	// 4. Logout with cross-site is rejected
	logoutReq := httptest.NewRequest("POST", "/api/auth/logout", nil)
	logoutReq.AddCookie(cookie)
	logoutReq.Header.Set("Sec-Fetch-Site", "cross-site")
	logoutRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusForbidden {
		t.Fatalf("Logout with cross-site = %d; want 403 Forbidden", logoutRec.Code)
	}
}

// TestCacheControlPolicies asserts proper cache headers across public rooms,
// admin endpoints, authentication, and SSE streams.
func TestCacheControlPolicies(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Cache Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)

	// Admin detail carries no-store and Vary: Cookie
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin get = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("admin Cache-Control = %q; want no-store", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Vary") != "Cookie" {
		t.Errorf("admin Vary = %q; want Cookie", rec.Header().Get("Vary"))
	}

	// Group detail payload includes member agent token
	var detail store.GroupDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	token := detail.Members[0].AgentToken

	// Room state carries private, no-cache (compatible with ETag)
	rec = doReq(t, srv, "GET", "/api/room/"+token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("room state = %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("room Cache-Control = %q; want private, no-cache", rec.Header().Get("Cache-Control"))
	}

	// Room unknown token also carries private, no-cache
	rec = doReq(t, srv, "GET", "/api/room/unknown9999", nil)
	if rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("unknown room Cache-Control = %q; want private, no-cache", rec.Header().Get("Cache-Control"))
	}

	// Room reply carries private, no-cache
	rec = doReq(t, srv, "POST", "/api/room/"+token+"/reply", map[string]any{
		"expectedTurnSeq": 1,
		"content":         "test reply",
	})
	if rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("room reply Cache-Control = %q; want private, no-cache", rec.Header().Get("Cache-Control"))
	}
}

// TestSSELimitsAndGoroutineCleanup tests the 128 group limit, 512 total limit,
// and ensures stream goroutines are cleaned up when clients disconnect,
// verified via go.uber.org/goleak.
func TestSSELimitsAndGoroutineCleanup(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
		goleak.VerifyNone(t, goleak.IgnoreCurrent())
	}()

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "SSE Group", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)

	// Open multiple streams and cancel their contexts
	const streamCount = 5
	cancels := make([]context.CancelFunc, streamCount)
	doneCh := make(chan struct{}, streamCount)

	for i := 0; i < streamCount; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel

		req := httptest.NewRequestWithContext(ctx, "GET", "/api/admin/groups/"+itoa(id)+"/events", nil)
		req.AddCookie(cookie)

		go func() {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			doneCh <- struct{}{}
		}()
	}

	// Wait for subscriptions to be registered
	eventually(t, 2*time.Second, 5*time.Millisecond, func() bool {
		return srv.broker.totalSubscribers() == streamCount
	})

	// Now cancel all client streams
	for _, cancel := range cancels {
		cancel()
	}

	for i := 0; i < streamCount; i++ {
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("stream %d did not terminate on context cancellation", i)
		}
	}
}

func TestSSEStreamExceedsWriteTimeout(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "SSE Timeout Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)

	// Spin up a real http.Server with a very short WriteTimeout (250ms)
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.WriteTimeout = 250 * time.Millisecond
	ts.Start()
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/admin/groups/"+itoa(id)+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	// Read ready event
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read ready event error: %v", err)
		}
		if strings.HasPrefix(line, "event: ready") {
			break
		}
	}

	// Sleep 350ms — longer than the 250ms WriteTimeout!
	time.Sleep(350 * time.Millisecond)

	// Trigger a group change
	if err := st.SendLeaderMessage(context.Background(), id, "hello", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	// Assert that change event is received without connection disconnect
	receivedChange := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream closed prematurely after WriteTimeout duration: %v", err)
		}
		if strings.HasPrefix(line, "event: change") {
			receivedChange = true
			break
		}
	}
	if !receivedChange {
		t.Fatal("expected change event after WriteTimeout period")
	}
}

func TestConditionalGETPreservesPresence(t *testing.T) {
	st := openTestStore(t)
	secret, err := GenerateEphemeralSecret()
	if err != nil {
		t.Fatalf("GenerateEphemeralSecret: %v", err)
	}

	var simTime int64 = 1_000_000
	srv := New(Config{
		Addr:            ":0",
		DBPath:          filepath.Join(t.TempDir(), "x.db"),
		FrontendDistDir: t.TempDir(),
		AdminPassword:   "secret123",
		SessionSecret:   secret,
		Clock:           func() int64 { return simTime },
	}, st)
	defer func() {
		srv.Close()
		st.Close()
	}()

	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Presence Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{
			{Name: "Bot1", IsMock: false},
			{Name: "Bot2", IsMock: false},
		},
	}, simTime)
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

	// Initial GET to get ETag
	req := httptest.NewRequest("GET", "/api/room/"+token, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial room get status = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag in room response")
	}

	// Poll conditionally every 10s up to 60s (> 45s presence threshold)
	for i := 0; i < 6; i++ {
		simTime += 10_000 // +10s
		req = httptest.NewRequest("GET", "/api/room/"+token, nil)
		req.Header.Set("If-None-Match", etag)
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotModified {
			t.Fatalf("iteration %d: status = %d; want 304 Not Modified", i, rec.Code)
		}
	}

	// Check that Bot1 is still online at 60s
	detailAfter, err := st.GetGroupDetail(context.Background(), id, simTime)
	if err != nil {
		t.Fatal(err)
	}
	if !detailAfter.Members[0].Online {
		t.Fatalf("bot 1 should remain Online after 60s of conditional polling; lastSeenAt=%d, simTime=%d",
			detailAfter.Members[0].LastSeenAt, simTime)
	}
}

func TestSharedIPValidTokenNeverBlockedByBadTokenLimiter(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Shared IP Group", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	validToken := detail.Members[0].AgentToken

	sharedIP := "198.51.100.55"

	// Attacker sends 20 bad tokens from sharedIP to trip badTokenLimiter
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("GET", "/api/room/badtoken"+strconv.Itoa(i), nil)
		req.RemoteAddr = sharedIP + ":54321"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
	}

	// Verify that bad tokens from sharedIP are now rejected with 429
	badReq := httptest.NewRequest("GET", "/api/room/anotherbadtoken", nil)
	badReq.RemoteAddr = sharedIP + ":54321"
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for bad token from blocked IP, got %d", badRec.Code)
	}

	// CRITICAL TEST: Legitimate bot with validToken on the EXACT SAME IP must succeed!
	legitReq := httptest.NewRequest("GET", "/api/room/"+validToken, nil)
	legitReq.RemoteAddr = sharedIP + ":54321"
	legitRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(legitRec, legitReq)
	if legitRec.Code != http.StatusOK {
		t.Fatalf("legitimate bot on shared IP blocked: got status %d, want 200 OK", legitRec.Code)
	}
}

type routeDef struct {
	pattern string
	method  string
	path    string
}

func inspectAdminRoutesAST(node ast.Node) ([]routeDef, error) {
	var adminRoutes []routeDef
	var inspectErr error
	ast.Inspect(node, func(n ast.Node) bool {
		if inspectErr != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		// Rejects Handle registration of admin routes
		if sel.Sel.Name == "Handle" {
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok {
					pattern := strings.Trim(lit.Value, `"`)
					if strings.Contains(pattern, "/api/admin/") {
						inspectErr = fmt.Errorf("admin route %q registered via Handle instead of HandleFunc", pattern)
						return false
					}
				}
			}
			return true
		}

		if sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}

		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			inspectErr = fmt.Errorf("HandleFunc called with non-literal pattern: %T", call.Args[0])
			return false
		}
		pattern := strings.Trim(lit.Value, `"`)
		if !strings.Contains(pattern, "/api/admin/") {
			return true
		}

		// Verify AST wrapper: handler must be a CallExpr to s.requireAdmin(...)
		handlerCall, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			inspectErr = fmt.Errorf("admin route %q handler is not wrapped in requireAdmin: %T", pattern, call.Args[1])
			return false
		}
		handlerSel, ok := handlerCall.Fun.(*ast.SelectorExpr)
		if !ok || handlerSel.Sel.Name != "requireAdmin" {
			inspectErr = fmt.Errorf("admin route %q handler must be wrapped in requireAdmin, got %v", pattern, handlerCall.Fun)
			return false
		}

		parts := strings.Fields(pattern)
		method := "GET"
		path := pattern
		if len(parts) == 2 {
			method = parts[0]
			path = parts[1]
		}
		adminRoutes = append(adminRoutes, routeDef{pattern: pattern, method: method, path: path})
		return true
	})

	if inspectErr != nil {
		return nil, inspectErr
	}
	return adminRoutes, nil
}

func TestAdminRoutesProtectedViaASTAndDynamicAuth(t *testing.T) {
	// Parse all package files to verify every registered admin route passes requireAdmin
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse package directory: %v", err)
	}

	var adminRoutes []routeDef
	for _, pkg := range pkgs {
		for fname, fileNode := range pkg.Files {
			routes, err := inspectAdminRoutesAST(fileNode)
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(fname), err)
			}
			adminRoutes = append(adminRoutes, routes...)
		}
	}

	if len(adminRoutes) == 0 {
		t.Fatal("AST found 0 admin routes across package files")
	}

	// Dynamic test: all discovered admin routes must reject unauthenticated requests with 401 & no-store
	srv, _ := newTestServer(t, "secret123")
	for _, r := range adminRoutes {
		testPath := r.path
		testPath = strings.ReplaceAll(testPath, "{id}", "1")
		testPath = strings.ReplaceAll(testPath, "{botId}", "bot_1")

		req := httptest.NewRequest(r.method, testPath, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s status = %d; want 401", r.method, testPath, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Errorf("%s %s Cache-Control = %q; want no-store", r.method, testPath, cc)
		}
	}
}

func TestAdminRoutesASTRejectsHandle(t *testing.T) {
	snippet := `package server
func register(s *Server) {
	s.mux.Handle("GET /api/admin/test", handler)
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "dummy.go", snippet, 0)
	if err != nil {
		t.Fatalf("parse dummy.go: %v", err)
	}

	_, err = inspectAdminRoutesAST(node)
	if err == nil {
		t.Fatal("expected AST inspection to fail on Handle registration, but it passed")
	}
	if !strings.Contains(err.Error(), "registered via Handle instead of HandleFunc") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestAdminRoutesASTRejectsNonLiteralPattern(t *testing.T) {
	snippet := `package server
func register(s *Server) {
	s.mux.HandleFunc(somePattern, handler)
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "dummy.go", snippet, 0)
	if err != nil {
		t.Fatalf("parse dummy.go: %v", err)
	}

	_, err = inspectAdminRoutesAST(node)
	if err == nil {
		t.Fatal("expected AST inspection to fail on non-literal pattern, but it passed")
	}
	if !strings.Contains(err.Error(), "HandleFunc called with non-literal pattern") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestAdminRoutesASTCatchesUnprotectedEndpoint(t *testing.T) {
	snippet := `package server
func register(s *Server) {
	s.mux.HandleFunc("GET /api/admin/unprotected", s.handleHealthz)
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "dummy.go", snippet, 0)
	if err != nil {
		t.Fatalf("parse dummy.go: %v", err)
	}

	_, err = inspectAdminRoutesAST(node)
	if err == nil {
		t.Fatal("expected AST inspection to fail on unprotected admin route, but it passed")
	}
	if !strings.Contains(err.Error(), "handler is not wrapped in requireAdmin") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRotateMemberTokenAdminOnlyAndInvalidatesOldToken(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Rotate Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	oldToken := detail.Members[0].AgentToken

	// 1. Unauthenticated rotate fails with 401
	unauthRec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id)+"/members/bot_1/rotate-token", nil)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rotate status = %d; want 401", unauthRec.Code)
	}

	// 2. Authenticated rotate succeeds and returns new token
	authRec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id)+"/members/bot_1/rotate-token", nil, cookie)
	if authRec.Code != http.StatusOK {
		t.Fatalf("authenticated rotate status = %d; want 200", authRec.Code)
	}
	var rotResp struct {
		AgentToken string `json:"agentToken"`
	}
	if err := json.Unmarshal(authRec.Body.Bytes(), &rotResp); err != nil {
		t.Fatal(err)
	}
	newToken := rotResp.AgentToken
	if newToken == "" || newToken == oldToken {
		t.Fatalf("newToken %q invalid or same as oldToken", newToken)
	}

	// 3. Old token immediately returns 200 null (not found)
	oldReq := httptest.NewRequest("GET", "/api/room/"+oldToken, nil)
	oldRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(oldRec, oldReq)
	if strings.TrimSpace(oldRec.Body.String()) != "null" {
		t.Fatalf("old token body = %q; want null", oldRec.Body.String())
	}

	// 4. New token succeeds in getting room state
	newReq := httptest.NewRequest("GET", "/api/room/"+newToken, nil)
	newRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(newRec, newReq)
	if newRec.Code != http.StatusOK || strings.TrimSpace(newRec.Body.String()) == "null" {
		t.Fatalf("new token get room state failed: code=%d body=%s", newRec.Code, newRec.Body.String())
	}
}

func TestTokenExpiryOnCompletedOrStopped(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()

	now := time.Now().UnixMilli()
	// Create group with maxTurns=1
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Expiry Test", TurnMode: "auto_round_robin", MaxTurns: 1,
		Members: []store.MemberInput{
			{Name: "Bot1", IsMock: false},
			{Name: "Bot2", IsMock: false},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	bot1Token := detail.Members[0].AgentToken

	// Submit turn 1 -> completes group because maxTurns=1
	subRec := doReq(t, srv, "POST", "/api/room/"+bot1Token+"/reply", map[string]any{
		"expectedTurnSeq": 1,
		"content":         "reply 1",
	})
	if subRec.Code != http.StatusOK {
		t.Fatalf("subRec = %d", subRec.Code)
	}

	// Check group status is now completed
	rec = doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.Group.Status != "completed" {
		t.Fatalf("expected group completed, got %s", detail.Group.Status)
	}

	// Attempting further submissions with token must return accepted: false
	sub2Rec := doReq(t, srv, "POST", "/api/room/"+bot1Token+"/reply", map[string]any{
		"expectedTurnSeq": 2,
		"content":         "reply after completed",
	})
	var sub2Resp struct {
		Accepted bool `json:"accepted"`
	}
	_ = json.Unmarshal(sub2Rec.Body.Bytes(), &sub2Resp)
	if sub2Resp.Accepted {
		t.Fatal("expected reply on completed group to be rejected (accepted: false)")
	}

	// Rotating token on completed group returns error
	rotRec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id)+"/members/bot_1/rotate-token", nil, cookie)
	if rotRec.Code != http.StatusBadRequest {
		t.Fatalf("rotate on completed group status = %d; want 400", rotRec.Code)
	}

	// Now test stopped group
	id2, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Stopped Test", TurnMode: "auto_round_robin", MaxTurns: 10,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	rec2 := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id2), nil, cookie)
	var detail2 store.GroupDetail
	_ = json.Unmarshal(rec2.Body.Bytes(), &detail2)
	botToken2 := detail2.Members[0].AgentToken

	// Stop group
	stopRec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id2)+"/stop", nil, cookie)
	if stopRec.Code != http.StatusOK {
		t.Fatalf("stop status = %d", stopRec.Code)
	}

	// Attempt reply on stopped group
	subStoppedRec := doReq(t, srv, "POST", "/api/room/"+botToken2+"/reply", map[string]any{
		"expectedTurnSeq": 1,
		"content":         "reply after stop",
	})
	var subStoppedResp struct {
		Accepted bool `json:"accepted"`
	}
	_ = json.Unmarshal(subStoppedRec.Body.Bytes(), &subStoppedResp)
	if subStoppedResp.Accepted {
		t.Fatal("expected reply on stopped group to be rejected (accepted: false)")
	}

	// Rotating token on stopped group returns error
	rotStoppedRec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(id2)+"/members/bot_1/rotate-token", nil, cookie)
	if rotStoppedRec.Code != http.StatusBadRequest {
		t.Fatalf("rotate on stopped group status = %d; want 400", rotStoppedRec.Code)
	}
}

func TestClientIPHeaderBehavior(t *testing.T) {
	// Case 1: CLIENT_IP_HEADER unset -> spoofed header is ignored, falls back to direct RemoteAddr
	srvUnset, st := newTestServer(t, "secret123")
	defer func() {
		srvUnset.Close()
		st.Close()
	}()

	req1 := httptest.NewRequest("GET", "/api/room/test", nil)
	req1.RemoteAddr = "192.0.2.1:12345"
	req1.Header.Set("X-Real-IP", "203.0.113.50")
	if ip := srvUnset.clientIP(req1); ip != "192.0.2.1" {
		t.Fatalf("unset clientIPHeader: got %q, want RemoteAddr 192.0.2.1", ip)
	}

	// Case 2: CLIENT_IP_HEADER is set to X-Real-IP -> header value is taken
	srvSet := New(Config{
		ClientIPHeader: "X-Real-IP",
		AdminPassword:  "secret123",
		SessionSecret:  make([]byte, 32),
	}, st)
	defer srvSet.Close()

	req2 := httptest.NewRequest("GET", "/api/room/test", nil)
	req2.RemoteAddr = "192.0.2.1:12345"
	req2.Header.Set("X-Real-IP", "203.0.113.50")
	if ip := srvSet.clientIP(req2); ip != "203.0.113.50" {
		t.Fatalf("configured clientIPHeader: got %q, want 203.0.113.50", ip)
	}

	// Case 3: CLIENT_IP_HEADER is set to X-Real-IP, but header is absent -> fallback to direct RemoteAddr
	req3 := httptest.NewRequest("GET", "/api/room/test", nil)
	req3.RemoteAddr = "192.0.2.1:12345"
	if ip := srvSet.clientIP(req3); ip != "192.0.2.1" {
		t.Fatalf("configured clientIPHeader with missing header: got %q, want fallback RemoteAddr 192.0.2.1", ip)
	}
}

func TestLoginParallelRateLimiting50Workers(t *testing.T) {
	srv, st := newTestServer(t, "correct-password-16-chars")
	defer func() {
		srv.Close()
		st.Close()
	}()

	const workers = 50
	var wg sync.WaitGroup
	results := make([]int, workers)
	retryAfter := make([]string, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", idx+1)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			results[idx] = rec.Code
			retryAfter[idx] = rec.Header().Get("Retry-After")
		}(i)
	}
	wg.Wait()

	count401 := 0
	count429 := 0
	for i, code := range results {
		switch code {
		case http.StatusUnauthorized:
			count401++
		case http.StatusTooManyRequests:
			count429++
			if retryAfter[i] == "" {
				t.Errorf("worker %d returned 429 without Retry-After header", i)
			}
		default:
			t.Errorf("worker %d returned unexpected status: %d", i, code)
		}
	}

	// At most 3-4 requests can be evaluated due to 3 concurrent waiters cap and 1s penalty per wrong password
	if count401 > 4 {
		t.Fatalf("expected at most 4 evaluated 401s, got %d", count401)
	}
	if count429 < 46 {
		t.Fatalf("expected at least 46 throttled 429s, got %d", count429)
	}
}

func TestLoginCorrectPasswordSucceedsPromptlyWithWaiters(t *testing.T) {
	srv, st := newTestServer(t, "correct-password-16-chars")
	defer func() {
		srv.Close()
		st.Close()
	}()

	start := time.Now()
	var wg sync.WaitGroup
	// Start 2 background wrong-password attempts (each sleeps 1s under lock)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", 200+idx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("background attempt %d status = %d; want 401", idx, rec.Code)
			}
		}(i)
	}

	// Give background goroutines a few ms to enter handler and queue
	time.Sleep(20 * time.Millisecond)

	// Now send correct password: it queues behind the 2 waiters (loginWaiters < 3)
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"correct-password-16-chars"}`))
	req.RemoteAddr = "192.0.2.199:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	wg.Wait()
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("correct login status = %d; want 200", rec.Code)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("correct login took %v, must be < 10s", elapsed)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "botlink_admin=") {
		t.Fatalf("missing session cookie in response")
	}
}

func TestLoginWithServerWriteTimeout15s(t *testing.T) {
	srv, st := newTestServer(t, "correct-password-16-chars")
	defer func() {
		srv.Close()
		st.Close()
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	httpSrv := &http.Server{
		Handler:      srv.Handler(),
		WriteTimeout: 15 * time.Second,
	}
	go func() {
		_ = httpSrv.Serve(ln)
	}()
	defer func() {
		_ = httpSrv.Close()
	}()

	client := &http.Client{Timeout: 30 * time.Second}
	url := "http://" + ln.Addr().String() + "/api/auth/login"

	// 1. Wrong password (evaluates under lock with 1s sleep, WriteDeadline extended)
	resp1, err := client.Post(url, "application/json", strings.NewReader(`{"password":"wrong-password"}`))
	if err != nil {
		t.Fatalf("POST wrong password: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", resp1.StatusCode)
	}

	// 2. Correct password (evaluates immediately, returns 200)
	resp2, err := client.Post(url, "application/json", strings.NewReader(`{"password":"correct-password-16-chars"}`))
	if err != nil {
		t.Fatalf("POST correct password: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp2.StatusCode)
	}
}

func TestRoomReplyRateLimitingDoesNotSaveMessage(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
	}()

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Rate Limit Msg Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{
			{Name: "Bot1", IsMock: false},
			{Name: "Bot2", IsMock: false},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	bot1Token := detail.Members[0].AgentToken

	// Send 10 replies (10 is the POST limit per minute)
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest("POST", "/api/room/"+bot1Token+"/reply", strings.NewReader(`{"expectedTurnSeq":1,"content":"msg"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("reply %d status = %d; want 200", i, rec.Code)
		}
	}

	// 11th reply must return 429 Too Many Requests
	req11 := httptest.NewRequest("POST", "/api/room/"+bot1Token+"/reply", strings.NewReader(`{"expectedTurnSeq":1,"content":"should-not-be-saved"}`))
	req11.Header.Set("Content-Type", "application/json")
	rec11 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec11, req11)

	if rec11.Code != http.StatusTooManyRequests {
		t.Fatalf("11th reply status = %d; want 429 Too Many Requests", rec11.Code)
	}

	// Verify rejected message was NEVER saved to database
	count, err := st.MessageCount(context.Background(), id)
	if err != nil {
		t.Fatalf("MessageCount: %v", err)
	}
	if count > 10 {
		t.Fatalf("expected message count <= 10, got %d", count)
	}
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range d.Messages {
		if m.Content == "should-not-be-saved" {
			t.Fatal("rejected message was saved in database!")
		}
	}
}

func TestBotPollingIntervalsRemainOnline(t *testing.T) {
	st := openTestStore(t)
	secret, err := GenerateEphemeralSecret()
	if err != nil {
		t.Fatalf("GenerateEphemeralSecret: %v", err)
	}
	var simTime int64 = 1_000_000
	srv := New(Config{
		Addr:            ":0",
		DBPath:          filepath.Join(t.TempDir(), "x.db"),
		FrontendDistDir: t.TempDir(),
		AdminPassword:   "secret123",
		SessionSecret:   secret,
		Clock:           func() int64 { return simTime },
	}, st)
	defer func() {
		srv.Close()
		st.Close()
		goleak.VerifyNone(t, goleak.IgnoreCurrent())
	}()

	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Polling Interval Test", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{
			{Name: "Bot1", IsMock: false},
			{Name: "Bot2", IsMock: false},
		},
	}, simTime)
	if err != nil {
		t.Fatal(err)
	}

	cookie := login(t, srv)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id), nil, cookie)
	var detail store.GroupDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	token := detail.Members[0].AgentToken

	// Poll 1 at t=0s (1_000_000)
	req1 := httptest.NewRequest("GET", "/api/room/"+token, nil)
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("poll 1 status = %d", rec1.Code)
	}

	d1, err := st.GetGroupDetail(context.Background(), id, simTime)
	if err != nil || !d1.Members[0].Online {
		t.Fatalf("expected bot online at t=0s, err=%v", err)
	}

	// Poll 2 at t=25s (1_025_000)
	simTime += 25_000
	req2 := httptest.NewRequest("GET", "/api/room/"+token, nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("poll 2 status = %d", rec2.Code)
	}

	d2, err := st.GetGroupDetail(context.Background(), id, simTime)
	if err != nil || !d2.Members[0].Online {
		t.Fatalf("expected bot online at t=25s, err=%v", err)
	}

	// Poll 3 at t=65s (40s after poll 2: 1_065_000)
	// Total elapsed is 65s (>45s presence threshold from start, but only 40s <= 45s from last poll)
	simTime += 40_000
	req3 := httptest.NewRequest("GET", "/api/room/"+token, nil)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("poll 3 status = %d", rec3.Code)
	}

	d3, err := st.GetGroupDetail(context.Background(), id, simTime)
	if err != nil || !d3.Members[0].Online {
		t.Fatalf("expected bot online at t=65s (40s since last poll), err=%v", err)
	}

	// At t=111s (46s after poll 3, no further polls) -> must be offline!
	simTime += 46_000
	d4, err := st.GetGroupDetail(context.Background(), id, simTime)
	if err != nil {
		t.Fatal(err)
	}
	if d4.Members[0].Online {
		t.Fatalf("expected bot offline at t=111s (46s with no poll)")
	}
}

func eventually(t *testing.T, timeout, interval time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("condition not met within timeout")
}

func TestRoomStateConcurrentRotationDoesNotPanic(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	gid, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name:     "Test Group",
		TurnMode: "auto_round_robin",
		MaxTurns: 10,
		Members: []store.MemberInput{
			{Name: "Bot 1", RoleTitle: "Worker", AccentColor: "#ff0000", IsMock: false},
			{Name: "Bot 2", RoleTitle: "Worker", AccentColor: "#00ff00", IsMock: false},
		},
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := st.GetGroupDetail(context.Background(), gid, 1000)
	if err != nil || len(detail.Members) == 0 {
		t.Fatalf("get detail: %v", err)
	}
	token := detail.Members[0].AgentToken
	if token == "" {
		t.Fatal("empty agent token")
	}

	// Set hook to rotate token right before GetRoomState executes
	srv.onBeforeGetRoomState = func() {
		_, err := st.RotateToken(context.Background(), gid, "bot_1", 2000)
		if err != nil {
			t.Fatalf("rotate token in hook: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/room/"+token, nil)
	rec := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PANIC occurred during concurrent token rotation: %v", r)
		}
	}()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "null" {
		t.Fatalf("expected 'null' body, got: %q", body)
	}
	if rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("expected private, no-cache header, got: %s", rec.Header().Get("Cache-Control"))
	}
}

type trackingReader struct {
	r         *strings.Reader
	readBytes int64
}

func (tr *trackingReader) Read(p []byte) (n int, err error) {
	n, err = tr.r.Read(p)
	tr.readBytes += int64(n)
	return n, err
}

func TestRoomPayloadCapAndLazyBodyReading(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	gid, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name:     "Test Group",
		TurnMode: "auto_round_robin",
		MaxTurns: 10,
		Members: []store.MemberInput{
			{Name: "Bot 1", RoleTitle: "Worker", AccentColor: "#ff0000", IsMock: false},
			{Name: "Bot 2", RoleTitle: "Worker", AccentColor: "#00ff00", IsMock: false},
		},
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := st.GetGroupDetail(context.Background(), gid, 1000)
	if err != nil || len(detail.Members) == 0 {
		t.Fatalf("get detail: %v", err)
	}
	validToken := detail.Members[0].AgentToken

	// 1. Invalid token with large body (100KB) -> must be rejected without reading body
	largeJSON := `{"expectedTurnSeq":1,"content":"` + strings.Repeat("A", 100*1024) + `"}`
	tracker := &trackingReader{r: strings.NewReader(largeJSON)}
	reqInvalid := httptest.NewRequest("POST", "/api/room/invalid-token-12345/reply", tracker)
	reqInvalid.Header.Set("Content-Type", "application/json")
	recInvalid := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recInvalid, reqInvalid)

	if recInvalid.Code != http.StatusNotFound && recInvalid.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 404 or 429 for invalid token, got: %d", recInvalid.Code)
	}
	if tracker.readBytes > 0 {
		t.Fatalf("expected 0 bytes read for invalid token, but server read %d bytes before token check", tracker.readBytes)
	}

	// 2. Valid token with body > 64KB (e.g. 70KB) -> must be rejected due to 64KB cap
	body70KB := `{"expectedTurnSeq":1,"content":"` + strings.Repeat("B", 70*1024) + `"}`
	reqValidTooBig := httptest.NewRequest("POST", "/api/room/"+validToken+"/reply", strings.NewReader(body70KB))
	reqValidTooBig.Header.Set("Content-Type", "application/json")
	recValidTooBig := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recValidTooBig, reqValidTooBig)

	if recValidTooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Request Entity Too Large for payload > 64KB, got: %d (body: %s)", recValidTooBig.Code, recValidTooBig.Body.String())
	}
}

func TestProductionEnforcesSecureCookieOnPlainHTTPRequest(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()
	secret, err := GenerateEphemeralSecret()
	if err != nil {
		t.Fatal(err)
	}

	// Server configured with Production = true
	srv := New(Config{
		Addr:            ":0",
		DBPath:          filepath.Join(t.TempDir(), "prod.db"),
		FrontendDistDir: t.TempDir(),
		AdminPassword:   "correct-production-password-12345",
		SessionSecret:   secret,
		Production:      true,
	}, st)

	// Plain HTTP request without TLS and without any X-Forwarded-Proto header
	loginBody := `{"password":"correct-production-password-12345"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected login 200 OK, got: %d", rec.Code)
	}

	cookieHeader := rec.Header().Get("Set-Cookie")
	if cookieHeader == "" {
		t.Fatal("missing Set-Cookie header")
	}

	// Must contain Secure attribute even on plain HTTP request in production!
	if !strings.Contains(cookieHeader, "Secure") {
		t.Fatalf("security violation: production Set-Cookie header missing 'Secure' attribute on plain HTTP request: %s", cookieHeader)
	}
	if !strings.Contains(cookieHeader, "HttpOnly") {
		t.Fatalf("production Set-Cookie header missing 'HttpOnly': %s", cookieHeader)
	}
	if !strings.Contains(cookieHeader, "SameSite=Strict") {
		t.Fatalf("production Set-Cookie header missing 'SameSite=Strict': %s", cookieHeader)
	}
}

func TestLoginQueueSaturationRateLimitedLog(t *testing.T) {
	var logBuf bytes.Buffer
	srv, _ := newTestServer(t, "correct-long-admin-password-16char")
	srv.log = log.New(&logBuf, "", 0)

	var wg sync.WaitGroup

	// Launch 3 requests that will wait
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", idx+1)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
		}(i)
	}

	// Wait briefly for the 3 waiters to occupy the queue
	eventually(t, 2*time.Second, 10*time.Millisecond, func() bool {
		srv.loginMu.Lock()
		w := srv.loginWaiters
		srv.loginMu.Unlock()
		return w == 3
	})

	// 4th request must be rejected with 429
	req4 := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
	req4.Header.Set("Content-Type", "application/json")
	req4.RemoteAddr = "192.0.2.99:12345"
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, req4)

	if rec4.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 4th request to get 429, got: %d", rec4.Code)
	}

	// Wait for goroutines to finish
	wg.Wait()

	logged := logBuf.String()
	expectedLog := "login queue saturated: rejecting excess attempts with 429"
	if !strings.Contains(logged, expectedLog) {
		t.Fatalf("expected log to contain %q, but got: %q", expectedLog, logged)
	}
	// Assert no IP address was logged
	if strings.Contains(logged, "192.0.2.") {
		t.Fatalf("privacy violation: log contained client IP address: %q", logged)
	}
}
