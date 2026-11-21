// Package store is the SQLite persistence + turn-engine layer for BOTLINK.
// It is a faithful port of src/backend/lib/groups.mo and lib/rooms.mo:
// the same group statuses, turn modes, 45-second presence window, atomic
// turnSeq deduplication and mock-partner behaviour, on top of SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	// OnlineWindowMs mirrors groups.mo onlineWindowNs (45 seconds).
	OnlineWindowMs = int64(45_000)

	DefaultGroupName        = "فريق البوتات"
	DefaultGroupDescription = "مجموعة خاصة لتنسيق المهام وتطوير المشاريع"
	DefaultMaxTurns         = int64(20)
	DefaultMockDelaySec     = int64(12)
)

var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("validation error")
)

func wrapErr(kind error, msg string) error {
	return fmt.Errorf("%w: %s", kind, msg)
}

// ---------------------------------------------------------------------------
// Types (JSON shapes match the old Candid records; timestamps are Unix ms)
// ---------------------------------------------------------------------------

type Group struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Topic            string   `json:"topic"`
	Status           string   `json:"status"` // running | waiting_for_leader | paused | completed | stopped
	TurnMode         string   `json:"turnMode"`
	MaxTurns         int64    `json:"maxTurns"`
	TurnSeq          int64    `json:"turnSeq"`
	CurrentTurnBotID *string  `json:"currentTurnBotId"`
	TurnQueue        []string `json:"turnQueue"`
	MockDelaySec     int64    `json:"mockDelaySec"`
	TurnStartedAt    int64    `json:"turnStartedAt"`
	CreatedAt        int64    `json:"createdAt"`
	UpdatedAt        int64    `json:"updatedAt"`
}

type Member struct {
	GroupID     int64  `json:"groupId"`
	BotID       string `json:"botId"`
	Name        string `json:"name"`
	RoleTitle   string `json:"roleTitle"`
	AccentColor string `json:"accentColor"`
	IsMock      bool   `json:"isMock"`
	AgentToken  string `json:"agentToken"`
	LastSeenAt  int64  `json:"lastSeenAt"`
	Online      bool   `json:"online"`
}

type Message struct {
	ID          int64  `json:"id"`
	GroupID     int64  `json:"groupId"`
	TurnSeq     *int64 `json:"turnSeq"`
	SenderBotID string `json:"senderBotId"`
	SenderName  string `json:"senderName"`
	SenderColor string `json:"senderColor"`
	Role        string `json:"role"` // bot | leader
	Content     string `json:"content"`
	CreatedAt   int64  `json:"createdAt"`
}

type GroupSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Topic       string `json:"topic"`
	Status      string `json:"status"`
	TurnMode    string `json:"turnMode"`
	TurnSeq     int64  `json:"turnSeq"`
	MaxTurns    int64  `json:"maxTurns"`
	MemberCount int64  `json:"memberCount"`
	OnlineCount int64  `json:"onlineCount"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type GroupDetail struct {
	Group    Group     `json:"group"`
	Members  []Member  `json:"members"`
	Messages []Message `json:"messages"`
}

// RoomState is the public agent-room view. It NEVER contains agent tokens.
type RoomState struct {
	Status      string    `json:"status"`
	BotID       string    `json:"botId"`
	BotName     string    `json:"botName"`
	RoleTitle   string    `json:"roleTitle"`
	GroupName   string    `json:"groupName"`
	OnlineNames []string  `json:"onlineNames"`
	TurnSeq     int64     `json:"turnSeq"`
	MaxTurns    int64     `json:"maxTurns"`
	Topic       string    `json:"topic"`
	IsYourTurn  bool      `json:"isYourTurn"`
	Messages    []Message `json:"messages"`
}

type MemberInput struct {
	Name        string `json:"name"`
	RoleTitle   string `json:"roleTitle"`
	AccentColor string `json:"accentColor"`
	IsMock      bool   `json:"isMock"`
}

type CreateGroupInput struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Topic        string        `json:"topic"`
	TurnMode     string        `json:"turnMode"`
	MaxTurns     int64         `json:"maxTurns"`
	MockDelaySec *int64        `json:"mockDelaySec"`
	Members      []MemberInput `json:"members"`
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

type Store struct {
	db *sql.DB
}

// Open creates parent directories, opens (or creates) the SQLite database
// and applies the schema with WAL mode, foreign keys and a busy timeout.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Serialize access: one connection is enough for this workload and
	// keeps BEGIN IMMEDIATE transactions free of busy errors.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'waiting_for_leader',
  turn_mode TEXT NOT NULL DEFAULT 'auto_round_robin',
  max_turns INTEGER NOT NULL DEFAULT 20,
  turn_seq INTEGER NOT NULL DEFAULT 1,
  current_turn_bot_id TEXT,
  turn_queue TEXT NOT NULL DEFAULT '[]',
  mock_delay_sec INTEGER NOT NULL DEFAULT 12,
  turn_started_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS bot_members (
  group_id INTEGER NOT NULL REFERENCES groups(id),
  bot_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  role_title TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '',
  is_mock INTEGER NOT NULL DEFAULT 0,
  agent_token TEXT NOT NULL UNIQUE,
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_id, bot_id)
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES groups(id),
  turn_seq INTEGER,
  sender_bot_id TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  sender_color TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'bot',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_id, id);
CREATE INDEX IF NOT EXISTS idx_members_group ON bot_members(group_id, bot_id);
`)
	return err
}

