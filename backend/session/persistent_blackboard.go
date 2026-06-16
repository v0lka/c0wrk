package session

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// defaultPersistenceTimeout is the maximum time allowed for a single
// blackboard write operation to the database.
const defaultPersistenceTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// PersistentBlackboard — decorator that persists write operations
// ---------------------------------------------------------------------------

// compile-time checks
var _ orchestration.Blackboard = (*PersistentBlackboard)(nil)
var _ core.PersistableBlackboard = (*PersistentBlackboard)(nil)

// PersistentBlackboard wraps a MapBlackboard and persists write operations to a TaskPersistence store.
// Read methods delegate to the embedded MapBlackboard. Write methods delegate AND persist.
// All persistence calls are best-effort: errors are logged but do not propagate to callers.
// Persistence operations are executed by a single background worker goroutine with a timeout
// and panic recovery to prevent hangs.
type PersistentBlackboard struct {
	// *orchestration.MapBlackboard is embedded for in-memory read operations.
	// Write operations are intercepted to persist changes to the database.
	*orchestration.MapBlackboard
	taskID             string
	sessionID          string
	store              core.TaskPersistence
	logger             *slog.Logger
	emitterMu          sync.RWMutex
	emitter            core.Emitter            // optional, nil-safe; used to surface persistence warnings to the user
	persistenceTimeout time.Duration           // timeout for persistence operations
	onChanged          func(changeType string) // optional callback for BB change notifications, nil-safe
	persistCh          chan persistOp          // buffered channel for serializing DB writes
}

// persistOp is a single persistence operation sent to the worker goroutine.
type persistOp struct {
	operation string
	fn        func() error
	done      chan error
}

// NewPersistentBlackboard creates a PersistentBlackboard that wraps a fresh MapBlackboard.
// The logger is optional (nil-safe).
func NewPersistentBlackboard(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) *PersistentBlackboard {
	return NewPersistentBlackboardWithTimeout(taskID, sessionID, store, logger, 0, opts...)
}

// NewPersistentBlackboardWithTimeout creates a PersistentBlackboard that wraps a fresh MapBlackboard with a configurable timeout.
// The logger is optional (nil-safe). If timeout is 0, defaultPersistenceTimeout is used.
func NewPersistentBlackboardWithTimeout(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, timeout time.Duration, opts ...orchestration.MapBlackboardOption) *PersistentBlackboard {
	ch := make(chan persistOp, 8)
	pb := &PersistentBlackboard{
		MapBlackboard:      orchestration.NewMapBlackboard(opts...),
		taskID:             taskID,
		sessionID:          sessionID,
		store:              store,
		logger:             logger,
		persistenceTimeout: timeout,
		persistCh:          ch,
	}
	go pb.persistenceWorker(ch)
	return pb
}

// SetEmitter sets the optional emitter for surfacing persistence warnings to the user.
func (pb *PersistentBlackboard) SetEmitter(emitter core.Emitter) {
	pb.emitterMu.Lock()
	pb.emitter = emitter
	pb.emitterMu.Unlock()
}

// SetOnChanged sets an optional callback invoked after every successful
// blackboard write. The changeType argument describes what changed (e.g. "plan",
// "step_result", "fact", "reflection"). The callback is nil-safe.
func (pb *PersistentBlackboard) SetOnChanged(fn func(changeType string)) {
	pb.onChanged = fn
}

// notifyChanged invokes the onChanged callback if set.
func (pb *PersistentBlackboard) notifyChanged(changeType string) {
	if pb.onChanged != nil {
		pb.onChanged(changeType)
	}
}

// ---------------------------------------------------------------------------
// Persistence safety wrapper
// ---------------------------------------------------------------------------

// persistSafe enqueues a persistence operation to the background worker.
// The caller blocks until the operation completes or the timeout expires.
// Errors are logged and optionally emitted to the user.
func (pb *PersistentBlackboard) persistSafe(operation string, fn func() error) {
	done := make(chan error, 1)
	op := persistOp{operation: operation, fn: fn, done: done}

	// Non-blocking send: if the channel is closed (shutting down) or full,
	// we skip persistence gracefully.
	select {
	case pb.persistCh <- op:
	default:
		pb.logWarn("persistence worker unavaiable; skipping "+operation, "task_id", pb.taskID)
		return
	}

	timeout := pb.persistenceTimeout
	if timeout == 0 {
		timeout = defaultPersistenceTimeout
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(timeout):
		err = fmt.Errorf("persistence timeout after %s", timeout)
	}

	if err != nil {
		pb.logWarn("persistence failure: "+operation, "task_id", pb.taskID, "error", err)
		pb.emitterMu.RLock()
		em := pb.emitter
		pb.emitterMu.RUnlock()
		if em != nil {
			em.ServiceWithMeta(
				fmt.Sprintf("Warning: failed to persist %s (execution continues)", operation),
				map[string]any{"phase": "persistence", "error": err.Error()},
			)
		}
	}
}

// persistenceWorker is the single goroutine that executes persist operations
// serially, with panic recovery for each operation.
func (pb *PersistentBlackboard) persistenceWorker(ch <-chan persistOp) {
	for op := range ch {
		func() {
			defer func() {
				if r := recover(); r != nil {
					op.done <- fmt.Errorf("panic in persistence: %v", r)
				}
			}()
			op.done <- op.fn()
		}()
	}
}

// shutdownPersister closes the persistence channel, causing the worker
// goroutine to exit after draining any queued operations.
func (pb *PersistentBlackboard) shutdownPersister() {
	if pb.persistCh != nil {
		close(pb.persistCh)
		pb.persistCh = nil
	}
}

// ---------------------------------------------------------------------------
// Write method overrides
// ---------------------------------------------------------------------------

