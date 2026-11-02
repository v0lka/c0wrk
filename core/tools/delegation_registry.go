package tools

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/agent"
)

// DelegationStatus tracks the lifecycle of a single delegation.
type DelegationStatus string

const (
	DelegationStatusPending   DelegationStatus = "pending"
	DelegationStatusRunning   DelegationStatus = "running"
	DelegationStatusCompleted DelegationStatus = "completed"
	DelegationStatusFailed    DelegationStatus = "failed"
	DelegationStatusCancelled DelegationStatus = "cancelled"
	DelegationStatusPaused    DelegationStatus = "paused"
)

// Delegation records a single subagent invocation launched by the delegate tool.
// One DelegationRegistry tracks all delegations for a single Conductor run;
// child registries (for recursive delegation) track sub-delegations.
type Delegation struct {
	ID          string
	Summary     string
	Status      DelegationStatus
	Output      string
	Error       error
	Steps       []agent.Step
	DependsOn   []string
	Mode        string // "blocking" | "async"
	StartedAt   time.Time
	CompletedAt time.Time
}

// DelegationKind classifies a registered spec so the unit-ledger writer can
// stamp the unit kind without decoding the task.
type DelegationKind string

const (
	// DelegationKindSubagent is a delegated subagent (the delegate tool).
	DelegationKindSubagent DelegationKind = "subagent"
	// DelegationKindPlanStep is a single step of a declared plan.
	DelegationKindPlanStep DelegationKind = "plan_step"
)

// DelegationSpec is the persistable rebuild spec fired by the sink at
// RegisterTask time — everything a resume needs to relaunch without an LLM
// decision (specs/domains/orchestration/delegation.md, unit ledger). Empty
// ParentID / Depth 0 = top-level; children carry the delegating step ID and
// an incremented depth so resume orders them first.
type DelegationSpec struct {
	Task     DelegationTask `json:"task"`
	ParentID string         `json:"parent_id,omitempty"`
	Depth    int            `json:"depth,omitempty"`
	// Kind classifies the spec for the unit ledger; empty means subagent.
	Kind DelegationKind `json:"kind,omitempty"`
}

// DelegationRegistry tracks one Conductor run's delegations; it does not
// outlive the run. Child registries carry an incremented depth (recursion cap).
type DelegationRegistry struct {
	mu          sync.Mutex
	delegations map[string]*Delegation
	// order records registration order (map iteration is random); entries are
	// never removed, so it stays in sync with the keys.
	order       []string
	cancelFuncs map[string]context.CancelFunc
	depth       int
	// parentID stamps child registries' specs with the delegating step ID so
	// resume orders children before parents; empty on the root.
	parentID string
	// specSink is invoked per RegisterTask with the full spec; wired by the
	// launcher, left nil by plan-step and test registries.
	specSink func(DelegationSpec)
}

// NewDelegationRegistry creates a root registry (depth 0).
func NewDelegationRegistry() *DelegationRegistry {
	return &DelegationRegistry{
		delegations: make(map[string]*Delegation),
		cancelFuncs: make(map[string]context.CancelFunc),
		depth:       0,
	}
}

// NewDelegationRegistryWithDepth creates a child registry for recursive
// delegation (allow_redelegate) at an incremented depth.
func NewDelegationRegistryWithDepth(depth int) *DelegationRegistry {
	return &DelegationRegistry{
		delegations: make(map[string]*Delegation),
		cancelFuncs: make(map[string]context.CancelFunc),
		depth:       depth,
	}
}

// Depth returns the current delegation depth (0 for the Conductor's registry).
func (r *DelegationRegistry) Depth() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.depth
}

// SetSpecSink wires the persistence callback; parentID stamps emitted specs.
// Install before any RegisterTask; must not block (persistence is best-effort).
func (r *DelegationRegistry) SetSpecSink(parentID string, sink func(DelegationSpec)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentID = parentID
	r.specSink = sink
}

// RegisterTask registers the delegate-tool form and fires the spec sink; the
// entry stays lean (ID/summary/deps/mode) — the full task lives in the spec.
func (r *DelegationRegistry) RegisterTask(t DelegationTask) error {
	return r.RegisterTaskKind(DelegationKindSubagent, t)
}

// RegisterTaskKind is RegisterTask with an explicit unit kind (plan steps
// register DelegationKindPlanStep for the unit ledger).
func (r *DelegationRegistry) RegisterTaskKind(kind DelegationKind, t DelegationTask) error {
	if t.Mode == "" {
		t.Mode = "blocking"
	}
	if err := r.Register(t.ID, t.Summary, t.DependsOn, t.Mode); err != nil {
		return err
	}
	if kind == "" {
		kind = DelegationKindSubagent
	}
	r.mu.Lock()
	sink := r.specSink
	parentID := r.parentID
	depth := r.depth
	r.mu.Unlock()
	if sink != nil {
		sink(DelegationSpec{Task: t, ParentID: parentID, Depth: depth, Kind: kind})
	}
	return nil
}

