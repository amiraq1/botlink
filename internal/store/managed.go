// Package store – managed.go: LLM connections, managed agents and agent runs.
//
// Invariants (single-instance, single SQLite writer):
//   - API keys live only in llm_connections.api_key_enc (AES-256-GCM, AAD
//     bound to the connection ID). They are never stored in plaintext, never
//     logged, and never serialised into a JSON response: LLMConnection has no
//     key field at all.
//   - The provider origin is fixed in code per provider. No admin-supplied
//     base_url is stored or accepted in this version.
//   - A managed agent is a real bot_members row with NO room token
//     (token_hash = token_enc = ”), so it counts toward the 2–10 member limit
//     and takes part in the turn engine without creating an unused public
//     access link. The server-side runner is its only voice.
//   - A turn assignment is identified by a monotonically increasing turn_epoch
//     on the group. The epoch is stable across pause/resume, server restarts
//     and explicit retries, and changes on every new assignment (reply
//     advance, skip rotation back to a bot, leader assignment, reassignment
//     after a member leaves). turn_started_at is only a timeout clock.
//   - At most one agent_run is open (queued|running) per assignment.
//   - A turn instance that already has ANY run (open or terminal) is never
//     re-run automatically. A retry is an explicit admin action that appends a
//     new attempt with retry_of pointing at the previous run.
//   - The assistant message, the group advance and the run's terminal status
//     are committed in one BEGIN IMMEDIATE transaction. A stored message can
//     therefore never coexist with an open run, and a crash before COMMIT
//     leaves neither.
//   - A monetary cap is check-at-start accounting, never an invoice
//     guarantee. With a budget set, no new run starts while any recorded cost
//     in the rolling 24h window is missing (queued, running, cancelled,
//     interrupted, no price, provider omitted usage) or when recorded spend
//     already reached the budget. Costs are frozen when the run finishes;
//     later price edits never reprice old runs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// ProviderDeepSeek is the only provider supported in this version.
	ProviderDeepSeek = "deepseek"

	DefaultMaxOutputTokens    = int64(512)
	DefaultContextMaxMessages = int64(20)
	DefaultContextMaxChars    = int64(12000)
	DefaultDailyRunLimit      = int64(50)

	// Agent run statuses. Everything except queued/running is terminal.
	AgentRunQueued      = "queued"
	AgentRunRunning     = "running"
	AgentRunCompleted   = "completed"
	AgentRunFailed      = "failed"
	AgentRunInterrupted = "interrupted"
	AgentRunCancelled   = "cancelled"
	AgentRunDiscarded   = "discarded"

	agentRunWindowMs = int64(24 * 60 * 60 * 1000)
	maxPersonaRunes  = 2000
	maxRunReplyRunes = 8000
)

// modelNameRE accepts the shape of provider model identifiers ("deepseek-v4-pro",
// "deepseek-reasoner", "gpt-4o-mini"). Anything with spaces, slashes or shell
// characters is rejected before it can reach a request body.
var modelNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

var errRunStale = errors.New("run is stale")

