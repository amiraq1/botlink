package store

// Tests for Phase 1: migration 004_tools, Tool CRUD, member-tool assignments,
// and tool-call idempotency.  All tests run against an in-memory SQLite DB
// (or a temp-file DB where multiple opens are needed).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// openTestStoreKeyed opens an in-memory store with a fixed 32-byte key so that
// AES-GCM encrypt/decrypt is exercised.
func openTestStoreKeyed(t *testing.T) *Store {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func makeTestGroup(t *testing.T, st *Store, now int64) int64 {
	t.Helper()
	id, err := st.CreateGroup(context.Background(), CreateGroupInput{
		Name: "G", Description: "d", Topic: "t", TurnMode: "auto_round_robin",
		MaxTurns: 10,
		Members: []MemberInput{
			{Name: "Bot1", RoleTitle: "r1", AccentColor: "#3B82F6"},
			{Name: "Bot2", RoleTitle: "r2", AccentColor: "#A855F7"},
		},
	}, now)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	return id
}

// firstBotID returns the bot_id of the first member (bot_1).
const firstBotID = "bot_1"

func validToolInput(name string) ToolInput {
	return ToolInput{
		Name:        name,
		Description: "A test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`),
		TimeoutMs:   3000,
		Enabled:     true,
		Kind:        ToolKindBuiltin,
	}
}

// ---------------------------------------------------------------------------
// Migration tests
// ---------------------------------------------------------------------------

// TestMigration4Fresh verifies that migration 004_tools applies cleanly to a
// brand-new database and that all three tables exist with the expected columns.
func TestMigration4Fresh(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	// schema_migrations must contain exactly 4 rows.
	var migCount int
	if err := st.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations").Scan(&migCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migCount != 4 {
		t.Errorf("want 4 migration rows, got %d", migCount)
	}

	// All three new tables must be queryable.
	for _, tbl := range []string{"tools", "member_tools", "tool_calls"} {
		var n int
		if err := st.ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Errorf("table %q missing or broken: %v", tbl, err)
		}
	}

	// UNIQUE index on tools.name must exist (confirmed by inserting a duplicate below
	// in TestCreateTool_DuplicateName, but check here via PRAGMA).
	rows, err := st.ro.QueryContext(ctx, "PRAGMA index_list(tools)")
	if err != nil {
		t.Fatalf("index_list: %v", err)
	}
	defer rows.Close()
	var foundUnique bool
	for rows.Next() {
		var seq int
		var name, origin string
		var unique int
		var partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list: %v", err)
		}
		if unique == 1 {
			foundUnique = true
		}
	}
	if !foundUnique {
		t.Error("expected at least one UNIQUE index on tools table")
	}
}

// TestMigration4Idempotent opens the same file-based DB twice and verifies
// that migration 4 is applied exactly once (no duplicate schema_migration rows,
// no errors on the second open).
func TestMigration4Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idempotent.db")
	key := make([]byte, 32)

	// First open: all 4 migrations run.
	s1, err := Open(dbPath, key)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = s1.Close()

	// Second open: no migration should re-run.
	s2, err := Open(dbPath, key)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	var migCount int
	if err := s2.ro.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migrations").Scan(&migCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migCount != 4 {
		t.Errorf("want exactly 4 migration rows after second open, got %d", migCount)
	}

	// Verify version 4 appears exactly once.
	var v4Count int
	if err := s2.ro.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 4").Scan(&v4Count); err != nil {
		t.Fatalf("count v4: %v", err)
	}
	if v4Count != 1 {
		t.Errorf("migration 4 should appear exactly once, got %d", v4Count)
	}
}

