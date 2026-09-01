//go:build !darwin && !linux

package tui

import (
	"os"
	"testing"
)

func openTestTTY(t *testing.T) *os.File {
	t.Helper()
	t.Skip("pty helper is implemented for darwin and linux")
	return nil
}
