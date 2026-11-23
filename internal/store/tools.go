// Package store – tools.go: Tool registry, member-tool assignments, and
// the tool-call invocation log.
//
// Design constraints (single-instance, single SQLite writer):
//   - All mutations run through immediateTx (BEGIN IMMEDIATE).
//   - Network calls MUST NOT be made while a write transaction is open.
//     InsertToolCallPending commits before execution; CompleteToolCall
//     opens a fresh transaction after the call returns.
//   - Secrets (webhook headers JSON, signing key) are AES-256-GCM encrypted
//     at rest with AAD = ToolAAD(toolID), following the same pattern as
//     member agent tokens.
//   - PublicTool is the ONLY tool shape returned on public (room) endpoints;
//     it carries no URLs, secrets, or internal identifiers.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
)

var (
	// ErrCallNotPending is returned by CompleteToolCall when the target tool_call
	// is not in 'pending' status (e.g. was cancelled by Kill Switch or already completed).
	ErrCallNotPending = errors.New("tool call was cancelled or not in pending state")
)

// toolCallRetentionMs is how long completed/terminal tool_call rows are kept.
// Default: 7 days. Override at startup via SetToolCallRetention.
var toolCallRetentionMs atomic.Int64

func init() {
	toolCallRetentionMs.Store(7 * 24 * 60 * 60 * 1000)
}

// SetToolCallRetention sets the tool-call retention period in milliseconds.
// Safe for concurrent use.
func SetToolCallRetention(ms int64) {
	if ms > 0 {
		toolCallRetentionMs.Store(ms)
	}
}

// ToolAAD returns the AES-256-GCM Additional Authenticated Data for a tool's
// encrypted secrets, binding the ciphertext to a specific tool ID so that
// secrets cannot be swapped between tools.
func ToolAAD(toolID int64) []byte {
	return []byte(fmt.Sprintf("botlink:tool:%d", toolID))
}

// EncryptToolSecret encrypts a plaintext secret string with AES-256-GCM,
// using ToolAAD(toolID) as the authenticated data. Returns "" for empty input.
func EncryptToolSecret(key []byte, secret string, toolID int64) (string, error) {
	if secret == "" {
		return "", nil
	}
	return EncryptToken(key, secret, ToolAAD(toolID))
}

// DecryptToolSecret decrypts a tool secret; returns "" for empty ciphertext.
func DecryptToolSecret(key []byte, enc string, toolID int64) (string, error) {
	if enc == "" {
		return "", nil
	}
	return DecryptToken(key, enc, ToolAAD(toolID))
}

// boolInt converts a Go bool to the SQLite integer representation (0 or 1).
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ToolKind classifies a tool's execution backend.
type ToolKind string

const (
	ToolKindBuiltin     ToolKind = "builtin"
	ToolKindHTTPWebhook ToolKind = "http_webhook"
)

// Tool is the full admin view of a registered tool.
// Secret fields (WebhookHeadersEnc, WebhookSecretEnc) are stored encrypted at
// rest and are tagged json:"-" so they are NEVER serialised to JSON responses.
type Tool struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	InputSchema       json.RawMessage `json:"inputSchema"`
	TimeoutMs         int64           `json:"timeoutMs"`
	Enabled           bool            `json:"enabled"`
	Kind              ToolKind        `json:"kind"`
	WebhookURL        string          `json:"webhookUrl,omitempty"`
	WebhookHeadersEnc string          `json:"-"` // AES-256-GCM, AAD = ToolAAD(ID)
	WebhookSecretEnc  string          `json:"-"` // AES-256-GCM, AAD = ToolAAD(ID)
	HasWebhookHeaders bool            `json:"hasWebhookHeaders"`
	HasWebhookSecret  bool            `json:"hasWebhookSecret"`
	CreatedAt         int64           `json:"createdAt"`
	UpdatedAt         int64           `json:"updatedAt"`
}