// TestMigration4OnPreExistingDB simulates a database that already has
// migrations 1-3 applied and verifies that migration 4 is correctly applied
// on the subsequent open.
func TestMigration4OnPreExistingDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "preexisting.db")
	key := make([]byte, 32)

	// Open fresh: all 4 migrations run.
	s1, err := Open(dbPath, key)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}

	// Undo migration 4: remove the schema_migration record and drop the tables,
	// simulating a DB that was created before this migration existed.
	for _, stmt := range []string{
		"DELETE FROM schema_migrations WHERE version = 4",
		"DROP TABLE IF EXISTS tool_calls",
		"DROP TABLE IF EXISTS member_tools",
		"DROP TABLE IF EXISTS tools",
	} {
		if _, err := s1.db.Exec(stmt); err != nil {
			t.Fatalf("undo migration 4 (%s): %v", stmt, err)
		}
	}
	_ = s1.Close()

	// Reopen: should detect the missing migration 4 and re-apply it.
	s2, err := Open(dbPath, key)
	if err != nil {
		t.Fatalf("reopen after undo: %v", err)
	}
	defer func() { _ = s2.Close() }()

	ctx := context.Background()

	// tools table must now exist.
	var n int
	if err := s2.ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM tools").Scan(&n); err != nil {
		t.Fatalf("tools table missing after reopen: %v", err)
	}

	// schema_migrations must have all 4 entries.
	var migCount int
	if err := s2.ro.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations").Scan(&migCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migCount != 4 {
		t.Errorf("want 4 migration rows after re-applying v4, got %d", migCount)
	}
}

// ---------------------------------------------------------------------------
// Tool CRUD
// ---------------------------------------------------------------------------

func TestCreateTool_OK(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := validToolInput("get_time")
	id, err := st.CreateTool(ctx, in, 1000)
	if err != nil {
		t.Fatalf("CreateTool: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive tool ID, got %d", id)
	}

	got, err := st.GetTool(ctx, id)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	if got.Name != "get_time" {
		t.Errorf("name: want get_time, got %s", got.Name)
	}
	if got.Kind != ToolKindBuiltin {
		t.Errorf("kind: want builtin, got %s", got.Kind)
	}
	if !got.Enabled {
		t.Error("tool should be enabled")
	}
	// InputSchema must be valid JSON.
	var v any
	if err := json.Unmarshal(got.InputSchema, &v); err != nil {
		t.Errorf("inputSchema not valid JSON: %v", err)
	}
}

