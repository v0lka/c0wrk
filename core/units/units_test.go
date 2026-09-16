package units

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/agent"
)

// fakeStore is an in-memory Store used to exercise the ledger without SQLite.
type fakeStore struct {
	mu      sync.Mutex
	recs    map[string]UnitRecord
	saveErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{recs: make(map[string]UnitRecord)}
}

func fakeKey(taskID, namespace, id string) string {
	return taskID + "\x00" + namespace + "\x00" + id
}

func (f *fakeStore) SaveUnit(rec UnitRecord) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[fakeKey(rec.TaskID, rec.Namespace, rec.ID)] = rec
	return nil
}

func (f *fakeStore) LoadUnits(taskID string) ([]UnitRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []UnitRecord
	for _, rec := range f.recs {
		if rec.TaskID == taskID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recs)
}

// TestLedgerBeginCheckpointSettleListRoundTrips is the headline contract: a
// unit's spec, status and steps survive a full Begin → Checkpoint → Settle
// cycle and come back out of List.
func TestLedgerBeginCheckpointSettleListRoundTrips(t *testing.T) {
	store := newFakeStore()
	l := NewLedger(store, "task-1", "main")

	spec := json.RawMessage(`{"task":{"id":"sub_1","task":"do the thing"}}`)
	if err := l.Begin(UnitRecord{
		ID:       "sub_1",
		Kind:     UnitKindSubagent,
		ParentID: "step_2",
		Depth:    1,
		Status:   UnitStatusRunning,
		Spec:     spec,
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	steps := []agent.Step{{Thought: "first"}, {Thought: "second"}}
	if err := l.Checkpoint("sub_1", steps); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := l.Settle("sub_1", UnitStatusCompleted); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	recs, err := l.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("List returned %d units, want 1", len(recs))
	}
	got := recs[0]
	if got.ID != "sub_1" || got.TaskID != "task-1" || got.Namespace != "main" {
		t.Errorf("identity = %+v, want sub_1/task-1/main", got)
	}
	if got.Kind != UnitKindSubagent || got.ParentID != "step_2" || got.Depth != 1 {
		t.Errorf("topology = kind=%q parent=%q depth=%d, want subagent/step_2/1", got.Kind, got.ParentID, got.Depth)
	}
	if got.Status != UnitStatusCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if string(got.Spec) != string(spec) {
		t.Errorf("spec = %s, want %s", got.Spec, spec)
	}
	var roundTripped []agent.Step
	if err := json.Unmarshal(got.Steps, &roundTripped); err != nil {
		t.Fatalf("unmarshal steps: %v", err)
	}
	if len(roundTripped) != 2 || roundTripped[0].Thought != "first" || roundTripped[1].Thought != "second" {
		t.Errorf("steps round-trip = %+v, want two thoughts", roundTripped)
	}
	if store.count() != 1 {
		t.Errorf("store holds %d records, want 1 (write-through)", store.count())
	}
}

// TestLedgerNilStoreDegradesToMemory verifies the graceful-degradation
// contract: with no store the ledger still round-trips units in memory.
func TestLedgerNilStoreDegradesToMemory(t *testing.T) {
	l := NewLedger(nil, "task-x", "isolated")

	if err := l.Begin(UnitRecord{ID: "u1", Kind: UnitKindGoalVerification}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := l.Checkpoint("u1", []agent.Step{{Thought: "verify"}}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := l.Settle("u1", UnitStatusPaused); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	recs, err := l.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("List returned %d units, want 1", len(recs))
	}
	if recs[0].Status != UnitStatusPaused || recs[0].Namespace != "isolated" {
		t.Errorf("record = %+v, want paused/isolated", recs[0])
	}
}

// TestLedgerBeginDefaultsAndStamp verifies Begin stamps the ledger's task and
// namespace, defaults an empty status to pending, and records timestamps.
func TestLedgerBeginDefaultsAndStamp(t *testing.T) {
	l := NewLedger(nil, "task-9", "ns-9")
	if err := l.Begin(UnitRecord{ID: "u"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recs, _ := l.List()
	got := recs[0]
	if got.Status != UnitStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.TaskID != "task-9" || got.Namespace != "ns-9" {
		t.Errorf("stamp = %q/%q, want task-9/ns-9", got.TaskID, got.Namespace)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}
}

// TestLedgerBeginRequiresID and the invalid-status guards cover the input
// validation the ledger enforces.
func TestLedgerValidateInputs(t *testing.T) {
	l := NewLedger(nil, "task", "")

	if err := l.Begin(UnitRecord{}); !errors.Is(err, ErrUnitIDRequired) {
		t.Errorf("Begin without id = %v, want ErrUnitIDRequired", err)
	}
	if err := l.Begin(UnitRecord{ID: "bad", Status: UnitStatus("bogus")}); err == nil {
		t.Error("Begin with invalid status should fail")
	}
	if err := l.Checkpoint("missing", nil); !errors.Is(err, ErrUnitNotFound) {
		t.Errorf("Checkpoint unknown = %v, want ErrUnitNotFound", err)
	}
	if err := l.Settle("missing", UnitStatusCompleted); !errors.Is(err, ErrUnitNotFound) {
		t.Errorf("Settle unknown = %v, want ErrUnitNotFound", err)
	}
	if err := l.Settle("x", UnitStatus("bogus")); err == nil {
		t.Error("Settle with invalid status should fail")
	}
}

// TestLedgerSharedTaskAcrossNamespaces verifies two ledgers scoped to the same
// task but different namespaces (mainline + isolated) both persist under that
// task and see each other's units via List.
func TestLedgerSharedTaskAcrossNamespaces(t *testing.T) {
	store := newFakeStore()
	mainline := NewLedger(store, "task-shared", "")
	isolated := NewLedger(store, "task-shared", "verifier")

	if err := mainline.Begin(UnitRecord{ID: "step_1", Kind: UnitKindPlanStep}); err != nil {
		t.Fatalf("mainline Begin: %v", err)
	}
	if err := isolated.Begin(UnitRecord{ID: "step_1", Kind: UnitKindGoalVerification}); err != nil {
		t.Fatalf("isolated Begin: %v", err)
	}

	// Same unit id in different namespaces must not collide.
	recs, err := mainline.List()
	if err != nil {
		t.Fatalf("mainline List: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("List returned %d units, want 2 (one per namespace)", len(recs))
	}
	seen := map[string]UnitKind{}
	for _, rec := range recs {
		seen[rec.Namespace] = rec.Kind
	}
	if seen[""] != UnitKindPlanStep || seen["verifier"] != UnitKindGoalVerification {
		t.Errorf("namespaced kinds = %+v, want mainline plan_step + verifier goal_verification", seen)
	}

	isolatedRecs, _ := isolated.List()
	if len(isolatedRecs) != 2 {
		t.Errorf("isolated List returned %d, want 2 (same task)", len(isolatedRecs))
	}
}

// TestStoreFrom verifies the store extraction helper used by isolated
// contexts: nil and unsupported values yield nil (best-effort in-memory),
// while a value exposing UnitStore() yields that store.
func TestStoreFrom(t *testing.T) {
	if StoreFrom(nil) != nil {
		t.Error("StoreFrom(nil) should be nil")
	}
	if StoreFrom("not-a-store") != nil {
		t.Error("StoreFrom(unsupported) should be nil")
	}

	store := newFakeStore()
	provider := &storeProvider{store: store}
	if got := StoreFrom(provider); got != store {
		t.Errorf("StoreFrom(provider) = %v, want the provider's store", got)
	}
	// A provider that has no unit persistence degrades to nil.
	if got := StoreFrom(&storeProvider{}); got != nil {
		t.Errorf("StoreFrom(empty provider) = %v, want nil", got)
	}
}

type storeProvider struct{ store Store }

func (p *storeProvider) UnitStore() Store { return p.store }

// TestLedgerStatusTerminal documents the Terminal classification used by
// callers to decide whether a unit is finished.
func TestLedgerStatusTerminal(t *testing.T) {
	terminal := []UnitStatus{UnitStatusCompleted, UnitStatusFailed, UnitStatusInterrupted}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	inFlight := []UnitStatus{UnitStatusPending, UnitStatusRunning, UnitStatusPaused}
	for _, s := range inFlight {
		if s.Terminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}
