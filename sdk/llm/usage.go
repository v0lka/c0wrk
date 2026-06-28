package llm

import (
	"context"
	"sync"
)

// UsageObserver is called after each LLM call with per-call and cumulative usage.
type UsageObserver func(usage TokenUsage, totalIn, totalOut int, model, family string)

// UsageTracker accumulates token usage across all LLM calls in a session.
// Thread-safe. Upper layers register observers to react to usage changes.
type UsageTracker struct {
	mu        sync.Mutex
	totalIn   int
	totalOut  int
	observers []UsageObserver
}

// NewUsageTracker creates a new UsageTracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{}
}

// Record adds per-call usage to the running totals and notifies all observers.
func (t *UsageTracker) Record(usage TokenUsage, model, family string) {
	t.mu.Lock()
	t.totalIn += usage.InputTokens
	t.totalOut += usage.OutputTokens
	totalIn := t.totalIn
	totalOut := t.totalOut
	// Snapshot observers under lock to avoid races on the slice.
	observers := make([]UsageObserver, len(t.observers))
	copy(observers, t.observers)
	t.mu.Unlock()

	for _, fn := range observers {
		fn(usage, totalIn, totalOut, model, family)
	}
}

// AddObserver registers a callback that is invoked on every Record call.
func (t *UsageTracker) AddObserver(fn UsageObserver) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observers = append(t.observers, fn)
}

// Totals returns the current accumulated input and output token counts.
func (t *UsageTracker) Totals() (inputTokens, outputTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totalIn, t.totalOut
}

// TrackingCaller wraps a Caller and automatically:
//   - Records usage from Call() responses into UsageTracker
//   - Corrects the active ContextTokenTracker (if set) after each call
type TrackingCaller struct {
	inner      Caller
	session    *UsageTracker
	ctxMu      sync.RWMutex
	ctxTracker *ContextTokenTracker
}

// NewTrackingCaller creates a TrackingCaller that records usage into tracker.
func NewTrackingCaller(inner Caller, tracker *UsageTracker) *TrackingCaller {
	return &TrackingCaller{
		inner:   inner,
		session: tracker,
	}
}

// Call delegates to the inner caller, then records usage and corrects the context tracker.
func (tc *TrackingCaller) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	resp, err := tc.inner.Call(ctx, req)
	if err != nil {
		return nil, err
	}

	tc.session.Record(resp.Usage, resp.Model, resp.Family)

	tc.ctxMu.RLock()
	ct := tc.ctxTracker
	tc.ctxMu.RUnlock()

	if ct != nil && resp.Usage.InputTokens > 0 {
		ct.Correct(resp.Usage.InputTokens)
	}

	return resp, nil
}

// WithContextTracker returns a new TrackingCaller that shares the same inner
// caller and session-level UsageTracker, but corrects the given per-step
// ContextTokenTracker. Use this to create step-local callers for parallel execution.
func (tc *TrackingCaller) WithContextTracker(t *ContextTokenTracker) *TrackingCaller {
	return &TrackingCaller{
		inner:      tc.inner,
		session:    tc.session,
		ctxTracker: t,
	}
}
