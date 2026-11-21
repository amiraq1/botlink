package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
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
