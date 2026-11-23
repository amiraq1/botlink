package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Mock-partner auto-replies, ported from lib/rooms.mo.

func quoteOf(content string) string {
	flat := strings.ReplaceAll(content, "\n", " ")
	rs := []rune(flat)
	if len(rs) > 80 {
		return string(rs[:80]) + "…"
	}
	return flat
}

func mockText(speaker Member, last *Message, turn int64) string {
	tag := speaker.Name
	if speaker.RoleTitle != "" {
		tag = speaker.Name + " (" + speaker.RoleTitle + ")"
	}
	if last == nil {
		return "[رد تلقائي] " + tag + ": مرحبًا بالجميع، أنا جاهز للبدء. من يقترح الخطوة الأولى؟\n" +
			"[Auto-reply] " + tag + ": Hello team, I'm ready to start. Who proposes the first step?"
	}
	q := quoteOf(last.Content)
	who := last.SenderName
	switch turn % 3 {
	case 0:
		return "[رد تلقائي] " + tag + ": بخصوص ما قاله " + who + " \"" + q + "\" — أتفق مع هذه النقطة وأقترح أن نحدد الخطوة التالية بوضوح.\n" +
			"[Auto-reply] " + tag + ": On " + who + "'s point \"" + q + "\" — I agree, and I suggest we define the next concrete step."
	case 1:
		return "[رد تلقائي] " + tag + ": تعليقًا على \"" + q + "\" — فكرة جيدة، لكن ما المخاطر التي يجب أن ننتبه لها؟\n" +
			"[Auto-reply] " + tag + ": About \"" + q + "\" — good idea, but which risks should we watch for?"
	default:
		return "[رد تلقائي] " + tag + ": ردًا على " + who + " (\"" + q + "\") — سأتولى الجزء الخاص بي وأعود بالنتيجة.\n" +
			"[Auto-reply] " + tag + ": In reply to " + who + " (\"" + q + "\") — I'll take my part and report back."
	}
}

// dueMockSpeaker finds a mock speaker whose delay has elapsed. It is used
// both inside the write transaction and by read-only pollers, so a GET can
// avoid taking a write lock unless a reply is actually due.
func dueMockSpeaker(ctx context.Context, q querier, groupID, nowMs int64) (*Group, *Member, error) {
	g, err := loadGroup(ctx, q, groupID)
	if err != nil {
		if err == ErrNotFound {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if g.Status != "running" || g.CurrentTurnBotID == nil {
		return nil, nil, nil
	}
	ms, err := loadMembersLite(ctx, q, groupID)
	if err != nil {
		return nil, nil, err
	}
	var speaker *Member
	for i := range ms {
		if ms[i].BotID == *g.CurrentTurnBotID {
			speaker = &ms[i]
			break
		}
	}
	if speaker == nil || !speaker.IsMock {
		return nil, nil, nil
	}
	if nowMs-g.TurnStartedAt < g.MockDelaySec*1000 {
		return nil, nil, nil
	}
	return g, speaker, nil
}

// processDueMockTx applies a due mock reply for one group: if the current
// speaker is a mock past mockDelaySec, it posts the mock message and
// advances the turn. No-op otherwise.
func (s *Store) processDueMockTx(ctx context.Context, conn *sql.Conn, groupID int64, nowMs int64) error {
	g, speaker, err := dueMockSpeaker(ctx, conn, groupID, nowMs)
	if err != nil || g == nil {
		return err
	}
	ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
	if err != nil {
		return err
	}
	last, err := loadLastMessage(ctx, conn, groupID)
	if err != nil {
		return err
	}
	seq := g.TurnSeq
	if _, err := insertMessage(ctx, conn, &Message{
		GroupID: groupID, TurnSeq: &seq, SenderBotID: speaker.BotID,
		SenderName: speaker.Name, SenderColor: speaker.AccentColor, Role: "bot",
		Content: mockText(*speaker, last, g.TurnSeq), CreatedAt: nowMs,
	}); err != nil {
		return err
	}
	g.SkipStreak = 0
	advanceAfterReply(g, ms, nowMs)
	return s.saveGroup(ctx, conn, g)
}

// ProcessDueMock applies a due mock reply for one group (public wrapper).
func (s *Store) ProcessDueMock(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return s.processDueMockTx(ctx, conn, groupID, nowMs)
	})
}

// DueState is the next automatic action for a running group: a mock reply
// after mockDelaySec, or a silent-bot timeout after turnTimeoutSec.
type DueState struct {
	Kind  string // "" | "mock" | "timeout"
	DueAt int64
}