// PublicTool is the ONLY tool shape exposed on public room endpoints.
// It NEVER includes webhook URLs, encrypted headers, signing secrets,
// or any internal metadata – only what a bot needs to call the tool.
type PublicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	TimeoutMs   int64           `json:"timeoutMs"`
}

// ToolInput carries the payload for creating or updating a tool.
// WebhookHeaders and WebhookSecret arrive as plaintext; the store encrypts
// them before persisting.  On update:
//   - nil WebhookHeaders → keep existing encrypted headers.
//   - "" WebhookSecret   → keep existing encrypted secret.
type ToolInput struct {
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	InputSchema    json.RawMessage   `json:"inputSchema"`
	TimeoutMs      int64             `json:"timeoutMs"`
	Enabled        bool              `json:"enabled"`
	Kind           ToolKind          `json:"kind"`
	WebhookURL     string            `json:"webhookUrl,omitempty"`
	WebhookHeaders map[string]string `json:"webhookHeaders,omitempty"` // nil = no change on update
	WebhookSecret  string            `json:"webhookSecret,omitempty"`  // "" = no change on update
}

// MemberTool is a single tool assignment for one bot inside one group.
type MemberTool struct {
	GroupID         int64  `json:"groupId"`
	BotID           string `json:"botId"`
	ToolID          int64  `json:"toolId"`
	ToolName        string `json:"toolName"`
	Enabled         bool   `json:"enabled"`
	MaxCallsPerTurn int64  `json:"maxCallsPerTurn"`
}

// ToolCallStatus is the lifecycle state of one tool invocation.
type ToolCallStatus string

const (
	ToolCallStatusPending   ToolCallStatus = "pending"
	ToolCallStatusSuccess   ToolCallStatus = "success"
	ToolCallStatusError     ToolCallStatus = "error"
	ToolCallStatusCancelled ToolCallStatus = "cancelled"
	ToolCallStatusTimeout   ToolCallStatus = "timeout"
)

// ToolCall is one recorded tool invocation.
// It is used for idempotency (via callId) and admin audit.
// Arguments and Result are sanitised before storage; the full raw
// content is never logged.
type ToolCall struct {
	ID          int64           `json:"id"`
	GroupID     int64           `json:"groupId"`
	BotID       string          `json:"botId"`
	ToolID      int64           `json:"toolId"`
	ToolName    string          `json:"toolName"` // denormalised via JOIN; empty if tool deleted
	CallID      string          `json:"callId"`
	TurnSeq     int64           `json:"turnSeq"`
	Status      ToolCallStatus  `json:"status"`
	Arguments   json.RawMessage `json:"arguments"`
	Result      string          `json:"result"`
	ErrorMsg    string          `json:"errorMsg,omitempty"`
	DurationMs  int64           `json:"durationMs"`
	CreatedAt   int64           `json:"createdAt"`
	CompletedAt int64           `json:"completedAt,omitempty"`
}

// ToolCallInsert carries the data needed to record a new pending call.
type ToolCallInsert struct {
	GroupID   int64
	BotID     string
	ToolID    int64
	CallID    string // client-supplied idempotency key
	TurnSeq   int64
	Arguments json.RawMessage // already validated JSON
	CreatedAt int64
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// toolNameRE enforces snake_case names: starts with a lowercase letter,
// followed by lowercase letters, digits, or underscores, max 100 chars total.
var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)

