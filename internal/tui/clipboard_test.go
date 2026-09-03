package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestOSC52Sequence(t *testing.T) {
	sequence, chars, truncated := osc52Sequence("hello 世界", maxOSC52Bytes)
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello 世界")) + "\x07"
	if sequence != want {
		t.Fatalf("sequence = %q want %q", sequence, want)
	}
	if chars != 8 || truncated {
		t.Fatalf("chars=%d truncated=%v", chars, truncated)
	}
}

func TestOSC52SequenceTruncatesOnUTF8Boundary(t *testing.T) {
	sequence, chars, truncated := osc52Sequence("ab世界", 5)
	wantPayload := "ab世"
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(wantPayload)) + "\x07"
	if sequence != want {
		t.Fatalf("sequence = %q want %q", sequence, want)
	}
	if chars != 3 || !truncated {
		t.Fatalf("chars=%d truncated=%v", chars, truncated)
	}
}

func TestSystemClipboardPrefersOSC52(t *testing.T) {
	var out bytes.Buffer
	pbcopyCalled := false
	clipboard := systemClipboard{
		out:      &out,
		term:     "xterm-kitty",
		goos:     "darwin",
		maxBytes: maxOSC52Bytes,
		pbcopy: func(string) error {
			pbcopyCalled = true
			return nil
		},
	}
	copied, err := clipboard.Copy("hello")
	if err != nil {
		t.Fatal(err)
	}
	if copied != 5 || pbcopyCalled {
		t.Fatalf("copied=%d pbcopyCalled=%v", copied, pbcopyCalled)
	}
	if !strings.HasPrefix(out.String(), "\x1b]52;c;") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSystemClipboardFallsBackToPBCopy(t *testing.T) {
	var got string
	clipboard := systemClipboard{
		out:      &bytes.Buffer{},
		term:     "linux",
		goos:     "darwin",
		maxBytes: maxOSC52Bytes,
		pbcopy: func(text string) error {
			got = text
			return nil
		},
	}
	copied, err := clipboard.Copy("fallback 世界")
	if err != nil {
		t.Fatal(err)
	}
	if copied != 11 || got != "fallback 世界" {
		t.Fatalf("copied=%d text=%q", copied, got)
	}
}

func TestSystemClipboardFallsBackAfterOSC52WriteFailure(t *testing.T) {
	var got string
	clipboard := systemClipboard{
		out:      failingWriter{},
		term:     "xterm-256color",
		goos:     "darwin",
		maxBytes: maxOSC52Bytes,
		pbcopy: func(text string) error {
			got = text
			return nil
		},
	}
	if _, err := clipboard.Copy("fallback"); err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Fatalf("pbcopy text = %q", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
