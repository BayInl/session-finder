package index

import (
	"path/filepath"
	"testing"
)

func TestAttachLastRoundsUsesNewestUserAndAssistant(t *testing.T) {
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
		VALUES ('codex', 'session-1', '/workspace', 'Q', 1, 4, '/one');
	INSERT INTO messages(session_pk, role, ts, text) VALUES
		(1, 'user', 1, 'old user'),
		(1, 'assistant', 2, 'old assistant'),
		(1, 'user', 3, 'latest user'),
		(1, 'assistant', 4, 'latest assistant'),
		(1, 'system', 5, 'ignore me');`)
	if err != nil {
		t.Fatal(err)
	}
	results := []SearchResult{{Tool: "codex", SessionID: "session-1"}}
	if err := AttachLastRounds(db, results); err != nil {
		t.Fatal(err)
	}
	if results[0].LastUser != "latest user" || results[0].LastAssistant != "latest assistant" {
		t.Fatalf("last round = %#v", results[0])
	}
}

func TestAttachLastRoundsSkipsToolCalls(t *testing.T) {
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
		VALUES ('kimi-code', 'session-2', '/workspace', 'Q', 1, 4, '/one');
	INSERT INTO messages(session_pk, role, ts, text) VALUES
		(1, 'user', 1, 'how do we polish the CLI?'),
		(1, 'assistant', 2, 'use lipgloss for TTY output'),
		(1, 'assistant', 3, 'tool.call Bash {"command":"ls"}'),
		(1, 'user', 4, 'tool.result {"stdout":"ok"}');`)
	if err != nil {
		t.Fatal(err)
	}
	results := []SearchResult{{Tool: "kimi-code", SessionID: "session-2"}}
	if err := AttachLastRounds(db, results); err != nil {
		t.Fatal(err)
	}
	if results[0].LastUser != "how do we polish the CLI?" || results[0].LastAssistant != "use lipgloss for TTY output" {
		t.Fatalf("last round = %#v", results[0])
	}
}