func TestCreateTool_WebhookEncryption(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := ToolInput{
		Name:           "my_hook",
		Description:    "webhook test",
		InputSchema:    json.RawMessage(`{}`),
		TimeoutMs:      5000,
		Enabled:        true,
		Kind:           ToolKindHTTPWebhook,
		WebhookURL:     "https://example.com/hook",
		WebhookHeaders: map[string]string{"Authorization": "Bearer secret123"},
		WebhookSecret:  "signing-secret-xyz",
	}
	id, err := st.CreateTool(ctx, in, 1000)
	if err != nil {
		t.Fatalf("CreateTool webhook: %v", err)
	}

	got, err := st.GetTool(ctx, id)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}

	// Secrets must NOT be empty (they were encrypted).
	if got.WebhookHeadersEnc == "" {
		t.Error("WebhookHeadersEnc should be non-empty after encryption")
	}
	if got.WebhookSecretEnc == "" {
		t.Error("WebhookSecretEnc should be non-empty after encryption")
	}

	// Webhook URL is NOT secret and must be stored plaintext.
	if got.WebhookURL != "https://example.com/hook" {
		t.Errorf("WebhookURL: got %q", got.WebhookURL)
	}

	// Decrypt and verify headers roundtrip.
	plainHeaders, err := DecryptToolSecret(st.key(), got.WebhookHeadersEnc, id)
	if err != nil {
		t.Fatalf("decrypt headers: %v", err)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(plainHeaders), &headers); err != nil {
		t.Fatalf("unmarshal decrypted headers: %v", err)
	}
	if headers["Authorization"] != "Bearer secret123" {
		t.Errorf("decrypted Authorization: got %q", headers["Authorization"])
	}

	// Decrypt and verify signing secret roundtrip.
	plainSecret, err := DecryptToolSecret(st.key(), got.WebhookSecretEnc, id)
	if err != nil {
		t.Fatalf("decrypt secret: %v", err)
	}
	if plainSecret != "signing-secret-xyz" {
		t.Errorf("decrypted signing secret: got %q", plainSecret)
	}

	// Confirm that JSON marshalling NEVER leaks the encrypted fields.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if containsAny(string(b), "WebhookHeadersEnc", "WebhookSecretEnc", "webhook_headers_enc", "webhook_secret_enc") {
		t.Error("json.Marshal of Tool leaked encrypted field names")
	}
	if containsAny(string(b), "secret123", "signing-secret-xyz") {
		t.Error("json.Marshal of Tool leaked plaintext secret content")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func TestCreateTool_DuplicateName(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := validToolInput("calculate")
	if _, err := st.CreateTool(ctx, in, 1000); err != nil {
		t.Fatalf("first CreateTool: %v", err)
	}
	_, err := st.CreateTool(ctx, in, 1001)
	if err == nil {
		t.Fatal("expected error on duplicate tool name, got nil")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("want ErrValidation, got %v", err)
	}
}

func TestCreateTool_InvalidName(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	bad := []string{"CamelCase", "1_start_with_digit", "has space", "has-dash", ""}
	for _, name := range bad {
		in := validToolInput(name)
		if name == "" {
			in.Name = ""
		}
		_, err := st.CreateTool(ctx, in, 1000)
		if err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

func TestCreateTool_InvalidSchema(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := validToolInput("bad_schema")
	in.InputSchema = json.RawMessage(`{not valid json`)
	_, err := st.CreateTool(ctx, in, 1000)
	if err == nil {
		t.Fatal("expected error for invalid input_schema")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("want ErrValidation, got %v", err)
	}
}

func TestCreateTool_EmptySchemaDefaultsToObject(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := validToolInput("no_schema")
	in.InputSchema = nil
	id, err := st.CreateTool(ctx, in, 1000)
	if err != nil {
		t.Fatalf("CreateTool nil schema: %v", err)
	}
	got, err := st.GetTool(ctx, id)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}
	var v any
	if err := json.Unmarshal(got.InputSchema, &v); err != nil {
		t.Errorf("defaulted schema not valid JSON: %v", err)
	}
}

func TestUpdateTool_OK(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	id, _ := st.CreateTool(ctx, validToolInput("original"), 1000)

	updated := validToolInput("updated_name")
	updated.Description = "new desc"
	updated.TimeoutMs = 8000
	if err := st.UpdateTool(ctx, id, updated, 2000); err != nil {
		t.Fatalf("UpdateTool: %v", err)
	}
	got, _ := st.GetTool(ctx, id)
	if got.Name != "updated_name" {
		t.Errorf("name not updated: got %q", got.Name)
	}
	if got.TimeoutMs != 8000 {
		t.Errorf("timeout not updated: got %d", got.TimeoutMs)
	}
	if got.UpdatedAt != 2000 {
		t.Errorf("updated_at: got %d, want 2000", got.UpdatedAt)
	}
}

func TestUpdateTool_NotFound(t *testing.T) {
	st := openTestStoreKeyed(t)
	err := st.UpdateTool(context.Background(), 9999, validToolInput("x"), 1000)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestEnableTool(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := validToolInput("toggle_me")
	in.Enabled = true
	id, _ := st.CreateTool(ctx, in, 1000)

	if err := st.EnableTool(ctx, id, false, 2000); err != nil {
		t.Fatalf("EnableTool false: %v", err)
	}
	got, _ := st.GetTool(ctx, id)
	if got.Enabled {
		t.Error("tool should be disabled")
	}

	if err := st.EnableTool(ctx, id, true, 3000); err != nil {
		t.Fatalf("EnableTool true: %v", err)
	}
	got, _ = st.GetTool(ctx, id)
	if !got.Enabled {
		t.Error("tool should be re-enabled")
	}
}

func TestDeleteTool_Cascade(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("cascaded"), 1000)
	_ = st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 3)

	// Confirm assignment exists.
	mts, err := st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if err != nil || len(mts) != 1 {
		t.Fatalf("want 1 member_tool before delete, got %d (err=%v)", len(mts), err)
	}

	// Delete the tool; member_tools and tool_calls for it should cascade.
	if err := st.DeleteTool(ctx, toolID); err != nil {
		t.Fatalf("DeleteTool: %v", err)
	}

	mts, _ = st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if len(mts) != 0 {
		t.Errorf("expected member_tools to cascade-delete, got %d rows", len(mts))
	}

	// GetTool must return ErrNotFound.
	if _, err := st.GetTool(ctx, toolID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetTool after delete: want ErrNotFound, got %v", err)
	}
}

func TestListTools(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	names := []string{"zzz_last", "aaa_first", "mmm_middle"}
	for _, n := range names {
		if _, err := st.CreateTool(ctx, validToolInput(n), 1000); err != nil {
			t.Fatalf("CreateTool %s: %v", n, err)
		}
	}
	tools, err := st.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
	}
	// Must be ordered by name ascending.
	if tools[0].Name != "aaa_first" || tools[1].Name != "mmm_middle" || tools[2].Name != "zzz_last" {
		t.Errorf("unexpected order: %v", tools)
	}
}

