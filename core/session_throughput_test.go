package core

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// sample is one recorded call: outputTokens produced over duration.
type sample struct {
	tokens int
	d      time.Duration
}

// runWindow records the samples in order and returns the window's view.
func runWindow(samples []sample) (median float64, count int) {
	w := newSessionThroughputWindow()
	for _, s := range samples {
		median, count = w.record(s.tokens, s.d)
	}
	return median, count
}

func TestSessionThroughputWindow_MedianTable(t *testing.T) {
	s := func(tokens int, secs float64) sample {
		return sample{tokens: tokens, d: time.Duration(secs * float64(time.Second))}
	}

	tests := []struct {
		name       string
		samples    []sample
		wantMedian float64
		wantCount  int
	}{
		{
			name:       "empty window reports no median",
			samples:    nil,
			wantMedian: 0,
			wantCount:  0,
		},
		{
			name:       "one sample is below the minimum",
			samples:    []sample{s(100, 1)},
			wantMedian: 0,
			wantCount:  1,
		},
		{
			name:       "two samples are below the minimum",
			samples:    []sample{s(100, 1), s(200, 2)},
			wantMedian: 0,
			wantCount:  2,
		},
		{
			name:       "three samples odd count median is middle value",
			samples:    []sample{s(10, 1), s(50, 1), s(90, 1)}, // rates 10, 50, 90
			wantMedian: 50,
			wantCount:  3,
		},
		{
			name:       "four samples even count median is mean of middle pair",
			samples:    []sample{s(10, 1), s(30, 1), s(50, 1), s(90, 1)}, // rates 10,30,50,90
			wantMedian: 40,
			wantCount:  4,
		},
		{
			name:       "unsorted insertion order does not matter",
			samples:    []sample{s(90, 1), s(10, 1), s(50, 1)}, // rates 90,10,50
			wantMedian: 50,
			wantCount:  3,
		},
		{
			name:       "fractional rates are preserved",
			samples:    []sample{s(100, 4), s(25, 1), s(75, 2)}, // rates 25, 25, 37.5
			wantMedian: 25,
			wantCount:  3,
		},
		{
			name: "zero output tokens are skipped",
			samples: []sample{
				s(0, 1),   // skipped: no output
				s(10, 1),  // 10
				s(30, 1),  // 30
				s(0, 0.5), // skipped: no output
				s(50, 1),  // 50
			},
			wantMedian: 30,
			wantCount:  3,
		},
		{
			name: "non-positive duration is skipped",
			samples: []sample{
				s(10, 1),            // 10
				s(10, 0),            // skipped: duration <= 0
				s(30, 1),            // 30
				s(10, -1), s(50, 1), // skipped, then 50
			},
			wantMedian: 30,
			wantCount:  3,
		},
		{
			name: "skip-only traffic keeps the window empty",
			samples: []sample{
				s(0, 1), s(10, 0), s(0, 0),
			},
			wantMedian: 0,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMedian, gotCount := runWindow(tt.samples)
			if gotCount != tt.wantCount {
				t.Errorf("count = %d, want %d", gotCount, tt.wantCount)
			}
			if math.Abs(gotMedian-tt.wantMedian) > 1e-9 {
				t.Errorf("median = %v, want %v", gotMedian, tt.wantMedian)
			}
		})
	}
}

// TestSessionThroughputWindow_EvictsOldestBeyondCap pins the sliding-window
// semantics: with more than throughputWindowCap recorded samples, the median
// is computed over the MOST RECENT cap samples only.
func TestSessionThroughputWindow_EvictsOldestBeyondCap(t *testing.T) {
	if throughputWindowCap != 64 {
		t.Fatalf("test assumes the production window cap of 64; got %d", throughputWindowCap)
	}

	w := newSessionThroughputWindow()

	// Record 70 one-second calls with rates 1..70 tok/s in order. After the
	// 70th call the window holds rates 7..70 (the first 6 evicted).
	for tok := 1; tok <= 70; tok++ {
		if _, count := w.record(tok, time.Second); count != min(tok, throughputWindowCap) {
			t.Fatalf("after %d records, count = %d", tok, count)
		}
	}

	// Median of 7..70 (64 samples, even count) = mean of 38 and 39.
	wantMedian := (38.0 + 39.0) / 2
	gotMedian, gotCount := w.median()
	if gotCount != 64 {
		t.Errorf("count = %d, want 64", gotCount)
	}
	if math.Abs(gotMedian-wantMedian) > 1e-9 {
		t.Errorf("median = %v, want %v (over rates 7..70)", gotMedian, wantMedian)
	}
}

