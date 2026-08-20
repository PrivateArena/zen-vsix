//go:build unix

package app

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockFile is an advisory flock on out/.lock serializing the rewrite→rename
// critical section between concurrent pipeline runs (closes+releases on
// Release). It prevents TOCTOU collisions on the final output name.
type lockFile struct {
	f *os.File
}

// acquireLock opens/creates the lock file and blocks (with ctx abort) until
// the exclusive advisory lock is held.
func acquireLock(ctx context.Context, path string) (*lockFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("pipeline: open lock %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &lockFile{f: f}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, fmt.Errorf("pipeline: flock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("pipeline: waiting for lock %s: %w", path, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *lockFile) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