// NextDue reports when the current turn becomes due, using only the read
// pool: the group row plus the current speaker's is_mock flag.
func (s *Store) NextDue(ctx context.Context, groupID, nowMs, timeoutMs int64) (DueState, error) {
	if timeoutMs <= 0 {
		timeoutMs = s.turnTimeoutMs
	}
	g, err := loadGroup(ctx, s.ro, groupID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return DueState{}, nil
		}
		return DueState{}, err
	}
	if g.Status != "running" || g.CurrentTurnBotID == nil {
		return DueState{}, nil
	}
	var isMock int
	err = s.ro.QueryRowContext(ctx,
		"SELECT is_mock FROM bot_members WHERE group_id = ? AND bot_id = ?",
		groupID, *g.CurrentTurnBotID).Scan(&isMock)
	if errors.Is(err, sql.ErrNoRows) {
		return DueState{}, nil
	}
	if err != nil {
		return DueState{}, err
	}
	if isMock != 0 {
		return DueState{Kind: "mock", DueAt: g.TurnStartedAt + g.MockDelaySec*1000}, nil
	}
	if timeoutMs <= 0 {
		return DueState{}, nil
	}
	return DueState{Kind: "timeout", DueAt: g.TurnStartedAt + timeoutMs}, nil
}

// ProcessDueGroup applies the due action for one group without taking the
// write lock when nothing is due. The boolean reports whether the due window
// was reached (the scheduler uses it to back off if the write was a no-op).
func (s *Store) ProcessDueGroup(ctx context.Context, groupID, nowMs, timeoutMs int64) (bool, error) {
	due, err := s.NextDue(ctx, groupID, nowMs, timeoutMs)
	if err != nil || due.Kind == "" || nowMs < due.DueAt {
		return false, err
	}
	if due.Kind == "mock" {
		return true, s.ProcessDueMock(ctx, groupID, nowMs)
	}
	return true, s.ProcessExpiredTurn(ctx, groupID, nowMs, timeoutMs)
}

// RunningGroupIDs lists groups whose turn can expire, using idx_groups_status.
func (s *Store) RunningGroupIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.ro.QueryContext(ctx, "SELECT id FROM groups WHERE status = 'running'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RoomGroupForToken resolves a public agent token to its group without
// loading any member fields (and never returning the token itself).
func (s *Store) RoomGroupForToken(ctx context.Context, token string) (int64, bool, error) {
	var groupID int64
	h := HashToken(token)
	err := s.ro.QueryRowContext(ctx,
		"SELECT group_id FROM bot_members WHERE token_hash = ? OR agent_token = ?", h, token).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return groupID, true, nil
}

// ProcessExpiredTurn skips a silent real bot after timeoutMs. The whole
// read-check-rotate sequence runs inside BEGIN IMMEDIATE, so a late reply
// racing the sweeper is either fully accepted or fully rejected.
func (s *Store) ProcessExpiredTurn(ctx context.Context, groupID, nowMs, timeoutMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		return s.processExpiredTurnTx(ctx, conn, groupID, nowMs, timeoutMs)
	})
}

func (s *Store) processExpiredTurnTx(ctx context.Context, conn *sql.Conn, groupID, nowMs, timeoutMs int64) error {
	if timeoutMs <= 0 {
		timeoutMs = s.turnTimeoutMs
	}
	g, err := loadGroup(ctx, conn, groupID)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	if g.Status != "running" || g.CurrentTurnBotID == nil {
		return nil
	}
	ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
	if err != nil {
		return err
	}
	var speaker *Member
	for i := range ms {
		if ms[i].BotID == *g.CurrentTurnBotID {
			speaker = &ms[i]
			break
		}
	}
	// Mock speakers have their own short delay and are handled by
	// processDueMockTx; only real bots need the long timeout.
	if speaker == nil || speaker.IsMock {
		return nil
	}
	if nowMs-g.TurnStartedAt < timeoutMs {
		return nil
	}

	// CAS: re-read happens under the write lock above; capture the
	// values so a late reply with the old turnSeq can no longer win.
	seq := g.TurnSeq
	g.SkipStreak++
	if g.SkipStreak >= int64(len(ms)) {
		g.Status = "paused"
		g.CurrentTurnBotID = nil
		g.TurnQueue = []string{}
		g.TurnStartedAt = nowMs
		var names strings.Builder
		for i, m := range ms {
			if i > 0 {
				names.WriteString("، ")
			}
			names.WriteString(m.Name)
		}
		event := "إيقاف تلقائي: لم يرد أي عضو بعد انتهاء مهلات متتالية. الأعضاء: " + names.String()
		if _, err := insertMessage(ctx, conn, &Message{
			GroupID: groupID, TurnSeq: &seq, SenderBotID: "system",
			SenderName: "النظام", SenderColor: "#6B7280", Role: "bot",
			Content: event, CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	}
	advanceAfterSkip(g, ms, nowMs)
	return s.saveGroup(ctx, conn, g)
}

// SweepTurns is the 15-second safety net: due mock replies, silent-bot
// timeouts, expired sessions and message retention. Due checks are read-only,
// so idle groups never take a write transaction.
func (s *Store) SweepTurns(ctx context.Context, nowMs, timeoutMs int64) error {
	ids, err := s.RunningGroupIDs(ctx)
	if err != nil {
		return err
	}
	// A failure in one group must not skip the remaining groups in this tick.
	var firstErr error
	record := func(label string, id int64, err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s group %d: %w", label, id, err)
		}
	}
	for _, id := range ids {
		_, err := s.ProcessDueGroup(ctx, id, nowMs, timeoutMs)
		record("due", id, err)
	}
	record("sessions", 0, s.DeleteExpiredSessions(ctx, nowMs))
	record("retention", 0, s.PruneOldMessages(ctx))
	return firstErr
}
