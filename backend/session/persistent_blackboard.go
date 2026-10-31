package session

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
)

// defaultPersistenceTimeout is the maximum time allowed for a single
// blackboard write operation to the database.
const defaultPersistenceTimeout = 5 * time.Second

// persistEnqueueRetryInterval is how long persistSafe waits between enqueue
// attempts when the worker's channel buffer is full (bounded by the overall
// persistence timeout).
const persistEnqueueRetryInterval = 5 * time.Millisecond

// ---------------------------------------------------------------------------
// PersistentBlackboard — decorator that persists write operations
// ---------------------------------------------------------------------------

// compile-time checks
var _ orchestration.Blackboard = (*PersistentBlackboard)(nil)
var _ core.PersistableBlackboard = (*PersistentBlackboard)(nil)

// PersistentBlackboard wraps a MapBlackboard: reads delegate to the embedded
// blackboard, writes delegate AND persist best-effort through one background
// worker (timeout, panic recovery). See specs/domains/memory/blackboard.md.
type PersistentBlackboard struct {
	// *orchestration.MapBlackboard is embedded for reads; writes are
	// intercepted to persist.
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
	persistMu          sync.Mutex              // guards persistCh lifecycle (send vs. shutdown close)

	// persistDone is closed when the worker returns; Shutdown waits on it to
	// guarantee the worker is gone, not merely asked to stop.
	persistDone chan struct{}

	// routing is cached at SetRouting so Routing() avoids a DB round-trip.
	routingMu sync.RWMutex
	routing   *router.RoutingDecision

	// delegationSpecsMu guards delegationSpecs — hydrated at restore, read by
	// the Resume auto-resume wave via DelegationSpecReader.
	delegationSpecsMu sync.RWMutex
	delegationSpecs   []tools.DelegationSpec

	// unitLedger is the task's MAINLINE unit ledger, built once so its
	// in-memory overlay survives across callers.
	unitLedgerOnce sync.Once
	unitLedger     units.Ledger
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

// NewPersistentBlackboardWithTimeout wraps a fresh MapBlackboard with a
// configurable timeout (0 selects defaultPersistenceTimeout). Logger is nil-safe.
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
		persistDone:        make(chan struct{}),
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

// SetOnChanged sets an optional post-write callback ("plan", "step_result",
// "fact", ...); nil-safe.
func (pb *PersistentBlackboard) SetOnChanged(fn func(changeType string)) {
	pb.onChanged = fn
}

func (pb *PersistentBlackboard) notifyChanged(changeType string) {
	if pb.onChanged != nil {
		pb.onChanged(changeType)
	}
}

// ---------------------------------------------------------------------------
// Persistence safety wrapper
// ---------------------------------------------------------------------------

// persistSafe enqueues a non-critical write to the background worker and
// blocks until it lands or the timeout expires; the error is returned so
// write methods can gate change notifications on the write landing.
// Enqueueing waits for queue space within the same budget instead of
// dropping the write. See specs/domains/memory/blackboard.md.
func (pb *PersistentBlackboard) persistSafe(operation string, fn func() error) error {
	done := make(chan error, 1)
	op := persistOp{operation: operation, fn: fn, done: done}

	timeout := pb.persistenceTimeout
	if timeout == 0 {
		timeout = defaultPersistenceTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		pb.persistMu.Lock()
		ch := pb.persistCh
		if ch == nil {
			pb.persistMu.Unlock()
			err := errors.New("persistence worker already shut down")
			pb.logWarn("persistence worker already shut down; skipping "+operation, "task_id", pb.taskID)
			return err
		}
		select {
		case ch <- op:
			pb.persistMu.Unlock()
			return pb.waitPersistenceResult(operation, done)
		default:
			pb.persistMu.Unlock()
		}

		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(min(remaining, persistEnqueueRetryInterval))
			continue
		}
		err := fmt.Errorf("persistence queue still full after %s", timeout)
		pb.logWarn("persistence worker full; skipping "+operation, "task_id", pb.taskID)
		return err
	}
}

// persistSynchronously runs a critical finalizing write (CompleteTask/FailTask/
// CancelTask) on the caller goroutine — the status change must be durable
// before the method returns — with panic recovery; the error is returned.
// See specs/domains/memory/blackboard.md.
func (pb *PersistentBlackboard) persistSynchronously(operation string, fn func() error) error {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in persistence: %v", r)
			}
		}()
		err = fn()
	}()

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
	return err
}

// waitPersistenceResult waits for the operation or the timeout; logging and
// the user-facing warning live here so every persistSafe caller surfaces
// failures uniformly.
func (pb *PersistentBlackboard) waitPersistenceResult(operation string, done <-chan error) error {
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
	return err
}

