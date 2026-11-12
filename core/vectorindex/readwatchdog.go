package vectorindex

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// The per-file read watchdog: the pass-level backstop for file reads that
// never return. safeio already refuses FIFOs, sockets and devices at open
// time (the planted-pipe hang), and os.Stat never blocks — but the read(2) of
// a REGULAR file can still block indefinitely (a wedged network or FUSE
// mount, a stalled disk), and one wedged read used to wedge its prep worker,
// then the whole pass and every shutdown join waiting on it. The watchdog
// bounds each file's read stages: a read that outlasts
// fileReadWatchdogTimeout is abandoned in place — Go cannot cancel a read
// stuck in the kernel, so its goroutine stays until the syscall returns — and
// the file is skipped like any other prep failure ("read timeout"), counted
// per the pass's countSkipped policy while the pass continues.
//
// Abandoned readers are the reason for the second knob: an indexing pass may
// take at most fileReadTimeoutSkipBudget read-timeout skips before it refuses
// explicitly (ErrReadSkipBudgetExhausted) instead of stacking more wedged
// goroutines. Both knobs are deliberate constants, not config: they are hang
// bounds, not tuning opinions.

// fileReadWatchdogTimeout bounds one read stage of one file (the
// binary-header pre-read and the full read each run under it). A stage
// timeout skips the file outright, so a file strands at most one reader
// goroutine per pass attempt.
const fileReadWatchdogTimeout = 30 * time.Second

// fileReadTimeoutSkipBudget is the read-timeout skips one indexing pass may
// take before it refuses explicitly. Sized to tolerate a genuinely sick
// subtree while bounding the reader goroutines a single pass can strand (the
// refusal itself leaves at most one further in-flight read per prep worker).
const fileReadTimeoutSkipBudget = 50

// errFileReadTimeout marks a file read that outlasted the watchdog deadline.
// It stays unexported: callers see it through the skip log's error ("reading
// file X: read timeout"), not as an identifiable sentinel.
var errFileReadTimeout = errors.New("read timeout")

// ErrReadSkipBudgetExhausted aborts an indexing pass whose
// fileReadTimeoutSkipBudget of read-timeout skips is spent: the pass refuses
// to continue so abandoned reader goroutines stop accumulating. Identify it
// with errors.Is; the files that still hang carry no sidecar entry, so the
// next pass re-attempts them.
var ErrReadSkipBudgetExhausted = errors.New("read-timeout skip budget exhausted")

// readSkipBudget is one indexing pass's allowance of read-timeout skips
// (fileReadTimeoutSkipBudget), consumed atomically by the prep workers as
// their reads time out.
type readSkipBudget struct {
	remaining atomic.Int64
}

// newReadSkipBudget returns a budget with n allowances.
func newReadSkipBudget(n int64) *readSkipBudget {
	b := &readSkipBudget{}
	b.remaining.Store(n)
	return b
}

// exhausted reports whether no allowance remains. processFile checks it
// BEFORE starting a read so a spent budget stops attempting reads at all —
// every attempted read that hangs strands another reader goroutine, and the
// refusal is meant to stop exactly that.
func (b *readSkipBudget) exhausted() bool {
	return b.remaining.Load() <= 0
}

// consume spends one allowance, reporting whether one was available.
func (b *readSkipBudget) consume() bool {
	for {
		n := b.remaining.Load()
		if n <= 0 {
			return false
		}
		if b.remaining.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// readOutcome carries one boundedRead attempt's result to the waiter.
type readOutcome[T any] struct {
	value T
	err   error
}

// boundedRead runs fn once under the watchdog deadline. The read runs on its
// own goroutine — a read stuck in the kernel cannot be interrupted — so on
// timeout the goroutine is abandoned in place (per pass bounded by the skip
// budget) and the buffered result channel guarantees the abandoned sender
// never blocks on its one delivery. Returns (value, false, err) when fn
// completes, and (zero value, true, nil) when the deadline fires first.
//
// The timer is a plain time.Timer so a synctest bubble (the watchdog tests)
// fires it in virtual time; nothing here touches the OS.
func boundedRead[T any](deadline time.Duration, fn func() (T, error)) (value T, timedOut bool, err error) {
	ch := make(chan readOutcome[T], 1)
	go func() {
		value, err := fn()
		ch <- readOutcome[T]{value: value, err: err}
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.value, false, res.err
	case <-timer.C:
		var zero T
		return zero, true, nil
	}
}

// readTimeoutSkipError builds a timed-out read's error: a tolerated skip
// ("read timeout") while the pass's budget holds, and the explicit
// ErrReadSkipBudgetExhausted refusal once spent. A nil budget (direct
// processFile callers: standalone services and tests) always tolerates the
// skip.
func readTimeoutSkipError(filePath string, budget *readSkipBudget) error {
	if budget != nil && !budget.consume() {
		return fmt.Errorf("reading file %s: %w: the pass aborts instead of stranding more reader goroutines", filePath, ErrReadSkipBudgetExhausted)
	}
	return fmt.Errorf("reading file %s: %w", filePath, errFileReadTimeout)
}
