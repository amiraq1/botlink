package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
	"github.com/amiraq1/botlink/internal/tools"
	_ "github.com/amiraq1/botlink/internal/tools/builtin"
)

// ---------------------------------------------------------------------------
// 1. SSRF Unit & Integration Tests
// ---------------------------------------------------------------------------

func TestSSRF_BlockedIPs(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},

		// IPv4-mapped IPv6 (CRITICAL: addr.Unmap() must catch these)
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:169.254.169.254", true},

		// Private IPv4
		{"10.0.0.1", true},
		{"10.254.254.254", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},

		// Private IPv6 (Unique Local fc00::/7)
		{"fc00::1", true},
		{"fd12:3456:789a::1", true},

		// Link-Local / Cloud Metadata (169.254.0.0/16, fe80::/10)
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"fe80::1", true},

		// CGNAT (100.64.0.0/10)
		{"100.64.0.1", true},
		{"100.100.100.100", true},
		{"100.127.255.254", true},

		// Multicast
		{"224.0.0.1", true},
		{"ff02::1", true},

		// Unspecified and 0.0.0.0/8
		{"0.0.0.0", true},
		{"0.0.0.1", true},
		{"::", true},

		// Broadcast & Reserved
		{"255.255.255.255", true},
		{"240.0.0.1", true},
		{"192.0.0.1", true},
		{"198.18.0.1", true},

		// IPv6 translation and transition
		{"64:ff9b::1", true},
		{"64:ff9b:1::1", true},
		{"2002::1", true},
		{"2001::1", true},
		{"fec0::1", true},

		// Public routable IPs (must NOT be blocked)
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
	}

	for _, tc := range cases {
		addr, err := netip.ParseAddr(tc.ip)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.ip, err)
		}
		got := isBlockedIP(addr, false)
		if got != tc.blocked {
			t.Errorf("isBlockedIP(%s) = %v; want %v", tc.ip, got, tc.blocked)
		}
	}
}

func TestSSRF_WebhookRejectsPrivateHosts(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")

	// Attempt to call local loopback via webhook client (private hosts disabled)
	client := NewSafeWebhookClient(false, true, 2*time.Second)
	req := WebhookRequest{
		URL:       "http://127.0.0.1:8080/test",
		Payload:   []byte(`{}`),
		TimeoutMs: 2000,
	}

	_, err := client.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected SSRF error when calling loopback IP, got nil")
	}
	_ = srv
}

func TestSSRF_WebhookRejectsRedirects(t *testing.T) {
	// Local server that returns a 302 redirect
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/target", http.StatusFound)
	}))
	defer ts.Close()

	// In test mode: allow private hosts so dialer connects to ts.URL, but redirects must still be rejected
	client := NewSafeWebhookClient(true, true, 2*time.Second)
	req := WebhookRequest{
		URL:       ts.URL,
		Payload:   []byte(`{}`),
		TimeoutMs: 2000,
	}

	_, err := client.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("expected error on redirect, got nil")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("expected redirect error message, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Output Sanitization Tests
// ---------------------------------------------------------------------------

func TestSanitizeToolOutput(t *testing.T) {
	// Case 1: Raw text containing a URL -> wrapped in {"text": "..."} with [LINK_REMOVED]
	rawText := "Check out http://evil.com/payload immediately"
	out1 := SanitizeToolOutput(rawText)
	var parsed1 map[string]string
	if err := json.Unmarshal([]byte(out1), &parsed1); err != nil {
		t.Fatalf("out1 is not valid JSON: %v", err)
	}
	if !strings.Contains(parsed1["text"], "[LINK_REMOVED]") || strings.Contains(parsed1["text"], "http://") {
		t.Errorf("out1 link was not stripped: %s", parsed1["text"])
	}

	// Case 2: Structured JSON containing links and bidi characters
	rawJSON := `{"message": "Welcome https://phishing.site", "count": 42, "status": "ok"}`
	out2 := SanitizeToolOutput(rawJSON)
	var parsed2 map[string]any
	if err := json.Unmarshal([]byte(out2), &parsed2); err != nil {
		t.Fatalf("out2 is not valid JSON: %v", err)
	}
	msg := parsed2["message"].(string)
	if !strings.Contains(msg, "[LINK_REMOVED]") || strings.Contains(msg, "https://") {
		t.Errorf("out2 link was not stripped: %s", msg)
	}
	if parsed2["count"].(float64) != 42 {
		t.Errorf("out2 count corrupted: %v", parsed2["count"])
	}
}

// ---------------------------------------------------------------------------
// 3. Room Tool Calling & Token Leak Tests
// ---------------------------------------------------------------------------

func setupGroupWithTools(t *testing.T, srv *Server, st *store.Store) (tokenA string, groupID int64, cookie *http.Cookie) {
	t.Helper()
	cookie = login(t, srv)

	// Sync builtin tools into database
	if err := tools.SyncBuiltinTools(context.Background(), st, 1000); err != nil {
		t.Fatalf("SyncBuiltinTools: %v", err)
	}

	// Create group with 2 bots
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Tool Test Group", Description: "Testing tools", Topic: "Math and Time",
		TurnMode: "auto_round_robin", MaxTurns: 10,
		Members: []store.MemberInput{
			{Name: "BotA", RoleTitle: "Calculator", AccentColor: "#3B82F6"},
			{Name: "BotB", RoleTitle: "Clock", AccentColor: "#10B981"},
		},
	}, 1000)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	detail, err := st.GetGroupDetail(context.Background(), id, 1000)
	if err != nil {
		t.Fatalf("GetGroupDetail: %v", err)
	}
	tokenA = detail.Members[0].AgentToken

	// Assign calculate and get_time to BotA (max 3 calls/turn)
	tCalc, _ := st.GetToolByName(context.Background(), "calculate")
	tTime, _ := st.GetToolByName(context.Background(), "get_time")

	_ = st.SetMemberTool(context.Background(), id, "bot_1", tCalc.ID, true, 3)
	_ = st.SetMemberTool(context.Background(), id, "bot_1", tTime.ID, true, 3)

	return tokenA, id, cookie
}

