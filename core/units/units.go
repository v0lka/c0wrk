// Package units defines the durable per-unit record contract shared by the
// mainline Conductor (backed by the persistent blackboard) and isolated
// execution contexts (such as the goal verifier).
//
// A "unit" is one durable record of an execution unit — a plan step, a
// delegated subagent, a goal-verification pass, and so on. Every unit carries
// its kind, parent/depth topology, lifecycle status, a rebuild spec (enough to
// re-launch it without an LLM decision) and a resume checkpoint (the ReAct
// step trajectory it can resume from). One write/read API (Ledger) is used by
// both the mainline and isolated constructors, so units persisted under a task
// are visible to every ledger scoped to that task.
package units

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/agent"
)

// UnitKind classifies a durable unit record. The kind is advisory metadata: it
// lets a reader tell a plan step apart from a subagent delegation or a goal
// verification pass without decoding the rebuild spec.
type UnitKind string

const (
	// UnitKindTask is the root unit of an orchestration task.
	UnitKindTask UnitKind = "task"
	// UnitKindPlanStep is a single step of a plan.
	UnitKindPlanStep UnitKind = "plan_step"
	// UnitKindSubagent is a delegated subagent (the delegate tool).
	UnitKindSubagent UnitKind = "subagent"
	// UnitKindGoalVerification is an isolated goal-verification pass.
	UnitKindGoalVerification UnitKind = "goal_verification"
	// UnitKindTool is a single tool invocation tracked as a unit.
	UnitKindTool UnitKind = "tool"
)

// UnitStatus is the lifecycle state of a unit record.
type UnitStatus string

const (
	// UnitStatusPending is a unit that has been registered but not started.
	UnitStatusPending UnitStatus = "pending"
	// UnitStatusRunning is a unit currently executing.
	UnitStatusRunning UnitStatus = "running"
	// UnitStatusPaused is a unit whose execution stopped at a resumable
	// checkpoint and can be resumed.
	UnitStatusPaused UnitStatus = "paused"
	// UnitStatusCompleted is a unit that finished successfully.
	UnitStatusCompleted UnitStatus = "completed"
	// UnitStatusFailed is a unit that finished with an error.
	UnitStatusFailed UnitStatus = "failed"
	// UnitStatusInterrupted is a unit left unfinished by a crash or app exit;
	// unlike paused it carries no explicit resume intent.
	UnitStatusInterrupted UnitStatus = "interrupted"
)

// Valid reports whether s is one of the defined unit statuses.
func (s UnitStatus) Valid() bool {
	switch s {
	case UnitStatusPending, UnitStatusRunning, UnitStatusPaused,
		UnitStatusCompleted, UnitStatusFailed, UnitStatusInterrupted:
		return true
	default:
		return false
	}
}

// Terminal reports whether s is a status a unit cannot leave (a terminal
// outcome). A completed, failed or interrupted unit is finished; pending,
// running and paused units are still in flight.
func (s UnitStatus) Terminal() bool {
	switch s {
	case UnitStatusCompleted, UnitStatusFailed, UnitStatusInterrupted:
		return true
	default:
		return false
	}
}

