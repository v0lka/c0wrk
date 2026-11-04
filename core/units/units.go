// Package units defines the durable per-unit record shared by the mainline
// Conductor (persistent blackboard) and isolated execution contexts (the
// goal verifier): kind, parent/depth topology, lifecycle status, an opaque
// rebuild spec (Spec) and resume checkpoint (Steps), behind one Ledger/Store
// API scoped per task. See specs/decisions/048-unified-recovery-ledger.md
// and specs/domains/orchestration/delegation.md (Durable Unit Ledger).
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
// outcome): completed or failed. This is the single definition of "finished"
// (the resume funnel's classification is defined in terms of it); interrupted
// is deliberately non-terminal — the funnel relaunches such a unit, never
// replays it as an outcome. See specs/decisions/048-unified-recovery-ledger.md.
func (s UnitStatus) Terminal() bool {
	switch s {
	case UnitStatusCompleted, UnitStatusFailed:
		return true
	default:
		return false
	}
}

// InFlight reports whether s is still executing or awaiting execution
// (pending/running) — the "abandoned in flight" predicate of the
// settle-on-read path. See specs/decisions/048-unified-recovery-ledger.md.
func (s UnitStatus) InFlight() bool {
	return s == UnitStatusPending || s == UnitStatusRunning
}

// Resumable reports whether a durable checkpoint (when the record carries
// one) must be resumed rather than discarded: pending, running, interrupted.
// Checkpoint and the abandonment sweep are independent writes, so an
// interrupted unit can still carry a usable checkpoint; the funnel treats
// checkpoint + non-terminal status as paused (resume, not fresh relaunch).
// See specs/decisions/048-unified-recovery-ledger.md.
func (s UnitStatus) Resumable() bool {
	return s == UnitStatusPending || s == UnitStatusRunning || s == UnitStatusInterrupted
}

// Container reports whether the kind groups other units (the task root, a
// goal-verification pass) rather than being relaunchable work; containers are
// excluded wherever units are enumerated as work — the resume funnel never
// relaunches them and the session-runtime snapshot never settles them.
// See specs/decisions/048-unified-recovery-ledger.md.
func (k UnitKind) Container() bool {
	switch k {
	case UnitKindTask, UnitKindGoalVerification:
		return true
	default:
		return false
	}
}

// UnitRecord is the durable, self-contained record of one execution unit.
// Spec (rebuild spec) and Steps (resume checkpoint, a JSON []agent.Step) are
// opaque JSON owned by the caller — persisted and returned verbatim, so new
// unit kinds ride the same record. See specs/decisions/048-unified-recovery-ledger.md.
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

// Store is the persistence seam a Ledger writes through: the mainline
// blackboard's store and an isolated context's task store both satisfy it,
// so units written by either are readable by the other for the same task.
// See specs/decisions/048-unified-recovery-ledger.md.
type Store interface {
	// SaveUnit inserts or replaces a unit record (keyed by task + namespace +
	// id). It must be safe for concurrent use.
	SaveUnit(rec UnitRecord) error
	// SettleUnitStatusIfInFlight transitions (taskID, namespace, id) to
	// status — ONLY the status column, only while the unit is still in
	// flight. Idempotent under racing callers and unable to drop another
	// writer's spec/checkpoint (unlike SaveUnit's whole-record upsert);
	// reports (false, nil) for a missing or out-of-flight id.
	SettleUnitStatusIfInFlight(taskID, namespace, id string, status UnitStatus) (bool, error)
	// LoadUnits returns every unit persisted for a task, ordered by creation
	// time. It returns an empty slice (not an error) when none exist.
	LoadUnits(taskID string) ([]UnitRecord, error)
}

// Ledger is the per-task unit ledger shared by the mainline Conductor and
// isolated contexts (e.g. the goal verifier); each is scoped to one task and
// one namespace so their unit ids never collide.
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

// StoreFrom extracts the durable unit Store from a value exposing
// UnitStore() (e.g. a task-persistence adapter); nil when absent, so callers
// can pass the result straight to NewLedger for best-effort in-memory use.
func StoreFrom(v any) Store {
	if p, ok := v.(interface{ UnitStore() Store }); ok {
		return p.UnitStore()
	}
	return nil
}

