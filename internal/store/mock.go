package store

import (
	"context"
	"database/sql"
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
	ms, err := loadMembers(ctx, q, groupID)
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
func processDueMockTx(ctx context.Context, conn *sql.Conn, groupID int64, nowMs int64) error {
	g, speaker, err := dueMockSpeaker(ctx, conn, groupID, nowMs)
	if err != nil || g == nil {
		return err
	}
	ms, err := loadMembers(ctx, conn, groupID)
	if err != nil {
		return err
	}
	msgs, err := loadMessages(ctx, conn, groupID)
	if err != nil {
		return err
	}
	var last *Message
	if len(msgs) > 0 {
		last = &msgs[len(msgs)-1]
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
	return saveGroup(ctx, conn, g)
}

// ProcessDueMock applies a due mock reply for one group (public wrapper).
func (s *Store) ProcessDueMock(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		return processDueMockTx(ctx, conn, groupID, nowMs)
	})
}

// SweepDueMocks applies every due mock reply across all groups — the safety
// net run by the background ticker (Motoko's 15-second recurring timer).
func (s *Store) SweepDueMocks(ctx context.Context, nowMs int64) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM groups")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.ProcessDueMock(ctx, id, nowMs); err != nil {
			return fmt.Errorf("group %d: %w", id, err)
		}
	}
	return nil
}

// ProcessExpiredTurn skips a silent real bot after timeoutMs. The whole
// read-check-rotate sequence runs inside BEGIN IMMEDIATE, so a late reply
// racing the sweeper is either fully accepted or fully rejected.
func (s *Store) ProcessExpiredTurn(ctx context.Context, groupID, nowMs, timeoutMs int64) error {
	if timeoutMs <= 0 {
		timeoutMs = s.turnTimeoutMs
	}
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
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
		ms, err := loadMembers(ctx, conn, groupID)
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
			return saveGroup(ctx, conn, g)
		}
		advanceAfterSkip(g, ms, nowMs)
		return saveGroup(ctx, conn, g)
	})
}

// SweepTurns applies due mock replies, silent-bot timeouts and session
// cleanup. It is the single background tick used by the server.
func (s *Store) SweepTurns(ctx context.Context, nowMs, timeoutMs int64) error {
	ids, err := s.groupIDs(ctx)
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
		record("mock", id, s.ProcessDueMock(ctx, id, nowMs))
		record("timeout", id, s.ProcessExpiredTurn(ctx, id, nowMs, timeoutMs))
	}
	record("sessions", 0, s.DeleteExpiredSessions(ctx, nowMs))
	record("retention", 0, s.PruneOldMessages(ctx))
	return firstErr
}

func (s *Store) groupIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.ro.QueryContext(ctx, "SELECT id FROM groups")
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