func validateToolInput(in *ToolInput) error {
	if !toolNameRE.MatchString(in.Name) {
		return wrapErr(ErrValidation, "tool name must be snake_case, start with a lowercase letter, and be at most 100 characters")
	}
	if in.Kind != ToolKindBuiltin && in.Kind != ToolKindHTTPWebhook {
		return wrapErr(ErrValidation, "tool kind must be 'builtin' or 'http_webhook'")
	}
	if in.Kind == ToolKindHTTPWebhook && strings.TrimSpace(in.WebhookURL) == "" {
		return wrapErr(ErrValidation, "webhook_url is required for http_webhook tools")
	}
	if len(in.WebhookURL) > 2048 {
		return wrapErr(ErrValidation, "webhook_url exceeds 2048 characters")
	}
	if in.TimeoutMs <= 0 {
		in.TimeoutMs = 5000
	}
	if in.TimeoutMs < 500 {
		return wrapErr(ErrValidation, "timeout_ms must be at least 500")
	}
	if in.TimeoutMs > 30_000 {
		return wrapErr(ErrValidation, "timeout_ms must not exceed 30000")
	}
	if len(in.InputSchema) == 0 {
		in.InputSchema = json.RawMessage(`{}`)
	} else {
		var v any
		if err := json.Unmarshal(in.InputSchema, &v); err != nil {
			return wrapErr(ErrValidation, "input_schema must be valid JSON: "+err.Error())
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Private row helpers
// ---------------------------------------------------------------------------

const toolCols = `id, name, description, input_schema, timeout_ms, enabled, kind,
	webhook_url, webhook_headers_enc, webhook_secret_enc, created_at, updated_at`

func scanToolRow(row *sql.Row) (*Tool, error) {
	var t Tool
	var enabled int
	var kind, rawSchema string
	err := row.Scan(&t.ID, &t.Name, &t.Description, &rawSchema, &t.TimeoutMs, &enabled,
		&kind, &t.WebhookURL, &t.WebhookHeadersEnc, &t.WebhookSecretEnc,
		&t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	t.Kind = ToolKind(kind)
	t.HasWebhookHeaders = t.WebhookHeadersEnc != ""
	t.HasWebhookSecret = t.WebhookSecretEnc != ""
	if rawSchema == "" {
		rawSchema = "{}"
	}
	t.InputSchema = json.RawMessage(rawSchema)
	return &t, nil
}

func loadToolByID(ctx context.Context, q querier, id int64) (*Tool, error) {
	return scanToolRow(q.QueryRowContext(ctx,
		"SELECT "+toolCols+" FROM tools WHERE id = ?", id))
}

// toolCallJoin is the base SELECT that joins tool_calls with tools for the
// tool name.  COALESCE guards the case where a tool_call row outlives the
// associated tool (should not happen with CASCADE DELETE, but is defensive).
const toolCallJoin = `SELECT tc.id, tc.group_id, tc.bot_id, tc.tool_id,
	COALESCE(t.name, ''), tc.call_id, tc.turn_seq, tc.status,
	tc.arguments, tc.result, tc.error_msg, tc.duration_ms,
	tc.created_at, tc.completed_at
FROM tool_calls tc LEFT JOIN tools t ON t.id = tc.tool_id`

func scanToolCallRows(rows *sql.Rows) ([]ToolCall, error) {
	var calls []ToolCall
	for rows.Next() {
		var c ToolCall
		var args, status string
		if err := rows.Scan(&c.ID, &c.GroupID, &c.BotID, &c.ToolID, &c.ToolName,
			&c.CallID, &c.TurnSeq, &status, &args, &c.Result, &c.ErrorMsg,
			&c.DurationMs, &c.CreatedAt, &c.CompletedAt); err != nil {
			return nil, err
		}
		c.Status = ToolCallStatus(status)
		if args == "" {
			args = "{}"
		}
		c.Arguments = json.RawMessage(args)
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if calls == nil {
		calls = []ToolCall{}
	}
	return calls, nil
}

func loadToolCallByIDConn(ctx context.Context, conn *sql.Conn, id int64) (*ToolCall, error) {
	rows, err := conn.QueryContext(ctx, toolCallJoin+" WHERE tc.id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls, err := scanToolCallRows(rows)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, ErrNotFound
	}
	return &calls[0], nil
}

func loadToolCallByCallIDConn(ctx context.Context, conn *sql.Conn, groupID int64, botID, callID string) (*ToolCall, error) {
	rows, err := conn.QueryContext(ctx,
		toolCallJoin+" WHERE tc.group_id = ? AND tc.bot_id = ? AND tc.call_id = ?",
		groupID, botID, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls, err := scanToolCallRows(rows)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, ErrNotFound
	}
	return &calls[0], nil
}

// ---------------------------------------------------------------------------
// Tool CRUD
// ---------------------------------------------------------------------------

// CreateTool validates in, inserts the tool, then encrypts any webhook secrets
// using the auto-assigned tool ID as AAD — both steps in one transaction.
// Returns the new tool ID.
func (s *Store) CreateTool(ctx context.Context, in ToolInput, nowMs int64) (int64, error) {
	if err := validateToolInput(&in); err != nil {
		return 0, err
	}
	desc := capText(trimText(in.Description), 500)
	var toolID int64
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// Insert without secrets first to obtain the AUTOINCREMENT id.
		res, err := conn.ExecContext(ctx, `
			INSERT INTO tools (name, description, input_schema, timeout_ms, enabled, kind,
			                   webhook_url, webhook_headers_enc, webhook_secret_enc, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
			in.Name, desc, string(in.InputSchema), in.TimeoutMs,
			boolInt(in.Enabled), string(in.Kind), in.WebhookURL, nowMs, nowMs)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return wrapErr(ErrValidation, "a tool named '"+in.Name+"' already exists")
			}
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		toolID = id
		// Now encrypt secrets with AAD bound to the real tool ID.
		if in.Kind == ToolKindHTTPWebhook {
			headersJSON := ""
			if len(in.WebhookHeaders) > 0 {
				b, merr := json.Marshal(in.WebhookHeaders)
				if merr != nil {
					return fmt.Errorf("marshal webhook headers: %w", merr)
				}
				headersJSON = string(b)
			}
			headersEnc, err := EncryptToolSecret(s.key(), headersJSON, toolID)
			if err != nil {
				return fmt.Errorf("encrypt headers: %w", err)
			}
			secretEnc, err := EncryptToolSecret(s.key(), in.WebhookSecret, toolID)
			if err != nil {
				return fmt.Errorf("encrypt secret: %w", err)
			}
			if _, err := conn.ExecContext(ctx,
				"UPDATE tools SET webhook_headers_enc = ?, webhook_secret_enc = ? WHERE id = ?",
				headersEnc, secretEnc, toolID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return toolID, nil
}

// UpdateTool updates a tool's metadata and optionally its encrypted secrets.
//   - nil WebhookHeaders → keep existing encrypted header blob.
//   - "" WebhookSecret   → keep existing signing secret.
//   - Switching kind to builtin → both secret fields are cleared.
func (s *Store) UpdateTool(ctx context.Context, id int64, in ToolInput, nowMs int64) error {
	if err := validateToolInput(&in); err != nil {
		return err
	}
	desc := capText(trimText(in.Description), 500)
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// Fetch existing encrypted values so we can selectively preserve them.
		var existingHeadersEnc, existingSecretEnc string
		qErr := conn.QueryRowContext(ctx,
			"SELECT webhook_headers_enc, webhook_secret_enc FROM tools WHERE id = ?", id).
			Scan(&existingHeadersEnc, &existingSecretEnc)
		if qErr == sql.ErrNoRows {
			return ErrNotFound
		}
		if qErr != nil {
			return qErr
		}
		headersEnc := existingHeadersEnc
		secretEnc := existingSecretEnc
		if in.Kind == ToolKindHTTPWebhook {
			if in.WebhookHeaders != nil { // nil pointer means "don't touch existing"
				b, merr := json.Marshal(in.WebhookHeaders)
				if merr != nil {
					return fmt.Errorf("marshal webhook headers: %w", merr)
				}
				var encErr error
				headersEnc, encErr = EncryptToolSecret(s.key(), string(b), id)
				if encErr != nil {
					return fmt.Errorf("encrypt headers: %w", encErr)
				}
			}
			if in.WebhookSecret != "" { // "" means "don't touch existing"
				var encErr error
				secretEnc, encErr = EncryptToolSecret(s.key(), in.WebhookSecret, id)
				if encErr != nil {
					return fmt.Errorf("encrypt secret: %w", encErr)
				}
			}
		} else {
			// Switching to builtin: wipe all secrets.
			headersEnc = ""
			secretEnc = ""
		}
		_, err := conn.ExecContext(ctx, `
			UPDATE tools SET name = ?, description = ?, input_schema = ?, timeout_ms = ?,
			enabled = ?, kind = ?, webhook_url = ?,
			webhook_headers_enc = ?, webhook_secret_enc = ?, updated_at = ?
			WHERE id = ?`,
			in.Name, desc, string(in.InputSchema), in.TimeoutMs,
			boolInt(in.Enabled), string(in.Kind), in.WebhookURL,
			headersEnc, secretEnc, nowMs, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return wrapErr(ErrValidation, "a tool named '"+in.Name+"' already exists")
			}
			return err
		}
		return nil
	})
}

// EnableTool toggles a tool's enabled state without altering any other field.
func (s *Store) EnableTool(ctx context.Context, id int64, enabled bool, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			"UPDATE tools SET enabled = ?, updated_at = ? WHERE id = ?",
			boolInt(enabled), nowMs, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteTool removes a tool and its cascaded member_tools and tool_calls rows.
func (s *Store) DeleteTool(ctx context.Context, id int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, "DELETE FROM tools WHERE id = ?", id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetTool returns the full admin view of one tool, or ErrNotFound.
func (s *Store) GetTool(ctx context.Context, id int64) (*Tool, error) {
	return loadToolByID(ctx, s.ro, id)
}

// GetToolByName returns a tool by its unique name, or ErrNotFound.
func (s *Store) GetToolByName(ctx context.Context, name string) (*Tool, error) {
	return scanToolRow(s.ro.QueryRowContext(ctx, "SELECT "+toolCols+" FROM tools WHERE name = ?", name))
}

// ListTools returns all tools ordered by name.
// The returned Tool structs include WebhookHeadersEnc / WebhookSecretEnc (still
// encrypted); callers that need plaintext must decrypt via DecryptToolSecret.
func (s *Store) ListTools(ctx context.Context) ([]Tool, error) {
	rows, err := s.ro.QueryContext(ctx,
		"SELECT "+toolCols+" FROM tools ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []Tool
	for rows.Next() {
		var t Tool
		var enabled int
		var kind, rawSchema string
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &rawSchema, &t.TimeoutMs, &enabled,
			&kind, &t.WebhookURL, &t.WebhookHeadersEnc, &t.WebhookSecretEnc,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled != 0
		t.Kind = ToolKind(kind)
		t.HasWebhookHeaders = t.WebhookHeadersEnc != ""
		t.HasWebhookSecret = t.WebhookSecretEnc != ""
		if rawSchema == "" {
			rawSchema = "{}"
		}
		t.InputSchema = json.RawMessage(rawSchema)
		tools = append(tools, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []Tool{}
	}
	return tools, nil
}

// ---------------------------------------------------------------------------
// Member-tool assignments
// ---------------------------------------------------------------------------

// SetMemberTool upserts a tool assignment for a bot in a group.
// maxCallsPerTurn is clamped to [1, 20].
func (s *Store) SetMemberTool(ctx context.Context, groupID int64, botID string, toolID int64, enabled bool, maxCallsPerTurn int64) error {
	if maxCallsPerTurn < 1 {
		maxCallsPerTurn = 1
	}
	if maxCallsPerTurn > 20 {
		maxCallsPerTurn = 20
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// Verify member exists.
		var memberExists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM bot_members WHERE group_id = ? AND bot_id = ?",
			groupID, botID).Scan(&memberExists); err != nil {
			return err
		}
		if memberExists == 0 {
			return wrapErr(ErrNotFound, "member not found")
		}
		// Verify tool exists.
		var toolExists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tools WHERE id = ?", toolID).Scan(&toolExists); err != nil {
			return err
		}
		if toolExists == 0 {
			return wrapErr(ErrNotFound, "tool not found")
		}
		_, err := conn.ExecContext(ctx, `
			INSERT INTO member_tools (group_id, bot_id, tool_id, enabled, max_calls_per_turn)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (group_id, bot_id, tool_id) DO UPDATE SET
				enabled            = excluded.enabled,
				max_calls_per_turn = excluded.max_calls_per_turn`,
			groupID, botID, toolID, boolInt(enabled), maxCallsPerTurn)
		return err
	})
}

// RemoveMemberTool removes a tool assignment from a bot.
func (s *Store) RemoveMemberTool(ctx context.Context, groupID int64, botID string, toolID int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			"DELETE FROM member_tools WHERE group_id = ? AND bot_id = ? AND tool_id = ?",
			groupID, botID, toolID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListMemberToolsForBot returns all tool assignments for a specific bot.
func (s *Store) ListMemberToolsForBot(ctx context.Context, groupID int64, botID string) ([]MemberTool, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT mt.group_id, mt.bot_id, mt.tool_id, t.name, mt.enabled, mt.max_calls_per_turn
		FROM member_tools mt JOIN tools t ON t.id = mt.tool_id
		WHERE mt.group_id = ? AND mt.bot_id = ?
		ORDER BY t.name`,
		groupID, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemberToolRows(rows)
}

// ListAllMemberToolsForGroup returns all tool assignments in a group (all bots).
func (s *Store) ListAllMemberToolsForGroup(ctx context.Context, groupID int64) ([]MemberTool, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT mt.group_id, mt.bot_id, mt.tool_id, t.name, mt.enabled, mt.max_calls_per_turn
		FROM member_tools mt JOIN tools t ON t.id = mt.tool_id
		WHERE mt.group_id = ?
		ORDER BY mt.bot_id, t.name`,
		groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemberToolRows(rows)
}

func scanMemberToolRows(rows *sql.Rows) ([]MemberTool, error) {
	var out []MemberTool
	for rows.Next() {
		var mt MemberTool
		var enabled int
		if err := rows.Scan(&mt.GroupID, &mt.BotID, &mt.ToolID, &mt.ToolName,
			&enabled, &mt.MaxCallsPerTurn); err != nil {
			return nil, err
		}
		mt.Enabled = enabled != 0
		out = append(out, mt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []MemberTool{}
	}
	return out, nil
}

// GetToolsForMember returns the bot-safe tool list: only tools that are
// enabled both at the global (tools.enabled) and assignment (member_tools.enabled)
// level. This is the ONLY shape returned on public endpoints — no URLs or secrets.
func (s *Store) GetToolsForMember(ctx context.Context, groupID int64, botID string) ([]PublicTool, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT t.name, t.description, t.input_schema, t.timeout_ms
		FROM member_tools mt JOIN tools t ON t.id = mt.tool_id
		WHERE mt.group_id = ? AND mt.bot_id = ?
		  AND mt.enabled = 1 AND t.enabled = 1
		ORDER BY t.name`,
		groupID, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicTool
	for rows.Next() {
		var pt PublicTool
		var rawSchema string
		if err := rows.Scan(&pt.Name, &pt.Description, &rawSchema, &pt.TimeoutMs); err != nil {
			return nil, err
		}
		if rawSchema == "" {
			rawSchema = "{}"
		}
		pt.InputSchema = json.RawMessage(rawSchema)
		out = append(out, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []PublicTool{}
	}
	return out, nil
}

// CheckMemberToolEnabled returns (enabled, maxCallsPerTurn) for a specific
// assignment. Returns (false, 0, nil) when no assignment row exists.
// Both the global tool flag and the assignment flag must be true for the
// returned enabled value to be true.
func (s *Store) CheckMemberToolEnabled(ctx context.Context, groupID int64, botID string, toolID int64) (bool, int64, error) {
	var assignEnabled, toolEnabled int
	var maxCalls int64
	err := s.ro.QueryRowContext(ctx, `
		SELECT mt.enabled, t.enabled, mt.max_calls_per_turn
		FROM member_tools mt JOIN tools t ON t.id = mt.tool_id
		WHERE mt.group_id = ? AND mt.bot_id = ? AND mt.tool_id = ?`,
		groupID, botID, toolID).Scan(&assignEnabled, &toolEnabled, &maxCalls)
	if err == sql.ErrNoRows {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return assignEnabled != 0 && toolEnabled != 0, maxCalls, nil
}

// ---------------------------------------------------------------------------
// Tool-call log (invocation record + idempotency)
// ---------------------------------------------------------------------------

// CountToolCallsForTurn counts non-cancelled tool_calls submitted by one bot
// in a single turn. If toolID > 0, it counts calls specifically for that tool
// (matching member_tools.max_calls_per_turn); if toolID <= 0, it counts all tool calls.
// Cancelled calls are excluded so a bot can retry after a Kill Switch.
func (s *Store) CountToolCallsForTurn(ctx context.Context, groupID int64, botID string, toolID int64, turnSeq int64) (int64, error) {
	var n int64
	var err error
	if toolID > 0 {
		err = s.ro.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tool_calls
			WHERE group_id = ? AND bot_id = ? AND tool_id = ? AND turn_seq = ? AND status != 'cancelled'`,
			groupID, botID, toolID, turnSeq).Scan(&n)
	} else {
		err = s.ro.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tool_calls
			WHERE group_id = ? AND bot_id = ? AND turn_seq = ? AND status != 'cancelled'`,
			groupID, botID, turnSeq).Scan(&n)
	}
	return n, err
}

// InsertToolCallPending records a new pending invocation.
//
// InsertToolCallPending inserts a new tool call in "pending" status, or
// returns the existing record if this (groupID, botID, callID) was already
// inserted.
//
// Return values:
//   - created == true:  Fresh insert (first reservation). The call is in "pending" state.
//   - created == false: Duplicate callID. The caller should inspect call.Status:
//     if pending -> 409 Conflict; if terminal -> return cached result.
//
// IMPORTANT: This method commits the INSERT before any network call.
// The caller must NOT hold any write transaction while executing the tool.
func (s *Store) InsertToolCallPending(ctx context.Context, in ToolCallInsert) (*ToolCall, bool, error) {
	args := string(in.Arguments)
	if args == "" {
		args = "{}"
	}
	var result *ToolCall
	var created bool
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `
			INSERT INTO tool_calls
			  (group_id, bot_id, tool_id, call_id, turn_seq, status, arguments, created_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
			ON CONFLICT (group_id, bot_id, call_id) DO NOTHING`,
			in.GroupID, in.BotID, in.ToolID, in.CallID, in.TurnSeq, args, in.CreatedAt)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// Duplicate: return the existing record for idempotency inspection.
			created = false
			tc, err := loadToolCallByCallIDConn(ctx, conn, in.GroupID, in.BotID, in.CallID)
			if err != nil {
				return err
			}
			result = tc
			return nil
		}
		// Fresh insert: load the full row (with tool name via JOIN).
		created = true
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		tc, err := loadToolCallByIDConn(ctx, conn, id)
		if err != nil {
			return err
		}
		result = tc
		return nil
	})
	return result, created, err
}

// CompleteToolCall updates a pending call to its terminal state.
// The WHERE clause includes `status = 'pending'` so a raced concurrent
// cancellation (Kill Switch) cannot be overwritten. If 0 rows were affected,
// ErrCallNotPending is returned so the caller knows the call was cancelled or
// already completed.
//
// result must already be sanitised by the caller (Phase 3).
func (s *Store) CompleteToolCall(ctx context.Context, id int64, status ToolCallStatus, result, errorMsg string, durationMs, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `
			UPDATE tool_calls
			SET status = ?, result = ?, error_msg = ?, duration_ms = ?, completed_at = ?
			WHERE id = ? AND status = 'pending'`,
			string(status), result, errorMsg, durationMs, nowMs, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrCallNotPending
		}
		return nil
	})
}

