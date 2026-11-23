// Package store is the SQLite persistence + turn-engine layer for BOTLINK.
// It is a faithful port of src/backend/lib/groups.mo and lib/rooms.mo:
// the same group statuses, turn modes, 45-second presence window, atomic
// turnSeq deduplication and mock-partner behaviour, on top of SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
// retention window (oldest first) from every group. It skips the scan
// entirely when no message was inserted since the previous prune and uses a
// single window-function statement instead of one delete per group.
func (s *Store) PruneOldMessages(ctx context.Context) error {
	if messageRetention <= 0 {
		return nil
	}
	var maxID int64
	if err := s.ro.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM messages").Scan(&maxID); err != nil {
		return err
	}
	if maxID == s.lastPruneMaxID.Load() {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM messages WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY group_id ORDER BY id DESC) AS rn
				FROM messages
			) WHERE rn > ?
		)`, messageRetention); err != nil {
		return err
	}
	s.lastPruneMaxID.Store(maxID)
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
	TokenHash   string `json:"-"`
	TokenEnc    string `json:"-"`
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

type presenceEntry struct {
	at      int64
	groupID int64
	botID   string
}

type presenceTouch struct {
	token string
	nowMs int64
}

type Store struct {
	dbPath string

	// db is the single writer connection. All mutations run through
	// immediateTx on this pool (MaxOpenConns=1), so BEGIN IMMEDIATE never
	// contends with another writer inside the process.
	db *sql.DB
	// ro is a separate read-only pool. WAL lets these readers run while a
	// writer is active, so polling never queues behind a write transaction.
	ro *sql.DB

	encryptionKey []byte

	presenceMu   sync.Mutex
	presence     map[string]presenceEntry
	lastPruneMs  int64
	presenceCh   chan presenceTouch
	presenceStop chan struct{}
	presenceWG   sync.WaitGroup
	closeOnce    sync.Once

	lastPruneMaxID atomic.Int64

	changeMu sync.Mutex
	onChange func(int64)

	turnTimeoutMs int64
}

// SetEncryptionKey sets the AES-256-GCM encryption key used for securing agent tokens at rest.
func (s *Store) SetEncryptionKey(key []byte) {
	if len(key) >= 32 {
		s.encryptionKey = append([]byte(nil), key[:32]...)
	}
}

func (s *Store) key() []byte {
	if len(s.encryptionKey) >= 32 {
		return s.encryptionKey
	}
	defaultKey := sha256.Sum256([]byte("botlink-default-encryption-key-for-dev"))
	return defaultKey[:]
}

const (
	// PresenceWriteInterval is the minimum gap between two database writes
	// of the same member's last_seen_at. 30s stays inside the 45s online
	// window while turning almost every room GET into a pure read.
	PresenceWriteInterval = int64(30_000)
	presencePruneInterval = int64(5 * 60_000)
	presenceFlushInterval = time.Second
	presenceQueueSize     = 1024
	DefaultReadPoolSize   = 4
	maxReadPoolSize       = 16
)

// SetChangeHook registers a callback invoked after a group row is saved, so
// the HTTP layer can push live events and wake its turn scheduler.
func (s *Store) SetChangeHook(fn func(groupID int64)) {
	s.changeMu.Lock()
	s.onChange = fn
	s.changeMu.Unlock()
}

func (s *Store) notifyGroup(id int64) {
	s.changeMu.Lock()
	fn := s.onChange
	s.changeMu.Unlock()
	if fn != nil {
		fn(id)
	}
}

func readPoolSize() int {
	n := runtime.NumCPU()
	if n < DefaultReadPoolSize {
		n = DefaultReadPoolSize
	}
	if n > maxReadPoolSize {
		n = maxReadPoolSize
	}
	return n
}

// Open creates parent directories, opens (or creates) the SQLite database
// and applies the schema with WAL mode, foreign keys and a busy timeout.
// An optional encryption key (at least 32 bytes) can be provided for token encryption.
func Open(path string, encryptionKey ...[]byte) (*Store, error) {
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
		dbPath:        path,
		db:            db,
		presence:      map[string]presenceEntry{},
		presenceCh:    make(chan presenceTouch, presenceQueueSize),
		presenceStop:  make(chan struct{}),
		lastPruneMs:   time.Now().UnixMilli(),
		turnTimeoutMs: DefaultTurnTimeoutSec * 1000,
	}
	if len(encryptionKey) > 0 && len(encryptionKey[0]) > 0 {
		s.SetEncryptionKey(encryptionKey[0])
	}
	// MaxOpenConns=1 pins every PRAGMA below to the single writer conn.
	// Read-side tuning (temp_store, cache_size, mmap) travels in the read DSN:
	// on the writer those pragmas measurably slowed small commits.
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

	// Read-only pool. PRAGMAs are per connection, so they travel in the DSN:
	// every pooled reader gets them, not just the first one Exec ran on.
	// mmap is read-pool-only: memory-mapped reads are much faster, while the
	// single writer commits small transactions and pays the dirty-page cost.
	roDSN := "file:" + filepath.ToSlash(path) + "?mode=ro" +
		"&_pragma=busy_timeout(5000)&_pragma=query_only(1)" +
		"&_pragma=temp_store(2)&_pragma=cache_size(-20000)&_pragma=mmap_size(67108864)"
	ro, err := sql.Open("sqlite", roDSN)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	poolSize := readPoolSize()
	ro.SetMaxOpenConns(poolSize)
	ro.SetMaxIdleConns(poolSize)
	if err := ro.Ping(); err != nil {
		ro.Close()
		db.Close()
		return nil, fmt.Errorf("ping read pool: %w", err)
	}
	s.ro = ro

	s.presenceWG.Add(1)
	go s.presenceLoop()
	return s, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.presenceStop)
		s.presenceWG.Wait()
	})
	if s.ro != nil {
		_ = s.ro.Close()
	}
	return s.db.Close()
}

// touchPresence records a room touch. It never touches SQL: the coalescing
// map is updated in memory and a single opaque touch is queued for the
// background flusher, so a room read never waits on the writer lock.
// Entries are pruned by TTL so a flood of random tokens cannot grow the map.
func (s *Store) touchPresence(m Member, nowMs int64) {
	s.presenceMu.Lock()
	prev, seen := s.presence[m.AgentToken]
	if seen && nowMs-prev.at < PresenceWriteInterval {
		s.presenceMu.Unlock()
		return
	}
	s.presence[m.AgentToken] = presenceEntry{at: nowMs, groupID: m.GroupID, botID: m.BotID}
	if nowMs-s.lastPruneMs > presencePruneInterval {
		for k, e := range s.presence {
			if nowMs-e.at > presencePruneInterval {
				delete(s.presence, k)
			}
		}
		s.lastPruneMs = nowMs
	}
	// Only 1..~4 entries can be pruned per call at steady state; cap the
	// map explicitly so an attacker with random tokens cannot exhaust RAM.
	if len(s.presence) > 8192 {
		for k, e := range s.presence {
			if nowMs-e.at > PresenceWriteInterval {
				delete(s.presence, k)
			}
			if len(s.presence) <= 4096 {
				break
			}
		}
	}
	queued := true
	select {
	case s.presenceCh <- presenceTouch{token: m.AgentToken, nowMs: nowMs}:
	default:
		queued = false
	}
	if !queued {
		// The queue is full: forget the coalescing mark so the next touch
		// retries instead of suppressing a presence write for 30 seconds.
		if seen {
			s.presence[m.AgentToken] = prev
		} else {
			delete(s.presence, m.AgentToken)
		}
	}
	s.presenceMu.Unlock()
}

// FlushPresence drains the queue and writes every pending last_seen_at in a
// single transaction. Safe to call concurrently with touchPresence.
func (s *Store) FlushPresence(ctx context.Context) error {
	pending := make(map[string]int64, len(s.presenceCh))
	for {
		select {
		case p := <-s.presenceCh:
			if p.nowMs > pending[p.token] {
				pending[p.token] = p.nowMs
			}
			continue
		default:
		}
		break
	}
	if len(pending) == 0 {
		return nil
	}
	return s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		stmt, err := conn.PrepareContext(ctx,
			"UPDATE bot_members SET last_seen_at = ? WHERE token_hash = ? OR agent_token = ?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for token, at := range pending {
			h := HashToken(token)
			if _, err := stmt.ExecContext(ctx, at, h, token); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) presenceLoop() {
	defer s.presenceWG.Done()
	ticker := time.NewTicker(presenceFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.presenceStop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.FlushPresence(ctx)
			cancel()
		}
	}
}

// onlinePresence returns the bot IDs of one group that touched the room
// within the online window, so a member's first read already sees itself
// online even though the database write is still queued.
func (s *Store) onlinePresence(groupID, nowMs int64) map[string]bool {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	var out map[string]bool
	for _, e := range s.presence {
		if e.groupID == groupID && nowMs-e.at <= OnlineWindowMs {
			if out == nil {
				out = map[string]bool{}
			}
			out[e.botID] = true
		}
	}
	return out
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
  agent_token TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  token_enc TEXT NOT NULL DEFAULT '',
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
  last_active_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_id, id);
CREATE INDEX IF NOT EXISTS idx_members_group_mock ON bot_members(group_id, is_mock);
CREATE INDEX IF NOT EXISTS idx_groups_updated ON groups(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_groups_status ON groups(status);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires ON admin_sessions(expires_at);
DROP INDEX IF EXISTS idx_members_group;
DROP INDEX IF EXISTS idx_members_presence;
`)
	if err != nil {
		return err
	}

	if err := s.ensureColumn(ctx, "groups", "skip_streak", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "groups", "current_turn_bot_id", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "bot_members", "token_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "bot_members", "token_enc", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "admin_sessions", "last_active_at", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_members_token_hash ON bot_members(token_hash)")

	return s.migrateUnencryptedTokens(ctx)
}

