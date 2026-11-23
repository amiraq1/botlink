package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amiraq1/botlink/internal/store"
	"github.com/amiraq1/botlink/internal/tools"
)

var toolCallIDRegex = regexp.MustCompile(`^[A-Za-z0-9_\-\.:]{1,128}$`)

// ---------------------------------------------------------------------------
// Public room tool calling
// ---------------------------------------------------------------------------

func (s *Server) handleRoomToolCall(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ip := s.clientIP(r)
	now := time.Now()

	// 1. Check global per-IP limit
	if !s.checkIPLimit(w, ip, now) {
		return
	}

	// 2. Read-only token check (bad token limiter protection)
	exists, err := s.store.MemberExistsByToken(r.Context(), token)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if !exists {
		if ok, retry := s.badTokenLimiter.check(ip, now); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusTooManyRequests, "too many bad token attempts, try again later")
			return
		}
		s.badTokenLimiter.hit(ip, now)
		writeError(w, http.StatusNotFound, "member not found")
		return
	}

	// 3. Tool-call rate limit (20/min per IP+token)
	if !s.toolCallRateOK(w, r, token) {
		return
	}

	// 4. Parse request body capped at 64KB
	var body struct {
		ExpectedTurnSeq int64           `json:"expectedTurnSeq"`
		Tool            string          `json:"tool"`
		Arguments       json.RawMessage `json:"arguments"`
		CallID          string          `json:"callId"`
	}
	if !decodeRoomJSON(w, r, &body) {
		return
	}

	callID := strings.TrimSpace(body.CallID)
	if !toolCallIDRegex.MatchString(callID) {
		writeError(w, http.StatusBadRequest, "invalid callId (must be 1-128 alphanumeric characters, dashes or underscores)")
		return
	}

	toolName := strings.TrimSpace(body.Tool)
	if toolName == "" || len(toolName) > 64 {
		writeError(w, http.StatusBadRequest, "invalid tool name")
		return
	}

	args := body.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}

	// 5. Validate bot turn: group must be running, current turn must match expectedTurnSeq and be this bot's turn
	groupID, botID, err := s.store.ValidateBotTurnForTool(r.Context(), token, body.ExpectedTurnSeq, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}

	// 6. Lookup tool by name
	tool, err := s.store.GetToolByName(r.Context(), toolName)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("tool %q not found", toolName))
		return
	}
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if !tool.Enabled {
		writeError(w, http.StatusForbidden, fmt.Sprintf("tool %q is disabled globally", toolName))
		return
	}

	// 7. Check if tool is assigned and enabled for this bot
	enabled, maxCallsPerTurn, err := s.store.CheckMemberToolEnabled(r.Context(), groupID, botID, tool.ID)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusForbidden, fmt.Sprintf("tool %q is not enabled for this bot in this group", toolName))
		return
	}

	// 8. Enforce per-turn limit for this tool
	callsInTurn, err := s.store.CountToolCallsForTurn(r.Context(), groupID, botID, tool.ID, body.ExpectedTurnSeq)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if callsInTurn >= maxCallsPerTurn {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("maximum tool calls reached for this turn (limit: %d)", maxCallsPerTurn))
		return
	}

	// 9. Reserve invocation in DB (idempotent via callId)
	insertIn := store.ToolCallInsert{
		GroupID:   groupID,
		BotID:     botID,
		ToolID:    tool.ID,
		CallID:    callID,
		TurnSeq:   body.ExpectedTurnSeq,
		Arguments: args,
		CreatedAt: s.nowMs(),
	}
	tc, created, err := s.store.InsertToolCallPending(r.Context(), insertIn)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}

	// If not newly created, handle duplicate / replay
	if !created {
		if tc.Status == store.ToolCallStatusPending {
			writeError(w, http.StatusConflict, "tool call is already in progress")
			return
		}
		// Completed or terminal state: return cached result
		var resJSON any = tc.Result
		if json.Valid([]byte(tc.Result)) {
			resJSON = json.RawMessage(tc.Result)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"callId": tc.CallID,
			"tool":   tc.ToolName,
			"status": string(tc.Status),
			"result": resJSON,
			"error":  tc.ErrorMsg,
			"cached": true,
		})
		return
	}

	// 10. Execute tool with timeout and in-memory cancellation tracking
	timeoutMs := tool.TimeoutMs
	if timeoutMs < 500 {
		timeoutMs = 500
	} else if timeoutMs > 30_000 {
		timeoutMs = 30_000
	}

	toolCtx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutMs)*time.Millisecond)
	seq := s.registerToolCancel(groupID, cancel)
	defer s.unregisterToolCancel(groupID, seq)
	defer cancel()

	startTime := time.Now()
	rawResult, execErr := s.executeTool(toolCtx, groupID, botID, tool, args)
	durationMs := time.Since(startTime).Milliseconds()

	// 11. Re-check group status before committing result
	running, runErr := s.store.IsGroupRunning(context.Background(), groupID)
	if runErr != nil || !running || errors.Is(toolCtx.Err(), context.Canceled) {
		_ = s.store.CompleteToolCall(context.Background(), tc.ID, store.ToolCallStatusCancelled, "", "turn was interrupted", durationMs, s.nowMs())
		writeError(w, http.StatusConflict, "turn was interrupted")
		return
	}

	// 12. Handle tool execution error vs success
	if execErr != nil {
		status := store.ToolCallStatusError
		if errors.Is(execErr, ErrWebhookTimeout) || errors.Is(toolCtx.Err(), context.DeadlineExceeded) || errors.Is(execErr, context.DeadlineExceeded) {
			status = store.ToolCallStatusTimeout
		} else if errors.Is(execErr, ErrWebhookCanceled) || errors.Is(toolCtx.Err(), context.Canceled) || errors.Is(execErr, context.Canceled) {
			status = store.ToolCallStatusCancelled
		}
		safeErr := store.Sanitize(execErr.Error())
		_ = s.store.CompleteToolCall(context.Background(), tc.ID, status, "", safeErr, durationMs, s.nowMs())
		writeJSON(w, http.StatusOK, map[string]any{
			"callId": callID,
			"tool":   tool.Name,
			"status": string(status),
			"error":  safeErr,
		})
		return
	}

	// 13. Sanitize output and commit terminal success
	cleanResult := SanitizeToolOutput(rawResult)
	completeErr := s.store.CompleteToolCall(context.Background(), tc.ID, store.ToolCallStatusSuccess, cleanResult, "", durationMs, s.nowMs())
	if errors.Is(completeErr, store.ErrCallNotPending) {
		// Call was cancelled concurrently by Kill Switch / Pause
		writeError(w, http.StatusConflict, "turn was interrupted")
		return
	}
	if completeErr != nil {
		s.handleStoreErr(w, completeErr)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"callId": callID,
		"tool":   tool.Name,
		"status": "success",
		"result": json.RawMessage(cleanResult),
	})
}

