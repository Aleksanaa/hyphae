package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and runs pending migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+
		"?_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(ON)"+
		"&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DefaultPath returns $XDG_DATA_HOME/hyphae/hyphae.db.
func DefaultPath() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "hyphae", "hyphae.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "hyphae", "hyphae.db")
}

// ── Schema ────────────────────────────────────────────────────────────────────

const sqlInitial = `
CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    work_dir   TEXT    NOT NULL,
    title      TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE messages (
    id         INTEGER PRIMARY KEY,
    session_id TEXT    NOT NULL REFERENCES sessions(id),
    seq        INTEGER NOT NULL,
    role       TEXT    NOT NULL,
    content       TEXT    NOT NULL DEFAULT '',
    thinking      TEXT    NOT NULL DEFAULT '',
    thinking_secs INTEGER NOT NULL DEFAULT 0,
    call_id       TEXT    NOT NULL DEFAULT '',  -- for role='tool': the LLM tool call id
    is_error      INTEGER NOT NULL DEFAULT 0,   -- for role='tool': whether the call errored
    created_at    INTEGER NOT NULL,
    UNIQUE(session_id, seq)
);

CREATE TABLE tool_calls (
    id          INTEGER PRIMARY KEY,
    message_id  INTEGER NOT NULL REFERENCES messages(id),
    call_id     TEXT    NOT NULL UNIQUE,
    tool_name   TEXT    NOT NULL,
    display_key TEXT    NOT NULL DEFAULT '',
    args        TEXT    NOT NULL DEFAULT '',
    result      TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'running',
    is_error    INTEGER NOT NULL DEFAULT 0
);
`

// ── Migrations ────────────────────────────────────────────────────────────────

type migration struct {
	id  int
	sql string
}

