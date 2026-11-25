package store

// Tests for migrations 005/006 and the managed-agent store layer: connection
// secrets, no-token membership, stable turn identity (no silent retry),
// atomic reply+status commit, late/cancelled replies, and cost/budget reporting.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testAPIKey = "sk-test-0123456789abcdefWXYZ"

// managedFixture builds a group with two normal members plus one managed agent
// (bot_3) that is linked to a DeepSeek connection.
func managedFixture(t *testing.T, in ManagedAgentInput) (*Store, int64, int64) {
	t.Helper()
	st := openTestStore(t)
	gid, connID := managedFixtureOn(t, st, in)
	return st, gid, connID
}

// managedFixtureOn builds the fixture in an existing store so a test can close
// and reopen the same database file to simulate a server restart.
func managedFixtureOn(t *testing.T, st *Store, in ManagedAgentInput) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := st.CreateConnection(ctx, LLMConnectionInput{
		Provider: "deepseek", Label: "main", APIKey: testAPIKey,
	}, 1_000)
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	gid := createTestGroup(t, st, 1_000, "auto_round_robin", []MemberInput{
		{Name: "Alpha", RoleTitle: "dev"},
		{Name: "Beta", RoleTitle: "qa"},
	})
	if in.ConnectionID == 0 {
		in.ConnectionID = conn.ID
	}
	if in.Model == "" {
		in.Model = "deepseek-chat"
	}
	in.Enabled = true
	if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "Writer", RoleTitle: "كاتب"},
		in, 1_000); err != nil {
		t.Fatalf("CreateManagedAgent: %v", err)
	}
	return gid, conn.ID
}

// advanceToManaged submits bot_1 and bot_2 replies so the managed agent
// (bot_3) holds turn 3.
func advanceToManaged(t *testing.T, st *Store, groupID, now int64) *GroupDetail {
	t.Helper()
	ctx := context.Background()
	d := detail(t, st, groupID, now)
	for _, botID := range []string{"bot_1", "bot_2"} {
		accepted, err := st.SubmitBotReply(ctx, tokenOf(d, botID), d.Group.TurnSeq, "reply from "+botID, now)
		if err != nil || !accepted {
			t.Fatalf("SubmitBotReply(%s) = %v, %v; want accepted", botID, accepted, err)
		}
		d = detail(t, st, groupID, now)
	}
	if d.Group.CurrentTurnBotID == nil || *d.Group.CurrentTurnBotID != "bot_3" {
		t.Fatalf("current turn = %v; want bot_3", d.Group.CurrentTurnBotID)
	}
	return d
}

// replyAsCurrent submits one normal-member reply for whoever holds the turn.
func replyAsCurrent(t *testing.T, st *Store, groupID, now int64) {
	t.Helper()
	ctx := context.Background()
	d := detail(t, st, groupID, now)
	if d.Group.CurrentTurnBotID == nil {
		t.Fatalf("group %d has no current speaker", groupID)
	}
	botID := *d.Group.CurrentTurnBotID
	accepted, err := st.SubmitBotReply(ctx, tokenOf(d, botID), d.Group.TurnSeq, "reply from "+botID, now)
	if err != nil || !accepted {
		t.Fatalf("SubmitBotReply(%s) = %v, %v; want accepted", botID, accepted, err)
	}
}

func countMessagesForTurn(t *testing.T, st *Store, groupID, turnSeq int64) int {
	t.Helper()
	var n int
	if err := st.ro.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM messages WHERE group_id = ? AND turn_seq = ?", groupID, turnSeq).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

func countRunsForTurn(t *testing.T, st *Store, groupID int64, botID string, turnSeq int64) int {
	t.Helper()
	var n int
	if err := st.ro.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM agent_runs WHERE group_id = ? AND bot_id = ? AND turn_seq = ?",
		groupID, botID, turnSeq).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

func countOpenRuns(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.ro.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM agent_runs WHERE status IN ('queued','running')").Scan(&n); err != nil {
		t.Fatalf("count open runs: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

func TestMigration5Fresh(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()

	var n int
	if err := st.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 7 {
		t.Errorf("want 7 migration rows, got %d", n)
	}
	for _, tbl := range []string{"llm_connections", "llm_model_prices", "managed_agents", "agent_runs"} {
		var c int
		if err := st.ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&c); err != nil {
			t.Errorf("table %q missing or broken: %v", tbl, err)
		}
	}

	// The status CHECK rejects anything outside the documented lifecycle.
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_started_at, status)
		 VALUES (?, 'bot_z', 9, 900, 'bogus')`, gid); err == nil {
		t.Error("expected CHECK constraint to reject an unknown run status")
	}
	// A second row for the same turn instance violates the attempt/open indexes.
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_started_at, status)
		 VALUES (?, 'bot_z', 9, 900, 'queued')`, gid); err != nil {
		t.Fatalf("first direct insert: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_started_at, status)
		 VALUES (?, 'bot_z', 9, 900, 'queued')`, gid); err == nil {
		t.Error("expected unique index to reject a duplicate open run")
	}
}

// TestMigration7RebuildsTurnEpochIndexes upgrades a database that still has
// clock-based indexes and two open runs for one assignment. Migration 7 must
// keep one run, discard the duplicate, renumber attempts and install the
// epoch-based indexes.
func TestMigration7RebuildsTurnEpochIndexes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clock-indexes.db")
	st, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gid, _ := managedFixtureOn(t, st, ManagedAgentInput{})
	// Rewind to the clock-based indexes and a v5 schema record.
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS idx_agent_runs_open",
		"DROP INDEX IF EXISTS idx_agent_runs_attempt",
		`CREATE UNIQUE INDEX idx_agent_runs_attempt
		   ON agent_runs(group_id, bot_id, turn_seq, turn_started_at, attempt)`,
		`CREATE UNIQUE INDEX idx_agent_runs_open
		   ON agent_runs(group_id, bot_id, turn_seq, turn_started_at)
		   WHERE status IN ('queued','running')`,
		"UPDATE groups SET turn_epoch = 0",
		"DELETE FROM schema_migrations WHERE version >= 6",
	} {
		if _, err := st.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("rewind (%s): %v", stmt, err)
		}
	}
	// The old index allows two open runs for the same assignment at two
	// clocks, with attempts that are still unique per turn_seq.
	for i, startedAt := range []int64{1_000, 2_000} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_epoch, turn_started_at, attempt, status)
			 VALUES (?, 'bot_3', 3, 0, ?, ?, 'queued')`, gid, startedAt, i+1); err != nil {
			t.Fatalf("insert open run at %d: %v", startedAt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("reopen (migration 7): %v", err)
	}
	defer func() { _ = st2.Close() }()
	var open, discarded int
	if err := st2.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agent_runs WHERE status IN ('queued','running')").Scan(&open); err != nil {
		t.Fatalf("count open: %v", err)
	}
	if err := st2.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agent_runs WHERE status = 'discarded'").Scan(&discarded); err != nil {
		t.Fatalf("count discarded: %v", err)
	}
	if open != 1 || discarded != 1 {
		t.Fatalf("open/discarded = %d/%d; want 1/1", open, discarded)
	}
	if _, err := st2.db.ExecContext(ctx,
		`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_epoch, turn_started_at, status)
		 VALUES (?, 'bot_3', 3, 0, 3_000, 'running')`, gid); err == nil {
		t.Error("the rebuilt open index still allows a second open run for the assignment")
	}
}