// ledger is the default Ledger implementation: an in-memory overlay of every
// write, persisted through the store when one is present.
type ledger struct {
	mu        sync.Mutex
	store     Store
	taskID    string
	namespace string
	// mem holds this ledger's own writes, keyed by key(id); the namespace in
	// the key keeps ledgers of different namespaces from cross-resolving ids.
	mem map[string]UnitRecord
	// loaded caches the store's records for this task after the first by-id
	// miss, so a batch of writes pays ONE task-wide store read. List always
	// reads the store and stays authoritative for full enumeration; a record
	// another writer adds after the first read is not visible to get.
	loaded   map[string]UnitRecord
	loadedOK bool
}

// key is the in-memory identity of a unit: its namespace qualifies its id.
func (l *ledger) key(id string) string { return l.namespace + "\x00" + id }

// NewLedger builds a unit ledger scoped to one task and namespace — the
// ISOLATED constructor (e.g. the goal verifier); the namespace keeps its ids
// distinct from the mainline's. A nil store (or a value without unit
// persistence) degrades to best-effort in-memory operation; extract a store
// with units.StoreFrom(...). See specs/decisions/048-unified-recovery-ledger.md.
func NewLedger(store Store, taskID, namespace string) Ledger {
	return &ledger{
		store:     store,
		taskID:    taskID,
		namespace: namespace,
		mem:       make(map[string]UnitRecord),
	}
}

// Begin implements Ledger. Re-registration is idempotent for the durable
// record: a unit may be registered again (the plan arm re-registers every
// step, a resumed run re-registers its delegates), and Begin preserves what
// the caller did not restate — overwriting a checkpoint or re-stamping
// created_at would make the durable record assert something untrue.
// See specs/decisions/048-unified-recovery-ledger.md.
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
	// A status the caller stated explicitly is validated up front; an absent one
	// defaults to pending below and never overwrites a durable status.
	explicitStatus := rec.Status != ""
	if explicitStatus && !rec.Status.Valid() {
		return fmt.Errorf("units: invalid status %q for unit %s", rec.Status, rec.ID)
	}
	if prev, ok := l.get(rec.ID); ok {
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = prev.CreatedAt
		}
		if len(rec.Spec) == 0 {
			rec.Spec = prev.Spec
		}
		if len(rec.Steps) == 0 {
			rec.Steps = prev.Steps
		}
		if !explicitStatus && prev.Status != "" {
			rec.Status = prev.Status
		}
	}
	if rec.Status == "" {
		rec.Status = UnitStatusPending
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	l.mu.Lock()
	l.mem[l.key(rec.ID)] = rec
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
	for k, rec := range l.mem {
		byKey[k] = rec
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
	l.mem[l.key(id)] = rec
	l.mu.Unlock()

	return l.persist(rec)
}

// get returns the unit with the given id in the ledger's OWN namespace,
// preferring the in-memory overlay, then the cached store load, then the
// store. Namespace equality holds in every tier: ids are only unique within
// a namespace, so a same-id unit from another namespace must never resolve
// here — that would settle the WRONG row. See specs/decisions/048-unified-recovery-ledger.md.
func (l *ledger) get(id string) (UnitRecord, bool) {
	k := l.key(id)

	l.mu.Lock()
	if rec, ok := l.mem[k]; ok {
		l.mu.Unlock()
		return rec, true
	}
	if l.loadedOK {
		rec, ok := l.loaded[k]
		l.mu.Unlock()
		return rec, ok
	}
	l.mu.Unlock()

	if l.store == nil {
		return UnitRecord{}, false
	}
	recs, err := l.store.LoadUnits(l.taskID)
	if err != nil {
		return UnitRecord{}, false
	}
	l.mu.Lock()
	l.loaded = make(map[string]UnitRecord, len(recs))
	for _, r := range recs {
		l.loaded[r.Namespace+"\x00"+r.ID] = r
	}
	l.loadedOK = true
	rec, ok := l.loaded[k]
	l.mu.Unlock()
	return rec, ok
}

// persist writes rec through the store when one is configured; a nil store
// keeps the ledger in-memory.
func (l *ledger) persist(rec UnitRecord) error {
	if l.store == nil {
		return nil
	}
	if err := l.store.SaveUnit(rec); err != nil {
		return fmt.Errorf("units: save unit %s: %w", rec.ID, err)
	}
	return nil
}
