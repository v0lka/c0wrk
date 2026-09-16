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

// DelegationKind classifies what a registered delegation spec represents. The
// spec emitted at register time carries it so a unit-ledger writer can stamp
// the matching unit kind without decoding the task.
type DelegationKind string

const (
	// DelegationKindSubagent is a delegated subagent (the delegate tool).
	DelegationKindSubagent DelegationKind = "subagent"
	// DelegationKindPlanStep is a single step of a declared plan.
	DelegationKindPlanStep DelegationKind = "plan_step"
)

// DelegationSpec is the persistable form of a delegation task: everything the
// system needs to rebuild and re-launch the subagent after a pause — without
// any LLM decision. The registry fires its spec sink (when wired) at
// RegisterTask time, so the spec survives even if the run later pauses or the
// app exits. Top-level delegations carry an empty ParentID and Depth 0;
// sub-delegations (allow_redelegate) reference the delegating subagent's step
// ID and an incremented depth so a resume wave can order children before
// parents.
type DelegationSpec struct {
	Task     DelegationTask `json:"task"`
	ParentID string         `json:"parent_id,omitempty"`
	Depth    int            `json:"depth,omitempty"`
	// Kind classifies the spec (a delegated subagent vs a plan step) so a
	// unit-ledger writer can stamp the right unit kind at register time. Empty
	// is treated as DelegationKindSubagent.
	Kind DelegationKind `json:"kind,omitempty"`
}

// DelegationRegistry tracks active and completed delegations for one
// Conductor run. It is injected into the Conductor context at launch and
// does not outlive the run. Child registries (for allow_redelegate) are
// created with an incremented depth to enforce the recursion cap.
type DelegationRegistry struct {
	mu          sync.Mutex
	delegations map[string]*Delegation
	// order records registration order; All() iterates it because Go map
	// iteration is randomized. Delegations are never removed from the map
	// (only cancelFuncs are), so order always stays in sync with the keys.
	order       []string
	cancelFuncs map[string]context.CancelFunc
	depth       int
	// parentID is the step ID of the delegating subagent for child registries
	// (allow_redelegate), empty for the root registry. It stamps every spec
	// emitted by the sink so a resume wave can order children before parents.
	parentID string
	// specSink, when non-nil, is invoked once per RegisterTask with the full
	// persistable spec. Wired by the launcher (which owns the task store);
	// plan-step local registries and test registries leave it nil.
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

// NewDelegationRegistryWithDepth creates a registry at the given depth.
// Used for recursive delegation (allow_redelegate) where child registries
// track sub-delegations at an incremented depth.
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

// SetSpecSink wires a persistence callback fired by RegisterTask with the full
// delegation spec. parentID stamps specs emitted by this registry: "" for the
// root (top-level) registry, or the delegating subagent's step ID for a child
// registry. The sink must be installed before RegisterTask calls and must not
// block (persistence implementations make it best-effort).
func (r *DelegationRegistry) SetSpecSink(parentID string, sink func(DelegationSpec)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentID = parentID
	r.specSink = sink
}

// RegisterTask registers a full delegation task (delegate-tool form) and
// fires the spec sink when one is wired, persisting everything a later resume
// needs to rebuild the subagent. The registry entry itself stays lean (ID,
// summary, deps, mode) — the full task lives on in the emitted spec. The
// emitted spec is stamped DelegationKindSubagent.
func (r *DelegationRegistry) RegisterTask(t DelegationTask) error {
	return r.RegisterTaskKind(DelegationKindSubagent, t)
}

// RegisterTaskKind is RegisterTask with an explicit unit kind. The plan-step
// path registers its steps with DelegationKindPlanStep so the register-time
// unit-ledger writer records them as plan-step units; the delegate tool uses
// RegisterTask (DelegationKindSubagent). The registry entry itself stays lean
// (ID, summary, deps, mode) — the full task lives on in the emitted spec.
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
// is already registered.
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
	// Don't overwrite a Cancelled status — Cancel may have been called
	// concurrently with the async goroutine's context-done path. The
	// cancellation is intentional and should take precedence. A Paused status
	// is likewise terminal for this run and must not be clobbered.
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

// CompletePaused marks a delegation as paused — a recoverable checkpoint. It
// stores the subagent's partial trajectory (steps) so a resumed run can seed
// its executor and continue from where it stopped. Distinct from Complete,
// which maps any non-nil error to "failed": a cooperative pause is a
// checkpoint, not a failure.
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

// Get returns a snapshot copy of the delegation with the given ID, or nil if not found.
// A copy is returned (not the internal pointer) because callers read fields like
// Steps and Output from goroutines separate from the one that writes them —
// returning the pointer would be a data race.
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