// TestAgentRunTurnIdentityIndexes proves the uniqueness defences use
// (group, bot, turn_epoch) and ignore the turn clock, so a resume cannot open
// a second run for an assignment while a skip cycle (new epoch) can.
func TestAgentRunTurnIdentityIndexes(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	insert := func(epoch, started, attempt int64, status string) error {
		_, err := st.db.ExecContext(ctx,
			`INSERT INTO agent_runs (group_id, bot_id, turn_seq, turn_epoch, turn_started_at, attempt, status)
			 VALUES (?, 'bot_z', 9, ?, ?, ?, ?)`, gid, epoch, started, attempt, status)
		return err
	}
	if err := insert(7, 100, 1, "failed"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Attempt numbers are unique per assignment regardless of the clock.
	if err := insert(7, 200, 1, "failed"); err == nil {
		t.Error("the same attempt number for one assignment at a different clock was accepted")
	}
	if err := insert(7, 200, 2, "failed"); err != nil {
		t.Errorf("a new attempt at a different clock was rejected: %v", err)
	}
	// Only one open run per assignment, also regardless of the clock.
	if err := insert(7, 300, 3, "queued"); err != nil {
		t.Fatalf("queued insert: %v", err)
	}
	if err := insert(7, 400, 4, "running"); err == nil {
		t.Error("a second open run at a different clock was accepted")
	}
	// A new assignment may open its own run for the same bot and turn_seq.
	if err := insert(8, 500, 1, "queued"); err != nil {
		t.Errorf("a new assignment could not open a run: %v", err)
	}
}

func TestMigration5UpgradeFromV4KeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	st, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	gid := createTestGroup(t, st, 1_000, "auto_round_robin", realMembers[:2])
	if err := st.SendLeaderMessage(ctx, gid, "legacy message", 2_000); err != nil {
		t.Fatalf("SendLeaderMessage: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Rewind the schema to version 4 exactly as an older binary left it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS agent_runs",
		"DROP TABLE IF EXISTS managed_agents",
		"DROP TABLE IF EXISTS llm_model_prices",
		"DROP TABLE IF EXISTS llm_connections",
		"DELETE FROM schema_migrations WHERE version >= 5",
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	st2, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("reopen (migration 5): %v", err)
	}
	defer func() { _ = st2.Close() }()

	var v int
	if err := st2.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 5").Scan(&v); err != nil {
		t.Fatalf("count migration 5: %v", err)
	}
	if v != 1 {
		t.Fatalf("migration 5 rows = %d; want 1", v)
	}
	d, err := st2.GetGroupDetail(ctx, gid, 3_000)
	if err != nil {
		t.Fatalf("GetGroupDetail after upgrade: %v", err)
	}
	if len(d.Members) != 2 {
		t.Errorf("members after upgrade = %d; want 2", len(d.Members))
	}
	if len(d.Messages) != 1 || d.Messages[0].Content != "legacy message" {
		t.Errorf("messages after upgrade = %+v; want the legacy message", d.Messages)
	}
	if agents, err := st2.ListManagedAgents(ctx, gid); err != nil || len(agents) != 0 {
		t.Errorf("ListManagedAgents = %v, %v; want empty", agents, err)
	}
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

func TestConnectionKeyEncryptedAtRest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	c, err := st.CreateConnection(ctx, LLMConnectionInput{
		Provider: "deepseek", Label: "main", APIKey: testAPIKey,
	}, 1_000)
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if !c.HasKey || c.KeyLast4 != "WXYZ" {
		t.Errorf("HasKey=%v KeyLast4=%q; want true/WXYZ", c.HasKey, c.KeyLast4)
	}

	var enc string
	if err := st.ro.QueryRowContext(ctx,
		"SELECT api_key_enc FROM llm_connections WHERE id = ?", c.ID).Scan(&enc); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if !strings.HasPrefix(enc, "v1:") || strings.Contains(enc, testAPIKey) {
		t.Fatalf("api_key_enc is not an AES-GCM envelope: %q", enc)
	}
	if _, err := DecryptToken(st.key(), enc, ConnectionAAD(c.ID+1)); !errors.Is(err, ErrDecryptionFailed) {
		t.Errorf("ciphertext decrypts under a different connection AAD; error = %v", err)
	}
	key, err := st.DecryptConnectionKey(ctx, c.ID)
	if err != nil || key != testAPIKey {
		t.Fatalf("DecryptConnectionKey = %q, %v", key, err)
	}

	blob, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), testAPIKey) || strings.Contains(strings.ToLower(string(blob)), "apikey") {
		t.Errorf("connection JSON leaks key material: %s", blob)
	}

	// An update with an empty key keeps the stored secret.
	if err := st.UpdateConnection(ctx, c.ID, LLMConnectionInput{
		Provider: "deepseek", Label: "renamed", ModelHint: "deepseek-chat",
	}, 2_000); err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}
	if key, err := st.DecryptConnectionKey(ctx, c.ID); err != nil || key != testAPIKey {
		t.Fatalf("key changed on metadata-only update: %q, %v", key, err)
	}
}

func TestConnectionValidation(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	cases := []LLMConnectionInput{
		{Provider: "openai", APIKey: testAPIKey},
		{Provider: "deepseek", APIKey: "short"},
		{Provider: "deepseek", APIKey: "sk-test-0123 456789abcdefWXYZ"},
	}
	for _, in := range cases {
		if _, err := st.CreateConnection(ctx, in, 1_000); !errors.Is(err, ErrValidation) {
			t.Errorf("CreateConnection(%q, len %d) error = %v; want validation error",
				in.Provider, len(in.APIKey), err)
		}
	}
	if _, err := st.CreateConnection(ctx, LLMConnectionInput{
		Provider: "deepseek", APIKey: testAPIKey,
	}, 1_000); err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	// One connection per provider in this version.
	if _, err := st.CreateConnection(ctx, LLMConnectionInput{
		Provider: "deepseek", APIKey: testAPIKey + "2",
	}, 1_000); err == nil {
		t.Error("expected a second deepseek connection to be rejected")
	}
}

func TestDeleteConnectionBlockedWhileInUse(t *testing.T) {
	st, _, connID := managedFixture(t, ManagedAgentInput{})
	err := st.DeleteConnection(context.Background(), connID)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "managed agent") {
		t.Fatalf("DeleteConnection = %v; want validation error naming the agents", err)
	}
}

// ---------------------------------------------------------------------------
// Managed agents and membership
// ---------------------------------------------------------------------------

