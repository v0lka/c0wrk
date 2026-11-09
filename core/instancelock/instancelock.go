// Package instancelock provides the advisory, process-lifetime OS lock
// guarding "the one c0wrk instance" for this user. It is transport-neutral:
// callers layer their own enforcement on top (the desktop app uses the Wails
// single-instance relay; a daemon takes the same primitive on its own
// lock path). See specs/decisions/075-single-instance-lock.md (D3) and
// specs/domains/crash-logging.md. The zero value is not a usable lock;
// AcquireSingleInstanceLock constructs it.
package instancelock

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// lockPollInterval is the lock-wait poll period used while waiting for a
// previous holder to release the lock (the relaunched child's poll period).
const lockPollInterval = 100 * time.Millisecond

// InstanceLock is the exclusively-held, process-lifetime advisory OS lock
// guarding "the one c0wrk instance" for this user: its job is to let a second
// process know, BEFORE it touches shared state, that it must stay read-only
// toward ~/.c0wrk — above all the crash-capture liveness marker, which a
// transient relay would otherwise poison. See
// specs/decisions/075-single-instance-lock.md (D3) and
// specs/domains/crash-logging.md. The zero value is not a usable lock;
// AcquireSingleInstanceLock constructs it.
type InstanceLock struct {
	file *os.File
}

// AcquireSingleInstanceLock opens (creating if needed) the lock file at path
// and tries to take an exclusive, non-blocking OS lock on it.
//
//   - (lock, true, nil): acquired — keep the lock reachable for the process
//     lifetime (a finalizer would release it under GC).
//   - (nil, false, nil): held by another process — the caller's relay path
//     decides what a second instance does.
//   - (nil, true, err): setup failed — FAILS OPEN to "first instance": a
//     wedged lock file must never brick startup.
func AcquireSingleInstanceLock(path string) (*InstanceLock, bool, error) {
	// MkdirAllReal + OpenFileNoFollow (not the symlink-following
	// MkdirAll/safeio.OpenFile): the fixed, predictable app.lock path must not
	// take its O_CREATE or flock through a planted symlink — a dangling link
	// is refused outright, a link to a regular file fails the open with ELOOP
	// (no-follow on the final component; Windows still resolves it, tempered
	// by symlink creation requiring elevated rights there), and MkdirAllReal
	// refuses a link swapped into the directory it creates. A pre-existing
	// symlinked directory component is operator intent, and the fail-open
	// contract above still applies.
	if err := safeio.MkdirAllReal(filepath.Dir(path), 0o750); err != nil {
		return nil, true, fmt.Errorf("single-instance lock: creating lock directory: %w", err)
	}
	file, err := safeio.OpenFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, true, fmt.Errorf("single-instance lock: opening lock file: %w", err)
	}
	held, err := tryLockExclusive(file)
	if err != nil {
		_ = file.Close()
		return nil, true, fmt.Errorf("single-instance lock: locking lock file: %w", err)
	}
	if !held {
		_ = file.Close()
		return nil, false, nil
	}
	return &InstanceLock{file: file}, true, nil
}

// Close releases the lock; the OS drops it on every exit path regardless.
func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// WaitForInstanceLockRelease polls the advisory single-instance lock until it
// can be taken — the previous holder exited — or the timeout expires. It is
// the ordering primitive behind the GPU-fallback relaunch (issue #34); on
// success the lock is released immediately, ownership being taken later by
// the normal startup acquire. Fail-open: a setup error keeps polling to the
// timeout and reports false. See specs/domains/webview-gpu-policy.md.
func WaitForInstanceLockRelease(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := safeio.MkdirAllReal(filepath.Dir(path), 0o750); err == nil {
			if file, err := safeio.OpenFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o640); err == nil {
				held, lockErr := tryLockExclusive(file)
				_ = file.Close()
				if lockErr == nil && held {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(lockPollInterval)
	}
}
