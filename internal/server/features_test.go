package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

func ptrInt64(v int64) *int64 { return &v }

func createPerfGroup(t *testing.T, st *store.Store, members []store.MemberInput) int64 {
	t.Helper()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Perf", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: 100, Members: members,
	}, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return id
}

func tokenFor(t *testing.T, st *store.Store, id int64, botID string) string {
	t.Helper()
	d, err := st.GetGroupDetail(context.Background(), id, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("GetGroupDetail: %v", err)
	}
	for _, m := range d.Members {
		if m.BotID == botID {
			return m.AgentToken
		}
	}
	t.Fatalf("member %s not found", botID)
	return ""
}

func TestRoomETagAnd304(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	token := tokenFor(t, st, id, "bot_1")
	target := "/api/room/" + token

	rec := doReq(t, srv, "GET", target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("room = %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q; want a weak validator", etag)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	body := rec.Body.String()

	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("conditional room = %d; want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("304 returned a %d-byte body", rec2.Body.Len())
	}
	if rec2.Header().Get("ETag") != etag {
		t.Fatalf("304 ETag = %q; want %q", rec2.Header().Get("ETag"), etag)
	}

	// A visible change must invalidate the validator.
	if err := st.SendLeaderMessage(context.Background(), id, "new turn", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", target, nil)
	req.Header.Set("If-None-Match", etag)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req)
	if rec3.Code != http.StatusOK {
		t.Fatalf("after change = %d; want 200", rec3.Code)
	}
	if rec3.Header().Get("ETag") == etag {
		t.Fatal("ETag did not change after a new message")
	}
	if rec3.Body.String() == body {
		t.Fatal("body did not change after a new message")
	}
}

func TestGroupDetailETagAnd304(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	cookie := login(t, srv)
	target := "/api/admin/groups/" + itoa(id)

	rec := doReq(t, srv, "GET", target, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d, body=%s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("detail response has no ETag")
	}

	req := httptest.NewRequest("GET", target, nil)
	req.AddCookie(cookie)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Fatalf("conditional detail = %d with %d bytes; want 304 empty", rec2.Code, rec2.Body.Len())
	}

	// Different paging parameters produce a different validator.
	req = httptest.NewRequest("GET", target+"?limit=100&after_id=1", nil)
	req.AddCookie(cookie)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req)
	if rec3.Code != http.StatusOK {
		t.Fatalf("paged detail = %d", rec3.Code)
	}
	if rec3.Header().Get("ETag") == "" || rec3.Header().Get("ETag") == etag {
		t.Fatalf("paged ETag = %q; want a distinct validator", rec3.Header().Get("ETag"))
	}
}

func TestGzipJSONAndSmallBody(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	cookie := login(t, srv)
	for i := 0; i < 30; i++ {
		if err := st.SendLeaderMessage(context.Background(), id, "payload message", time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	target := "/api/admin/groups/" + itoa(id) + "?limit=100"

	plainReq := httptest.NewRequest("GET", target, nil)
	plainReq.AddCookie(cookie)
	plain := httptest.NewRecorder()
	srv.Handler().ServeHTTP(plain, plainReq)
	if plain.Code != http.StatusOK {
		t.Fatalf("plain = %d", plain.Code)
	}
	if enc := plain.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("plain response encoded as %q without Accept-Encoding", enc)
	}

	gzReq := httptest.NewRequest("GET", target, nil)
	gzReq.AddCookie(cookie)
	gzReq.Header.Set("Accept-Encoding", "gzip")
	gzRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(gzRec, gzReq)
	if enc := gzRec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q; want gzip", enc)
	}
	zr, err := gzip.NewReader(gzRec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(decoded) != plain.Body.String() {
		t.Fatalf("gzip body differs from plain body (%d vs %d bytes)",
			len(decoded), plain.Body.Len())
	}

	// Tiny JSON stays uncompressed.
	smallReq := httptest.NewRequest("GET", "/api/auth/me", nil)
	smallReq.Header.Set("Accept-Encoding", "gzip")
	small := httptest.NewRecorder()
	srv.Handler().ServeHTTP(small, smallReq)
	if enc := small.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("tiny response got Content-Encoding %q", enc)
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	srv, _ := newTestServer(t, "secret123")
	dist := srv.cfg.FrontendDistDir
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app-abc123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, srv, "GET", "/assets/app-abc123.js", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", cc)
	}

	rec = doReq(t, srv, "GET", "/admin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("spa fallback = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index Cache-Control = %q", cc)
	}
}

// streamLines connects to an SSE endpoint and collects frames until the
// trigger fires and the expected event arrives.
func streamLines(t *testing.T, ts *httptest.Server, target string, cookie *http.Cookie,
	trigger func(), wantEvent string) (headers http.Header, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	headers = resp.Header
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status = %d body = %s", resp.StatusCode, data)
	}
	scanner := bufio.NewScanner(resp.Body)
	var b strings.Builder
	fired := false
	for scanner.Scan() {
		line := scanner.Text()
		b.WriteString(line)
		b.WriteByte('\n')
		if line == "event: ready" && !fired {
			fired = true
			if trigger != nil {
				go trigger()
			}
		}
		if line == "event: "+wantEvent {
			break
		}
	}
	return headers, b.String()
}