func (s *Store) ensureColumn(ctx context.Context, table, col, colDef string) error {
	rows, err := ctxQuery(ctx, s.db, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+col+" "+colDef); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateUnencryptedTokens(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM bot_members WHERE agent_token != '' AND token_hash = ''").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	var bakPath string
	isMemory := s.dbPath == "" || s.dbPath == ":memory:" || strings.HasPrefix(s.dbPath, "file::memory:")
	if !isMemory {
		bakPath = s.dbPath + ".bak"
		if _, err := os.Stat(bakPath); err == nil {
			bakPath = fmt.Sprintf("%s.pre_migration_%d.bak", s.dbPath, time.Now().UnixNano())
		}
		escaped := strings.ReplaceAll(filepath.ToSlash(bakPath), "'", "''")
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
			return fmt.Errorf("pre-migration backup failed: %w", err)
		}
	}

	err := s.immediateTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx,
			"SELECT group_id, bot_id, agent_token FROM bot_members WHERE agent_token != '' AND token_hash = ''")
		if err != nil {
			return err
		}
		defer rows.Close()

		type unmigrated struct {
			groupID int64
			botID   string
			token   string
		}
		var list []unmigrated
		for rows.Next() {
			var u unmigrated
			if err := rows.Scan(&u.groupID, &u.botID, &u.token); err != nil {
				return err
			}
			list = append(list, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		key := s.key()
		for _, u := range list {
			h := HashToken(u.token)
			enc, err := EncryptToken(key, u.token, MemberAAD(u.botID))
			if err != nil {
				return fmt.Errorf("encrypt token for group %d bot %s: %w", u.groupID, u.botID, err)
			}
			if _, err := conn.ExecContext(ctx,
				"UPDATE bot_members SET token_hash = ?, token_enc = ?, agent_token = '' WHERE group_id = ? AND bot_id = ?",
				h, enc, u.groupID, u.botID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	var remaining int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM bot_members WHERE agent_token != ''").Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("migration verification failed: %d plaintext tokens remain", remaining)
	}
	if bakPath != "" {
		_ = os.Remove(bakPath)
	}
	return nil
}

// ctxQuery is a tiny helper so migrate can use QueryContext uniformly.
func ctxQuery(ctx context.Context, db *sql.DB, query string) (*sql.Rows, error) {
	return db.QueryContext(ctx, query)
}

// immediateTx runs fn inside a SQLite BEGIN IMMEDIATE transaction on a
// dedicated connection, so every turn mutation is atomic. Group changes are
// collected during the transaction and published only after COMMIT, so a
// woken reader can never observe the pre-commit snapshot.
func (s *Store) immediateTx(ctx context.Context, fn func(ctx context.Context, conn *sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	changes := &txChanges{ids: map[int64]struct{}{}}
	txCtx := context.WithValue(ctx, txChangesKey{}, changes)
	if err := fn(txCtx, conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	for id := range changes.ids {
		s.notifyGroup(id)
	}
	return nil
}

type txChanges struct {
	ids map[int64]struct{}
}

type txChangesKey struct{}

// noteChange records a group change for post-commit notification.
func noteChange(ctx context.Context, id int64) {
	if changes, ok := ctx.Value(txChangesKey{}).(*txChanges); ok {
		changes.ids[id] = struct{}{}
	}
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

func loadMembers(ctx context.Context, q querier, groupID int64, key []byte) ([]Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, token_hash, token_enc, last_seen_at
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
			&isMock, &m.AgentToken, &m.TokenHash, &m.TokenEnc, &m.LastSeenAt); err != nil {
			return nil, err
		}
		m.IsMock = isMock != 0
		if m.TokenEnc != "" && len(key) >= 32 {
			if dec, err := DecryptToken(key, m.TokenEnc, MemberAAD(m.BotID)); err == nil {
				m.AgentToken = dec
			}
		}
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

// loadMembersLite is the public-room projection: no agent_token is ever
// selected on the anonymous path.
func loadMembersLite(ctx context.Context, q querier, groupID int64) ([]Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, last_seen_at
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
			&isMock, &m.LastSeenAt); err != nil {
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

// markPresenceOnline folds queued presence into already-loaded members in
// place (token lookup, no map allocation on the write path).
func (s *Store) markPresenceOnline(groupID int64, ms []Member, nowMs int64) {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	if len(s.presence) == 0 {
		return
	}
	for i := range ms {
		e, ok := s.presence[ms[i].AgentToken]
		if ok && e.groupID == groupID && nowMs-e.at <= OnlineWindowMs {
			ms[i].LastSeenAt = nowMs
		}
	}
}

// loadMembersLive loads members and folds in queued presence, so turn
// routing sees a bot that just polled the room even before the batched
// last_seen_at write lands.
func (s *Store) loadMembersLive(ctx context.Context, q querier, groupID, nowMs int64) ([]Member, error) {
	ms, err := loadMembers(ctx, q, groupID, s.key())
	if err != nil {
		return nil, err
	}
	s.markPresenceOnline(groupID, ms, nowMs)
	return ms, nil
}

// loadMemberByToken returns nil (not an error) for an unknown token.
func loadMemberByToken(ctx context.Context, q querier, token string, key []byte) (*Member, error) {
	var m Member
	var isMock int
	h := HashToken(token)
	err := q.QueryRowContext(ctx,
		`SELECT group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, token_hash, token_enc, last_seen_at
		 FROM bot_members WHERE token_hash = ? OR agent_token = ?`, h, token).
		Scan(&m.GroupID, &m.BotID, &m.Name, &m.RoleTitle, &m.AccentColor,
			&isMock, &m.AgentToken, &m.TokenHash, &m.TokenEnc, &m.LastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.IsMock = isMock != 0
	if m.TokenEnc != "" && len(key) >= 32 {
		if dec, err := DecryptToken(key, m.TokenEnc, MemberAAD(m.BotID)); err == nil {
			m.AgentToken = dec
		}
	}
	return &m, nil
}

// loadLastMessage returns the newest message of a group (nil when empty),
// so mock replies can quote without loading the whole buffered history.
func loadLastMessage(ctx context.Context, q querier, groupID int64) (*Message, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, group_id, turn_seq, sender_bot_id, sender_name, sender_color, role, content, created_at
		 FROM messages WHERE group_id = ? ORDER BY id DESC LIMIT 1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &msgs[0], nil
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

func (s *Store) saveGroup(ctx context.Context, q querier, g *Group) error {
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
	if _, err := ex.ExecContext(ctx,
		`UPDATE groups SET name=?, description=?, topic=?, status=?, turn_mode=?, max_turns=?,
		 turn_seq=?, current_turn_bot_id=?, turn_queue=?, mock_delay_sec=?, turn_started_at=?,
		 skip_streak=?, updated_at=? WHERE id=?`,
		g.Name, g.Description, g.Topic, g.Status, g.TurnMode, g.MaxTurns, g.TurnSeq,
		current, string(queue), g.MockDelaySec, g.TurnStartedAt, g.SkipStreak, g.UpdatedAt, g.ID); err != nil {
		return err
	}
	noteChange(ctx, g.ID)
	return nil
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

func insertMember(ctx context.Context, q querier, m *Member, plainToken string, key []byte) error {
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
	h := HashToken(plainToken)
	enc, err := EncryptToken(key, plainToken, MemberAAD(m.BotID))
	if err != nil {
		return err
	}
	m.TokenHash = h
	m.TokenEnc = enc
	m.AgentToken = plainToken
	_, err = ex.ExecContext(ctx,
		`INSERT INTO bot_members (group_id, bot_id, name, role_title, accent_color, is_mock, agent_token, token_hash, token_enc, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		m.GroupID, m.BotID, m.Name, m.RoleTitle, m.AccentColor, isMock, h, enc, m.LastSeenAt)
	return err
}
