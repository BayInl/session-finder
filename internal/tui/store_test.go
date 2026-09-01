package tui

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/BayInl/session-finder/internal/index"
)

func testIndexDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := index.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := index.InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
VALUES
  ('codex', 'sess-a', '/workspace/alpha', 'Alpha', 1704067200, 1704067200, '/a'),
  ('grok', 'sess-b', '/tmp/beta', 'Beta', 1704153600, 1704153600, '/b'),
  ('codex', 'sess-c', '/workspace/gamma', 'Gamma', 1704240000, 1704240000, '/c');
INSERT INTO messages(session_pk, role, ts, text) VALUES
  (1, 'user', 1, 'alpha user'),
  (1, 'assistant', 2, 'alpha assistant'),
  (1, 'assistant', 3, 'tool.call Bash {"command":"ls"}'),
  (2, 'user', 1, 'beta user'),
  (2, 'assistant', 2, 'beta assistant'),
  (3, 'user', 1, 'gamma user'),
  (3, 'assistant', 2, 'gamma assistant');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestListRecentIncludesLastRound(t *testing.T) {
	db := testIndexDB(t)
	results, err := listRecent(db, "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d want 3: %#v", len(results), results)
	}
	if results[0].SessionID != "sess-c" || results[1].SessionID != "sess-b" || results[2].SessionID != "sess-a" {
		t.Fatalf("order = %q %q %q", results[0].SessionID, results[1].SessionID, results[2].SessionID)
	}
	if results[2].LastUser != "alpha user" || results[2].LastAssistant != "alpha assistant" {
		t.Fatalf("alpha last round = user %q assistant %q", results[2].LastUser, results[2].LastAssistant)
	}
	if results[1].LastUser != "beta user" || results[1].LastAssistant != "beta assistant" {
		t.Fatalf("beta last round = user %q assistant %q", results[1].LastUser, results[1].LastAssistant)
	}
	if results[0].LastUser != "gamma user" || results[0].LastAssistant != "gamma assistant" {
		t.Fatalf("gamma last round = user %q assistant %q", results[0].LastUser, results[0].LastAssistant)
	}
}

func TestListRecentFiltersAndLimit(t *testing.T) {
	db := testIndexDB(t)

	codex, err := listRecent(db, "codex", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 2 || codex[0].SessionID != "sess-c" || codex[1].SessionID != "sess-a" {
		t.Fatalf("tool filter = %#v", codex)
	}

	alpha, err := listRecent(db, "", "/workspace/alpha", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alpha) != 1 || alpha[0].SessionID != "sess-a" {
		t.Fatalf("cwd filter = %#v", alpha)
	}

	after, err := listRecent(db, "", "", "2024-01-02", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].SessionID != "sess-c" || after[1].SessionID != "sess-b" {
		t.Fatalf("after filter = %#v", after)
	}

	limited, err := listRecent(db, "", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].SessionID != "sess-c" {
		t.Fatalf("limit = %#v", limited)
	}

	if _, err := listRecent(db, "", "", "not-a-date", 10); err == nil {
		t.Fatal("expected after format error")
	}
}

func TestEmptyQuerySearchUsesListRecent(t *testing.T) {
	db := testIndexDB(t)
	backend := newDBBackend(db, Config{Limit: 10})
	results, err := backend.Search("", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("empty query len = %d", len(results))
	}
	if results[0].LastUser == "" || results[0].LastAssistant == "" {
		t.Fatalf("empty-query browse omitted last round: %#v", results[0])
	}
	hits := HitsFromResults(results)
	if hits[0].LastUser != results[0].LastUser || hits[0].LastAssistant != results[0].LastAssistant {
		t.Fatalf("hits dropped last round: %#v", hits[0])
	}
}