func TestManagedAgentMemberHasNoRoomToken(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()

	agent, err := st.GetManagedAgent(ctx, gid, "bot_3")
	if err != nil {
		t.Fatalf("GetManagedAgent: %v", err)
	}
	if agent.BotName != "Writer" || agent.Model != "deepseek-chat" || !agent.Enabled {
		t.Errorf("agent = %+v", agent)
	}
	var hash, enc string
	if err := st.ro.QueryRowContext(ctx,
		"SELECT token_hash, token_enc FROM bot_members WHERE group_id = ? AND bot_id = ?",
		gid, "bot_3").Scan(&hash, &enc); err != nil {
		t.Fatalf("read member token columns: %v", err)
	}
	if hash != "" || enc != "" {
		t.Errorf("managed member has token material (hash=%q enc=%q); want none", hash, enc)
	}
	if ok, err := st.MemberExistsByToken(ctx, ""); err != nil || ok {
		t.Errorf("MemberExistsByToken(\"\") = %v, %v; want false", ok, err)
	}
	if state, err := st.GetRoomState(ctx, "", 1_000); err != nil || state != nil {
		t.Errorf("GetRoomState(\"\") = %v, %v; want nil state", state, err)
	}
	d := detail(t, st, gid, 1_000)
	if len(d.Members) != 3 {
		t.Fatalf("members = %d; want 3 (managed agent counts)", len(d.Members))
	}
}

func TestManagedAgentRejectsBadInputAndEleventhMember(t *testing.T) {
	st, gid, connID := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()

	bad := []ManagedAgentInput{
		{ConnectionID: connID, Model: ""},
		{ConnectionID: connID, Model: "deep seek"},
		{ConnectionID: 999, Model: "deepseek-chat"},
		{ConnectionID: connID, Model: "deepseek-chat", MaxOutputTokens: 5},
		{ConnectionID: connID, Model: "deepseek-chat", DailyRunLimit: -1},
	}
	for i, in := range bad {
		in.Enabled = true
		if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "X"}, in, 1_000); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}

	for i := 0; i < 7; i++ {
		if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "Filler"},
			ManagedAgentInput{ConnectionID: connID, Model: "deepseek-chat", Enabled: true}, 1_000); err != nil {
			t.Fatalf("filler %d: %v", i, err)
		}
	}
	if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "Eleventh"},
		ManagedAgentInput{ConnectionID: connID, Model: "deepseek-chat", Enabled: true}, 1_000); !errors.Is(err, ErrValidation) {
		t.Fatalf("11th member error = %v; want validation error", err)
	}
}

func TestManagedAgentRemovalAndCascade(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()

	// Removing the member (admin action) drops the config through the FK cascade.
	if err := st.RemoveMember(ctx, gid, "bot_3", 2_000); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := st.GetManagedAgent(ctx, gid, "bot_3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("config survived member removal: %v", err)
	}

	// Deleting the agent removes its member too, so no silent tokenless seat remains.
	if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "Writer"},
		ManagedAgentInput{ConnectionID: 1, Model: "deepseek-chat", Enabled: true}, 3_000); err != nil {
		t.Fatalf("re-create managed agent: %v", err)
	}
	if err := st.DeleteManagedAgent(ctx, gid, "bot_3", 4_000); err != nil {
		t.Fatalf("DeleteManagedAgent: %v", err)
	}
	d := detail(t, st, gid, 4_000)
	if len(d.Members) != 2 {
		t.Errorf("members = %d; want 2 after deleting the agent", len(d.Members))
	}

	// The 2-member floor still applies to agent deletion.
	if _, err := st.CreateManagedAgent(ctx, gid, MemberInput{Name: "Writer"},
		ManagedAgentInput{ConnectionID: 1, Model: "deepseek-chat", Enabled: true}, 5_000); err != nil {
		t.Fatalf("re-create managed agent: %v", err)
	}
	if err := st.RemoveMember(ctx, gid, "bot_1", 5_000); err != nil {
		t.Fatalf("RemoveMember bot_1: %v", err)
	}
	if err := st.DeleteManagedAgent(ctx, gid, "bot_3", 6_000); !errors.Is(err, ErrValidation) {
		t.Fatalf("DeleteManagedAgent with 2 members = %v; want validation error", err)
	}
}

// ---------------------------------------------------------------------------
// Run claiming and no silent retry
// ---------------------------------------------------------------------------

func TestEnsureAgentRunClaimsTurnOnce(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()

	// Not its turn yet: nothing is created.
	if run, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_000); err != nil || run != nil || created {
		t.Fatalf("EnsureAgentRun before its turn = %v, %v, %v; want nil,false,nil", run, created, err)
	}
	// A normal (non-managed) member never gets a run.
	if run, created, err := st.EnsureAgentRun(ctx, gid, "bot_1", 1_000); err != nil || run != nil || created {
		t.Fatalf("EnsureAgentRun for a token member = %v, %v, %v; want nil,false,nil", run, created, err)
	}

	d := advanceToManaged(t, st, gid, 1_000)
	run, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_500)
	if err != nil || !created || run == nil {
		t.Fatalf("EnsureAgentRun = %v, %v, %v; want created", run, created, err)
	}
	if run.Status != AgentRunQueued || run.Attempt != 1 || run.TurnSeq != d.Group.TurnSeq {
		t.Errorf("run = %+v; want queued attempt 1 at turn %d", run, d.Group.TurnSeq)
	}
	again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_600)
	if err != nil || created || again == nil || again.ID != run.ID {
		t.Fatalf("second EnsureAgentRun = %v, %v, %v; want the same run, not created", again, created, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{RequestMsgCount: 3, RequestChars: 120}, 1_700); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	running, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_800)
	if err != nil || created || running == nil || running.Status != AgentRunRunning {
		t.Fatalf("EnsureAgentRun on a running turn = %v, %v, %v", running, created, err)
	}
	if countRunsForTurn(t, st, gid, "bot_3", run.TurnSeq) != 1 {
		t.Errorf("more than one run created for the same turn instance")
	}
}

func TestNoSilentRetryAfterInterrupt(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}

	// Simulate a crash: startup recovery interrupts the open run.
	n, err := st.InterruptStaleAgentRuns(ctx, 5_000)
	if err != nil || n != 1 {
		t.Fatalf("InterruptStaleAgentRuns = %d, %v; want 1", n, err)
	}
	interrupted, err := st.GetAgentRun(ctx, run.ID)
	if err != nil || interrupted.Status != AgentRunInterrupted {
		t.Fatalf("run after recovery = %+v, %v", interrupted, err)
	}
	if got := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); got != 0 {
		t.Errorf("messages for the turn = %d; want 0 (nothing was committed)", got)
	}

	// The scheduler and onGroupChange paths must not create a second attempt.
	again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 6_000)
	if err != nil || created || again == nil || again.ID != run.ID || again.Status != AgentRunInterrupted {
		t.Fatalf("EnsureAgentRun after interrupt = %v, %v, %v; want the interrupted run", again, created, err)
	}
	if got := countRunsForTurn(t, st, gid, "bot_3", run.TurnSeq); got != 1 {
		t.Errorf("runs for the turn = %d; want 1 (no silent retry)", got)
	}
	if countOpenRuns(t, st) != 0 {
		t.Errorf("interrupted run still counts as open")
	}

	// Retry is an explicit admin action and appends attempt 2.
	retry, err := st.RetryAgentRun(ctx, run.ID, 7_000)
	if err != nil {
		t.Fatalf("RetryAgentRun: %v", err)
	}
	if retry.Attempt != 2 || retry.RetryOf == nil || *retry.RetryOf != run.ID || retry.Status != AgentRunQueued {
		t.Errorf("retry run = %+v; want attempt 2 queued with retryOf=%d", retry, run.ID)
	}
	if retry.TurnStartedAt != 7_000 {
		t.Errorf("retry did not reset the turn clock: %d", retry.TurnStartedAt)
	}
	if _, err := st.RetryAgentRun(ctx, run.ID, 7_100); !errors.Is(err, ErrValidation) {
		t.Errorf("retrying a superseded run = %v; want validation error", err)
	}
}

