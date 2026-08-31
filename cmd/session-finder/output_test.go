package main

import (
	"strings"
	"testing"
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
	t.Setenv("NO_COLOR", "1")
	if got := colorText("1", "text"); got != "text" {
		t.Fatalf("colorText with NO_COLOR = %q", got)
	}
	t.Setenv("NO_COLOR", "")
	if got := colorText("1", "text"); got != "\x1b[1mtext\x1b[0m" {
		t.Fatalf("colorText = %q", got)
	}
}
