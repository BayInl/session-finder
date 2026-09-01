package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/record"
)

func TestColorEnabledPolicy(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	if ColorEnabled(&buf) {
		t.Fatal("NO_COLOR must win over CLICOLOR_FORCE")
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "dumb")
	if !ColorEnabled(&buf) {
		t.Fatal("CLICOLOR_FORCE should color a non-TTY")
	}

	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if ColorEnabled(&buf) {
		t.Fatal("TERM=dumb should disable color without FORCE")
	}

	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	if ColorEnabled(&buf) {
		t.Fatal("non-TTY should not enable color without FORCE")
	}
}

func TestWrapLinesRespectsMaxAndWidth(t *testing.T) {
	lines := wrapLines("hello world from search", 8, 3)
	if len(lines) == 0 {
		t.Fatal("wrapLines returned no lines")
	}
	for _, line := range lines {
		if DisplayWidth(line) > 8 {
			t.Fatalf("line wider than budget: %q width=%d", line, DisplayWidth(line))
		}
	}
	if got := wrapLines("", 8, 3); got != nil {
		t.Fatalf("empty wrap = %#v", got)
	}
}

func TestTruncateCJKAndStripANSI(t *testing.T) {
	if got := Truncate("abcdefgh", 5); got != "abcd…" {
		t.Fatalf("Truncate ascii = %q", got)
	}
	if got := Truncate("你好世界", 5); got != "你好…" {
		t.Fatalf("Truncate CJK = %q", got)
	}
	if DisplayWidth(Truncate("你好世界", 5)) > 5 {
		t.Fatalf("truncated CJK width = %d", DisplayWidth(Truncate("你好世界", 5)))
	}
	value := "safe\x1b[31m red\x1b[0m\x00\nnext\x7f"
	if got := StripANSI(value); got != "safe red next" {
		t.Fatalf("StripANSI = %q", got)
	}
	osc := "before\x1b]8;;https://example.test\x07label\x1b]8;;\x07after"
	if got := StripANSI(osc); got != "beforelabelafter" {
		t.Fatalf("StripANSI OSC = %q", got)
	}
}

func TestPlainFieldPathSummaryRelativeTime(t *testing.T) {
	if got := PlainField("  hello\nworld\t"); got != "hello world" {
		t.Fatalf("PlainField = %q", got)
	}
	if got := PathSummary([]string{"/one", "/two", "/three"}); got != "/one (+2)" {
		t.Fatalf("PathSummary = %q", got)
	}
	if got := RelativeTime("-"); got != "-" {
		t.Fatalf("RelativeTime(-) = %q", got)
	}
}

func TestPythonQuote(t *testing.T) {
	if got := PythonQuote("alpha"); got != "'alpha'" {
		t.Fatalf("PythonQuote = %q", got)
	}
}

func TestRenderSearchPipeHasNoANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	hits := []SearchHit{{
		Tool: "codex", SessionID: "session-1", Title: "Question",
		Snippets: []string{"alpha snippet"}, SourcePaths: []string{"/one"},
		MessageCount: 2, Updated: "2024-01-01T00:00:00Z",
	}}
	var buf bytes.Buffer
	RenderSearch(&buf, "alpha", hits, true)
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("pipe search leaked ANSI: %q", out)
	}
	if !strings.HasPrefix(out, "search: 'alpha' (1 sessions)\n") {
		t.Fatalf("header = %q", out)
	}
	if strings.Contains(out, "title:") && strings.Contains(out, "\n  title:") {
		t.Fatalf("verbose expanded on pipe: %q", out)
	}
	if !strings.Contains(out, "1. [codex] session-1 | title=Question | snippet=alpha snippet | path=/one | messages=2 | updated=2024-01-01T00:00:00Z") {
		t.Fatalf("one-liner missing: %q", out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 1 {
		t.Fatalf("want header + one compact line, got %q", out)
	}
}

func TestWriteLastRoundShowsUserAndAssistant(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	theme := NewTheme(&buf)
	writeLastRound(&buf, SearchHit{
		LastUser:      "please search hello in the index",
		LastAssistant: "I found three matching sessions",
	}, "hello", theme, theme.Style(TokenPrimary), 80, 4)
	out := buf.String()
	if !strings.Contains(out, "user: please search hello in the index") {
		t.Fatalf("missing user line: %q", out)
	}
	if !strings.Contains(out, "assistant: I found three matching sessions") {
		t.Fatalf("missing assistant line: %q", out)
	}
}

func TestWriteLastRoundDoesNotOverflowWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	width := 80
	longASCII := strings.Repeat("abcdefghij ", 40)
	longCJK := strings.Repeat("中文测试内容宽度检查", 20)
	var buf bytes.Buffer
	theme := NewTheme(&buf)
	writeLastRound(&buf, SearchHit{LastUser: longASCII, LastAssistant: longCJK}, "ab", theme, theme.Style(TokenPrimary), width, 8)
	for i, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if DisplayWidth(line) > width {
			t.Fatalf("line %d width %d > %d: %q", i, DisplayWidth(line), width, line)
		}
	}
}

