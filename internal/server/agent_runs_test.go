package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

// blockingTransport is the smallest fake provider for server-level tests: it
// reports when a request arrives, blocks until cancelled, and never returns a
// reply of its own.
type blockingTransport struct {
	entered chan struct{}
	once    sync.Once
	calls   int
}

func (b *blockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b.calls++
	b.once.Do(func() { close(b.entered) })
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func testManagedGroup(t *testing.T, st *store.Store, now, connID int64) (int64, *store.ManagedAgent, int64) {
	t.Helper()
	ctx := context.Background()
	if connID == 0 {
		conn, err := st.CreateConnection(ctx, store.LLMConnectionInput{
			Provider: "deepseek", Label: "test", APIKey: "sk-test-fake-key-not-real",
		}, now)
		if err != nil {
			t.Fatalf("create connection: %v", err)
		}
		connID = conn.ID
	}
	var err error
	gid, err := st.CreateGroup(ctx, store.CreateGroupInput{
		Name: "Room", Topic: "plan", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}},
	}, now)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	ag, err := st.CreateManagedAgent(ctx, gid, store.MemberInput{Name: "Writer"},
		store.ManagedAgentInput{
			ConnectionID: connID, Model: "deepseek-flash", Persona: "short replies",
			MaxOutputTokens: 128, ContextMaxMessages: 20, ContextMaxChars: 2000,
			DailyRunLimit: 50, Enabled: true,
		}, now)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return gid, ag, connID
}

func advanceServerToManaged(t *testing.T, st *store.Store, gid int64, botID string, now int64) *store.GroupDetail {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		d, err := st.GetGroupDetail(ctx, gid, now)
		if err != nil {
			t.Fatalf("detail: %v", err)
		}
		if d.Group.CurrentTurnBotID == nil {
			t.Fatalf("no current speaker")
		}
		if *d.Group.CurrentTurnBotID == botID {
			return d
		}
		var token string
		for _, m := range d.Members {
			if m.BotID == *d.Group.CurrentTurnBotID {
				token = m.AgentToken
			}
		}
		accepted, err := st.SubmitBotReply(ctx, token, d.Group.TurnSeq, "real reply", now)
		if err != nil || !accepted {
			t.Fatalf("submit: accepted=%v err=%v", accepted, err)
		}
	}
	t.Fatal("managed agent never got the turn")
	return nil
}

func waitServerRunStatus(t *testing.T, st *store.Store, gid int64, status string) *store.AgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := st.ListAgentRuns(context.Background(), gid, 100)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		for _, r := range runs {
			if r.Status == status {
				rr := r
				return &rr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no run reached %s", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAgentRunAdminRoutesAndOwnership(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	now := time.Now().UnixMilli()
	gid, ag, connID := testManagedGroup(t, st, now, 0)
	otherGID, otherAgent, _ := testManagedGroup(t, st, now, connID)

	listPath := "/api/admin/groups/" + itoa(gid) + "/agent-runs"
	if rec := doReq(t, srv, "GET", listPath, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d; want 401", rec.Code)
	}
	cookie := login(t, srv)

	rec := doReq(t, srv, "GET", listPath, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var runs []store.AgentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("empty group returned %d runs", len(runs))
	}

	usagePath := "/api/admin/groups/" + itoa(gid) + "/agents/" + ag.BotID + "/usage"
	rec = doReq(t, srv, "GET", usagePath, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d %s", rec.Code, rec.Body.String())
	}
	var usage store.ManagedAgentUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.BudgetState != store.BudgetUnset || usage.BudgetBlocked {
		t.Errorf("unexpected usage: %+v", usage)
	}

	// A run created under the other group must not be addressable here.
	advanceServerToManaged(t, st, otherGID, otherAgent.BotID, now)
	run, _, err := st.EnsureAgentRun(context.Background(), otherGID, otherAgent.BotID, now)
	if err != nil || run == nil {
		t.Fatalf("ensure other run: %v %v", run, err)
	}
	foreign := "/api/admin/groups/" + itoa(gid) + "/agent-runs/" + itoa(run.ID) + "/retry"
	if rec := doReq(t, srv, "POST", foreign, nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-group retry = %d; want 404", rec.Code)
	}
	missing := "/api/admin/groups/" + itoa(gid) + "/agent-runs/999999/cancel"
	if rec := doReq(t, srv, "POST", missing, nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("missing cancel = %d; want 404", rec.Code)
	}
}

func TestPauseRouteCancelsInFlightManagedRun(t *testing.T) {
	srv, st := newTestServer(t, "secret123")
	now := time.Now().UnixMilli()
	gid, ag, _ := testManagedGroup(t, st, now, 0)
	d := advanceServerToManaged(t, st, gid, ag.BotID, now)

	fp := &blockingTransport{entered: make(chan struct{})}
	srv.agentRunner.SetTransport(fp)
	srv.agentRunner.Tick(context.Background())
	select {
	case <-fp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never called the provider")
	}
	waitServerRunStatus(t, st, gid, store.AgentRunRunning)

	cookie := login(t, srv)
	rec := doReq(t, srv, "POST", "/api/admin/groups/"+itoa(gid)+"/pause", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", rec.Code, rec.Body.String())
	}
	run := waitServerRunStatus(t, st, gid, store.AgentRunCancelled)
	if run.MessageID != nil {
		t.Errorf("cancelled run has a message id")
	}

	got, err := st.GetGroupDetail(context.Background(), gid, now)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if got.Group.Status != "paused" {
		t.Errorf("status = %s; want paused", got.Group.Status)
	}
	if len(got.Messages) != len(d.Messages) {
		t.Errorf("pause stored a late reply: %d messages", len(got.Messages))
	}
	if fp.calls != 1 {
		t.Errorf("provider calls = %d; want 1", fp.calls)
	}
}
