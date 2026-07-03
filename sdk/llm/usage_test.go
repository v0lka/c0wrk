package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// --- Test helpers ---

// mockCaller implements Caller for testing.
type mockCaller struct {
	resp *ChatResponse
	err  error
}

func (m *mockCaller) Call(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return m.resp, m.err
}

// --- UsageTracker tests ---

func TestUsageTracker_Record(t *testing.T) {
	tr := NewUsageTracker()
	tr.Record(TokenUsage{InputTokens: 100, OutputTokens: 50}, "gpt-4o", "openai")
	tr.Record(TokenUsage{InputTokens: 200, OutputTokens: 80}, "gpt-4o", "openai")

	in, out := tr.Totals()
	if in != 300 {
		t.Errorf("totalIn = %d, want 300", in)
	}
	if out != 130 {
		t.Errorf("totalOut = %d, want 130", out)
	}
}

func TestUsageTracker_Observer(t *testing.T) {
	tr := NewUsageTracker()

	var calls []struct {
		usage    TokenUsage
		totalIn  int
		totalOut int
		model    string
		family   string
	}

	tr.AddObserver(func(usage TokenUsage, totalIn, totalOut int, model, family string) {
		calls = append(calls, struct {
			usage    TokenUsage
			totalIn  int
			totalOut int
			model    string
			family   string
		}{usage, totalIn, totalOut, model, family})
	})

	tr.Record(TokenUsage{InputTokens: 10, OutputTokens: 5}, "m1", "f1")
	tr.Record(TokenUsage{InputTokens: 20, OutputTokens: 15}, "m2", "f2")

	if len(calls) != 2 {
		t.Fatalf("observer called %d times, want 2", len(calls))
	}

	// First call: per-call usage and cumulative
	if calls[0].usage.InputTokens != 10 || calls[0].totalIn != 10 {
		t.Errorf("first call: usage.InputTokens=%d totalIn=%d", calls[0].usage.InputTokens, calls[0].totalIn)
	}

	// Second call: cumulative should include first
	if calls[1].totalIn != 30 || calls[1].totalOut != 20 {
		t.Errorf("second call: totalIn=%d totalOut=%d, want 30/20", calls[1].totalIn, calls[1].totalOut)
	}
	if calls[1].model != "m2" || calls[1].family != "f2" {
		t.Errorf("second call: model=%q family=%q", calls[1].model, calls[1].family)
	}
}

func TestUsageTracker_Totals(t *testing.T) {
	tr := NewUsageTracker()

	in, out := tr.Totals()
	if in != 0 || out != 0 {
		t.Errorf("initial totals: %d/%d, want 0/0", in, out)
	}

	tr.Record(TokenUsage{InputTokens: 42, OutputTokens: 7}, "x", "y")
	in, out = tr.Totals()
	if in != 42 || out != 7 {
		t.Errorf("after record: %d/%d, want 42/7", in, out)
	}
}

func TestUsageTracker_ConcurrentRecord(t *testing.T) {
	tr := NewUsageTracker()
	const goroutines = 50
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range perGoroutine {
				tr.Record(TokenUsage{InputTokens: 1, OutputTokens: 1}, "m", "f")
			}
		}(i)
	}
	wg.Wait()

	in, out := tr.Totals()
	want := goroutines * perGoroutine
	if in != want || out != want {
		t.Errorf("concurrent totals: %d/%d, want %d/%d", in, out, want, want)
	}
}

// --- TrackingCaller tests ---

func TestTrackingCaller_Call(t *testing.T) {
	inner := &mockCaller{
		resp: &ChatResponse{
			Model:  "gpt-4o",
			Family: "openai",
			Usage:  TokenUsage{InputTokens: 100, OutputTokens: 50},
		},
	}
	tracker := NewUsageTracker()
	tc := NewTrackingCaller(inner, tracker)

	resp, err := tc.Call(context.Background(), ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("resp.Usage.InputTokens = %d, want 100", resp.Usage.InputTokens)
	}

	in, out := tracker.Totals()
	if in != 100 || out != 50 {
		t.Errorf("tracker totals: %d/%d, want 100/50", in, out)
	}
}

func TestTrackingCaller_CallCorrectContextTracker(t *testing.T) {
	inner := &mockCaller{
		resp: &ChatResponse{
			Model:  "gpt-4o",
			Family: "openai",
			Usage:  TokenUsage{InputTokens: 200, OutputTokens: 30},
		},
	}
	tracker := NewUsageTracker()
	tc := NewTrackingCaller(inner, tracker)

	ctxTracker := NewContextTokenTracker(NewSimpleTokenCounter())
	ctxTracker.AddDelta("some pending text that should be replaced")
	tc = tc.WithContextTracker(ctxTracker)

	_, err := tc.Call(context.Background(), ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After correction, estimate should be exactly the API input tokens (pendingDelta reset)
	est := ctxTracker.EstimateTotal()
	if est != 200 {
		t.Errorf("context tracker estimate = %d, want 200", est)
	}
}

func TestTrackingCaller_CallNoError(t *testing.T) {
	inner := &mockCaller{
		err: errors.New("llm unavailable"),
	}
	tracker := NewUsageTracker()
	tc := NewTrackingCaller(inner, tracker)

	_, err := tc.Call(context.Background(), ChatRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	in, out := tracker.Totals()
	if in != 0 || out != 0 {
		t.Errorf("tracker should be 0/0 on error, got %d/%d", in, out)
	}
}

func TestTrackingCaller_WithContextTracker(t *testing.T) {
	inner := &mockCaller{
		resp: &ChatResponse{
			Model:  "gpt-4o",
			Family: "openai",
			Usage:  TokenUsage{InputTokens: 100, OutputTokens: 50},
		},
	}
	sessionTracker := NewUsageTracker()
	base := NewTrackingCaller(inner, sessionTracker)

	// Create two step-local callers with independent context trackers
	ctx1 := NewContextTokenTracker(NewSimpleTokenCounter())
	ctx1.AddDelta("step1 pending")
	caller1 := base.WithContextTracker(ctx1)

	ctx2 := NewContextTokenTracker(NewSimpleTokenCounter())
	ctx2.AddDelta("step2 pending")
	caller2 := base.WithContextTracker(ctx2)

	// Call through caller1 — should only correct ctx1
	_, err := caller1.Call(context.Background(), ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("caller1 error: %v", err)
	}

	// ctx1 should be corrected to 100 (the API response input tokens)
	if est := ctx1.EstimateTotal(); est != 100 {
		t.Errorf("ctx1 estimate = %d, want 100", est)
	}
	// ctx2 should be unchanged (still has its pending delta)
	if est := ctx2.EstimateTotal(); est == 100 {
		t.Errorf("ctx2 should NOT be corrected by caller1, got %d", est)
	}

	// Call through caller2 — should only correct ctx2
	_, err = caller2.Call(context.Background(), ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("caller2 error: %v", err)
	}

	if est := ctx2.EstimateTotal(); est != 100 {
		t.Errorf("ctx2 estimate = %d, want 100", est)
	}

	// Both calls should have been recorded in the shared session tracker
	in, out := sessionTracker.Totals()
	if in != 200 || out != 100 {
		t.Errorf("session totals = %d/%d, want 200/100", in, out)
	}
}
