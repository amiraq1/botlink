package store

import (
	"context"
	"database/sql"
	"strings"
)

// This file ports the turn engine of lib/groups.mo and lib/rooms.mo.

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
	err := s.immediateTx(ctx, func(conn *sql.Conn) error {
		name := capText(trimText(in.Name), 80)
		if name == "" {
			name = DefaultGroupName
		}
		desc := capText(trimText(in.Description), 300)
		if desc == "" {
			desc = DefaultGroupDescription
		}
		maxTurns := in.MaxTurns
		if maxTurns == 0 {
			maxTurns = DefaultMaxTurns
		}
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
			name, desc, capText(trimText(in.Topic), 500), status, in.TurnMode, maxTurns,
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
			if err := insertMember(ctx, conn, &m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return groupID, nil
}

func (s *Store) ListGroups(ctx context.Context, nowMs int64) ([]GroupSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, topic, status, turn_mode, turn_seq, max_turns, created_at, updated_at
		 FROM groups ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupSummary
	for rows.Next() {
		var g GroupSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Topic, &g.Status, &g.TurnMode,
			&g.TurnSeq, &g.MaxTurns, &g.CreatedAt, &g.UpdatedAt); err != nil {
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
	// Member / online counts per group.
	for i := range out {
		ms, err := loadMembers(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].MemberCount = int64(len(ms))
		var online int64
		for _, m := range ms {
			if isOnline(m, nowMs) {
				online++
			}
		}
		out[i].OnlineCount = online
	}
	return out, nil
}

// getGroupDetailTx loads a detail inside a transaction after applying a due
// mock reply (mirrors the Motoko update-call behaviour).
func (s *Store) getGroupDetailTx(ctx context.Context, conn *sql.Conn, id int64, nowMs int64) (*GroupDetail, error) {
	if err := processDueMockTx(ctx, conn, id, nowMs); err != nil {
		return nil, err
	}
	g, err := loadGroup(ctx, conn, id)
	if err != nil {
		return nil, err
	}
	ms, err := loadMembers(ctx, conn, id)
	if err != nil {
		return nil, err
	}
	msgs, err := loadMessages(ctx, conn, id)
	if err != nil {
		return nil, err
	}
	members := make([]Member, len(ms))
	for i, m := range ms {
		members[i] = m.withOnline(nowMs)
	}
	return &GroupDetail{Group: *g, Members: members, Messages: msgs}, nil
}

func (s *Store) GetGroupDetail(ctx context.Context, id int64, nowMs int64) (*GroupDetail, error) {
	var detail *GroupDetail
	err := s.immediateTx(ctx, func(conn *sql.Conn) error {
		d, err := s.getGroupDetailTx(ctx, conn, id, nowMs)
		if err != nil {
			return err
		}
		detail = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Store) SendLeaderMessage(ctx context.Context, groupID int64, rawText string, nowMs int64) error {
	text := Sanitize(rawText)
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
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
			ms, err := loadMembers(ctx, conn, groupID)
			if err != nil {
				return err
			}
			lowered := strings.ToLower(text)
			var mentioned []Member
			for _, m := range ms {
				if m.Name != "" && strings.Contains(lowered, "@"+strings.ToLower(m.Name)) {
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
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) SkipCurrentTurn(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.Status != "running" {
			return wrapErr(ErrValidation, "Group is not running")
		}
		ms, err := loadMembers(ctx, conn, groupID)
		if err != nil {
			return err
		}
		switch g.TurnMode {
		case "auto_round_robin":
			g.CurrentTurnBotID = nextAutoSpeaker(ms, g.CurrentTurnBotID, nowMs)
			g.TurnQueue = []string{}
			g.TurnStartedAt = nowMs
		case "leader_round":
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
		g.UpdatedAt = nowMs
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) PauseGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		switch g.Status {
		case "running", "waiting_for_leader":
			g.Status = "paused"
			g.UpdatedAt = nowMs
			return saveGroup(ctx, conn, g)
		case "paused":
			return nil
		default:
			return wrapErr(ErrValidation, "Group has "+g.Status)
		}
	})
}

func (s *Store) ResumeGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
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
		ms, err := loadMembers(ctx, conn, groupID)
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
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) StopGroup(ctx context.Context, groupID int64, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
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
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) SetTurnMode(ctx context.Context, groupID int64, mode string, nowMs int64) error {
	if mode != "auto_round_robin" && mode != "leader_round" {
		return wrapErr(ErrValidation, "unknown turn mode")
	}
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if g.TurnMode == mode {
			return nil
		}
		ms, err := loadMembers(ctx, conn, groupID)
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
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) SetMaxTurns(ctx context.Context, groupID int64, maxTurns int64, nowMs int64) error {
	if maxTurns < 1 {
		return wrapErr(ErrValidation, "maxTurns must be at least 1")
	}
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
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
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) AddMember(ctx context.Context, groupID int64, in MemberInput, nowMs int64) error {
	token, err := NewAgentToken()
	if err != nil {
		return err
	}
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		ms, err := loadMembers(ctx, conn, groupID)
		if err != nil {
			return err
		}
		if len(ms) >= 10 {
			return wrapErr(ErrValidation, "A group can have at most 10 members")
		}
		var exists int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM bot_members WHERE agent_token = ?", token).Scan(&exists); err != nil {
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
		msgs, err := loadMessages(ctx, conn, groupID)
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			if n := botNum(msg.SenderBotID); n > highest {
				highest = n
			}
		}
		m := buildMember(groupID, highest+1, in, token)
		if err := insertMember(ctx, conn, &m); err != nil {
			return err
		}
		g.UpdatedAt = nowMs
		return saveGroup(ctx, conn, g)
	})
}

func (s *Store) RemoveMember(ctx context.Context, groupID int64, botID string, nowMs int64) error {
	return s.immediateTx(ctx, func(conn *sql.Conn) error {
		g, err := loadGroup(ctx, conn, groupID)
		if err != nil {
			return err
		}
		ms, err := loadMembers(ctx, conn, groupID)
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
			return saveGroup(ctx, conn, g)
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
		return saveGroup(ctx, conn, g)
	})
}

// RotateToken issues a fresh 128-bit agent token for one member and returns it.
func (s *Store) RotateToken(ctx context.Context, groupID int64, botID string, nowMs int64) (string, error) {
	token, err := NewAgentToken()
	if err != nil {
		return "", err
	}
	err = s.immediateTx(ctx, func(conn *sql.Conn) error {
		if _, err := loadGroup(ctx, conn, groupID); err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx,
			"UPDATE bot_members SET agent_token = ? WHERE group_id = ? AND bot_id = ?",
			token, groupID, botID)
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
		_, err = conn.ExecContext(ctx,
			"UPDATE groups SET updated_at = ? WHERE id = ?", nowMs, groupID)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ---------------------------------------------------------------------------
// Public room
// ---------------------------------------------------------------------------

func (s *Store) GetRoomState(ctx context.Context, token string, nowMs int64) (*RoomState, error) {
	var state *RoomState
	err := s.immediateTx(ctx, func(conn *sql.Conn) error {
		// Touch: refresh lastSeenAt (no-op for an unknown token).
		_, _ = conn.ExecContext(ctx,
			"UPDATE bot_members SET last_seen_at = ? WHERE agent_token = ?", nowMs, token)
		var m Member
		var isMock int
		err := conn.QueryRowContext(ctx,
			`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, last_seen_at
			 FROM bot_members WHERE agent_token = ?`, token).
			Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor, &isMock, &m.AgentToken, &m.LastSeenAt)
		if err == sql.ErrNoRows {
			return nil // unknown token -> nil state, like Motoko
		}
		if err != nil {
			return err
		}
		m.IsMock = isMock != 0
		if err := processDueMockTx(ctx, conn, m.GroupID, nowMs); err != nil {
			return err
		}
		g, err := loadGroup(ctx, conn, m.GroupID)
		if err != nil {
			return err
		}
		ms, err := loadMembers(ctx, conn, m.GroupID)
		if err != nil {
			return err
		}
		var onlineNames []string
		for _, x := range ms {
			if isOnline(x, nowMs) {
				onlineNames = append(onlineNames, x.Name)
			}
		}
		if onlineNames == nil {
			onlineNames = []string{}
		}
		msgs, err := loadMessages(ctx, conn, m.GroupID)
		if err != nil {
			return err
		}
		// Last 15, oldest first.
		if len(msgs) > 15 {
			msgs = msgs[len(msgs)-15:]
		}
		yourTurn := g.Status == "running" && g.CurrentTurnBotID != nil && *g.CurrentTurnBotID == m.BotID
		state = &RoomState{
			Status: g.Status, BotID: m.BotID, BotName: m.Name, RoleTitle: m.RoleTitle,
			GroupName: g.Name, OnlineNames: onlineNames, TurnSeq: g.TurnSeq,
			MaxTurns: g.MaxTurns, Topic: g.Topic, IsYourTurn: yourTurn, Messages: msgs,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// SubmitBotReply is the atomic turnSeq deduplication entry point: stale,
// duplicate, wrong-turn, non-running or mock submits return accepted=false
// with HTTP 200 semantics (no error), exactly like Motoko.
func (s *Store) SubmitBotReply(ctx context.Context, token string, expectedTurnSeq int64, rawContent string, nowMs int64) (bool, error) {
	accepted := false
	err := s.immediateTx(ctx, func(conn *sql.Conn) error {
		_, _ = conn.ExecContext(ctx,
			"UPDATE bot_members SET last_seen_at = ? WHERE agent_token = ?", nowMs, token)
		var m Member
		var isMock int
		err := conn.QueryRowContext(ctx,
			`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, last_seen_at
			 FROM bot_members WHERE agent_token = ?`, token).
			Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor, &isMock, &m.AgentToken, &m.LastSeenAt)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		m.IsMock = isMock != 0
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
		ms, err := loadMembers(ctx, conn, m.GroupID)
		if err != nil {
			return err
		}
		advanceAfterReply(g, ms, nowMs)
		if err := saveGroup(ctx, conn, g); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}
