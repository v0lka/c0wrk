package tools

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
)

// DelegationStatus tracks the lifecycle of a single delegation.
type DelegationStatus string

const (
	DelegationStatusPending   DelegationStatus = "pending"
	DelegationStatusRunning   DelegationStatus = "running"
	DelegationStatusCompleted DelegationStatus = "completed"
	DelegationStatusFailed    DelegationStatus = "failed"
	DelegationStatusCancelled DelegationStatus = "cancelled"
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

// DelegationRegistry tracks active and completed delegations for one
// Conductor run. It is injected into the Conductor context at launch and
// does not outlive the run. Child registries (for allow_redelegate) are
// created with an incremented depth to enforce the recursion cap.
type DelegationRegistry struct {
	mu          sync.Mutex
	delegations map[string]*Delegation
	cancelFuncs map[string]context.CancelFunc
	depth       int
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
	// cancellation is intentional and should take precedence.
	if d.Status == DelegationStatusCancelled {
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

// Cancel cancels a pending or running delegation via its stored CancelFunc
// and marks it "cancelled". No-op for completed, failed, or unknown delegations.
func (r *DelegationRegistry) Cancel(id string) {
	r.mu.Lock()
	d, ok := r.delegations[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	if d.Status == DelegationStatusCompleted || d.Status == DelegationStatusFailed || d.Status == DelegationStatusCancelled {
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
	out := make([]Delegation, 0, len(r.delegations))
	for _, d := range r.delegations {
		out = append(out, *d)
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
