package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSystemClipboardUsesTeaSetClipboard(t *testing.T) {
	pbcopyCalled := false
	clipboard := systemClipboard{
		term:     "xterm-kitty",
		goos:     "darwin",
		maxBytes: maxOSC52Bytes,
		pbcopy: func(string) error {
			pbcopyCalled = true
			return nil
		},
	}
	plan := clipboard.Copy("hello 世界", 7)
	if plan.async || plan.chars != 8 || plan.truncated || plan.cmd == nil {
		t.Fatalf("plan = %#v", plan)
	}
	msg := plan.cmd()
	if _, raw := msg.(tea.RawMsg); raw {
		t.Fatalf("clipboard must not use tea.Raw: %T", msg)
	}
	if got := fmt.Sprint(msg); got != "hello 世界" {
		t.Fatalf("clipboard payload = %q", got)
	}
	if !strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg") {
		t.Fatalf("command returned %T, want bubbletea setClipboardMsg", msg)
	}
	if pbcopyCalled {
		t.Fatal("OSC52 path called pbcopy")
	}
}

func TestSystemClipboardTruncatesOnUTF8Boundary(t *testing.T) {
	clipboard := systemClipboard{term: "xterm-256color", goos: "linux", maxBytes: 5}
	plan := clipboard.Copy("ab世界", 1)
	if plan.chars != 3 || !plan.truncated {
		t.Fatalf("chars=%d truncated=%v", plan.chars, plan.truncated)
	}
	if got := fmt.Sprint(plan.cmd()); got != "ab世" {
		t.Fatalf("clipboard payload = %q", got)
	}
}

func TestSystemClipboardFallsBackToPBCopyCmd(t *testing.T) {
	var got string
	clipboard := systemClipboard{
		term:     "linux",
		goos:     "darwin",
		maxBytes: maxOSC52Bytes,
		pbcopy: func(text string) error {
			got = text
			return nil
		},
	}
	plan := clipboard.Copy("fallback 世界", 9)
	if !plan.async || plan.cmd == nil || got != "" {
		t.Fatalf("fallback plan=%#v text=%q", plan, got)
	}
	msg, ok := plan.cmd().(clipboardResultMsg)
	if !ok {
		t.Fatalf("fallback command returned %T", plan.cmd())
	}
	if msg.seq != 9 || msg.chars != 11 || msg.truncated || msg.err != nil || got != "fallback 世界" {
		t.Fatalf("result=%#v text=%q", msg, got)
	}
}

func TestSystemClipboardReportsUnsupported(t *testing.T) {
	clipboard := systemClipboard{term: "dumb", goos: "linux", maxBytes: maxOSC52Bytes}
	plan := clipboard.Copy("hello", 3)
	msg, ok := plan.cmd().(clipboardResultMsg)
	if !ok || msg.seq != 3 || !errors.Is(msg.err, errClipboardUnsupported) {
		t.Fatalf("result = %#v", msg)
	}
}
