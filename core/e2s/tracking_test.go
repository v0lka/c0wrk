package e2s

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// timedRecord captures one TimedUsageObserver notification.
type timedRecord struct {
	usage    llm.TokenUsage
	duration time.Duration
	totalIn  int
	totalOut int
	model    string
	family   string
}

// turnResponse wraps a scripted e2s_step response with realistic usage so a
// TrackingCaller around the scripted caller has something to record.
func turnResponse(patch, tool, args string, in, out int) *llm.ChatResponse {
	resp := stepResponse(patch, tool, args)
	resp.Usage = llm.TokenUsage{InputTokens: in, OutputTokens: out}
	resp.Model = "test-model"
	resp.Family = "test-family"
	return resp
}

// TestRun_TrackingCallerFeedsTimedObserver pins the builder-level wiring
// contract for E2S: the orchestrator hands the E2S loop deps.llm — the session
// TrackingCaller chain (core/builder.go wraps llm.NewTrackingCaller's output
// in the logging/dump callers and assigns it to OrchestratorDeps.LLM) — so
// every E2S turn flows through the session UsageTracker and fires its timed
// observers with a positive duration. That observer is the seam the
// per-session output-token throughput window (session_tokens events'
// median_output_tok_s) subscribes to: E2S is COVERED by the throughput seam,
// not excluded.
func TestRun_TrackingCallerFeedsTimedObserver(t *testing.T) {
	scripted := &scriptedCaller{responses: []*llm.ChatResponse{
		turnResponse(`{}`, "probe", `{"q":1}`, 20, 40),
		turnResponse(`{}`, "probe", `{"q":2}`, 20, 40),
		turnResponse(`{}`, "finish", `{"answer":"done"}`, 15, 35),
	}}

	tracker := llm.NewUsageTracker()
	var mu sync.Mutex
	var records []timedRecord
	tracker.AddTimedObserver(func(usage llm.TokenUsage, duration time.Duration, totalIn, totalOut int, model, family string) {
		mu.Lock()
		records = append(records, timedRecord{usage, duration, totalIn, totalOut, model, family})
		mu.Unlock()
	})

	loop := New(llm.NewTrackingCaller(scripted, tracker), &mockRegistry{}, nil, testConfig())
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 3 {
		t.Fatalf("timed observer fired %d times, want 3 (one per E2S turn, including finish)", len(records))
	}
	for i, r := range records {
		if r.usage.OutputTokens == 0 {
			t.Errorf("record %d: per-call output tokens = 0", i)
		}
		if r.duration <= 0 {
			t.Errorf("record %d: duration = %v, want > 0", i, r.duration)
		}
		if r.model != "test-model" || r.family != "test-family" {
			t.Errorf("record %d: model/family = %q/%q", i, r.model, r.family)
		}
	}
	// Cumulative totals accumulate across turns — the window consumer reads
	// them alongside the per-call rate.
	last := records[len(records)-1]
	if last.totalIn != 55 || last.totalOut != 115 {
		t.Errorf("cumulative totals = %d/%d, want 55/115", last.totalIn, last.totalOut)
	}
}
