package skill

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquirePendingLockSerializesSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	unlock, err := acquirePendingLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		second, err := acquirePendingLock(context.Background(), path)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		acquired <- second
	}()
	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case second := <-acquired:
		second()
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire did not proceed after the first lock was released")
	}
}

func TestAcquirePendingLockRespectsContextCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.db")
	unlock, err := acquirePendingLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := acquirePendingLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire error = %v, want context deadline", err)
	}
}

func TestAcquirePendingLockAllowsDistinctPaths(t *testing.T) {
	root := t.TempDir()
	first, err := acquirePendingLock(context.Background(), filepath.Join(root, "one.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := acquirePendingLock(context.Background(), filepath.Join(root, "two.db"))
	if err != nil {
		t.Fatal(err)
	}
	second()
}
