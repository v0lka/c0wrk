package core

import (
	"math"
	"sync"
	"testing"
	"time"
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
