// Package index builds and queries the local SQLite FTS5 session index.
package index

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/BayInl/session-finder/internal/parsers"
	"github.com/BayInl/session-finder/internal/record"
)

var (
	DefaultIndexPath = filepath.Join(userHome(), ".cache", "session-finder", "index.db")
)

const ftsTriggersSQL = `
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text)
        VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text)
        VALUES ('delete', old.id, old.text);
    INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_tri(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_tri(messages_tri, rowid, text)
        VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_tri_au AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_tri(messages_tri, rowid, text)
        VALUES ('delete', old.id, old.text);
    INSERT INTO messages_tri(rowid, text) VALUES (new.id, new.text);
END;
`

var ftsTriggerNames = []string{
	"messages_ai", "messages_ad", "messages_au",
	"messages_tri_ai", "messages_tri_ad", "messages_tri_au",
}

// Summary is the result of an index operation.
type Summary struct {
	Tools   map[string]ToolStats `json:"tools"`
	Sources SourceStats          `json:"sources"`
	DBPath  string               `json:"db_path"`
}

// ToolStats contains distinct session and message counts.
type ToolStats struct {
	Sessions int `json:"sessions"`
	Messages int `json:"messages"`
}

// SourceStats contains source processing counts.
type SourceStats struct {
	Processed int `json:"processed"`
	Unchanged int `json:"unchanged"`
	Errors    int `json:"errors"`
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func dbURI(path string, readOnly bool) string {
	escaped := url.PathEscape(path)
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	escaped = strings.ReplaceAll(escaped, "%2f", "/")
	uri := "file:" + escaped
	if readOnly {
		uri += "?mode=ro"
	}
	return uri
}

// Open opens a writable local index and configures cache-oriented pragmas.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		path = DefaultIndexPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbURI(path, false))
	if err != nil {
		return nil, err
	}
	// Match Python's single-connection semantics and keep PRAGMA state stable.
	db.SetMaxOpenConns(1)
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = OFF",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA mmap_size = 268435456",
		"PRAGMA cache_size = -65536",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// InitializeSchema creates tables, FTS5 indexes, and maintenance triggers.
func InitializeSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY,
    tool TEXT NOT NULL,
    session_id TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    created REAL,
    updated REAL,
    source_path TEXT NOT NULL,
    mtime REAL NOT NULL DEFAULT 0,
    size INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_tool_id ON sessions(tool, session_id);
CREATE INDEX IF NOT EXISTS sessions_source_path ON sessions(source_path);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY,
    session_pk INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    ts REAL,
    text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_session ON messages(session_pk, ts, id);
CREATE INDEX IF NOT EXISTS messages_role ON messages(role);

CREATE TABLE IF NOT EXISTS sources (
    source_path TEXT PRIMARY KEY,
    tool TEXT NOT NULL,
    mtime REAL NOT NULL,
    size INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text,
    content='messages',
    content_rowid='id',
    tokenize='unicode61'
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_tri USING fts5(
    text,
    content='messages',
    content_rowid='id',
    tokenize='trigram'
);
CREATE TABLE IF NOT EXISTS opencode_sessions (
    session_id TEXT PRIMARY KEY,
    time_updated INTEGER NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := ensureSessionColumns(db); err != nil {
		return err
	}
	if err := removeSourcesSkippedColumn(db); err != nil {
		return err
	}
	if err := ensureFTSIndexes(db); err != nil {
		return err
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func ensureSessionColumns(db *sql.DB) error {
	columns, err := tableColumns(db, "sessions")
	if err != nil {
		return err
	}
	if !columns["mtime"] {
		if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN mtime REAL NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if !columns["size"] {
		if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN size INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

func removeSourcesSkippedColumn(db *sql.DB) error {
	columns, err := tableColumns(db, "sources")
	if err != nil {
		return err
	}
	if !columns["skipped"] {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE sources DROP COLUMN skipped"); err == nil {
		return nil
	}
	// Fallback for SQLite builds without DROP COLUMN support.
	if _, err := db.Exec("ALTER TABLE sources RENAME TO sources_legacy"); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE sources (
		source_path TEXT PRIMARY KEY,
		tool TEXT NOT NULL,
		mtime REAL NOT NULL,
		size INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO sources(source_path, tool, mtime, size)
		SELECT source_path, tool, mtime, size FROM sources_legacy`); err != nil {
		return err
	}
	_, err = db.Exec("DROP TABLE sources_legacy")
	return err
}

func ensureFTSIndexes(db *sql.DB) error {
	// External-content FTS5 tables expose docsize shadow tables. Their row count
	// is the reliable indicator that the index itself is in sync with messages.
	var messages int
	if err := db.QueryRow("SELECT count(*) FROM messages").Scan(&messages); err != nil {
		return fmt.Errorf("messages unavailable: %w", err)
	}
	for _, table := range []string{"messages_fts", "messages_tri"} {
		var indexed int
		if err := db.QueryRow("SELECT count(*) FROM " + table + "_docsize").Scan(&indexed); err != nil {
			return fmt.Errorf("%s unavailable: %w", table, err)
		}
		if indexed != messages {
			if _, err := db.Exec("INSERT INTO " + table + "(" + table + ") VALUES ('rebuild')"); err != nil {
				return fmt.Errorf("rebuild %s: %w", table, err)
			}
		}
	}
	_, err := db.Exec(ftsTriggersSQL)
	return err
}

// Clear removes indexed data while preserving schema and FTS tables.
func Clear(db *sql.DB) error {
	_, err := db.Exec("DELETE FROM sources; DELETE FROM sessions; DELETE FROM opencode_sessions")
	return err
}

// SourceSignature returns max mtime and total size over a source's files.
func SourceSignature(spec record.SourceSpec) (float64, int64) {
	paths := append([]string{spec.Path}, spec.AuxiliaryPath...)
	if spec.Tool == "opencode" {
		paths = append(paths, spec.Path+"-wal", spec.Path+"-shm")
	}
	var maxMtime float64
	var total int64
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		mtime := float64(stat.ModTime().UnixNano()) / 1e9
		if mtime > maxMtime {
			maxMtime = mtime
		}
		total += stat.Size()
	}
	return maxMtime, total
}

// TimestampEpoch converts epoch seconds/milliseconds or ISO-8601 to UTC seconds.
func TimestampEpoch(value any) (*float64, bool) {
	if value == nil {
		return nil, false
	}
	if b, ok := value.(bool); ok {
		_ = b
		return nil, false
	}
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int64:
		number = float64(value)
	case int32:
		number = float64(value)
	case uint64:
		number = float64(value)
	case uint:
		number = float64(value)
	case []byte:
		return TimestampEpoch(string(value))
	case time.Time:
		result := float64(value.UnixNano()) / 1e9
		return &result, true
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, false
		}
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			number = parsed
		} else {
			parsedTime, ok := parseISOTime(text)
			if !ok {
				return nil, false
			}
			result := float64(parsedTime.UnixNano()) / 1e9
			return &result, true
		}
	default:
		return TimestampEpoch(fmt.Sprint(value))
	}
	if number > 10_000_000_000 {
		number /= 1000
	}
	return &number, true
}

func parseISOTime(text string) (time.Time, bool) {
	normalized := text
	if strings.HasSuffix(normalized, "Z") {
		normalized = strings.TrimSuffix(normalized, "Z") + "+00:00"
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, normalized)
		if err == nil {
			if parsed.Location() == time.UTC || parsed.Format("-07:00") != "+00:00" {
				return parsed, true
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// FormatTimestamp formats any supported timestamp as compact UTC ISO-8601.
func FormatTimestamp(value any) string {
	epoch, ok := TimestampEpoch(value)
	if !ok || epoch == nil {
		return "-"
	}
	// Python's datetime.fromtimestamp rounds the fractional second to the
	// nearest microsecond using ties-to-even before isoformat(timespec="seconds")
	// drops the remainder. Split before scaling so float multiplication cannot
	// move a value just below a half-microsecond across the tie.
	seconds := math.Floor(*epoch)
	fraction := *epoch - seconds
	micros := int64(math.RoundToEven(fraction * 1e6))
	if micros == 1_000_000 {
		seconds++
		micros = 0
	}
	return time.Unix(int64(seconds), micros*1_000).UTC().Format("2006-01-02T15:04:05Z")
}

func mergeMin(current, candidate *float64) *float64 {
	if current == nil {
		return candidate
	}
	if candidate == nil || *current <= *candidate {
		return current
	}
	return candidate
}

func mergeMax(current, candidate *float64) *float64 {
	if current == nil {
		return candidate
	}
	if candidate == nil || *current >= *candidate {
		return current
	}
	return candidate
}

func sqlFloat(value any) *float64 {
	epoch, _ := TimestampEpoch(value)
	return epoch
}

type sessionState struct {
	id               int64
	cwd, title       string
	created, updated *float64
	dirty            bool
}

func getOrCreateSession(tx *sql.Tx, cache map[string]*sessionState, msg record.MessageRecord) (int64, error) {
	key := msg.Tool + "\x00" + msg.SessionID + "\x00" + msg.SourcePath
	stamp, _ := TimestampEpoch(msg.Timestamp)
	state := cache[key]
	if state == nil {
		title := msg.Title
		if title == "" && msg.Role == "user" {
			title = parsers.TitleFromText(msg.Text, 120)
		}
		result, err := tx.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, msg.Tool, msg.SessionID, msg.CWD, title, timestampValue(stamp), timestampValue(stamp), msg.SourcePath)
		if err != nil {
			return 0, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		cache[key] = &sessionState{id: id, cwd: msg.CWD, title: title, created: stamp, updated: stamp}
		return id, nil
	}
	previousCWD, previousTitle := state.cwd, state.title
	previousCreated, previousUpdated := state.created, state.updated
	if state.cwd == "" {
		state.cwd = msg.CWD
	}
	if state.title == "" {
		state.title = msg.Title
	}
	if state.title == "" && msg.Role == "user" {
		state.title = parsers.TitleFromText(msg.Text, 120)
	}
	state.created = mergeMin(state.created, stamp)
	state.updated = mergeMax(state.updated, stamp)
	if state.cwd != previousCWD || state.title != previousTitle ||
		!sameFloat(state.created, previousCreated) || !sameFloat(state.updated, previousUpdated) {
		state.dirty = true
	}
	return state.id, nil
}

func timestampValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameFloat(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func insertRecords(tx *sql.Tx, records func(parsers.Emit) error) (int, int, error) {
	cache := map[string]*sessionState{}
	stmt, err := tx.Prepare("INSERT INTO messages(session_pk, role, ts, text) VALUES (?, ?, ?, ?)")
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()
	messages := 0
	err = records(func(msg record.MessageRecord) error {
		session, err := getOrCreateSession(tx, cache, msg)
		if err != nil {
			return err
		}
		stamp, _ := TimestampEpoch(msg.Timestamp)
		if _, err := stmt.Exec(session, msg.Role, timestampValue(stamp), msg.Text); err != nil {
			return err
		}
		messages++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	updateStmt, err := tx.Prepare("UPDATE sessions SET cwd = ?, title = ?, created = ?, updated = ? WHERE id = ?")
	if err != nil {
		return 0, 0, err
	}
	defer updateStmt.Close()
	for _, state := range cache {
		if !state.dirty {
			continue
		}
		if _, err := updateStmt.Exec(state.cwd, state.title, timestampValue(state.created), timestampValue(state.updated), state.id); err != nil {
			return 0, 0, err
		}
	}
	return len(cache), messages, nil
}

func upsertSource(tx *sql.Tx, spec record.SourceSpec, mtime float64, size int64) error {
	_, err := tx.Exec(`INSERT INTO sources(source_path, tool, mtime, size)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_path) DO UPDATE SET tool = excluded.tool,
		mtime = excluded.mtime, size = excluded.size`, spec.Path, spec.Tool, mtime, size)
	return err
}

func sourceUnchanged(db *sql.DB, spec record.SourceSpec, mtime float64, size int64) bool {
	var oldMtime float64
	var oldSize int64
	err := db.QueryRow("SELECT mtime, size FROM sources WHERE source_path = ?", spec.Path).Scan(&oldMtime, &oldSize)
	return err == nil && oldMtime == mtime && oldSize == size
}

func indexSource(db *sql.DB, spec record.SourceSpec, mtime float64, size int64) (int, int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec("DELETE FROM sessions WHERE source_path = ?", spec.Path); err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	sessions, messages, err := insertRecords(tx, func(emit parsers.Emit) error { return parsers.Parse(spec, emit) })
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	if _, err = tx.Exec("UPDATE sessions SET mtime = ?, size = ? WHERE source_path = ?", mtime, size, spec.Path); err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	if spec.Tool == "grok" && len(spec.AuxiliaryPath) > 0 {
		if err = updateGrokMetadata(tx, spec); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	if err = upsertSource(tx, spec, mtime, size); err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return sessions, messages, nil
}

func updateGrokMetadata(tx *sql.Tx, spec record.SourceSpec) error {
	// The parser already obtains cwd/title/created. Summary updated_at is the
	// only metadata not present in chat_history records, so merge all fields as
	// Python does without failing indexing on malformed auxiliary JSON.
	summary := readSummary(spec.AuxiliaryPath[0])
	info := map[string]any{}
	if nested, ok := summary["info"].(map[string]any); ok {
		info = nested
	}
	created, _ := TimestampEpoch(firstNonEmpty(summary["created_at"], info["created_at"]))
	updated, _ := TimestampEpoch(firstNonEmpty(summary["updated_at"], info["updated_at"]))
	cwd := anyString(firstNonEmpty(summary["cwd"], info["cwd"]))
	title := anyString(firstNonEmpty(summary["generated_title"], info["generated_title"]))
	_, err := tx.Exec(`UPDATE sessions SET created = COALESCE(?, created), updated = COALESCE(?, updated),
		cwd = CASE WHEN ? <> '' THEN ? ELSE cwd END,
		title = CASE WHEN ? <> '' THEN ? ELSE title END WHERE source_path = ?`,
		timestampValue(created), timestampValue(updated), cwd, cwd, title, title, spec.Path)
	return err
}

func readSummary(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func anyString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstNonEmpty(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		return value
	}
	return nil
}

func integerValue(value any) int64 {
	switch value := value.(type) {
	case nil:
		return 0
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case []byte:
		parsed, _ := strconv.ParseInt(string(value), 10, 64)
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		if parsed != 0 {
			return parsed
		}
		floatValue, _ := strconv.ParseFloat(value, 64)
		return int64(floatValue)
	default:
		return integerValue(fmt.Sprint(value))
	}
}

func indexOpencode(db *sql.DB, spec record.SourceSpec, mtime float64, size int64) (int, int, error) {
	source, err := sql.Open("sqlite", dbURI(spec.Path, true))
	if err != nil {
		return 0, 0, err
	}
	defer source.Close()
	rows, err := source.Query("SELECT id, time_updated FROM session")
	if err != nil {
		return 0, 0, err
	}
	current := map[string]int64{}
	currentOrder := make([]string, 0, 128)
	for rows.Next() {
		var id string
		var updated any
		if err := rows.Scan(&id, &updated); err != nil {
			rows.Close()
			return 0, 0, err
		}
		current[id] = integerValue(updated)
		currentOrder = append(currentOrder, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	trackedRows, err := db.Query("SELECT session_id, time_updated FROM opencode_sessions")
	if err != nil {
		return 0, 0, err
	}
	tracked := map[string]int64{}
	for trackedRows.Next() {
		var id string
		var updated int64
		if err := trackedRows.Scan(&id, &updated); err != nil {
			trackedRows.Close()
			return 0, 0, err
		}
		tracked[id] = updated
	}
	if err := trackedRows.Err(); err != nil {
		trackedRows.Close()
		return 0, 0, err
	}
	trackedRows.Close()
	var removed, changed []string
	for id := range tracked {
		if _, ok := current[id]; !ok {
			removed = append(removed, id)
		}
	}
	for _, id := range currentOrder {
		updated := current[id]
		if old, ok := tracked[id]; !ok || old != updated {
			changed = append(changed, id)
		}
	}
	sort.Strings(removed)
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	for _, id := range append(removed, changed...) {
		if _, err := tx.Exec("DELETE FROM sessions WHERE tool = 'opencode' AND session_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
		if _, err := tx.Exec("DELETE FROM opencode_sessions WHERE session_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	sessions, messages := 0, 0
	for _, id := range changed {
		countSessions, countMessages, parseErr := insertRecords(tx, func(emit parsers.Emit) error {
			return parsers.Opencode(spec.Path, id, emit)
		})
		if parseErr != nil {
			_ = tx.Rollback()
			return 0, 0, parseErr
		}
		sessions += countSessions
		messages += countMessages
		if _, err := tx.Exec("INSERT OR REPLACE INTO opencode_sessions(session_id, time_updated) VALUES (?, ?)", id, current[id]); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	if err := upsertSource(tx, spec, mtime, size); err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return sessions, messages, nil
}

// Progress is called with total==0 before Discover, then total==len(specs)
// (done==0), then once per spec (done is 1-based). err is a per-source
// warning, not fatal.
type Progress func(done, total int, spec record.SourceSpec, err error)

func IndexAllWithProgress(full bool, dbPath string, progress Progress) (Summary, error) {
	if dbPath == "" {
		dbPath = DefaultIndexPath
	}
	db, err := Open(dbPath)
	if err != nil {
		return Summary{}, err
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		return Summary{}, err
	}
	if full {
		if err := dropFTSTriggers(db); err != nil {
			return Summary{}, err
		}
		if err := Clear(db); err != nil {
			return Summary{}, err
		}
	}
	if progress != nil {
		progress(0, 0, record.SourceSpec{}, nil)
	}
	specs, err := parsers.Discover()
	if err != nil {
		return Summary{}, err
	}
	if progress != nil {
		progress(0, len(specs), record.SourceSpec{}, nil)
	}
	stats := SourceStats{}
	for i, spec := range specs {
		var indexErr error
		mtime, size := SourceSignature(spec)
		if spec.Tool == "opencode" {
			stats.Processed++
			if _, _, err := indexOpencode(db, spec, mtime, size); err != nil {
				stats.Errors++
				indexErr = err
			}
		} else if !full && sourceUnchanged(db, spec, mtime, size) {
			stats.Unchanged++
		} else {
			stats.Processed++
			if _, _, err := indexSource(db, spec, mtime, size); err != nil {
				stats.Errors++
				indexErr = err
			}
		}
		if progress != nil {
			progress(i+1, len(specs), spec, indexErr)
		} else if indexErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to index %s: %v\n", spec.Path, indexErr)
		}
	}
	if full {
		if _, err := db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('rebuild'); INSERT INTO messages_tri(messages_tri) VALUES('rebuild')"); err != nil {
			return Summary{}, err
		}
		if _, err := db.Exec(ftsTriggersSQL); err != nil {
			return Summary{}, err
		}
	}
	tools, err := Stats(db)
	if err != nil {
		return Summary{}, err
	}
	return Summary{Tools: tools, Sources: stats, DBPath: dbPath}, nil
}

func dropFTSTriggers(db *sql.DB) error {
	for _, name := range ftsTriggerNames {
		if _, err := db.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns counts for every supported tool.
func Stats(db *sql.DB) (map[string]ToolStats, error) {
	result := make(map[string]ToolStats, len(record.Tools))
	for _, tool := range record.Tools {
		result[tool] = ToolStats{}
	}
	rows, err := db.Query(`SELECT s.tool, COUNT(DISTINCT s.session_id), COUNT(m.id)
		FROM sessions AS s LEFT JOIN messages AS m ON m.session_pk = s.id GROUP BY s.tool`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tool string
		var sessions, messages int
		if err := rows.Scan(&tool, &sessions, &messages); err != nil {
			return nil, err
		}
		result[tool] = ToolStats{Sessions: sessions, Messages: messages}
	}
	return result, rows.Err()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// SearchResult is an aggregated session search hit.
type SearchResult struct {
	Tool          string   `json:"tool"`
	SessionID     string   `json:"session_id"`
	Title         string   `json:"title"`
	CWD           string   `json:"cwd"`
	Created       string   `json:"created"`
	Updated       string   `json:"updated"`
	MessageCount  int      `json:"message_count"`
	Snippets      []string `json:"snippets"`
	SourcePaths   []string `json:"source_paths"`
	LastUser      string   `json:"last_user,omitempty"`
	LastAssistant string   `json:"last_assistant,omitempty"`
	matchedTerms  map[string]bool
	createdEpoch  *float64
	updatedEpoch  *float64
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeRole(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// ShowRow is one normalized row returned by Show.
type ShowRow struct {
	Tool      string `json:"tool"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	Created   string `json:"created"`
	Updated   string `json:"updated"`
	Role      string `json:"role"`
	Timestamp string `json:"timestamp"`
	Text      string `json:"text"`
}

// Show returns messages for all sessions matching a session ID prefix.
func Show(db *sql.DB, sessionPrefix, role string, limit int) ([]ShowRow, error) {
	if sessionPrefix == "" {
		return []ShowRow{}, nil
	}
	where := []string{`s.session_id LIKE ? ESCAPE '\'`}
	params := []any{escapeLike(sessionPrefix) + "%"}
	if role != "" {
		where = append(where, "m.role = ?")
		params = append(params, role)
	}
	query := `SELECT s.tool, s.session_id, s.title, s.cwd, s.created, s.updated,
		m.role, m.ts, m.text, m.id FROM sessions AS s JOIN messages AS m ON m.session_pk = s.id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY s.tool, s.session_id,
		COALESCE(m.ts, 0), m.id`
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ShowRow{}
	for rows.Next() {
		var tool, sessionID, title, cwd string
		var created, updated, ts any
		var rowRole, text sql.NullString
		var id int64
		if err := rows.Scan(&tool, &sessionID, &title, &cwd, &created, &updated, &rowRole, &ts, &text, &id); err != nil {
			return nil, err
		}
		result = append(result, ShowRow{Tool: tool, SessionID: sessionID, Title: title, CWD: cwd,
			Created: FormatTimestamp(created), Updated: FormatTimestamp(updated), Role: decodeRole(rowRole),
			Timestamp: FormatTimestamp(ts), Text: text.String})
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, rows.Err()
}

// IsCJK reports whether text contains a CJK code point; kept for callers/tests.
func IsCJK(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool {
		return unicode.Is(unicode.Han, r)
	}) >= 0
}
