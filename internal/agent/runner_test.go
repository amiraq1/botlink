package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amiraq1/botlink/internal/store"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef"

type testClock struct {
	mu  sync.Mutex
	now int64
}

func (c *testClock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(ms int64) {
	c.mu.Lock()
	c.now = ms
	c.mu.Unlock()
}

// fakeProvider is an http.RoundTripper that records calls and concurrency. Its
// handler receives the request context, so a test can block until cancellation.
type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	inFlight int
	peak     int
	body     map[string]any
	handler  func(context.Context, *http.Request) (*http.Response, error)
}

func (f *fakeProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body)
	}
	f.mu.Lock()
	f.calls++
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.body = body
	h := f.handler
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if h == nil {
		return jsonResponse(okChat("رد تجريبي", 10, 5)), nil
	}
	return h(req.Context(), req)
}

func (f *fakeProvider) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProvider) Peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func (f *fakeProvider) Body() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.body
}

func (f *fakeProvider) SetHandler(h func(context.Context, *http.Request) (*http.Response, error)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func okChat(content string, promptTokens, completionTokens int64) string {
	enc, _ := json.Marshal(content)
	return fmt.Sprintf(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		enc, promptTokens, completionTokens, promptTokens+completionTokens)
}

func errorResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"denied"}}`)),
	}
}

type fixture struct {
	t      *testing.T
	st     *store.Store
	path   string
	now    int64
	gid    int64
	connID int64
	agent  *store.ManagedAgent
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := openFixture(t, filepath.Join(t.TempDir(), "botlink.db"), true)
	return f
}

func openFixture(t *testing.T, path string, create bool) *fixture {
	t.Helper()
	st, err := store.Open(path, []byte(testEncryptionKey))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	f := &fixture{t: t, st: st, path: path, now: int64(1_700_000_000_000)}
	if !create {
		return f
	}
	conn, err := st.CreateConnection(context.Background(), store.LLMConnectionInput{
		Provider: "deepseek", Label: "test", APIKey: "sk-test-fake-key-not-real",
	}, f.now)
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	f.connID = conn.ID
	f.gid, f.agent = f.newManagedGroup("Strategy Room")
	return f
}

func (f *fixture) newManagedGroup(name string) (int64, *store.ManagedAgent) {
	f.t.Helper()
	ctx := context.Background()
	gid, err := f.st.CreateGroup(ctx, store.CreateGroupInput{
		Name: name, Topic: "plan", TurnMode: "auto_round_robin", MaxTurns: 50,
		Members: []store.MemberInput{{Name: "Alpha"}, {Name: "Beta"}},
	}, f.now)
	if err != nil {
		f.t.Fatalf("create group: %v", err)
	}
	ag, err := f.st.CreateManagedAgent(ctx, gid,
		store.MemberInput{Name: "Writer", RoleTitle: "كاتب"},
		store.ManagedAgentInput{
			ConnectionID:       f.connID,
			Model:              "deepseek-flash",
			Persona:            "اكتب ردوداً قصيرة ومفيدة.",
			MaxOutputTokens:    128,
			ContextMaxMessages: 20,
			ContextMaxChars:    2000,
			DailyRunLimit:      100,
			Enabled:            true,
		}, f.now)
	if err != nil {
		f.t.Fatalf("create managed agent: %v", err)
	}
	return gid, ag
}

func (f *fixture) detail() *store.GroupDetail {
	f.t.Helper()
	d, err := f.st.GetGroupDetail(context.Background(), f.gid, f.now)
	if err != nil {
		f.t.Fatalf("group detail: %v", err)
	}
	return d
}

func (f *fixture) setPrice(input, output int64) {
	f.t.Helper()
	if err := f.st.SetModelPrice(context.Background(), store.ModelPrice{
		ConnectionID: f.connID, Model: "deepseek-flash",
		InputMicrosPer1M: input, OutputMicrosPer1M: output, Currency: "USD",
	}, f.now); err != nil {
		f.t.Fatalf("set price: %v", err)
	}
}

func (f *fixture) updateBudget(budget *int64) {
	f.t.Helper()
	a := f.agent
	if _, err := f.st.UpdateManagedAgent(context.Background(), f.gid, a.BotID, store.ManagedAgentInput{
		ConnectionID: a.ConnectionID, Model: a.Model, Persona: a.Persona,
		MaxOutputTokens: a.MaxOutputTokens, ContextMaxMessages: a.ContextMaxMessages,
		ContextMaxChars: a.ContextMaxChars, DailyRunLimit: a.DailyRunLimit,
		DailyBudgetMicros: budget, Enabled: true,
	}, f.now); err != nil {
		f.t.Fatalf("update agent budget: %v", err)
	}
}

// advanceToManaged submits real-bot replies until the managed agent holds the
// turn. The guard turns a routing regression into a clear test failure.
func (f *fixture) advanceToManaged() *store.GroupDetail {
	f.t.Helper()
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		d := f.detail()
		if d.Group.Status != "running" || d.Group.CurrentTurnBotID == nil {
			f.t.Fatalf("group is not running at a turn: %s", d.Group.Status)
		}
		if *d.Group.CurrentTurnBotID == f.agent.BotID {
			return d
		}
		var token string
		for _, m := range d.Members {
			if m.BotID == *d.Group.CurrentTurnBotID {
				token = m.AgentToken
			}
		}
		if token == "" {
			f.t.Fatalf("current bot %s has no token and is not managed", *d.Group.CurrentTurnBotID)
		}
		accepted, err := f.st.SubmitBotReply(ctx, token, d.Group.TurnSeq, "رد "+*d.Group.CurrentTurnBotID, f.now)
		if err != nil {
			f.t.Fatalf("submit real reply: %v", err)
		}
		if !accepted {
			f.t.Fatalf("real reply was not accepted")
		}
	}
	f.t.Fatal("managed agent never received the turn")
	return nil
}

func (f *fixture) runner(fp *fakeProvider, clock *testClock, turnMs int64, concurrency int) *Runner {
	return NewRunner(f.st, Config{
		MaxConcurrency: concurrency,
		TurnTimeoutMs:  turnMs,
		Transport:      fp,
		Clock:          clock.Now,
		Logger:         log.New(io.Discard, "", 0),
		TickInterval:   5 * time.Millisecond,
	})
}

func (f *fixture) runs() []store.AgentRun {
	f.t.Helper()
	runs, err := f.st.ListAgentRuns(context.Background(), f.gid, 100)
	if err != nil {
		f.t.Fatalf("list runs: %v", err)
	}
	return runs
}

func (f *fixture) findRun(id int64) *store.AgentRun {
	f.t.Helper()
	for _, r := range f.runs() {
		if r.ID == id {
			rr := r
			return &rr
		}
	}
	f.t.Fatalf("run %d not found", id)
	return nil
}

func waitRuns(t *testing.T, f *fixture, n int) []store.AgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs := f.runs()
		if len(runs) >= n {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d run(s); have %d", n, len(runs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitTerminal(t *testing.T, f *fixture, runID int64, want string) *store.AgentRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := f.findRun(runID)
		if r.Status == want {
			return r
		}
		if r.Status == store.AgentRunCompleted || r.Status == store.AgentRunFailed ||
			r.Status == store.AgentRunCancelled || r.Status == store.AgentRunInterrupted ||
			r.Status == store.AgentRunDiscarded {
			t.Fatalf("run %d reached %q; want %q (code=%s msg=%s)", runID, r.Status, want, r.ErrorCode, r.ErrorMsg)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for run %d to become %s; got %s", runID, want, r.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitRunning(t *testing.T, f *fixture, runID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := f.findRun(runID)
		if r.Status == store.AgentRunRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d never became running (status %s)", runID, r.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitIdle(t *testing.T, r *Runner, groupID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.isGroupBusy(groupID) {
		if time.Now().After(deadline) {
			t.Fatal("runner stayed busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitEntered(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not called")
	}
}

func TestRunnerCompletesTurnAndStoresReply(t *testing.T) {
	f := newFixture(t)
	d0 := f.advanceToManaged()
	f.setPrice(1_000_000, 2_000_000)

	fp := &fakeProvider{}
	fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
		return jsonResponse(okChat("هذا ردي كعضو مُدار", 100, 20)), nil
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	runs := waitRuns(t, f, 1)
	run := waitTerminal(t, f, runs[0].ID, store.AgentRunCompleted)

	if run.Attempt != 1 || run.RetryOf != nil {
		t.Errorf("attempt/retryOf = %d/%v; want 1/nil", run.Attempt, run.RetryOf)
	}
	if run.TurnEpoch != d0.Group.TurnEpoch || run.TurnSeq != d0.Group.TurnSeq {
		t.Errorf("run identity = (%d,%d); want (%d,%d)", run.TurnEpoch, run.TurnSeq,
			d0.Group.TurnEpoch, d0.Group.TurnSeq)
	}
	if run.MessageID == nil || run.ResponseChars == 0 {
		t.Errorf("completed run lacks a message id or response size: %+v", run)
	}
	if run.PromptTokens == nil || *run.PromptTokens != 100 || run.CompletionTokens == nil || *run.CompletionTokens != 20 {
		t.Errorf("usage was not stored: %+v", run)
	}
	if run.CostMicros == nil || *run.CostMicros != 140 || run.CostConfirmed || run.CostBasis != "usage" {
		t.Errorf("cost = %v confirmed=%v basis=%q; want 140/false/usage", run.CostMicros, run.CostConfirmed, run.CostBasis)
	}
	if run.RequestMsgCount < 2 || run.RequestChars == 0 {
		t.Errorf("request accounting missing: msgs=%d chars=%d", run.RequestMsgCount, run.RequestChars)
	}

	d := f.detail()
	if len(d.Messages) != len(d0.Messages)+1 {
		t.Fatalf("message count = %d; want %d", len(d.Messages), len(d0.Messages)+1)
	}
	last := d.Messages[len(d.Messages)-1]
	if last.SenderBotID != f.agent.BotID || last.Content != "هذا ردي كعضو مُدار" {
		t.Errorf("stored message = %+v", last)
	}
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID == f.agent.BotID {
		t.Errorf("turn did not advance away from the managed agent")
	}
	if d.Group.TurnEpoch == d0.Group.TurnEpoch {
		t.Errorf("turn epoch did not advance")
	}

	body := fp.Body()
	if body == nil || body["model"] != "deepseek-flash" || body["stream"] != false {
		t.Fatalf("request body = %v", body)
	}
	if thinking, _ := body["thinking"].(map[string]any); thinking["type"] != "disabled" {
		t.Errorf("thinking = %v; want disabled", body["thinking"])
	}
	if _, ok := body["tools"]; ok {
		t.Error("request contained tools")
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("request messages = %d; want system + history + instruction", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first message role = %v; want system", first["role"])
	}
	tail, _ := msgs[len(msgs)-1].(map[string]any)
	if tail["role"] != "user" || !strings.Contains(tail["content"].(string), "حان دورك") {
		t.Errorf("final instruction = %v", tail)
	}
}

func TestRunnerFailsWhenTurnDeadlineExpiredWhileQueued(t *testing.T) {
	f := newFixture(t)
	f.advanceToManaged()
	fp := &fakeProvider{}
	clock := &testClock{now: f.now + 90_001}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	runs := waitRuns(t, f, 1)
	run := waitTerminal(t, f, runs[0].ID, store.AgentRunFailed)
	if run.ErrorCode != "turn_deadline" {
		t.Errorf("error code = %q; want turn_deadline", run.ErrorCode)
	}
	if fp.Calls() != 0 {
		t.Errorf("provider was called %d time(s) after the deadline", fp.Calls())
	}
	if d := f.detail(); len(d.Messages) != 1 || d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != f.agent.BotID {
		t.Errorf("deadline must not write a message or move the turn")
	}
}

func TestRunnerProviderFailures(t *testing.T) {
	cases := []struct {
		name     string
		handler  func(context.Context, *http.Request) (*http.Response, error)
		wantCode string
	}{
		{"server error", func(context.Context, *http.Request) (*http.Response, error) {
			return errorResponse(http.StatusInternalServerError), nil
		}, "provider_unavailable"},
		{"auth error", func(context.Context, *http.Request) (*http.Response, error) {
			return errorResponse(http.StatusUnauthorized), nil
		}, "provider_auth"},
		{"unexpected shape", func(context.Context, *http.Request) (*http.Response, error) {
			return jsonResponse(`{"object":"chat.completion","choices":[]}`), nil
		}, "provider_unexpected_response"},
		{"malformed json", func(context.Context, *http.Request) (*http.Response, error) {
			return jsonResponse(`not-json`), nil
		}, "provider_unexpected_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			d0 := f.advanceToManaged()
			fp := &fakeProvider{}
			fp.SetHandler(tc.handler)
			clock := &testClock{now: f.now}
			r := f.runner(fp, clock, 90_000, 2)
			defer r.Close()

			r.Tick(context.Background())
			runs := waitRuns(t, f, 1)
			run := waitTerminal(t, f, runs[0].ID, store.AgentRunFailed)
			if run.ErrorCode != tc.wantCode {
				t.Errorf("error code = %q; want %q", run.ErrorCode, tc.wantCode)
			}
			if strings.Contains(run.ErrorMsg, "sk-test") {
				t.Errorf("stored error leaks key material: %q", run.ErrorMsg)
			}
			d := f.detail()
			if len(d.Messages) != len(d0.Messages) {
				t.Errorf("failure wrote a message")
			}
			if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != f.agent.BotID {
				t.Errorf("failure must leave the turn with the agent for the scheduler to skip")
			}
		})
	}
}

func TestRunnerPauseResumeRetry(t *testing.T) {
	f := newFixture(t)
	d0 := f.advanceToManaged()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fp := &fakeProvider{}
	fp.SetHandler(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return jsonResponse(okChat("رد بعد الاستئناف", 10, 5)), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	runs := waitRuns(t, f, 1)
	runID := runs[0].ID
	waitRunning(t, f, runID)
	waitEntered(t, entered)

	if err := f.st.PauseGroup(context.Background(), f.gid, f.now); err != nil {
		t.Fatalf("pause: %v", err)
	}
	r.CancelGroup(f.gid)
	run := waitTerminal(t, f, runID, store.AgentRunCancelled)
	if fp.Calls() != 1 {
		t.Fatalf("provider calls = %d; want 1", fp.Calls())
	}
	waitIdle(t, r, f.gid)

	d := f.detail()
	if d.Group.Status != "paused" {
		t.Fatalf("status = %s; want paused", d.Group.Status)
	}
	if len(d.Messages) != len(d0.Messages) {
		t.Fatalf("pause stored a reply")
	}

	if err := f.st.ResumeGroup(context.Background(), f.gid, f.now); err != nil {
		t.Fatalf("resume: %v", err)
	}
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 1 {
		t.Fatalf("resume silently restarted the cancelled run (%d calls)", fp.Calls())
	}
	if got := f.findRun(runID); got.Status != store.AgentRunCancelled {
		t.Fatalf("cancelled run changed to %s", got.Status)
	}

	fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
		return jsonResponse(okChat("رد بعد الاستئناف", 10, 5)), nil
	})
	retried, err := f.st.RetryAgentRun(context.Background(), runID, f.now)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.Attempt != 2 || retried.RetryOf == nil || *retried.RetryOf != runID {
		t.Fatalf("retry = attempt %d retryOf %v", retried.Attempt, retried.RetryOf)
	}
	r.Tick(context.Background())
	done := waitTerminal(t, f, retried.ID, store.AgentRunCompleted)
	if done.Attempt != 2 {
		t.Errorf("completed attempt = %d; want 2", done.Attempt)
	}
	if fp.Calls() != 2 {
		t.Errorf("provider calls = %d; want 2", fp.Calls())
	}
	if d := f.detail(); len(d.Messages) != len(d0.Messages)+1 {
		t.Errorf("retry stored %d new message(s); want 1", len(d.Messages)-len(d0.Messages))
	}
	if run.TurnEpoch != d0.Group.TurnEpoch {
		t.Errorf("pause/resume changed the epoch")
	}
}

func TestRunnerSkipThenTurnReturnsToSameAgent(t *testing.T) {
	f := newFixture(t)
	d0 := f.advanceToManaged()
	entered := make(chan struct{}, 1)
	fp := &fakeProvider{}
	fp.SetHandler(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	first := waitRuns(t, f, 1)[0]
	waitRunning(t, f, first.ID)
	waitEntered(t, entered)

	if err := f.st.SkipCurrentTurn(context.Background(), f.gid, f.now); err != nil {
		t.Fatalf("skip: %v", err)
	}
	r.CancelGroup(f.gid)
	waitTerminal(t, f, first.ID, store.AgentRunCancelled)
	waitIdle(t, r, f.gid)

	afterSkip := f.detail()
	if afterSkip.Group.CurrentTurnBotID == nil || *afterSkip.Group.CurrentTurnBotID == f.agent.BotID {
		t.Fatalf("skip did not move the turn away from the agent")
	}
	if afterSkip.Group.TurnEpoch == d0.Group.TurnEpoch {
		t.Fatalf("skip did not change the epoch")
	}

	fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
		return jsonResponse(okChat("رد الجولة الثانية", 10, 5)), nil
	})
	back := f.advanceToManaged()
	if back.Group.TurnEpoch == d0.Group.TurnEpoch {
		t.Fatalf("returned turn reused the old epoch")
	}
	if *back.Group.CurrentTurnBotID != f.agent.BotID {
		t.Fatalf("managed agent did not get the turn back")
	}
	if fp.Calls() != 1 {
		t.Fatalf("provider calls before the second turn = %d; want 1", fp.Calls())
	}

	r.Tick(context.Background())
	second := waitRuns(t, f, 2)[0]
	if second.ID == first.ID {
		second = waitRuns(t, f, 2)[1]
	}
	if second.ID == first.ID {
		t.Fatalf("no new run was created for the new epoch")
	}
	waitTerminal(t, f, second.ID, store.AgentRunCompleted)
	if second.Attempt != 1 || second.TurnEpoch != back.Group.TurnEpoch {
		t.Errorf("second run = attempt %d epoch %d; want 1/%d", second.Attempt, second.TurnEpoch, back.Group.TurnEpoch)
	}
	if fp.Calls() != 2 {
		t.Errorf("provider calls = %d; want 2", fp.Calls())
	}
}

func TestRunnerStopCancelsInFlightRun(t *testing.T) {
	f := newFixture(t)
	d0 := f.advanceToManaged()
	entered := make(chan struct{}, 1)
	fp := &fakeProvider{}
	fp.SetHandler(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	run := waitRuns(t, f, 1)[0]
	waitRunning(t, f, run.ID)
	waitEntered(t, entered)

	if err := f.st.StopGroup(context.Background(), f.gid, f.now); err != nil {
		t.Fatalf("stop: %v", err)
	}
	r.CancelGroup(f.gid)
	waitTerminal(t, f, run.ID, store.AgentRunCancelled)
	waitIdle(t, r, f.gid)

	d := f.detail()
	if d.Group.Status != "stopped" {
		t.Fatalf("status = %s; want stopped", d.Group.Status)
	}
	if len(d.Messages) != len(d0.Messages) {
		t.Fatalf("stop stored a reply")
	}
	if fp.Calls() != 1 {
		t.Fatalf("provider calls = %d; want 1", fp.Calls())
	}
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 1 {
		t.Fatalf("a stopped group started another run")
	}
}

func TestRunnerRejectsLateReplyAfterNewAssignment(t *testing.T) {
	f := newFixture(t)
	d0 := f.advanceToManaged()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fp := &fakeProvider{}
	fp.SetHandler(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return jsonResponse(okChat("رد متأخر يجب رفضه", 10, 5)), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()

	r.Tick(context.Background())
	run := waitRuns(t, f, 1)[0]
	waitRunning(t, f, run.ID)
	waitEntered(t, entered)

	// Skip cancels the row but does not abort the HTTP call, so the provider
	// still returns a success for the old turn.
	if err := f.st.SkipCurrentTurn(context.Background(), f.gid, f.now); err != nil {
		t.Fatalf("skip: %v", err)
	}
	waitTerminal(t, f, run.ID, store.AgentRunCancelled)
	close(release)
	waitIdle(t, r, f.gid)

	if fp.Calls() != 1 {
		t.Fatalf("provider calls = %d; want 1", fp.Calls())
	}
	d := f.detail()
	if len(d.Messages) != len(d0.Messages) {
		t.Fatalf("late reply was stored: %d messages", len(d.Messages))
	}
	if got := f.findRun(run.ID); got.Status != store.AgentRunCancelled || got.MessageID != nil {
		t.Fatalf("late reply changed the cancelled row: %+v", got)
	}
	if d.Group.TurnEpoch == d0.Group.TurnEpoch {
		t.Fatalf("turn did not move after skip")
	}
}

func TestRunnerRestartInterruptsAndExplicitRetryRunsOnce(t *testing.T) {
	f := newFixture(t)
	f.advanceToManaged()
	run, created, err := f.st.EnsureAgentRun(context.Background(), f.gid, f.agent.BotID, f.now)
	if err != nil || !created || run == nil {
		t.Fatalf("ensure: run=%v created=%v err=%v", run, created, err)
	}

	// Simulate a server restart: close the store while the run is still queued.
	path := f.path
	if err := f.st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := openFixture(t, path, false)
	reopened.gid = f.gid
	reopened.agent = f.agent
	n, err := reopened.st.InterruptStaleAgentRuns(context.Background(), f.now+1)
	if err != nil {
		t.Fatalf("interrupt stale: %v", err)
	}
	if n != 1 {
		t.Fatalf("interrupted %d run(s); want 1", n)
	}
	if got := reopened.findRun(run.ID); got.Status != store.AgentRunInterrupted {
		t.Fatalf("run status = %s; want interrupted", got.Status)
	}

	fp := &fakeProvider{}
	fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
		return jsonResponse(okChat("رد بعد إعادة التشغيل", 10, 5)), nil
	})
	clock := &testClock{now: f.now + 2}
	r := reopened.runner(fp, clock, 90_000, 2)
	defer r.Close()

	// The interrupted assignment stays claimed: no silent restart.
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 0 {
		t.Fatalf("restart silently called the provider %d time(s)", fp.Calls())
	}
	if got := reopened.findRun(run.ID); got.Status != store.AgentRunInterrupted {
		t.Fatalf("interrupted run changed to %s without an explicit retry", got.Status)
	}

	retried, err := reopened.st.RetryAgentRun(context.Background(), run.ID, f.now+2)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	r.Tick(context.Background())
	waitTerminal(t, reopened, retried.ID, store.AgentRunCompleted)
	waitIdle(t, r, reopened.gid)
	if fp.Calls() != 1 {
		t.Fatalf("provider calls = %d; want exactly 1", fp.Calls())
	}
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 1 {
		t.Fatalf("a completed turn was executed twice (%d calls)", fp.Calls())
	}
	if runs := reopened.runs(); len(runs) != 2 {
		t.Fatalf("run count = %d; want 2 (interrupted + retry)", len(runs))
	}
}

func TestRunnerGlobalConcurrencyLimit(t *testing.T) {
	f := newFixture(t)
	f.advanceToManaged()
	gid2, agent2 := f.newManagedGroup("Second Room")
	f.gid, f.agent = gid2, agent2
	f.advanceToManaged()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	fp := &fakeProvider{}
	fp.SetHandler(func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return jsonResponse(okChat("رد", 10, 5)), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 1)
	defer r.Close()

	r.Tick(context.Background())
	waitEntered(t, entered)
	if fp.Calls() != 1 || fp.Peak() != 1 {
		t.Fatalf("calls/peak = %d/%d; want 1/1", fp.Calls(), fp.Peak())
	}
	// A second tick may claim the other group's run, but must not start it.
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 1 {
		t.Fatalf("global concurrency cap exceeded: %d calls", fp.Calls())
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for fp.Calls() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("second group never ran; calls=%d", fp.Calls())
		}
		r.Tick(context.Background())
		time.Sleep(10 * time.Millisecond)
	}
	if fp.Peak() != 1 {
		t.Fatalf("peak concurrency = %d; want 1", fp.Peak())
	}
}

func TestRunnerBudgetDoesNotSelfBlockAndStillEnforcesCaps(t *testing.T) {
	t.Run("queued run does not count as unknown", func(t *testing.T) {
		f := newFixture(t)
		f.setPrice(1_000_000, 0)
		budget := int64(100)
		f.updateBudget(&budget)
		f.advanceToManaged()
		fp := &fakeProvider{}
		fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
			return jsonResponse(okChat("رد", 10, 0)), nil
		})
		clock := &testClock{now: f.now}
		r := f.runner(fp, clock, 90_000, 2)
		defer r.Close()
		r.Tick(context.Background())
		first := waitRuns(t, f, 1)[0]
		waitTerminal(t, f, first.ID, store.AgentRunCompleted)

		d := f.advanceToManaged()
		run, created, err := f.st.EnsureAgentRun(context.Background(), d.Group.ID, f.agent.BotID, f.now)
		if err != nil || !created || run == nil {
			t.Fatalf("ensure second run: %v %v %v", run, created, err)
		}
		started, err := f.st.StartAgentRun(context.Background(), run.ID, store.AgentRunStart{}, f.now)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if !started {
			t.Fatalf("queued run blocked itself as cost_unknown")
		}
		if got := f.findRun(run.ID); got.Status != store.AgentRunRunning {
			t.Fatalf("run status = %s; want running", got.Status)
		}
		_ = f.st.CancelAgentRun(context.Background(), run.ID, f.now)
	})

	t.Run("known spend at cap blocks new runs", func(t *testing.T) {
		f := newFixture(t)
		f.setPrice(1_000_000, 0)
		f.advanceToManaged()
		fp := &fakeProvider{}
		fp.SetHandler(func(context.Context, *http.Request) (*http.Response, error) {
			return jsonResponse(okChat("رد", 10, 0)), nil
		})
		clock := &testClock{now: f.now}
		r := f.runner(fp, clock, 90_000, 2)
		defer r.Close()
		r.Tick(context.Background())
		first := waitRuns(t, f, 1)[0]
		waitTerminal(t, f, first.ID, store.AgentRunCompleted)

		budget := int64(10)
		f.updateBudget(&budget)
		f.advanceToManaged()
		run, created, err := f.st.EnsureAgentRun(context.Background(), f.gid, f.agent.BotID, f.now)
		if err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if run != nil || created {
			t.Fatalf("ensure created a run at the cap: %+v", run)
		}
		usage, err := f.st.GetManagedAgentUsage(context.Background(), f.agent.ID, f.now)
		if err != nil {
			t.Fatalf("usage: %v", err)
		}
		if usage.BudgetState != store.BudgetExhausted || !usage.BudgetBlocked || usage.BudgetBlockReason != store.BudgetReasonReached {
			t.Errorf("usage = %+v", usage)
		}
	})

	t.Run("unknown spend blocks new runs", func(t *testing.T) {
		f := newFixture(t)
		f.advanceToManaged()
		run, _, err := f.st.EnsureAgentRun(context.Background(), f.gid, f.agent.BotID, f.now)
		if err != nil || run == nil {
			t.Fatalf("ensure: %v %v", run, err)
		}
		if err := f.st.CancelAgentRun(context.Background(), run.ID, f.now); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if err := f.st.SkipCurrentTurn(context.Background(), f.gid, f.now); err != nil {
			t.Fatalf("skip: %v", err)
		}
		f.advanceToManaged()
		budget := int64(1000)
		f.updateBudget(&budget)

		blocked, created, err := f.st.EnsureAgentRun(context.Background(), f.gid, f.agent.BotID, f.now)
		if err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if blocked != nil || created {
			t.Fatalf("ensure created a run while the cost is unknown: %+v", blocked)
		}
		usage, err := f.st.GetManagedAgentUsage(context.Background(), f.agent.ID, f.now)
		if err != nil {
			t.Fatalf("usage: %v", err)
		}
		if usage.CostKnown || usage.BudgetState != store.BudgetUnknown || usage.BudgetBlockReason != store.BudgetReasonUnknown {
			t.Errorf("usage = %+v", usage)
		}
	})
}

func TestRunnerIgnoresGroupsWithoutAManagedTurn(t *testing.T) {
	f := newFixture(t)
	// The group has a managed agent, but the turn is still with bot_1.
	fp := &fakeProvider{}
	clock := &testClock{now: f.now}
	r := f.runner(fp, clock, 90_000, 2)
	defer r.Close()
	r.Tick(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fp.Calls() != 0 {
		t.Fatalf("runner called the provider for a turn it does not own")
	}
	if runs := f.runs(); len(runs) != 0 {
		t.Fatalf("runner claimed %d run(s) it does not own", len(runs))
	}

	d := f.detail()
	var token string
	for _, m := range d.Members {
		if m.BotID == "bot_1" {
			token = m.AgentToken
		}
	}
	accepted, err := f.st.SubmitBotReply(context.Background(), token, d.Group.TurnSeq, "رد بشري", f.now)
	if err != nil || !accepted {
		t.Fatalf("external agent path regressed: accepted=%v err=%v", accepted, err)
	}
}