func TestRoomToolCall_Builtins(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	tokenA, groupID, _ := setupGroupWithTools(t, srv, st)

	// 1. Call calculate tool during BotA's turn
	callBody := map[string]any{
		"expectedTurnSeq": 1,
		"tool":            "calculate",
		"arguments":       map[string]string{"expression": "2 + 3 * 4"},
		"callId":          "call-calc-1",
	}

	rec := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("tool-call status = %d; body = %s", rec.Code, rec.Body.String())
	}

	var res struct {
		CallID string `json:"callId"`
		Tool   string `json:"tool"`
		Status string `json:"status"`
		Result struct {
			Value     float64 `json:"value"`
			Formatted string  `json:"formatted"`
		} `json:"result"`
		Cached bool `json:"cached"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v; body = %s", err, rec.Body.String())
	}

	if res.Status != "success" || res.Result.Value != 14 {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.Cached {
		t.Error("fresh call should not be marked cached")
	}

	// Turn sequence must NOT have advanced
	detail, err := st.GetGroupDetail(context.Background(), groupID, 1500)
	if err != nil || detail.Group.TurnSeq != 1 {
		t.Fatalf("turnSeq advanced unexpectedly: %d", detail.Group.TurnSeq)
	}

	// 2. Idempotent retry of the exact same callId returns cached result
	rec2 := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second call status = %d", rec2.Code)
	}
	var res2 struct {
		Cached bool `json:"cached"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &res2)
	if !res2.Cached {
		t.Error("duplicate callId must return cached=true")
	}

	// 3. Call get_time
	timeBody := map[string]any{
		"expectedTurnSeq": 1,
		"tool":            "get_time",
		"arguments":       map[string]string{"timezone": "Africa/Cairo"},
		"callId":          "call-time-1",
	}
	rec3 := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", timeBody)
	if rec3.Code != http.StatusOK {
		t.Fatalf("get_time status = %d; body = %s", rec3.Code, rec3.Body.String())
	}
}

