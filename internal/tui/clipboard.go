package tui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"
)

const maxOSC52Bytes = 100_000

var errClipboardUnsupported = errors.New("clipboard is not supported by this terminal")

type clipboardWriter interface {
	Copy(text string) (int, error)
}

type systemClipboard struct {
	out      io.Writer
	term     string
	goos     string
	maxBytes int
	pbcopy   func(string) error
}

func newSystemClipboard(out io.Writer) clipboardWriter {
	return systemClipboard{
		out:      out,
		term:     os.Getenv("TERM"),
		goos:     runtime.GOOS,
		maxBytes: maxOSC52Bytes,
		pbcopy:   runPBCopy,
	}
}

func (c systemClipboard) Copy(text string) (int, error) {
	sequence, copied, _ := osc52Sequence(text, c.maxBytes)
	var oscErr error
	if supportsOSC52(c.term) && c.out != nil {
		var written int
		written, oscErr = io.WriteString(c.out, sequence)
		if oscErr == nil && written != len(sequence) {
			oscErr = io.ErrShortWrite
		}
		if oscErr == nil {
			return copied, nil
		}
	}
	if c.goos == "darwin" && c.pbcopy != nil {
		if err := c.pbcopy(text); err != nil {
			if oscErr != nil {
				return 0, fmt.Errorf("OSC52: %v; pbcopy: %w", oscErr, err)
			}
			return 0, fmt.Errorf("pbcopy: %w", err)
		}
		return utf8.RuneCountInString(text), nil
	}
	if oscErr != nil {
		return 0, fmt.Errorf("OSC52: %w", oscErr)
	}
	return 0, errClipboardUnsupported
}

func supportsOSC52(term string) bool {
	switch strings.ToLower(strings.TrimSpace(term)) {
	case "dumb", "linux":
		return false
	default:
		return true
	}
}

func osc52Sequence(text string, maxBytes int) (sequence string, chars int, truncated bool) {
	if maxBytes <= 0 {
		maxBytes = maxOSC52Bytes
	}
	payload := text
	if len(payload) > maxBytes {
		end := maxBytes
		for end > 0 && !utf8.ValidString(payload[:end]) {
			end--
		}
		payload = payload[:end]
		truncated = true
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	return "\x1b]52;c;" + encoded + "\x07", utf8.RuneCountInString(payload), truncated
}

func runPBCopy(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
