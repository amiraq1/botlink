package server

// managed.go: admin API for LLM connections (Phase 2).
//
// Scope: connection CRUD, model prices and a bounded GET /models probe.
// No run scheduler, no chat completion and no UI live here yet.
//
// The API never accepts or returns an API key: it is encrypted at rest by the
// store, and the only place it is decrypted is in handleCheckLLMConnection,
// immediately before the fixed-origin provider request. Provider error text is
// mapped to a short safe message; it is never echoed raw and the key is never
// included in a response or a log line.

import (
	"net/http"
	"strconv"

	"github.com/amiraq1/botlink/internal/provider"
	"github.com/amiraq1/botlink/internal/store"
)

func (s *Server) connectionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid connection id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleListLLMConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := s.store.ListConnections(r.Context())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conns)
}

func (s *Server) handleCreateLLMConnection(w http.ResponseWriter, r *http.Request) {
	var in store.LLMConnectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	conn, err := s.store.CreateConnection(r.Context(), in, s.nowMs())
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) handleUpdateLLMConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	var in store.LLMConnectionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.UpdateConnection(r.Context(), id, in, s.nowMs()); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	conn, err := s.store.GetConnection(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conn)
}

func (s *Server) handleDeleteLLMConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteConnection(r.Context(), id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// llmModel is the sanitised, display-only model entry returned by the probe.
type llmModel struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	OwnedBy         string `json:"ownedBy,omitempty"`
	ContextWindow   int64  `json:"contextWindow,omitempty"`
	MaxOutputTokens int64  `json:"maxOutputTokens,omitempty"`
}

type connectionCheckResult struct {
	// Status is "ok" or "error". Provider failures are reported as a result,
	// not as a server error, so the admin UI can show the reason.
	Status string `json:"status"`
	// Models is the catalog returned by GET /models. Its presence only proves
	// that this key can read the model list: it does not validate a chat
	// payload, the account balance or generation.
	Models     []llmModel           `json:"models"`
	Error      string               `json:"error,omitempty"`
	CheckedAt  int64                `json:"checkedAt"`
	Connection *store.LLMConnection `json:"connection"`
}

// handleCheckLLMConnection performs the bounded models probe. Success is
// recorded on the connection and returned with the model list.
func (s *Server) handleCheckLLMConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	conn, err := s.store.GetConnection(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if conn.Provider != store.ProviderDeepSeek {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}
	if !conn.HasKey {
		writeError(w, http.StatusBadRequest, "the connection has no API key")
		return
	}

	key, err := s.store.DecryptConnectionKey(r.Context(), id)
	if err != nil {
		// Decryption failures are internal: never include ciphertext or key
		// material in the response.
		s.log.Printf("llm connection %d key decryption failed: %v", id, err)
		writeError(w, http.StatusInternalServerError, "stored API key could not be read")
		return
	}

	client := provider.NewClient(key, s.llmTransport)
	if s.providerBaseURL != "" {
		client = provider.NewClientAt(s.providerBaseURL, key, s.llmTransport)
	}
	models, checkErr := client.ListModels(r.Context())
	now := s.nowMs()
	result := connectionCheckResult{
		Status:    "ok",
		Models:    make([]llmModel, 0, len(models)),
		CheckedAt: now,
	}
	checkStatus, checkMessage := "ok", ""
	if checkErr != nil {
		result.Status = "error"
		result.Error = provider.ErrorMessage(checkErr)
		checkStatus, checkMessage = "error", result.Error
	} else {
		for _, m := range models {
			result.Models = append(result.Models, llmModel{
				ID:              m.ID,
				Name:            m.Name,
				OwnedBy:         m.OwnedBy,
				ContextWindow:   m.ContextWindow,
				MaxOutputTokens: m.MaxOutputTokens,
			})
		}
	}
	if err := s.store.SetConnectionCheck(r.Context(), id, checkStatus, checkMessage, now); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	updated, err := s.store.GetConnection(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	result.Connection = updated
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// Model prices
// ---------------------------------------------------------------------------

func (s *Server) handleListModelPrices(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetConnection(r.Context(), id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	prices, err := s.store.ListModelPrices(r.Context(), id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prices)
}

func (s *Server) handleSetModelPrice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	var body struct {
		InputMicrosPer1M  int64  `json:"inputMicrosPer1M"`
		OutputMicrosPer1M int64  `json:"outputMicrosPer1M"`
		Currency          string `json:"currency"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	model := r.PathValue("model")
	price := store.ModelPrice{
		ConnectionID:      id,
		Model:             model,
		InputMicrosPer1M:  body.InputMicrosPer1M,
		OutputMicrosPer1M: body.OutputMicrosPer1M,
		Currency:          body.Currency,
	}
	now := s.nowMs()
	if err := s.store.SetModelPrice(r.Context(), price, now); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	saved, err := s.store.GetModelPrice(r.Context(), id, model)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteModelPrice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.connectionID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteModelPrice(r.Context(), id, r.PathValue("model")); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