var migrations = []migration{
	{1, sqlInitial},
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id         INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL UNIQUE,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}

	for _, m := range migrations {
		var count int
		s.db.QueryRow(`SELECT COUNT(*) FROM migrations WHERE id = ?`, m.id).Scan(&count) //nolint:errcheck
		if count > 0 {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback() //nolint:errcheck
			return err
		}
		if _, err := tx.Exec(`INSERT INTO migrations (id, name, applied_at) VALUES (?, ?, ?)`,
			m.id, "initial", now()); err != nil {
			tx.Rollback() //nolint:errcheck
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ── Sessions ──────────────────────────────────────────────────────────────────

// SessionRow is a lightweight session record for listing.
type SessionRow struct {
	ID        string
	WorkDir   string
	Title     string
	CreatedAt int64
	UpdatedAt int64
}

// CreateSession inserts a new session row. No-ops if the id already exists.
func (s *Store) CreateSession(id, workDir string) error {
	n := now()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions (id, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, workDir, n, n,
	)
	return err
}

// UpdateSessionTitle updates the title and bumps updated_at.
func (s *Store) UpdateSessionTitle(id, title string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
		title, now(), id,
	)
	return err
}

// ListSessions returns sessions for workDir ordered newest first.
func (s *Store) ListSessions(workDir string) ([]SessionRow, error) {
	rows, err := s.db.Query(
		`SELECT id, work_dir, title, created_at, updated_at
		   FROM sessions WHERE work_dir = ? ORDER BY updated_at DESC`,
		workDir,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var title sql.NullString
		if err := rows.Scan(&r.ID, &r.WorkDir, &title, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Title = title.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Messages ──────────────────────────────────────────────────────────────────

// InsertMessage inserts a message and returns its row id.
// Returns (0, nil) if the row already exists (INSERT OR IGNORE).
// If 0 is returned the caller may call MessageID to retrieve the existing id.
// callID and isError are only meaningful for role='tool'.
func (s *Store) InsertMessage(sessionID string, seq int, role, content, thinking string, thinkingSecs int, callID string, isError bool) (int64, error) {
	isErrorInt := 0
	if isError {
		isErrorInt = 1
	}
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages (session_id, seq, role, content, thinking, thinking_secs, call_id, is_error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, seq, role, content, thinking, thinkingSecs, callID, isErrorInt, now(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MessageID returns the row id of an existing message by (sessionID, seq).
func (s *Store) MessageID(sessionID string, seq int) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM messages WHERE session_id = ? AND seq = ?`, sessionID, seq,
	).Scan(&id)
	return id, err
}

// LoadedToolCall is a tool call record as loaded from the DB.
type LoadedToolCall struct {
	CallID     string
	Name       string
	DisplayKey string
	Args       string
	Result     string
	Status     string
	IsError    bool
}

// LoadedMessage is a message record with its associated tool calls.
type LoadedMessage struct {
	Seq          int
	Role         string
	Content      string
	Thinking     string
	ThinkingSecs int
	CallID       string // for role='tool': the tool call id
	IsError      bool   // for role='tool'
	ToolCalls    []LoadedToolCall
}

// LoadSessionMessages returns all messages for a session in seq order,
// with tool calls nested under their assistant messages.
func (s *Store) LoadSessionMessages(sessionID string) ([]LoadedMessage, error) {
	rows, err := s.db.Query(`
		SELECT m.seq, m.role, m.content, m.thinking, m.thinking_secs, m.call_id, m.is_error,
		       tc.call_id, tc.tool_name, tc.display_key, tc.args, tc.result, tc.status, tc.is_error
		  FROM messages m
		  LEFT JOIN tool_calls tc ON tc.message_id = m.id
		 WHERE m.session_id = ?
		 ORDER BY m.seq, tc.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []LoadedMessage
	curSeq := -1
	for rows.Next() {
		var seq, thinkingSecs, msgIsError int
		var role, content, thinking, msgCallID string
		var tcCallID, tcName, tcDisplayKey, tcArgs, tcResult, tcStatus sql.NullString
		var tcIsError sql.NullInt64
		if err := rows.Scan(
			&seq, &role, &content, &thinking, &thinkingSecs, &msgCallID, &msgIsError,
			&tcCallID, &tcName, &tcDisplayKey, &tcArgs, &tcResult, &tcStatus, &tcIsError,
		); err != nil {
			return nil, err
		}
		if seq != curSeq {
			msgs = append(msgs, LoadedMessage{
				Seq:          seq,
				Role:         role,
				Content:      content,
				Thinking:     thinking,
				ThinkingSecs: thinkingSecs,
				CallID:       msgCallID,
				IsError:      msgIsError != 0,
			})
			curSeq = seq
		}
		if tcCallID.Valid {
			msgs[len(msgs)-1].ToolCalls = append(msgs[len(msgs)-1].ToolCalls, LoadedToolCall{
				CallID:     tcCallID.String,
				Name:       tcName.String,
				DisplayKey: tcDisplayKey.String,
				Args:       tcArgs.String,
				Result:     tcResult.String,
				Status:     tcStatus.String,
				IsError:    tcIsError.Int64 != 0,
			})
		}
	}
	return msgs, rows.Err()
}

// ── Tool calls ────────────────────────────────────────────────────────────────

// InsertToolCall inserts a tool call record. No-ops if call_id already exists.
func (s *Store) InsertToolCall(messageID int64, callID, name, displayKey, args string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO tool_calls (message_id, call_id, tool_name, display_key, args)
		 VALUES (?, ?, ?, ?, ?)`,
		messageID, callID, name, displayKey, args,
	)
	return err
}

// FinalizeToolCall updates result, status, and is_error for a completed tool call.
func (s *Store) FinalizeToolCall(callID, result, status string, isError bool) error {
	isErrorInt := 0
	if isError {
		isErrorInt = 1
	}
	_, err := s.db.Exec(
		`UPDATE tool_calls SET result = ?, status = ?, is_error = ? WHERE call_id = ?`,
		result, status, isErrorInt, callID,
	)
	return err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func now() int64 { return time.Now().UnixMilli() }
