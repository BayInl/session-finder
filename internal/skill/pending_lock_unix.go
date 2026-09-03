//go:build darwin || linux

package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// acquirePendingLock serializes pending extraction for one candidate database.
// It takes the in-process gate first, then a non-blocking flock on
// <candidatePath>.skill-pending.lock (retried until ctx is done) so concurrent
// sfind processes — the CLI and the session-end hook — cannot interleave
// scans. Scans snapshot the candidate list once for slug allocation, so an
// overlapping scan would allocate duplicate slugs and recreate candidates that
// the other scan already queued. The returned function releases both locks.
func acquirePendingLock(ctx context.Context, candidatePath string) (func(), error) {
	if candidatePath == "" {
		home, _ := os.UserHomeDir()
		candidatePath = filepath.Join(home, ".cache", "session-finder", "index.db")
	}
	unlockProcess, err := acquireProcessPendingLock(ctx, candidatePath)
	if err != nil {
		return nil, err
	}
	if candidatePath == ":memory:" || strings.HasPrefix(candidatePath, "file:") {
		return unlockProcess, nil
	}
	if err := os.MkdirAll(filepath.Dir(candidatePath), 0o755); err != nil {
		unlockProcess()
		return nil, err
	}
	file, err := os.OpenFile(candidatePath+".skill-pending.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		unlockProcess()
		return nil, err
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
				unlockProcess()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			unlockProcess()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			unlockProcess()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