func TestRetryRefusedWhenTurnMovedOn(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	if err := st.FailAgentRun(ctx, run.ID, "provider_error", "boom", 2_000); err != nil {
		t.Fatalf("FailAgentRun: %v", err)
	}
	if err := st.SkipCurrentTurn(ctx, gid, 3_000); err != nil {
		t.Fatalf("SkipCurrentTurn: %v", err)
	}
	if _, err := st.RetryAgentRun(ctx, run.ID, 4_000); !errors.Is(err, ErrValidation) {
		t.Fatalf("RetryAgentRun after skip = %v; want validation error", err)
	}
}

// ---------------------------------------------------------------------------
// Reply consistency
// ---------------------------------------------------------------------------

func TestSubmitManagedReplyCommitsMessageAndStatusTogether(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	// Between start and submit the run is open and no message exists.
	if got := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); got != 0 {
		t.Fatalf("messages before submit = %d; want 0", got)
	}

	accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "hello from the model", AgentUsage{}, 1_300)
	if err != nil || !accepted {
		t.Fatalf("SubmitManagedAgentReply = %v, %v; want accepted", accepted, err)
	}
	done, err := st.GetAgentRun(ctx, run.ID)
	if err != nil || done.Status != AgentRunCompleted || done.MessageID == nil {
		t.Fatalf("run after submit = %+v, %v", done, err)
	}
	var sender string
	var seq int64
	if err := st.ro.QueryRowContext(ctx,
		"SELECT sender_bot_id, turn_seq FROM messages WHERE id = ?", *done.MessageID).
		Scan(&sender, &seq); err != nil {
		t.Fatalf("stored message missing: %v", err)
	}
	if sender != "bot_3" || seq != d.Group.TurnSeq {
		t.Errorf("message sender/turn = %s/%d", sender, seq)
	}
	if countOpenRuns(t, st) != 0 {
		t.Errorf("a completed message left an open run behind")
	}
	after := detail(t, st, gid, 1_300)
	if after.Group.TurnSeq != d.Group.TurnSeq+1 {
		t.Errorf("turnSeq = %d; want %d", after.Group.TurnSeq, d.Group.TurnSeq+1)
	}
	// A second submit for the same run is refused and changes nothing.
	if accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "dup", AgentUsage{}, 1_400); err != nil || accepted {
		t.Errorf("duplicate submit = %v, %v; want refused", accepted, err)
	}
}

func TestSubmitRejectedAfterCancelAndAfterSkip(t *testing.T) {
	ctx := context.Background()

	t.Run("cancelled", func(t *testing.T) {
		st, gid, _ := managedFixture(t, ManagedAgentInput{})
		d := advanceToManaged(t, st, gid, 1_000)
		run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
		if run == nil {
			t.Fatal("no run created")
		}
		if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
			t.Fatal("run did not start")
		}
		if err := st.PauseGroup(ctx, gid, 1_300); err != nil {
			t.Fatalf("PauseGroup: %v", err)
		}
		if err := st.CancelAgentRuns(ctx, gid, 1_300); err != nil {
			t.Fatalf("CancelAgentRuns: %v", err)
		}
		accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "late", AgentUsage{}, 1_400)
		if err != nil || accepted {
			t.Fatalf("submit after cancel = %v, %v; want refused", accepted, err)
		}
		got, _ := st.GetAgentRun(ctx, run.ID)
		if got.Status != AgentRunCancelled {
			t.Errorf("status = %s; want cancelled", got.Status)
		}
		if n := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); n != 0 {
			t.Errorf("messages = %d; want 0", n)
		}
	})

	t.Run("skipped", func(t *testing.T) {
		st, gid, _ := managedFixture(t, ManagedAgentInput{})
		d := advanceToManaged(t, st, gid, 1_000)
		run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
		if run == nil {
			t.Fatal("no run created")
		}
		if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
			t.Fatal("run did not start")
		}
		if err := st.SkipCurrentTurn(ctx, gid, 2_000); err != nil {
			t.Fatalf("SkipCurrentTurn: %v", err)
		}
		accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "late", AgentUsage{}, 2_100)
		if err != nil || accepted {
			t.Fatalf("submit after skip = %v, %v; want refused", accepted, err)
		}
		got, _ := st.GetAgentRun(ctx, run.ID)
		if got.Status != AgentRunCancelled {
			t.Errorf("status = %s; want cancelled", got.Status)
		}
		if n := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); n != 0 {
			t.Errorf("messages = %d; want 0", n)
		}
	})
}

// ---------------------------------------------------------------------------
// Cost and budget
// ---------------------------------------------------------------------------

func TestRunCostComesFromModelPrice(t *testing.T) {
	st, gid, connID := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 270_000, OutputMicrosPer1M: 1_100_000,
	}, 1_000); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if run == nil {
		t.Fatal("no run created")
	}
	if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("run did not start")
	}
	prompt, completion := int64(1_000), int64(500)
	accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "reply",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 1_300)
	if err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	done, _ := st.GetAgentRun(ctx, run.ID)
	if done.CostMicros == nil || *done.CostMicros != 820 {
		t.Fatalf("cost = %v; want 820 micros", done.CostMicros)
	}
	if done.CostBasis != "usage" || done.TotalTokens == nil || *done.TotalTokens != 1_500 {
		t.Errorf("basis/total = %s/%v", done.CostBasis, done.TotalTokens)
	}
	if done.CostConfirmed {
		t.Error("a cost computed from usage+price must not be marked provider-confirmed")
	}
	agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
	usage, err := st.GetManagedAgentUsage(ctx, agent.ID, 1_400)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.RunsLast24h != 1 || usage.SpentMicros != 820 || !usage.CostKnown {
		t.Errorf("usage = %+v; want 1 run, 820 micros, cost known", usage)
	}

	// The pre-run estimate uses the max output and an estimated prompt; its
	// cost is unavailable without a stored price.
	price, err := st.GetModelPrice(ctx, connID, "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelPrice: %v", err)
	}
	if est := EstimateRunCostMicros(price, 1_000, 512); est == nil || *est != 833 {
		t.Errorf("pre-run estimate = %v; want 833 micros", est)
	}
	if est := EstimateRunCostMicros(nil, 1_000, 512); est != nil {
		t.Errorf("estimate without a price = %v; want nil (unavailable)", est)
	}
}

