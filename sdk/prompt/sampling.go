package prompt

// SamplingConfig holds family-aware generation parameter defaults.
// Pointer fields indicate "set" vs "unset" — nil means no override.
type SamplingConfig struct {
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
}

// DefaultSampling returns recommended generation parameters for the given model family.
// These are advisory defaults — providers should use them only when no explicit
// user overrides are set.
func DefaultSampling(family string) SamplingConfig {
	switch family {
	case "anthropic":
		// Anthropic recommends letting model self-select temperature
		return SamplingConfig{} // all nil
	case "openai_flagship", "openai_standard":
		return SamplingConfig{Temperature: fp(0.3)}
	case "gemini":
		return SamplingConfig{Temperature: fp(1.0)} // Google recommends higher; 0.3 causes looping
	case "mistral":
		return SamplingConfig{Temperature: fp(0.3)}
	case "deepseek":
		return SamplingConfig{Temperature: fp(0.3)}
	case "kimi":
		return SamplingConfig{Temperature: fp(0.55)} // Empirically optimal for coding
	default:
		return SamplingConfig{
			Temperature: fp(0.5),
			TopP:        fp(0.95),
		}
	}
}

// fp is a helper that returns a pointer to a float64 value.
func fp(v float64) *float64 { return &v }
