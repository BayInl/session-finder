package index

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseQueryFieldsBooleanAndEscapes(t *testing.T) {
	expr, filters, err := parseQuery(`tool:codex cwd:"/work space" after:2024-01-02 (alpha OR "beta gamma") NOT escaped\ AND`)
	if err != nil {
		t.Fatal(err)
	}
	if expr == nil {
		t.Fatal("parseQuery returned nil expression")
	}
	if !reflect.DeepEqual(filters.tools, []string{"codex"}) ||
		!reflect.DeepEqual(filters.cwds, []string{"/work space"}) ||
		!reflect.DeepEqual(filters.afters, []string{"2024-01-02"}) {
		t.Fatalf("filters = %#v", filters)
	}
	debug, err := queryDebugString(`tool:codex cwd:"/work space" after:2024-01-02 (alpha OR "beta gamma") NOT escaped\ AND`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(debug, "tool:codex") || !strings.Contains(debug, `"beta gamma"`) || !strings.Contains(debug, "NOT escaped AND") {
		t.Fatalf("debug query = %q", debug)
	}
}

func TestPositiveTermsSkipsBooleanAndFields(t *testing.T) {
	got := PositiveTerms(`hello AND NOT test tool:codex "exact phrase"`)
	want := []string{"exact phrase", "hello"}
	if len(got) != len(want) {
		t.Fatalf("PositiveTerms = %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PositiveTerms = %#v want %#v", got, want)
		}
	}
	orTerms := PositiveTerms("docker OR panic")
	if len(orTerms) != 2 || orTerms[0] != "docker" || orTerms[1] != "panic" {
		t.Fatalf("OR terms = %#v", orTerms)
	}
	grouped := PositiveTerms("hello AND NOT (foo OR bar)")
	if len(grouped) != 1 || grouped[0] != "hello" {
		t.Fatalf("grouped NOT = %#v", grouped)
	}
	if terms := PositiveTerms("alpha AND"); terms != nil {
		t.Fatalf("invalid query = %#v", terms)
	}
	if terms := PositiveTerms("tool:codex"); terms != nil {
		t.Fatalf("filter-only = %#v", terms)
	}
}

func TestParseQueryRejectsInvalidBooleanExpressions(t *testing.T) {
	for _, query := range []string{
		`NOT alpha`,
		`NOT (alpha)`,
		`alpha AND`,
		`alpha OR`,
		`(alpha OR beta`,
		`alpha beta)`,
		`"unterminated`,
		"alpha\\",
	} {
		if _, _, err := parseQuery(query); err == nil {
			t.Errorf("parseQuery(%q) unexpectedly succeeded", query)
		}
	}
}

func TestSearchBooleanPhraseAndFields(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'one', '/work/app', 'One', 1704067200, 1704067300, '/src/one');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067201, 'alpha beta phrase here');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'assistant', 1704067202, 'alpha only');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('grok', 'two', '/work/other', 'Two', 1704067200, 1704067400, '/src/two');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'user', 1704067203, 'beta phrase here');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'assistant', 1704067204, 'gamma only');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'three', '/other', 'Three', 1704067200, 1704067500, '/src/three');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (3, 'user', 1704067205, 'alpha forbidden');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (3, 'assistant', 1704067206, 'delta');
	`)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "and", query: "alpha beta", want: []string{"one"}},
		{name: "or", query: "alpha OR gamma", want: []string{"one", "three", "two"}},
		{name: "not", query: "alpha NOT forbidden", want: []string{"one"}},
		{name: "phrase", query: `"beta phrase"`, want: []string{"two", "one"}},
		{name: "field", query: `tool:codex alpha`, want: []string{"one", "three"}},
		{name: "quoted field", query: `cwd:"/work/app" alpha`, want: []string{"one"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := Search(db, tc.query, "", "", "", 20, false)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(results))
			for _, result := range results {
				got = append(got, result.SessionID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Search(%q) sessions = %#v, want %#v; results=%#v", tc.query, got, tc.want, results)
			}
		})
	}
}

func TestSearchCJKAndShortLiteralFallback(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'unicode', '/tmp', 'Unicode', 1704067200, 1704067200, '/src/unicode');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067200, '部署完成 ✅');
	`)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"部署完成", "✅", "部"} {
		results, err := Search(db, query, "", "", "", 20, false)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) != 1 || results[0].SessionID != "unicode" {
			t.Fatalf("Search(%q) = %#v", query, results)
		}
	}
}

func TestSearchCJKEmbeddedShortTermUnion(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'embedded', '/tmp', 'Embedded', 1704067200, 1704067200, '/src/embedded');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067200, '我们要部署到生产环境');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'isolated', '/tmp', 'Isolated', 1704067200, 1704067200, '/src/isolated');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'user', 1704067201, '请 部署 一下');
	`)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Search(db, "部署", "", "", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("Search(%q) returned %d results, want 2: %#v", "部署", len(results), results)
	}
	got := map[string]bool{}
	for _, result := range results {
		got[result.SessionID] = true
		if len(result.Snippets) == 0 || !strings.Contains(strings.Join(result.Snippets, " "), "部署") {
			t.Fatalf("Search(%q) result lacks matching snippet: %#v", "部署", result)
		}
	}
	if !got["embedded"] || !got["isolated"] {
		t.Fatalf("Search(%q) sessions = %#v, want embedded and isolated", "部署", got)
	}
}

func TestSearchLegacySessionANDAndPunctuationTokens(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitializeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'split', '/tmp', 'Split', 1704067200, 1704067200, '/tmp/split');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'user', 1704067200, 'apple in first message');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (1, 'assistant', 1704067201, 'banana in second message');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'punctuated', '/tmp/session-finder', 'Punctuated', 1704067200, 1704067200, '/session-finder/data');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'user', 1704067202, 'session appears here');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (2, 'assistant', 1704067203, 'finder appears there');
		INSERT INTO sessions(tool, session_id, cwd, title, created, updated, source_path)
		VALUES ('codex', 'partial', '/tmp', 'Partial', 1704067200, 1704067200, '/tmp/partial');
		INSERT INTO messages(session_pk, role, ts, text) VALUES (3, 'user', 1704067204, 'session only');
	`)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Search(db, "apple banana", "", "", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SessionID != "split" || results[0].MessageCount != 2 {
		t.Fatalf("session-level AND results = %#v", results)
	}
	if len(results[0].Snippets) != 2 || !strings.Contains(strings.Join(results[0].Snippets, " "), "apple") ||
		!strings.Contains(strings.Join(results[0].Snippets, " "), "banana") {
		t.Fatalf("session-level AND snippets = %#v", results[0].Snippets)
	}

	results, err = Search(db, "session-finder", "", "", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SessionID != "punctuated" || results[0].MessageCount != 2 {
		t.Fatalf("punctuation-token results = %#v", results)
	}
	if strings.Contains(strings.Join(results[0].Snippets, " "), "session only") {
		t.Fatalf("punctuation snippets contain unrelated message: %#v", results[0].Snippets)
	}
}