// ---------------------------------------------------------------------------
// Member-tool assignments
// ---------------------------------------------------------------------------

func TestSetMemberTool_Upsert(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("calc"), 1000)

	// First assign.
	if err := st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 3); err != nil {
		t.Fatalf("SetMemberTool: %v", err)
	}
	mts, _ := st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if len(mts) != 1 || !mts[0].Enabled || mts[0].MaxCallsPerTurn != 3 {
		t.Fatalf("unexpected after first assign: %+v", mts)
	}

	// Upsert: change maxCalls and disable.
	if err := st.SetMemberTool(ctx, gid, firstBotID, toolID, false, 5); err != nil {
		t.Fatalf("SetMemberTool upsert: %v", err)
	}
	mts, _ = st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if len(mts) != 1 || mts[0].Enabled || mts[0].MaxCallsPerTurn != 5 {
		t.Fatalf("unexpected after upsert: %+v", mts)
	}
}

func TestSetMemberTool_MaxCallsClamped(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("clamped"), 1000)

	// 0 → clamped to 1.
	_ = st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 0)
	mts, _ := st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if mts[0].MaxCallsPerTurn != 1 {
		t.Errorf("0 not clamped to 1, got %d", mts[0].MaxCallsPerTurn)
	}

	// 100 → clamped to 20.
	_ = st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 100)
	mts, _ = st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if mts[0].MaxCallsPerTurn != 20 {
		t.Errorf("100 not clamped to 20, got %d", mts[0].MaxCallsPerTurn)
	}
}

func TestRemoveMemberTool(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("removable"), 1000)
	_ = st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 3)

	if err := st.RemoveMemberTool(ctx, gid, firstBotID, toolID); err != nil {
		t.Fatalf("RemoveMemberTool: %v", err)
	}
	mts, _ := st.ListMemberToolsForBot(ctx, gid, firstBotID)
	if len(mts) != 0 {
		t.Errorf("expected empty after remove, got %d", len(mts))
	}

	// Second remove should return ErrNotFound.
	if err := st.RemoveMemberTool(ctx, gid, firstBotID, toolID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second RemoveMemberTool: want ErrNotFound, got %v", err)
	}
}

func TestGetToolsForMember_FiltersDisabled(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)

	// Three tools: enabled, disabled-globally, disabled-in-assignment.
	t1, _ := st.CreateTool(ctx, validToolInput("tool_on"), 1000)
	t2, _ := st.CreateTool(ctx, validToolInput("tool_global_off"), 1000)
	t3, _ := st.CreateTool(ctx, validToolInput("tool_assign_off"), 1000)

	_ = st.SetMemberTool(ctx, gid, firstBotID, t1, true, 3)
	_ = st.SetMemberTool(ctx, gid, firstBotID, t2, true, 3)
	_ = st.SetMemberTool(ctx, gid, firstBotID, t3, false, 3) // disabled in assignment

	// Disable t2 globally.
	_ = st.EnableTool(ctx, t2, false, 2000)

	pts, err := st.GetToolsForMember(ctx, gid, firstBotID)
	if err != nil {
		t.Fatalf("GetToolsForMember: %v", err)
	}
	if len(pts) != 1 || pts[0].Name != "tool_on" {
		t.Errorf("expected only tool_on, got %v", pts)
	}
}