// TestSessionThroughputWindow_SkipsDoNotConsumeWindowSlots verifies that
// skipped calls (zero output / non-positive duration) neither occupy ring
// slots nor evict valid samples.
func TestSessionThroughputWindow_SkipsDoNotConsumeWindowSlots(t *testing.T) {
	w := newSessionThroughputWindow()

	// Three valid samples interleaved with 100 skipped ones.
	for i := 0; i < 100; i++ {
		w.record(0, time.Second) // skipped
		w.record(10, 0)          // skipped
	}
	w.record(10, time.Second)
	w.record(30, time.Second)
	for i := 0; i < 100; i++ {
		w.record(0, time.Second) // skipped
	}
	w.record(50, time.Second)

	gotMedian, gotCount := w.median()
	if gotCount != 3 {
		t.Errorf("count = %d, want 3 (skips must not consume slots)", gotCount)
	}
	if math.Abs(gotMedian-30) > 1e-9 {
		t.Errorf("median = %v, want 30", gotMedian)
	}
}

// TestSessionThroughputWindow_ConcurrentRecord exercises the observer fan-out
// contract: the session UsageTracker may notify from arbitrary goroutines
// (conductor steps, subagents share one TrackingCaller), so record/median must
// be safe under concurrency. Run with -race.
func TestSessionThroughputWindow_ConcurrentRecord(t *testing.T) {
	w := newSessionThroughputWindow()

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				w.record(10*(g+1), time.Second)
				// A concurrent read must never observe a torn window.
				if _, count := w.median(); count > throughputWindowCap {
					t.Errorf("count = %d exceeds cap %d", count, throughputWindowCap)
				}
			}
		}(g)
	}
	wg.Wait()

	_, count := w.median()
	if count != goroutines*perGoroutine && count != throughputWindowCap {
		t.Errorf("count = %d, want %d (or capped at %d)", count, goroutines*perGoroutine, throughputWindowCap)
	}
}

// TestSessionThroughputWindow_MinSamplesConstant documents the configured
// minimum: the median stays hidden until three samples exist.
func TestSessionThroughputWindow_MinSamplesConstant(t *testing.T) {
	if throughputMinSamples != 3 {
		t.Fatalf("throughputMinSamples = %d, want 3 (task contract)", throughputMinSamples)
	}
}

// usage builds an llm.TokenUsage for observe tests.
func usage(in, out, cached, write int) llm.TokenUsage {
	return llm.TokenUsage{InputTokens: in, OutputTokens: out, CachedInputTokens: cached, CacheWriteTokens: write}
}

// TestSessionThroughputWindow_Observe_CacheTotalsAndHitRate pins the
// session-cumulative prompt-cache accounting: cached/write totals accumulate
// across calls and the hit rate is cached over observed per-call input
// tokens (both details are already included in InputTokens, so the rate is
// never above 100).
func TestSessionThroughputWindow_Observe_CacheTotalsAndHitRate(t *testing.T) {
	w := newSessionThroughputWindow()

	// Call 1: 1000 in, 400 cached (40%), 100 written.
	stats := w.observe(usage(1000, 50, 400, 100), time.Second)
	if stats.CachedInputTokensTotal != 400 || stats.CacheWriteTokensTotal != 100 {
		t.Errorf("after call 1: cached/write = %d/%d, want 400/100", stats.CachedInputTokensTotal, stats.CacheWriteTokensTotal)
	}
	if stats.CacheHitPercent != 40 {
		t.Errorf("after call 1: hit rate = %v, want 40", stats.CacheHitPercent)
	}

	// Call 2: 900 in, 700 cached → cumulative 1100 cached / 1900 in.
	stats = w.observe(usage(900, 60, 700, 0), time.Second)
	if stats.CachedInputTokensTotal != 1100 || stats.CacheWriteTokensTotal != 100 {
		t.Errorf("after call 2: cached/write = %d/%d, want 1100/100", stats.CachedInputTokensTotal, stats.CacheWriteTokensTotal)
	}
	want := 1100.0 / 1900.0 * 100
	if math.Abs(stats.CacheHitPercent-want) > 1e-9 {
		t.Errorf("after call 2: hit rate = %v, want %v", stats.CacheHitPercent, want)
	}

	// A call without cache detail leaves the totals untouched and dilutes
	// the rate (2000 in, 0 cached → 1100/3900).
	stats = w.observe(usage(2000, 70, 0, 0), time.Second)
	if stats.CachedInputTokensTotal != 1100 {
		t.Errorf("after call 3: cached = %d, want 1100 (zero detail must not reset)", stats.CachedInputTokensTotal)
	}
	want = 1100.0 / 3900.0 * 100
	if math.Abs(stats.CacheHitPercent-want) > 1e-9 {
		t.Errorf("after call 3: hit rate = %v, want %v", stats.CacheHitPercent, want)
	}

	if stats.CallCount != 3 {
		t.Errorf("CallCount = %d, want 3", stats.CallCount)
	}
}