func TestWrapLinesBreaksOnSpace(t *testing.T) {
	lines := wrapLines("Connection closed by UNKNOWN port 65535 extra", 24, 4)
	if len(lines) < 2 {
		t.Fatalf("expected wrap: %#v", lines)
	}
	for _, line := range lines {
		if DisplayWidth(line) > 24 {
			t.Fatalf("over-wide %q", line)
		}
		if strings.HasPrefix(line, "osed") || strings.HasPrefix(line, "tion") {
			t.Fatalf("mid-word leftover %q in %#v", line, lines)
		}
	}
}

func TestRenderShowPipeStable(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "")
	var buf bytes.Buffer
	RenderShow(&buf, []ShowMessage{{
		Tool: "codex", SessionID: "session-1", Title: "Question", CWD: "/workspace/project",
		Role: "user", Timestamp: "2024-01-01T00:00:01Z", Text: "find alpha here",
	}})
	want := "=== [codex] session-1 ===\ntitle: Question\ncwd: /workspace/project\n\n[2024-01-01T00:00:01Z] user\nfind alpha here\n"
	if buf.String() != want {
		t.Fatalf("show pipe = %q, want %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatal("show pipe leaked ANSI")
	}
}

func TestWriteJSONNoHTMLEscape(t *testing.T) {
	var buf bytes.Buffer
	payload := struct {
		Query   string   `json:"query"`
		Count   int      `json:"count"`
		Results []string `json:"results"`
	}{Query: "a<b", Count: 0, Results: []string{}}
	if err := WriteJSON(&buf, payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `\u003c`) {
		t.Fatalf("HTML escaped: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"results": []`) {
		t.Fatalf("empty results = %s", buf.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestPrintErrorPipeUnchanged(t *testing.T) {
	err := errors.New("--limit must be positive\n(use --help for usage)")
	var buf bytes.Buffer
	PrintError(&buf, err)
	if buf.String() != err.Error()+"\n" {
		t.Fatalf("pipe error = %q", buf.String())
	}
}

func TestPrintVersionNonTTY(t *testing.T) {
	var buf bytes.Buffer
	PrintVersion(&buf, "v1.2.3", "abc123", "2026-08-31T10:11:12Z")
	want := "session-finder version v1.2.3\ncommit: abc123\ndate: 2026-08-31T10:11:12Z\n"
	if buf.String() != want {
		t.Fatalf("PrintVersion = %q", buf.String())
	}
}

func TestPrintRootUsageFlagsOnce(t *testing.T) {
	var buf bytes.Buffer
	PrintRootUsage(&buf, []string{"index", "search", "show"}, "blurb")
	if got := strings.Count(buf.String(), "[flags]"); got != 1 {
		t.Fatalf("[flags] count = %d: %q", got, buf.String())
	}
}

func TestIndexProgressNonTTYWarning(t *testing.T) {
	var buf bytes.Buffer
	p := NewIndexProgress(&buf)
	p.Report(1, 1, record.SourceSpec{Tool: "codex", Path: "/tmp/session.jsonl"}, io.EOF)
	p.Close()
	if !strings.Contains(buf.String(), "warning: failed to index /tmp/session.jsonl: EOF") {
		t.Fatalf("warning = %q", buf.String())
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("non-TTY warning leaked ANSI: %q", buf.String())
	}
}

func TestShortID(t *testing.T) {
	if got := ShortID("550e8400-e29b-41d4-a716-446655440000"); got != "550e8400" {
		t.Fatalf("uuid = %q", got)
	}
	if got := ShortID("session_ad6f2fc9-1781-44dd-9ffc-70f88ecb58da"); got != "ad6f2fc9" {
		t.Fatalf("prefixed uuid = %q", got)
	}
	if got := ShortID("session-1"); got != "session-1" {
		t.Fatalf("short = %q", got)
	}
	if got := ShortID("session-12345"); got != "session-123…" {
		t.Fatalf("prefix = %q", got)
	}
}

func TestPrintErrorSkipsPrintedFlagError(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, Printed(errors.New("flag provided but not defined: -x")))
	if buf.Len() != 0 {
		t.Fatalf("reprinted flag error: %q", buf.String())
	}
}

func TestPrintUsageShowsNonZeroDefaults(t *testing.T) {
	set := flag.NewFlagSet("search", flag.ContinueOnError)
	set.Int("limit", 20, "maximum sessions to show")
	set.Bool("json", false, "emit JSON")
	set.String("db", "", "path to the SQLite index database")
	var buf bytes.Buffer
	PrintUsage(&buf, "usage: session-finder search <query> [flags]", "Search indexed session transcripts.", set, []FlagGroup{
		{Title: "Filter", Names: []string{"limit"}},
		{Title: "Output", Names: []string{"json"}},
		{Title: "Database", Names: []string{"db"}},
	})
	out := buf.String()
	if !strings.Contains(out, "--limit int") || !strings.Contains(out, "(default 20)") {
		t.Fatalf("limit default missing: %q", out)
	}
	if strings.Contains(out, "(default false)") {
		t.Fatalf("bool zero default shown: %q", out)
	}
	if strings.Contains(out, `(default "")`) || strings.Contains(out, "(default )") {
		t.Fatalf("empty string default shown: %q", out)
	}
}

func TestRenderTableColorsStatusAfterTruncate(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	var buf bytes.Buffer
	RenderTable(&buf, Table{
		Headers:   []string{"ID", "STATUS", "TITLE"},
		Rows:      [][]string{{"candidate-1", "rejected", "Use SQLite"}},
		StatusCol: 1,
	})
	out := buf.String()
	if !strings.Contains(out, "rejected") {
		t.Fatalf("missing status: %q", out)
	}
	if !strings.Contains(out, "\x1b") {
		t.Fatalf("status column not colored: %q", out)
	}
	styled := NewTheme(io.Discard).Status("rejected").Render("rejected")
	if !strings.Contains(out, styled) {
		t.Fatalf("status style missing: styled=%q out=%q", styled, out)
	}
}

func TestRenderTableZeroStatusColDoesNotColorID(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	var buf bytes.Buffer
	RenderTable(&buf, Table{
		Headers: []string{"ID", "TITLE"},
		Rows:    [][]string{{"candidate-1", "Use SQLite"}},
	})
	styled := NewTheme(io.Discard).Status("candidate-1").Render("candidate-1")
	if strings.Contains(buf.String(), styled) {
		t.Fatalf("unset StatusCol colored column 0: %q", buf.String())
	}
}

func TestHighlightAfterTruncate(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	theme := NewTheme(io.Discard)
	got := Highlight("hello alpha world", "alpha", theme.Style(TokenPrimary))
	if !strings.Contains(got, "alpha") {
		t.Fatalf("highlight missing term: %q", got)
	}
	if !strings.Contains(got, "\x1b") {
		t.Fatalf("FORCE should color highlight: %q", got)
	}
	plain := Truncate("hello alpha world", 20)
	styled := Highlight(plain, "alpha", theme.Style(TokenPrimary))
	if DisplayWidth(styled) != DisplayWidth(plain) {
		t.Fatalf("highlight changed width: %d vs %d", DisplayWidth(styled), DisplayWidth(plain))
	}
}
