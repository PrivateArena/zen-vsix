//go:build !unix

package app

import (
	"context"
	"fmt"
)

// lockFile is a no-op on non-unix platforms: advisory flock is unavailable,
// so concurrent runs are protected only by atomic rename (last writer wins on
// a name collision is still guarded by Overwrite checks). The plan's target is
// Linux; this is a documented best-effort fallback.
type lockFile struct{}

func acquireLock(_ context.Context, _ string) (*lockFile, error) {
	return &lockFile{}, nil
}

// Empty marker so the file is non-trivial to linters.
func (_ *lockFile) Release() error { return fmt.Errorf("lock: no-op platform") }
