package server

// agent_runs.go: admin API for managed-agent runs (Phase 3).
//
// The runner is the only writer of assistant replies; these handlers only list
// runs, expose the rolling usage/budget state, and let an admin make the two
// explicit decisions the runner never takes automatically: retry one terminal
// attempt of the current turn, or cancel one open attempt. No handler starts a
// run directly, and no response carries message content or key material.

import (
	"net/http"
	"strconv"

	"github.com/amiraq1/botlink/internal/store"
)

func runIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

// loadGroupRun resolves a run and refuses to expose a run from another group
// as if it existed under this one.
func (s *Server) loadGroupRun(w http.ResponseWriter, r *http.Request, groupID, runID int64) (*store.AgentRun, bool) {
	run, err := s.store.GetAgentRun(r.Context(), runID)
	if err != nil {
		s.handleStoreErr(w, err)
		return nil, false
	}
	if run.GroupID != groupID {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return run, true
}

func (s *Server) handleListAgentRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if _, err := s.store.GetGroup(r.Context(), id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	runs, err := s.store.ListAgentRuns(r.Context(), id, 100)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleRetryAgentRun appends one explicit attempt for a terminal run of the
// current turn. The store refuses a moved turn, a disabled agent, the run cap
// and a blocked budget; the runner is woken to pick the new queued attempt.
func (s *Server) handleRetryAgentRun(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	runID, ok := runIDParam(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if _, ok := s.loadGroupRun(w, r, groupID, runID); !ok {
		return
	}
	retried, err := s.store.RetryAgentRun(r.Context(), runID, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.agentRunner.Wake()
	writeJSON(w, http.StatusOK, retried)
}

// handleCancelAgentRun cancels one open attempt. The store transition is the
// authority; the in-memory cancel only stops an already-sent HTTP request.
func (s *Server) handleCancelAgentRun(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	runID, ok := runIDParam(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if _, ok := s.loadGroupRun(w, r, groupID, runID); !ok {
		return
	}
	if err := s.store.CancelAgentRun(r.Context(), runID, s.nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.agentRunner.CancelRun(runID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAgentUsage reports the rolling 24h run count, spend and budget state
// for one managed agent. BudgetBlocked/BudgetBlockReason explain why a turn may
// stay unclaimed even though the group is running.
func (s *Server) handleAgentUsage(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	botID := r.PathValue("botId")
	if botID == "" {
		writeError(w, http.StatusBadRequest, "invalid bot id")
		return
	}
	agent, err := s.store.GetManagedAgent(r.Context(), groupID, botID)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	usage, err := s.store.GetManagedAgentUsage(r.Context(), agent.ID, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