// TestBudgetUnknownBlocksNewRunsAndRetry pins the stricter rule: with a
// monetary cap configured, an unverifiable cost state stops new runs and
// explicit retries, and the reason is exposed instead of silently allowing
// spend. In-flight, interrupted and cancelled requests all leave no cost, so
// they all count as unverifiable because the provider may still bill them.
func TestBudgetUnknownBlocksNewRunsAndRetry(t *testing.T) {
	ctx := context.Background()
	budget := int64(5_000)

	t.Run("in flight", func(t *testing.T) {
		st, gid, _ := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budget})
		d := advanceToManaged(t, st, gid, 1_000)
		run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
		if run == nil {
			t.Fatal("no run created")
		}
		if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
			t.Fatal("run did not start")
		}
		// A skip cycle assigns the turn again while the first request is still
		// in flight and has no recorded cost yet.
		for i := 0; i < 3; i++ {
			if err := st.SkipCurrentTurn(ctx, gid, 2_000+int64(i)); err != nil {
				t.Fatalf("SkipCurrentTurn #%d: %v", i+1, err)
			}
		}
		back := detail(t, st, gid, 2_050)
		if back.Group.TurnEpoch == d.Group.TurnEpoch {
			t.Fatal("skip cycle did not create a new assignment")
		}
		blocked, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 2_100)
		if err != nil || blocked != nil || created {
			t.Fatalf("EnsureAgentRun with an in-flight unknown cost = %+v, %v, %v; want no run", blocked, created, err)
		}
		agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
		usage, _ := st.GetManagedAgentUsage(ctx, agent.ID, 2_100)
		if usage.CostKnown || usage.BudgetState != BudgetUnknown || !usage.BudgetBlocked ||
			usage.BudgetBlockReason != BudgetReasonUnknown || usage.UnknownRuns != 1 {
			t.Fatalf("usage = %+v; want an unknown, blocked budget", usage)
		}
	})

	t.Run("interrupted retry", func(t *testing.T) {
		st, gid, _ := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budget})
		advanceToManaged(t, st, gid, 1_000)
		run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
		if run == nil {
			t.Fatal("no run created")
		}
		if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
			t.Fatal("run did not start")
		}
		if _, err := st.InterruptStaleAgentRuns(ctx, 2_000); err != nil {
			t.Fatalf("InterruptStaleAgentRuns: %v", err)
		}
		if _, err := st.RetryAgentRun(ctx, run.ID, 2_100); !errors.Is(err, ErrValidation) ||
			!strings.Contains(err.Error(), "cannot be verified") {
			t.Fatalf("retry with unknown cost = %v; want a budget-verification error", err)
		}
		// Ensure still returns the interrupted attempt instead of a new one.
		again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 2_200)
		if err != nil || created || again == nil || again.ID != run.ID || again.Status != AgentRunInterrupted {
			t.Fatalf("EnsureAgentRun = %+v, %v, %v; want the interrupted run", again, created, err)
		}
	})

	t.Run("cancelled retry", func(t *testing.T) {
		st, gid, _ := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budget})
		advanceToManaged(t, st, gid, 1_000)
		run, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
		if run == nil {
			t.Fatal("no run created")
		}
		if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
			t.Fatal("run did not start")
		}
		if err := st.CancelAgentRun(ctx, run.ID, 1_300); err != nil {
			t.Fatalf("CancelAgentRun: %v", err)
		}
		if _, err := st.RetryAgentRun(ctx, run.ID, 1_400); !errors.Is(err, ErrValidation) ||
			!strings.Contains(err.Error(), "cannot be verified") {
			t.Fatalf("retry after cancel = %v; want a budget-verification error", err)
		}
		agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
		usage, _ := st.GetManagedAgentUsage(ctx, agent.ID, 1_400)
		if usage.CostKnown || usage.BudgetState != BudgetUnknown || usage.UnknownRuns != 1 {
			t.Fatalf("usage = %+v; a cancelled request must count as unverifiable", usage)
		}
	})
}

func TestDailyRunLimitBlocksExplicitRetry(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{DailyRunLimit: 1})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || !created || run == nil {
		t.Fatalf("EnsureAgentRun = %v, %v, %v", run, created, err)
	}
	if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("run did not start")
	}
	if _, err := st.InterruptStaleAgentRuns(ctx, 2_000); err != nil {
		t.Fatalf("InterruptStaleAgentRuns: %v", err)
	}
	if _, err := st.RetryAgentRun(ctx, run.ID, 2_100); !errors.Is(err, ErrValidation) {
		t.Fatalf("RetryAgentRun at the run cap = %v; want validation error", err)
	}
	if n := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); n != 0 {
		t.Errorf("messages = %d; want 0", n)
	}
}

// TestBudgetGateBlocksWhenKnownSpendReached pins the other pre-run branch: a
// fully recorded window whose spend reached the budget refuses new runs and
// reports budget_reached.
func TestBudgetGateBlocksWhenKnownSpendReached(t *testing.T) {
	budget := int64(500)
	st, gid, connID := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budget})
	ctx := context.Background()
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 270_000, OutputMicrosPer1M: 1_100_000,
	}, 1_000); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, _ := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("run did not start")
	}
	prompt, completion := int64(1_000), int64(500) // 820 micros at this price
	if accepted, err := st.SubmitManagedAgentReply(ctx, run.ID, d.Group.TurnSeq, "reply",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 1_300); err != nil || !accepted {
		t.Fatalf("submit = %v, %v", accepted, err)
	}
	for _, botID := range []string{"bot_1", "bot_2"} {
		d = detail(t, st, gid, 1_400)
		if accepted, err := st.SubmitBotReply(ctx, tokenOf(d, botID), d.Group.TurnSeq,
			"normal reply", 1_400); err != nil || !accepted {
			t.Fatalf("SubmitBotReply(%s) = %v, %v", botID, accepted, err)
		}
	}
	blocked, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_500)
	if err != nil || blocked != nil || created {
		t.Fatalf("EnsureAgentRun over budget = %+v, %v, %v; want no run", blocked, created, err)
	}
	agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
	usage, err := st.GetManagedAgentUsage(ctx, agent.ID, 1_500)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !usage.CostKnown || usage.BudgetState != BudgetExhausted || !usage.BudgetBlocked ||
		usage.BudgetBlockReason != BudgetReasonReached || usage.SpentMicros != 820 ||
		usage.ComputedMicros != 820 || usage.ConfirmedMicros != 0 {
		t.Fatalf("usage = %+v; want known, computed 820 and exhausted", usage)
	}
	done, _ := st.GetAgentRun(ctx, run.ID)
	if done.CostConfirmed {
		t.Errorf("a locally computed cost must not be reported as provider-confirmed")
	}
}

// ---------------------------------------------------------------------------
// Stable turn identity across pause/resume, skip cycles and restart
// ---------------------------------------------------------------------------

