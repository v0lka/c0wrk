package llmbudget

import "time"

// This file owns the numeric constants that cross the layer boundary
// (ADR-071 D8). They are exported following the core/embeddedllm/limits.go
// precedent (ADR-067): backend/config validates the persisted operator surface
// against the very same figures, and a bound that exists at only one of the two
// layers is a bound an operator can walk around by hand-editing config.yaml.
// The floors and ceilings below are the compile-time envelope of the adaptive
// budget — the clamp in the estimator, not any config key, is what bounds it.

const (
	// FloorRemote is the minimum adaptive budget for the remote class: slow
	// enough never to clip a healthy hosted call, tight enough that a wedged
	// remote request surfaces no later than today's fixed 600 s regime would
	// let it after at most one escalation.
	FloorRemote time.Duration = 120 * time.Second

	// FloorLocal is the minimum adaptive budget for the local class (loopback
	// openai_compatible endpoints such as Ollama or LM Studio).
	FloorLocal time.Duration = 300 * time.Second

	// FloorEmbedded is the minimum adaptive budget for the embedded class (the
	// backend-owned `embedded` provider, by definition of its name).
	FloorEmbedded time.Duration = 600 * time.Second

	// CeilingRemote is the maximum adaptive budget for the remote class: it is
	// exactly the legacy fixed 600 s, so remote behavior is never worse than
	// today's regime (ADR-071 Consequences).
	CeilingRemote time.Duration = 600 * time.Second

	// CeilingLocal is the maximum adaptive budget for the local class: 30
	// minutes of headroom for long local reasoning traces (the 21-minute
	// trace measured in docs/development/model-profiles-defaults-research.md
	// motivated exactly this figure).
	CeilingLocal time.Duration = 1800 * time.Second

	// CeilingEmbedded is the maximum adaptive budget for the embedded class.
	CeilingEmbedded time.Duration = 1800 * time.Second
)

const (
	// WarmupMinSamples is the number of retained samples a model needs before
	// the trained budget engages (ADR-071 D4). Below it the deadline is the
	// warmup override or DefaultWarmupBudget — exactly today's behavior.
	WarmupMinSamples = 3

	// DefaultWarmupBudget is the warmup deadline when no per-model warmup
	// override is configured: the legacy fixed 600 s, for every class.
	DefaultWarmupBudget time.Duration = 600 * time.Second

	// LegacyFixedBudget is the legacy meaning of a zero llmRequestTimeout when
	// the kill-switch is off (ADR-071 D11): "no opinion" never means "no
	// deadline" — it means the fixed 600 s the key used to default to.
	LegacyFixedBudget time.Duration = 600 * time.Second
)

const (
	// MinOutputReserve is the floor of out_reserve (ADR-071 D7): the budget
	// always prices at least 1024 output tokens, however small the model's
	// observed outputs have been so far.
	MinOutputReserve = 1024

	// BytesPerTokenEstimate is the conservative bytes→tokens divisor for
	// est_in (ADR-071 D7): the request body is assumed to carry one token per
	// three bytes, erring toward a larger input estimate and therefore a
	// larger budget.
	BytesPerTokenEstimate = 3

	// SampleWindow is the per-model ring size (ADR-071 D10). The table keeps
	// only the most recent SampleWindow samples per model, so a session's
	// budget tracks the model's recent behavior instead of its history.
	SampleWindow = 32

	// MaxEscalationFactor caps the ×2 escalation ladder (ADR-071 D9). The
	// class ceiling clamps the actual budget, so the cap only keeps the
	// multiplication mathematically total; after at most a handful of
	// doublings the budget is pinned at the ceiling anyway.
	MaxEscalationFactor = 1024
)
