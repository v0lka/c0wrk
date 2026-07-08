package orchestration

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/sp4rk/agent"
)

// defaultPersistenceTimeout is the maximum time allowed for a single
// checkpoint write operation.
const defaultPersistenceTimeout = 5 * time.Second

// compile-time check
var _ Blackboard = (*CheckpointedBlackboard)(nil)

// CheckpointedBlackboard wraps a MapBlackboard and persists write operations
// through a Checkpointer. Read methods delegate to the embedded MapBlackboard.
// Write methods delegate AND persist.
//
// All persistence calls are best-effort: errors are logged but do not propagate
// to callers. Persistence operations are executed by a single background worker
// goroutine with a timeout and panic recovery to prevent hangs.
//
// Call Shutdown() to release the background worker. The caller (typically the
// Orchestrator) is responsible for calling Shutdown when the blackboard is no
// longer needed to prevent goroutine leaks.
type CheckpointedBlackboard struct {
	*MapBlackboard
	id                 string
	checkpointer       Checkpointer
	logger             *slog.Logger
	persistenceTimeout time.Duration
	queue              []persistOp
	queueMu            sync.Mutex
	queueCh            chan struct{}
	closed             atomic.Bool
	persistCtx         context.Context         // context for persistence operations; nil-safe (falls back to Background)
	onChanged          func(changeType string) // optional callback, nil-safe
	wg                 sync.WaitGroup          // waits for persistence worker on shutdown
	shutdownOnce       sync.Once               // ensures Shutdown is executed at most once
	configMu           sync.RWMutex            // guards onChanged and persistCtx
}

// persistOp is a single persistence operation sent to the worker goroutine.
type persistOp struct {
	operation string
	fn        func(context.Context) error
	done      chan error
}

// NewCheckpointedBlackboard creates a CheckpointedBlackboard that wraps a fresh MapBlackboard.
// The logger is optional (nil-safe). If timeout is 0, defaultPersistenceTimeout is used.
func NewCheckpointedBlackboard(id string, cp Checkpointer, logger *slog.Logger, timeout time.Duration, opts ...MapBlackboardOption) *CheckpointedBlackboard {
	pb := &CheckpointedBlackboard{
		MapBlackboard:      NewMapBlackboard(opts...),
		id:                 id,
		checkpointer:       cp,
		logger:             logger,
		persistenceTimeout: timeout,
		queueCh:            make(chan struct{}, 1),
	}
	pb.wg.Add(1)
	go pb.persistenceWorker()
	return pb
}

// SetOnChanged sets an optional callback invoked after every successful
// blackboard write. The changeType argument describes what changed (e.g. "plan",
// "step_result", "fact", "reflection"). The callback is nil-safe.
func (pb *CheckpointedBlackboard) SetOnChanged(fn func(changeType string)) {
	pb.configMu.Lock()
	defer pb.configMu.Unlock()
	pb.onChanged = fn
}

// SetPersistContext sets the context used for persistence operations.
// When nil, context.Background() is used. Call before any write operations.
// The context carries cancellation and tracing through to the Checkpointer.
func (pb *CheckpointedBlackboard) SetPersistContext(ctx context.Context) {
	pb.configMu.Lock()
	defer pb.configMu.Unlock()
	pb.persistCtx = ctx
}

// persistCtxOrDefault returns the persistence context or Background if unset.
func (pb *CheckpointedBlackboard) persistCtxOrDefault() context.Context {
	pb.configMu.RLock()
	defer pb.configMu.RUnlock()
	if pb.persistCtx != nil {
		return pb.persistCtx
	}
	return context.Background()
}

// notifyChanged invokes the onChanged callback if set.
func (pb *CheckpointedBlackboard) notifyChanged(changeType string) {
	pb.configMu.RLock()
	fn := pb.onChanged
	pb.configMu.RUnlock()
	if fn != nil {
		fn(changeType)
	}
}

// ---------------------------------------------------------------------------
// Write method overrides — delegate to MapBlackboard AND persist
// ---------------------------------------------------------------------------