// CancelPendingToolCalls marks all pending tool_calls for a group as
// "cancelled".  Called on Kill Switch, pause, and stop to ensure running
// tool executions don't commit stale results.
func (s *Store) CancelPendingToolCalls(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `
			UPDATE tool_calls
			SET status = 'cancelled', completed_at = ?
			WHERE group_id = ? AND status = 'pending'`,
			nowMs, groupID)
		return err
	})
}

// CancelAllPendingToolCalls marks all pending tool_calls across all groups as
// "cancelled". Called on server startup to clean up orphan calls left behind
// by a previous crash or abrupt shutdown.
func (s *Store) CancelAllPendingToolCalls(ctx context.Context, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `
			UPDATE tool_calls
			SET status = 'cancelled', completed_at = ?
			WHERE status = 'pending'`,
			nowMs)
		return err
	})
}

// GetToolCallByCallID fetches a call by its client-supplied idempotency key.
func (s *Store) GetToolCallByCallID(ctx context.Context, groupID int64, botID, callID string) (*ToolCall, error) {
	rows, err := s.ro.QueryContext(ctx,
		toolCallJoin+" WHERE tc.group_id = ? AND tc.bot_id = ? AND tc.call_id = ?",
		groupID, botID, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls, err := scanToolCallRows(rows)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, ErrNotFound
	}
	return &calls[0], nil
}