func TestCheckMemberToolEnabled(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("chk"), 1000)
	_ = st.SetMemberTool(ctx, gid, firstBotID, toolID, true, 7)

	ok, maxCalls, err := st.CheckMemberToolEnabled(ctx, gid, firstBotID, toolID)
	if err != nil {
		t.Fatalf("CheckMemberToolEnabled: %v", err)
	}
	if !ok || maxCalls != 7 {
		t.Errorf("want (true, 7), got (%v, %d)", ok, maxCalls)
	}

	// Non-existent assignment.
	ok, maxCalls, err = st.CheckMemberToolEnabled(ctx, gid, firstBotID, 9999)
	if err != nil {
		t.Fatalf("CheckMemberToolEnabled unknown: %v", err)
	}
	if ok || maxCalls != 0 {
		t.Errorf("want (false, 0) for unknown, got (%v, %d)", ok, maxCalls)
	}
}

// ---------------------------------------------------------------------------
// Tool-call idempotency
// ---------------------------------------------------------------------------

func makeToolCallInsert(groupID int64, toolID int64, callID string, turnSeq int64) ToolCallInsert {
	return ToolCallInsert{
		GroupID:   groupID,
		BotID:     firstBotID,
		ToolID:    toolID,
		CallID:    callID,
		TurnSeq:   turnSeq,
		Arguments: json.RawMessage(`{"x":42}`),
		CreatedAt: 1000,
	}
}

func TestInsertToolCallPending_FreshInsert(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("fresh_tool"), 1000)

	in := makeToolCallInsert(gid, toolID, "call-aaa", 1)
	call, created, err := st.InsertToolCallPending(ctx, in)
	if err != nil {
		t.Fatalf("InsertToolCallPending: %v", err)
	}
	if !created {
		t.Error("first insert: created should be true")
	}
	if call.Status != ToolCallStatusPending {
		t.Errorf("status: want pending, got %s", call.Status)
	}
	if call.CallID != "call-aaa" {
		t.Errorf("callID: got %s", call.CallID)
	}
	if call.ID <= 0 {
		t.Error("expected positive ID")
	}
}

func TestInsertToolCallPending_DuplicatePending(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("dup_tool"), 1000)

	in := makeToolCallInsert(gid, toolID, "call-dup", 1)
	first, _, _ := st.InsertToolCallPending(ctx, in)

	// Second insert of the same callID while still pending.
	second, created, err := st.InsertToolCallPending(ctx, in)
	if err != nil {
		t.Fatalf("second InsertToolCallPending: %v", err)
	}
	if created {
		t.Error("duplicate insert: created should be false")
	}
	if second.Status != ToolCallStatusPending {
		t.Errorf("status: want pending, got %s", second.Status)
	}
	if second.ID != first.ID {
		t.Errorf("IDs must match: first=%d second=%d", first.ID, second.ID)
	}
	// Handler must return 409 for still-pending duplicates (checked by caller, not store).
}

func TestInsertToolCallPending_DuplicateCompleted(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("comp_tool"), 1000)

	in := makeToolCallInsert(gid, toolID, "call-comp", 1)
	first, _, _ := st.InsertToolCallPending(ctx, in)

	// Complete the call.
	if err := st.CompleteToolCall(ctx, first.ID, ToolCallStatusSuccess, `{"result":1}`, "", 50, 2000); err != nil {
		t.Fatalf("CompleteToolCall: %v", err)
	}

	// Third insert of the same callID — should return cached completed result.
	cached, created, err := st.InsertToolCallPending(ctx, in)
	if err != nil {
		t.Fatalf("InsertToolCallPending after complete: %v", err)
	}
	if created {
		t.Error("created should be false after completion")
	}
	if cached.Status != ToolCallStatusSuccess {
		t.Errorf("cached status: want success, got %s", cached.Status)
	}
	if cached.Result != `{"result":1}` {
		t.Errorf("cached result: got %q", cached.Result)
	}
}

