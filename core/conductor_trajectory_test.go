package core

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// Compile-time assertion that compositeTrajectoryStore satisfies the SDK
// TrajectoryStore contract (Sync + Steps).
var _ agent.TrajectoryStore = (*compositeTrajectoryStore)(nil)

// trajectoryTaskStore is a TaskPersistence mock focused on SaveTrajectory. It
// records every persisted snapshot, and can simulate a slow DB (saveDelay) or a
// failing DB (saveErr). All non-trajectory methods are inherited from
// mockTaskStoreWithReactivate as no-ops. Safe for concurrent use.
type trajectoryTaskStore struct {
	mockTaskStoreWithReactivate
	mu        sync.Mutex
	saved     [][]agent.Step
	saveDelay time.Duration
	saveErr   error
}

func (m *trajectoryTaskStore) SaveTrajectory(_ string, steps []agent.Step) error {
	if m.saveDelay > 0 {
		time.Sleep(m.saveDelay)
	}
	m.mu.Lock()
	snap := make([]agent.Step, len(steps))
	copy(snap, steps)
	m.saved = append(m.saved, snap)
	m.mu.Unlock()
	return m.saveErr
}

func (m *trajectoryTaskStore) LoadTrajectory(_ string) ([]agent.Step, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.saved) == 0 {
		return nil, nil
	}
	last := m.saved[len(m.saved)-1]
	out := make([]agent.Step, len(last))
	copy(out, last)
	return out, nil
}

func (m *trajectoryTaskStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

func (m *trajectoryTaskStore) lastSaved() []agent.Step {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.saved) == 0 {
		return nil
	}
	return m.saved[len(m.saved)-1]
}

// trajSteps builds a trajectory of named steps for assertions.
func trajSteps(names ...string) []agent.Step {
	steps := make([]agent.Step, len(names))
	for i, n := range names {
		steps[i] = agent.Step{
			Thought:     "thinking " + n,
			Action:      llm.ToolCall{ID: n, Name: n},
			Observation: "obs " + n,
		}
	}
	return steps
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitForTrajectoryCount polls the mock until at least `want` snapshots have
// been persisted, failing the test on timeout. Because the DB write is async,
// callers MUST wait before asserting on persisted state.
func waitForTrajectoryCount(tb testing.TB, m *trajectoryTaskStore, want int, timeout time.Duration) {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.count() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	tb.Fatalf("timed out waiting for %d persisted snapshots; got %d", want, m.count())
}

func assertStepsEqual(tb testing.TB, got, want []agent.Step) {
	tb.Helper()
	if len(got) != len(want) {
		tb.Fatalf("expected %d steps, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i].Action.Name != want[i].Action.Name {
			tb.Errorf("step %d: action name = %q, want %q", i, got[i].Action.Name, want[i].Action.Name)
		}
		if got[i].Thought != want[i].Thought {
			tb.Errorf("step %d: thought = %q, want %q", i, got[i].Thought, want[i].Thought)
		}
	}
}

// TestCompositeTrajectoryStore_PersistsFullTrajectoryOnSync verifies that each
// Sync persists the FULL current trajectory (a complete snapshot upsert), not
// just a delta — satisfying the "each ReAct iteration persists the full
// trajectory" criterion. Between iterations we let the prior async write settle,
// exactly as the LLM-bound ReAct loop would (seconds between Syncs vs. a
// millisecond DB write).
func TestCompositeTrajectoryStore_PersistsFullTrajectoryOnSync(t *testing.T) {
	store := &trajectoryTaskStore{}
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", store, discardLogger())

	// Simulate the ReAct loop syncing a growing trajectory.
	cs.Sync(trajSteps("a"))
	waitForTrajectoryCount(t, store, 1, time.Second)
	assertStepsEqual(t, store.lastSaved(), trajSteps("a"))

	cs.Sync(trajSteps("a", "b"))
	waitForTrajectoryCount(t, store, 2, time.Second)
	assertStepsEqual(t, store.lastSaved(), trajSteps("a", "b"))

	cs.Sync(trajSteps("a", "b", "c"))
	waitForTrajectoryCount(t, store, 3, time.Second)
	assertStepsEqual(t, store.lastSaved(), trajSteps("a", "b", "c"))

	// The in-memory copy (consumed by reflect) must match.
	assertStepsEqual(t, cs.Steps(), trajSteps("a", "b", "c"))
}

// TestCompositeTrajectoryStore_StepsReadsMemoryImmediately verifies the reflect
// tool can read the trajectory from memory synchronously, without waiting for
// the (async) DB write. Sync updates memory before kicking off the DB write.
func TestCompositeTrajectoryStore_StepsReadsMemoryImmediately(t *testing.T) {
	// A slow DB so that, if Steps() depended on the write, it would block.
	store := &trajectoryTaskStore{saveDelay: 200 * time.Millisecond}
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", store, discardLogger())

	want := trajSteps("a", "b")
	cs.Sync(want)

	// Steps() must return immediately from memory (no waiting on the 200ms write).
	assertStepsEqual(t, cs.Steps(), want)

	// Drain the background write before the test ends.
	waitForTrajectoryCount(t, store, 1, time.Second)
}

// TestCompositeTrajectoryStore_NonBlockingBestEffort verifies that a slow DB
// never stalls the ReAct loop: Sync returns near-instantly even when the DB
// write takes a long time, because at most one write is outstanding and the
// producer never blocks on the semaphore.
func TestCompositeTrajectoryStore_NonBlockingBestEffort(t *testing.T) {
	store := &trajectoryTaskStore{saveDelay: 300 * time.Millisecond}
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", store, discardLogger())

	steps := trajSteps("a")

	// 10 rapid Syncs. Each must return without waiting on the 300ms DB write.
	start := time.Now()
	for i := 0; i < 10; i++ {
		cs.Sync(steps)
	}
	elapsed := time.Since(start)

	// Threshold chosen well below one saveDelay (300ms) so a blocking Sync
	// would blow past it, but generous enough to avoid CI flakiness.
	if elapsed > 100*time.Millisecond {
		t.Errorf("Sync blocked on slow DB: elapsed=%v (expected near-instant)", elapsed)
	}

	// Best-effort: at least the first write should eventually land.
	waitForTrajectoryCount(t, store, 1, time.Second)
}

// TestCompositeTrajectoryStore_NilStoreDegradesToMemory verifies that without a
// DB store (or a task ID) the composite behaves exactly like the plain
// in-memory trajectoryHolder — no panic, no persistence.
func TestCompositeTrajectoryStore_NilStoreDegradesToMemory(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		mem := &trajectoryHolder{}
		cs := newCompositeTrajectoryStore(mem, "task-1", nil, discardLogger())

		want := trajSteps("a", "b")
		cs.Sync(want) // must not panic

		assertStepsEqual(t, cs.Steps(), want)
	})

	t.Run("empty task id", func(t *testing.T) {
		store := &trajectoryTaskStore{}
		mem := &trajectoryHolder{}
		cs := newCompositeTrajectoryStore(mem, "", store, discardLogger())

		want := trajSteps("a", "b")
		cs.Sync(want) // must not persist (no task ID)

		assertStepsEqual(t, cs.Steps(), want)
		// Give any (erroneous) background write a chance to surface, then assert none.
		time.Sleep(20 * time.Millisecond)
		if store.count() != 0 {
			t.Errorf("expected no DB writes for empty task ID, got %d", store.count())
		}
	})
}