// UnitRecord is the durable, self-contained record of one execution unit.
//
// Spec and Steps are opaque JSON blobs owned by the caller: Spec carries the
// rebuild spec (everything needed to re-launch the unit) and Steps carries the
// resume checkpoint (a JSON-encoded []agent.Step). The ledger never inspects
// them beyond persisting and returning them verbatim, so new unit kinds can
// ride the same record without a schema change.
type UnitRecord struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	Namespace string          `json:"namespace,omitempty"`
	Kind      UnitKind        `json:"kind,omitempty"`
	ParentID  string          `json:"parent_id,omitempty"`
	Depth     int             `json:"depth,omitempty"`
	Status    UnitStatus      `json:"status"`
	Spec      json.RawMessage `json:"spec,omitempty"`
	Steps     json.RawMessage `json:"steps,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Store is the persistence seam a Ledger writes through. It is a facade over
// the orchestrator's task storage: the mainline blackboard's store and an
// isolated context's task store both satisfy it, so a unit written by either
// is readable by the other when they target the same task.
//
// This is the single write/read API: SaveUnit upserts a whole record (spec +
// status + steps) and LoadUnits returns every unit persisted under a task,
// across all namespaces. Implementations live outside this package so the
// contract stays free of storage dependencies.
type Store interface {
	// SaveUnit inserts or replaces a unit record (keyed by task + namespace +
	// id). It must be safe for concurrent use.
	SaveUnit(rec UnitRecord) error
	// LoadUnits returns every unit persisted for a task, ordered by creation
	// time. It returns an empty slice (not an error) when none exist.
	LoadUnits(taskID string) ([]UnitRecord, error)
}

// Ledger is the per-task unit ledger. The mainline Conductor and isolated
// contexts (e.g. the goal verifier) share this API; each is scoped to one task
// and one namespace so their unit ids never collide.
type Ledger interface {
	// Begin registers a new unit record. It stamps the ledger's task and
	// namespace, defaults an empty status to pending, and returns an error when
	// the record has no id or the store rejects the write.
	Begin(rec UnitRecord) error
	// Checkpoint replaces the resume checkpoint (ReAct steps) of the unit with
	// the given id.
	Checkpoint(id string, steps []agent.Step) error
	// Settle records the final (or paused/running) status of the unit with the
	// given id.
	Settle(id string, status UnitStatus) error
	// List returns every unit persisted under the ledger's task, across all
	// namespaces, with the ledger's own in-memory writes overlaid.
	List() ([]UnitRecord, error)
}

// ErrUnitNotFound is returned by Checkpoint/Settle when no unit with the given
// id exists in the ledger's task/namespace.
var ErrUnitNotFound = errors.New("units: unit not found")

// ErrUnitIDRequired is returned by Begin when the record carries no id.
var ErrUnitIDRequired = errors.New("units: unit id is required")

// StoreFrom extracts the durable unit Store from an arbitrary value (for
// example a task-persistence adapter that exposes a UnitStore() method). It
// returns nil when v is nil or does not provide one, so callers can pass the
// result straight to NewLedger and get a best-effort in-memory ledger.
func StoreFrom(v any) Store {
	if p, ok := v.(interface{ UnitStore() Store }); ok {
		return p.UnitStore()
	}
	return nil
}

// ledger is the default Ledger implementation. It keeps an in-memory overlay
// of every write so it works with no store at all, and persists each write
// through the store when one is present.
type ledger struct {
	mu        sync.Mutex
	store     Store
	taskID    string
	namespace string
	mem       map[string]UnitRecord
}

// NewLedger builds a unit ledger scoped to one task and namespace. This is the
// ISOLATED constructor: an isolated execution context (such as the goal
// verifier) passes its task store, the task it belongs to, and a namespace
// that keeps its unit ids distinct from the mainline's.
//
// store may be nil — or a value that does not provide unit persistence — in
// which case the ledger degrades to best-effort in-memory operation: units
// round-trip within the process but are not durable. Pass units.StoreFrom(...)
// to extract a store from a task-persistence adapter.
func NewLedger(store Store, taskID, namespace string) Ledger {
	return &ledger{
		store:     store,
		taskID:    taskID,
		namespace: namespace,
		mem:       make(map[string]UnitRecord),
	}
}

// Begin implements Ledger.
func (l *ledger) Begin(rec UnitRecord) error {
	if rec.ID == "" {
		return ErrUnitIDRequired
	}
	now := time.Now().UTC()
	if l.taskID != "" {
		rec.TaskID = l.taskID
	}
	if l.namespace != "" {
		rec.Namespace = l.namespace
	}
	if rec.Status == "" {
		rec.Status = UnitStatusPending
	}
	if !rec.Status.Valid() {
		return fmt.Errorf("units: invalid status %q for unit %s", rec.Status, rec.ID)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	l.mu.Lock()
	l.mem[rec.ID] = rec
	l.mu.Unlock()

	return l.persist(rec)
}

// Checkpoint implements Ledger.
func (l *ledger) Checkpoint(id string, steps []agent.Step) error {
	data, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("units: marshal checkpoint for %s: %w", id, err)
	}
	return l.update(id, func(rec *UnitRecord) {
		rec.Steps = data
	})
}

// Settle implements Ledger.
func (l *ledger) Settle(id string, status UnitStatus) error {
	if !status.Valid() {
		return fmt.Errorf("units: invalid status %q for unit %s", status, id)
	}
	return l.update(id, func(rec *UnitRecord) {
		rec.Status = status
	})
}

// List implements Ledger.
func (l *ledger) List() ([]UnitRecord, error) {
	var persisted []UnitRecord
	if l.store != nil {
		recs, err := l.store.LoadUnits(l.taskID)
		if err != nil {
			return nil, fmt.Errorf("units: load units for task %s: %w", l.taskID, err)
		}
		persisted = recs
	}

	l.mu.Lock()
	byKey := make(map[string]UnitRecord, len(persisted)+len(l.mem))
	for _, rec := range persisted {
		byKey[rec.Namespace+"\x00"+rec.ID] = rec
	}
	// The in-memory overlay wins: a write that the store rejected (or that had
	// no store to reach) is still visible to the writer.
	for id, rec := range l.mem {
		byKey[rec.Namespace+"\x00"+id] = rec
	}
	l.mu.Unlock()

	out := make([]UnitRecord, 0, len(byKey))
	for _, rec := range byKey {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// update applies fn to the unit with the given id (loading it from the store
// when it is not in the in-memory overlay), then persists the result.
func (l *ledger) update(id string, fn func(*UnitRecord)) error {
	rec, ok := l.get(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnitNotFound, id)
	}
	fn(&rec)
	rec.UpdatedAt = time.Now().UTC()

	l.mu.Lock()
	l.mem[id] = rec
	l.mu.Unlock()

	return l.persist(rec)
}

// get returns the unit with the given id, preferring the in-memory overlay and
// falling back to the store.
func (l *ledger) get(id string) (UnitRecord, bool) {
	l.mu.Lock()
	rec, ok := l.mem[id]
	l.mu.Unlock()
	if ok {
		return rec, true
	}
	if l.store == nil {
		return UnitRecord{}, false
	}
	recs, err := l.store.LoadUnits(l.taskID)
	if err != nil {
		return UnitRecord{}, false
	}
	for _, r := range recs {
		if r.ID == id && (l.namespace == "" || r.Namespace == l.namespace) {
			return r, true
		}
	}
	return UnitRecord{}, false
}

// persist writes rec through the store when one is configured. A nil store is
// a no-op so the ledger still works in-memory.
func (l *ledger) persist(rec UnitRecord) error {
	if l.store == nil {
		return nil
	}
	if err := l.store.SaveUnit(rec); err != nil {
		return fmt.Errorf("units: save unit %s: %w", rec.ID, err)
	}
	return nil
}
