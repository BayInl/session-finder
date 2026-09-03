package skill

import (
	"context"
	"sync"
)

// pendingProcessLocks holds one buffered gate channel per lock key. Receiving
// from the gate acquires the lock; sending back releases it.
var pendingProcessLocks sync.Map

// acquireProcessPendingLock serializes pending extraction within this process.
// It exists because candidate slug allocation now snapshots the candidate list
// once per scan instead of re-listing before every persist, so two overlapping
// scans in one process would otherwise allocate identical slugs and duplicate
// candidates. The returned function releases the lock.
func acquireProcessPendingLock(ctx context.Context, key string) (func(), error) {
	created := make(chan struct{}, 1)
	created <- struct{}{}
	value, _ := pendingProcessLocks.LoadOrStore(key, created)
	gate := value.(chan struct{})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
		return func() { gate <- struct{}{} }, nil
	}
}
