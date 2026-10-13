package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireCreatesDirAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "a.lock")
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = release()
}

func TestAcquireTimesOutWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.lock")
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = Acquire(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestAcquireFailsWhenDirCannotBeCreated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), filepath.Join(file, "a.lock")); err == nil {
		t.Fatal("want error when the lock dir is a file")
	}
}

func TestAcquireFailsOnUnlockablePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Acquire(context.Background(), dir); err == nil {
		t.Fatal("want error when the lock path is a directory")
	}
}

func TestAcquireIsMutuallyExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.lock")
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			release, err := Acquire(context.Background(), path)
			if err != nil {
				t.Error(err)
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
			_ = release()
		})
	}
	wg.Wait()
	if maxInside.Load() != 1 {
		t.Fatalf("lock held by %d goroutines at once", maxInside.Load())
	}
}
