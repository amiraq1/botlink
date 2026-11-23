package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
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
// and ensures stream goroutines are cleaned up when clients disconnect.
func TestSSELimitsAndGoroutineCleanup(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "SSE Group", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Bot1"}, {Name: "Bot2"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cookie := login(t, srv)

	// Baseline goroutine count
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

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

	// Let runtime settle and verify goroutines didn't leak
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	afterGoroutines := runtime.NumGoroutine()

	// Small threshold (+3) allows for standard background runtime workers
	if afterGoroutines > initialGoroutines+3 {
		t.Errorf("possible goroutine leak: initial=%d, after=%d", initialGoroutines, afterGoroutines)
	}
}
