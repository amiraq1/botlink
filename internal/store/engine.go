package store

import (
	"context"
	"database/sql"
	"strings"
	"unicode"
)

// This file ports the turn engine of lib/groups.mo and lib/rooms.mo.

// isMentionBoundary treats whitespace and most punctuation as separators so
// "@Ali" never matches "@Ali2". Hyphen and underscore stay inside a name:
// "@Ali-2" and "@Ali_X" must not match a member named "Ali".
func isMentionBoundary(r rune) bool {
	if r == '-' || r == '_' {
		return false
	}
	return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// mentionMatcher lowercases the message once and reuses it for every member,
// so a leader message with N members costs one conversion instead of N.
type mentionMatcher struct {
	text []rune
}

func newMentionMatcher(text string) mentionMatcher {
	return mentionMatcher{text: []rune(strings.ToLower(text))}
}

func (m mentionMatcher) matches(name string) bool {
	if name == "" {
		return false
	}
	target := []rune(strings.ToLower("@" + name))
	rs := m.text
	for i := 0; i+len(target) <= len(rs); i++ {
		matched := true
		for j := range target {
			if rs[i+j] != target[j] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		beforeOK := i == 0 || isMentionBoundary(rs[i-1])
		after := i + len(target)
		afterOK := after == len(rs) || isMentionBoundary(rs[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func mentionsMember(text, name string) bool {
	return newMentionMatcher(text).matches(name)
}

// nextAutoSpeaker returns the next speaker in circular order after `after`
// (the speaker itself is skipped): the first online member; when nobody else
// is online, simply the next member in order. Nil when there are no members.
func nextAutoSpeaker(ms []Member, after *string, nowMs int64) *string {
	n := len(ms)
	if n == 0 {
		return nil
	}
	afterID := ""
	if after != nil {
		afterID = *after
	}
	start := 0
	if afterID != "" {
		afterNum := botNum(afterID)
		for _, m := range ms {
			if botNum(m.BotID) <= afterNum {
				start++
			}
		}
	}
	for k := 0; k < n; k++ {
		candidate := ms[(start+k)%n]
		if candidate.BotID != afterID && isOnline(candidate, nowMs) {
			id := candidate.BotID
			return &id
		}
	}
	id := ms[start%n].BotID
	return &id
}

// popQueue pops the first still-existing botId off the queue.
func popQueue(queue []string, ms []Member) (*string, []string) {
	exists := func(id string) bool {
		for _, m := range ms {
			if m.BotID == id {
				return true
			}
		}
		return false
	}
	for i, id := range queue {
		if exists(id) {
			rest := append([]string{}, queue[i+1:]...)
			v := id
			return &v, rest
		}
	}
	return nil, []string{}
}

func hasMember(ms []Member, botID *string) bool {
	if botID == nil {
		return false
	}
	for _, m := range ms {
		if m.BotID == *botID {
			return true
		}
	}
	return false
}

// advanceAfterReply is the group state after the current speaker has
// delivered a reply (real or mock): turnSeq increments and the next speaker
// / status is chosen according to the turn mode.
func advanceAfterReply(g *Group, ms []Member, nowMs int64) {
	newSeq := g.TurnSeq + 1
	if newSeq > g.MaxTurns {
		g.Status = "completed"
		g.TurnSeq = newSeq
		g.CurrentTurnBotID = nil
		g.TurnQueue = []string{}
		g.TurnStartedAt = nowMs
		g.UpdatedAt = nowMs
		return
	}
	switch g.TurnMode {
	case "auto_round_robin":
		g.Status = "running"
		g.TurnSeq = newSeq
		g.CurrentTurnBotID = nextAutoSpeaker(ms, g.CurrentTurnBotID, nowMs)
		g.TurnQueue = []string{}
		g.TurnStartedAt = nowMs
		g.UpdatedAt = nowMs
	case "leader_round":
		next, rest := popQueue(g.TurnQueue, ms)
		if next != nil {
			g.Status = "running"
			g.TurnSeq = newSeq
			g.CurrentTurnBotID = next
			g.TurnQueue = rest
			g.TurnStartedAt = nowMs
			g.UpdatedAt = nowMs
		} else {
			g.Status = "waiting_for_leader"
			g.TurnSeq = newSeq
			g.CurrentTurnBotID = nil
			g.TurnQueue = []string{}
			g.TurnStartedAt = nowMs
			g.UpdatedAt = nowMs
		}
	}
}

// advanceAfterSkip rotates the turn without consuming a turnSeq, so silent
// bots never exhaust maxTurns or complete a group without any messages.
func advanceAfterSkip(g *Group, ms []Member, nowMs int64) {
	switch g.TurnMode {
	case "auto_round_robin":
		g.CurrentTurnBotID = nextAutoSpeaker(ms, g.CurrentTurnBotID, nowMs)
		g.TurnQueue = []string{}
	case "leader_round":
		next, rest := popQueue(g.TurnQueue, ms)
		if next != nil {
			g.Status = "running"
		} else {
			g.Status = "waiting_for_leader"
		}
		g.CurrentTurnBotID = next
		g.TurnQueue = rest
	}
	g.TurnStartedAt = nowMs
	g.UpdatedAt = nowMs
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func (s *Store) CreateGroup(ctx context.Context, in CreateGroupInput, nowMs int64) (int64, error) {
	if len(in.Members) < 2 || len(in.Members) > 10 {
		return 0, wrapErr(ErrValidation, "A group needs between 2 and 10 members")
	}
	if in.TurnMode != "auto_round_robin" && in.TurnMode != "leader_round" {
		return 0, wrapErr(ErrValidation, "unknown turn mode")
	}
	tokens := make([]string, len(in.Members))
	for i := range tokens {
		t, err := NewAgentToken()
		if err != nil {
			return 0, err
		}
		tokens[i] = t
	}
	var groupID int64
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		name := capText(trimText(Sanitize(in.Name)), 80)
		if name == "" {
			name = DefaultGroupName
		}
		desc := capText(trimText(Sanitize(in.Description)), 300)
		if desc == "" {
			desc = DefaultGroupDescription
		}
		maxTurns := in.MaxTurns
		if maxTurns == 0 {
			maxTurns = DefaultMaxTurns
		}
		validatedTurns, err := validateMaxTurns(maxTurns)
		if err != nil {
			return err
		}
		maxTurns = validatedTurns
		mockDelay := DefaultMockDelaySec
		if in.MockDelaySec != nil {
			mockDelay = *in.MockDelaySec
		}
		status := "running"
		var current *string
		if in.TurnMode == "auto_round_robin" {
			v := "bot_1"
			current = &v
		} else {
			status = "waiting_for_leader"
		}
		res, err := conn.ExecContext(ctx,
			`INSERT INTO groups (name, description, topic, status, turn_mode, max_turns, turn_seq,
			 current_turn_bot_id, turn_queue, mock_delay_sec, turn_started_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, 1, ?, '[]', ?, ?, ?, ?)`,
			name, desc, capText(trimText(Sanitize(in.Topic)), 500), status, in.TurnMode, maxTurns,
			current, mockDelay, nowMs, nowMs, nowMs)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		groupID = id
		for i, mi := range in.Members {
			m := buildMember(id, i+1, mi, tokens[i])
			if err := insertMember(ctx, conn, &m, tokens[i], s.key()); err != nil {
				return err
			}
		}
		noteChange(ctx, groupID)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return groupID, nil
}

func (s *Store) ListGroups(ctx context.Context, nowMs int64) ([]GroupSummary, error) {
	rows, err := s.ro.QueryContext(ctx,
		`SELECT g.id, g.name, g.description, g.topic, g.status, g.turn_mode,
		        g.turn_seq, g.max_turns, g.created_at, g.updated_at,
		        COUNT(m.bot_id) AS member_count,
		        COALESCE(SUM(CASE WHEN m.is_mock = 1 OR ?1 - m.last_seen_at <= ?2 THEN 1 ELSE 0 END), 0) AS online_count
		 FROM groups g
		 LEFT JOIN bot_members m ON m.group_id = g.id
		 GROUP BY g.id
		 ORDER BY g.updated_at DESC, g.id DESC`, nowMs, OnlineWindowMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupSummary
	for rows.Next() {
		var g GroupSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Topic, &g.Status, &g.TurnMode,
			&g.TurnSeq, &g.MaxTurns, &g.CreatedAt, &g.UpdatedAt,
			&g.MemberCount, &g.OnlineCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []GroupSummary{}
	}
	return out, nil
}

// mockDue reports whether the current speaker is a mock whose delay elapsed,
// using already-loaded rows so pollers need no extra queries for the check.
func mockDue(g *Group, ms []Member, nowMs int64) bool {
	if g.Status != "running" || g.CurrentTurnBotID == nil {
		return false
	}
	for _, m := range ms {
		if m.BotID == *g.CurrentTurnBotID {
			return m.IsMock && nowMs-g.TurnStartedAt >= g.MockDelaySec*1000
		}
	}
	return false
}

// GetGroupDetailPaged returns group state plus a bounded slice of messages.
// beforeID loads older history, afterID returns only newer messages for
// delta polling; both are capped at 200.
func (s *Store) GetGroupDetailPaged(ctx context.Context, id, nowMs, beforeID, afterID, limit int64) (*GroupDetail, error) {
	g, err := loadGroup(ctx, s.ro, id)
	if err != nil {
		return nil, err
	}
	ms, err := loadMembers(ctx, s.ro, id, s.key())
	if err != nil {
		return nil, err
	}
	// Load once; only reload when a mock reply actually had to be applied.
	if mockDue(g, ms, nowMs) {
		if err := s.ProcessDueMock(ctx, id, nowMs); err != nil {
			return nil, err
		}
		if g, err = loadGroup(ctx, s.ro, id); err != nil {
			return nil, err
		}
		if ms, err = loadMembers(ctx, s.ro, id, s.key()); err != nil {
			return nil, err
		}
	}
	// Normalize once so the page size used for HasMore is exactly the one
	// passed to SQL (loadMessagesPage would otherwise clamp >200 internally).
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	msgs, err := loadMessagesPage(ctx, s.ro, id, beforeID, afterID, limit)
	if err != nil {
		return nil, err
	}
	s.markPresenceOnline(id, ms, nowMs)
	members := make([]Member, len(ms))
	for i, m := range ms {
		members[i] = m.withOnline(nowMs)
	}
	return &GroupDetail{
		Group: *g, Members: members, Messages: msgs,
		HasMore: len(msgs) >= int(limit),
	}, nil
}

func (s *Store) GetGroupDetail(ctx context.Context, id int64, nowMs int64) (*GroupDetail, error) {
	return s.GetGroupDetailPaged(ctx, id, nowMs, 0, 0, 100)
}

func (s *Store) SendLeaderMessage(ctx context.Context, groupID int64, rawText string, nowMs int64) error {
	text := Sanitize(rawText)
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		switch g.Status {
		case "paused":
			return wrapErr(ErrValidation, "Group is paused: resume it before sending messages")
		case "completed":
			return wrapErr(ErrValidation, "Group has completed")
		case "stopped":
			return wrapErr(ErrValidation, "Group is stopped")
		}
		if text == "" {
			return wrapErr(ErrValidation, "Message is empty")
		}
		if _, err := insertMessage(ctx, conn, &Message{
			GroupID: groupID, TurnSeq: nil, SenderBotID: "leader",
			SenderName: "القائد", SenderColor: "#1D6BFF", Role: "leader",
			Content: text, CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		if g.TurnMode == "leader_round" {
			ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
			if err != nil {
				return err
			}
			matcher := newMentionMatcher(text)
			var mentioned []Member
			for _, m := range ms {
				if matcher.matches(m.Name) {
					mentioned = append(mentioned, m)
				}
			}
			var chosen []Member
			switch {
			case len(mentioned) > 0:
				chosen = mentioned
			default:
				var online []Member
				for _, m := range ms {
					if isOnline(m, nowMs) {
						online = append(online, m)
					}
				}
				if len(online) > 0 {
					chosen = online
				} else {
					chosen = ms
				}
			}
			ids := make([]string, len(chosen))
			for i, m := range chosen {
				ids[i] = m.BotID
			}
			first, rest := popQueue(ids, ms)
			if first != nil {
				g.Status = "running"
			} else {
				g.Status = "waiting_for_leader"
			}
			g.CurrentTurnBotID = first
			g.TurnQueue = rest
			g.TurnStartedAt = nowMs
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) SkipCurrentTurn(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status != "running" {
			return wrapErr(ErrValidation, "Group is not running")
		}
		ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
		if err != nil {
			return err
		}
		advanceAfterSkip(g, ms, nowMs)
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) PauseGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		switch g.Status {
		case "running", "waiting_for_leader":
			g.Status = "paused"
			g.UpdatedAt = nowMs
			return s.saveGroup(ctx, conn, g)
		case "paused":
			return nil
		default:
			return wrapErr(ErrValidation, "Group has "+g.Status)
		}
	})
}

func (s *Store) ResumeGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status != "paused" {
			if g.Status == "completed" || g.Status == "stopped" {
				return wrapErr(ErrValidation, "Group has "+g.Status)
			}
			return nil
		}
		ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
		if err != nil {
			return err
		}
		switch g.TurnMode {
		case "auto_round_robin":
			current := g.CurrentTurnBotID
			if !hasMember(ms, current) {
				current = nextAutoSpeaker(ms, g.CurrentTurnBotID, nowMs)
			}
			g.Status = "running"
			g.CurrentTurnBotID = current
			g.TurnQueue = []string{}
			g.TurnStartedAt = nowMs
		case "leader_round":
			if hasMember(ms, g.CurrentTurnBotID) {
				g.Status = "running"
				g.TurnStartedAt = nowMs
			} else {
				next, rest := popQueue(g.TurnQueue, ms)
				if next != nil {
					g.Status = "running"
				} else {
					g.Status = "waiting_for_leader"
				}
				g.CurrentTurnBotID = next
				g.TurnQueue = rest
				g.TurnStartedAt = nowMs
			}
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) StopGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status == "completed" || g.Status == "stopped" {
			return nil
		}
		g.Status = "stopped"
		g.CurrentTurnBotID = nil
		g.TurnQueue = []string{}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) SetTurnMode(ctx context.Context, groupID int64, mode string, nowMs int64) error {
	if mode != "auto_round_robin" && mode != "leader_round" {
		return wrapErr(ErrValidation, "unknown turn mode")
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.TurnMode == mode {
			return nil
		}
		ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
		if err != nil {
			return err
		}
		switch mode {
		case "leader_round":
			// The current speaker (if any) finishes, then the group waits for the leader.
			if g.Status == "running" && !hasMember(ms, g.CurrentTurnBotID) {
				g.Status = "waiting_for_leader"
			}
			g.TurnMode = mode
			g.TurnQueue = []string{}
		case "auto_round_robin":
			switch g.Status {
			case "running":
				current := g.CurrentTurnBotID
				if !hasMember(ms, current) {
					current = nextAutoSpeaker(ms, nil, nowMs)
				}
				g.TurnMode = mode
				g.CurrentTurnBotID = current
				g.TurnQueue = []string{}
			case "waiting_for_leader":
				g.TurnMode = mode
				g.Status = "running"
				g.CurrentTurnBotID = nextAutoSpeaker(ms, nil, nowMs)
				g.TurnQueue = []string{}
				g.TurnStartedAt = nowMs
			default:
				g.TurnMode = mode
				g.TurnQueue = []string{}
			}
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) SetMaxTurns(ctx context.Context, groupID int64, maxTurns int64, nowMs int64) error {
	maxTurns, err := validateMaxTurns(maxTurns)
	if err != nil {
		return err
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		g.MaxTurns = maxTurns
		if g.TurnSeq > maxTurns {
			g.Status = "completed"
			g.CurrentTurnBotID = nil
			g.TurnQueue = []string{}
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) AddMember(ctx context.Context, groupID int64, in MemberInput, nowMs int64) error {
	token, err := NewAgentToken()
	if err != nil {
		return err
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		ms, err := loadMembers(ctx, conn, groupID, s.key())
		if err != nil {
			return err
		}
		if len(ms) >= 10 {
			return wrapErr(ErrValidation, "A group can have at most 10 members")
		}
		var exists int
		h := HashToken(token)
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM bot_members WHERE token_hash = ?", h).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return wrapErr(ErrValidation, "Token collision, please retry")
		}
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
			return err
		}
		if int(highestMsg) > highest {
			highest = int(highestMsg)
		}
		m := buildMember(groupID, highest+1, in, token)
		if err := insertMember(ctx, conn, &m, token, s.key()); err != nil {
			return err
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

func (s *Store) RemoveMember(ctx context.Context, groupID int64, botID string, nowMs int64) error {
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		ms, err := s.loadMembersLive(ctx, conn, groupID, nowMs)
		if err != nil {
			return err
		}
		if len(ms) <= 2 {
			return wrapErr(ErrValidation, "A group needs at least 2 members")
		}
		var target *Member
		for i := range ms {
			if ms[i].BotID == botID {
				target = &ms[i]
				break
			}
		}
		if target == nil {
			return wrapErr(ErrNotFound, "Member not found")
		}
		if _, err := conn.ExecContext(ctx,
			"DELETE FROM bot_members WHERE group_id = ? AND bot_id = ?", groupID, botID); err != nil {
			return err
		}
		var remaining []Member
		for _, m := range ms {
			if m.BotID != botID {
				remaining = append(remaining, m)
			}
		}
		var queue []string
		for _, b := range g.TurnQueue {
			if b != botID {
				queue = append(queue, b)
			}
		}
		if queue == nil {
			queue = []string{}
		}
		heldTurn := g.CurrentTurnBotID != nil && *g.CurrentTurnBotID == botID
		if !heldTurn {
			g.TurnQueue = queue
			g.UpdatedAt = nowMs
			return s.saveGroup(ctx, conn, g)
		}
		switch g.TurnMode {
		case "auto_round_robin":
			g.CurrentTurnBotID = nextAutoSpeaker(remaining, &botID, nowMs)
			g.TurnQueue = []string{}
			g.TurnStartedAt = nowMs
		case "leader_round":
			next, rest := popQueue(queue, remaining)
			if g.Status == "running" {
				if next != nil {
					g.Status = "running"
				} else {
					g.Status = "waiting_for_leader"
				}
			}
			g.CurrentTurnBotID = next
			g.TurnQueue = rest
			g.TurnStartedAt = nowMs
		}
		g.UpdatedAt = nowMs
		return s.saveGroup(ctx, conn, g)
	})
}

// RotateToken issues a fresh 128-bit agent token for one member and returns it.
func (s *Store) RotateToken(ctx context.Context, groupID int64, botID string, nowMs int64) (string, error) {
	token, err := NewAgentToken()
	if err != nil {
		return "", err
	}
	h := HashToken(token)
	enc, err := EncryptToken(s.key(), token, MemberAAD(groupID, botID))
	if err != nil {
		return "", err
	}
	err = s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status == "completed" || g.Status == "stopped" {
			return wrapErr(ErrValidation, "Cannot rotate token on a "+g.Status+" group")
		}
		res, err := conn.ExecContext(ctx,
			"UPDATE bot_members SET token_hash = ?, token_enc = ? WHERE group_id = ? AND bot_id = ?",
			h, enc, groupID, botID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return wrapErr(ErrNotFound, "Member not found")
		}
		if _, err = conn.ExecContext(ctx,
			"UPDATE groups SET updated_at = ? WHERE id = ?", nowMs, groupID); err != nil {
			return err
		}
		noteChange(ctx, groupID)
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ---------------------------------------------------------------------------
// Public room
// ---------------------------------------------------------------------------

// GetRoomState is a read-only poll in the common case. Presence is queued
// for the background flusher and unknown tokens never touch the writer.
func (s *Store) GetRoomState(ctx context.Context, token string, nowMs int64) (*RoomState, error) {
	m, err := loadMemberByToken(ctx, s.ro, token, s.key())
	if err != nil || m == nil {
		return nil, err // unknown token -> nil state, like Motoko
	}
	s.touchPresence(*m, nowMs)
	g, err := loadGroup(ctx, s.ro, m.GroupID)
	if err != nil {
		return nil, err
	}
	ms, err := loadMembersLite(ctx, s.ro, m.GroupID)
	if err != nil {
		return nil, err
	}
	if mockDue(g, ms, nowMs) {
		if err := s.ProcessDueMock(ctx, m.GroupID, nowMs); err != nil {
			return nil, err
		}
		if g, err = loadGroup(ctx, s.ro, m.GroupID); err != nil {
			return nil, err
		}
		if ms, err = loadMembersLite(ctx, s.ro, m.GroupID); err != nil {
			return nil, err
		}
	}
	online := s.onlinePresence(m.GroupID, nowMs)
	var onlineNames []string
	for _, x := range ms {
		if isOnline(x, nowMs) || online[x.BotID] {
			onlineNames = append(onlineNames, x.Name)
		}
	}
	if onlineNames == nil {
		onlineNames = []string{}
	}
	// Last 15, oldest first.
	msgs, err := loadMessagesPage(ctx, s.ro, m.GroupID, 0, 0, 15)
	if err != nil {
		return nil, err
	}
	yourTurn := g.Status == "running" && g.CurrentTurnBotID != nil && *g.CurrentTurnBotID == m.BotID
	return &RoomState{
		Status: g.Status, BotID: m.BotID, BotName: m.Name, RoleTitle: m.RoleTitle,
		GroupName: g.Name, OnlineNames: onlineNames, TurnSeq: g.TurnSeq,
		MaxTurns: g.MaxTurns, Topic: g.Topic, IsYourTurn: yourTurn, Messages: msgs,
	}, nil
}

// SubmitBotReply is the atomic turnSeq deduplication entry point: stale,
// duplicate, wrong-turn, non-running or mock submits return accepted=false
// with HTTP 200 semantics (no error), exactly like Motoko.
func (s *Store) SubmitBotReply(ctx context.Context, token string, expectedTurnSeq int64, rawContent string, nowMs int64) (bool, error) {
	accepted := false
	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		m, err := loadMemberByToken(ctx, conn, token, s.key())
		if err != nil || m == nil {
			return err
		}
		h := HashToken(token)
		_, _ = conn.ExecContext(ctx,
			"UPDATE bot_members SET last_seen_at = ? WHERE token_hash = ?", nowMs, h)
		g, err := loadGroup(ctx, conn, m.GroupID)
		if err != nil {
			return err
		}
		if m.IsMock || g.Status != "running" {
			return nil
		}
		if g.CurrentTurnBotID == nil || *g.CurrentTurnBotID != m.BotID {
			return nil
		}
		if g.TurnSeq != expectedTurnSeq {
			return nil
		}
		content := Sanitize(rawContent)
		if content == "" {
			return nil
		}
		seq := g.TurnSeq
		if _, err := insertMessage(ctx, conn, &Message{
			GroupID: m.GroupID, TurnSeq: &seq, SenderBotID: m.BotID,
			SenderName: m.Name, SenderColor: m.AccentColor, Role: "bot",
			Content: content, CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		ms, err := s.loadMembersLive(ctx, conn, m.GroupID, nowMs)
		if err != nil {
			return err
		}
		g.SkipStreak = 0
		advanceAfterReply(g, ms, nowMs)
		if err := s.saveGroup(ctx, conn, g); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}
