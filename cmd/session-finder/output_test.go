package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BayInl/session-finder/internal/ui"
)

func TestTruncateCellsUsesTerminalWidth(t *testing.T) {
	if got := truncateCells("abcdefgh", 5); got != "abcd…" {
		t.Fatalf("truncateCells ascii = %q", got)
	}
	if got := truncateCells("你好世界", 5); got != "你好…" {
		t.Fatalf("truncateCells CJK = %q", got)
	}
	if displayWidth(truncateCells("你好世界", 5)) > 5 {
		t.Fatalf("truncated CJK width = %d", displayWidth(truncateCells("你好世界", 5)))
	}
}

func TestStripANSIAndControls(t *testing.T) {
	value := "safe\x1b[31m red\x1b[0m\x00\nnext\x7f"
	if got := stripANSIAndControls(value); got != "safe red next" {
		t.Fatalf("stripANSIAndControls = %q", got)
	}
	osc := "before\x1b]8;;https://example.test\x07label\x1b]8;;\x07after"
	if got := stripANSIAndControls(osc); got != "beforelabelafter" {
		t.Fatalf("stripANSIAndControls OSC = %q", got)
	}
	if strings.ContainsRune(stripANSIAndControls(value), '\x1b') {
		t.Fatal("ANSI escape survived")
	}
}

func TestPlainFieldAndPathSummary(t *testing.T) {
	if got := plainField("  hello\nworld\t"); got != "hello world" {
		t.Fatalf("plainField = %q", got)
	}
	if got := pathSummary([]string{"/one", "/two", "/three"}); got != "/one (+2)" {
		t.Fatalf("pathSummary = %q", got)
	}
}

func TestRelativeTimeInvalid(t *testing.T) {
	if got := relativeTime("-"); got != "-" {
		t.Fatalf("relativeTime(-) = %q", got)
	}
}

func TestColorTextHonorsNoColor(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	if got := ui.LegacySGR(&buf, "1", "text"); got != "text" {
		t.Fatalf("NO_COLOR=1 = %q", got)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	if got := ui.LegacySGR(&buf, "1", "text"); got != "text" {
		t.Fatalf("empty NO_COLOR non-TTY = %q", got)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	if got := ui.LegacySGR(&buf, "1", "text"); got != "\x1b[1mtext\x1b[0m" {
		t.Fatalf("CLICOLOR_FORCE = %q", got)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("TERM", "dumb")
	if got := ui.LegacySGR(&buf, "1", "text"); got != "text" {
		t.Fatalf("TERM=dumb = %q", got)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "dumb")
	if got := ui.LegacySGR(&buf, "1", "text"); got != "\x1b[1mtext\x1b[0m" {
		t.Fatalf("TERM=dumb FORCE = %q", got)
	}
}
