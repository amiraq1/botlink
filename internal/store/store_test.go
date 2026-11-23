package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testEncryptionKey = []byte("0123456789abcdef0123456789abcdef")

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), testEncryptionKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createTestGroup(t *testing.T, st *Store, now int64, mode string, members []MemberInput) int64 {
	t.Helper()
	id, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "Test", Description: "d", Topic: "t", TurnMode: mode,
		MaxTurns: 20, Members: members,
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return id
}

func detail(t *testing.T, st *Store, id int64, now int64) *GroupDetail {
	t.Helper()
	d, err := st.GetGroupDetail(context.Background(), id, now)
	if err != nil {
		t.Fatalf("GetGroupDetail: %v", err)
	}
	return d
}

func tokenOf(d *GroupDetail, botID string) string {
	for _, m := range d.Members {
		if m.BotID == botID {
			return m.AgentToken
		}
	}
	return ""
}

var realMembers = []MemberInput{
	{Name: "Alpha", RoleTitle: "dev", AccentColor: "#3B82F6"},
	{Name: "Beta", RoleTitle: "qa", AccentColor: "#A855F7"},
	{Name: "Gamma", RoleTitle: "ops", AccentColor: "#EC4899"},
}

func TestRoundRobinOnlineRouting(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	// Beta (bot_2) stays offline (never seen); Gamma (bot_3) is online via a room touch.
	d := detail(t, st, id, now)
	if _, err := st.GetRoomState(context.Background(), tokenOf(d, "bot_3"), now); err != nil {
		t.Fatalf("GetRoomState: %v", err)
	}
	// Alpha (bot_1) holds turn 1; after its reply the turn must skip offline Beta.
	accepted, err := st.SubmitBotReply(context.Background(), tokenOf(d, "bot_1"), 1, "hello team", now)
	if err != nil || !accepted {
		t.Fatalf("SubmitBotReply = %v, %v; want accepted", accepted, err)
	}
	d = detail(t, st, id, now)
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != "bot_3" {
		t.Fatalf("current turn = %v; want bot_3 (online routing)", d.Group.CurrentTurnBotID)
	}
	if d.Group.TurnSeq != 2 {
		t.Fatalf("turnSeq = %d; want 2", d.Group.TurnSeq)
	}
}

func TestRoundRobinFallbackWhenAllOffline(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	accepted, err := st.SubmitBotReply(context.Background(), tokenOf(d, "bot_1"), 1, "hi", now)
	if err != nil || !accepted {
		t.Fatalf("SubmitBotReply = %v, %v", accepted, err)
	}
	d = detail(t, st, id, now)
	// Nobody is online: circular fallback to the next member, bot_2.
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != "bot_2" {
		t.Fatalf("current turn = %v; want bot_2 (circular fallback)", d.Group.CurrentTurnBotID)
	}
}

func TestLeaderMentionQueuing(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "leader_round", realMembers)
	d := detail(t, st, id, now)
	if d.Group.Status != "waiting_for_leader" {
		t.Fatalf("status = %s; want waiting_for_leader", d.Group.Status)
	}
	// Case-insensitive @mention of Beta only.
	if err := st.SendLeaderMessage(context.Background(), id, "hello @beta, your turn", now); err != nil {
		t.Fatalf("SendLeaderMessage: %v", err)
	}
	d = detail(t, st, id, now)
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != "bot_2" {
		t.Fatalf("current turn = %v; want bot_2 (mentioned)", d.Group.CurrentTurnBotID)
	}
	if len(d.Group.TurnQueue) != 0 {
		t.Fatalf("turnQueue = %v; want empty", d.Group.TurnQueue)
	}
	// Beta replies: queue empties, back to waiting_for_leader.
	accepted, err := st.SubmitBotReply(context.Background(), tokenOf(d, "bot_2"), 1, "on it", now)
	if err != nil || !accepted {
		t.Fatalf("SubmitBotReply = %v, %v", accepted, err)
	}
	d = detail(t, st, id, now)
	if d.Group.Status != "waiting_for_leader" || d.Group.CurrentTurnBotID != nil {
		t.Fatalf("status = %s current = %v; want waiting_for_leader/nil", d.Group.Status, d.Group.CurrentTurnBotID)
	}
}

