package parsers

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/BayInl/session-finder/internal/record"
)

func collect(t *testing.T, parse func(Emit) error) []record.MessageRecord {
	t.Helper()
	var records []record.MessageRecord
	if err := parse(func(value record.MessageRecord) error {
		records = append(records, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return records
}

func writeJSONL(t *testing.T, path string, values []map[string]any, trailingNewline bool) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if !trailingNewline {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r') {
			data = data[:len(data)-1]
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractTextAndRoles(t *testing.T) {
	value := []any{"first", map[string]any{"text": "second"}, map[string]any{
		"content": []any{map[string]any{"text": "third"}},
	}}
	if got, want := ExtractText(value), "first\nsecond\nthird"; got != want {
		t.Fatalf("ExtractText() = %q, want %q", got, want)
	}
	if got := NormalizeRole("assistant", "<system-reminder>hidden"); got != "system" {
		t.Fatalf("noise role = %q, want system", got)
	}
	if got := TitleFromText("  first  line\nsecond", 120); got != "first line" {
		t.Fatalf("TitleFromText() = %q", got)
	}
}

func TestGrokRecords(t *testing.T) {
	root := t.TempDir()
	chatPath := filepath.Join(root, "Users%2Fme%2Fproject", "grok-session", "chat_history.jsonl")
	if err := os.MkdirAll(filepath.Dir(chatPath), 0o755); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(filepath.Dir(chatPath), "summary.json")
	if err := os.WriteFile(summaryPath, []byte(`{"info":{"cwd":"/workspace","created_at":"2024-01-02T00:00:00Z"},"generated_title":"Grok title","updated_at":"2024-01-03T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, chatPath, []map[string]any{
		{"type": "metadata", "content": "ignored"},
		{"type": "user", "content": "grok question"},
		{"type": "assistant", "content": []any{map[string]any{"text": "grok answer"}}},
	}, true)
	records := collect(t, func(emit Emit) error { return Grok(chatPath, summaryPath, emit) })
	if len(records) != 2 {
		t.Fatalf("got %d Grok records, want 2", len(records))
	}
	if records[0].SessionID != "grok-session" || records[0].CWD != "/workspace" || records[0].Title != "Grok title" {
		t.Fatalf("unexpected Grok metadata: %+v", records[0])
	}
	if records[1].Role != "assistant" || records[1].Text != "grok answer" {
		t.Fatalf("unexpected Grok assistant: %+v", records[1])
	}
}

func TestCodexRecordsWithoutFinalNewline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-123e4567-e89b-12d3-a456-426614174000.jsonl")
	writeJSONL(t, path, []map[string]any{
		{"type": "noise", "payload": map[string]any{"type": "other"}},
		{"type": "session_meta", "payload": map[string]any{"id": "codex-session", "cwd": "/codex", "timestamp": "2024-01-02T00:00:00Z"}},
		{"type": "message", "timestamp": "2024-01-02T00:01:00Z", "payload": map[string]any{"type": "message", "role": "user", "content": "codex question"}},
		{"type": "message", "timestamp": "2024-01-02T00:02:00Z", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"text": "codex answer"}}}},
	}, false)
	records := collect(t, func(emit Emit) error { return Codex(path, emit) })
	if len(records) != 2 {
		t.Fatalf("got %d Codex records, want 2", len(records))
	}
	if records[0].SessionID != "codex-session" || records[0].CWD != "/codex" || records[0].Role != "user" {
		t.Fatalf("unexpected Codex record: %+v", records[0])
	}
}

func TestKimiRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session_k1", "agents", "agent_a", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, []map[string]any{
		{"type": "other", "message": map[string]any{"content": "ignored"}},
		{"type": "context.append_message", "time": "2024-01-02T00:00:00Z", "message": map[string]any{
			"role": "assistant", "origin": map[string]any{"kind": "user"}, "content": "Workspace Path: /kimi\nkimi question",
		}},
		{"type": "context.append_message", "time": "2024-01-02T00:01:00Z", "message": map[string]any{
			"role": "assistant", "content": "kimi answer",
		}},
	}, true)
	records := collect(t, func(emit Emit) error { return Kimi(path, emit) })
	if len(records) != 2 {
		t.Fatalf("got %d Kimi records, want 2", len(records))
	}
	want := []record.MessageRecord{
		{Tool: "kimi-code", SessionID: "session_k1", CWD: "/kimi", Role: "user", Text: "Workspace Path: /kimi\nkimi question", SourcePath: path},
		{Tool: "kimi-code", SessionID: "session_k1", CWD: "/kimi", Role: "assistant", Text: "kimi answer", SourcePath: path},
	}
	for i := range want {
		if records[i].Tool != want[i].Tool || records[i].SessionID != want[i].SessionID || records[i].CWD != want[i].CWD || records[i].Role != want[i].Role || records[i].Text != want[i].Text || records[i].SourcePath != want[i].SourcePath {
			t.Fatalf("record %d = %+v, want %+v", i, records[i], want[i])
		}
	}
}

func TestClaudeRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "project", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, []map[string]any{
		{"type": "file-history-snapshot", "message": map[string]any{"content": "ignored"}},
		{"type": "user", "sessionId": "claude-session", "cwd": "/claude", "timestamp": "2024-01-02T00:00:00Z", "message": map[string]any{"role": "user", "content": "claude question"}},
		{"type": "assistant", "sessionId": "claude-session", "cwd": "/claude", "timestamp": "2024-01-02T00:01:00Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": "claude answer"}}}},
	}, true)
	records := collect(t, func(emit Emit) error { return Claude(path, emit) })
	if got := []string{records[0].Role, records[1].Role}; !reflect.DeepEqual(got, []string{"user", "assistant"}) {
		t.Fatalf("roles = %#v", got)
	}
	if records[0].SessionID != "claude-session" || records[1].CWD != "/claude" {
		t.Fatalf("unexpected Claude records: %+v", records)
	}
}

func TestOpencodeRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode.db")
	db, err := sql.Open("sqlite", fileURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT, title TEXT, time_updated INTEGER);
CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT);
CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT, time_created INTEGER, data TEXT);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	messageData, _ := json.Marshal(map[string]any{"role": "user"})
	for _, row := range []struct {
		id, messageID, text string
		time                int
	}{
		{"p1", "m1", "hello", 1704067202},
		{"p2", "m1", "world", 1704067203},
	} {
		if _, err := db.Exec(`INSERT INTO part(id, message_id, time_created, data) VALUES (?, ?, ?, ?)`, row.id, row.messageID, row.time, mustJSON(map[string]any{"text": row.text})); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO session VALUES ('oc-session','/opencode','OC title',1704067200);
INSERT INTO message VALUES ('m1','oc-session',1704067201,?)`, string(messageData))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	records := collect(t, func(emit Emit) error { return Opencode(path, "", emit) })
	if len(records) != 1 {
		t.Fatalf("got %d OpenCode records, want 1", len(records))
	}
	if records[0].Text != "hello\nworld" || records[0].Role != "user" || records[0].Title != "OC title" {
		t.Fatalf("unexpected OpenCode record: %+v", records[0])
	}
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
