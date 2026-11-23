package core

import (
	"slices"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

const (
	throughputWindowCap  = 64 // sliding-window size over the most recent per-call output rates / durations
	throughputMinSamples = 3  // below this the median is noise, not signal (reported as 0)
)

// SessionTokenCallStats carries the per-observation aggregates one tracked
// LLM call adds to the session's cache-efficiency and latency observability,
// emitted on the session_tokens stream (specs/contracts/event-catalog.md) and
// mirrored into agent_metrics. Windowed medians cover the most recent
// throughputWindowCap calls and report 0 below throughputMinSamples (warming
// up); totals and percents are session-cumulative across every timed call.
type SessionTokenCallStats struct {
	// MedianOutputTokPerSec is the median per-call output-token rate over the
	// sliding window; 0 below the minimum sample count.
	MedianOutputTokPerSec float64
	// TokSSamples is the sample count behind MedianOutputTokPerSec.
	TokSSamples int
	// MedianCallMillis is the median per-call wall-clock duration in
	// milliseconds over the sliding window; 0 below the minimum sample count.
	MedianCallMillis float64
	// MeanCallMillis is the session-cumulative mean per-call wall-clock
	// duration in milliseconds; 0 when no measurable call has been observed.
	MeanCallMillis float64
	// MaxCallMillis is the session-cumulative maximum per-call wall-clock
	// duration in milliseconds.
	MaxCallMillis float64
	// CallCount counts the timed LLM calls observed this session.
	CallCount int
	// CachedInputTokensTotal accumulates the prompt tokens providers served
	// from their prompt caches (llm.TokenUsage.CachedInputTokens — a detail
	// already included in each call's InputTokens).
	CachedInputTokensTotal int
	// CacheWriteTokensTotal accumulates the prompt tokens providers wrote
	// into their prompt caches (llm.TokenUsage.CacheWriteTokens — likewise a
	// detail of InputTokens; only Anthropic-family wire formats report it).
	CacheWriteTokensTotal int
	// CacheHitPercent is the session cache-hit rate: cumulative cached input
	// tokens over cumulative observed per-call input tokens, times 100.
	// 0 when no input usage has been observed yet.
	CacheHitPercent float64
}

// sessionThroughputWindow maintains a per-session sliding window of per-call
// output-token rates and call durations, plus session-cumulative latency and
// prompt-cache counters; fed by the UsageTracker's timed observer
// (specs/domains/llm-providers.md, token accounting & throughput seam).
// Observers fan out from arbitrary goroutines, hence the mutex.
type sessionThroughputWindow struct {
	mu    sync.Mutex
	head  int       // next write position in the rate ring
	count int       // valid rate samples (<= cap)
	rates []float64 // ring buffer of per-call output rates, len == cap

	dHead     int       // next write position in the duration ring
	dCount    int       // valid duration samples (<= cap)
	durations []float64 // ring buffer of per-call durations (ms), len == cap

	callCount     int     // cumulative timed calls observed
	sumCallMillis float64 // cumulative sum of observed call durations (ms)
	maxCallMillis float64 // cumulative max observed call duration (ms)

	cachedInputTotal   int64 // cumulative cache-hit input tokens
	cacheWriteTotal    int64 // cumulative cache-write input tokens
	observedInputTotal int64 // cumulative per-call input tokens over observed calls
}

func newSessionThroughputWindow() *sessionThroughputWindow {
	return &sessionThroughputWindow{
		rates:     make([]float64, throughputWindowCap),
		durations: make([]float64, throughputWindowCap),
	}
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
	w.rates[w.head] = rate
	w.head = (w.head + 1) % throughputWindowCap
	if w.count < throughputWindowCap {
		w.count++
	}
	w.mu.Unlock()

	return w.median()
}

// observe folds one timed call's usage and duration into every aggregate and
// returns the resulting stats view. Rate samples require positive output
// tokens and duration (the throughput window's existing skip rules); the
// latency and cache counters accumulate whenever the timed observer fires,
// clamping negative values to zero so a misbehaving provider cannot skew the
// totals.
func (w *sessionThroughputWindow) observe(usage llm.TokenUsage, duration time.Duration) SessionTokenCallStats {
	medianRate, samples := w.record(usage.OutputTokens, duration)

	w.mu.Lock()
	if duration > 0 {
		millis := float64(duration.Microseconds()) / 1000.0
		w.durations[w.dHead] = millis
		w.dHead = (w.dHead + 1) % throughputWindowCap
		if w.dCount < throughputWindowCap {
			w.dCount++
		}
		w.callCount++
		w.sumCallMillis += millis
		if millis > w.maxCallMillis {
			w.maxCallMillis = millis
		}
	}
	if cached := usage.CachedInputTokens; cached > 0 {
		w.cachedInputTotal += int64(cached)
	}
	if written := usage.CacheWriteTokens; written > 0 {
		w.cacheWriteTotal += int64(written)
	}
	if inputs := usage.InputTokens; inputs > 0 {
		w.observedInputTotal += int64(inputs)
	}

	stats := SessionTokenCallStats{
		MedianOutputTokPerSec:  medianRate,
		TokSSamples:            samples,
		CallCount:              w.callCount,
		CachedInputTokensTotal: int(w.cachedInputTotal),
		CacheWriteTokensTotal:  int(w.cacheWriteTotal),
	}
	if w.dCount >= throughputMinSamples {
		stats.MedianCallMillis = medianOfWindow(w.durations, w.dHead, w.dCount)
	}
	if w.callCount > 0 {
		stats.MeanCallMillis = w.sumCallMillis / float64(w.callCount)
		stats.MaxCallMillis = w.maxCallMillis
	}
	if w.observedInputTotal > 0 {
		stats.CacheHitPercent = float64(w.cachedInputTotal) / float64(w.observedInputTotal) * 100
	}
	w.mu.Unlock()

	return stats
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
	return medianOfWindow(w.rates, w.head, n), n
}

// medianOfWindow copies the count most recent values of a ring buffer (oldest
// first) and returns their median. Caller holds the window mutex.
func medianOfWindow(ring []float64, head, count int) float64 {
	sorted := make([]float64, count)
	for i := 0; i < count; i++ {
		sorted[i] = ring[(head-count+i+throughputWindowCap)%throughputWindowCap]
	}
	slices.Sort(sorted)

	if count%2 == 1 {
		return sorted[count/2]
	}
	return (sorted[count/2-1] + sorted[count/2]) / 2
}