func (s *Server) executeTool(ctx context.Context, groupID int64, botID string, t *store.Tool, args json.RawMessage) (string, error) {
	if t.Kind == store.ToolKindBuiltin {
		bTool, ok := tools.Get(t.Name)
		if !ok {
			return "", fmt.Errorf("builtin tool %q is not implemented in registry", t.Name)
		}
		res, err := bTool.Execute(ctx, args)
		if err != nil {
			return "", err
		}
		return res.Text, nil
	}

	if t.Kind == store.ToolKindHTTPWebhook {
		var headers map[string]string
		if t.WebhookHeadersEnc != "" {
			plain, err := s.store.DecryptToolSecret(t.WebhookHeadersEnc, t.ID)
			if err == nil && plain != "" {
				_ = json.Unmarshal([]byte(plain), &headers)
			}
		}

		var secret string
		if t.WebhookSecretEnc != "" {
			var err error
			secret, err = s.store.DecryptToolSecret(t.WebhookSecretEnc, t.ID)
			if err != nil {
				return "", fmt.Errorf("failed to decrypt webhook secret")
			}
		}

		req := WebhookRequest{
			URL:       t.WebhookURL,
			Headers:   headers,
			Secret:    secret,
			Payload:   args,
			TimeoutMs: t.TimeoutMs,
			GroupID:   groupID,
			BotID:     botID,
			ToolID:    t.ID,
			ToolName:  t.Name,
		}

		return s.webhookClient.Execute(ctx, req)
	}

	return "", fmt.Errorf("unsupported tool kind: %s", t.Kind)
}