// ListToolCallsAudit returns the most recent tool_calls for a group,
// newest first, for the admin audit view. Limit is clamped to [1, 200]
// (defaulting to 50 when <= 0).
func (s *Store) ListToolCallsAudit(ctx context.Context, groupID int64, limit int64) ([]ToolCall, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	rows, err := s.ro.QueryContext(ctx,
		toolCallJoin+" WHERE tc.group_id = ? ORDER BY tc.id DESC LIMIT ?",
		groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanToolCallRows(rows)
}

// SweepToolCalls deletes terminal tool_call rows older than the retention
// window. Only rows with a non-zero completed_at and a non-pending status
// are removed. Pending rows are never pruned automatically.
// Called from the existing turn sweeper in server.go (Phase 3 integration).
func (s *Store) SweepToolCalls(ctx context.Context, nowMs int64) error {
	ret := toolCallRetentionMs.Load()
	if ret <= 0 {
		return nil
	}
	cutoff := nowMs - ret
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `
			DELETE FROM tool_calls
			WHERE status != 'pending' AND completed_at > 0 AND completed_at < ?`,
			cutoff)
		return err
	})
}

// DecryptToolSecret decrypts a tool secret using the store's configured encryption key.
func (s *Store) DecryptToolSecret(enc string, toolID int64) (string, error) {
	return DecryptToolSecret(s.key(), enc, toolID)
}