func TestResumeDoesNotRetryFailedTurn(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	if err := st.FailAgentRun(ctx, run.ID, "provider_error", "boom", 1_300); err != nil {
		t.Fatalf("FailAgentRun: %v", err)
	}
	if err := st.PauseGroup(ctx, gid, 2_000); err != nil {
		t.Fatalf("PauseGroup: %v", err)
	}
	if err := st.ResumeGroup(ctx, gid, 3_000); err != nil {
		t.Fatalf("ResumeGroup: %v", err)
	}
	resumed := detail(t, st, gid, 3_000)
	if resumed.Group.TurnStartedAt != 3_000 {
		t.Fatalf("resume did not move the turn clock: %d", resumed.Group.TurnStartedAt)
	}
	if resumed.Group.TurnEpoch != d.Group.TurnEpoch {
		t.Fatalf("resume changed the turn epoch: %d -> %d", d.Group.TurnEpoch, resumed.Group.TurnEpoch)
	}

	again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 3_100)
	if err != nil || created || again == nil || again.ID != run.ID || again.Status != AgentRunFailed {
		t.Fatalf("EnsureAgentRun after resume = %+v, %v, %v; want the failed run, not created", again, created, err)
	}
	if got := countRunsForTurn(t, st, gid, "bot_3", d.Group.TurnSeq); got != 1 {
		t.Fatalf("runs after resume = %d; want 1 (no silent retry)", got)
	}

	// Only an explicit retry appends attempt 2, and the scheduler keeps
	// returning that attempt afterwards.
	retry, err := st.RetryAgentRun(ctx, run.ID, 3_200)
	if err != nil || retry == nil || retry.Attempt != 2 || retry.Status != AgentRunQueued {
		t.Fatalf("RetryAgentRun after resume = %+v, %v; want attempt 2 queued", retry, err)
	}
	if retry.TurnEpoch != run.TurnEpoch {
		t.Fatalf("retry changed the turn epoch: %d -> %d", run.TurnEpoch, retry.TurnEpoch)
	}
	if again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 3_300); err != nil || created ||
		again == nil || again.ID != retry.ID {
		t.Fatalf("EnsureAgentRun after retry = %+v, %v, %v; want the retry run", again, created, err)
	}
	if got := countRunsForTurn(t, st, gid, "bot_3", d.Group.TurnSeq); got != 2 {
		t.Fatalf("runs after retry = %d; want 2", got)
	}
}

func TestResumeDoesNotRetryCancelledRun(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	if err := st.PauseGroup(ctx, gid, 1_300); err != nil {
		t.Fatalf("PauseGroup: %v", err)
	}
	if err := st.CancelAgentRuns(ctx, gid, 1_300); err != nil {
		t.Fatalf("CancelAgentRuns: %v", err)
	}
	if err := st.ResumeGroup(ctx, gid, 2_000); err != nil {
		t.Fatalf("ResumeGroup: %v", err)
	}
	again, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 2_100)
	if err != nil || created || again == nil || again.ID != run.ID || again.Status != AgentRunCancelled {
		t.Fatalf("EnsureAgentRun after cancelled+resume = %+v, %v, %v; want the cancelled run", again, created, err)
	}
	if got := countRunsForTurn(t, st, gid, "bot_3", d.Group.TurnSeq); got != 1 {
		t.Fatalf("runs after cancelled+resume = %d; want 1", got)
	}
}

// TestSkipCycleCreatesNewAssignment: skipping keeps turn_seq, but returning
// to the same bot is a NEW assignment with a new epoch. Its old run must not
// block the new run, and its late reply must not be accepted for the new one.
func TestSkipCycleCreatesNewAssignment(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	old, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || old == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", old, err)
	}
	if ok, err := st.StartAgentRun(ctx, old.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}

	// Three skips in a 3-member group return the turn to bot_3 at the same
	// turn_seq but under a new assignment.
	for i := 0; i < 3; i++ {
		if err := st.SkipCurrentTurn(ctx, gid, 2_000+int64(i)); err != nil {
			t.Fatalf("SkipCurrentTurn #%d: %v", i+1, err)
		}
	}
	back := detail(t, st, gid, 2_050)
	if back.Group.TurnSeq != d.Group.TurnSeq || back.Group.CurrentTurnBotID == nil ||
		*back.Group.CurrentTurnBotID != "bot_3" {
		t.Fatalf("after three skips turn = %d/%v; want %d/bot_3",
			back.Group.TurnSeq, back.Group.CurrentTurnBotID, d.Group.TurnSeq)
	}
	if back.Group.TurnEpoch == d.Group.TurnEpoch {
		t.Fatal("skip cycle did not advance the turn epoch")
	}

	fresh, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 2_100)
	if err != nil || !created || fresh == nil || fresh.ID == old.ID ||
		fresh.TurnEpoch != back.Group.TurnEpoch {
		t.Fatalf("EnsureAgentRun after skip cycle = %+v, %v, %v; want a new assignment run", fresh, created, err)
	}

	// Skip cancels the open run atomically, so a late reply from the old
	// assignment is refused even though turn_seq and the speaker are the same
	// again under a new turn_epoch.
	accepted, err := st.SubmitManagedAgentReply(ctx, old.ID, d.Group.TurnSeq, "late", AgentUsage{}, 2_200)
	if err != nil || accepted {
		t.Fatalf("late reply from the old assignment = %v, %v; want refused", accepted, err)
	}
	gotOld, _ := st.GetAgentRun(ctx, old.ID)
	if gotOld.Status != AgentRunCancelled {
		t.Fatalf("old run status = %s; want cancelled", gotOld.Status)
	}
	if n := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); n != 0 {
		t.Fatalf("messages after the late reply = %d; want 0", n)
	}

	// The new assignment can reply normally, and only its message is stored.
	if ok, _ := st.StartAgentRun(ctx, fresh.ID, AgentRunStart{}, 2_300); !ok {
		t.Fatal("the new assignment run did not start")
	}
	accepted, err = st.SubmitManagedAgentReply(ctx, fresh.ID, back.Group.TurnSeq, "fresh", AgentUsage{}, 2_400)
	if err != nil || !accepted {
		t.Fatalf("reply from the new assignment = %v, %v; want accepted", accepted, err)
	}
	if n := countMessagesForTurn(t, st, gid, d.Group.TurnSeq); n != 1 {
		t.Fatalf("messages after the fresh reply = %d; want 1", n)
	}
}

func TestRestartAndResumeDoNotRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	st, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gid, _ := managedFixtureOn(t, st, ManagedAgentInput{})
	epoch := advanceToManaged(t, st, gid, 1_000).Group.TurnEpoch
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := Open(path, testEncryptionKey)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st2.Close() }()
	n, err := st2.InterruptStaleAgentRuns(ctx, 2_000)
	if err != nil || n != 1 {
		t.Fatalf("InterruptStaleAgentRuns = %d, %v; want 1", n, err)
	}
	if err := st2.PauseGroup(ctx, gid, 3_000); err != nil {
		t.Fatalf("PauseGroup: %v", err)
	}
	if err := st2.ResumeGroup(ctx, gid, 4_000); err != nil {
		t.Fatalf("ResumeGroup: %v", err)
	}
	if after := detail(t, st2, gid, 4_000); after.Group.TurnEpoch != epoch {
		t.Fatalf("restart+resume changed the turn epoch: %d -> %d", epoch, after.Group.TurnEpoch)
	}
	again, created, err := st2.EnsureAgentRun(ctx, gid, "bot_3", 4_100)
	if err != nil || created || again == nil || again.ID != run.ID || again.Status != AgentRunInterrupted {
		t.Fatalf("EnsureAgentRun after restart+resume = %+v, %v, %v; want the interrupted run", again, created, err)
	}
	if got := countRunsForTurn(t, st2, gid, "bot_3", run.TurnSeq); got != 1 {
		t.Fatalf("runs after restart+resume = %d; want 1", got)
	}
}