func TestLeaderNoMentionQueuesOnline(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "leader_round", realMembers)
	d := detail(t, st, id, now)
	// Gamma online only.
	if _, err := st.GetRoomState(context.Background(), tokenOf(d, "bot_3"), now); err != nil {
		t.Fatalf("GetRoomState: %v", err)
	}
	if err := st.SendLeaderMessage(context.Background(), id, "everyone, go", now); err != nil {
		t.Fatalf("SendLeaderMessage: %v", err)
	}
	d = detail(t, st, id, now)
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != "bot_3" {
		t.Fatalf("current turn = %v; want bot_3 (only online bot)", d.Group.CurrentTurnBotID)
	}
}

func TestAtomicDuplicateTurnSeq(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	token := tokenOf(d, "bot_1")

	// Sequential duplicate: the second submit for the same turnSeq is stale.
	accepted, err := st.SubmitBotReply(context.Background(), token, 1, "first", now)
	if err != nil || !accepted {
		t.Fatalf("first submit = %v, %v", accepted, err)
	}
	accepted, err = st.SubmitBotReply(context.Background(), token, 1, "duplicate", now)
	if err != nil {
		t.Fatalf("second submit err: %v", err)
	}
	if accepted {
		t.Fatal("duplicate submit was accepted; want rejected")
	}

	// Concurrent duplicates: exactly one of 10 racing submits wins.
	id2 := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d2 := detail(t, st, id2, now)
	token2 := tokenOf(d2, "bot_1")
	var wg sync.WaitGroup
	results := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := st.SubmitBotReply(context.Background(), token2, 1, "race", now)
			if err != nil {
				t.Errorf("race submit err: %v", err)
				return
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent submits accepted %d; want exactly 1", wins)
	}
	d2 = detail(t, st, id2, now)
	if len(d2.Messages) != 1 {
		t.Fatalf("messages = %d; want 1 (no duplicate stored)", len(d2.Messages))
	}
}

func TestRemoveMemberDuringActiveTurn(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	// bot_1 holds the turn; removing it must advance the turn cleanly.
	if err := st.RemoveMember(context.Background(), id, "bot_1", now); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	d := detail(t, st, id, now)
	if len(d.Members) != 2 {
		t.Fatalf("members = %d; want 2", len(d.Members))
	}
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID == "bot_1" {
		t.Fatalf("current turn = %v; want a remaining member", d.Group.CurrentTurnBotID)
	}
	// Min-2 rule: cannot remove down to one member.
	if err := st.RemoveMember(context.Background(), id, "bot_2", now); err == nil {
		t.Fatal("removing to 1 member succeeded; want an error")
	}
}

func TestSetMaxTurnsBelowTurnSeqCompletes(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	accepted, err := st.SubmitBotReply(context.Background(), tokenOf(d, "bot_1"), 1, "hi", now)
	if err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	d = detail(t, st, id, now)
	if d.Group.TurnSeq != 2 {
		t.Fatalf("turnSeq = %d; want 2", d.Group.TurnSeq)
	}
	if err := st.SetMaxTurns(context.Background(), id, 1, now); err != nil {
		t.Fatalf("SetMaxTurns: %v", err)
	}
	d = detail(t, st, id, now)
	if d.Group.Status != "completed" || d.Group.CurrentTurnBotID != nil {
		t.Fatalf("status = %s current = %v; want completed/nil", d.Group.Status, d.Group.CurrentTurnBotID)
	}
}