// persistenceWorker is the single goroutine that executes persist operations
// serially, with panic recovery for each operation.
func (pb *PersistentBlackboard) persistenceWorker(ch <-chan persistOp) {
	defer close(pb.persistDone)
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

// shutdownPersister closes the persistence channel under persistMu — race-free
// against persistSafe sends and idempotent across duplicate finalizers; the
// worker drains buffered ops before exiting.
func (pb *PersistentBlackboard) shutdownPersister() {
	pb.persistMu.Lock()
	defer pb.persistMu.Unlock()
	if pb.persistCh != nil {
		close(pb.persistCh)
		pb.persistCh = nil
	}
}

// Shutdown stops the worker and waits up to timeout for it to drain; idempotent
// when a terminal finalizer already stopped it. Exists so the session Manager
// can guarantee no worker outlives Shutdown (a paused/abandoned task would
// otherwise block its worker on the channel forever).
func (pb *PersistentBlackboard) Shutdown(timeout time.Duration) {
	pb.shutdownPersister()
	if pb.persistDone == nil {
		return
	}
	select {
	case <-pb.persistDone:
	case <-time.After(timeout):
		if pb.logger != nil {
			pb.logger.Warn("timed out waiting for blackboard persistence worker to stop",
				"task_id", pb.taskID, "session_id", pb.sessionID)
		}
	}
}

// persistenceWorkerStopped reports whether the worker already exited (terminal
// finalizer or Shutdown) so the session manager can prune finished blackboards
// from its shutdown tracking list.
func (pb *PersistentBlackboard) persistenceWorkerStopped() bool {
	select {
	case <-pb.persistDone:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Write method overrides
// ---------------------------------------------------------------------------

// SetOriginalRequest sets the original user request and persists a new task record.
func (pb *PersistentBlackboard) SetOriginalRequest(req string) {
	pb.MapBlackboard.SetOriginalRequest(req)
	_ = pb.persistSafe("new task", func() error {
		return pb.store.PersistNewTask(pb.taskID, pb.sessionID, req)
	})
}

// SetPlan stores a plan and persists it; the notification is gated on the
// write landing (see StoreFact).
func (pb *PersistentBlackboard) SetPlan(plan *orchestration.Plan) {
	pb.MapBlackboard.SetPlan(plan)
	if err := pb.persistSafe("plan", func() error {
		return pb.store.PersistPlan(pb.taskID, plan)
	}); err == nil {
		pb.notifyChanged("plan")
	}
}

// SetStepResult records a step result and persists it; the notification is
// gated on the write landing (see StoreFact).
func (pb *PersistentBlackboard) SetStepResult(stepID, output string, err error, steps []agent.Step) {
	pb.MapBlackboard.SetStepResult(stepID, output, err, steps)

	maxLen := pb.MaxSummaryLen()
	if maxLen == 0 {
		maxLen = 500
	}
	summary := orchestration.GenerateSummary(output, maxLen)

	var errText string
	if err != nil {
		errText = err.Error()
	}

	if persistErr := pb.persistSafe("step result ("+stepID+")", func() error {
		return pb.store.PersistStepResult(pb.taskID, stepID, summary, output, errText, steps)
	}); persistErr == nil {
		pb.notifyChanged("step_result")
	}
}

// AddReflection appends a reflection and persists it (notification gated on
// the write landing, see StoreFact).
func (pb *PersistentBlackboard) AddReflection(r orchestration.Reflection) {
	pb.MapBlackboard.AddReflection(r)
	if err := pb.persistSafe("reflection", func() error {
		return pb.store.PersistReflection(pb.taskID, r)
	}); err == nil {
		pb.notifyChanged("reflection")
	}
}

// StoreFact appends a fact and persists the full list; the change notification
// fires only when the write landed — the panel refetches SQLite, so announcing
// un-persisted state makes the fact look missing. See
// specs/domains/memory/blackboard.md.
func (pb *PersistentBlackboard) StoreFact(fact orchestration.Fact) {
	pb.MapBlackboard.StoreFact(fact)
	facts := pb.GetFacts()
	if err := pb.persistSafe("facts", func() error {
		return pb.store.PersistFacts(pb.taskID, facts)
	}); err == nil {
		pb.notifyChanged("fact")
	}
}

// AddAttachment appends an attachment and persists the full list (gated
// notification, see StoreFact).
func (pb *PersistentBlackboard) AddAttachment(a orchestration.Attachment) {
	pb.MapBlackboard.AddAttachment(a)
	attachments := pb.GetAttachments()
	if err := pb.persistSafe("attachments", func() error {
		return pb.store.PersistAttachments(pb.taskID, attachments)
	}); err == nil {
		pb.notifyChanged("attachment")
	}
}

// RemoveAttachment removes an attachment and persists the full list when one
// was actually removed (gated notification, see StoreFact).
func (pb *PersistentBlackboard) RemoveAttachment(id string) bool {
	removed := pb.MapBlackboard.RemoveAttachment(id)
	if !removed {
		return false
	}
	attachments := pb.GetAttachments()
	if err := pb.persistSafe("attachments", func() error {
		return pb.store.PersistAttachments(pb.taskID, attachments)
	}); err == nil {
		pb.notifyChanged("attachment")
	}
	return true
}

// SetFinalResult does NOT persist — CompleteTask persists the final output.
func (pb *PersistentBlackboard) SetFinalResult(result string) {
	pb.MapBlackboard.SetFinalResult(result)
}

// SetRouting persists the routing decision for the task.
func (pb *PersistentBlackboard) SetRouting(routing *router.RoutingDecision) {
	pb.routingMu.Lock()
	pb.routing = routing
	pb.routingMu.Unlock()
	_ = pb.persistSafe("routing", func() error {
		return pb.store.PersistRouting(pb.taskID, routing)
	})
}

// Routing returns the cached routing decision, or nil if none has been set.
func (pb *PersistentBlackboard) Routing() *router.RoutingDecision {
	pb.routingMu.RLock()
	defer pb.routingMu.RUnlock()
	return pb.routing
}

// ---------------------------------------------------------------------------
// Additional methods (not part of Blackboard interface)
// ---------------------------------------------------------------------------

// CompleteTask marks the task completed, writing synchronously so the status
// change is durable before returning. See specs/domains/memory/blackboard.md.
func (pb *PersistentBlackboard) CompleteTask(attemptCount int) {
	finalOutput := pb.GetFinalResult()
	err := pb.persistSynchronously("task completion", func() error {
		return pb.store.PersistCompletion(pb.taskID, finalOutput, attemptCount)
	})
	if err == nil {
		pb.notifyChanged("completed")
	}
	pb.shutdownPersister()
}

// FailTask marks the task failed, writing synchronously (see CompleteTask).
func (pb *PersistentBlackboard) FailTask() {
	_ = pb.persistSynchronously("task failure", func() error {
		return pb.store.PersistFailure(pb.taskID)
	})
	pb.shutdownPersister()
}

// CancelTask marks the task cancelled, writing synchronously (see CompleteTask).
func (pb *PersistentBlackboard) CancelTask() {
	_ = pb.persistSynchronously("task cancellation", func() error {
		return pb.store.PersistCancellation(pb.taskID)
	})
	pb.shutdownPersister()
}

// PauseTask marks the task paused (resumable checkpoint), writing
// synchronously. Unlike the terminal finalizers it does NOT shut down the
// persister: a paused task is still live and may still write (e.g. a final
// trajectory flush).
func (pb *PersistentBlackboard) PauseTask() {
	_ = pb.persistSynchronously("task pause", func() error {
		return pb.store.PersistPause(pb.taskID)
	})
}

// ReactivateTask reactivates a completed task back to in_progress.
func (pb *PersistentBlackboard) ReactivateTask() {
	_ = pb.persistSafe("task reactivation", func() error {
		return pb.store.ReactivateTask(pb.taskID)
	})
}

// TaskID returns the task ID.
func (pb *PersistentBlackboard) TaskID() string {
	return pb.taskID
}

// SessionID returns the session ID the blackboard was created for; the Manager
// groups tracked blackboards by it so DeleteSession can stop their workers.
func (pb *PersistentBlackboard) SessionID() string {
	return pb.sessionID
}

// ---------------------------------------------------------------------------
// Restoration
// ---------------------------------------------------------------------------

// RestoreBlackboard hydrates a PersistentBlackboard from persisted task state
// (nil, nil when not found). See specs/domains/memory/blackboard.md.
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
		// Raw: SetStepResult would regenerate the summary.
		mb.SetStepResultRaw(stepID, sr)
	}
	for _, r := range state.Reflections {
		mb.AddReflection(r)
	}
	if len(state.Facts) > 0 {
		mb.SetFacts(state.Facts)
	}
	if len(state.Attachments) > 0 {
		mb.SetAttachments(state.Attachments)
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
		persistDone:        make(chan struct{}),
		delegationSpecs:    state.Delegations,
	}
	go pb.persistenceWorker(ch)
	return pb, nil
}

// DelegationSpecs implements core.DelegationSpecReader: the specs hydrated at
// restore, which the Resume auto-resume wave uses to rebuild paused delegates.
// Fresh blackboards return nil.
func (pb *PersistentBlackboard) DelegationSpecs() []tools.DelegationSpec {
	pb.delegationSpecsMu.RLock()
	defer pb.delegationSpecsMu.RUnlock()
	return pb.delegationSpecs
}

// UnitStore implements core.UnitStoreProvider: the task's durable unit store,
// or nil when the store has no unit persistence.
func (pb *PersistentBlackboard) UnitStore() units.Store {
	if p, ok := pb.store.(interface{ UnitStore() units.Store }); ok {
		return p.UnitStore()
	}
	return nil
}

// UnitLedger returns the MAINLINE unit ledger, built once and cached;
// degrades to an in-memory ledger without unit persistence.
func (pb *PersistentBlackboard) UnitLedger() units.Ledger {
	pb.unitLedgerOnce.Do(func() {
		pb.unitLedger = units.NewLedger(pb.UnitStore(), pb.taskID, "")
	})
	return pb.unitLedger
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
