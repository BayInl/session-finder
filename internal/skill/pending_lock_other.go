//go:build !darwin && !linux

package skill

import "context"

// acquirePendingLock falls back to the in-process gate on platforms without
// flock support; cross-process serialization is unavailable there.
func acquirePendingLock(ctx context.Context, candidatePath string) (func(), error) {
	if candidatePath == "" {
		candidatePath = "default"
	}
	return acquireProcessPendingLock(ctx, candidatePath)
}