func TestRoomNeverLeaksTokens(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	tok1 := tokenOf(d, "bot_1")
	tok2 := tokenOf(d, "bot_2")
	tok3 := tokenOf(d, "bot_3")
	room, err := st.GetRoomState(context.Background(), tok1, now)
	if err != nil || room == nil {
		t.Fatalf("GetRoomState = %v, %v", room, err)
	}
	// The RoomState struct has no token field at all; assert the other
	// members' tokens appear nowhere in its JSON either.
	blob, _ := json.Marshal(room)
	for _, tok := range []string{tok1, tok2, tok3} {
		if strings.Contains(string(blob), tok) {
			t.Fatalf("room JSON leaks token %s", tok)
		}
	}
	// The room owner just touched the room, so they are online; the
	// never-seen Beta is not.
	if len(room.OnlineNames) != 1 || room.OnlineNames[0] != "Alpha" {
		t.Fatalf("onlineNames = %v; want [Alpha]", room.OnlineNames)
	}
	// Unknown token -> nil state, no error.
	room, err = st.GetRoomState(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeef", now)
	if err != nil || room != nil {
		t.Fatalf("unknown token room = %v, %v; want nil, nil", room, err)
	}
}

func TestMockAutoReply(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	members := []MemberInput{
		{Name: "Alpha", RoleTitle: "dev", AccentColor: "#3B82F6"},
		{Name: "Mocky", RoleTitle: "bot", AccentColor: "#A855F7", IsMock: true},
	}
	id, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "M", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: 20, MockDelaySec: int64Ptr(0), Members: members,
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	d := detail(t, st, id, now)
	// Alpha replies; turn passes to the mock, whose 0s delay is already due.
	accepted, err := st.SubmitBotReply(context.Background(), tokenOf(d, "bot_1"), 1, "go mock", now)
	if err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	// A state read applies the due mock reply lazily.
	d = detail(t, st, id, now+1000)
	if len(d.Messages) != 2 {
		t.Fatalf("messages = %d; want 2 (leader reply + mock reply)", len(d.Messages))
	}
	if !strings.Contains(d.Messages[1].Content, "[رد تلقائي]") {
		t.Fatalf("mock message = %q; want auto-reply text", d.Messages[1].Content)
	}
	if d.Group.TurnSeq != 3 {
		t.Fatalf("turnSeq = %d; want 3 after mock advance", d.Group.TurnSeq)
	}
}

func TestCreateGroupValidation(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	_, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "X", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []MemberInput{{Name: "Solo"}},
	}, now)
	if err == nil {
		t.Fatal("1-member group created; want an error")
	}
	many := make([]MemberInput, 11)
	for i := range many {
		many[i] = MemberInput{Name: "Bot"}
	}
	_, err = st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "X", TurnMode: "auto_round_robin", MaxTurns: 20, Members: many,
	}, now)
	if err == nil {
		t.Fatal("11-member group created; want an error")
	}
}

