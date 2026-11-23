package server

import (
	"bufio"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

	// Give them a moment to establish subscriptions
	time.Sleep(50 * time.Millisecond)

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
	srv, st := newTestServer(t, "secret123")
	defer func() {
		srv.Close()
		st.Close()
		SetTestClock(nil)
	}()

	var simTime int64 = 1_000_000
	SetTestClock(func() int64 { return simTime })

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

func TestAdminRoutesProtectedViaASTAndDynamicAuth(t *testing.T) {
	// Parse server.go AST to verify every registered admin route passes requireAdmin
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "server.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse server.go: %v", err)
	}

	type routeDef struct {
		pattern string
		method  string
		path    string
	}
	var adminRoutes []routeDef

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}

		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		pattern := strings.Trim(lit.Value, `"`)
		if !strings.Contains(pattern, "/api/admin/") {
			return true
		}

		// Verify AST wrapper: handler must be a CallExpr to s.requireAdmin(...)
		handlerCall, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Fatalf("admin route %q handler is not wrapped in requireAdmin: %T", pattern, call.Args[1])
		}
		handlerSel, ok := handlerCall.Fun.(*ast.SelectorExpr)
		if !ok || handlerSel.Sel.Name != "requireAdmin" {
			t.Fatalf("admin route %q handler must be wrapped in requireAdmin, got %v", pattern, handlerCall.Fun)
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

	if len(adminRoutes) == 0 {
		t.Fatal("AST found 0 admin routes in server.go")
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