func TestCountToolCallsForTurn(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("counter"), 1000)

	for i, callID := range []string{"c1", "c2", "c3"} {
		in := makeToolCallInsert(gid, toolID, callID, 5)
		call, _, _ := st.InsertToolCallPending(ctx, in)
		if i == 2 {
			// Cancel the third one — it must NOT count toward the per-turn cap.
			_ = st.CompleteToolCall(ctx, call.ID, ToolCallStatusCancelled, "", "killed", 0, 2000)
		}
	}

	n, err := st.CountToolCallsForTurn(ctx, gid, firstBotID, toolID, 5)
	if err != nil {
		t.Fatalf("CountToolCallsForTurn: %v", err)
	}
	// Two non-cancelled calls for turn 5.
	if n != 2 {
		t.Errorf("want 2 non-cancelled calls, got %d", n)
	}

	// Total across all tools (toolID=0) should also be 2.
	nTotal, err := st.CountToolCallsForTurn(ctx, gid, firstBotID, 0, 5)
	if err != nil || nTotal != 2 {
		t.Errorf("want 2 total calls, got %d (err: %v)", nTotal, err)
	}
}

func TestCancelPendingToolCalls(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("cancel_tool"), 1000)

	for _, cid := range []string{"p1", "p2"} {
		in := makeToolCallInsert(gid, toolID, cid, 1)
		_, _, _ = st.InsertToolCallPending(ctx, in)
	}

	if err := st.CancelPendingToolCalls(ctx, gid, 2000); err != nil {
		t.Fatalf("CancelPendingToolCalls: %v", err)
	}

	// All calls for this group must now be cancelled.
	n, _ := st.CountToolCallsForTurn(ctx, gid, firstBotID, toolID, 1)
	if n != 0 {
		t.Errorf("want 0 non-cancelled after CancelPending, got %d", n)
	}
}

func TestCancelAllPendingToolCalls(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	g1 := makeTestGroup(t, st, 1000)
	g2 := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("global_cancel_tool"), 1000)

	// Insert pending calls across multiple groups
	in1 := makeToolCallInsert(g1, toolID, "g1_call", 1)
	in2 := makeToolCallInsert(g2, toolID, "g2_call", 1)
	_, _, _ = st.InsertToolCallPending(ctx, in1)
	_, _, _ = st.InsertToolCallPending(ctx, in2)

	if err := st.CancelAllPendingToolCalls(ctx, 2000); err != nil {
		t.Fatalf("CancelAllPendingToolCalls: %v", err)
	}

	// Both should be cancelled
	c1, err := st.GetToolCallByCallID(ctx, g1, firstBotID, "g1_call")
	if err != nil || c1.Status != ToolCallStatusCancelled {
		t.Errorf("c1 not cancelled: %v, %+v", err, c1)
	}
	c2, err := st.GetToolCallByCallID(ctx, g2, firstBotID, "g2_call")
	if err != nil || c2.Status != ToolCallStatusCancelled {
		t.Errorf("c2 not cancelled: %v, %+v", err, c2)
	}
}

func TestSweepToolCalls(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	// Keep 5 seconds (very short) for testing.
	SetToolCallRetention(5_000)
	t.Cleanup(func() { SetToolCallRetention(7 * 24 * 60 * 60 * 1000) })

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("sweep_tool"), 1000)

	// Old completed call (completedAt = 1000, nowMs = 1000+6000 = 7000 → expired).
	in1 := makeToolCallInsert(gid, toolID, "old-call", 1)
	call1, created1, err1 := st.InsertToolCallPending(ctx, in1)
	if err1 != nil || !created1 {
		t.Fatalf("insert call1: err=%v, created=%v", err1, created1)
	}
	if err := st.CompleteToolCall(ctx, call1.ID, ToolCallStatusSuccess, "{}", "", 10, 1000); err != nil {
		t.Fatalf("complete call1: %v", err)
	}

	// Recent completed call (completedAt = 6500, nowMs = 7000 → 500ms old, within retention).
	in2 := makeToolCallInsert(gid, toolID, "new-call", 2)
	call2, created2, err2 := st.InsertToolCallPending(ctx, in2)
	if err2 != nil || !created2 {
		t.Fatalf("insert call2: err=%v, created=%v", err2, created2)
	}
	if err := st.CompleteToolCall(ctx, call2.ID, ToolCallStatusSuccess, "{}", "", 10, 6500); err != nil {
		t.Fatalf("complete call2: %v", err)
	}

	// Pending call must NEVER be swept.
	in3 := makeToolCallInsert(gid, toolID, "pending-call", 3)
	if _, _, err := st.InsertToolCallPending(ctx, in3); err != nil {
		t.Fatalf("insert call3: %v", err)
	}

	if err := st.SweepToolCalls(ctx, 7000); err != nil {
		t.Fatalf("SweepToolCalls: %v", err)
	}

	// old-call should be gone.
	if c, err := st.GetToolCallByCallID(ctx, gid, firstBotID, "old-call"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old-call should be swept: got err=%v, c=%+v", err, c)
	}
	// new-call and pending-call must remain.
	if _, err := st.GetToolCallByCallID(ctx, gid, firstBotID, "new-call"); err != nil {
		t.Errorf("new-call should remain: %v", err)
	}
	if _, err := st.GetToolCallByCallID(ctx, gid, firstBotID, "pending-call"); err != nil {
		t.Errorf("pending-call should remain: %v", err)
	}
}