// ValidateBotTurnForTool checks whether the given token corresponds to an active member
// in a running group whose turn it currently is (matching expectedTurnSeq).
// It returns (groupID, botID, nil) if valid, or a descriptive error.
func (s *Store) ValidateBotTurnForTool(ctx context.Context, token string, expectedTurnSeq int64, nowMs int64) (int64, string, error) {
	m, err := loadMemberByToken(ctx, s.ro, token, s.key())
	if err != nil || m == nil {
		return 0, "", ErrNotFound
	}
	s.touchPresence(*m, nowMs)
	g, err := loadGroup(ctx, s.ro, m.GroupID)
	if err != nil {
		return 0, "", err
	}
	if g.Status != "running" {
		return 0, "", wrapErr(ErrValidation, "group is not running (status: "+g.Status+")")
	}
	if g.TurnSeq != expectedTurnSeq {
		return 0, "", wrapErr(ErrValidation, "stale turn sequence")
	}
	if g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != m.BotID {
		return 0, "", wrapErr(ErrValidation, "it is not your turn")
	}
	return m.GroupID, m.BotID, nil
}

// IsGroupRunning checks if a group currently has status == 'running'.
func (s *Store) IsGroupRunning(ctx context.Context, groupID int64) (bool, error) {
	var status string
	err := s.ro.QueryRowContext(ctx, "SELECT status FROM groups WHERE id = ?", groupID).Scan(&status)
	if err == sql.ErrNoRows {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return status == "running", nil
}
