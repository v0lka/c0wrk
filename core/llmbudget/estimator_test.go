package llmbudget

import (
	"math"
	"testing"
	"time"
)

// nearDuration reports whether got and want agree within tol (float dust from
// the least-squares solve makes exact equality wrong for trained budgets).
func nearDuration(got, want, tol time.Duration) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// fitSamples generates six well-conditioned samples from the exact rates
// r_in = 0.1 s/tok and r_out = 0.5 s/tok, so the two-parameter fit recovers
// them (up to float dust) and the p85 ratio multiplier is 1.
var fitSamples = []struct {
	in, out int
	d       time.Duration
}{
	{1000, 100, 150 * time.Second},
	{500, 400, 250 * time.Second},
	{2000, 50, 225 * time.Second},
	{800, 600, 380 * time.Second},
	{1500, 200, 250 * time.Second},
	{300, 900, 480 * time.Second},
}

// ingestFitSamples feeds the fixture to the table under key. OutputLimit 8192
// with p85(out)=900 gives out_reserve = 1024, and BodyBytes 3000 gives
// est_in = 1000, so the priced budget is 0.1·1000 + 0.5·1024 = 612 s.
func ingestFitSamples(t *testing.T, tb *BudgetTable, key string) {
	t.Helper()
	for _, s := range fitSamples {
		tb.Ingest(key, s.in, s.out, s.d)
	}
}

// fitResolverInput is the resolver input that prices the fixture at 612 s.
func fitResolverInput(key string, class Class) ResolverInput {
	return ResolverInput{
		Class:           class,
		Key:             key,
		BodyBytes:       3000,
		OutputLimit:     8192,
		AdaptiveEnabled: true,
	}
}

func TestEstimateTwoParameterFit(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "m")
	got := tb.ResolveDeadline(fitResolverInput("m", ClassLocal))
	if !nearDuration(got, 612*time.Second, time.Millisecond) {
		t.Fatalf("trained budget = %v, want ~612s", got)
	}
}

func TestEstimateClampsToClassEnvelope(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "m")
	// The raw 612s budget exceeds the remote ceiling but sits inside the
	// local and embedded envelopes.
	if got := tb.ResolveDeadline(fitResolverInput("m", ClassRemote)); got != CeilingRemote {
		t.Fatalf("remote budget = %v, want ceiling %v", got, CeilingRemote)
	}
	if got := tb.ResolveDeadline(fitResolverInput("m", ClassLocal)); !nearDuration(got, 612*time.Second, time.Millisecond) {
		t.Fatalf("local budget = %v, want ~612s (inside the envelope)", got)
	}
	// A huge request body inflates est_in far past every ceiling.
	huge := fitResolverInput("m", ClassEmbedded)
	huge.BodyBytes = 3_000_000
	if got := tb.ResolveDeadline(huge); got != CeilingEmbedded {
		t.Fatalf("embedded budget = %v, want ceiling %v", got, CeilingEmbedded)
	}
}

func TestEstimateClampsToClassFloor(t *testing.T) {
	tb := NewBudgetTable()
	// Same shapes as the fixture but priced at r_in=0.001, r_out=0.005 —
	// a fast model whose raw budget (1s + 5.12s) lands under every floor.
	for _, s := range fitSamples {
		tb.Ingest("m", s.in, s.out, s.d/100)
	}
	if got := tb.ResolveDeadline(fitResolverInput("m", ClassRemote)); got != FloorRemote {
		t.Fatalf("remote budget = %v, want floor %v", got, FloorRemote)
	}
	if got := tb.ResolveDeadline(fitResolverInput("m", ClassLocal)); got != FloorLocal {
		t.Fatalf("local budget = %v, want floor %v", got, FloorLocal)
	}
}

func TestDegenerateFitFallsBackToNormalizedPercentile(t *testing.T) {
	tb := NewBudgetTable()
	// in == out for every sample: the design matrix is rank-deficient, so the
	// two rates are not identifiable and the fit must be declared degenerate.
	for _, s := range []struct {
		in, out int
		d       time.Duration
	}{
		{100, 100, 15 * time.Second},
		{200, 200, 30 * time.Second},
		{400, 400, 60 * time.Second},
		{800, 800, 120 * time.Second},
	} {
		tb.Ingest("m", s.in, s.out, s.d)
	}
	if _, ok := fitRates(tb.snapshot("m")); ok {
		t.Fatal("collinear design must be degenerate")
	}
	// Ladder rung two: per-token duration p85 = 0.075 s/tok (uniform), sizes
	// est_in=1000 and out_reserve=1024 → 0.075·2024 = 151.8s, inside the
	// remote envelope.
	got := tb.ResolveDeadline(fitResolverInput("m", ClassRemote))
	if !nearDuration(got, time.Duration(0.075*2024*float64(time.Second)), time.Millisecond) {
		t.Fatalf("normalized-percentile budget = %v, want ~151.8s", got)
	}
}

