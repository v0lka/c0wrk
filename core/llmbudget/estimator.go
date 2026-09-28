package llmbudget

import (
	"math"
	"slices"
	"time"
)

// BudgetInput carries the per-request facts the budget formula consumes
// (ADR-071 D7).
type BudgetInput struct {
	// BodyBytes is the size of the marshaled request body. est_in is derived
	// from it as ceil(bytes/BytesPerTokenEstimate) — erring toward a larger
	// input estimate.
	BodyBytes int
	// OutputLimit is the EXPLICIT per-model generation ceiling from
	// llm.models.<name>.output_limit. Zero or negative means no override is set,
	// so the observed p85 output is used without an upper clamp (the clamp only
	// ever bounds a value that is already below the model's real ceiling, so
	// omitting it is safe; the wiring does not resolve the built-in metadata).
	OutputLimit int
}

// rates are the fitted per-token speeds, in seconds per token, priced at the
// p85 tail (ADR-071 D6).
type rates struct{ in, out float64 }

// MinFitSamples is the smallest sample count the two-parameter fit attempts.
// Two rates need at least one residual degree of freedom; below this the fit
// is degenerate by construction and the degradation ladder takes over.
const MinFitSamples = 3

// ResolveDeadline resolves the deadline for one LLM request (ADR-071 D5), in
// priority order:
//
//  1. PerModelRequestTimeout > 0 — fixed, never escalated;
//  2. GlobalRequestTimeout > 0 — fixed, never escalated;
//  3. the adaptive budget: DefaultWarmupBudget while the key holds fewer than
//     WarmupMinSamples samples, then the trained budget (estimate + any latched
//     escalation, D9);
//  4. the kill-switch off — the legacy fixed regime: GlobalRequestTimeout
//     when positive, else LegacyFixedBudget ("no opinion" never means "no
//     deadline", D11).
//
// Key is the budget key the caller resolved for this request (wire-extracted
// model, or the provider-level fallback); Class bounds the adaptive budget.
// Warmup carries no escalation by design (D4): until evidence exists the
// deadline is exactly today's behavior.
func (t *BudgetTable) ResolveDeadline(in ResolverInput) time.Duration {
	if !in.AdaptiveEnabled {
		if in.GlobalRequestTimeout > 0 {
			return in.GlobalRequestTimeout
		}
		return LegacyFixedBudget
	}
	if in.PerModelRequestTimeout > 0 {
		return in.PerModelRequestTimeout
	}
	if in.GlobalRequestTimeout > 0 {
		return in.GlobalRequestTimeout
	}
	count, escal := t.state(in.Key)
	if count < WarmupMinSamples {
		return DefaultWarmupBudget
	}
	budget := t.estimate(in.Key, in.Class, BudgetInput{
		BodyBytes:   in.BodyBytes,
		OutputLimit: in.OutputLimit,
	})
	if escal > 1 {
		// Escalation multiplies the trained budget and re-clamps at the class
		// ceiling (D9). The floor cannot be undercut: the multiplier is ≥ 1.
		if scaled := scaleDuration(budget, escal); scaled < in.Class.Ceiling() {
			budget = scaled
		} else {
			budget = in.Class.Ceiling()
		}
	}
	return budget
}

// ResolverInput carries the operator-owned deadline opinions for one request.
// All durations are resolved values (seconds in config; callers convert).
type ResolverInput struct {
	Class Class
	// Key is the budget key: the wire-extracted model name, or the
	// provider-level fallback key when extraction failed.
	Key string
	// BodyBytes and OutputLimit feed the budget formula (see BudgetInput).
	BodyBytes   int
	OutputLimit int
	// PerModelRequestTimeout is llm.models.<name>.request_timeout: a positive
	// value is a fixed deadline that is never escalated (D5).
	PerModelRequestTimeout time.Duration
	// GlobalRequestTimeout is timeouts.llmRequestTimeout: a positive value is
	// a fixed deadline that is never escalated (D5).
	GlobalRequestTimeout time.Duration
	// AdaptiveEnabled mirrors timeouts.adaptive_budget.enabled (D11).
	AdaptiveEnabled bool
}

// estimate computes the trained adaptive budget for the key (ADR-071 D6/D7),
// unescalated but already clamped to the class envelope:
//
//  1. the two-parameter fit priced at p85 — the main path;
//  2. a size-normalized p85 percentile of observed durations — when the fit
//     is degenerate (too few effective samples, rank-deficient or non-positive
//     rates);
//  3. the class floor — when no sample carries token information at all.
//
// Rung 3 is the reading of D6's "class constants": data that cannot inform
// size at all leaves only the class envelope, and the floor is its minimum
// defensible budget. D9's escalation bounds the harm of a floor that is too
// tight, and the percentile rung (which works from durations alone) covers
// every degenerate-but-informative case short of this one.
func (t *BudgetTable) estimate(key string, class Class, in BudgetInput) time.Duration {
	samples := t.snapshot(key)
	estIn := estInputTokens(in.BodyBytes)
	outReserve := outputReserve(samples, in.OutputLimit)

	if r, ok := fitRates(samples); ok {
		sec := r.in*float64(estIn) + r.out*float64(outReserve)
		return clampBudget(secondsToDuration(sec), class)
	}
	if size := estIn + outReserve; size > 0 {
		if d, ok := normalizedPercentile(samples, size); ok {
			return clampBudget(d, class)
		}
	}
	return class.Floor()
}