// ConnectionAAD binds a connection's encrypted API key to its row ID, so a
// ciphertext cannot be moved to another connection.
func ConnectionAAD(id int64) []byte {
	return []byte(fmt.Sprintf("botlink:llm-connection:%d", id))
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// LLMConnection is the admin-facing view of a provider connection. It carries
// no secret material: only the last four characters of the key for display.
type LLMConnection struct {
	ID              int64  `json:"id"`
	Provider        string `json:"provider"`
	Label           string `json:"label"`
	ModelHint       string `json:"modelHint"`
	KeyLast4        string `json:"keyLast4"`
	HasKey          bool   `json:"hasKey"`
	LastCheckedAt   int64  `json:"lastCheckedAt"`
	LastCheckStatus string `json:"lastCheckStatus"` // "" | "ok" | "error"
	LastCheckError  string `json:"lastCheckError"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
}

// LLMConnectionInput carries plaintext key material into the store, which
// encrypts it before persisting. On update an empty APIKey keeps the stored key.
type LLMConnectionInput struct {
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	APIKey    string `json:"apiKey"`
	ModelHint string `json:"modelHint"`
}

// ModelPrice is the published price of one model on one connection, expressed
// in micro-units of Currency per 1,000,000 tokens. Prices hang off the model,
// not the connection alone, because two models on one account can differ.
type ModelPrice struct {
	ConnectionID      int64  `json:"connectionId"`
	Model             string `json:"model"`
	InputMicrosPer1M  int64  `json:"inputMicrosPer1M"`
	OutputMicrosPer1M int64  `json:"outputMicrosPer1M"`
	Currency          string `json:"currency"`
	UpdatedAt         int64  `json:"updatedAt"`
}

// ManagedAgent ties one bot_members row to one LLM connection, model, persona
// and a set of limits.
type ManagedAgent struct {
	ID                 int64  `json:"id"`
	GroupID            int64  `json:"groupId"`
	BotID              string `json:"botId"`
	BotName            string `json:"botName"`
	RoleTitle          string `json:"roleTitle"`
	ConnectionID       int64  `json:"connectionId"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Persona            string `json:"persona"`
	MaxOutputTokens    int64  `json:"maxOutputTokens"`
	ContextMaxMessages int64  `json:"contextMaxMessages"`
	ContextMaxChars    int64  `json:"contextMaxChars"`
	DailyRunLimit      int64  `json:"dailyRunLimit"`
	DailyBudgetMicros  *int64 `json:"dailyBudgetMicros"`
	Enabled            bool   `json:"enabled"`
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
}

// ManagedAgentInput is the editable part of a managed agent.
type ManagedAgentInput struct {
	ConnectionID       int64  `json:"connectionId"`
	Model              string `json:"model"`
	Persona            string `json:"persona"`
	MaxOutputTokens    int64  `json:"maxOutputTokens"`
	ContextMaxMessages int64  `json:"contextMaxMessages"`
	ContextMaxChars    int64  `json:"contextMaxChars"`
	DailyRunLimit      int64  `json:"dailyRunLimit"`
	DailyBudgetMicros  *int64 `json:"dailyBudgetMicros"`
	Enabled            bool   `json:"enabled"`
}

// AgentRun is one attempt to produce one assistant reply for one turn
// assignment. Attempts of the same assignment share (group, bot, turn_epoch)
// and are chained through RetryOf so the whole history stays readable.
// TurnStartedAt records the clock when the attempt row was created and is not
// part of the identity. A skip cycle back to the same bot creates a new epoch,
// so its old run can neither block nor be accepted as the new assignment.
type AgentRun struct {
	ID               int64  `json:"id"`
	GroupID          int64  `json:"groupId"`
	BotID            string `json:"botId"`
	AgentID          *int64 `json:"agentId"`
	TurnSeq          int64  `json:"turnSeq"`
	TurnEpoch        int64  `json:"turnEpoch"`
	TurnStartedAt    int64  `json:"turnStartedAt"`
	Attempt          int64  `json:"attempt"`
	RetryOf          *int64 `json:"retryOf"`
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	RequestMsgCount  int64  `json:"requestMsgCount"`
	RequestChars     int64  `json:"requestChars"`
	PromptTokens     *int64 `json:"promptTokens"`
	CompletionTokens *int64 `json:"completionTokens"`
	TotalTokens      *int64 `json:"totalTokens"`
	EstCostMicros    *int64 `json:"estCostMicros"`
	CostMicros       *int64 `json:"costMicros"`
	CostBasis        string `json:"costBasis"` // none | estimate | usage
	// CostConfirmed is true only when the provider itself reported the cost.
	// A cost derived from provider token usage and a stored price is real
	// accounting input, but it is still computed locally, not confirmed.
	CostConfirmed bool   `json:"costConfirmed"`
	ResponseChars int64  `json:"responseChars"`
	MessageID     *int64 `json:"messageId"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMsg      string `json:"errorMsg,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	StartedAt     int64  `json:"startedAt"`
	FinishedAt    int64  `json:"finishedAt"`
}

// AgentRunStart is supplied by the runner once it has assembled the request.
type AgentRunStart struct {
	RequestMsgCount int64  `json:"requestMsgCount"`
	RequestChars    int64  `json:"requestChars"`
	EstCostMicros   *int64 `json:"estCostMicros"`
}

// AgentUsage is the provider's reported usage for a finished call. Every field
// is optional: when the provider omits usage, cost stays "unavailable" instead
// of being guessed. CostMicros is only set when the provider reported the
// charge itself (confirmed cost); token counts alone are computed locally
// against the stored price.
type AgentUsage struct {
	PromptTokens     *int64
	CompletionTokens *int64
	TotalTokens      *int64
	CostMicros       *int64
	CostBasis        string // "usage" when CostMicros is set
}

// Budget states reported with ManagedAgentUsage.
const (
	BudgetUnset     = "unset"
	BudgetOK        = "ok"
	BudgetExhausted = "exhausted"
	BudgetUnknown   = "unknown"

	BudgetReasonReached = "budget_reached"
	BudgetReasonUnknown = "cost_unknown"
)

// ManagedAgentUsage summarises the rolling 24h window (created_at >= now-24h)
// used by the run cap and the optional cost cap. Every attempt counts toward
// RunsLast24h, including failed, cancelled and discarded ones, so the cap
// cannot be sidestepped by retrying or pausing.
type ManagedAgentUsage struct {
	RunsLast24h       int64  `json:"runsLast24h"`
	DailyRunLimit     int64  `json:"dailyRunLimit"`
	SpentMicros       int64  `json:"spentMicros"`
	ConfirmedMicros   int64  `json:"confirmedMicros"`
	ComputedMicros    int64  `json:"computedMicros"`
	UnknownRuns       int64  `json:"unknownRuns"`
	DailyBudgetMicros *int64 `json:"dailyBudgetMicros"`
	// CostKnown is false when at least one run in the window has no recorded
	// cost: the provider omitted usage, the model had no stored price, or a
	// cancelled/interrupted request may still have been billed. In that case
	// SpentMicros is a lower bound, not the real spend.
	CostKnown bool `json:"costKnown"`
	// BudgetState is unset | ok | exhausted | unknown. With a budget set and
	// CostKnown=false the state is unknown and new runs are refused: the cap
	// cannot be checked, so the safe action is not to spend more.
	BudgetState       string `json:"budgetState"`
	BudgetBlocked     bool   `json:"budgetBlocked"`
	BudgetBlockReason string `json:"budgetBlockReason"` // "" | budget_reached | cost_unknown
}

type scanner interface {
	Scan(dest ...any) error
}

// ---------------------------------------------------------------------------
// LLM connections
// ---------------------------------------------------------------------------

const connectionCols = `id, provider, label, api_key_enc, api_key_last4, model_hint,
	last_checked_at, last_check_status, last_check_error, created_at, updated_at`

func scanConnection(sc scanner) (*LLMConnection, string, error) {
	var c LLMConnection
	var enc string
	err := sc.Scan(&c.ID, &c.Provider, &c.Label, &enc, &c.KeyLast4, &c.ModelHint,
		&c.LastCheckedAt, &c.LastCheckStatus, &c.LastCheckError, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	c.HasKey = enc != ""
	return &c, enc, nil
}

func loadConnection(ctx context.Context, q querier, id int64) (*LLMConnection, string, error) {
	return scanConnection(q.QueryRowContext(ctx,
		"SELECT "+connectionCols+" FROM llm_connections WHERE id = ?", id))
}

// validAPIKey accepts a trimmed 16–300 byte key made only of printable,
// non-space ASCII. DeepSeek keys ("sk-…") fit this shape; anything with
// whitespace or control characters is a paste error, not a key.
func validAPIKey(k string) bool {
	if len(k) < 16 || len(k) > 300 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 33 || k[i] > 126 {
			return false
		}
	}
	return true
}

func keyLast4(k string) string {
	if len(k) <= 4 {
		return ""
	}
	return k[len(k)-4:]
}

// CreateConnection stores a provider connection with its key encrypted at rest.
func (s *Store) CreateConnection(ctx context.Context, in LLMConnectionInput, nowMs int64) (*LLMConnection, error) {
	provider := strings.ToLower(trimText(in.Provider))
	if provider != ProviderDeepSeek {
		return nil, wrapErr(ErrValidation, "unsupported provider: only deepseek is available in this version")
	}
	key := strings.TrimSpace(in.APIKey)
	if !validAPIKey(key) {
		return nil, wrapErr(ErrValidation, "API key must be 16-300 printable characters without spaces")
	}
	label := capText(trimText(Sanitize(in.Label)), 80)
	if label == "" {
		label = "DeepSeek"
	}
	hint := trimText(Sanitize(in.ModelHint))
	if hint != "" && !modelNameRE.MatchString(hint) {
		return nil, wrapErr(ErrValidation, "model hint is not a valid model name")
	}
	var id int64
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `
			INSERT INTO llm_connections
			  (provider, label, api_key_enc, api_key_last4, model_hint, created_at, updated_at)
			VALUES (?, ?, '', ?, ?, ?, ?)`,
			provider, label, keyLast4(key), hint, nowMs, nowMs)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return wrapErr(ErrValidation, "a "+provider+" connection already exists")
			}
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		enc, err := EncryptToken(s.key(), key, ConnectionAAD(id))
		if err != nil {
			return fmt.Errorf("encrypt API key: %w", err)
		}
		if _, err := conn.ExecContext(ctx,
			"UPDATE llm_connections SET api_key_enc = ? WHERE id = ?", enc, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c, _, err := loadConnection(ctx, s.ro, id)
	return c, err
}

