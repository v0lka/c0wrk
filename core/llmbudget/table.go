// Package llmbudget implements ADR-071: adaptive per-model LLM request
// budgets learned from live traffic.
//
// The sp4rk UsageTracker feed (TimedUsageObserver) delivers one timed sample
// per successful LLM call — input/output tokens and the wall-clock duration.
// BudgetTable retains the most recent SampleWindow samples per model; the
// estimator prices a request's deadline as
//
//	budget = clamp(est_in·r_in + out_reserve·r_out, floor(class), ceiling(class))
//
// with the two rates fitted per model (duration ≈ in·r_in + out·r_out, priced
// at the p85 tail) and degrading through a size-normalized duration percentile
// down to the class constants when the fit is degenerate (ADR-071 D6/D7).
// ResolveDeadline layers the operator-owned opinions on top (per-model
// request_timeout, global llmRequestTimeout, warmup, kill-switch; ADR-071
// D4/D5/D11), and Transport applies the resolved budget as a context deadline
// on the provider's HTTP wire, escalating ×2 on its own expiry (ADR-071 D9).
//
// Everything here is session-scoped and in-memory (ADR-071 D10); nothing is
// persisted. The package never probes: its only input is traffic the user was
// generating anyway (ADR-071 D1).
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

// NewBudgetTable returns an empty table. Keys are free-form strings: the
// transport keys budgets by the wire-extracted model name and falls back to a
// provider-level key when extraction fails, so both live in the same map.
func NewBudgetTable() *BudgetTable {
	return &BudgetTable{models: make(map[string]*modelRecord)}
}

// Ingest records one timed sample (ADR-071 D1). Samples with a non-positive
// duration or an empty model are ignored — there is nothing to learn from a
// call whose duration was not measured. Zero-output samples are KEPT: they are
// prefill evidence and feed the input-rate fit. A successful ingest clears the
// model's escalation latch: the request completed, so the next budget is
// computed from evidence again rather than from the previous expiry.
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
// (ADR-071 D9), called when the transport detects that a request died of the
// budget it armed itself. Repeated expiries compound (×2, ×4, …) so the ladder
// walks up to the class ceiling; MaxEscalationFactor keeps the multiplication
// total. The latch clears on the model's next successful ingest.
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
