package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleGroupEvents streams coalesced change notifications for one admin
// group. Authentication is enforced by requireAdmin and re-checked on every
// heartbeat, so a revoked session stops receiving events.
func (s *Server) handleGroupEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.groupID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	s.streamEvents(w, r, id, true)
}

// handleRoomEvents streams change notifications for a public agent room. The
// stream carries only the group id: no tokens, no message content.
func (s *Server) handleRoomEvents(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	ip := s.clientIP(r)
	now := time.Now()

	groupID, ok, err := s.store.RoomGroupForToken(r.Context(), token)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if !ok {
		if ok, retry := s.badTokenLimiter.check(ip, now); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusTooManyRequests, "too many bad token attempts, try again later")
			return
		}
		s.badTokenLimiter.hit(ip, now)
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.roomRateOK(w, r, token, false) {
		return
	}
	s.streamEvents(w, r, groupID, false)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, groupID int64, revalidateAdmin bool) {
	sub, ok := s.broker.subscribe(groupID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	defer s.broker.unsubscribe(groupID, sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	writeFrame := func(frame string) bool {
		// Refresh the per-write deadline: this is what lets a stream outlive
		// the server's WriteTimeout.
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
		if _, err := w.Write([]byte(frame)); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
		_ = rc.SetWriteDeadline(time.Time{})
		return true
	}

	readyFrame := fmt.Sprintf(
		"retry: %d\n\nevent: ready\ndata: {\"group\":%d}\n\n", streamRetryMs, groupID)
	if !writeFrame(readyFrame) {
		return
	}
	changeFrame := fmt.Sprintf("event: change\ndata: {\"group\":%d}\n\n", groupID)

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	life := time.NewTimer(streamLifetime)
	defer life.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamCtx.Done():
			return
		case <-sub.closing:
			return
		case <-life.C:
			return
		case <-heartbeat.C:
			if revalidateAdmin && !s.isAuthenticated(r) {
				_ = writeFrame("event: unauthorized\ndata: {}\n\n")
				return
			}
			if !writeFrame(": ping\n\n") {
				return
			}
		case <-sub.events:
			if !writeFrame(changeFrame) {
				return
			}
		}
	}
}
