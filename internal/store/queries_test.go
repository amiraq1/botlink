package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"
)

func TestListGroupsCounts(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	ctx := context.Background()

	idA, err := st.CreateGroup(ctx, CreateGroupInput{
		Name: "A", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []MemberInput{
			{Name: "Alpha", AccentColor: "#3B82F6"},
			{Name: "Beta", AccentColor: "#A855F7"},
			{Name: "Mocky", AccentColor: "#EC4899", IsMock: true},
		},
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup A: %v", err)
	}
	idB := createTestGroup(t, st, now, "auto_round_robin", realMembers)

	dA := detail(t, st, idA, now)
	if _, err := st.GetRoomState(ctx, tokenOf(dA, "bot_1"), now); err != nil {
		t.Fatalf("GetRoomState: %v", err)
	}
	if err := st.FlushPresence(ctx); err != nil {
		t.Fatalf("FlushPresence: %v", err)
	}

	groups, err := st.ListGroups(ctx, now)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	byID := map[int64]GroupSummary{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	a, ok := byID[idA]
	if !ok {
		t.Fatalf("group A missing from %v", groups)
	}
	if a.MemberCount != 3 || a.OnlineCount != 2 {
		t.Fatalf("group A counts = %d/%d; want 3/2 (Alpha online + mock always online)",
			a.MemberCount, a.OnlineCount)
	}
	b, ok := byID[idB]
	if !ok {
		t.Fatalf("group B missing from %v", groups)
	}
	if b.MemberCount != 3 || b.OnlineCount != 0 {
		t.Fatalf("group B counts = %d/%d; want 3/0", b.MemberCount, b.OnlineCount)
	}
	// Recency order: the most recently updated group is first.
	if len(groups) != 2 || groups[0].ID != idB {
		t.Fatalf("group order = %v; want group B first", groups)
	}
}

func TestPruneOldMessagesWindow(t *testing.T) {
	previous := messageRetention
	SetMessageRetention(2)
	t.Cleanup(func() { messageRetention = previous })

	st := openTestStore(t)
	now := time.Now().UnixMilli()
	ctx := context.Background()
	idA := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	idB := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	for i := 0; i < 4; i++ {
		if err := st.SendLeaderMessage(ctx, idA, "a"+strconv.Itoa(i), now); err != nil {
			t.Fatal(err)
		}
		if err := st.SendLeaderMessage(ctx, idB, "b"+strconv.Itoa(i), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PruneOldMessages(ctx); err != nil {
		t.Fatalf("PruneOldMessages: %v", err)
	}
	for _, id := range []int64{idA, idB} {
		d := detail(t, st, id, now)
		if len(d.Messages) != 2 {
			t.Fatalf("group %d retained %d messages; want 2", id, len(d.Messages))
		}
	}
	// Idle fast-path: pruning again without new inserts changes nothing.
	if err := st.PruneOldMessages(ctx); err != nil {
		t.Fatal(err)
	}
	if d := detail(t, st, idA, now); len(d.Messages) != 2 {
		t.Fatalf("second prune changed count to %d", len(d.Messages))
	}
	// New inserts trigger exactly one window prune again.
	if err := st.SendLeaderMessage(ctx, idA, "a4", now); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneOldMessages(ctx); err != nil {
		t.Fatal(err)
	}
	d := detail(t, st, idA, now)
	if len(d.Messages) != 2 || d.Messages[1].Content != "a4" {
		t.Fatalf("after prune messages = %+v; want newest two ending in a4", d.Messages)
	}
}

func TestNextDueAndProcessDueGroup(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	ctx := context.Background()

	// Mock group: Alpha holds turn 1 with the real-bot timeout, then the mock
	// becomes due after its 5s delay.
	id, err := st.CreateGroup(ctx, CreateGroupInput{
		Name: "M", TurnMode: "auto_round_robin", MaxTurns: 20,
		MockDelaySec: int64Ptr(5),
		Members: []MemberInput{
			{Name: "Alpha", AccentColor: "#3B82F6"},
			{Name: "Mocky", AccentColor: "#A855F7", IsMock: true},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	due, err := st.NextDue(ctx, id, now, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if due.Kind != "timeout" || due.DueAt != now+90_000 {
		t.Fatalf("NextDue = %+v; want timeout at now+90s", due)
	}
	changed, err := st.ProcessDueGroup(ctx, id, now+1000, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("ProcessDueGroup acted before the timeout")
	}

	d := detail(t, st, id, now)
	if accepted, err := st.SubmitBotReply(ctx, tokenOf(d, "bot_1"), 1, "go", now); err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	due, err = st.NextDue(ctx, id, now, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if due.Kind != "mock" || due.DueAt != now+5000 {
		t.Fatalf("NextDue after reply = %+v; want mock at now+5s", due)
	}
	if changed, err := st.ProcessDueGroup(ctx, id, now+4999, 90_000); err != nil || changed {
		t.Fatalf("early mock = changed %v, err %v", changed, err)
	}
	changed, err = st.ProcessDueGroup(ctx, id, now+5000, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("ProcessDueGroup did not apply the due mock reply")
	}
	d = detail(t, st, id, now+5000)
	if len(d.Messages) != 2 {
		t.Fatalf("messages = %d; want 2 (reply + mock)", len(d.Messages))
	}
	if d.Group.TurnSeq != 3 {
		t.Fatalf("turnSeq = %d; want 3", d.Group.TurnSeq)
	}

	// Real-bot timeout: two real members, explicit 1s timeout.
	id2 := createTestGroup(t, st, now, "auto_round_robin", realMembers[:2])
	due, err = st.NextDue(ctx, id2, now, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if due.Kind != "timeout" || due.DueAt != now+1000 {
		t.Fatalf("NextDue real = %+v; want timeout at now+1s", due)
	}
	changed, err = st.ProcessDueGroup(ctx, id2, now+1000, 1000)
	if err != nil || !changed {
		t.Fatalf("timeout sweep = changed %v, err %v", changed, err)
	}
	d = detail(t, st, id2, now)
	if d.Group.TurnSeq != 1 || d.Group.Status != "running" {
		t.Fatalf("after skip seq=%d status=%s; want 1/running", d.Group.TurnSeq, d.Group.Status)
	}
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID == "bot_1" {
		t.Fatalf("speaker = %v; want the other member", d.Group.CurrentTurnBotID)
	}
}

func TestPresenceFlushBatches(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	ctx := context.Background()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	tokens := map[string]string{}
	for _, m := range d.Members {
		tokens[m.BotID] = m.AgentToken
	}

	// Three distinct members touch the room at three different times.
	for i, botID := range []string{"bot_1", "bot_2", "bot_3"} {
		if _, err := st.GetRoomState(ctx, tokens[botID], now+int64(i+1)*1000); err != nil {
			t.Fatal(err)
		}
	}
	online := st.onlinePresence(id, now+3000)
	for _, botID := range []string{"bot_1", "bot_2", "bot_3"} {
		if !online[botID] {
			t.Fatalf("queued presence missed %s", botID)
		}
	}
	if err := st.FlushPresence(ctx); err != nil {
		t.Fatal(err)
	}
	for i, botID := range []string{"bot_1", "bot_2", "bot_3"} {
		var seen int64
		if err := st.ro.QueryRowContext(ctx,
			"SELECT last_seen_at FROM bot_members WHERE token_hash = ? OR agent_token = ?",
			HashToken(tokens[botID]), tokens[botID]).Scan(&seen); err != nil {
			t.Fatal(err)
		}
		if want := now + int64(i+1)*1000; seen != want {
			t.Fatalf("%s last_seen_at = %d; want %d", botID, seen, want)
		}
	}

	// A second flush with no queued touches must not rewrite anything.
	if err := st.FlushPresence(ctx); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := st.ro.QueryRowContext(ctx,
		"SELECT last_seen_at FROM bot_members WHERE token_hash = ? OR agent_token = ?",
		HashToken(tokens["bot_1"]), tokens["bot_1"]).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != now+1000 {
		t.Fatalf("idle flush changed last_seen_at to %d", seen)
	}
}

func TestReadPoolPragmas(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	// Hold every read connection open at once so each one is checked.
	open := make([]*sql.Conn, 0, readPoolSize())
	for i := 0; i < readPoolSize(); i++ {
		conn, err := st.ro.Conn(ctx)
		if err != nil {
			t.Fatalf("ro.Conn: %v", err)
		}
		open = append(open, conn)
		var cacheSize, tempStore int64
		if err := conn.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheSize); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
			t.Fatal(err)
		}
		if cacheSize != -20000 {
			t.Fatalf("conn %d cache_size = %d; want -20000 (DSN pragma)", i, cacheSize)
		}
		if tempStore != 2 {
			t.Fatalf("conn %d temp_store = %d; want 2 (memory)", i, tempStore)
		}
	}
	for _, conn := range open {
		_ = conn.Close()
	}
}