// SetOriginalRequest sets the original user request and persists a checkpoint.
func (pb *CheckpointedBlackboard) SetOriginalRequest(req string) {
	pb.MapBlackboard.SetOriginalRequest(req)
	pb.persistSafe("set_original_request", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("original_request")
}

// SetPlan stores a plan and persists a checkpoint.
func (pb *CheckpointedBlackboard) SetPlan(plan *Plan) {
	pb.MapBlackboard.SetPlan(plan)
	pb.persistSafe("set_plan", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("plan")
}

// SetStepResult records a step result and persists a checkpoint.
func (pb *CheckpointedBlackboard) SetStepResult(stepID, output string, err error, steps []agent.Step) {
	pb.MapBlackboard.SetStepResult(stepID, output, err, steps)
	pb.persistSafe("set_step_result", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("step_result")
}

// AddReflection appends a reflection and persists a checkpoint.
func (pb *CheckpointedBlackboard) AddReflection(r Reflection) {
	pb.MapBlackboard.AddReflection(r)
	pb.persistSafe("add_reflection", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("reflection")
}

// StoreFact appends a fact and persists a checkpoint.
func (pb *CheckpointedBlackboard) StoreFact(fact Fact) {
	pb.MapBlackboard.StoreFact(fact)
	pb.persistSafe("store_fact", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("fact")
}

// SetFinalResult sets the final result and persists a checkpoint.
func (pb *CheckpointedBlackboard) SetFinalResult(result string) {
	pb.MapBlackboard.SetFinalResult(result)
	pb.persistSafe("set_final_result", func(ctx context.Context) error {
		return pb.checkpointer.SaveCheckpoint(ctx, pb.id, pb.MapBlackboard)
	})
	pb.notifyChanged("final_result")
}

// ID returns the checkpoint identifier.
func (pb *CheckpointedBlackboard) ID() string {
	return pb.id
}

// Shutdown signals the persistence worker to stop, waits for it to drain any
// queued operations, then returns. Safe to call multiple times.
func (pb *CheckpointedBlackboard) Shutdown() {
	pb.shutdownOnce.Do(func() {
		// Set closed under queueMu. persistSafe checks closed and sends the
		// wake-up signal under the same lock, so once we release queueMu here
		// every subsequent persistSafe observes closed==true and returns
		// without sending. Closing queueCh outside the lock is therefore
		// safe: no sender can race with the close.
		pb.queueMu.Lock()
		pb.closed.Store(true)
		pb.queueMu.Unlock()
		close(pb.queueCh)
		pb.wg.Wait()
	})
}

// ---------------------------------------------------------------------------
// Persistence safety wrapper
// ---------------------------------------------------------------------------

// persistSafe enqueues a persistence operation to the background worker.
// The caller blocks until the operation completes or the timeout expires.
// Errors are logged.
//
// The enqueue never blocks: the operation is appended to an unbounded queue
// under queueMu, then a non-blocking signal is sent to the worker goroutine.
// This prevents deadlock under I/O contention — unlike a fixed-size buffered
// channel, the queue grows as needed. If the blackboard has been shut down,
// the operation is skipped (logged as a warning).
//
// The closed-check, queue-append, and signal-send are all performed under
// queueMu. Shutdown sets the closed flag under the same lock, which gives a
// happens-before edge: any persistSafe that acquires queueMu after Shutdown
// released it observes closed==true and never sends. The actual close(queueCh)
// happens outside the lock, which is safe because no sender can race with it.
func (pb *CheckpointedBlackboard) persistSafe(operation string, fn func(context.Context) error) {
	done := make(chan error, 1)
	op := persistOp{operation: operation, fn: fn, done: done}

	pb.queueMu.Lock()
	if pb.closed.Load() {
		pb.queueMu.Unlock()
		if pb.logger != nil {
			pb.logger.Warn("persistence skipped: blackboard shut down", "operation", operation)
		}
		return
	}
	pb.queue = append(pb.queue, op)
	select {
	case pb.queueCh <- struct{}{}:
	default:
	}
	pb.queueMu.Unlock()

	pb.waitPersistenceResult(operation, done)
}

// waitPersistenceResult waits for a persistence operation to complete or timeout.
func (pb *CheckpointedBlackboard) waitPersistenceResult(operation string, done <-chan error) {
	timeout := pb.persistenceTimeout
	if timeout == 0 {
		timeout = defaultPersistenceTimeout
	}
	var err error
	timer := time.NewTimer(timeout)
	select {
	case err = <-done:
		timer.Stop()
	case <-timer.C:
		err = fmt.Errorf("persistence timeout after %s", timeout)
	}

	if err != nil {
		if pb.logger != nil {
			pb.logger.Warn("persistence failure", "operation", operation, "error", err)
		}
	}
}

// persistenceWorker is the single goroutine that executes persist operations
// serially, with panic recovery for each operation. It drains the entire queue
// on each signal from queueCh, ensuring all queued operations are processed.
// The worker exits when queueCh is closed by Shutdown.
func (pb *CheckpointedBlackboard) persistenceWorker() {
	defer pb.wg.Done()
	for range pb.queueCh {
		for {
			pb.queueMu.Lock()
			if len(pb.queue) == 0 {
				pb.queueMu.Unlock()
				break
			}
			op := pb.queue[0]
			pb.queue = pb.queue[1:]
			pb.queueMu.Unlock()

			func() {
				defer func() {
					if r := recover(); r != nil {
						op.done <- fmt.Errorf("panic in persistence: %v", r)
					}
				}()
				op.done <- op.fn(pb.persistCtxOrDefault())
			}()
		}
	}
}

// ---------------------------------------------------------------------------
// Restoration
// ---------------------------------------------------------------------------

// RestoreBlackboard loads a blackboard state from a Checkpointer and hydrates
// a CheckpointedBlackboard. Returns nil, nil if the checkpoint does not exist.
func RestoreBlackboard(ctx context.Context, id string, cp Checkpointer, logger *slog.Logger, timeout time.Duration, opts ...MapBlackboardOption) (*CheckpointedBlackboard, error) {
	restored, err := cp.LoadCheckpoint(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to load checkpoint: %w", err)
	}
	if restored == nil {
		return nil, nil
	}

	// Create a fresh MapBlackboard and copy state from the restored one.
	mb := NewMapBlackboard(opts...)
	mb.SetOriginalRequest(restored.GetOriginalRequest())
	if plan := restored.GetPlan(); plan != nil {
		mb.SetPlan(plan)
	}
	for stepID, sr := range restored.GetAllStepResults() {
		mb.SetStepResultRaw(stepID, sr)
	}
	for _, r := range restored.GetReflections() {
		mb.AddReflection(r)
	}
	if facts := restored.GetFacts(); len(facts) > 0 {
		mb.SetFacts(facts)
	}
	if finalResult := restored.GetFinalResult(); finalResult != "" {
		mb.SetFinalResult(finalResult)
	}

	pb := &CheckpointedBlackboard{
		MapBlackboard:      mb,
		id:                 id,
		checkpointer:       cp,
		logger:             logger,
		persistenceTimeout: timeout,
		queueCh:            make(chan struct{}, 1),
	}
	pb.wg.Add(1)
	go pb.persistenceWorker()
	return pb, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
