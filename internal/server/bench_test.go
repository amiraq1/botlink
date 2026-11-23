package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

var benchEncryptionKey = []byte("0123456789abcdef0123456789abcdef")

func openBenchStore(b *testing.B) *store.Store {
	b.Helper()
	st, err := store.Open(filepath.Join(b.TempDir(), "bench.db"), benchEncryptionKey)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

// newBenchServer builds a server with a valid admin cookie and room limiters
// wide enough to benchmark without tripping the 60/min window.
func newBenchServer(b *testing.B) (*Server, *store.Store, *http.Cookie) {
	b.Helper()
	st := openBenchStore(b)
	secret, err := GenerateEphemeralSecret()
	if err != nil {
		b.Fatalf("GenerateEphemeralSecret: %v", err)
	}
	srv := New(Config{
		Addr: ":0", DBPath: filepath.Join(b.TempDir(), "x.db"),
		FrontendDistDir: b.TempDir(), AdminPassword: "secret123",
		SessionSecret: secret,
	}, st)
	srv.roomGETLimiter = newWindowLimiter(1<<30, time.Hour, 8192)
	srv.roomPOSTLimiter = newWindowLimiter(1<<30, time.Hour, 8192)
	srv.roomIPLimiter = newWindowLimiter(1<<30, time.Hour, 8192)

	now := time.Now()
	sessionID, err := store.NewSessionID()
	if err != nil {
		b.Fatalf("NewSessionID: %v", err)
	}
	if err := st.CreateAdminSession(context.Background(), sessionID,
		now.UnixMilli(), now.Add(time.Hour).UnixMilli()); err != nil {
		b.Fatalf("CreateAdminSession: %v", err)
	}
	cookie := &http.Cookie{
		Name:  sessionCookieName,
		Value: newSessionToken(secret, sessionID, now),
	}
	return srv, st, cookie
}

func benchRoomFixture(b *testing.B, st *store.Store, messages int) (int64, string) {
	b.Helper()
	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), store.CreateGroupInput{
		Name: "Bench", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: 500,
		Members: []store.MemberInput{
			{Name: "Alpha", RoleTitle: "dev", AccentColor: "#3B82F6"},
			{Name: "Beta", RoleTitle: "qa", AccentColor: "#A855F7"},
			{Name: "Gamma", RoleTitle: "ops", AccentColor: "#EC4899"},
			{Name: "Delta", RoleTitle: "ops", AccentColor: "#10B981"},
			{Name: "Epsilon", RoleTitle: "ops", AccentColor: "#F59E0B"},
		},
	}, now)
	if err != nil {
		b.Fatalf("CreateGroup: %v", err)
	}
	for i := 0; i < messages; i++ {
		if err := st.SendLeaderMessage(context.Background(), id,
			"bench message "+strconv.Itoa(i), now); err != nil {
			b.Fatal(err)
		}
	}
	detail, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		b.Fatalf("GetGroupDetail: %v", err)
	}
	return id, detail.Members[0].AgentToken
}

// BenchmarkRoomStateHTTP measures the full public room handler (ETag + JSON).
func BenchmarkRoomStateHTTP(b *testing.B) {
	srv, st, _ := newBenchServer(b)
	_, token := benchRoomFixture(b, st, 20)
	target := "/api/room/" + token
	handler := srv.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	var total int64
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
		total += int64(rec.Body.Len())
	}
	b.ReportMetric(float64(total)/float64(b.N), "resp_bytes/op")
}

// BenchmarkGroupDetailHTTP measures the admin chat poll over a 100-message page.
func BenchmarkGroupDetailHTTP(b *testing.B) {
	srv, st, cookie := newBenchServer(b)
	id, _ := benchRoomFixture(b, st, 150)
	target := "/api/admin/groups/" + strconv.FormatInt(id, 10) + "?limit=100"
	handler := srv.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	var total int64
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", target, nil)
		req.AddCookie(cookie)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		total += int64(rec.Body.Len())
	}
	b.ReportMetric(float64(total)/float64(b.N), "resp_bytes/op")
}

// BenchmarkStaticIndexHTML measures serving the SPA shell from disk.
func BenchmarkStaticIndexHTML(b *testing.B) {
	srv, _, _ := newBenchServer(b)
	dist := b.TempDir()
	html := make([]byte, 0, 2048)
	html = append(html, "<!doctype html><html><head><title>BOTLINK</title>"...)
	for i := 0; i < 40; i++ {
		html = append(html, "<meta name=\"bench\" content=\"botlink performance baseline\">"...)
	}
	html = append(html, "</head><body><div id=\"root\"></div></body></html>"...)
	if err := os.WriteFile(filepath.Join(dist, "index.html"), html, 0o644); err != nil {
		b.Fatal(err)
	}
	srv.cfg.FrontendDistDir = dist
	handler := srv.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	var total int64
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/admin", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
		total += int64(rec.Body.Len())
	}
	b.ReportMetric(float64(total)/float64(b.N), "resp_bytes/op")
}
