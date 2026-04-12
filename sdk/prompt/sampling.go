package prompt

// SamplingConfig holds tier-aware generation parameter defaults.
// Pointer fields indicate "set" vs "unset" — nil means no override.
type SamplingConfig struct {
	Temperature       *float64
	TopP              *float64
	MaxTokens         *int
	RepetitionPenalty *float64
	StopSequences     []string
}

// DefaultSampling returns recommended generation parameters for the given model tier.
// These are advisory defaults — providers should use them only when no explicit
// user overrides are set.
func DefaultSampling(tier ModelTier) SamplingConfig {
	switch tier {
	case TierLarge:
		return SamplingConfig{
			Temperature: fp(0.5),
			TopP:        fp(0.95),
			// No repetition penalty or stop sequences for large models
		}
	case TierSmall:
		return SamplingConfig{
			Temperature:       fp(0.2),
			TopP:              fp(0.9),
			RepetitionPenalty: fp(1.1),
			StopSequences:     []string{"\n\n\n\n"},
		}
	default:
		// Unknown tier — return large defaults as safe fallback
		return DefaultSampling(TierLarge)
	}
}

// fp is a helper that returns a pointer to a float64 value.
func fp(v float64) *float64 { return &v }