func TestDegenerateEverythingFallsBackToClassFloor(t *testing.T) {
	tb := NewBudgetTable()
	// No sample carries token information: the fit is degenerate AND the
	// percentile rung has nothing to renormalize — the ladder ends at the
	// class constants.
	for _, d := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		tb.Ingest("m", 0, 0, d)
	}
	got := tb.ResolveDeadline(fitResolverInput("m", ClassRemote))
	if got != FloorRemote {
		t.Fatalf("zero-token ladder end = %v, want class floor %v", got, FloorRemote)
	}
}

func TestFitPricedRatesFollowUniformDurationScaling(t *testing.T) {
	// Doubling every duration doubles both priced rates exactly: the fit
	// recovers scaled coefficients and the p85 ratio multiplier is 2.
	tb := NewBudgetTable()
	for _, s := range fitSamples {
		tb.Ingest("m", s.in, s.out, 2*s.d)
	}
	r, ok := fitRates(tb.snapshot("m"))
	if !ok {
		t.Fatal("fit must succeed on scaled exact data")
	}
	if !nearDuration(time.Duration(r.in*float64(time.Second)), 200*time.Millisecond, time.Millisecond) ||
		!nearDuration(time.Duration(r.out*float64(time.Second)), time.Second, time.Millisecond) {
		t.Fatalf("priced rates = (%v, %v) s/tok, want ~(0.2, 1.0)", r.in, r.out)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	vals := []float64{10, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if got := percentile(vals, 0.85); got != 9 {
		t.Fatalf("p85 of 1..10 = %v, want 9 (nearest rank)", got)
	}
	if got := percentile([]float64{5}, 0.85); got != 5 {
		t.Fatalf("p85 of a single value = %v, want 5", got)
	}
	if got := percentile([]float64{1, 2}, 0.85); got != 2 {
		t.Fatalf("p85 of [1,2] = %v, want 2", got)
	}
	if !math.IsNaN(percentile(nil, 0.85)) {
		t.Fatal("p85 of nothing must be NaN")
	}
}

func TestEstInputTokens(t *testing.T) {
	tests := []struct {
		bodyBytes, want int
	}{
		{0, 0}, {-10, 0}, {1, 1}, {3, 1}, {4, 2}, {3000, 1000}, {3001, 1001},
	}
	for _, tt := range tests {
		if got := estInputTokens(tt.bodyBytes); got != tt.want {
			t.Errorf("estInputTokens(%d) = %d, want %d", tt.bodyBytes, got, tt.want)
		}
	}
}

func TestOutputReserve(t *testing.T) {
	samples := make([]Sample, 0, 6)
	for _, out := range []int{100, 400, 50, 600, 200, 900} {
		samples = append(samples, Sample{In: 1, Out: out, Duration: time.Second})
	}
	// p85(out) = 900 → the MinOutputReserve floor engages.
	if got := outputReserve(samples, 8192); got != MinOutputReserve {
		t.Fatalf("reserve = %d, want %d", got, MinOutputReserve)
	}
	// The generation ceiling caps the reserve.
	if got := outputReserve(samples, 800); got != 800 {
		t.Fatalf("reserve with OutputLimit 800 = %d, want 800", got)
	}
	// Unknown OutputLimit applies no upper clamp.
	if got := outputReserve(samples, 0); got != MinOutputReserve {
		t.Fatalf("reserve with unknown OutputLimit = %d, want %d", got, MinOutputReserve)
	}
	// Large observed outputs pass through above the floor.
	big := []Sample{{In: 1, Out: 5000, Duration: time.Second}, {In: 1, Out: 7000, Duration: time.Second}}
	if got := outputReserve(big, 0); got != 7000 {
		t.Fatalf("reserve = %d, want 7000 (observed p85)", got)
	}
}

func TestResolveWarmupUnderThreeSamples(t *testing.T) {
	tb := NewBudgetTable()
	in := ResolverInput{Class: ClassRemote, Key: "m", AdaptiveEnabled: true}
	for range 2 {
		if got := tb.ResolveDeadline(in); got != DefaultWarmupBudget {
			t.Fatalf("warmup budget = %v, want %v", got, DefaultWarmupBudget)
		}
		tb.Ingest("m", 1000, 100, time.Second)
	}
	// The third sample flips to the trained path: three identical samples are
	// rank-deficient, so the ladder renormalizes 1s/1100tok against
	// out_reserve 1024 → 0.93s, clamped to the remote floor.
	tb.Ingest("m", 1000, 100, time.Second)
	if got := tb.ResolveDeadline(in); got != FloorRemote {
		t.Fatalf("trained budget at exactly 3 samples = %v, want floor %v", got, FloorRemote)
	}
}

func TestResolveOverridePriority(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "m")
	base := fitResolverInput("m", ClassLocal)
	if got := tb.ResolveDeadline(base); !nearDuration(got, 612*time.Second, time.Millisecond) {
		t.Fatalf("setup: trained budget = %v, want ~612s", got)
	}
	// 1. per-model request_timeout beats everything…
	perModel := base
	perModel.PerModelRequestTimeout = 60 * time.Second
	if got := tb.ResolveDeadline(perModel); got != 60*time.Second {
		t.Fatalf("per-model override = %v, want 60s", got)
	}
	// 2. …global llmRequestTimeout beats the trained budget…
	global := base
	global.GlobalRequestTimeout = 900 * time.Second
	if got := tb.ResolveDeadline(global); got != 900*time.Second {
		t.Fatalf("global override = %v, want 900s", got)
	}
	// 3. …and a fresh key (no samples) warms up.
	fresh := base
	fresh.Key = "fresh"
	if got := tb.ResolveDeadline(fresh); got != DefaultWarmupBudget {
		t.Fatalf("warmup = %v, want %v", got, DefaultWarmupBudget)
	}
	// Escalation latched for the trained key must not move the fixed
	// overrides (D5: an explicit override is never escalated).
	tb.Escalate("m")
	if got := tb.ResolveDeadline(perModel); got != 60*time.Second {
		t.Fatalf("escalated per-model override = %v, want 60s", got)
	}
	if got := tb.ResolveDeadline(global); got != 900*time.Second {
		t.Fatalf("escalated global override = %v, want 900s", got)
	}
	// …but does move the trained budget ×2.
	if got := tb.ResolveDeadline(base); !nearDuration(got, 1224*time.Second, time.Millisecond) {
		t.Fatalf("escalated trained budget = %v, want ~1224s", got)
	}
}

func TestResolveKillSwitchOffIsInert(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "m")
	in := fitResolverInput("m", ClassLocal)
	in.AdaptiveEnabled = false
	// "No opinion" never means "no deadline": zero global → the legacy fixed.
	if got := tb.ResolveDeadline(in); got != LegacyFixedBudget {
		t.Fatalf("kill-switch-off budget = %v, want legacy %v", got, LegacyFixedBudget)
	}
	in.GlobalRequestTimeout = 777 * time.Second
	if got := tb.ResolveDeadline(in); got != 777*time.Second {
		t.Fatalf("kill-switch-off budget = %v, want 777s", got)
	}
	// The whole ladder — per-model override included — is inert when off.
	in.PerModelRequestTimeout = 60 * time.Second
	if got := tb.ResolveDeadline(in); got != 777*time.Second {
		t.Fatalf("kill-switch-off budget = %v, want 777s (per-model inert too)", got)
	}
}