func TestRoomToolCall_TurnValidation(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	tokenA, groupID, cookie := setupGroupWithTools(t, srv, st)

	detail, _ := st.GetGroupDetail(context.Background(), groupID, 1000)
	tokenB := detail.Members[1].AgentToken

	// 1. BotB calls tool when it's BotA's turn -> rejected (not your turn)
	callBody := map[string]any{
		"expectedTurnSeq": 1,
		"tool":            "calculate",
		"arguments":       map[string]string{"expression": "1 + 1"},
		"callId":          "call-botb-wrong-turn",
	}
	rec := doReq(t, srv, "POST", "/api/room/"+tokenB+"/tool-call", callBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong turn want 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. BotA calls with stale turnSeq (e.g. 99) -> rejected
	callBody["expectedTurnSeq"] = 99
	rec = doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stale turnSeq want 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Pause group -> tool execution rejected
	_ = doReq(t, srv, "POST", "/api/admin/groups/"+fmt.Sprint(groupID)+"/pause", nil, cookie)
	callBody["expectedTurnSeq"] = 1
	rec = doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("paused group want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoomToolCall_TokenLeakTest(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	tokenA, _, cookie := setupGroupWithTools(t, srv, st)

	// Add an HTTP webhook tool with secret headers
	in := store.ToolInput{
		Name:           "secret_webhook",
		Description:    "Internal webhook",
		InputSchema:    json.RawMessage(`{}`),
		TimeoutMs:      3000,
		Enabled:        true,
		Kind:           store.ToolKindHTTPWebhook,
		WebhookURL:     "https://internal-api.example.com/secret-endpoint",
		WebhookHeaders: map[string]string{"X-Secret-Key": "ultra-sensitive-key-999"},
		WebhookSecret:  "hmac-signing-secret-888",
	}
	recTool := doReq(t, srv, "POST", "/api/admin/tools", in, cookie)
	if recTool.Code != http.StatusCreated {
		t.Fatalf("create tool status = %d", recTool.Code)
	}

	// Fetch public room state
	rec := doReq(t, srv, "GET", "/api/room/"+tokenA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("room state status = %d", rec.Code)
	}

	body := rec.Body.String()

	// Assert NO secret data appears in the public room response
	forbiddenSubstrings := []string{
		"ultra-sensitive-key-999",
		"hmac-signing-secret-888",
		"https://internal-api.example.com",
		"secret-endpoint",
		tokenA,
	}

	for _, s := range forbiddenSubstrings {
		if strings.Contains(body, s) {
			t.Fatalf("SECURITY VIOLATION: public room response leaked sensitive string %q: %s", s, body)
		}
	}
}

func TestRoomToolCall_PerTurnLimit(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	tokenA, _, _ := setupGroupWithTools(t, srv, st)

	// BotA has max 3 calls/turn for calculate
	for i := 1; i <= 3; i++ {
		callBody := map[string]any{
			"expectedTurnSeq": 1,
			"tool":            "calculate",
			"arguments":       map[string]string{"expression": fmt.Sprintf("%d + 1", i)},
			"callId":          fmt.Sprintf("call-limit-%d", i),
		}
		rec := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d failed: status = %d, body = %s", i, rec.Code, rec.Body.String())
		}
	}

	// 4th call must be rejected with 429
	excessBody := map[string]any{
		"expectedTurnSeq": 1,
		"tool":            "calculate",
		"arguments":       map[string]string{"expression": "4 + 1"},
		"callId":          "call-limit-4-excess",
	}
	recExcess := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", excessBody)
	if recExcess.Code != http.StatusTooManyRequests {
		t.Fatalf("excess call want 429, got %d: %s", recExcess.Code, recExcess.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 4. Admin Tools CRUD Endpoints Tests
// ---------------------------------------------------------------------------

func TestAdminTools_CRUD(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	cookie := login(t, srv)

	// 1. Create Tool
	createIn := store.ToolInput{
		Name:        "custom_tool",
		Description: "A custom tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		TimeoutMs:   4000,
		Enabled:     true,
		Kind:        store.ToolKindBuiltin,
	}
	rec := doReq(t, srv, "POST", "/api/admin/tools", createIn, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID int64 }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID <= 0 {
		t.Fatalf("invalid tool id: %d", created.ID)
	}

	// 2. Get Tool
	recGet := doReq(t, srv, "GET", fmt.Sprintf("/api/admin/tools/%d", created.ID), nil, cookie)
	if recGet.Code != http.StatusOK {
		t.Fatalf("get status = %d", recGet.Code)
	}

	// 3. Update Tool
	updateIn := createIn
	updateIn.Description = "Updated description"
	recUpdate := doReq(t, srv, "PUT", fmt.Sprintf("/api/admin/tools/%d", created.ID), updateIn, cookie)
	if recUpdate.Code != http.StatusOK {
		t.Fatalf("update status = %d", recUpdate.Code)
	}

	// 4. Enable/Disable Tool
	recEnable := doReq(t, srv, "POST", fmt.Sprintf("/api/admin/tools/%d/enable", created.ID), map[string]bool{"enabled": false}, cookie)
	if recEnable.Code != http.StatusOK {
		t.Fatalf("enable status = %d", recEnable.Code)
	}

	// 5. Test Tool endpoint
	recTest := doReq(t, srv, "POST", fmt.Sprintf("/api/admin/tools/%d/test", created.ID), map[string]any{"arguments": map[string]any{}}, cookie)
	if recTest.Code != http.StatusOK {
		t.Fatalf("test status = %d", recTest.Code)
	}

	// 6. Delete Tool
	recDel := doReq(t, srv, "DELETE", fmt.Sprintf("/api/admin/tools/%d", created.ID), nil, cookie)
	if recDel.Code != http.StatusOK {
		t.Fatalf("delete status = %d", recDel.Code)
	}
}

// ---------------------------------------------------------------------------
// 5. In-Flight Cancellation & Webhook Signing Integration Tests
// ---------------------------------------------------------------------------

type slowBlockingTool struct {
	started chan struct{}
}

func (s *slowBlockingTool) Name() string            { return "slow_blocker" }
func (s *slowBlockingTool) Description() string     { return "Blocks until context is cancelled" }
func (s *slowBlockingTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (s *slowBlockingTool) Execute(ctx context.Context, in json.RawMessage) (tools.Result, error) {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	select {
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	case <-time.After(2 * time.Second):
		return tools.Result{Text: `{"done":true}`}, nil
	}
}

func TestRoomToolCall_CancellationOnPause(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	tokenA, groupID, cookie := setupGroupWithTools(t, srv, st)

	// Register the slow blocking tool in global registry
	slow := &slowBlockingTool{started: make(chan struct{}, 1)}
	_ = tools.Register(slow)

	// Add tool to DB and assign to BotA
	tID, err := st.CreateTool(context.Background(), store.ToolInput{
		Name:        "slow_blocker",
		Description: "slow",
		InputSchema: json.RawMessage(`{}`),
		TimeoutMs:   5000,
		Enabled:     true,
		Kind:        store.ToolKindBuiltin,
	}, 1000)
	if err != nil {
		t.Fatalf("CreateTool: %v", err)
	}
	_ = st.SetMemberTool(context.Background(), groupID, "bot_1", tID, true, 3)

	errCh := make(chan int, 1)

	// Call tool in background
	go func() {
		callBody := map[string]any{
			"expectedTurnSeq": 1,
			"tool":            "slow_blocker",
			"arguments":       map[string]any{},
			"callId":          "call-slow-1",
		}
		rec := doReq(t, srv, "POST", "/api/room/"+tokenA+"/tool-call", callBody)
		errCh <- rec.Code
	}()

	// Wait for execution to start
	select {
	case <-slow.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("tool did not start in time")
	}

	// Trigger pause (Kill Switch / pause action)
	recPause := doReq(t, srv, "POST", fmt.Sprintf("/api/admin/groups/%d/pause", groupID), nil, cookie)
	if recPause.Code != http.StatusOK {
		t.Fatalf("pause status = %d", recPause.Code)
	}

	// The in-flight call must return 409 Conflict ("turn was interrupted")
	select {
	case code := <-errCh:
		if code != http.StatusConflict {
			t.Errorf("cancelled call want 409 Conflict, got status = %d", code)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for cancelled tool call response")
	}

	// The DB record must be cancelled
	call, err := st.GetToolCallByCallID(context.Background(), groupID, "bot_1", "call-slow-1")
	if err != nil {
		t.Fatalf("get call: %v", err)
	}
	if call.Status != store.ToolCallStatusCancelled {
		t.Errorf("call status in DB: want cancelled, got %s", call.Status)
	}
}

func TestSSRF_WebhookSigningAndHeaders(t *testing.T) {
	signingSecret := "super-secure-webhook-secret-42"
	var receivedTimestamp, receivedSignature, receivedAuth string
	var receivedBody string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTimestamp = r.Header.Get("X-Botlink-Timestamp")
		receivedSignature = r.Header.Get("X-Botlink-Signature")
		receivedAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"received": true, "val": 100}`))
	}))
	defer ts.Close()

	// Create test client allowing private hosts (since ts is on 127.0.0.1)
	client := NewSafeWebhookClient(true, true, 3*time.Second)

	req := WebhookRequest{
		URL:       ts.URL,
		Headers:   map[string]string{"Authorization": "Bearer secret-token-123"},
		Secret:    signingSecret,
		Payload:   []byte(`{"greeting": "hello botlink"}`),
		TimeoutMs: 3000,
	}

	res, err := client.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !strings.Contains(res, `"received": true`) {
		t.Errorf("unexpected response: %s", res)
	}
	if receivedTimestamp == "" {
		t.Error("expected non-empty X-Botlink-Timestamp")
	}
	if !strings.HasPrefix(receivedSignature, "hmac-sha256=") {
		t.Errorf("expected hmac-sha256 signature, got %s", receivedSignature)
	}
	if receivedAuth != "Bearer secret-token-123" {
		t.Errorf("expected custom auth header, got %s", receivedAuth)
	}
	if receivedBody != `{"greeting": "hello botlink"}` {
		t.Errorf("payload mismatch: %s", receivedBody)
	}
}

func TestSSRF_URLStructureValidation(t *testing.T) {
	client := NewSafeWebhookClient(false, true, 2*time.Second)

	// Missing hostname
	_, err := client.Execute(context.Background(), WebhookRequest{
		URL:       "https:///path",
		Payload:   []byte(`{}`),
		TimeoutMs: 1000,
	})
	if err == nil || !strings.Contains(err.Error(), "hostname") {
		t.Errorf("expected hostname error, got %v", err)
	}

	// Embedded credentials
	_, err = client.Execute(context.Background(), WebhookRequest{
		URL:       "https://admin:secret@example.com/webhook",
		Payload:   []byte(`{}`),
		TimeoutMs: 1000,
	})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("expected credentials error, got %v", err)
	}
}

func TestSSRF_ForbiddenHeadersAndEmptySecret(t *testing.T) {
	var receivedHeaders http.Header

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()

	client := NewSafeWebhookClient(true, true, 3*time.Second)

	// Attempt to spoof system headers and pass custom headers with no secret
	req := WebhookRequest{
		URL: ts.URL,
		Headers: map[string]string{
			"Host":                "evil.com",
			"Content-Type":        "text/plain",
			"User-Agent":          "evil-agent",
			"X-Botlink-Signature": "forged-sig",
			"X-Botlink-Timestamp": "forged-time",
			"X-Custom-Header":     "legit-value",
		},
		Secret:    "", // Empty secret -> X-Botlink-Signature must NOT be set
		Payload:   []byte(`{}`),
		TimeoutMs: 2000,
	}

	_, err := client.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if sig := receivedHeaders.Get("X-Botlink-Signature"); sig != "" {
		t.Errorf("expected NO signature when secret is empty, got %q", sig)
	}
	if ct := receivedHeaders.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected system Content-Type, got %q", ct)
	}
	if ua := receivedHeaders.Get("User-Agent"); ua != "botlink-webhook/1.0" {
		t.Errorf("expected system User-Agent, got %q", ua)
	}
	if custom := receivedHeaders.Get("X-Custom-Header"); custom != "legit-value" {
		t.Errorf("expected custom header preserved, got %q", custom)
	}
}

func TestSSRF_LimitReaderPayloadTooLarge(t *testing.T) {
	// Server returns 65KB (exceeding 64KB limit)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		largeBody := make([]byte, 65*1024)
		_, _ = w.Write(largeBody)
	}))
	defer ts.Close()

	client := NewSafeWebhookClient(true, true, 3*time.Second)
	req := WebhookRequest{
		URL:       ts.URL,
		Payload:   []byte(`{}`),
		TimeoutMs: 2000,
	}

	_, err := client.Execute(context.Background(), req)
	if !errors.Is(err, ErrWebhookPayloadTooLarge) {
		t.Fatalf("expected ErrWebhookPayloadTooLarge, got %v", err)
	}
}

func TestSSRF_TimeoutRespectsReqTimeoutMs(t *testing.T) {
	// Server delays 800ms
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()

	client := NewSafeWebhookClient(true, true, 10*time.Second) // client has high default
	req := WebhookRequest{
		URL:       ts.URL,
		Payload:   []byte(`{}`),
		TimeoutMs: 500, // tool requires <= 500ms
	}

	_, err := client.Execute(context.Background(), req)
	if !errors.Is(err, ErrWebhookTimeout) {
		t.Fatalf("expected ErrWebhookTimeout, got %v", err)
	}
}