// UpdateConnection edits a connection. An empty APIKey keeps the stored key.
func (s *Store) UpdateConnection(ctx context.Context, id int64, in LLMConnectionInput, nowMs int64) error {
	provider := strings.ToLower(trimText(in.Provider))
	if provider != ProviderDeepSeek {
		return wrapErr(ErrValidation, "unsupported provider: only deepseek is available in this version")
	}
	label := capText(trimText(Sanitize(in.Label)), 80)
	if label == "" {
		label = "DeepSeek"
	}
	hint := trimText(Sanitize(in.ModelHint))
	if hint != "" && !modelNameRE.MatchString(hint) {
		return wrapErr(ErrValidation, "model hint is not a valid model name")
	}
	key := strings.TrimSpace(in.APIKey)
	if key != "" && !validAPIKey(key) {
		return wrapErr(ErrValidation, "API key must be 16-300 printable characters without spaces")
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		current, enc, err := loadConnection(ctx, conn, id)
		if err != nil {
			return err
		}
		// A metadata-only edit must not erase the displayed key suffix.
		last4 := current.KeyLast4
		if key != "" {
			enc, err = EncryptToken(s.key(), key, ConnectionAAD(id))
			if err != nil {
				return fmt.Errorf("encrypt API key: %w", err)
			}
			last4 = keyLast4(key)
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE llm_connections
			SET provider = ?, label = ?, api_key_enc = ?, api_key_last4 = ?, model_hint = ?, updated_at = ?
			WHERE id = ?`, provider, label, enc, last4, hint, nowMs, id); err != nil {
			return err
		}
		return nil
	})
}

// DeleteConnection removes a connection that no managed agent references.
func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var users int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM managed_agents WHERE connection_id = ?", id).Scan(&users); err != nil {
			return err
		}
		if users > 0 {
			return wrapErr(ErrValidation,
				fmt.Sprintf("%d managed agent(s) still use this connection", users))
		}
		res, err := conn.ExecContext(ctx, "DELETE FROM llm_connections WHERE id = ?", id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetConnectionCheck records the outcome of a model-list probe. status is
// "ok" or "error"; errMsg must already be free of secret material (it is
// sanitised and capped here as a second line of defence).
func (s *Store) SetConnectionCheck(ctx context.Context, id int64, status, errMsg string, nowMs int64) error {
	if status != "ok" && status != "error" {
		return wrapErr(ErrValidation, "check status must be 'ok' or 'error'")
	}
	msg := ""
	if status == "error" {
		msg = capText(trimText(Sanitize(errMsg)), 300)
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `
			UPDATE llm_connections
			SET last_checked_at = ?, last_check_status = ?, last_check_error = ?, updated_at = ?
			WHERE id = ?`, nowMs, status, msg, nowMs, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetConnection returns the sanitised connection view.
func (s *Store) GetConnection(ctx context.Context, id int64) (*LLMConnection, error) {
	c, _, err := loadConnection(ctx, s.ro, id)
	return c, err
}

// ListConnections returns every connection, newest first, without key material.
func (s *Store) ListConnections(ctx context.Context) ([]LLMConnection, error) {
	rows, err := s.ro.QueryContext(ctx,
		"SELECT "+connectionCols+" FROM llm_connections ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMConnection
	for rows.Next() {
		c, _, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []LLMConnection{}
	}
	return out, nil
}

// DecryptConnectionKey returns the plaintext API key for the provider client.
// It must never be logged or returned over HTTP.
func (s *Store) DecryptConnectionKey(ctx context.Context, id int64) (string, error) {
	_, enc, err := loadConnection(ctx, s.ro, id)
	if err != nil {
		return "", err
	}
	if enc == "" {
		return "", ErrNotFound
	}
	return DecryptToken(s.key(), enc, ConnectionAAD(id))
}

// ---------------------------------------------------------------------------
// Model prices
// ---------------------------------------------------------------------------

// SetModelPrice upserts the price of one model on one connection.
func (s *Store) SetModelPrice(ctx context.Context, p ModelPrice, nowMs int64) error {
	model := trimText(p.Model)
	if !modelNameRE.MatchString(model) {
		return wrapErr(ErrValidation, "model name is invalid")
	}
	if p.InputMicrosPer1M < 0 || p.OutputMicrosPer1M < 0 {
		return wrapErr(ErrValidation, "prices must not be negative")
	}
	currency := strings.ToUpper(trimText(p.Currency))
	if currency == "" {
		currency = "USD"
	}
	if currency != "USD" {
		return wrapErr(ErrValidation, "only USD prices are supported")
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var exists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM llm_connections WHERE id = ?", p.ConnectionID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return wrapErr(ErrNotFound, "LLM connection not found")
		}
		_, err := conn.ExecContext(ctx, `
			INSERT INTO llm_model_prices
			  (connection_id, model, input_micros_per_1m, output_micros_per_1m, currency, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (connection_id, model) DO UPDATE SET
			  input_micros_per_1m  = excluded.input_micros_per_1m,
			  output_micros_per_1m = excluded.output_micros_per_1m,
			  currency             = excluded.currency,
			  updated_at           = excluded.updated_at`,
			p.ConnectionID, model, p.InputMicrosPer1M, p.OutputMicrosPer1M, currency, nowMs)
		return err
	})
}

func scanModelPrice(sc scanner) (*ModelPrice, error) {
	var p ModelPrice
	err := sc.Scan(&p.ConnectionID, &p.Model, &p.InputMicrosPer1M,
		&p.OutputMicrosPer1M, &p.Currency, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const modelPriceCols = `connection_id, model, input_micros_per_1m, output_micros_per_1m, currency, updated_at`

// GetModelPrice returns the stored price, or ErrNotFound when the model has no
// published price on this connection.
func (s *Store) GetModelPrice(ctx context.Context, connectionID int64, model string) (*ModelPrice, error) {
	return scanModelPrice(s.ro.QueryRowContext(ctx,
		"SELECT "+modelPriceCols+" FROM llm_model_prices WHERE connection_id = ? AND model = ?",
		connectionID, model))
}

func loadModelPrice(ctx context.Context, q querier, connectionID int64, model string) (*ModelPrice, error) {
	return scanModelPrice(q.QueryRowContext(ctx,
		"SELECT "+modelPriceCols+" FROM llm_model_prices WHERE connection_id = ? AND model = ?",
		connectionID, model))
}

// ListModelPrices returns every stored price for a connection, by model name.
func (s *Store) ListModelPrices(ctx context.Context, connectionID int64) ([]ModelPrice, error) {
	rows, err := s.ro.QueryContext(ctx,
		"SELECT "+modelPriceCols+" FROM llm_model_prices WHERE connection_id = ? ORDER BY model",
		connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelPrice
	for rows.Next() {
		p, err := scanModelPrice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ModelPrice{}
	}
	return out, nil
}

// DeleteModelPrice removes one price row.
func (s *Store) DeleteModelPrice(ctx context.Context, connectionID int64, model string) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			"DELETE FROM llm_model_prices WHERE connection_id = ? AND model = ?", connectionID, model)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ComputeCostMicros converts token counts and a per-1M price into micro-units.
// It is an approximation: providers bill on their own accounting, so the value
// is an estimate and is never presented as a guaranteed charge.
func ComputeCostMicros(inputMicrosPer1M, outputMicrosPer1M, promptTokens, completionTokens int64) int64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	return (promptTokens*inputMicrosPer1M + completionTokens*outputMicrosPer1M) / 1_000_000
}

// EstimateRunCostMicros estimates one call from an estimated prompt token
// count and the request's maximum output tokens. Only the output part is a
// hard ceiling for this request; the prompt part is exactly as good as the
// estimator behind it. A character-count heuristic is NOT a proven upper
// bound for every script (Arabic, emoji and symbols can tokenise at more than
// one token per character), so callers must label the result an estimate and
// must never present it as a guaranteed spending cap. It returns nil when the
// model has no stored price, so callers show "unavailable" instead of a
// misleading zero.
func EstimateRunCostMicros(price *ModelPrice, estPromptTokens, maxOutputTokens int64) *int64 {
	if price == nil || estPromptTokens < 0 || maxOutputTokens < 0 {
		return nil
	}
	v := ComputeCostMicros(price.InputMicrosPer1M, price.OutputMicrosPer1M, estPromptTokens, maxOutputTokens)
	return &v
}

// ---------------------------------------------------------------------------
// Managed agents
// ---------------------------------------------------------------------------

const managedAgentCols = `ma.id, ma.group_id, ma.bot_id, ma.connection_id, c.provider, ma.model, ma.persona,
	ma.max_output_tokens, ma.context_max_messages, ma.context_max_chars,
	ma.daily_run_limit, ma.daily_budget_micros, ma.enabled, ma.created_at, ma.updated_at,
	m.name, m.role_title`

const managedAgentFrom = ` FROM managed_agents ma
	JOIN llm_connections c ON c.id = ma.connection_id
	JOIN bot_members m ON m.group_id = ma.group_id AND m.bot_id = ma.bot_id`

func scanManagedAgent(sc scanner) (*ManagedAgent, error) {
	var a ManagedAgent
	var budget sql.NullInt64
	var enabled int
	err := sc.Scan(&a.ID, &a.GroupID, &a.BotID, &a.ConnectionID, &a.Provider, &a.Model, &a.Persona,
		&a.MaxOutputTokens, &a.ContextMaxMessages, &a.ContextMaxChars,
		&a.DailyRunLimit, &budget, &enabled, &a.CreatedAt, &a.UpdatedAt,
		&a.BotName, &a.RoleTitle)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if budget.Valid {
		v := budget.Int64
		a.DailyBudgetMicros = &v
	}
	a.Enabled = enabled != 0
	return &a, nil
}

func loadManagedAgent(ctx context.Context, q querier, groupID int64, botID string) (*ManagedAgent, error) {
	return scanManagedAgent(q.QueryRowContext(ctx,
		"SELECT "+managedAgentCols+managedAgentFrom+" WHERE ma.group_id = ? AND ma.bot_id = ?",
		groupID, botID))
}

func validateManagedAgentInput(in *ManagedAgentInput) error {
	if in.ConnectionID <= 0 {
		return wrapErr(ErrValidation, "a connection is required")
	}
	in.Model = trimText(in.Model)
	if !modelNameRE.MatchString(in.Model) {
		return wrapErr(ErrValidation, "model must be chosen from the models the provider reports for this key")
	}
	in.Persona = trimText(Sanitize(in.Persona))
	if len([]rune(in.Persona)) > maxPersonaRunes {
		return wrapErr(ErrValidation, fmt.Sprintf("persona must be at most %d characters", maxPersonaRunes))
	}
	if in.MaxOutputTokens == 0 {
		in.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if in.MaxOutputTokens < 64 || in.MaxOutputTokens > 8192 {
		return wrapErr(ErrValidation, "max output tokens must be between 64 and 8192")
	}
	if in.ContextMaxMessages == 0 {
		in.ContextMaxMessages = DefaultContextMaxMessages
	}
	if in.ContextMaxMessages < 1 || in.ContextMaxMessages > 100 {
		return wrapErr(ErrValidation, "context messages must be between 1 and 100")
	}
	if in.ContextMaxChars == 0 {
		in.ContextMaxChars = DefaultContextMaxChars
	}
	if in.ContextMaxChars < 500 || in.ContextMaxChars > 100000 {
		return wrapErr(ErrValidation, "context characters must be between 500 and 100000")
	}
	if in.DailyRunLimit == 0 {
		in.DailyRunLimit = DefaultDailyRunLimit
	}
	if in.DailyRunLimit < 1 || in.DailyRunLimit > 1000 {
		return wrapErr(ErrValidation, "daily run limit must be between 1 and 1000")
	}
	if in.DailyBudgetMicros != nil && *in.DailyBudgetMicros < 0 {
		return wrapErr(ErrValidation, "daily budget must not be negative")
	}
	return nil
}

// insertManagedMember creates the bot_members row for a managed agent. It has
// no token at all: token_hash and token_enc stay empty, so no public room link
// exists for a member whose replies the server writes.
func insertManagedMember(ctx context.Context, conn *sql.Conn, m *Member) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO bot_members
		  (group_id, bot_id, name, role_title, accent_color, is_mock, token_hash, token_enc, last_seen_at)
		VALUES (?, ?, ?, ?, ?, 0, '', '', ?)`,
		m.GroupID, m.BotID, m.Name, m.RoleTitle, m.AccentColor, m.LastSeenAt)
	return err
}

// nextBotNumber picks the next free bot_N number, also considering senders
// already present in the message log so a re-used number cannot collide.
func nextBotNumber(ctx context.Context, conn *sql.Conn, groupID int64, ms []Member) (int, error) {
	highest := 0
	for _, m := range ms {
		if n := botNum(m.BotID); n > highest {
			highest = n
		}
	}
	var highestMsg int64
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(CAST(substr(sender_bot_id, 5) AS INTEGER)), 0)
		 FROM messages WHERE group_id = ? AND sender_bot_id LIKE 'bot_%'`,
		groupID).Scan(&highestMsg); err != nil {
		return 0, err
	}
	if int(highestMsg) > highest {
		highest = int(highestMsg)
	}
	return highest + 1, nil
}

// CreateManagedAgent adds a new bot member to the group and links it to an LLM
// connection in one transaction. The member counts toward the 2–10 limit.
func (s *Store) CreateManagedAgent(ctx context.Context, groupID int64, member MemberInput, in ManagedAgentInput, nowMs int64) (*ManagedAgent, error) {
	if err := validateManagedAgentInput(&in); err != nil {
		return nil, err
	}
	member.IsMock = false
	var botID string
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		var connExists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM llm_connections WHERE id = ?", in.ConnectionID).Scan(&connExists); err != nil {
			return err
		}
		if connExists == 0 {
			return wrapErr(ErrNotFound, "LLM connection not found")
		}
		ms, err := loadMembers(ctx, conn, groupID, s.key())
		if err != nil {
			return err
		}
		if len(ms) >= 10 {
			return wrapErr(ErrValidation, "A group can have at most 10 members")
		}
		num, err := nextBotNumber(ctx, conn, groupID, ms)
		if err != nil {
			return err
		}
		m := buildMember(groupID, num, member, "")
		if err := insertManagedMember(ctx, conn, &m); err != nil {
			return err
		}
		botID = m.BotID
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO managed_agents
			  (group_id, bot_id, connection_id, model, persona, max_output_tokens,
			   context_max_messages, context_max_chars, daily_run_limit, daily_budget_micros,
			   enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			groupID, m.BotID, in.ConnectionID, in.Model, in.Persona, in.MaxOutputTokens,
			in.ContextMaxMessages, in.ContextMaxChars, in.DailyRunLimit, in.DailyBudgetMicros,
			boolInt(in.Enabled), nowMs, nowMs); err != nil {
			return err
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
	if err != nil {
		return nil, err
	}
	return s.GetManagedAgent(ctx, groupID, botID)
}

// GetManagedAgent returns one agent config with its member's display fields.
func (s *Store) GetManagedAgent(ctx context.Context, groupID int64, botID string) (*ManagedAgent, error) {
	return loadManagedAgent(ctx, s.ro, groupID, botID)
}

// GetGroup returns one group row from the read pool. Unlike GetGroupDetail it
// never applies a due mock reply, so read-only admin endpoints can use it.
func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	return loadGroup(ctx, s.ro, id)
}

// ListManagedAgents returns every agent config in a group.
func (s *Store) ListManagedAgents(ctx context.Context, groupID int64) ([]ManagedAgent, error) {
	rows, err := s.ro.QueryContext(ctx,
		"SELECT "+managedAgentCols+managedAgentFrom+" WHERE ma.group_id = ? ORDER BY ma.id", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManagedAgent
	for rows.Next() {
		a, err := scanManagedAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ManagedAgent{}
	}
	return out, nil
}

// UpdateManagedAgent edits an existing config. The member row is untouched.
func (s *Store) UpdateManagedAgent(ctx context.Context, groupID int64, botID string, in ManagedAgentInput, nowMs int64) (*ManagedAgent, error) {
	if err := validateManagedAgentInput(&in); err != nil {
		return nil, err
	}
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var agentID int64
		err := conn.QueryRowContext(ctx,
			"SELECT id FROM managed_agents WHERE group_id = ? AND bot_id = ?", groupID, botID).Scan(&agentID)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var connExists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM llm_connections WHERE id = ?", in.ConnectionID).Scan(&connExists); err != nil {
			return err
		}
		if connExists == 0 {
			return wrapErr(ErrNotFound, "LLM connection not found")
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE managed_agents
			SET connection_id = ?, model = ?, persona = ?, max_output_tokens = ?,
			    context_max_messages = ?, context_max_chars = ?, daily_run_limit = ?,
			    daily_budget_micros = ?, enabled = ?, updated_at = ?
			WHERE group_id = ? AND bot_id = ?`,
			in.ConnectionID, in.Model, in.Persona, in.MaxOutputTokens,
			in.ContextMaxMessages, in.ContextMaxChars, in.DailyRunLimit,
			in.DailyBudgetMicros, boolInt(in.Enabled), nowMs, groupID, botID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			"UPDATE groups SET updated_at = ? WHERE id = ?", nowMs, groupID); err != nil {
			return err
		}
		noteChange(ctx, groupID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetManagedAgent(ctx, groupID, botID)
}

// DeleteManagedAgent removes the config AND its member: a member without a
// token that is neither mock nor managed could never reply, so keeping it
// would only leave a permanently silent seat. The group must keep 2 members.
func (s *Store) DeleteManagedAgent(ctx context.Context, groupID int64, botID string, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var exists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM managed_agents WHERE group_id = ? AND bot_id = ?",
			groupID, botID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		return s.removeMemberTx(ctx, conn, groupID, botID, nowMs)
	})
}

