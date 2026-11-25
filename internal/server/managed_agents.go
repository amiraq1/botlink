package server

// managed_agents.go: admin API for managed agents (Phase 4).

import (
	"net/http"

	"github.com/amiraq1/botlink/internal/store"
)

type createManagedAgentRequest struct {
	Member store.MemberInput       `json:"member"`
	Agent  store.ManagedAgentInput `json:"agent"`
}

func (s *Server) handleListManagedAgents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	if _, err := s.store.GetGroup(r.Context(), id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	agents, err := s.store.ListManagedAgents(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleCreateManagedAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	var req createManagedAgentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	agent, err := s.store.CreateManagedAgent(r.Context(), id, req.Member, req.Agent, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleUpdateManagedAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	botID := r.PathValue("botId")
	if botID == "" {
		writeError(w, http.StatusBadRequest, "invalid bot id")
		return
	}
	var in store.ManagedAgentInput
	if !decodeJSON(w, r, &in) {
		return
	}
	agent, err := s.store.UpdateManagedAgent(r.Context(), id, botID, in, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleDeleteManagedAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	botID := r.PathValue("botId")
	if botID == "" {
		writeError(w, http.StatusBadRequest, "invalid bot id")
		return
	}
	if err := s.store.DeleteManagedAgent(r.Context(), id, botID, s.nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