func TestCompleteToolCall_RejectsWhenNotPending(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("cancel_tool"), 1000)

	in := makeToolCallInsert(gid, toolID, "test-cancel", 1)
	call, created, err := st.InsertToolCallPending(ctx, in)
	if err != nil || !created {
		t.Fatalf("insert: err=%v, created=%v", err, created)
	}

	// 1. Cancel the call
	if err := st.CancelPendingToolCalls(ctx, gid, 1500); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// 2. Late completion must be rejected with ErrCallNotPending
	err = st.CompleteToolCall(ctx, call.ID, ToolCallStatusSuccess, `{"ok":true}`, "", 100, 2000)
	if !errors.Is(err, ErrCallNotPending) {
		t.Errorf("want ErrCallNotPending on cancelled call, got %v", err)
	}

	// 3. Status must remain cancelled in DB
	c, err := st.GetToolCallByCallID(ctx, gid, firstBotID, "test-cancel")
	if err != nil {
		t.Fatalf("get call: %v", err)
	}
	if c.Status != ToolCallStatusCancelled {
		t.Errorf("status was overwritten: got %s, want cancelled", c.Status)
	}

	// 4. Calling CompleteToolCall again on terminal state also returns ErrCallNotPending
	err = st.CompleteToolCall(ctx, call.ID, ToolCallStatusSuccess, `{"ok":true}`, "", 100, 2000)
	if !errors.Is(err, ErrCallNotPending) {
		t.Errorf("want ErrCallNotPending on repeated completion, got %v", err)
	}
}

func TestUpdateTool_NewNonceOnReEncryption(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	in := ToolInput{
		Name:           "webhook_nonce_test",
		Description:    "Testing random nonce generation",
		InputSchema:    json.RawMessage(`{}`),
		TimeoutMs:      3000,
		Enabled:        true,
		Kind:           ToolKindHTTPWebhook,
		WebhookURL:     "https://api.example.com/webhook",
		WebhookHeaders: map[string]string{"Authorization": "Bearer token123"},
		WebhookSecret:  "super-secret-signing-key",
	}

	id, err := st.CreateTool(ctx, in, 1000)
	if err != nil {
		t.Fatalf("CreateTool: %v", err)
	}

	t1, err := st.GetTool(ctx, id)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}

	// Re-encrypt the same secret via UpdateTool
	inUpdate := in
	inUpdate.WebhookSecret = "super-secret-signing-key"
	inUpdate.WebhookHeaders = map[string]string{"Authorization": "Bearer token123"}
	if err := st.UpdateTool(ctx, id, inUpdate, 2000); err != nil {
		t.Fatalf("UpdateTool: %v", err)
	}

	t2, err := st.GetTool(ctx, id)
	if err != nil {
		t.Fatalf("GetTool: %v", err)
	}

	// Ciphertexts must differ due to fresh random 12-byte nonce
	if t1.WebhookSecretEnc == t2.WebhookSecretEnc {
		t.Error("expected different ciphertext for WebhookSecretEnc on re-encryption (random nonce)")
	}
	if t1.WebhookHeadersEnc == t2.WebhookHeadersEnc {
		t.Error("expected different ciphertext for WebhookHeadersEnc on re-encryption (random nonce)")
	}

	// Both ciphertexts must successfully decrypt to the original secret
	s1, err := DecryptToolSecret(st.key(), t1.WebhookSecretEnc, id)
	if err != nil || s1 != "super-secret-signing-key" {
		t.Errorf("decrypted s1 mismatch: %v, %s", err, s1)
	}
	s2, err := DecryptToolSecret(st.key(), t2.WebhookSecretEnc, id)
	if err != nil || s2 != "super-secret-signing-key" {
		t.Errorf("decrypted s2 mismatch: %v, %s", err, s2)
	}
}