// SetOriginalRequest sets the original user request and persists a new task record.
func (pb *PersistentBlackboard) SetOriginalRequest(req string) {
	pb.MapBlackboard.SetOriginalRequest(req)
	pb.persistSafe("new task", func() error {
		return pb.store.PersistNewTask(pb.taskID, pb.sessionID, req)
	})
}

// SetPlan stores a plan and persists it.
func (pb *PersistentBlackboard) SetPlan(plan *orchestration.Plan) {
	pb.MapBlackboard.SetPlan(plan)
	pb.persistSafe("plan", func() error {
		return pb.store.PersistPlan(pb.taskID, plan)
	})
	pb.notifyChanged("plan")
}

// SetStepResult records a step result and persists it.
// The summary is generated using the same logic as MapBlackboard.
func (pb *PersistentBlackboard) SetStepResult(stepID, output string, err error, steps []agent.Step) {
	pb.MapBlackboard.SetStepResult(stepID, output, err, steps)

	maxLen := pb.MaxSummaryLen()
	if maxLen == 0 {
		maxLen = 500
	}
	summary := orchestration.GenerateSummary(output, maxLen)
	// Apply the same token-budget cap as MapBlackboard.
	if pb.MaxSummaryTokens() > 0 {
		maxChars := pb.MaxSummaryTokens() * 4
		if len(summary) > maxChars {
			summary = summary[:maxChars] + "..."
		}
	}

	var errText string
	if err != nil {
		errText = err.Error()
	}

	pb.persistSafe("step result ("+stepID+")", func() error {
		return pb.store.PersistStepResult(pb.taskID, stepID, summary, output, errText, steps)
	})
	pb.notifyChanged("step_result")
}

// AddReflection appends a reflection and persists it.
func (pb *PersistentBlackboard) AddReflection(r orchestration.Reflection) {
	pb.MapBlackboard.AddReflection(r)
	pb.persistSafe("reflection", func() error {
		return pb.store.PersistReflection(pb.taskID, r)
	})
	pb.notifyChanged("reflection")
}

// StoreFact appends a fact and persists the full facts list.
func (pb *PersistentBlackboard) StoreFact(fact orchestration.Fact) {
	pb.MapBlackboard.StoreFact(fact)
	facts := pb.GetFacts()
	pb.persistSafe("facts", func() error {
		return pb.store.PersistFacts(pb.taskID, facts)
	})
	pb.notifyChanged("fact")
}

// SetFinalResult sets the final result string.
// Does NOT call PersistCompletion — the orchestrator calls CompleteTask() separately.
func (pb *PersistentBlackboard) SetFinalResult(result string) {
	pb.MapBlackboard.SetFinalResult(result)
}

// SetRouting persists the routing decision for the task.
func (pb *PersistentBlackboard) SetRouting(routing *router.RoutingDecision) {
	pb.persistSafe("routing", func() error {
		return pb.store.PersistRouting(pb.taskID, routing)
	})
}

// ---------------------------------------------------------------------------
// Additional methods (not part of Blackboard interface)
// ---------------------------------------------------------------------------

// CompleteTask marks the task as completed.
// Called by the orchestrator after Handle() succeeds.
func (pb *PersistentBlackboard) CompleteTask(attemptCount int) {
	finalOutput := pb.GetFinalResult()
	pb.persistSafe("task completion", func() error {
		return pb.store.PersistCompletion(pb.taskID, finalOutput, attemptCount)
	})
	pb.notifyChanged("completed")
	pb.shutdownPersister()
}

// FailTask marks the task as failed.
func (pb *PersistentBlackboard) FailTask() {
	pb.persistSafe("task failure", func() error {
		return pb.store.PersistFailure(pb.taskID)
	})
	pb.shutdownPersister()
}

// ReactivateTask reactivates a completed task back to in_progress.
func (pb *PersistentBlackboard) ReactivateTask() {
	pb.persistSafe("task reactivation", func() error {
		return pb.store.ReactivateTask(pb.taskID)
	})
}

// TaskID returns the task ID.
func (pb *PersistentBlackboard) TaskID() string {
	return pb.taskID
}

// ---------------------------------------------------------------------------
// Restoration
// ---------------------------------------------------------------------------

// RestoreBlackboard loads a task's state from persistence and hydrates a PersistentBlackboard.
// Returns nil, nil if the task is not found.
func RestoreBlackboard(taskID, sessionID string, store core.TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (*PersistentBlackboard, error) {
	state, err := store.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	mb := orchestration.NewMapBlackboard(opts...)

	// Hydrate the in-memory blackboard with restored state.
	mb.SetOriginalRequest(state.OriginalRequest)
	if state.Plan != nil {
		mb.SetPlan(state.Plan)
	}
	for stepID, sr := range state.StepResults {
		// Re-use the raw data; SetStepResult would regenerate the summary,
		// so we populate the map directly via SetStepResultRaw.
		mb.SetStepResultRaw(stepID, sr)
	}
	for _, r := range state.Reflections {
		mb.AddReflection(r)
	}
	if len(state.Facts) > 0 {
		mb.SetFacts(state.Facts)
	}
	if state.FinalOutput != "" {
		mb.SetFinalResult(state.FinalOutput)
	}

	ch := make(chan persistOp, 8)
	pb := &PersistentBlackboard{
		MapBlackboard:      mb,
		taskID:             taskID,
		sessionID:          sessionID,
		store:              store,
		logger:             logger,
		persistenceTimeout: defaultPersistenceTimeout,
		persistCh:          ch,
	}
	go pb.persistenceWorker(ch)
	return pb, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// logWarn logs a warning message if the logger is non-nil.
func (pb *PersistentBlackboard) logWarn(msg string, args ...any) {
	if pb.logger != nil {
		pb.logger.Warn(msg, args...)
	}
}