func TestEnsureAndRetryRaceKeepsOneAttemptChain(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	d := advanceToManaged(t, st, gid, 1_000)
	run, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || run == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", run, err)
	}
	if ok, err := st.StartAgentRun(ctx, run.ID, AgentRunStart{}, 1_200); err != nil || !ok {
		t.Fatalf("StartAgentRun = %v, %v", ok, err)
	}
	if err := st.FailAgentRun(ctx, run.ID, "provider_error", "boom", 1_300); err != nil {
		t.Fatalf("FailAgentRun: %v", err)
	}

	// Scheduler Ensure calls race the admin's explicit Retry. Exactly one
	// retry may win and no Ensure may append another attempt.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := 2_000 + int64(i)
			if i%2 == 0 {
				_, _, _ = st.EnsureAgentRun(ctx, gid, "bot_3", now)
				return
			}
			_, _ = st.RetryAgentRun(ctx, run.ID, now)
		}(i)
	}
	wg.Wait()

	if got := countRunsForTurn(t, st, gid, "bot_3", d.Group.TurnSeq); got != 2 {
		t.Fatalf("runs after Ensure/Retry race = %d; want the failed run + one retry", got)
	}
	var maxAttempt int64
	if err := st.ro.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(attempt), 0) FROM agent_runs WHERE group_id = ? AND bot_id = 'bot_3' AND turn_seq = ?",
		gid, d.Group.TurnSeq).Scan(&maxAttempt); err != nil {
		t.Fatalf("max attempt: %v", err)
	}
	if maxAttempt != 2 {
		t.Errorf("max attempt = %d; want 2", maxAttempt)
	}
	if got := countOpenRuns(t, st); got != 1 {
		t.Errorf("open runs = %d; want only the single retry", got)
	}
}

// TestSubmitReplyRacesCancellationConsistently races the atomic reply commit
// against the admin cancel. Either the reply wins (message + completed) or the
// cancel wins (no message, cancelled); a message must never survive with a
// non-completed run.
func TestSubmitReplyRacesCancellationConsistently(t *testing.T) {
	st, gid, _ := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	advanceToManaged(t, st, gid, 1_000)
	old, _, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if err != nil || old == nil {
		t.Fatalf("EnsureAgentRun: %v, %v", old, err)
	}
	if ok, _ := st.StartAgentRun(ctx, old.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("old run did not start")
	}
	// A skip cycle makes the old run stale and assigns a fresh run.
	for i := 0; i < 3; i++ {
		if err := st.SkipCurrentTurn(ctx, gid, 1_300+int64(i)); err != nil {
			t.Fatalf("SkipCurrentTurn: %v", err)
		}
	}
	back := detail(t, st, gid, 1_350)
	fresh, created, err := st.EnsureAgentRun(ctx, gid, "bot_3", 1_400)
	if err != nil || !created || fresh == nil {
		t.Fatalf("EnsureAgentRun for the new assignment = %+v, %v, %v", fresh, created, err)
	}
	if ok, _ := st.StartAgentRun(ctx, fresh.ID, AgentRunStart{}, 1_500); !ok {
		t.Fatal("fresh run did not start")
	}

	var wg sync.WaitGroup
	var accepted bool
	var submitErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		accepted, submitErr = st.SubmitManagedAgentReply(ctx, fresh.ID, back.Group.TurnSeq,
			"racer", AgentUsage{}, 1_600)
	}()
	go func() {
		defer wg.Done()
		_ = st.CancelAgentRuns(ctx, gid, 1_600)
	}()
	wg.Wait()

	got, err := st.GetAgentRun(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("GetAgentRun: %v", err)
	}
	messages := countMessagesForTurn(t, st, gid, back.Group.TurnSeq)
	switch got.Status {
	case AgentRunCompleted:
		if submitErr != nil || !accepted {
			t.Fatalf("completed run but submit = %v, %v", accepted, submitErr)
		}
		if messages != 1 {
			t.Fatalf("completed run with %d messages; want 1", messages)
		}
	case AgentRunCancelled, AgentRunDiscarded:
		if accepted {
			t.Fatalf("a reply was accepted but its run ended %s", got.Status)
		}
		if messages != 0 {
			t.Fatalf("a %s run left %d messages; want 0", got.Status, messages)
		}
	default:
		t.Fatalf("unexpected run status %q", got.Status)
	}
	if countOpenRuns(t, st) != 0 {
		t.Error("the race left an open run behind")
	}
}

// TestBudgetDistinguishesConfirmedAndComputedCost keeps the two cost sources
// apart: provider-reported charges are confirmed, locally computed ones only
// contribute to the computed bucket.
func TestBudgetDistinguishesConfirmedAndComputedCost(t *testing.T) {
	budget := int64(100_000)
	st, gid, connID := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budget})
	ctx := context.Background()
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 270_000, OutputMicrosPer1M: 1_100_000,
	}, 1_000); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	d := advanceToManaged(t, st, gid, 1_000)
	first, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if first == nil {
		t.Fatal("no first run")
	}
	if ok, _ := st.StartAgentRun(ctx, first.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("first run did not start")
	}
	prompt, completion := int64(1_000), int64(500)
	if accepted, err := st.SubmitManagedAgentReply(ctx, first.ID, d.Group.TurnSeq, "computed",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 1_300); err != nil || !accepted {
		t.Fatalf("first submit = %v, %v", accepted, err)
	}
	replyAsCurrent(t, st, gid, 1_400)
	replyAsCurrent(t, st, gid, 1_500)
	d2 := detail(t, st, gid, 1_550)
	second, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_600)
	if second == nil {
		t.Fatal("no second run")
	}
	if ok, _ := st.StartAgentRun(ctx, second.ID, AgentRunStart{}, 1_650); !ok {
		t.Fatal("second run did not start")
	}
	providerCost := int64(2_000)
	if accepted, err := st.SubmitManagedAgentReply(ctx, second.ID, d2.Group.TurnSeq, "confirmed",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion, CostMicros: &providerCost}, 1_700); err != nil || !accepted {
		t.Fatalf("second submit = %v, %v", accepted, err)
	}
	done, _ := st.GetAgentRun(ctx, second.ID)
	if done.CostMicros == nil || *done.CostMicros != 2_000 || !done.CostConfirmed {
		t.Fatalf("provider cost = %v confirmed=%v; want 2000 confirmed", done.CostMicros, done.CostConfirmed)
	}
	agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
	usage, _ := st.GetManagedAgentUsage(ctx, agent.ID, 1_800)
	if !usage.CostKnown || usage.BudgetState != BudgetOK || usage.BudgetBlocked ||
		usage.SpentMicros != 2_820 || usage.ConfirmedMicros != 2_000 || usage.ComputedMicros != 820 {
		t.Fatalf("usage = %+v; want 2820 = 2000 confirmed + 820 computed, state ok", usage)
	}
}