// ---------------------------------------------------------------------------
// Crypto helpers
// ---------------------------------------------------------------------------

func TestToolAAD_Distinct(t *testing.T) {
	a1 := ToolAAD(1)
	a2 := ToolAAD(2)
	a1b := ToolAAD(1)
	if string(a1) == string(a2) {
		t.Error("ToolAAD(1) must differ from ToolAAD(2)")
	}
	if string(a1) != string(a1b) {
		t.Error("ToolAAD(1) must be deterministic")
	}
}

func TestEncryptDecryptToolSecret_Roundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	const toolID = int64(42)
	const plaintext = `{"Authorization":"Bearer tok"}`

	enc, err := EncryptToolSecret(key, plaintext, toolID)
	if err != nil {
		t.Fatalf("EncryptToolSecret: %v", err)
	}
	if enc == "" {
		t.Fatal("encrypted output must not be empty")
	}

	dec, err := DecryptToolSecret(key, enc, toolID)
	if err != nil {
		t.Fatalf("DecryptToolSecret: %v", err)
	}
	if dec != plaintext {
		t.Errorf("roundtrip mismatch: got %q, want %q", dec, plaintext)
	}
}

func TestEncryptToolSecret_EmptyReturnsEmpty(t *testing.T) {
	key := make([]byte, 32)
	enc, err := EncryptToolSecret(key, "", 1)
	if err != nil {
		t.Fatalf("EncryptToolSecret empty: %v", err)
	}
	if enc != "" {
		t.Errorf("empty input should produce empty output, got %q", enc)
	}
	dec, err := DecryptToolSecret(key, "", 1)
	if err != nil {
		t.Fatalf("DecryptToolSecret empty: %v", err)
	}
	if dec != "" {
		t.Errorf("empty ciphertext should produce empty output, got %q", dec)
	}
}

func TestDecryptToolSecret_WrongToolID(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}
	enc, _ := EncryptToolSecret(key, "secret", 10)

	// Decrypting with a different tool ID must fail (AAD mismatch).
	_, err := DecryptToolSecret(key, enc, 11)
	if err == nil {
		t.Error("expected decryption failure with wrong tool ID (AAD mismatch)")
	}
}

func TestListToolCallsAudit(t *testing.T) {
	st := openTestStoreKeyed(t)
	ctx := context.Background()

	gid := makeTestGroup(t, st, 1000)
	toolID, _ := st.CreateTool(ctx, validToolInput("audit_tool"), 1000)

	for i, cid := range []string{"a1", "a2", "a3"} {
		in := makeToolCallInsert(gid, toolID, cid, int64(i+1))
		_, _, _ = st.InsertToolCallPending(ctx, in)
	}

	calls, err := st.ListToolCallsAudit(ctx, gid, 10)
	if err != nil {
		t.Fatalf("ListToolCallsAudit: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 audit entries, got %d", len(calls))
	}
	// Must be newest first.
	if calls[0].CallID != "a3" {
		t.Errorf("expected a3 first (newest), got %s", calls[0].CallID)
	}

	// Limit capping: <= 0 defaults to 50, > 200 clamped to 200
	callsDefault, err := st.ListToolCallsAudit(ctx, gid, 0)
	if err != nil || len(callsDefault) != 3 {
		t.Errorf("expected 3 entries with default limit, got %d, err: %v", len(callsDefault), err)
	}
	callsCapped, err := st.ListToolCallsAudit(ctx, gid, 500)
	if err != nil || len(callsCapped) != 3 {
		t.Errorf("expected 3 entries with clamped limit, got %d, err: %v", len(callsCapped), err)
	}
}
