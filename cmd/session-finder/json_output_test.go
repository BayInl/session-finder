package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/decisions"
	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/index"
	"github.com/BayInl/session-finder/internal/skill"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	fn()
	_ = writer.Close()
	os.Stdout = original
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestSearchJSONUnchanged(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := index.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.InitializeSchema(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'session-1', '/workspace/project', 'Question', 1704067200, 1704067300, '/source/one');
	INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067201, 'find <alpha> here');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runSearch([]string{"alpha", "--json", "--db", path}); err != nil {
			t.Errorf("runSearch: %v", err)
		}
	})
	if strings.Contains(out, `\u003c`) {
		t.Fatalf("HTML escaped snippet: %s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("JSON leaked ANSI: %s", out)
	}
	var payload struct {
		Query   string               `json:"query"`
		Count   int                  `json:"count"`
		Results []index.SearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode: %v; output=%s", err, out)
	}
	if payload.Query != "alpha" || payload.Count != 1 || len(payload.Results) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
	got := payload.Results[0]
	if got.SessionID != "session-1" || got.Tool != "codex" {
		t.Fatalf("result = %#v", got)
	}
	if got.Title != "Question" || got.CWD != "/workspace/project" || got.MessageCount != 1 {
		t.Fatalf("result fields = %#v", got)
	}
	if got.Created == "" || got.Updated == "" || len(got.SourcePaths) == 0 {
		t.Fatalf("result timestamps/paths = %#v", got)
	}
	if len(got.Snippets) == 0 || !strings.Contains(got.Snippets[0], "<alpha>") {
		t.Fatalf("snippets = %#v", got.Snippets)
	}
	for _, key := range []string{
		`"query"`, `"count"`, `"results"`, `"tool"`, `"session_id"`, `"title"`,
		`"cwd"`, `"created"`, `"updated"`, `"message_count"`, `"snippets"`, `"source_paths"`,
	} {
		if !strings.Contains(out, key) {
			t.Fatalf("JSON missing %s: %s", key, out)
		}
	}

	empty := captureStdout(t, func() {
		if err := runSearch([]string{"zzzznotfound", "--json", "--db", path}); err != nil {
			t.Errorf("runSearch empty: %v", err)
		}
	})
	var emptyPayload struct {
		Query   string               `json:"query"`
		Count   int                  `json:"count"`
		Results []index.SearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(empty), &emptyPayload); err != nil {
		t.Fatalf("decode empty: %v; output=%s", err, empty)
	}
	if emptyPayload.Count != 0 || emptyPayload.Results == nil || len(emptyPayload.Results) != 0 {
		t.Fatalf("empty results = %#v (nil=%v)", emptyPayload.Results, emptyPayload.Results == nil)
	}
	if !strings.Contains(empty, `"results": []`) {
		t.Fatalf("empty results JSON = %s", empty)
	}
}

func TestSkillListTSVHasNoHeader(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	path := filepath.Join(t.TempDir(), "index.db")
	store, err := extract.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(context.Background(), extract.CandidateInput{
		ID: "candidate-1", SessionID: "session-1", Tool: "codex", Kind: "skill",
		Title: "Use SQLite", Payload: []byte(`{}`), Status: extract.StatusDetected,
	})
	store.Close()
	if err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := skill.RunCommand([]string{"list", "--db", path}); err != nil {
			t.Errorf("skill list: %v", err)
		}
	})
	if strings.Contains(out, "\x1b") {
		t.Fatalf("TSV leaked ANSI: %q", out)
	}
	if strings.HasPrefix(strings.ToUpper(out), "ID") {
		t.Fatalf("TSV has header: %q", out)
	}
	line := strings.TrimSuffix(out, "\n")
	fields := strings.Split(line, "\t")
	if len(fields) != 4 || fields[0] != "candidate-1" || fields[1] != extract.StatusDetected || fields[2] != "Use SQLite" || fields[3] != "session-1" {
		t.Fatalf("TSV = %q", out)
	}
}

func TestDecisionsListTSVHasNoHeader(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	path := filepath.Join(t.TempDir(), "index.db")
	store, err := decisions.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	out := captureStdout(t, func() {
		if err := decisions.RunCommand([]string{"list", "--db", path}); err != nil {
			t.Errorf("decisions list: %v", err)
		}
	})
	if strings.Contains(out, "\x1b") {
		t.Fatalf("TSV leaked ANSI: %q", out)
	}
	upper := strings.ToUpper(out)
	if strings.Contains(upper, "STATUS") || strings.Contains(upper, "CHOSEN") || strings.Contains(out, "No rows.") {
		t.Fatalf("pipe list used table/header: %q", out)
	}
}
