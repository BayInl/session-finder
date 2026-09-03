package tui

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

const maxOSC52Bytes = 100_000

var errClipboardUnsupported = errors.New("clipboard is not supported by this terminal")

type clipboardResultMsg struct {
	seq       uint64
	chars     int
	truncated bool
	err       error
}

type clipboardPlan struct {
	cmd       tea.Cmd
	chars     int
	truncated bool
	async     bool
}

type clipboardWriter interface {
	Copy(text string, seq uint64) clipboardPlan
}

type systemClipboard struct {
	term     string
	goos     string
	maxBytes int
	pbcopy   func(string) error
}

func newSystemClipboard() clipboardWriter {
	return systemClipboard{
		term:     os.Getenv("TERM"),
		goos:     runtime.GOOS,
		maxBytes: maxOSC52Bytes,
		pbcopy:   runPBCopy,
	}
}

func (c systemClipboard) Copy(text string, seq uint64) clipboardPlan {
	if supportsOSC52(c.term) {
		payload, chars, truncated := truncateClipboardText(text, c.maxBytes)
		return clipboardPlan{
			cmd:       tea.SetClipboard(payload),
			chars:     chars,
			truncated: truncated,
		}
	}
	if c.goos == "darwin" && c.pbcopy != nil {
		return clipboardPlan{
			async: true,
			cmd: func() tea.Msg {
				return clipboardResultMsg{
					seq:   seq,
					chars: utf8.RuneCountInString(text),
					err:   c.pbcopy(text),
				}
			},
		}
	}
	return clipboardPlan{
		async: true,
		cmd: func() tea.Msg {
			return clipboardResultMsg{seq: seq, err: errClipboardUnsupported}
		},
	}
}

func supportsOSC52(term string) bool {
	switch strings.ToLower(strings.TrimSpace(term)) {
	case "dumb", "linux":
		return false
	default:
		return true
	}
}

func truncateClipboardText(text string, maxBytes int) (payload string, chars int, truncated bool) {
	if maxBytes <= 0 {
		maxBytes = maxOSC52Bytes
	}
	payload = text
	if len(payload) > maxBytes {
		end := maxBytes
		for end > 0 && !utf8.ValidString(payload[:end]) {
			end--
		}
		payload = payload[:end]
		truncated = true
	}
	return payload, utf8.RuneCountInString(payload), truncated
}

func runPBCopy(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
