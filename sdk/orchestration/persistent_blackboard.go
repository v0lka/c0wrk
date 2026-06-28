package orchestration

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
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
	persistCh          chan persistOp
	persistCtx         context.Context // context for persistence operations; nil-safe (falls back to Background)
	onChanged          func(changeType string) // optional callback, nil-safe
	wg                 sync.WaitGroup          // waits for persistence worker on shutdown
	shutdownOnce       sync.Once               // ensures Shutdown is executed at most once
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
	ch := make(chan persistOp, 8)
	pb := &CheckpointedBlackboard{
		MapBlackboard:      NewMapBlackboard(opts...),
		id:                 id,
		checkpointer:       cp,
		logger:             logger,
		persistenceTimeout: timeout,
		persistCh:          ch,
	}
	pb.wg.Add(1)
	go pb.persistenceWorker(ch)
	return pb
}

// SetOnChanged sets an optional callback invoked after every successful
// blackboard write. The changeType argument describes what changed (e.g. "plan",
// "step_result", "fact", "reflection"). The callback is nil-safe.
func (pb *CheckpointedBlackboard) SetOnChanged(fn func(changeType string)) {
	pb.onChanged = fn
}

// SetPersistContext sets the context used for persistence operations.
// When nil, context.Background() is used. Call before any write operations.
// The context carries cancellation and tracing through to the Checkpointer.
func (pb *CheckpointedBlackboard) SetPersistContext(ctx context.Context) {
	pb.persistCtx = ctx
}

// persistCtxOrDefault returns the persistence context or Background if unset.
func (pb *CheckpointedBlackboard) persistCtxOrDefault() context.Context {
	if pb.persistCtx != nil {
		return pb.persistCtx
	}
	return context.Background()
}

// notifyChanged invokes the onChanged callback if set.
func (pb *CheckpointedBlackboard) notifyChanged(changeType string) {
	if pb.onChanged != nil {
		pb.onChanged(changeType)
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
}

// ID returns the checkpoint identifier.
func (pb *CheckpointedBlackboard) ID() string {
	return pb.id
}

// Shutdown closes the persistence channel, waits for the worker to finish,
// then returns. Safe to call multiple times.
func (pb *CheckpointedBlackboard) Shutdown() {
	pb.shutdownOnce.Do(func() {
		if pb.persistCh != nil {
			close(pb.persistCh)
			pb.persistCh = nil
		}
		pb.wg.Wait()
	})
}

// ---------------------------------------------------------------------------
// Persistence safety wrapper
// ---------------------------------------------------------------------------

// persistSafe enqueues a persistence operation to the background worker.
// The caller blocks until the operation completes or the timeout expires.
// Errors are logged. A blocking send ensures no checkpoint is silently lost;
// if the worker is healthy, the operation will start within one prior-op latency.
func (pb *CheckpointedBlackboard) persistSafe(operation string, fn func(context.Context) error) {
	done := make(chan error, 1)
	op := persistOp{operation: operation, fn: fn, done: done}

	pb.persistCh <- op

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
		pb.logWarn("persistence failure: " + operation + " error: " + err.Error())
	}
}

// persistenceWorker is the single goroutine that executes persist operations
// serially, with panic recovery for each operation.
func (pb *CheckpointedBlackboard) persistenceWorker(ch <-chan persistOp) {
	defer pb.wg.Done()
	for op := range ch {
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

	ch := make(chan persistOp, 8)
	pb := &CheckpointedBlackboard{
		MapBlackboard:      mb,
		id:                 id,
		checkpointer:       cp,
		logger:             logger,
		persistenceTimeout: timeout,
		persistCh:          ch,
	}
	pb.wg.Add(1)
	go pb.persistenceWorker(ch)
	return pb, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// logWarn logs a warning message if the logger is non-nil.
func (pb *CheckpointedBlackboard) logWarn(msg string) {
	if pb.logger != nil {
		pb.logger.Warn(msg)
	}
}
