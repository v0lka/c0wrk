package desktop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/sp4rk/agent"
)

// fakeStepLimitResolver stands in for *App in adapter tests: it reports
// whether a boundary was resolved autonomously (silent + step_limit=auto) and
// records the boundary it was consulted about.
type fakeStepLimitResolver struct {
	resp    agent.StepLimitResponse
	reason  string
	handled bool

	mu       sync.Mutex
	calls    int
	lastSeen stepLimitCall
}

type stepLimitCall struct {
	sessionID   string
	currentStep int
	maxSteps    int
	abortReason string
}

func (f *fakeStepLimitResolver) ResolveSilentStepLimit(_ context.Context, sessionID string, currentStep, maxSteps int, abortReason string) (agent.StepLimitResponse, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSeen = stepLimitCall{sessionID: sessionID, currentStep: currentStep, maxSteps: maxSteps, abortReason: abortReason}
	return f.resp, f.reason, f.handled
}

// recordEvents returns an emit sink that captures every emitted event.
func recordEvents() (emit func(session.Event), snapshot func() []session.Event) {
	var mu sync.Mutex
	var events []session.Event
	emit = func(e session.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	snapshot = func() []session.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]session.Event, len(events))
		copy(out, events)
		return out
	}
	return emit, snapshot
}

func TestStepLimitAdapter_SilentAutoReturnsJudgeDecisionWithoutPrompt(t *testing.T) {
	emit, events := recordEvents()
	resolver := &fakeStepLimitResolver{resp: agent.StepLimitAllowOnce, reason: "one step left", handled: true}
	adapter := &stepLimitHITLAdapter{
		ctx:              context.Background(),
		pendingStepLimit: &sync.Map{},
		uiEmit:           emit,
		resolver:         resolver,
	}

	ctx := session.ContextWithSessionID(context.Background(), "sess-silent")
	resp, err := adapter.OnStepLimit(ctx, 9, 8, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != agent.StepLimitAllowOnce {
		t.Errorf("resp = %v, want StepLimitAllowOnce", resp)
	}
	if got := events(); len(got) != 0 {
		t.Errorf("silent path must NOT emit a step_limit prompt, got %d events", len(got))
	}
	if resolver.calls != 1 {
		t.Errorf("resolver calls = %d, want 1", resolver.calls)
	}
	if resolver.lastSeen.sessionID != "sess-silent" || resolver.lastSeen.currentStep != 9 || resolver.lastSeen.maxSteps != 8 {
		t.Errorf("resolver saw %+v, want the boundary args", resolver.lastSeen)
	}
}

func TestStepLimitAdapter_SilentDenyStopsRun(t *testing.T) {
	emit, events := recordEvents()
	adapter := &stepLimitHITLAdapter{
		ctx:              context.Background(),
		pendingStepLimit: &sync.Map{},
		uiEmit:           emit,
		resolver:         &fakeStepLimitResolver{resp: agent.StepLimitDeny, handled: true},
	}
	ctx := session.ContextWithSessionID(context.Background(), "sess-silent")
	resp, err := adapter.OnStepLimit(ctx, 4, 4, "repeated identical tool calls")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != agent.StepLimitDeny {
		t.Errorf("resp = %v, want StepLimitDeny", resp)
	}
	if got := events(); len(got) != 0 {
		t.Errorf("silent path must NOT emit a step_limit prompt, got %d events", len(got))
	}
}

func TestStepLimitAdapter_FallsBackToPromptWhenNotHandled(t *testing.T) {
	emit, events := recordEvents()
	pending := &sync.Map{}
	adapter := &stepLimitHITLAdapter{
		ctx:              context.Background(),
		pendingStepLimit: pending,
		uiEmit:           emit,
		resolver:         &fakeStepLimitResolver{handled: false},
	}

	ctx := session.ContextWithSessionID(context.Background(), "sess-fallback")
	done := make(chan agent.StepLimitResponse, 1)
	go func() {
		resp, _ := adapter.OnStepLimit(ctx, 10, 10, "output truncated")
		done <- resp
	}()

	// Wait for the interactive step_limit prompt to be emitted.
	var requestID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && requestID == "" {
		for _, e := range events() {
			if e.Type != "step_limit" {
				continue
			}
			if p, ok := e.Data.(session.StepLimitPayload); ok {
				requestID = p.RequestID
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if requestID == "" {
		t.Fatal("non-handled boundary must emit a step_limit prompt (interactive fallback)")
	}

	val, ok := pending.Load(requestID)
	if !ok {
		t.Fatalf("no pending step-limit entry for request %q", requestID)
	}
	entry, ok := val.(*pendingStepLimitEntry)
	if !ok {
		t.Fatalf("pending entry has wrong type: %T", val)
	}
	entry.ch <- agent.StepLimitAllowAlways

	select {
	case resp := <-done:
		if resp != agent.StepLimitAllowAlways {
			t.Errorf("resp = %v, want the user's StepLimitAllowAlways", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnStepLimit did not return after the user resolved the prompt")
	}
}

func TestStepLimitAdapter_NilResolverFallsBack(t *testing.T) {
	// A nil resolver (e.g. a bare adapter) must follow the interactive path:
	// with no session in context that is an immediate deny, matching the
	// pre-silent-mode behavior.
	adapter := &stepLimitHITLAdapter{
		ctx:              context.Background(),
		pendingStepLimit: &sync.Map{},
		uiEmit:           func(session.Event) {},
	}
	resp, err := adapter.OnStepLimit(context.Background(), 3, 3, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != agent.StepLimitDeny {
		t.Errorf("resp = %v, want StepLimitDeny (no session context)", resp)
	}
}
