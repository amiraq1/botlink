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

// processDueMockTx applies a due mock reply for one group: if the current
// speaker is a mock past mockDelaySec, it posts the mock message and
// advances the turn. No-op otherwise.
func processDueMockTx(ctx context.Context, conn *sql.Conn, groupID int64, nowMs int64) error {
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
	if speaker == nil || !speaker.IsMock {
		return nil
	}
	if nowMs-g.TurnStartedAt < g.MockDelaySec*1000 {
		return nil
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
