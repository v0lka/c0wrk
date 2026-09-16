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
// outcome): completed or failed.
//
// `interrupted` is deliberately NON-terminal: it marks a unit left unfinished
// by a crash/app exit, and the resume funnel must relaunch it (fresh, since it
// carries no checkpoint) rather than replay it as a finished outcome. The
// funnel's classification (resumeUnit.terminal) is defined in terms of this
// method, so this is the single definition of "finished".
func (s UnitStatus) Terminal() bool {
	switch s {
	case UnitStatusCompleted, UnitStatusFailed:
		return true
	default:
		return false
	}
}

// InFlight reports whether s is a status a unit occupies while it is still
// executing or awaiting execution (pending or running). It is the predicate the
// "settle an abandoned unit on read" path uses: only an in-flight unit on a
// non-executing task was abandoned by a crash/app exit.
func (s UnitStatus) InFlight() bool {
	return s == UnitStatusPending || s == UnitStatusRunning
}

// Container reports whether the kind is a CONTAINER record — a unit that groups
// other units (the task root, a goal-verification pass) rather than being
// relaunchable execution work itself. Containers are excluded wherever units
// are enumerated as work: the resume funnel never relaunches them, and the
// session-runtime snapshot must not surface (or settle) them either.
func (k UnitKind) Container() bool {
	switch k {
	case UnitKindTask, UnitKindGoalVerification:
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
// status + steps), SettleUnitStatusIfInFlight performs a column-scoped
// conditional status transition, and LoadUnits returns every unit persisted
// under a task, across all namespaces. Implementations live outside this
// package so the contract stays free of storage dependencies.
type Store interface {
	// SaveUnit inserts or replaces a unit record (keyed by task + namespace +
	// id). It must be safe for concurrent use.
	SaveUnit(rec UnitRecord) error
	// SettleUnitStatusIfInFlight transitions the unit identified by
	// (taskID, namespace, id) to status, updating ONLY its status column, and
	// reports whether the row was actually transitioned. The write is applied
	// only while the unit is still in flight (pending/running), so it is
	// idempotent under concurrent callers — exactly one of them observes the
	// transition and emits, the rest are no-ops — and because it never rewrites
	// spec/steps/topology it cannot drop another writer's rebuild spec or resume
	// checkpoint the way SaveUnit's whole-record upsert can. An id that does not
	// exist, or is already out of flight, reports (false, nil).
	SettleUnitStatusIfInFlight(taskID, namespace, id string, status UnitStatus) (bool, error)
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
	// mem holds this ledger's own writes, keyed by key(id) — the namespace is
	// part of the key, so a mainline ledger (namespace "") and an isolated one
	// (the goal verifier) cannot resolve each other's same-id units.
	mem map[string]UnitRecord
	// loaded caches the store's records for this task after the first by-id
	// lookup that misses mem, keyed like mem. It makes a batch of Begin /
	// Checkpoint / Settle calls (a plan arm registering every step, a resume
	// wave settling every abandoned unit) pay ONE task-wide store read instead
	// of one per call. List always reads the store, so it stays authoritative
	// for a full enumeration; a record another writer adds after this ledger's
	// first read is not visible to get.
	loaded   map[string]UnitRecord
	loadedOK bool
}

// key is the in-memory identity of a unit: its namespace qualifies its id.
func (l *ledger) key(id string) string { return l.namespace + "\x00" + id }

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
//
// Re-registration is idempotent for the durable record: a unit can be
// registered more than once for the same task (the plan arm re-registers every
// step on each execute_plan, a resumed run re-registers its delegates), and the
// record it lands on may already carry a rebuild spec, a resume checkpoint, a
// settled status and a creation timestamp. Begin therefore preserves what the
// caller did not supply instead of clobbering it — overwriting a checkpoint, or
// re-stamping created_at, would make the single durable record assert something
// untrue and shift created_at-ordered reads.
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
// preferring the in-memory overlay, then the cached store load, then the store.
// Namespace equality is required in every tier: an id is only unique within a
// namespace, so a mainline ledger (namespace "") must never resolve a
// goal-verifier unit that happens to share the id — resolving it would settle
// (and persist) the WRONG row, leaving the intended one un-settled.
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