func TestRotateToken(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	old := tokenOf(d, "bot_1")
	newTok, err := st.RotateToken(context.Background(), id, "bot_1", now)
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if newTok == old || len(newTok) != 32 {
		t.Fatalf("new token = %q; want a fresh 32-hex token", newTok)
	}
	// Old token is dead; new token works.
	room, err := st.GetRoomState(context.Background(), old, now)
	if err != nil || room != nil {
		t.Fatalf("old token room = %v, %v; want nil", room, err)
	}
	room, err = st.GetRoomState(context.Background(), newTok, now)
	if err != nil || room == nil || room.BotID != "bot_1" {
		t.Fatalf("new token room = %v, %v; want bot_1", room, err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestSilentTurnTimeoutAndCircuitBreaker(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	for i := 1; i <= len(realMembers); i++ {
		if err := st.ProcessExpiredTurn(context.Background(), id, now+int64(i)*100_000, 90_000); err != nil {
			t.Fatalf("ProcessExpiredTurn %d: %v", i, err)
		}
		d := detail(t, st, id, now)
		if i < len(realMembers) {
			if d.Group.TurnSeq != 1 || d.Group.Status != "running" {
				t.Fatalf("after skip %d: seq=%d status=%s; want 1/running", i, d.Group.TurnSeq, d.Group.Status)
			}
		} else if d.Group.Status != "paused" {
			t.Fatalf("after full silent cycle status = %s; want paused", d.Group.Status)
		}
	}
	d := detail(t, st, id, now)
	found := false
	for _, m := range d.Messages {
		if m.SenderBotID == "system" {
			found = true
		}
	}
	if !found {
		t.Fatal("circuit breaker did not record a system event")
	}
}

func TestLateReplyDoesNotDoubleAdvance(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	token := tokenOf(d, "bot_1")

	type result struct {
		accepted bool
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		ok, err := st.SubmitBotReply(context.Background(), token, 1, "late reply", now+100_000)
		ch <- result{ok, err}
	}()
	if err := st.ProcessExpiredTurn(context.Background(), id, now+100_000, 90_000); err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	d = detail(t, st, id, now)
	if r.accepted && d.Group.TurnSeq != 2 {
		t.Fatalf("reply accepted but turnSeq = %d; want 2", d.Group.TurnSeq)
	}
	if !r.accepted && d.Group.TurnSeq != 1 {
		t.Fatalf("reply rejected by skip but turnSeq = %d; want 1", d.Group.TurnSeq)
	}
	if r.accepted && d.Group.SkipStreak != 0 {
		t.Fatalf("accepted reply left skipStreak = %d", d.Group.SkipStreak)
	}
}

func TestPresenceWritesAreCoalesced(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	d := detail(t, st, id, now)
	token := tokenOf(d, "bot_1")

	for i := 1; i <= 5; i++ {
		if _, err := st.GetRoomState(context.Background(), token, now+int64(i)*1000); err != nil {
			t.Fatal(err)
		}
	}
	// Presence is queued for the background flusher; flush explicitly so the
	// assertion does not race the one-second batching tick.
	if err := st.FlushPresence(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := st.ro.QueryRowContext(context.Background(),
		"SELECT last_seen_at FROM bot_members WHERE token_hash = ?", HashToken(token)).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != now+1000 {
		t.Fatalf("last_seen_at = %d after 5 polls; want first touch %d", seen, now+1000)
	}
	if _, err := st.GetRoomState(context.Background(), token, now+31_000); err != nil {
		t.Fatal(err)
	}
	if err := st.FlushPresence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.ro.QueryRowContext(context.Background(),
		"SELECT last_seen_at FROM bot_members WHERE token_hash = ?", HashToken(token)).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != now+31_000 {
		t.Fatalf("last_seen_at = %d after interval; want %d", seen, now+31_000)
	}
}

func TestMentionBoundary(t *testing.T) {
	cases := []struct {
		text, name string
		want       bool
	}{
		{"hello @Ali2", "Ali", false},
		{"hello @Ali-2", "Ali", false},
		{"hello @Ali_X", "Ali", false},
		{"hello @Ali, your turn", "Ali", true},
		{"@Ali", "Ali", true},
		{"foo@Ali bar", "Ali", false},
		{"@ali!", "Ali", true},
	}
	for _, c := range cases {
		if got := mentionsMember(c.text, c.name); got != c.want {
			t.Errorf("mentionsMember(%q, %q) = %v; want %v", c.text, c.name, got, c.want)
		}
	}
}

func TestMaxTurnsBoundsAndPagination(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	bad := createTestGroupInput()
	bad.MaxTurns = -1
	if _, err := st.CreateGroup(context.Background(), bad, now); err == nil {
		t.Fatal("negative maxTurns was accepted")
	}
	bad.MaxTurns = 501
	if _, err := st.CreateGroup(context.Background(), bad, now); err == nil {
		t.Fatal("maxTurns above limit was accepted")
	}
	if err := st.SetMaxTurns(context.Background(), 1, 0, now); err == nil {
		t.Fatal("SetMaxTurns(0) was accepted")
	}

	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	for i := 0; i < 3; i++ {
		if err := st.SendLeaderMessage(context.Background(), id, "message "+strconv.Itoa(i), now); err != nil {
			t.Fatal(err)
		}
	}
	all := detail(t, st, id, now)
	if len(all.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(all.Messages))
	}
	delta, err := st.GetGroupDetailPaged(context.Background(), id, now, 0, all.Messages[0].ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range delta.Messages {
		if m.ID <= all.Messages[0].ID {
			t.Fatalf("delta returned old message id %d", m.ID)
		}
	}
	if len(delta.Messages) != len(all.Messages)-1 {
		t.Fatalf("delta returned %d messages; want %d", len(delta.Messages), len(all.Messages)-1)
	}
}

func TestMemberNamesAndRolesSanitized(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "G", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []MemberInput{
			{Name: "A\u202e\u200bli", RoleTitle: "de\u202e\u200bv"},
			{Name: "Beta", RoleTitle: "qa"},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	d := detail(t, st, id, now)
	if d.Members[0].Name != "Ali" || d.Members[0].RoleTitle != "dev" {
		t.Fatalf("created member = %q / %q; want Ali/dev", d.Members[0].Name, d.Members[0].RoleTitle)
	}

	if err := st.AddMember(context.Background(), id,
		MemberInput{Name: "C\u200b", RoleTitle: "s\ufe0fre"}, now); err != nil {
		t.Fatal(err)
	}
	d = detail(t, st, id, now)
	var added *Member
	for i := range d.Members {
		if d.Members[i].BotID == "bot_3" {
			added = &d.Members[i]
		}
	}
	if added == nil || added.Name != "C" || added.RoleTitle != "sre" {
		t.Fatalf("added member = %+v; want C/sre", added)
	}
}

func TestMessageRetention(t *testing.T) {
	previous := messageRetention
	SetMessageRetention(2)
	t.Cleanup(func() { messageRetention = previous })

	st := openTestStore(t)
	now := time.Now().UnixMilli()
	id := createTestGroup(t, st, now, "auto_round_robin", realMembers)
	for i := 0; i < 4; i++ {
		if err := st.SendLeaderMessage(context.Background(), id, "m"+strconv.Itoa(i), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PruneOldMessages(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := detail(t, st, id, now)
	if len(d.Messages) != 2 {
		t.Fatalf("retained %d messages; want 2", len(d.Messages))
	}
}

func createTestGroupInput() CreateGroupInput {
	return CreateGroupInput{
		Name: "G", TurnMode: "auto_round_robin", MaxTurns: 20,
		Members: []MemberInput{{Name: "A"}, {Name: "B"}},
	}
}

func TestTokenMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = rawDB.Exec(`
CREATE TABLE groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  turn_mode TEXT NOT NULL DEFAULT 'auto_round_robin',
  turn_seq INTEGER NOT NULL DEFAULT 1,
  max_turns INTEGER NOT NULL DEFAULT 20,
  current_turn_bot_id TEXT DEFAULT '',
  turn_queue TEXT NOT NULL DEFAULT '[]',
  mock_delay_sec INTEGER NOT NULL DEFAULT 12,
  turn_started_at INTEGER NOT NULL DEFAULT 0,
  skip_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bot_members (
  group_id INTEGER NOT NULL REFERENCES groups(id),
  bot_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role_title TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '',
  is_mock INTEGER NOT NULL DEFAULT 0,
  agent_token TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, bot_id)
);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES groups(id),
  turn_seq INTEGER,
  sender_bot_id TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  sender_color TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'bot',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE admin_sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER
);
INSERT INTO groups (id, name, status, turn_seq, max_turns, current_turn_bot_id)
VALUES (1, 'Legacy Group', 'active', 1, 10, 'bot_1');
INSERT INTO bot_members (group_id, bot_id, name, agent_token)
VALUES (1, 'bot_1', 'Legacy Bot', 'legacy-plain-token-xyz-12345');
`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("setup legacy schema: %v", err)
	}
	rawDB.Close()

	testKey := []byte("01234567890123456789012345678901")
	st, err := Open(dbPath, testKey)
	if err != nil {
		t.Fatalf("Open with migration: %v", err)
	}
	defer st.Close()

	bakPath := dbPath + ".bak"
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Errorf("expected backup file %s to be deleted after migration, got err: %v", bakPath, err)
	}

	// Verify raw bytes of .db, -wal, -shm do NOT contain plaintext token
	tokenBytes := []byte("legacy-plain-token-xyz-12345")
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if content, err := os.ReadFile(f); err == nil {
			if bytes.Contains(content, tokenBytes) {
				t.Fatalf("plaintext token found in raw file %s after migration!", f)
			}
		}
	}

	var tokenHash, tokenEnc string
	row := st.db.QueryRow("SELECT token_hash, token_enc FROM bot_members WHERE group_id = 1 AND bot_id = 'bot_1'")
	if err := row.Scan(&tokenHash, &tokenEnc); err != nil {
		t.Fatalf("scan member row: %v", err)
	}
	expectedHash := HashToken("legacy-plain-token-xyz-12345")
	if tokenHash != expectedHash {
		t.Errorf("token_hash = %q; want %q", tokenHash, expectedHash)
	}
	if tokenEnc == "" {
		t.Errorf("token_enc is empty")
	}

	// Verify agent_token column was dropped
	var dummy string
	if err := st.db.QueryRow("SELECT agent_token FROM bot_members WHERE group_id = 1").Scan(&dummy); err == nil {
		t.Error("expected error querying dropped column agent_token, but query succeeded")
	}

	ctx := context.Background()
	now := time.Now().UnixMilli()
	roomState, err := st.GetRoomState(ctx, "legacy-plain-token-xyz-12345", now)
	if err != nil {
		t.Fatalf("GetRoomState with legacy token: %v", err)
	}
	if roomState == nil || roomState.BotName != "Legacy Bot" {
		t.Fatalf("unexpected room state: %+v", roomState)
	}

	detail, err := st.GetGroupDetail(ctx, 1, now)
	if err != nil {
		t.Fatalf("GetGroupDetail: %v", err)
	}
	if len(detail.Members) != 1 || detail.Members[0].AgentToken != "legacy-plain-token-xyz-12345" {
		t.Fatalf("GetGroupDetail member token = %q; want legacy-plain-token-xyz-12345", detail.Members[0].AgentToken)
	}
}

func TestMigrationsUpgrade(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migration_upgrade.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}

	// Setup schema_migrations at version 1 only
	_, err = rawDB.Exec(`
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at INTEGER NOT NULL
);
INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, '001_initial_tables', 1000);

CREATE TABLE groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'waiting_for_leader',
  turn_mode TEXT NOT NULL DEFAULT 'auto_round_robin',
  max_turns INTEGER NOT NULL DEFAULT 20,
  turn_seq INTEGER NOT NULL DEFAULT 1,
  current_turn_bot_id TEXT,
  turn_queue TEXT NOT NULL DEFAULT '[]',
  mock_delay_sec INTEGER NOT NULL DEFAULT 12,
  turn_started_at INTEGER NOT NULL DEFAULT 0,
  skip_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bot_members (
  group_id INTEGER NOT NULL REFERENCES groups(id),
  bot_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role_title TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '',
  is_mock INTEGER NOT NULL DEFAULT 0,
  agent_token TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  token_enc TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, bot_id)
);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES groups(id),
  turn_seq INTEGER,
  sender_bot_id TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  sender_color TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'bot',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE admin_sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_active_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER
);

INSERT INTO groups (id, name, status, turn_seq, max_turns) VALUES (1, 'Upgrade Test Group', 'running', 1, 10);
INSERT INTO bot_members (group_id, bot_id, name, token_hash) VALUES (1, 'bot_1', 'Bot 1', 'h1');
INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, content, created_at)
VALUES (1, 1, 'bot_1', 'Bot 1', 'first message', 1000);
`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("setup v1 schema: %v", err)
	}
	rawDB.Close()

	// Open with store.Open which must run migrations up to latest version (version 2)
	testKey := []byte("01234567890123456789012345678901")
	st, err := Open(dbPath, testKey)
	if err != nil {
		t.Fatalf("Open store with migrations: %v", err)
	}
	defer st.Close()

	// Check schema_migrations has version 2
	var v2Count int
	if err := st.db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&v2Count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if v2Count != 1 {
		t.Fatalf("expected version 2 in schema_migrations, got count=%d", v2Count)
	}

	// Check schema_migrations has version 3 (dropping agent_token)
	var v3Count int
	if err := st.db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version = 3").Scan(&v3Count); err != nil {
		t.Fatalf("query schema_migrations v3: %v", err)
	}
	if v3Count != 1 {
		t.Fatalf("expected version 3 in schema_migrations, got count=%d", v3Count)
	}

	// Verify agent_token column was dropped
	rows, err := st.db.Query("PRAGMA table_info(bot_members)")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "agent_token" {
			t.Fatalf("agent_token column still exists in bot_members after migration 3!")
		}
	}

	// Verify unique index idx_messages_group_turn prevents duplicate (group_id, turn_seq)
	_, err = st.db.Exec("INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, content, created_at) VALUES (1, 1, 'bot_1', 'Bot 1', 'duplicate turn seq', 2000)")
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation on duplicate (group_id, turn_seq), but insert succeeded")
	}

	// Messages with NULL turn_seq are allowed multiple times
	_, err = st.db.Exec("INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, content, created_at) VALUES (1, NULL, 'bot_1', 'Bot 1', 'system message 1', 3000)")
	if err != nil {
		t.Fatalf("insert NULL turn_seq 1 failed: %v", err)
	}
	_, err = st.db.Exec("INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, content, created_at) VALUES (1, NULL, 'bot_1', 'Bot 1', 'system message 2', 4000)")
	if err != nil {
		t.Fatalf("insert NULL turn_seq 2 failed: %v", err)
	}
}

func TestMigration2CleansDuplicateSystemTurnSeq(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migration_dup_turn.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}

	_, err = rawDB.Exec(`
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at INTEGER NOT NULL
);
INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, '001_initial_tables', 1000);

CREATE TABLE groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'waiting_for_leader',
  turn_mode TEXT NOT NULL DEFAULT 'auto_round_robin',
  max_turns INTEGER NOT NULL DEFAULT 20,
  turn_seq INTEGER NOT NULL DEFAULT 1,
  current_turn_bot_id TEXT,
  turn_queue TEXT NOT NULL DEFAULT '[]',
  mock_delay_sec INTEGER NOT NULL DEFAULT 12,
  turn_started_at INTEGER NOT NULL DEFAULT 0,
  skip_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bot_members (
  group_id INTEGER NOT NULL REFERENCES groups(id),
  bot_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role_title TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '',
  is_mock INTEGER NOT NULL DEFAULT 0,
  agent_token TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  token_enc TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, bot_id)
);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES groups(id),
  turn_seq INTEGER,
  sender_bot_id TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  sender_color TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'bot',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE admin_sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_active_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER
);

INSERT INTO groups (id, name, status, turn_seq, max_turns) VALUES (5, 'Live Group', 'paused', 10, 100);
INSERT INTO bot_members (group_id, bot_id, name, token_hash) VALUES (5, 'bot_1', 'Muse', 'h1');
INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at)
VALUES (5, 10, 'system', 'النظام', '#6B7280', 'bot', 'إيقاف تلقائي', 1000);
INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at)
VALUES (5, 10, 'bot_1', 'Muse', '#3B82F6', 'bot', 'reply on same turn', 1001);
`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("setup v1 schema: %v", err)
	}
	rawDB.Close()

	st, err := Open(dbPath, testEncryptionKey)
	if err != nil {
		t.Fatalf("Open with duplicate system turn_seq should succeed after migration 2 cleanup: %v", err)
	}
	defer st.Close()

	var v2Count int
	if err := st.db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&v2Count); err != nil {
		t.Fatalf("query schema_migrations v2: %v", err)
	}
	if v2Count != 1 {
		t.Fatalf("expected migration 2 applied, got count=%d", v2Count)
	}

	var systemTurnSeq sql.NullInt64
	if err := st.db.QueryRow("SELECT turn_seq FROM messages WHERE sender_bot_id = 'system' AND group_id = 5").Scan(&systemTurnSeq); err != nil {
		t.Fatalf("query system message: %v", err)
	}
	if systemTurnSeq.Valid {
		t.Fatalf("system message turn_seq should be NULL after migration, got %d", systemTurnSeq.Int64)
	}

	var botTurnSeq sql.NullInt64
	if err := st.db.QueryRow("SELECT turn_seq FROM messages WHERE sender_bot_id = 'bot_1' AND group_id = 5").Scan(&botTurnSeq); err != nil {
		t.Fatalf("query bot message: %v", err)
	}
	if !botTurnSeq.Valid || botTurnSeq.Int64 != 10 {
		t.Fatalf("bot message turn_seq = valid:%v value:%d; want 10", botTurnSeq.Valid, botTurnSeq.Int64)
	}

	var dupCount int
	if err := st.db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT group_id, turn_seq FROM messages WHERE turn_seq IS NOT NULL
			GROUP BY group_id, turn_seq HAVING COUNT(*) > 1
		)
	`).Scan(&dupCount); err != nil {
		t.Fatalf("query duplicate turn_seq: %v", err)
	}
	if dupCount != 0 {
		t.Fatalf("expected no duplicate (group_id, turn_seq) rows, got %d", dupCount)
	}

	// Future system messages must not conflict with bot turn_seq values.
	if _, err := st.db.Exec(`INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at)
		VALUES (5, NULL, 'system', 'النظام', '#6B7280', 'bot', 'future system', 2000)`); err != nil {
		t.Fatalf("insert future NULL-turn system message: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at)
		VALUES (5, 11, 'bot_1', 'Muse', '#3B82F6', 'bot', 'future bot', 2001)`); err != nil {
		t.Fatalf("insert future bot turn message: %v", err)
	}
}

func TestBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	backupDir := filepath.Join(dir, "backups")

	testKey := []byte("01234567890123456789012345678901")
	st, err := Open(dbPath, testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now().UnixMilli()

	gid, err := st.CreateGroup(ctx, CreateGroupInput{
		Name:     "Backup Test Group",
		TurnMode: "auto_round_robin",
		MaxTurns: 20,
		Members: []MemberInput{
			{Name: "Bot1", RoleTitle: "Worker", AccentColor: "#3b82f6"},
			{Name: "Bot2", RoleTitle: "Reviewer", AccentColor: "#10b981"},
		},
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Create 3 backups with maxKeep = 2
	bak1, err := st.Backup(ctx, backupDir, 2)
	if err != nil {
		t.Fatalf("Backup 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	bak2, err := st.Backup(ctx, backupDir, 2)
	if err != nil {
		t.Fatalf("Backup 2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	bak3, err := st.Backup(ctx, backupDir, 2)
	if err != nil {
		t.Fatalf("Backup 3: %v", err)
	}

	// Verify rotation kept at most 2 backups: bak1 was deleted, bak2 and bak3 exist
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("ReadDir backupDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 backup files after rotation, got %d", len(entries))
	}
	if _, err := os.Stat(bak1); !os.IsNotExist(err) {
		t.Errorf("expected oldest backup %s to be rotated out, but it still exists", bak1)
	}
	if _, err := os.Stat(bak2); err != nil {
		t.Errorf("expected backup %s to exist, got err: %v", bak2, err)
	}
	if _, err := os.Stat(bak3); err != nil {
		t.Errorf("expected newest backup %s to exist, got err: %v", bak3, err)
	}

	// Verify restore by opening backup database directly
	restoredStore, err := Open(bak3, testKey)
	if err != nil {
		t.Fatalf("Open restored backup: %v", err)
	}
	defer restoredStore.Close()

	detail, err := restoredStore.GetGroupDetail(ctx, gid, now)
	if err != nil {
		t.Fatalf("GetGroupDetail on restored backup: %v", err)
	}
	if detail.Group.Name != "Backup Test Group" {
		t.Fatalf("expected restored group name %q, got %q", "Backup Test Group", detail.Group.Name)
	}
	if len(detail.Members) != 2 {
		t.Fatalf("expected 2 members in restored backup, got %d", len(detail.Members))
	}
}

func TestTokenMigrationAbortsAndKeepsBackupOnVerificationFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy_fail.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}

	_, err = rawDB.Exec(`
CREATE TABLE groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'waiting_for_leader',
  turn_seq INTEGER NOT NULL DEFAULT 0,
  max_turns INTEGER NOT NULL DEFAULT 20,
  current_turn_bot_id TEXT DEFAULT '',
  turn_queue TEXT NOT NULL DEFAULT '[]',
  mock_delay_sec INTEGER NOT NULL DEFAULT 12,
  turn_started_at INTEGER NOT NULL DEFAULT 0,
  skip_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bot_members (
  group_id INTEGER NOT NULL REFERENCES groups(id),
  bot_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role_title TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '',
  is_mock INTEGER NOT NULL DEFAULT 0,
  agent_token TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, bot_id)
);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES groups(id),
  turn_seq INTEGER,
  sender_bot_id TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  sender_color TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'bot',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE admin_sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER
);
INSERT INTO groups (id, name, status, turn_seq, max_turns, current_turn_bot_id)
VALUES (1, 'Legacy Group', 'active', 1, 10, 'bot_1');
INSERT INTO bot_members (group_id, bot_id, name, agent_token)
VALUES (1, 'bot_1', 'Legacy Bot', 'token-to-fail');
CREATE TRIGGER tamper_hash AFTER UPDATE ON bot_members
BEGIN
  UPDATE bot_members SET token_hash = 'tampered_hash_that_does_not_match' WHERE bot_id = NEW.bot_id;
END;
`)
	if err != nil {
		rawDB.Close()
		t.Fatalf("setup legacy schema: %v", err)
	}
	rawDB.Close()

	validKey := []byte("01234567890123456789012345678901")
	st, err := Open(dbPath, validKey)
	t.Logf("Open error was: %v", err)
	if err == nil {
		if st != nil {
			st.Close()
		}
		t.Fatal("expected Open to fail due to hash verification mismatch during token migration")
	}

	// Backup file must be preserved on failure!
	bakPath := dbPath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		t.Fatalf("expected backup file %s to be kept on migration failure, got err: %v", bakPath, err)
	}
}