// Register adds a new delegation as "pending". Returns an error if the ID
// is already registered. Unlike RegisterTask it fires no spec sink — the
// delegate duplicate-guard replay uses it so the unit ledger is not
// re-written. See specs/contracts/conductor-tools.md (delegate).
func (r *DelegationRegistry) Register(id, summary string, dependsOn []string, mode string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.delegations[id]; exists {
		return errDelegationIDExists(id)
	}
	r.delegations[id] = &Delegation{
		ID:        id,
		Summary:   summary,
		Status:    DelegationStatusPending,
		DependsOn: append([]string(nil), dependsOn...),
		Mode:      mode,
	}
	r.order = append(r.order, id)
	return nil
}

// Start marks a delegation as "running" and stores the cancellation handle.
// No-op if the delegation does not exist or is no longer pending.
func (r *DelegationRegistry) Start(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delegations[id]
	if !ok || d.Status != DelegationStatusPending {
		return
	}
	d.Status = DelegationStatusRunning
	d.StartedAt = time.Now()
	if cancel != nil {
		r.cancelFuncs[id] = cancel
	}
}

// Complete marks a delegation as "completed" or "failed" and stores the
// output, error, and steps. No-op if the delegation does not exist.
func (r *DelegationRegistry) Complete(id, output string, execErr error, steps []agent.Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delegations[id]
	if !ok {
		return
	}
	// Cancelled/Paused are intentional terminal states for this run and take
	// precedence over a racing completion.
	if d.Status == DelegationStatusCancelled || d.Status == DelegationStatusPaused {
		return
	}
	d.Output = output
	d.Error = execErr
	d.Steps = steps
	d.CompletedAt = time.Now()
	if execErr != nil {
		d.Status = DelegationStatusFailed
	} else {
		d.Status = DelegationStatusCompleted
	}
	delete(r.cancelFuncs, id)
}

// CompletePaused marks the recoverable checkpoint, storing the partial
// trajectory a resume seeds from — a cooperative pause is not a failure.
func (r *DelegationRegistry) CompletePaused(id, output string, steps []agent.Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delegations[id]
	if !ok {
		return
	}
	if d.Status == DelegationStatusCancelled {
		return
	}
	d.Output = output
	d.Steps = steps
	d.CompletedAt = time.Now()
	d.Status = DelegationStatusPaused
	delete(r.cancelFuncs, id)
}

// Cancel cancels a pending or running delegation via its stored CancelFunc
// and marks it "cancelled". No-op for completed, failed, cancelled, paused, or
// unknown delegations.
func (r *DelegationRegistry) Cancel(id string) {
	r.mu.Lock()
	d, ok := r.delegations[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	if d.Status == DelegationStatusCompleted || d.Status == DelegationStatusFailed || d.Status == DelegationStatusCancelled || d.Status == DelegationStatusPaused {
		r.mu.Unlock()
		return
	}
	if cancel, hasCancel := r.cancelFuncs[id]; hasCancel {
		cancel()
	}
	d.Status = DelegationStatusCancelled
	d.CompletedAt = time.Now()
	delete(r.cancelFuncs, id)
	r.mu.Unlock()
}

// Get returns a snapshot copy, not the internal pointer — callers read Steps
// and Output from goroutines other than the writer's.
func (r *DelegationRegistry) Get(id string) *Delegation {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delegations[id]
	if !ok {
		return nil
	}
	cp := *d
	return &cp
}

// ListPending returns the IDs of all delegations currently pending or running.
// Used by the finish-join check to prevent abandoning async work silently.
func (r *DelegationRegistry) ListPending() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, d := range r.delegations {
		if d.Status == DelegationStatusPending || d.Status == DelegationStatusRunning {
			ids = append(ids, id)
		}
	}
	return ids
}

// Has returns true if a delegation with the given ID exists.
func (r *DelegationRegistry) Has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.delegations[id]
	return ok
}

// IsCompleted returns true if the delegation exists and is in a terminal
// state (completed, failed, or cancelled).
func (r *DelegationRegistry) IsCompleted(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.delegations[id]
	if !ok {
		return false
	}
	return d.Status == DelegationStatusCompleted || d.Status == DelegationStatusFailed || d.Status == DelegationStatusCancelled
}

// All returns a snapshot slice of all delegations in insertion order.
// Used for HandleResult.Delegations summary at the end of a Conductor run.
func (r *DelegationRegistry) All() []Delegation {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Delegation, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.delegations[id])
	}
	return out
}

// errDelegationIDExists is a sentinel returned when Register is called with
// a duplicate ID. Typed so callers can use errors.Is.
type delegationIDExistsErr struct{ id string }

func (e *delegationIDExistsErr) Error() string { return "delegation ID already exists: " + e.id }

func errDelegationIDExists(id string) error { return &delegationIDExistsErr{id: id} }

// IsDelegationIDExistsError reports whether err is a duplicate-ID error.
func IsDelegationIDExistsError(err error) bool {
	var e *delegationIDExistsErr
	return errors.As(err, &e)
}

// --- Context plumbing ---

type delegationRegistryKey struct{}

// WithDelegationRegistry returns a new context with the registry attached.
func WithDelegationRegistry(ctx context.Context, registry *DelegationRegistry) context.Context {
	return context.WithValue(ctx, delegationRegistryKey{}, registry)
}

// DelegationRegistryFrom extracts the registry from the context, or returns nil.
func DelegationRegistryFrom(ctx context.Context) *DelegationRegistry {
	if v, ok := ctx.Value(delegationRegistryKey{}).(*DelegationRegistry); ok {
		return v
	}
	return nil
}