// MarkManagedAgentOnline records in-memory presence for a managed member that
// has no token to touch the room with, so online routing and the admin view
// treat it like any other active member.
func (s *Store) MarkManagedAgentOnline(groupID int64, botID string, nowMs int64) {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	if s.botSeen[groupID] == nil {
		s.botSeen[groupID] = map[string]int64{}
	}
	s.botSeen[groupID][botID] = nowMs
}

// ---------------------------------------------------------------------------
// Agent runs
// ---------------------------------------------------------------------------

const agentRunCols = `id, group_id, bot_id, agent_id, turn_seq, turn_epoch, turn_started_at, attempt, retry_of,
	status, provider, model, request_msg_count, request_chars,
	prompt_tokens, completion_tokens, total_tokens, est_cost_micros, cost_micros,
	cost_basis, cost_confirmed, response_chars, message_id, error_code, error_msg,
	created_at, started_at, finished_at`

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func scanAgentRun(sc scanner) (*AgentRun, error) {
	var r AgentRun
	var agentID, retryOf, prompt, completion, total sql.NullInt64
	var estCost, cost, messageID sql.NullInt64
	var costConfirmed int
	err := sc.Scan(&r.ID, &r.GroupID, &r.BotID, &agentID, &r.TurnSeq, &r.TurnEpoch, &r.TurnStartedAt,
		&r.Attempt, &retryOf, &r.Status, &r.Provider, &r.Model,
		&r.RequestMsgCount, &r.RequestChars, &prompt, &completion, &total,
		&estCost, &cost, &r.CostBasis, &costConfirmed, &r.ResponseChars, &messageID,
		&r.ErrorCode, &r.ErrorMsg, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.AgentID = nullInt(agentID)
	r.RetryOf = nullInt(retryOf)
	r.PromptTokens = nullInt(prompt)
	r.CompletionTokens = nullInt(completion)
	r.TotalTokens = nullInt(total)
	r.EstCostMicros = nullInt(estCost)
	r.CostMicros = nullInt(cost)
	r.CostConfirmed = costConfirmed != 0
	r.MessageID = nullInt(messageID)
	return &r, nil
}

func loadAgentRun(ctx context.Context, q querier, id int64) (*AgentRun, error) {
	return scanAgentRun(q.QueryRowContext(ctx,
		"SELECT "+agentRunCols+" FROM agent_runs WHERE id = ?", id))
}

// loadLatestRunForTurn returns the newest attempt of one turn assignment,
// open or terminal, regardless of the turn clock. Any row it finds means the
// assignment is already claimed, even if a pause/resume moved the clock.
func loadLatestRunForTurn(ctx context.Context, q querier, groupID int64, botID string, turnEpoch int64) (*AgentRun, error) {
	r, err := scanAgentRun(q.QueryRowContext(ctx,
		"SELECT "+agentRunCols+` FROM agent_runs
		 WHERE group_id = ? AND bot_id = ? AND turn_epoch = ?
		 ORDER BY attempt DESC, id DESC LIMIT 1`,
		groupID, botID, turnEpoch))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return r, err
}

func isRunTerminal(status string) bool {
	switch status {
	case AgentRunCompleted, AgentRunFailed, AgentRunInterrupted, AgentRunCancelled, AgentRunDiscarded:
		return true
	}
	return false
}

// insertAgentRun appends the next attempt number for a turn. Attempt numbers
// are continuous across clock resets and retries of the same turn, so the
// admin sees one history line per attempt instead of a reset after every
// pause/resume.
func insertAgentRun(ctx context.Context, conn *sql.Conn, groupID int64, botID string,
	agentID int64, provider, model string, turnSeq, turnEpoch, turnClock int64,
	retryOf *int64, nowMs int64) (int64, error) {
	var attempt int64
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(attempt), 0) + 1 FROM agent_runs
		 WHERE group_id = ? AND bot_id = ? AND turn_epoch = ?`,
		groupID, botID, turnEpoch).Scan(&attempt); err != nil {
		return 0, err
	}
	res, err := conn.ExecContext(ctx, `
		INSERT INTO agent_runs
		  (group_id, bot_id, agent_id, turn_seq, turn_epoch, turn_started_at, attempt, retry_of,
		   status, provider, model, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?)`,
		groupID, botID, agentID, turnSeq, turnEpoch, turnClock, attempt, retryOf,
		provider, model, nowMs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnsureAgentRun is the ONLY automatic path that creates a run. It never
// retries: if the turn instance already has a run — queued, running, failed,
// interrupted, cancelled or discarded — it returns that row with created=false.
func (s *Store) EnsureAgentRun(ctx context.Context, groupID int64, botID string, nowMs int64) (*AgentRun, bool, error) {
	var out *AgentRun
	created := false
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status != "running" || g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != botID {
			return nil
		}
		agent, err := loadManagedAgent(ctx, conn, groupID, botID)
		if errors.Is(err, ErrNotFound) {
			return nil // a normal (token-driven) member
		}
		if err != nil {
			return err
		}
		if !agent.Enabled {
			return nil
		}
		existing, err := loadLatestRunForTurn(ctx, conn, groupID, botID, g.TurnEpoch)
		if err != nil {
			return err
		}
		if existing != nil {
			out = existing // claimed, including terminal rows: never silent-retry
			return nil
		}
		usage, err := usageFor(ctx, conn, agent.ID, agent.DailyRunLimit, agent.DailyBudgetMicros, nowMs)
		if err != nil {
			return err
		}
		if usage.RunsLast24h >= usage.DailyRunLimit {
			return nil // run cap reached: no run row, so the admin can retry later
		}
		if usage.BudgetBlocked {
			// Either the known spend reached the budget, or a cost in the
			// window is unknown so the cap cannot be checked. In both cases
			// no new request starts; GetManagedAgentUsage exposes the reason.
			return nil
		}
		if _, err := conn.ExecContext(ctx,
			"UPDATE bot_members SET last_seen_at = ? WHERE group_id = ? AND bot_id = ?",
			nowMs, groupID, botID); err != nil {
			return err
		}
		id, err := insertAgentRun(ctx, conn, groupID, botID, agent.ID, agent.Provider,
			agent.Model, g.TurnSeq, g.TurnEpoch, g.TurnStartedAt, nil, nowMs)
		if err != nil {
			return err
		}
		out, err = loadAgentRun(ctx, conn, id)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if out != nil {
		s.MarkManagedAgentOnline(groupID, botID, nowMs)
	}
	return out, created, nil
}

// StartAgentRun moves a queued run to running after re-checking that its turn
// instance is still current. A run whose turn was skipped, paused or taken by
// another speaker is marked discarded instead of being executed.
func (s *Store) StartAgentRun(ctx context.Context, runID int64, in AgentRunStart, nowMs int64) (bool, error) {
	started := false
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		r, err := loadAgentRun(ctx, conn, runID)
		if err != nil {
			return err
		}
		if r.Status != AgentRunQueued {
			return nil
		}
		g, err := loadGroup(ctx, conn, r.GroupID)
		if err != nil {
			return err
		}
		if g.Status != "running" || g.TurnEpoch != r.TurnEpoch || g.TurnSeq != r.TurnSeq ||
			g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != r.BotID {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "turn_moved_on",
				"the turn was skipped, paused or completed before the run started", nowMs)
		}
		agent, err := loadManagedAgent(ctx, conn, r.GroupID, r.BotID)
		if errors.Is(err, ErrNotFound) {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "agent_gone",
				"the managed agent config no longer exists", nowMs)
		}
		if err != nil {
			return err
		}
		if !agent.Enabled {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "agent_disabled",
				"the managed agent was disabled before the run started", nowMs)
		}
		// The budget is re-checked here, immediately before the provider
		// request, because another run may have finished since the queue row
		// was created. This run's own row is excluded: it has no cost yet.
		blocked, reason, err := budgetBlockedWithoutRun(ctx, conn, agent.ID, r.ID,
			agent.DailyBudgetMicros, nowMs)
		if err != nil {
			return err
		}
		if blocked {
			msg := "the daily budget for this agent has been reached"
			if reason == BudgetReasonUnknown {
				msg = "the daily budget cannot be verified: one or more runs in the window have no recorded cost"
			}
			return markRunTerminal(ctx, conn, r.ID, AgentRunFailed, reason, msg, nowMs)
		}
		msgCount := in.RequestMsgCount
		if msgCount < 0 {
			msgCount = 0
		}
		chars := in.RequestChars
		if chars < 0 {
			chars = 0
		}
		res, err := conn.ExecContext(ctx, `
			UPDATE agent_runs
			SET status = 'running', started_at = ?, request_msg_count = ?, request_chars = ?,
			    est_cost_micros = ?
			WHERE id = ? AND status = 'queued'`,
			nowMs, msgCount, chars, in.EstCostMicros, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		started = n > 0
		return nil
	})
	return started, err
}

// markRunTerminal is the guarded transition used by the store itself. Callers
// must already have decided the transition is valid.
func markRunTerminal(ctx context.Context, conn *sql.Conn, runID int64, status, code, msg string, nowMs int64) error {
	msg = capText(trimText(Sanitize(msg)), 300)
	_, err := conn.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = ?, error_code = ?, error_msg = ?, finished_at = ?
		WHERE id = ? AND status IN ('queued', 'running')`,
		status, code, msg, nowMs, runID)
	return err
}

