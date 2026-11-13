package lexical

// The lock gate serializes bleve opens of one index directory. A bleve open
// itself blocks on bbolt's own file lock with NO timeout — a stuck or foreign
// holder would hang the open forever. The gate is a bounded replica of that
// lock: a sentinel file (gateFileName) inside the index directory is flocked
// before bleve is invoked (exclusive for read-write opens, shared for
// read-only ones) and held until the index closes, so a second opener
// refuses within gateWait instead of waiting on bbolt eternally.
//
// The sentinel lives INSIDE the index directory on purpose: every quarantine
// scope (ADR-120) removes the whole lexical leg, so the doomed gate dies with
// it — the repair's rebuild lands on a fresh directory (a new inode) whose
// gate is free, while the stuck holder keeps its fds on the orphaned old
// inodes and cannot wedge the rebuilt index. That is the ADR-120 attach rung:
// ErrLockBusy is a proven defect (Repairable), quarantined and rebuilt once,
// never retried in a loop.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// gateFileName is the sentinel file the gate locks, inside the index
// directory. Hidden-name, app-infrastructure content only — it carries no
// bytes and is never parsed; it dies with its directory.
const gateFileName = ".lockgate"

// DefaultGateWait bounds one open's wait for the gate. It is exported for
// documentation; opens read the gateWait var (same test-seam shape as
// corpusBuildWait in the parent package: only tests shrink it).
const DefaultGateWait = 5 * time.Second

// gatePollInterval spaces consecutive non-blocking lock probes. A bounded
// poll (never a blocking lock) keeps the wait cancellable-by-deadline and
// lets virtual-time tests traverse the whole budget instantly.
const gatePollInterval = 20 * time.Millisecond

// gateWait bounds how long an open waits for a busy gate before refusing
// with ErrLockBusy. A var (not the const) so tests can shrink the window;
// production code never reassigns it.
var gateWait = DefaultGateWait

// ErrLockBusy reports that another live opener held the index's lock gate
// for the whole gateWait: the open refused FAST instead of hanging on
// bbolt's unbounded lock. A stuck holder is a proven defect only a rebuild
// can clear (the holder keeps the orphaned inode busy forever), so the
// parent package's repair ladder classifies it Repairable (ADR-120): the
// lexical leg is quarantined and rebuilt on a new inode while the holder
// lives. It deliberately does not wrap context.DeadlineExceeded — a busy
// gate is store damage to heal, not a transient fault to retry.
var ErrLockBusy = errors.New("lexical index is locked by another live opener")

// lockGate is one held gate: the open sentinel file, locked until release.
type lockGate struct {
	f *os.File
}

// acquireGate locks the sentinel file of dir before the caller touches
// bleve. Read-write opens take the gate exclusively; read-only opens share
// it (N corpus readers of one published snapshot may hold it together,
// while any exclusive holder excludes them all). Busy gates are probed
// every gatePollInterval until gateWait expires, then refused with
// ErrLockBusy; real lock/setup errors surface verbatim.
func acquireGate(dir string, exclusive bool) (*lockGate, error) {
	gatePath := filepath.Join(dir, gateFileName)
	f, err := os.OpenFile(gatePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lexical lock gate %s: %w", gatePath, err)
	}
	deadline := time.Now().Add(gateWait)
	for {
		busy, lockErr := tryGateLock(f, exclusive)
		if lockErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking lexical lock gate %s: %w", gatePath, lockErr)
		}
		if !busy {
			return &lockGate{f: f}, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lexical lock gate %s stayed busy for %s: %w", gatePath, gateWait, ErrLockBusy)
		}
		timer := time.NewTimer(gatePollInterval)
		<-timer.C
	}
}

// release unlocks and closes the gate file. Idempotent; a nil gate (a
// failed acquire) releases nothing.
func (g *lockGate) release() error {
	if g == nil || g.f == nil {
		return nil
	}
	err := unlockGate(g.f)
	if closeErr := g.f.Close(); err == nil {
		err = closeErr
	}
	g.f = nil
	if err != nil {
		return fmt.Errorf("releasing lexical lock gate: %w", err)
	}
	return nil
}