func TestAdminEventStream(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	cookie := login(t, srv)
	tokenA := tokenFor(t, st, id, "bot_1")
	tokenB := tokenFor(t, st, id, "bot_2")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})

	start := time.Now()
	headers, body := streamLines(t, ts, "/api/admin/groups/"+itoa(id)+"/events", cookie, func() {
		if err := st.SendLeaderMessage(context.Background(), id, "trigger", time.Now().UnixMilli()); err != nil {
			t.Errorf("trigger: %v", err)
		}
	}, "change")
	elapsed := time.Since(start)
	t.Logf("admin change frame arrived after %s", elapsed)
	if elapsed > time.Second {
		t.Fatalf("change frame took %s; want < 1s", elapsed)
	}
	if !strings.Contains(body, `data: {"group":`) {
		t.Fatalf("change frame missing group payload: %q", body)
	}
	if strings.Contains(body, tokenA) || strings.Contains(body, tokenB) {
		t.Fatalf("event stream leaks an agent token: %q", body)
	}
	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if enc := headers.Get("Content-Encoding"); enc != "" {
		t.Fatalf("event stream was compressed with %q", enc)
	}
	if headers.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("missing X-Accel-Buffering: no")
	}

	// Without a cookie the stream endpoint is a plain JSON 401.
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id)+"/events", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stream = %d; want 401", rec.Code)
	}
}

func TestRoomEventStreamValidatesToken(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	token := tokenFor(t, st, id, "bot_1")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})

	_, body := streamLines(t, ts, "/api/room/"+token+"/events", nil, func() {
		if err := st.SendLeaderMessage(context.Background(), id, "trigger", time.Now().UnixMilli()); err != nil {
			t.Errorf("trigger: %v", err)
		}
	}, "change")
	if strings.Contains(body, token) {
		t.Fatalf("room stream leaks its own token: %q", body)
	}
	if strings.Contains(body, tokenFor(t, st, id, "bot_2")) {
		t.Fatalf("room stream leaks another member token: %q", body)
	}

	rec := doReq(t, srv, "GET", "/api/room/deadbeefdeadbeefdeadbeefdeadbeef/events", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token stream = %d; want 404", rec.Code)
	}
}

func TestEventStreamCapacity(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	cookie := login(t, srv)
	srv.broker = newEventBroker(0, 0)
	rec := doReq(t, srv, "GET", "/api/admin/groups/"+itoa(id)+"/events", nil, cookie)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-capacity stream = %d; want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("503 response missing Retry-After")
	}
}

func TestSchedulerAppliesDueMockPromptly(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(srv.Close)

	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(ctx, store.CreateGroupInput{
		Name: "Sched", TurnMode: "auto_round_robin", MaxTurns: 20,
		MockDelaySec: ptrInt64(1),
		Members: []store.MemberInput{
			{Name: "Alpha", AccentColor: "#3B82F6"},
			{Name: "Mocky", AccentColor: "#A855F7", IsMock: true},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFor(t, st, id, "bot_1")
	go srv.sched.run(ctx)

	startTurn := time.Now()
	if accepted, err := st.SubmitBotReply(ctx, token, 1, "go", startTurn.UnixMilli()); err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	// Probe with NextDue (read-only): while the mock is still pending it
	// reports "mock"; once the scheduler fired, the turn moved on. A state
	// read would apply the mock lazily and hide whether the scheduler ran.
	deadline := time.Now().Add(5 * time.Second)
	lastLog := time.Now()
	lastDue := store.DueState{}
	for time.Now().Before(deadline) {
		due, err := st.NextDue(ctx, id, time.Now().UnixMilli(), 90_000)
		if err != nil {
			t.Fatal(err)
		}
		lastDue = due
		if time.Since(lastLog) > time.Second {
			lastLog = time.Now()
			ids, _ := st.RunningGroupIDs(ctx)
			t.Logf("probe: due=%+v running=%v elapsed=%s", due, ids, time.Since(startTurn))
		}
		if due.Kind != "mock" {
			elapsed := time.Since(startTurn)
			t.Logf("scheduler applied the 1s mock reply after %s", elapsed)
			if elapsed < 900*time.Millisecond {
				t.Fatalf("mock fired after %s; the 1s delay was not respected", elapsed)
			}
			if elapsed > 2500*time.Millisecond {
				t.Fatalf("mock fired after %s; want about 1s", elapsed)
			}
			d, err := st.GetGroupDetail(ctx, id, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Messages) != 2 {
				t.Fatalf("messages = %d after the mock; want 2", len(d.Messages))
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scheduler never applied the due mock reply (last due=%+v)", lastDue)
}

func TestGzipSkippedForEventStream(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	id := createPerfGroup(t, st, []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}})
	cookie := login(t, srv)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})
	headers, body := streamLines(t, ts, "/api/admin/groups/"+itoa(id)+"/events", cookie, nil, "ready")
	if headers.Get("Content-Encoding") != "" {
		t.Fatalf("event stream compressed: %q", headers.Get("Content-Encoding"))
	}
	if strings.Contains(body, "event: change") {
		t.Fatal("stream emitted a change before any mutation")
	}
}