// FailAgentRun records a provider or transport failure. A run that was already
// cancelled or interrupted is left untouched.
func (s *Store) FailAgentRun(ctx context.Context, runID int64, code, msg string, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return markRunTerminal(ctx, conn, runID, AgentRunFailed, code, msg, nowMs)
	})
}

// CancelAgentRun cancels one open run (admin action on a single turn).
func (s *Store) CancelAgentRun(ctx context.Context, runID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return markRunTerminal(ctx, conn, runID, AgentRunCancelled, "cancelled", "cancelled by admin", nowMs)
	})
}

// CancelAgentRuns cancels every open run of a group. It is the hook Pause,
// Skip, stop, the kill switch and a silent-turn timeout call inside the same
// transaction that changes the turn, so a late provider reply can never be
// accepted after the turn state moved. A cancelled run stays claimed for its
// turn epoch: it is never retried automatically.
func (s *Store) CancelAgentRuns(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return cancelAgentRunsTx(ctx, conn, groupID, "cancelled",
			"cancelled because the group was paused or stopped", nowMs)
	})
}

// cancelAgentRunsTx is the transaction-scoped cancel used by turn mutations.
// It only touches open rows, so a completed reply is never overwritten.
func cancelAgentRunsTx(ctx context.Context, conn *sql.Conn, groupID int64, code, msg string, nowMs int64) error {
	msg = capText(trimText(Sanitize(msg)), 300)
	_, err := conn.ExecContext(ctx, `
		UPDATE agent_runs
		SET status = 'cancelled', error_code = ?, error_msg = ?, finished_at = ?
		WHERE group_id = ? AND status IN ('queued', 'running')`,
		code, msg, nowMs, groupID)
	return err
}