// TestCompositeTrajectoryStore_SaveErrorIsSwallowed verifies that a failing DB
// write is best-effort: the error is logged but never propagated to the caller,
// and the in-memory trajectory remains usable.
func TestCompositeTrajectoryStore_SaveErrorIsSwallowed(t *testing.T) {
	store := &trajectoryTaskStore{saveErr: errors.New("db down")}
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", store, discardLogger())

	want := trajSteps("a", "b")
	cs.Sync(want) // must not return an error or panic

	// Reflect still works off the in-memory copy.
	assertStepsEqual(t, cs.Steps(), want)

	// Wait for the failed write to complete so the goroutine exits cleanly.
	waitForTrajectoryCount(t, store, 1, time.Second)
}

// TestCompositeTrajectoryStore_FlushPersistsFreshestSnapshot verifies that
// Flush recovers the data from a Sync that was dropped because an async write
// was already in flight — the exact gap the final flush closes. With a slow DB,
// the first Sync starts an in-flight write; the second Sync is dropped by the
// non-blocking semaphore. Flush then waits for the in-flight write and performs
// a synchronous write of the freshest (in-memory) snapshot, so the persisted
// trajectory ends with the second Sync's data, not the stale first one.
func TestCompositeTrajectoryStore_FlushPersistsFreshestSnapshot(t *testing.T) {
	store := &trajectoryTaskStore{saveDelay: 150 * time.Millisecond}
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", store, discardLogger())

	cs.Sync(trajSteps("a")) // acquires the slot; the slow write is now in flight

	// While the first write is still in flight, Sync a fresher snapshot. The
	// semaphore is full, so this Sync is dropped — no async write is queued.
	cs.Sync(trajSteps("a", "b"))

	// Flush must drain the in-flight write and then synchronously persist the
	// freshest snapshot ("a", "b") from memory.
	cs.Flush()

	// The persisted trajectory ends with the freshest snapshot, proving the
	// dropped Sync's data was recovered by the final flush.
	assertStepsEqual(t, store.lastSaved(), trajSteps("a", "b"))
	// Two writes landed: the async one ("a") and the flush ("a", "b").
	if got := store.count(); got < 2 {
		t.Fatalf("expected at least 2 persisted snapshots after flush; got %d", got)
	}
}

// TestCompositeTrajectoryStore_FlushNilStoreNoOp verifies that Flush is a safe
// no-op when there is no DB store or task ID (non-persistent sessions), and
// does not block waiting on a write that can never run.
func TestCompositeTrajectoryStore_FlushNilStoreNoOp(t *testing.T) {
	mem := &trajectoryHolder{}
	cs := newCompositeTrajectoryStore(mem, "task-1", nil, discardLogger())

	cs.Sync(trajSteps("a", "b"))
	cs.Flush() // must not panic or block
	assertStepsEqual(t, cs.Steps(), trajSteps("a", "b"))
}
