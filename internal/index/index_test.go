package index

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BayInl/session-finder/internal/parsers"
	"github.com/BayInl/session-finder/internal/record"

	_ "modernc.org/sqlite"
)

func TestInitializeSchemaMigratesLegacyColumnsAndBuildsFTS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE sessions (
		id INTEGER PRIMARY KEY, tool TEXT NOT NULL, session_id TEXT NOT NULL,
		cwd TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', created REAL,
		updated REAL, source_path TEXT NOT NULL
	);
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY, session_pk INTEGER NOT NULL,
		role TEXT NOT NULL, ts REAL, text TEXT NOT NULL
	);
	CREATE TABLE sources (
		source_path TEXT PRIMARY KEY, tool TEXT NOT NULL, mtime REAL NOT NULL,
		size INTEGER NOT NULL, skipped INTEGER NOT NULL DEFAULT 0
	);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	columns, err := tableColumns(db, "sources")
	if err != nil {
		t.Fatal(err)
	}
	if columns["skipped"] {
		t.Fatal("legacy sources.skipped column still present")
	}
	columns, err = tableColumns(db, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !columns["mtime"] || !columns["size"] {
		t.Fatalf("missing migrated session columns: %#v", columns)
	}
	if _, err := db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 's1', '/tmp', 'title', 1704067200, 1704067200, '/tmp/s1.jsonl');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067201, 'alpha needle');`); err != nil {
		t.Fatal(err)
	}
	var ftsCount, triCount int
	if err := db.QueryRow(`SELECT count(*) FROM messages_fts_docsize`).Scan(&ftsCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM messages_tri_docsize`).Scan(&triCount); err != nil {
		t.Fatal(err)
	}
	if ftsCount != 1 || triCount != 1 {
		t.Fatalf("FTS docsize counts = %d/%d, want 1/1", ftsCount, triCount)
	}
}

func TestSearchAndShow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'session-1', '/workspace/project', 'Question', 1704067200, 1704067300, '/source/one');
	INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067201, 'find alpha here');
	INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'assistant', 1704067202, 'alpha answer');
	INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'system', 1704067203, '<system-reminder>alpha hidden');
	INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'session-1', '/workspace/project', 'Question', 1704067200, 1704067300, '/source/two');
	INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'assistant', 1704067204, 'unmatched extra');`)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Search(db, "alpha", "", "project", "2024-01-01", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SessionID != "session-1" ||
		results[0].MessageCount != 4 || results[0].MatchCount != 2 {
		t.Fatalf("unexpected search results: %#v", results)
	}
	if len(results[0].Snippets) != 2 {
		t.Fatalf("snippets = %#v, want two visible messages", results[0].Snippets)
	}
	if results[0].LastUser != "find alpha here" || results[0].LastAssistant != "unmatched extra" {
		t.Fatalf("last round = user %q assistant %q", results[0].LastUser, results[0].LastAssistant)
	}
	allResults, err := Search(db, "alpha", "", "", "", 20, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(allResults) != 1 || allResults[0].MessageCount != 4 || allResults[0].MatchCount != 3 {
		t.Fatalf("--all search results: %#v", allResults)
	}
	rows, err := Show(db, "session-", "user", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []ShowRow{{Tool: "codex", SessionID: "session-1", Title: "Question", CWD: "/workspace/project", Created: "2024-01-01T00:00:00Z", Updated: "2024-01-01T00:01:40Z", Role: "user", Timestamp: "2024-01-01T00:00:01Z", Text: "find alpha here"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("show rows = %#v, want %#v", rows, want)
	}
}

func TestInsertRecordsTruncatesLargeMessagesFromEveryParser(t *testing.T) {
	for _, tool := range record.Tools {
		t.Run(tool, func(t *testing.T) {
			root := t.TempDir()
			needle := "searchable" + strings.ReplaceAll(tool, "-", "")
			text := needle + " " + strings.Repeat("界", messageTextLimit)
			spec, sessionID, role := largeMessageSource(t, root, tool, text)

			db, err := Open(filepath.Join(root, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := InitializeSchema(db); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = insertRecords(tx, func(emit parsers.Emit) error {
				return parsers.Parse(spec, emit)
			})
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}

			var stored, storedRole string
			if err := db.QueryRow("SELECT role, text FROM messages").Scan(&storedRole, &stored); err != nil {
				t.Fatal(err)
			}
			if storedRole != role {
				t.Fatalf("stored role = %q, want %q", storedRole, role)
			}
			if len(stored) > messageTextLimit || !strings.HasSuffix(stored, truncatedMarker) || !utf8.ValidString(stored) {
				t.Fatalf("stored message: bytes=%d suffix=%t valid_utf8=%t", len(stored), strings.HasSuffix(stored, truncatedMarker), utf8.ValidString(stored))
			}
			results, err := Search(db, needle, tool, "", "", 20, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].SessionID != sessionID {
				t.Fatalf("search results = %#v, want truncated %s session %q", results, tool, sessionID)
			}
		})
	}
}

func TestIndexMessageTextBoundary(t *testing.T) {
	unchanged := strings.Repeat("a", messageTextLimit)
	if got := indexMessageText(unchanged); got != unchanged {
		t.Fatalf("message at limit changed: bytes=%d", len(got))
	}
	for _, role := range []string{"user", "assistant", "system"} {
		text := role + " " + strings.Repeat("界", messageTextLimit)
		got := indexMessageText(text)
		if len(got) > messageTextLimit || !strings.HasSuffix(got, truncatedMarker) || !utf8.ValidString(got) {
			t.Fatalf("%s message: bytes=%d suffix=%t valid_utf8=%t", role, len(got), strings.HasSuffix(got, truncatedMarker), utf8.ValidString(got))
		}
	}
}

func largeMessageSource(t *testing.T, root, tool, text string) (record.SourceSpec, string, string) {
	t.Helper()
	switch tool {
	case "opencode":
		path := filepath.Join(root, "opencode.db")
		db, err := sql.Open("sqlite", dbURI(path, false))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT, title TEXT, time_updated INTEGER);
CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT);
CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, time_created INTEGER, data TEXT);
INSERT INTO session VALUES ('session-opencode', '/workspace', '', 1);
INSERT INTO message VALUES ('message-opencode', 'session-opencode', 1, '{"role":"user"}');
INSERT INTO part VALUES ('part-opencode', 'message-opencode', 1, ?)`, mustJSONText(text)); err != nil {
			t.Fatal(err)
		}
		return record.SourceSpec{Tool: tool, Path: path}, "session-opencode", "user"
	case "grok":
		path := filepath.Join(root, "session-grok", "chat_history.jsonl")
		writeIndexJSONL(t, path, map[string]any{"type": "assistant", "content": text})
		return record.SourceSpec{Tool: tool, Path: path}, "session-grok", "assistant"
	case "codex":
		path := filepath.Join(root, "codex.jsonl")
		writeIndexJSONL(t, path,
			map[string]any{"type": "session_meta", "payload": map[string]any{"id": "session-codex"}},
			map[string]any{"type": "message", "payload": map[string]any{"type": "message", "role": "tool", "content": text}},
		)
		return record.SourceSpec{Tool: tool, Path: path}, "session-codex", "system"
	case "kimi-code":
		path := filepath.Join(root, "session_kimi", "agents", "agent_a", "wire.jsonl")
		writeIndexJSONL(t, path, map[string]any{
			"type":  "context.append_loop_event",
			"event": map[string]any{"type": "tool.result", "toolCallId": "call-kimi", "result": map[string]any{"output": text}},
		})
		return record.SourceSpec{Tool: tool, Path: path}, "session_kimi", "assistant"
	case "claude":
		path := filepath.Join(root, "claude.jsonl")
		writeIndexJSONL(t, path, map[string]any{
			"type": "user", "sessionId": "session-claude", "message": map[string]any{"role": "user", "content": text},
		})
		return record.SourceSpec{Tool: tool, Path: path}, "session-claude", "user"
	default:
		t.Fatalf("unsupported tool %q", tool)
		return record.SourceSpec{}, "", ""
	}
}