// TestSessionThroughputWindow_Observe_EmptyWindow pins the cold-start shape:
// before any call, every aggregate reports its zero value.
func TestSessionThroughputWindow_Observe_EmptyWindow(t *testing.T) {
	w := newSessionThroughputWindow()
	stats := w.observe(llm.TokenUsage{}, 0)
	if stats != (SessionTokenCallStats{}) {
		t.Errorf("empty observation must report the zero stats, got %+v", stats)
	}
}

// TestSessionThroughputWindow_Observe_LatencyAggregates pins the duration
// semantics: the median is windowed and hidden below the minimum sample
// count, mean/max/count are session-cumulative, and non-positive durations
// neither count calls nor pollute the aggregates.
func TestSessionThroughputWindow_Observe_LatencyAggregates(t *testing.T) {
	w := newSessionThroughputWindow()

	// Two calls: below the minimum the median stays hidden while the
	// cumulative counters advance.
	w.observe(usage(100, 10, 0, 0), 500*time.Millisecond)
	stats := w.observe(usage(100, 10, 0, 0), 1500*time.Millisecond)
	if stats.MedianCallMillis != 0 {
		t.Errorf("median below minimum samples = %v, want 0", stats.MedianCallMillis)
	}
	if stats.CallCount != 2 || stats.MaxCallMillis != 1500 {
		t.Errorf("count/max = %d/%v, want 2/1500", stats.CallCount, stats.MaxCallMillis)
	}
	if math.Abs(stats.MeanCallMillis-1000) > 1e-9 {
		t.Errorf("mean = %v, want 1000", stats.MeanCallMillis)
	}

	// Third call: the windowed median becomes visible (500, 1500, 2000 →
	// median 1500).
	stats = w.observe(usage(100, 10, 0, 0), 2*time.Second)
	if math.Abs(stats.MedianCallMillis-1500) > 1e-9 {
		t.Errorf("median = %v, want 1500", stats.MedianCallMillis)
	}
	if stats.CallCount != 3 || stats.MaxCallMillis != 2000 {
		t.Errorf("count/max = %d/%v, want 3/2000", stats.CallCount, stats.MaxCallMillis)
	}
	if math.Abs(stats.MeanCallMillis-4000.0/3.0) > 1e-9 {
		t.Errorf("mean = %v, want %v", stats.MeanCallMillis, 4000.0/3.0)
	}

	// A non-positive duration is a skip: no call counted, aggregates intact.
	stats = w.observe(usage(100, 10, 0, 0), 0)
	if stats.CallCount != 3 || stats.MaxCallMillis != 2000 {
		t.Errorf("skip changed count/max: %d/%v, want 3/2000", stats.CallCount, stats.MaxCallMillis)
	}
}

// TestSessionThroughputWindow_Observe_NegativeUsageClamped pins the defensive
// clamp: a misbehaving provider reporting negative counters must not skew
// the cumulative totals or the hit rate.
func TestSessionThroughputWindow_Observe_NegativeUsageClamped(t *testing.T) {
	w := newSessionThroughputWindow()

	w.observe(usage(1000, 50, 400, 100), time.Second)
	stats := w.observe(usage(-500, -10, -50, -50), time.Second)
	if stats.CachedInputTokensTotal != 400 || stats.CacheWriteTokensTotal != 100 {
		t.Errorf("cached/write = %d/%d, want 400/100 (negatives clamped)", stats.CachedInputTokensTotal, stats.CacheWriteTokensTotal)
	}
	if stats.CacheHitPercent != 40 {
		t.Errorf("hit rate = %v, want 40 (negative inputs must not dilute)", stats.CacheHitPercent)
	}
	if stats.CallCount != 2 {
		t.Errorf("CallCount = %d, want 2", stats.CallCount)
	}
}

// TestSessionThroughputWindow_Observe_Concurrent exercises the observer
// fan-out contract for the full aggregate path: observe must be safe under
// concurrency. Run with -race.
func TestSessionThroughputWindow_Observe_Concurrent(t *testing.T) {
	w := newSessionThroughputWindow()

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				stats := w.observe(usage(100, 10, 40, 0), time.Second)
				if stats.CallCount > goroutines*perGoroutine {
					t.Errorf("CallCount = %d exceeds %d", stats.CallCount, goroutines*perGoroutine)
				}
			}
		}()
	}
	wg.Wait()

	stats := w.observe(llm.TokenUsage{}, 0)
	if stats.CallCount != goroutines*perGoroutine {
		t.Errorf("CallCount = %d, want %d", stats.CallCount, goroutines*perGoroutine)
	}
	if stats.CachedInputTokensTotal != goroutines*perGoroutine*40 {
		t.Errorf("cached total = %d, want %d", stats.CachedInputTokensTotal, goroutines*perGoroutine*40)
	}
}