// fitRates fits duration ≈ in·r_in + out·r_out over the size-carrying samples
// by ordinary least squares, then prices both rates at the p85 of the
// per-sample duration/fitted ratios, so the priced rates predict the duration
// 85% of observed calls stay under (ADR-071 D6). Zero-token samples are
// excluded up front: they carry no size information and would only bias the
// intercepts. ok is false on the degenerate paths — fewer than MinFitSamples
// effective samples, a rank-deficient (collinear) design, or non-positive /
// non-finite rates — and the caller degrades down the ladder.
func fitRates(samples []Sample) (rates, bool) {
	var (
		sxx, soo, sio, sdIn, sdOut float64
		n                          int
	)
	for _, s := range samples {
		if s.tokens() <= 0 {
			continue
		}
		x, y, d := float64(s.In), float64(s.Out), s.Duration.Seconds()
		sxx += x * x
		soo += y * y
		sio += x * y
		sdIn += x * d
		sdOut += y * d
		n++
	}
	if n < MinFitSamples {
		return rates{}, false
	}
	det := sxx*soo - sio*sio
	scale := math.Max(sxx, soo)
	if scale <= 0 || det <= scale*scale*1e-12 {
		// Rank-deficient design: in and out carry (nearly) the same signal,
		// so the two rates are not identifiable — the classic degenerate case.
		return rates{}, false
	}
	rIn := (soo*sdIn - sio*sdOut) / det
	rOut := (sxx*sdOut - sio*sdIn) / det
	if !isSaneRate(rIn) || !isSaneRate(rOut) {
		return rates{}, false
	}
	ratios := make([]float64, 0, n)
	for _, s := range samples {
		if s.tokens() <= 0 {
			continue
		}
		fitted := rIn*float64(s.In) + rOut*float64(s.Out)
		if fitted <= 0 {
			continue
		}
		ratios = append(ratios, s.Duration.Seconds()/fitted)
	}
	m := percentile(ratios, 0.85)
	if !isSaneRate(m) {
		return rates{}, false
	}
	return rates{in: rIn * m, out: rOut * m}, true
}

// isSaneRate reports whether v is a usable positive rate.
func isSaneRate(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

// normalizedPercentile is ladder rung two (ADR-071 D6): the p85 of observed
// per-token durations, renormalized against the current request's estimated
// size (est_in + out_reserve) instead of the historical sizes. ok is false
// when no sample carries token information — the ladder then ends at the
// class constants.
func normalizedPercentile(samples []Sample, estTokens int) (time.Duration, bool) {
	perToken := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.tokens() <= 0 {
			continue
		}
		perToken = append(perToken, s.Duration.Seconds()/float64(s.tokens()))
	}
	r := percentile(perToken, 0.85)
	if !isSaneRate(r) {
		return 0, false
	}
	return secondsToDuration(r * float64(estTokens)), true
}

// estInputTokens is est_in of ADR-071 D7: ceil(bodyBytes / BytesPerTokenEstimate).
func estInputTokens(bodyBytes int) int {
	if bodyBytes <= 0 {
		return 0
	}
	return (bodyBytes + BytesPerTokenEstimate - 1) / BytesPerTokenEstimate
}

// outputReserve is out_reserve of ADR-071 D7: the model's observed p85 output,
// never below MinOutputReserve, never above the explicit generation ceiling
// when one is set (llm.models.<name>.output_limit).
// The p85 spans every retained sample's output (zero-output samples included;
// at an 85th percentile they can only pull the reserve down when more than
// 15% of outputs are zero, which is itself evidence of a small-output
// workload). An unset OutputLimit applies no upper clamp.
func outputReserve(samples []Sample, outputLimit int) int {
	outs := make([]float64, 0, len(samples))
	for _, s := range samples {
		outs = append(outs, float64(s.Out))
	}
	p := percentile(outs, 0.85)
	reserve := 0
	if !math.IsNaN(p) {
		reserve = int(math.Ceil(p))
	}
	return clampInt(reserve, MinOutputReserve, outputLimit)
}

// clampInt clamps v to [lo, hi]; a non-positive hi means "no upper bound".
func clampInt(v, lo, hi int) int {
	if v < lo {
		v = lo
	}
	if hi > 0 && v > hi {
		v = hi
	}
	return v
}

// clampBudget clamps d to the class envelope (ADR-071 D7/D8).
func clampBudget(d time.Duration, class Class) time.Duration {
	if d < class.Floor() {
		return class.Floor()
	}
	if d > class.Ceiling() {
		return class.Ceiling()
	}
	return d
}

// percentile returns the p-quantile of vals by nearest rank (the smallest
// value whose cumulative rank reaches ⌈p·n⌉). Empty input yields NaN.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return math.NaN()
	}
	sorted := slices.Clone(vals)
	slices.Sort(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// secondsToDuration converts float seconds to a Duration, saturating instead
// of overflowing: a pathological fit can produce an astronomically large raw
// budget, and the class clamp — not int64 wraparound — must be what bounds it.
//
// The saturation ceiling is derived in INTEGER arithmetic. Computing it in
// float — float64(math.MaxInt64) / float64(time.Second) — rounds math.MaxInt64
// UP to 2^63 (the nearest float64), so the back-conversion
// sec * float64(time.Second) reaches exactly 2^63, which is out of int64 range.
// That out-of-range float→int conversion is implementation-defined: amd64 and
// Windows produce math.MinInt64 (a NEGATIVE duration, defeating the clamp),
// while arm64 saturates to math.MaxInt64. The integer quotient is strictly
// below 2^63, so the product is always representable and the result is
// identical on every architecture.
func secondsToDuration(sec float64) time.Duration {
	if math.IsNaN(sec) || sec <= 0 {
		return 0
	}
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	if sec >= float64(maxSeconds) {
		return time.Duration(maxSeconds) * time.Second
	}
	return time.Duration(sec * float64(time.Second))
}

// scaleDuration multiplies a duration by a factor, saturating safely.
func scaleDuration(d time.Duration, factor float64) time.Duration {
	return secondsToDuration(d.Seconds() * factor)
}