// AgentAssignment is a running group whose current speaker is an enabled
// managed agent. The runner uses it to decide which turn to claim; the claim
// itself is re-validated inside EnsureAgentRun's write transaction.
type AgentAssignment struct {
	GroupID       int64
	BotID         string
	TurnSeq       int64
	TurnEpoch     int64
	TurnStartedAt int64
}

// RunnableManagedAssignments lists current managed assignments using the read
// pool. Disabled agents and groups that are not running are never returned.
func (s *Store) RunnableManagedAssignments(ctx context.Context) ([]AgentAssignment, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT g.id, g.current_turn_bot_id, g.turn_seq, g.turn_epoch, g.turn_started_at
		FROM groups g
		JOIN managed_agents ma ON ma.group_id = g.id AND ma.bot_id = g.current_turn_bot_id
		WHERE g.status = 'running' AND g.current_turn_bot_id IS NOT NULL AND ma.enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentAssignment
	for rows.Next() {
		var a AgentAssignment
		var botID sql.NullString
		if err := rows.Scan(&a.GroupID, &botID, &a.TurnSeq, &a.TurnEpoch, &a.TurnStartedAt); err != nil {
			return nil, err
		}
		if !botID.Valid {
			continue
		}
		a.BotID = botID.String
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []AgentAssignment{}
	}
	return out, nil
}