// ---------------------------------------------------------------------------
// Admin tools CRUD
// ---------------------------------------------------------------------------

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListTools(r.Context())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateTool(w http.ResponseWriter, r *http.Request) {
	var in store.ToolInput
	if !decodeJSON(w, r, &in) {
		return
	}
	id, err := s.store.CreateTool(r.Context(), in, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) toolID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("toolId"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func (s *Server) handleGetTool(w http.ResponseWriter, r *http.Request) {
	id, ok := s.toolID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}
	tool, err := s.store.GetTool(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tool)
}

func (s *Server) handleUpdateTool(w http.ResponseWriter, r *http.Request) {
	id, ok := s.toolID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}
	var in store.ToolInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.UpdateTool(r.Context(), id, in, s.nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleEnableTool(w http.ResponseWriter, r *http.Request) {
	id, ok := s.toolID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.EnableTool(r.Context(), id, body.Enabled, s.nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteTool(w http.ResponseWriter, r *http.Request) {
	id, ok := s.toolID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}
	if err := s.store.DeleteTool(r.Context(), id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestTool(w http.ResponseWriter, r *http.Request) {
	id, ok := s.toolID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}

	tool, err := s.store.GetTool(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}

	var body struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	args := body.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	timeoutMs := tool.TimeoutMs
	if timeoutMs < 500 {
		timeoutMs = 500
	} else if timeoutMs > 30_000 {
		timeoutMs = 30_000
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	startTime := time.Now()
	res, execErr := s.executeTool(ctx, 0, "admin_test", tool, args)
	durationMs := time.Since(startTime).Milliseconds()

	if execErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"error":      execErr.Error(),
			"durationMs": durationMs,
		})
		return
	}

	cleanResult := SanitizeToolOutput(res)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"result":     json.RawMessage(cleanResult),
		"durationMs": durationMs,
	})
}

// ---------------------------------------------------------------------------
// Admin Member Tools Assignment
// ---------------------------------------------------------------------------

func (s *Server) handleListGroupMemberTools(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	list, err := s.store.ListAllMemberToolsForGroup(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSetMemberTool(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	botID := r.PathValue("botId")
	toolID, err := strconv.ParseInt(r.PathValue("toolId"), 10, 64)
	if err != nil || toolID < 1 {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}

	var body struct {
		Enabled         bool  `json:"enabled"`
		MaxCallsPerTurn int64 `json:"maxCallsPerTurn"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	if err := s.store.SetMemberTool(r.Context(), groupID, botID, toolID, body.Enabled, body.MaxCallsPerTurn); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRemoveMemberTool(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	botID := r.PathValue("botId")
	toolID, err := strconv.ParseInt(r.PathValue("toolId"), 10, 64)
	if err != nil || toolID < 1 {
		writeError(w, http.StatusBadRequest, "invalid tool id")
		return
	}

	if err := s.store.RemoveMemberTool(r.Context(), groupID, botID, toolID); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Admin Tool Calls Audit Log
// ---------------------------------------------------------------------------

func (s *Server) handleListToolCallsAudit(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	limit := int64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	list, err := s.store.ListToolCallsAudit(r.Context(), groupID, limit)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
