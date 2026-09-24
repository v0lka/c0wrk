package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// This file pins the readiness gate on the manager's own one-shot service LLM
// request (session title generation).
//
// The gate exists because of an ordering fact, not a preference: the manager
// arms serviceLLMTimeout BEFORE issuing the request, so a model that has to load
// gigabytes of weights first would spend that budget on the load and the title
// would be lost. Gating first is what makes "no LLM request starts until the
// model is ready" hold for this path too — and a gate failure must skip the
// request rather than issue it against something that cannot answer.

// testServiceLLMBudget is shorter than the gate delay used below, so a budget
// armed before the gate is provably spent by the time the request starts.
const testServiceLLMBudget = 200 * time.Millisecond

// testGateDelay stands in for a cold weight load: longer than the budget.
const testGateDelay = 500 * time.Millisecond

// orderLog records the order two concurrent-ish callbacks ran in.
type orderLog struct {
	mu   sync.Mutex
	seen []string
}

func (o *orderLog) record(what string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, what)
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen...)
}

// recordingTitleCaller is an LLMTitleCaller double: it reports when it ran and
// what budget it was handed.
type recordingTitleCaller struct {
	mu       sync.Mutex
	calls    int
	deadline time.Time
	hasDDL   bool
	live     bool
	done     chan struct{}
	// onCall, when set, records the call in a shared ordering log so a test can
	// compare it with the gate's.
	onCall func()
}

func newRecordingTitleCaller() *recordingTitleCaller {
	return &recordingTitleCaller{done: make(chan struct{})}
}

func (c *recordingTitleCaller) GenerateTitle(ctx context.Context, _ string, _ []string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.live = ctx.Err() == nil
	c.deadline, c.hasDDL = ctx.Deadline()
	first := c.calls == 1
	c.mu.Unlock()
	if c.onCall != nil {
		c.onCall()
	}
	if first {
		close(c.done)
	}
	return "a generated title", nil
}

func (c *recordingTitleCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// gateThenTitle drives one maybeSpawnTitleGeneration with a gate that records
// the order it ran in relative to the LLM call, and returns both observations.
//
// The service budget is deliberately set SHORTER than the gate a caller passes
// in. That is what makes the ordering observable: arm the budget first and the
// title request starts already expired, gate first and it starts with its whole
// budget. Comparing the two order log entries alone would NOT catch a
// gate-after-budget regression — both orders still run the gate before the call.
func gateThenTitle(t *testing.T, gate func(context.Context) error) (*recordingTitleCaller, *orderLog, *Manager) {
	t.Helper()
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "done"}), func(Event) {}, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetServiceLLMTimeout(testServiceLLMBudget)

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	order := &orderLog{}
	caller := newRecordingTitleCaller()
	caller.onCall = func() { order.record("title request") }
	mgr.SetTitleGenerator(NewTitleGenerator(caller))
	if gate != nil {
		wrapped := gate
		mgr.SetServiceLLMGate(func(ctx context.Context) error {
			order.record("gate")
			return wrapped(ctx)
		})
	}

	mgr.mu.RLock()
	session := mgr.sessions[info.ID]
	mgr.mu.RUnlock()
	if session == nil {
		t.Fatalf("session %s is not in the manager", info.ID)
	}

	mgr.maybeSpawnTitleGeneration(session, info.ID, "hello world", false, nil)
	return caller, order, mgr
}

