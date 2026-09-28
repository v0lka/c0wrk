package llmbudget

import (
	"testing"
	"time"
)

func TestIngestSkipsInvalidSamples(t *testing.T) {
	tb := NewBudgetTable()
	tb.Ingest("", 100, 100, time.Second)   // empty model
	tb.Ingest("m", 100, 100, 0)            // unmeasured duration
	tb.Ingest("m", 100, 100, -time.Second) // negative duration
	if got := tb.SampleCount("m"); got != 0 {
		t.Fatalf("SampleCount after invalid ingest = %d, want 0", got)
	}
	tb.Ingest("m", 100, 100, time.Second)
	if got := tb.SampleCount("m"); got != 1 {
		t.Fatalf("SampleCount = %d, want 1", got)
	}
}

func TestIngestKeepsZeroOutputAsPrefillEvidence(t *testing.T) {
	tb := NewBudgetTable()
	tb.Ingest("m", 1000, 0, 5*time.Second)
	samples := tb.snapshot("m")
	if len(samples) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(samples))
	}
	if samples[0].Out != 0 || samples[0].In != 1000 {
		t.Fatalf("zero-output sample not kept as-is: %+v", samples[0])
	}
}

func TestIngestClampsNegativeTokens(t *testing.T) {
	tb := NewBudgetTable()
	tb.Ingest("m", -5, -7, time.Second)
	samples := tb.snapshot("m")
	if len(samples) != 1 || samples[0].In != 0 || samples[0].Out != 0 {
		t.Fatalf("negative tokens not clamped: %+v", samples)
	}
}

func TestTableWindowHoldsLast32Samples(t *testing.T) {
	tb := NewBudgetTable()
	const total = SampleWindow + 8
	for i := range total {
		tb.Ingest("m", i, i, time.Duration(i+1)*time.Second)
	}
	if got := tb.SampleCount("m"); got != SampleWindow {
		t.Fatalf("SampleCount = %d, want %d", got, SampleWindow)
	}
	samples := tb.snapshot("m")
	if len(samples) != SampleWindow {
		t.Fatalf("snapshot len = %d, want %d", len(samples), SampleWindow)
	}
	if samples[0].In != 8 || samples[0].Duration != 9*time.Second {
		t.Fatalf("oldest retained sample = %+v, want the 9th ingested", samples[0])
	}
	if last := samples[len(samples)-1]; last.In != total-1 {
		t.Fatalf("newest retained sample = %+v, want In=%d", last, total-1)
	}
}

func TestTableKeysAreIndependent(t *testing.T) {
	tb := NewBudgetTable()
	tb.Ingest("a", 1, 1, time.Second)
	tb.Ingest("b", 2, 2, 2*time.Second)
	if got := tb.SampleCount("a"); got != 1 {
		t.Fatalf("SampleCount(a) = %d, want 1", got)
	}
	if got := tb.SampleCount("b"); got != 1 {
		t.Fatalf("SampleCount(b) = %d, want 1", got)
	}
	if got := tb.SampleCount("c"); got != 0 {
		t.Fatalf("SampleCount(c) = %d, want 0", got)
	}
}

func TestEscalateLatchesDoublesAndClearsOnSuccess(t *testing.T) {
	tb := NewBudgetTable()
	// Before any record exists the latch still arms at ×2 (an expiry against
	// a warmup-only model must not be silently dropped).
	tb.Escalate("m")
	if _, escal := tb.state("m"); escal != 2 {
		t.Fatalf("escalation after first Escalate = %v, want 2", escal)
	}
	// A successful ingest clears the latch.
	tb.Ingest("m", 100, 100, time.Second)
	if _, escal := tb.state("m"); escal != 1 {
		t.Fatalf("escalation after successful ingest = %v, want 1", escal)
	}
	// Repeated expiries compound up to the cap.
	for range 20 {
		tb.Escalate("m")
	}
	if _, escal := tb.state("m"); escal != MaxEscalationFactor {
		t.Fatalf("escalation cap = %v, want %v", escal, MaxEscalationFactor)
	}
	tb.Escalate("")
	if _, escal := tb.state("m"); escal != MaxEscalationFactor {
		t.Fatalf("Escalate(\"\") must not touch other keys; got %v", escal)
	}
}