func writeIndexJSONL(t *testing.T, path string, values ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, data...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(path, lines, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustJSONText(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

func TestSourceSignatureIncludesDerivedGrokSummary(t *testing.T) {
	root := t.TempDir()
	chatPath := filepath.Join(root, "chat_history.jsonl")
	summaryPath := filepath.Join(root, "summary.json")
	if err := os.WriteFile(chatPath, []byte("chat"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summaryPath, []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, size := SourceSignature(record.SourceSpec{Tool: "grok", Path: chatPath})
	if size != int64(len("chat")+len("summary")) {
		t.Fatalf("SourceSignature size = %d, want %d", size, len("chat")+len("summary"))
	}
}

func TestIndexSourcePropagatesGrokMetadataSQLError(t *testing.T) {
	root := t.TempDir()
	chatPath := filepath.Join(root, "chat_history.jsonl")
	summaryPath := filepath.Join(root, "summary.json")
	if err := os.WriteFile(chatPath, []byte(`{"type":"user","content":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summaryPath, []byte(`{"updated_at":"2024-01-01"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_grok_metadata BEFORE UPDATE OF updated ON sessions
		BEGIN SELECT RAISE(FAIL, 'metadata rejected'); END`); err != nil {
		t.Fatal(err)
	}
	spec := record.SourceSpec{Tool: "grok", Path: chatPath}
	_, _, err = indexSource(db, spec, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "metadata rejected") {
		t.Fatalf("indexSource error = %v, want metadata rejection", err)
	}
	var sessions int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions after rollback = %d, want 0", sessions)
	}
}

func TestTimestampParsing(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"2024-01-02 03:04:05", "2024-01-02T03:04:05Z"},
		{"2024-01-02T03:04:05+0800", "2024-01-01T19:04:05Z"},
		{"2024-01-02", "2024-01-02T00:00:00Z"},
		{1704067200, "2024-01-01T00:00:00Z"},
		{0.9999994, "1970-01-01T00:00:00Z"},
		{0.9999995, "1970-01-01T00:00:01Z"},
		{1.9999995, "1970-01-01T00:00:01Z"},
		{1.9999999, "1970-01-01T00:00:02Z"},
		{-0.0000005, "1970-01-01T00:00:00Z"},
		{-0.0000006, "1969-12-31T23:59:59Z"},
		{-2.0000001, "1969-12-31T23:59:58Z"},
		{nil, "-"},
	}
	for _, tc := range cases {
		if got := FormatTimestamp(tc.value); got != tc.want {
			t.Errorf("FormatTimestamp(%#v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