func TestResolveEscalationClampsAtCeiling(t *testing.T) {
	tb := NewBudgetTable()
	ingestFitSamples(t, tb, "m")
	base := fitResolverInput("m", ClassLocal)
	tb.Escalate("m")
	tb.Escalate("m") // ×4 = 2448s → clamped to the local ceiling
	if got := tb.ResolveDeadline(base); got != CeilingLocal {
		t.Fatalf("doubly-escalated budget = %v, want ceiling %v", got, CeilingLocal)
	}
	// The same latch under the remote class pins at the remote ceiling.
	base.Class = ClassRemote
	if got := tb.ResolveDeadline(base); got != CeilingRemote {
		t.Fatalf("doubly-escalated remote budget = %v, want ceiling %v", got, CeilingRemote)
	}
}

func TestResolveEscalationDoesNotTouchWarmup(t *testing.T) {
	tb := NewBudgetTable()
	tb.Escalate("fresh")
	in := ResolverInput{Class: ClassRemote, Key: "fresh", AdaptiveEnabled: true}
	if got := tb.ResolveDeadline(in); got != DefaultWarmupBudget {
		t.Fatalf("escalated warmup = %v, want %v (warmup is behavior-preserving, D4)", got, DefaultWarmupBudget)
	}
}

func TestSecondsToDurationSaturates(t *testing.T) {
	if got := secondsToDuration(math.NaN()); got != 0 {
		t.Fatalf("NaN → %v, want 0", got)
	}
	if got := secondsToDuration(-1); got != 0 {
		t.Fatalf("negative → %v, want 0", got)
	}
	if got := secondsToDuration(1e300); got <= 0 {
		t.Fatalf("huge value must saturate, not wrap; got %v", got)
	}
	if got := secondsToDuration(2); got != 2*time.Second {
		t.Fatalf("2s → %v, want 2s", got)
	}
}
