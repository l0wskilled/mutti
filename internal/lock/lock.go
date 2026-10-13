// Package lock provides host-wide, cross-process locks backed by flock(2). The kernel releases
// them when the process dies, so there are no stale locks to detect.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const retryDelay = 50 * time.Millisecond

// Acquire blocks until the lock at path is held or ctx is done.
func Acquire(ctx context.Context, path string) (release func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	l := flock.New(path)
	// TryLockContext only returns ok=false together with an error (ctx done or flock failed).
	if _, err := l.TryLockContext(ctx, retryDelay); err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	return l.Unlock, nil
}