// immediateTx runs fn inside a SQLite BEGIN IMMEDIATE transaction on a
// dedicated connection, so every turn mutation is atomic.
func (s *Store) immediateTx(ctx context.Context, fn func(conn *sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func NewAgentToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var palette = []string{
	"#3B82F6", "#A855F7", "#EC4899", "#10B981", "#F59E0B",
	"#06B6D4", "#EF4444", "#84CC16", "#8B5CF6", "#14B8A6",
}

func botNum(botID string) int {
	rest, ok := strings.CutPrefix(botID, "bot_")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return n
}

func isHexColor(t string) bool {
	if len(t) != 4 && len(t) != 7 {
		return false
	}
	if t[0] != '#' {
		return false
	}
	for i := 1; i < len(t); i++ {
		c := t[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func trimText(t string) string { return strings.TrimSpace(t) }

func capText(t string, limit int) string {
	rs := []rune(t)
	if len(rs) > limit {
		return string(rs[:limit])
	}
	return t
}

func isOnline(m Member, nowMs int64) bool {
	return m.IsMock || nowMs-m.LastSeenAt <= OnlineWindowMs
}

func sortMembers(ms []Member) {
	sort.SliceStable(ms, func(i, j int) bool { return botNum(ms[i].BotID) < botNum(ms[j].BotID) })
}

func buildMember(groupID int64, num int, in MemberInput, token string) Member {
	rawName := capText(trimText(in.Name), 40)
	name := rawName
	if name == "" {
		name = "Bot " + strconv.Itoa(num)
	}
	color := trimText(in.AccentColor)
	if !isHexColor(color) {
		color = palette[(num+9)%10]
	}
	return Member{
		GroupID:     groupID,
		BotID:       "bot_" + strconv.Itoa(num),
		Name:        name,
		RoleTitle:   capText(trimText(in.RoleTitle), 60),
		AccentColor: color,
		IsMock:      in.IsMock,
		AgentToken:  token,
		LastSeenAt:  0,
	}
}

func (m Member) withOnline(nowMs int64) Member {
	m.Online = isOnline(m, nowMs)
	return m
}

// ---------------------------------------------------------------------------
// Row loading (on a *sql.Conn inside a transaction, or on the *sql.DB)
// ---------------------------------------------------------------------------

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanGroup(row *sql.Row) (*Group, error) {
	var g Group
	var current sql.NullString
	var queue string
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.Topic, &g.Status, &g.TurnMode,
		&g.MaxTurns, &g.TurnSeq, &current, &queue, &g.MockDelaySec,
		&g.TurnStartedAt, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if current.Valid {
		g.CurrentTurnBotID = &current.String
	}
	g.TurnQueue = []string{}
	if queue != "" {
		_ = json.Unmarshal([]byte(queue), &g.TurnQueue)
		if g.TurnQueue == nil {
			g.TurnQueue = []string{}
		}
	}
	return &g, nil
}

const groupCols = `id, name, description, topic, status, turn_mode, max_turns, turn_seq,
	current_turn_bot_id, turn_queue, mock_delay_sec, turn_started_at, created_at, updated_at`

func loadGroup(ctx context.Context, q querier, id int64) (*Group, error) {
	g, err := scanGroup(q.QueryRowContext(ctx,
		"SELECT "+groupCols+" FROM groups WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return g, err
}

func loadMembers(ctx context.Context, q querier, groupID int64) ([]Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, last_seen_at
		 FROM bot_members WHERE group_id = ?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []Member
	for rows.Next() {
		var m Member
		var isMock int
		if err := rows.Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor,
			&isMock, &m.AgentToken, &m.LastSeenAt); err != nil {
			return nil, err
		}
		m.IsMock = isMock != 0
		ms = append(ms, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortMembers(ms)
	if ms == nil {
		ms = []Member{}
	}
	return ms, nil
}

func loadMessages(ctx context.Context, q querier, groupID int64) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at
		 FROM messages WHERE group_id = ? ORDER BY id ASC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var msgs []Message
	for rows.Next() {
		var m Message
		var turnSeq sql.NullInt64
		if err := rows.Scan(&m.ID, &m.GroupID, &turnSeq, &m.SenderBotID, &m.SenderName,
			&m.SenderColor, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		if turnSeq.Valid {
			v := turnSeq.Int64
			m.TurnSeq = &v
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return msgs, nil
}

func saveGroup(ctx context.Context, q querier, g *Group) error {
	queue, _ := json.Marshal(g.TurnQueue)
	if queue == nil {
		queue = []byte("[]")
	}
	var current any
	if g.CurrentTurnBotID != nil {
		current = *g.CurrentTurnBotID
	}
	// querier may be *sql.Conn (ExecContext) or *sql.DB; use an interface check.
	type execer interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}
	ex, ok := q.(execer)
	if !ok {
		return errors.New("store: querier does not support ExecContext")
	}
	_, err := ex.ExecContext(ctx,
		`UPDATE groups SET name=?, description=?, topic=?, status=?, turn_mode=?, max_turns=?,
		 turn_seq=?, current_turn_bot_id=?, turn_queue=?, mock_delay_sec=?, turn_started_at=?, updated_at=?
		 WHERE id=?`,
		g.Name, g.Description, g.Topic, g.Status, g.TurnMode, g.MaxTurns, g.TurnSeq,
		current, string(queue), g.MockDelaySec, g.TurnStartedAt, g.UpdatedAt, g.ID)
	return err
}

func insertMessage(ctx context.Context, q querier, m *Message) (int64, error) {
	type execer interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}
	ex, ok := q.(execer)
	if !ok {
		return 0, errors.New("store: querier does not support ExecContext")
	}
	var turnSeq any
	if m.TurnSeq != nil {
		turnSeq = *m.TurnSeq
	}
	res, err := ex.ExecContext(ctx,
		`INSERT INTO messages (group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.GroupID, turnSeq, m.SenderBotID, m.SenderName, m.SenderColor, m.Role, m.Content, m.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertMember(ctx context.Context, q querier, m *Member) error {
	type execer interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}
	ex, ok := q.(execer)
	if !ok {
		return errors.New("store: querier does not support ExecContext")
	}
	isMock := 0
	if m.IsMock {
		isMock = 1
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO bot_members (group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.GroupID, m.BotID, m.Name, m.RoleTitle, m.AccentColor, isMock, m.AgentToken, m.LastSeenAt)
	return err
}
