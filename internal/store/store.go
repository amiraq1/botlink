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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// OnlineWindowMs mirrors groups.mo onlineWindowNs (45 seconds).
	OnlineWindowMs = int64(45_000)

	DefaultGroupName        = "فريق البوتات"
	DefaultGroupDescription = "مجموعة خاصة لتنسيق المهام وتطوير المشاريع"
	DefaultMaxTurns         = int64(20)
	DefaultMockDelaySec     = int64(12)
	DefaultTurnTimeoutSec   = int64(90)
)

var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("validation error")

	// maxTurnsLimit caps conversation length; configurable at startup.
	maxTurnsLimit = int64(500)
	// messageRetention is the maximum number of messages kept per group.
	messageRetention = int64(2000)
)

func wrapErr(kind error, msg string) error {
	return fmt.Errorf("%w: %s", kind, msg)
}

// SetMaxTurnsLimit overrides the upper bound used by validateMaxTurns.
func SetMaxTurnsLimit(n int64) {
	if n >= 1 {
		maxTurnsLimit = n
	}
}

// SetMessageRetention overrides the per-group message cap.
func SetMessageRetention(n int64) {
	if n >= 1 {
		messageRetention = n
	}
}

// PruneOldMessages keeps storage bounded by dropping messages beyond the
// retention window (oldest first) from every group.
func (s *Store) PruneOldMessages(ctx context.Context) error {
	if messageRetention <= 0 {
		return nil
	}
	ids, err := s.groupIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM messages WHERE group_id = ? AND id NOT IN
			 (SELECT id FROM messages WHERE group_id = ? ORDER BY id DESC LIMIT ?)`,
			id, id, messageRetention); err != nil {
			return err
		}
	}
	return nil
}

func validateMaxTurns(n int64) (int64, error) {
	if n < 1 || n > maxTurnsLimit {
		return 0, wrapErr(ErrValidation,
			fmt.Sprintf("maxTurns must be between 1 and %d", maxTurnsLimit))
	}
	return n, nil
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
	SkipStreak       int64    `json:"skipStreak"`
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
	HasMore  bool      `json:"hasMore"`
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
	// db is the single writer connection. All mutations run through
	// immediateTx on this pool (MaxOpenConns=1), so BEGIN IMMEDIATE never
	// contends with another writer inside the process.
	db *sql.DB
	// ro is a separate read-only pool. WAL lets these readers run while a
	// writer is active, so polling never queues behind a write transaction.
	ro *sql.DB

	presenceMu  sync.Mutex
	presence    map[string]int64
	lastPruneMs int64

	turnTimeoutMs int64
}

const (
	// PresenceWriteInterval is the minimum gap between two database writes
	// of the same member's last_seen_at. 30s stays inside the 45s online
	// window while turning most room GETs into pure reads.
	PresenceWriteInterval = int64(30_000)
	presencePruneInterval = int64(5 * 60_000)
	DefaultReadPoolSize   = 4
)

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
	// Serialize writes: one connection is enough for this workload and
	// keeps BEGIN IMMEDIATE transactions free of busy errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{
		db:            db,
		presence:      map[string]int64{},
		lastPruneMs:   time.Now().UnixMilli(),
		turnTimeoutMs: DefaultTurnTimeoutSec * 1000,
	}
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

	// Read-only pool. modernc.org/sqlite accepts file:<path>?mode=ro.
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	ro.SetMaxOpenConns(DefaultReadPoolSize)
	ro.SetMaxIdleConns(DefaultReadPoolSize)
	for _, p := range []string{"PRAGMA busy_timeout=5000", "PRAGMA query_only=ON"} {
		if _, err := ro.Exec(p); err != nil {
			ro.Close()
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	s.ro = ro
	return s, nil
}

func (s *Store) Close() error {
	if s.ro != nil {
		_ = s.ro.Close()
	}
	return s.db.Close()
}

// touchPresence records a room touch, writing to the database at most once
// per PresenceWriteInterval per token. Entries are pruned by TTL so a flood
// of random tokens cannot grow the map without bound.
func (s *Store) touchPresence(ctx context.Context, token string, nowMs int64) {
	s.presenceMu.Lock()
	last := s.presence[token]
	if nowMs-last < PresenceWriteInterval {
		s.presenceMu.Unlock()
		return
	}
	s.presence[token] = nowMs
	if nowMs-s.lastPruneMs > presencePruneInterval {
		for k, t := range s.presence {
			if nowMs-t > presencePruneInterval {
				delete(s.presence, k)
			}
		}
		s.lastPruneMs = nowMs
	}
	// Only 1..~4 entries can be pruned per call at steady state; cap the
	// map explicitly so an attacker with random tokens cannot exhaust RAM.
	if len(s.presence) > 8192 {
		for k, t := range s.presence {
			if nowMs-t > PresenceWriteInterval {
				delete(s.presence, k)
			}
			if len(s.presence) <= 4096 {
				break
			}
		}
	}
	s.presenceMu.Unlock()

	// Errors are deliberately ignored, as in the previous inline UPDATE:
	// presence is best-effort and must never fail a room read.
	_, _ = s.db.ExecContext(ctx,
		"UPDATE bot_members SET last_seen_at = ? WHERE agent_token = ?", nowMs, token)
}

// SetTurnTimeout configures the sweep timeout for silent real bots.
func (s *Store) SetTurnTimeout(seconds int64) {
	if seconds > 0 {
		s.turnTimeoutMs = seconds * 1000
	}
}

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
  skip_streak INTEGER NOT NULL DEFAULT 0,
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
CREATE TABLE IF NOT EXISTS admin_sessions (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_id, id);
CREATE INDEX IF NOT EXISTS idx_members_group ON bot_members(group_id, bot_id);
`)
	if err != nil {
		return err
	}
	// Existing databases created before the silent-turn circuit breaker need
	// the new column; ALTER is idempotent via a pragma check.
	rows, err := ctxQuery(ctx, s.db, "PRAGMA table_info(groups)")
	if err != nil {
		return err
	}
	hasSkipStreak := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "skip_streak" {
			hasSkipStreak = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !hasSkipStreak {
		if _, err := s.db.ExecContext(ctx,
			"ALTER TABLE groups ADD COLUMN skip_streak INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

// ctxQuery is a tiny helper so migrate can use QueryContext uniformly.
func ctxQuery(ctx context.Context, db *sql.DB, query string) (*sql.Rows, error) {
	return db.QueryContext(ctx, query)
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
	// buildMember is used by both CreateGroup and AddMember, so sanitizing
	// here covers every path that persists a member name or role title.
	rawName := capText(trimText(Sanitize(in.Name)), 40)
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
		RoleTitle:   capText(trimText(Sanitize(in.RoleTitle)), 60),
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
		&g.TurnStartedAt, &g.SkipStreak, &g.CreatedAt, &g.UpdatedAt)
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
	current_turn_bot_id, turn_queue, mock_delay_sec, turn_started_at, skip_streak, created_at, updated_at`

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

// loadMemberByToken returns nil (not an error) for an unknown token.
func loadMemberByToken(ctx context.Context, q querier, token string) (*Member, error) {
	var m Member
	var isMock int
	err := q.QueryRowContext(ctx,
		`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, last_seen_at
		 FROM bot_members WHERE agent_token = ?`, token).
		Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor,
			&isMock, &m.AgentToken, &m.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.IsMock = isMock != 0
	return &m, nil
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

// loadMessagesPage loads at most limit messages. beforeID/afterID are
// cursor-style bounds: beforeID returns older messages (newest of that
// batch first, then reversed), afterID returns newer messages in order.
// A zero value means "no bound".
func loadMessagesPage(ctx context.Context, q querier, groupID, beforeID, afterID, limit int64) ([]Message, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	var query string
	var args []any
	switch {
	case beforeID > 0:
		query = `SELECT id, group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at
			 FROM messages WHERE group_id = ? AND id < ? ORDER BY id DESC LIMIT ?`
		args = []any{groupID, beforeID, limit}
	case afterID > 0:
		query = `SELECT id, group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at
			 FROM messages WHERE group_id = ? AND id > ? ORDER BY id ASC LIMIT ?`
		args = []any{groupID, afterID, limit}
	default:
		query = `SELECT id, group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at
			 FROM (SELECT * FROM messages WHERE group_id = ? ORDER BY id DESC LIMIT ?)
			 ORDER BY id ASC`
		args = []any{groupID, limit}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if beforeID > 0 {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
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
		 turn_seq=?, current_turn_bot_id=?, turn_queue=?, mock_delay_sec=?, turn_started_at=?,
		 skip_streak=?, updated_at=? WHERE id=?`,
		g.Name, g.Description, g.Topic, g.Status, g.TurnMode, g.MaxTurns, g.TurnSeq,
		current, string(queue), g.MockDelaySec, g.TurnStartedAt, g.SkipStreak, g.UpdatedAt, g.ID)
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