// AgentRunPrompt is everything the runner needs to build one request. It is a
// read-only snapshot: StartAgentRun re-validates the turn before the request
// is sent, and SubmitManagedAgentReply re-validates it before the reply is
// stored, so a stale snapshot can never win a race.
type AgentRunPrompt struct {
	Run      AgentRun
	Group    Group
	Agent    ManagedAgent
	Price    *ModelPrice
	Messages []Message
}

// LoadAgentRunPrompt loads a run with its group, agent config, stored model
// price and the newest maxMessages messages, oldest first. The price is nil
// when the model has no stored price; that is reported as an unknown cost, not
// as an error.
func (s *Store) LoadAgentRunPrompt(ctx context.Context, runID, maxMessages int64) (*AgentRunPrompt, error) {
	run, err := loadAgentRun(ctx, s.ro, runID)
	if err != nil {
		return nil, err
	}
	g, err := loadGroup(ctx, s.ro, run.GroupID)
	if err != nil {
		return nil, err
	}
	agent, err := loadManagedAgent(ctx, s.ro, run.GroupID, run.BotID)
	if err != nil {
		return nil, err
	}
	if maxMessages <= 0 {
		maxMessages = agent.ContextMaxMessages
	}
	if maxMessages < 1 {
		maxMessages = 1
	}
	if maxMessages > 100 {
		maxMessages = 100
	}
	msgs, err := loadMessagesPage(ctx, s.ro, run.GroupID, 0, 0, maxMessages)
	if err != nil {
		return nil, err
	}
	out := &AgentRunPrompt{Run: *run, Group: *g, Agent: *agent, Messages: msgs}
	// Price the model frozen on the run row: an admin may have changed the
	// agent's model after this attempt was created.
	if price, err := loadModelPrice(ctx, s.ro, agent.ConnectionID, run.Model); err == nil {
		out.Price = price
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return out, nil
}

// budgetBlockedWithoutRun re-checks the optional cost cap immediately before a
// provider request, ignoring the run being started. The queued row has no cost
// yet, so counting it would make every run block itself as "cost unknown"
// during the queued -> running transition.
func budgetBlockedWithoutRun(ctx context.Context, q querier, agentID, runID int64, budget *int64, nowMs int64) (bool, string, error) {
	if budget == nil {
		return false, "", nil
	}
	var unknown, spent int64
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN cost_micros IS NULL THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(cost_micros), 0)
		FROM agent_runs
		WHERE agent_id = ? AND created_at >= ? AND id != ?`,
		agentID, nowMs-agentRunWindowMs, runID).Scan(&unknown, &spent)
	if err != nil {
		return false, "", err
	}
	if unknown > 0 {
		return true, BudgetReasonUnknown, nil
	}
	if spent >= *budget {
		return true, BudgetReasonReached, nil
	}
	return false, "", nil
}

// InterruptStaleAgentRuns is the startup recovery pass. Anything left open by a
// previous process becomes interrupted and stays claimed: no scheduler tick or
// group-change hook can turn it into a fresh automatic run.
func (s *Store) InterruptStaleAgentRuns(ctx context.Context, nowMs int64) (int64, error) {
	var n int64
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `
			UPDATE agent_runs
			SET status = 'interrupted', error_code = 'interrupted', error_msg = ?, finished_at = ?
			WHERE status IN ('queued', 'running')`,
			"server restarted before the run finished", nowMs)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// RetryAgentRun is the explicit admin retry. It appends a new attempt for the
// same turn instance, resets the turn clock so the new attempt gets a full
// window, and refuses when the turn moved on or the budget is exhausted.
func (s *Store) RetryAgentRun(ctx context.Context, runID int64, nowMs int64) (*AgentRun, error) {
	var out *AgentRun
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		prev, err := loadAgentRun(ctx, conn, runID)
		if err != nil {
			return err
		}
		if !isRunTerminal(prev.Status) {
			return wrapErr(ErrValidation, "stop the run before retrying it")
		}
		latest, err := loadLatestRunForTurn(ctx, conn, prev.GroupID, prev.BotID, prev.TurnEpoch)
		if err != nil {
			return err
		}
		if latest == nil || latest.ID != prev.ID {
			return wrapErr(ErrValidation, "a newer attempt already exists for this turn")
		}
		g, err := loadGroup(ctx, conn, prev.GroupID)
		if err != nil {
			return err
		}
		if g.Status != "running" || g.TurnEpoch != prev.TurnEpoch || g.TurnSeq != prev.TurnSeq ||
			g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != prev.BotID {
			return wrapErr(ErrValidation, "the turn has moved on; retry is no longer possible")
		}
		agent, err := loadManagedAgent(ctx, conn, prev.GroupID, prev.BotID)
		if errors.Is(err, ErrNotFound) {
			return wrapErr(ErrValidation, "the managed agent config no longer exists")
		}
		if err != nil {
			return err
		}
		if !agent.Enabled {
			return wrapErr(ErrValidation, "the managed agent is disabled")
		}
		usage, err := usageFor(ctx, conn, agent.ID, agent.DailyRunLimit, agent.DailyBudgetMicros, nowMs)
		if err != nil {
			return err
		}
		if usage.RunsLast24h >= usage.DailyRunLimit {
			return wrapErr(ErrValidation, "the daily run limit for this agent has been reached")
		}
		if usage.BudgetBlocked {
			if usage.BudgetBlockReason == BudgetReasonUnknown {
				return wrapErr(ErrValidation, "the daily budget cannot be verified: one or more runs in the window have no recorded cost")
			}
			return wrapErr(ErrValidation, "the daily budget for this agent has been reached")
		}
		// The turn identity is (turn_seq, turn_epoch), so moving the clock only
		// gives the new attempt a full timeout window; it does not create a new
		// turn.
		g.TurnStartedAt = nowMs
		g.SkipStreak = 0
		g.UpdatedAt = nowMs
		if err := s.saveGroup(ctx, conn, g); err != nil {
			return err
		}
		prevID := prev.ID
		id, err := insertAgentRun(ctx, conn, prev.GroupID, prev.BotID, agent.ID, agent.Provider,
			agent.Model, prev.TurnSeq, prev.TurnEpoch, nowMs, &prevID, nowMs)
		if err != nil {
			return err
		}
		out, err = loadAgentRun(ctx, conn, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SubmitManagedAgentReply commits the assistant message and the run's
// completion in one transaction. accepted is false (with no error) when the
// run is no longer the current turn — the caller answers 200 with accepted:false,
// exactly like SubmitBotReply.
func (s *Store) SubmitManagedAgentReply(ctx context.Context, runID, expectedTurnSeq int64, rawContent string, usage AgentUsage, nowMs int64) (bool, error) {
	accepted := false
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		r, err := loadAgentRun(ctx, conn, runID)
		if err != nil {
			return err
		}
		if r.Status != AgentRunRunning {
			// Cancelled, interrupted or already finished: refuse silently.
			return nil
		}
		if r.TurnSeq != expectedTurnSeq {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "stale_turn",
				"the reply was for an older turn", nowMs)
		}
		m, err := loadMemberRow(ctx, conn, r.GroupID, r.BotID)
		if err != nil {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "member_gone",
				"the member no longer exists", nowMs)
		}
		g, err := loadGroup(ctx, conn, r.GroupID)
		if err != nil {
			return err
		}
		if g.Status != "running" {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "group_not_running",
				"the group was paused or stopped", nowMs)
		}
		if g.TurnEpoch != r.TurnEpoch || g.TurnSeq != r.TurnSeq ||
			g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != r.BotID {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "turn_moved_on",
				"the turn was skipped or completed", nowMs)
		}
		content := capText(Sanitize(rawContent), maxRunReplyRunes)
		if content == "" {
			return markRunTerminal(ctx, conn, r.ID, AgentRunDiscarded, "empty_reply",
				"the model returned an empty reply", nowMs)
		}
		seq := g.TurnSeq
		messageID, err := insertMessage(ctx, conn, &Message{
			GroupID: r.GroupID, TurnSeq: &seq, SenderBotID: m.BotID,
			SenderName: m.Name, SenderColor: m.AccentColor, Role: "bot",
			Content: content, CreatedAt: nowMs,
		})
		if err != nil {
			return err
		}
		ms, err := s.loadMembersLive(ctx, conn, r.GroupID, nowMs)
		if err != nil {
			return err
		}
		g.SkipStreak = 0
		advanceAfterReply(g, ms, nowMs)
		if err := s.saveGroup(ctx, conn, g); err != nil {
			return err
		}

		// Cost: only from reported usage and a stored price. Anything else
		// stays NULL so the UI can show "unavailable".
		prompt := nonNegPtr(usage.PromptTokens)
		completion := nonNegPtr(usage.CompletionTokens)
		total := nonNegPtr(usage.TotalTokens)
		if total == nil && prompt != nil && completion != nil {
			sum := *prompt + *completion
			total = &sum
		}
		var cost *int64
		basis := "none"
		confirmed := 0
		if usage.CostMicros != nil && *usage.CostMicros >= 0 {
			v := *usage.CostMicros
			cost = &v
			basis = "usage"
			confirmed = 1 // the provider reported the charge itself
		} else if prompt != nil && completion != nil {
			if price, err := loadModelPrice(ctx, conn, agentConnectionID(ctx, conn, r.GroupID, r.BotID), r.Model); err == nil {
				v := ComputeCostMicros(price.InputMicrosPer1M, price.OutputMicrosPer1M, *prompt, *completion)
				cost = &v
				basis = "usage"
			}
		}
		res, err := conn.ExecContext(ctx, `
			UPDATE agent_runs
			SET status = 'completed', message_id = ?, response_chars = ?,
			    prompt_tokens = ?, completion_tokens = ?, total_tokens = ?,
			    cost_micros = ?, cost_basis = ?, cost_confirmed = ?, finished_at = ?
			WHERE id = ? AND status = 'running'`,
			messageID, len([]rune(content)), prompt, completion, total,
			cost, basis, confirmed, nowMs, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// Cannot happen under the single writer, but never report a
			// stored message with an open run: roll everything back.
			return errRunStale
		}
		accepted = true
		return nil
	})
	if errors.Is(err, errRunStale) {
		return false, nil
	}
	return accepted, err
}

// agentConnectionID is a tiny helper for the cost lookup. It returns 0 when the
// agent config is gone, which simply means no price row is found.
func agentConnectionID(ctx context.Context, q querier, groupID int64, botID string) int64 {
	var id int64
	if err := q.QueryRowContext(ctx,
		"SELECT connection_id FROM managed_agents WHERE group_id = ? AND bot_id = ?",
		groupID, botID).Scan(&id); err != nil {
		return 0
	}
	return id
}

func nonNegPtr(v *int64) *int64 {
	if v == nil || *v < 0 {
		return nil
	}
	n := *v
	return &n
}

// loadMemberRow reads one member by group and bot ID without touching tokens.
// Managed members have no token, so decryption is never needed here.
func loadMemberRow(ctx context.Context, q querier, groupID int64, botID string) (*Member, error) {
	var m Member
	var isMock int
	err := q.QueryRowContext(ctx, `
		SELECT group_id, bot_id, name, role_title, accent_color, is_mock, token_hash, token_enc, last_seen_at
		FROM bot_members WHERE group_id = ? AND bot_id = ?`, groupID, botID).
		Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor,
			&isMock, &m.TokenHash, &m.TokenEnc, &m.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.IsMock = isMock != 0
	return &m, nil
}

// GetAgentRun returns one run by ID.
func (s *Store) GetAgentRun(ctx context.Context, id int64) (*AgentRun, error) {
	return loadAgentRun(ctx, s.ro, id)
}

// ListAgentRuns returns the most recent runs of a group, newest first.
func (s *Store) ListAgentRuns(ctx context.Context, groupID int64, limit int64) ([]AgentRun, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	rows, err := s.ro.QueryContext(ctx,
		"SELECT "+agentRunCols+" FROM agent_runs WHERE group_id = ? ORDER BY id DESC LIMIT ?",
		groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRun
	for rows.Next() {
		r, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []AgentRun{}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Usage / budget
// ---------------------------------------------------------------------------

func usageFor(ctx context.Context, q querier, agentID, runLimit int64, budget *int64, nowMs int64) (*ManagedAgentUsage, error) {
	windowStart := nowMs - agentRunWindowMs
	var runs, unknown, confirmed, computed, spent int64
	err := q.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN cost_micros IS NULL THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cost_micros IS NOT NULL AND cost_confirmed = 1 THEN cost_micros ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cost_micros IS NOT NULL AND cost_confirmed = 0 THEN cost_micros ELSE 0 END), 0),
		       COALESCE(SUM(cost_micros), 0)
		FROM agent_runs WHERE agent_id = ? AND created_at >= ?`,
		agentID, windowStart).Scan(&runs, &unknown, &confirmed, &computed, &spent)
	if err != nil {
		return nil, err
	}
	u := &ManagedAgentUsage{
		RunsLast24h:       runs,
		DailyRunLimit:     runLimit,
		SpentMicros:       spent,
		ConfirmedMicros:   confirmed,
		ComputedMicros:    computed,
		UnknownRuns:       unknown,
		DailyBudgetMicros: budget,
		CostKnown:         runs == 0 || unknown == 0,
	}
	switch {
	case budget == nil:
		u.BudgetState = BudgetUnset
	case !u.CostKnown:
		// The cap cannot be checked, so no new request may start. SpentMicros
		// is only a lower bound here.
		u.BudgetState = BudgetUnknown
		u.BudgetBlocked = true
		u.BudgetBlockReason = BudgetReasonUnknown
	case spent >= *budget:
		u.BudgetState = BudgetExhausted
		u.BudgetBlocked = true
		u.BudgetBlockReason = BudgetReasonReached
	default:
		u.BudgetState = BudgetOK
	}
	return u, nil
}

// GetManagedAgentUsage reports the rolling 24h run count and cost for an agent.
// This is check-at-start accounting, not a provider invoice guarantee: a run
// started while spend was below the budget can finish above it, and charges
// the provider never reports cannot be seen here. When CostKnown is false the
// budget state is "unknown" and new runs are refused.
func (s *Store) GetManagedAgentUsage(ctx context.Context, agentID, nowMs int64) (*ManagedAgentUsage, error) {
	var limit int64
	var budget sql.NullInt64
	err := s.ro.QueryRowContext(ctx,
		"SELECT daily_run_limit, daily_budget_micros FROM managed_agents WHERE id = ?", agentID).
		Scan(&limit, &budget)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var b *int64
	if budget.Valid {
		v := budget.Int64
		b = &v
	}
	return usageFor(ctx, s.ro, agentID, limit, b, nowMs)
}
