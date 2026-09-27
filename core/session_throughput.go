package core

import (
	"slices"
	"sync"
	"time"
)

const (
	// throughputWindowCap is the maximum number of per-call output-rate
	// samples retained per session (a sliding window over the most recent
	// LLM calls).
	throughputWindowCap = 64
	// throughputMinSamples is the sample count below which no median is
	// reported. A median over one or two calls is noise, not signal.
	throughputMinSamples = 3
)

// sessionThroughputWindow maintains a per-session sliding window of per-call
// output-token rates (output tokens / wall-clock seconds) and reports their
// median. It is fed by the UsageTracker's timed observer, so one sample is
// recorded per successful timed LLM call.
//
// Calls with zero output tokens or a non-positive duration are skipped: they
// carry no meaningful rate (empty completions, degenerate clocks) and would
// drag the median toward zero.
//
// Thread-safe: the session UsageTracker fans out to observers from arbitrary
// goroutines (conductor steps, delegated subagents share one TrackingCaller).
type sessionThroughputWindow struct {
	mu      sync.Mutex
	samples []float64 // ring buffer of rates, len == cap
	head    int       // next write position
	count   int       // number of valid samples (<= cap)
}

// newSessionThroughputWindow returns an empty window with the default
// capacity and minimum-sample threshold.
func newSessionThroughputWindow() *sessionThroughputWindow {
	return &sessionThroughputWindow{samples: make([]float64, throughputWindowCap)}
}

// record adds one call's output rate to the window and returns the resulting
// (median, sampleCount). Calls with outputTokens <= 0 or duration <= 0 are
// skipped — the window is left untouched and its current view is returned.
func (w *sessionThroughputWindow) record(outputTokens int, duration time.Duration) (median float64, samples int) {
	if outputTokens <= 0 || duration <= 0 {
		return w.median()
	}
	rate := float64(outputTokens) / duration.Seconds()

	w.mu.Lock()
	w.samples[w.head] = rate
	w.head = (w.head + 1) % throughputWindowCap
	if w.count < throughputWindowCap {
		w.count++
	}
	w.mu.Unlock()

	return w.median()
}

// median returns the median output rate over the window and the number of
// samples currently in it. When fewer than throughputMinSamples samples are
// recorded, the median is reported as 0 (unavailable) while the sample count
// still reflects reality, so consumers can distinguish "warming up" from
// "no data".
func (w *sessionThroughputWindow) median() (median float64, samples int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := w.count
	if n < throughputMinSamples {
		return 0, n
	}

	sorted := make([]float64, n)
	for i := 0; i < n; i++ {
		sorted[i] = w.samples[(w.head-n+i+throughputWindowCap)%throughputWindowCap]
	}
	slices.Sort(sorted)

	if n%2 == 1 {
		return sorted[n/2], n
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2, n
}
