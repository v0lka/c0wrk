package llmbudget

import "time"

// This file owns the numeric constants that cross the layer boundary
// (ADR-071 D8): exported per the core/embeddedllm/limits.go precedent so
// backend/config validates against the very same figures — a bound at only
// one layer is one an operator can walk around by hand-editing config.yaml.
// specs/decisions/071-adaptive-llm-request-budgets.md

const (
	// FloorRemote is the remote minimum: never clips a healthy hosted call,
	// and a wedged one surfaces within the legacy regime (ADR-071 D8).
	FloorRemote time.Duration = 120 * time.Second

	// FloorLocal is the local minimum (loopback openai_compatible endpoints).
	FloorLocal time.Duration = 300 * time.Second

	// FloorEmbedded is the embedded minimum (the backend-owned `embedded` provider).
	FloorEmbedded time.Duration = 600 * time.Second

	// CeilingRemote is exactly the legacy fixed 600 s: remote is never worse
	// than today's regime (ADR-071 Consequences).
	CeilingRemote time.Duration = 600 * time.Second

	// CeilingLocal is 30 minutes of headroom for long local reasoning traces
	// (the measured 21-minute trace motivated this figure).
	CeilingLocal time.Duration = 1800 * time.Second

	// CeilingEmbedded is the embedded maximum (ADR-071 D8).
	CeilingEmbedded time.Duration = 1800 * time.Second
)

const (
	// WarmupMinSamples is the sample count a model needs before the trained
	// budget engages (D4); below it the deadline is DefaultWarmupBudget. No
	// warmup knob exists — set the senior fixed request_timeout instead (D5).
	WarmupMinSamples = 3

	// DefaultWarmupBudget is the warmup deadline for every class: the legacy
	// fixed 600 s, not overridable (D4 carries no warmup knob).
	DefaultWarmupBudget time.Duration = 600 * time.Second

	// LegacyFixedBudget is a zero llmRequestTimeout's legacy meaning with the
	// kill-switch off (D11): "no opinion" means the fixed 600 s, never "no deadline".
	LegacyFixedBudget time.Duration = 600 * time.Second
)

const (
	// MinOutputReserve is the out_reserve floor (D7): always price ≥1024
	// output tokens, however small the observed outputs have been.
	MinOutputReserve = 1024

	// BytesPerTokenEstimate is the conservative bytes→tokens divisor for
	// est_in (D7): erring toward a larger input estimate and budget.
	BytesPerTokenEstimate = 3

	// SampleWindow is the per-model ring size (D10): budgets track recent
	// behavior, not history.
	SampleWindow = 32

	// MaxEscalationFactor caps the ×2 ladder (D9); the class ceiling clamps
	// the actual budget, so the cap only keeps the multiplication total.
	MaxEscalationFactor = 1024
)
