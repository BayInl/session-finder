//go:build darwin

package tui

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/BayInl/session-finder/internal/ui"
)

func openTestTTY(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open ptmx: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	_ = unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYGRANT, 0)
	if err := unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYUNLK, 0); err != nil {
		t.Fatalf("unlock ptmx: %v", err)
	}
	var raw [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&raw[0])))
	if errno != 0 {
		t.Fatalf("ptsname: %v", errno)
	}
	n := 0
	for n < len(raw) && raw[n] != 0 {
		n++
	}
	slave, err := os.OpenFile(string(raw[:n]), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open pty slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if !ui.IsTTY(slave) {
		t.Fatal("pty slave is not a TTY")
	}
	return slave
}
