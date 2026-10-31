package core

import (
	"slices"
	"sync"
	"time"
)

const (
	throughputWindowCap  = 64 // sliding-window size over the most recent per-call output rates
	throughputMinSamples = 3  // below this the median is noise, not signal (reported as 0)
)

// sessionThroughputWindow maintains a per-session sliding window of per-call
// output-token rates and reports their median; fed by the UsageTracker's timed
// observer (specs/domains/llm-providers.md, token accounting & throughput
// seam). Observers fan out from arbitrary goroutines, hence the mutex.
type sessionThroughputWindow struct {
	mu      sync.Mutex
	samples []float64 // ring buffer of rates, len == cap
	head    int       // next write position
	count   int       // number of valid samples (<= cap)
}

func newSessionThroughputWindow() *sessionThroughputWindow {
	return &sessionThroughputWindow{samples: make([]float64, throughputWindowCap)}
}

// record adds one call's output rate and returns the resulting (median,
// samples); calls with non-positive tokens or duration return the current
// view untouched.
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

// median returns the median output rate over the window and the sample count;
// below throughputMinSamples the median is 0 (unavailable) while the count
// still reflects reality, so consumers can tell "warming up" from "no data".
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
