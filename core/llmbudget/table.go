// Package llmbudget implements ADR-071: adaptive per-model LLM request budgets
// learned from live traffic — one timed sample per successful call from the
// sp4rk timed-usage feed, never probed, never persisted (session-scoped,
// in-memory). The estimator prices clamp(est_in·r_in + out_reserve·r_out,
// floor, ceiling) with a degradation ladder for degenerate fits, and
// Transport arms the result as a context deadline, escalating ×2 on its own
// expiry. specs/decisions/071-adaptive-llm-request-budgets.md
package llmbudget

import (
	"sync"
	"time"
)

// Sample is one timed LLM call observation (the llm.TimedUsageObserver shape,
// reduced to what the estimator consumes).
type Sample struct {
	In       int           // input tokens reported by the provider
	Out      int           // output tokens reported by the provider
	Duration time.Duration // measured wall-clock duration of the call
}

// tokens is the sample's total token count — the size the duration is
// decomposed against. Zero-token samples carry no size information.
func (s Sample) tokens() int { return s.In + s.Out }

type modelRecord struct {
	samples [SampleWindow]Sample
	head    int     // next write index in the ring
	count   int     // samples retained (≤ SampleWindow)
	escal   float64 // escalation multiplier latched by Escalate; 1 = none
}

// push adds one sample to the ring, overwriting the oldest beyond the window.
func (r *modelRecord) push(s Sample) {
	r.samples[r.head] = s
	r.head = (r.head + 1) % SampleWindow
	if r.count < SampleWindow {
		r.count++
	}
}

// snapshot copies the retained samples in insertion order (oldest first).
func (r *modelRecord) snapshot() []Sample {
	out := make([]Sample, r.count)
	for i := range r.count {
		out[i] = r.samples[(r.head-r.count+i+SampleWindow)%SampleWindow]
	}
	return out
}

// BudgetTable accumulates per-model timed samples and resolves adaptive
// request budgets. Safe for concurrent use; the zero value is not usable —
// construct with NewBudgetTable.
type BudgetTable struct {
	mu     sync.Mutex
	models map[string]*modelRecord
}

// NewBudgetTable returns an empty table. Keys are free-form: wire-extracted
// model names and provider-level fallback keys share one map.
func NewBudgetTable() *BudgetTable {
	return &BudgetTable{models: make(map[string]*modelRecord)}
}

// Ingest records one timed sample (ADR-071 D1), ignoring non-positive
// durations and empty models; zero-output samples are KEPT (prefill evidence).
// A successful ingest clears the escalation latch: the request completed.
func (t *BudgetTable) Ingest(model string, in, out int, duration time.Duration) {
	if model == "" || duration <= 0 {
		return
	}
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.record(model)
	r.push(Sample{In: in, Out: out, Duration: duration})
	r.escal = 1
}

// Escalate latches a ×2 escalation for the model's next trained budget
// (ADR-071 D9); expiries compound to MaxEscalationFactor, and the latch
// clears on the model's next successful ingest.
func (t *BudgetTable) Escalate(model string) {
	if model == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.record(model)
	escal := r.escal * 2
	if escal > MaxEscalationFactor {
		escal = MaxEscalationFactor
	}
	r.escal = escal
}

// SampleCount reports how many samples are currently retained for the key.
func (t *BudgetTable) SampleCount(key string) int {
	count, _ := t.state(key)
	return count
}

// Samples returns a copy of the retained samples for the key, oldest first —
// the read-only diagnostics view of the estimator's snapshot.
func (t *BudgetTable) Samples(key string) []Sample {
	return t.snapshot(key)
}

// state returns the retained sample count and the escalation multiplier for
// the key under one lock acquisition.
func (t *BudgetTable) state(key string) (count int, escal float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.models[key]
	if !ok {
		return 0, 1
	}
	return r.count, r.escal
}

// snapshot returns the retained samples for the key, oldest first. The result
// is a fresh slice; callers may use it without holding the lock.
func (t *BudgetTable) snapshot(key string) []Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.models[key]
	if !ok {
		return nil
	}
	return r.snapshot()
}

// record returns the (created-on-demand) record for the key. The caller must
// hold t.mu.
func (t *BudgetTable) record(key string) *modelRecord {
	r, ok := t.models[key]
	if !ok {
		r = &modelRecord{escal: 1}
		t.models[key] = r
	}
	return r
}