// TestServiceLLMGateRunsBeforeTheTitleRequest is the ordering property: the gate
// completes before the LLM call is issued, and the call's own budget is armed
// only afterwards — so a slow load cannot spend it.
func TestServiceLLMGateRunsBeforeTheTitleRequest(t *testing.T) {
	caller, order, _ := gateThenTitle(t, func(context.Context) error {
		time.Sleep(testGateDelay)
		return nil
	})

	select {
	case <-caller.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the title request never ran")
	}

	if snapshot := order.snapshot(); len(snapshot) != 2 || snapshot[0] != "gate" || snapshot[1] != "title request" {
		t.Errorf("order = %v, want [gate, title request]", snapshot)
	}

	caller.mu.Lock()
	live, hasDDL, deadline := caller.live, caller.hasDDL, caller.deadline
	calls := caller.calls
	caller.mu.Unlock()

	if calls != 1 {
		t.Errorf("title calls = %d, want 1", calls)
	}
	if !hasDDL {
		t.Fatal("the title request carries no deadline — serviceLLMTimeout was not applied")
	}
	// The gate slept longer than the budget, so this is the discriminating
	// assertion: a budget armed AFTER the gate still has (almost) all of it
	// left, while one armed BEFORE it expired during the wait.
	if remaining := time.Until(deadline); remaining < testServiceLLMBudget/2 {
		t.Errorf("remaining budget = %v, want ≈%v — the %v gate was charged to it",
			remaining, testServiceLLMBudget, testGateDelay)
	}
	if !live {
		t.Error("the title request started with an already-expired context: the gate " +
			"ran after the budget was armed and the load was charged to it")
	}
}

// TestServiceLLMGateFailureSkipsTheTitleRequest is the fail-closed half: a model
// that cannot be loaded must not be sent a request. The session keeps its
// generated name, which is the graceful outcome — the run's own request is what
// reports the cause to the user.
func TestServiceLLMGateFailureSkipsTheTitleRequest(t *testing.T) {
	gateErr := errors.New("the embedded model could not be loaded")
	caller, order, mgr := gateThenTitle(t, func(context.Context) error { return gateErr })

	// The gate failure returns from the goroutine without ever reaching the
	// caller, so there is no signal to wait for; give the goroutine room to run
	// and then assert nothing was issued. Shutdown joins it deterministically.
	select {
	case <-caller.done:
		t.Fatal("the title request ran although the readiness gate failed")
	case <-time.After(300 * time.Millisecond):
	}
	mgr.Shutdown()

	if got := caller.callCount(); got != 0 {
		t.Errorf("title calls = %d, want 0 after a failed gate", got)
	}
	if snapshot := order.snapshot(); len(snapshot) != 1 || snapshot[0] != "gate" {
		t.Errorf("order = %v, want only [gate]", snapshot)
	}
}

// TestNoServiceLLMGateIsTheNormalPosture keeps the gate optional: with none
// installed — every provider that is always listening — the title request runs
// exactly as before.
func TestNoServiceLLMGateIsTheNormalPosture(t *testing.T) {
	caller, _, _ := gateThenTitle(t, nil)

	select {
	case <-caller.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the title request never ran without a gate")
	}
	if got := caller.callCount(); got != 1 {
		t.Errorf("title calls = %d, want 1", got)
	}
}

// TestTitleGenerationIsSkippedForAnAlreadyNamedSession guards the extraction's
// own precondition: only a session still carrying its placeholder name is
// renamed, so a gate must not be able to fire (or block) on later messages.
func TestTitleGenerationIsSkippedForAnAlreadyNamedSession(t *testing.T) {
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "done"}), func(Event) {}, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.CreateSession(testProjectID, runtimeTempDir(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	caller := newRecordingTitleCaller()
	mgr.SetTitleGenerator(NewTitleGenerator(caller))
	mgr.SetServiceLLMGate(func(context.Context) error {
		t.Error("the gate ran for a session that is not eligible for a title")
		return nil
	})

	mgr.mu.RLock()
	session := mgr.sessions[info.ID]
	mgr.mu.RUnlock()
	session.mu.Lock()
	session.Name = "already renamed"
	session.mu.Unlock()

	mgr.maybeSpawnTitleGeneration(session, info.ID, "hello world", false, nil)

	select {
	case <-caller.done:
		t.Error("an already-named session was renamed again")
	case <-time.After(200 * time.Millisecond):
	}
	if got := caller.callCount(); got != 0 {
		t.Errorf("title calls = %d, want 0", got)
	}
}
