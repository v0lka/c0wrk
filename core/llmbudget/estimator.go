package llmbudget

import (
	"math"
	"slices"
	"time"
)

// BudgetInput carries the per-request facts the budget formula consumes
// (ADR-071 D7).
type BudgetInput struct {
	// BodyBytes is the marshaled request body size; est_in derives from it
	// (ADR-071 D7), erring toward a larger input estimate.
	BodyBytes int
	// OutputLimit is the EXPLICIT llm.models.<name>.output_limit ceiling; zero
	// or negative applies no upper clamp — the wiring deliberately does not
	// resolve built-in metadata (ADR-071 D7).
	OutputLimit int
}

// rates are the fitted per-token speeds, in seconds per token, priced at the
// p85 tail (ADR-071 D6).
type rates struct{ in, out float64 }

// MinFitSamples is the smallest sample count the two-parameter fit attempts.
// Two rates need at least one residual degree of freedom; below this the fit
// is degenerate by construction and the degradation ladder takes over.
const MinFitSamples = 3

// ResolveDeadline resolves one LLM request's deadline in the ADR-071 D5
// priority order: per-model fixed override, global fixed override, adaptive
// (warmup until WarmupMinSamples, then trained estimate + latched escalation),
// else the legacy fixed regime with the kill-switch off. Warmup never
// escalates (D4). Full ordering: specs/decisions/071-adaptive-llm-request-budgets.md.
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

// ResolverInput carries the operator-owned deadline opinions for one request;
// all durations are resolved values (seconds in config; callers convert).
type ResolverInput struct {
	Class Class
	Key   string // wire-extracted model name, or the provider-level fallback
	// BodyBytes and OutputLimit feed the budget formula (see BudgetInput).
	BodyBytes              int
	OutputLimit            int
	PerModelRequestTimeout time.Duration // >0: fixed, never escalated (D5)
	GlobalRequestTimeout   time.Duration // >0: fixed, never escalated (D5)
	AdaptiveEnabled        bool          // mirrors timeouts.adaptive_budget.enabled (D11)
}

// estimate computes the trained adaptive budget (ADR-071 D6/D7), unescalated
// but clamped to the class envelope, down the degradation ladder: the p85-
// priced two-parameter fit, then a size-normalized duration percentile when
// the fit is degenerate, then the class floor when no sample carries token
// information — the envelope's minimum defensible budget, with D9 escalation
// bounding the harm of a floor set too tight.
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

// fitRates fits duration ≈ in·r_in + out·r_out by OLS, then prices both rates
// at the p85 of the per-sample duration/fitted ratios (ADR-071 D6). Zero-token
// samples are excluded (no size information, biased intercepts). ok is false
// on the degenerate paths; the caller degrades down the ladder.
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

// normalizedPercentile is ladder rung two (ADR-071 D6): the p85 per-token
// duration, renormalized against the current request's estimated size. ok is
// false when no sample carries token information (ladder ends at the class
// constants).
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

// outputReserve is out_reserve of ADR-071 D7: the observed p85 output clamped
// to [MinOutputReserve, OutputLimit]; zero-output samples only pull it down
// past the 15% mark (small-output workload evidence).
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
// of overflowing: the class clamp — not int64 wraparound — must bound a
// pathological fit. The ceiling is derived in INTEGER arithmetic: the float
// form rounds math.MaxInt64 up to 2^63, and the out-of-range float→int
// conversion is implementation-defined (amd64/Windows yield math.MinInt64 —
// a NEGATIVE duration defeating the clamp — while arm64 saturates); the
// integer quotient stays representable on every architecture.
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