// TestPriceChangeAffectsOnlyFutureRuns: stored costs are not repriced when the
// price table changes. A missing price leaves the new run's cost unknown, so a
// configured budget would stop further requests.
func TestPriceChangeAffectsOnlyFutureRuns(t *testing.T) {
	st, gid, connID := managedFixture(t, ManagedAgentInput{})
	ctx := context.Background()
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 270_000, OutputMicrosPer1M: 1_100_000,
	}, 1_000); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	d := advanceToManaged(t, st, gid, 1_000)
	first, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 1_100)
	if first == nil {
		t.Fatal("no first run")
	}
	if ok, _ := st.StartAgentRun(ctx, first.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("first run did not start")
	}
	prompt, completion := int64(1_000), int64(500)
	if accepted, err := st.SubmitManagedAgentReply(ctx, first.ID, d.Group.TurnSeq, "cheap",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 1_300); err != nil || !accepted {
		t.Fatalf("first submit = %v, %v", accepted, err)
	}
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 2_700_000, OutputMicrosPer1M: 11_000_000,
	}, 2_000); err != nil {
		t.Fatalf("SetModelPrice (new): %v", err)
	}
	agent, _ := st.GetManagedAgent(ctx, gid, "bot_3")
	usage, _ := st.GetManagedAgentUsage(ctx, agent.ID, 2_050)
	if usage.SpentMicros != 820 || usage.ComputedMicros != 820 {
		t.Fatalf("usage after a price change = %+v; stored costs must not be repriced", usage)
	}

	replyAsCurrent(t, st, gid, 2_100)
	replyAsCurrent(t, st, gid, 2_200)
	d2 := detail(t, st, gid, 2_250)
	second, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 2_300)
	if second == nil {
		t.Fatal("no second run")
	}
	if ok, _ := st.StartAgentRun(ctx, second.ID, AgentRunStart{}, 2_400); !ok {
		t.Fatal("second run did not start")
	}
	if accepted, err := st.SubmitManagedAgentReply(ctx, second.ID, d2.Group.TurnSeq, "expensive",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 2_500); err != nil || !accepted {
		t.Fatalf("second submit = %v, %v", accepted, err)
	}
	done, _ := st.GetAgentRun(ctx, second.ID)
	if done.CostMicros == nil || *done.CostMicros != 8_200 {
		t.Fatalf("second cost = %v; want 8200 at the new price", done.CostMicros)
	}
	usage, _ = st.GetManagedAgentUsage(ctx, agent.ID, 2_600)
	if usage.SpentMicros != 9_020 {
		t.Errorf("window spend = %d; want 9020", usage.SpentMicros)
	}

	if err := st.DeleteModelPrice(ctx, connID, "deepseek-chat"); err != nil {
		t.Fatalf("DeleteModelPrice: %v", err)
	}
	replyAsCurrent(t, st, gid, 2_700)
	replyAsCurrent(t, st, gid, 2_800)
	d3 := detail(t, st, gid, 2_850)
	third, _, _ := st.EnsureAgentRun(ctx, gid, "bot_3", 2_900)
	if third == nil {
		t.Fatal("no third run")
	}
	if ok, _ := st.StartAgentRun(ctx, third.ID, AgentRunStart{}, 3_000); !ok {
		t.Fatal("third run did not start")
	}
	if accepted, err := st.SubmitManagedAgentReply(ctx, third.ID, d3.Group.TurnSeq, "unknown",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 3_100); err != nil || !accepted {
		t.Fatalf("third submit = %v, %v", accepted, err)
	}
	unknown, _ := st.GetAgentRun(ctx, third.ID)
	if unknown.CostMicros != nil || unknown.CostConfirmed {
		t.Fatalf("cost without a price = %v confirmed=%v; want unavailable", unknown.CostMicros, unknown.CostConfirmed)
	}
	usage, _ = st.GetManagedAgentUsage(ctx, agent.ID, 3_200)
	if usage.CostKnown || usage.UnknownRuns != 1 || usage.SpentMicros != 9_020 {
		t.Fatalf("usage = %+v; want an unknown run and a 9020 lower bound", usage)
	}
}

// TestBudgetIsPerAgentAcrossGroups proves budgets are not shared between
// agents or groups: an exhausted agent does not block a different agent.
func TestBudgetIsPerAgentAcrossGroups(t *testing.T) {
	ctx := context.Background()
	budgetA := int64(500)
	st, gidA, connID := managedFixture(t, ManagedAgentInput{DailyBudgetMicros: &budgetA})
	if err := st.SetModelPrice(ctx, ModelPrice{
		ConnectionID: connID, Model: "deepseek-chat",
		InputMicrosPer1M: 270_000, OutputMicrosPer1M: 1_100_000,
	}, 1_000); err != nil {
		t.Fatalf("SetModelPrice: %v", err)
	}
	d := advanceToManaged(t, st, gidA, 1_000)
	runA, _, _ := st.EnsureAgentRun(ctx, gidA, "bot_3", 1_100)
	if runA == nil {
		t.Fatal("no run in group A")
	}
	if ok, _ := st.StartAgentRun(ctx, runA.ID, AgentRunStart{}, 1_200); !ok {
		t.Fatal("group A run did not start")
	}
	prompt, completion := int64(1_000), int64(500)
	if accepted, err := st.SubmitManagedAgentReply(ctx, runA.ID, d.Group.TurnSeq, "A",
		AgentUsage{PromptTokens: &prompt, CompletionTokens: &completion}, 1_300); err != nil || !accepted {
		t.Fatalf("group A submit = %v, %v", accepted, err)
	}
	replyAsCurrent(t, st, gidA, 1_400)
	replyAsCurrent(t, st, gidA, 1_500)

	gidB := createTestGroup(t, st, 2_000, "auto_round_robin", realMembers[:2])
	budgetB := int64(10_000)
	if _, err := st.CreateManagedAgent(ctx, gidB, MemberInput{Name: "Writer B"}, ManagedAgentInput{
		ConnectionID: connID, Model: "deepseek-chat", DailyBudgetMicros: &budgetB, Enabled: true,
	}, 2_000); err != nil {
		t.Fatalf("CreateManagedAgent in group B: %v", err)
	}
	advanceToManaged(t, st, gidB, 2_100)
	agentB, _ := st.GetManagedAgent(ctx, gidB, "bot_3")
	usageB, _ := st.GetManagedAgentUsage(ctx, agentB.ID, 2_150)
	if !usageB.CostKnown || usageB.BudgetState != BudgetOK || usageB.BudgetBlocked || usageB.RunsLast24h != 0 {
		t.Fatalf("group B usage before its first run = %+v; want an independent ok budget", usageB)
	}

	blocked, created, err := st.EnsureAgentRun(ctx, gidA, "bot_3", 2_200)
	if err != nil || blocked != nil || created {
		t.Fatalf("group A EnsureAgentRun = %+v, %v, %v; want blocked", blocked, created, err)
	}
	runB, created, err := st.EnsureAgentRun(ctx, gidB, "bot_3", 2_200)
	if err != nil || !created || runB == nil {
		t.Fatalf("group B EnsureAgentRun = %+v, %v, %v; want a new run", runB, created, err)
	}
}
