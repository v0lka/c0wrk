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
	case "google":
		return SamplingConfig{Temperature: fp(1.0)} // Google recommends higher; low values cause looping
	case "mistral":
		return SamplingConfig{Temperature: fp(0.3)}
	case "deepseek":
		return SamplingConfig{Temperature: fp(0.0)} // Recommended for coding/math; ignored when thinking enabled
	case "qwen":
		return SamplingConfig{Temperature: fp(0.6)} // Recommended for reasoning/analytical tasks
	case "glm":
		return SamplingConfig{Temperature: fp(0.2)} // Optimal for analytical/coding tasks
	case "kimi":
		// Kimi server enforces temperature per model (0.6 standard, 1.0 thinking)
		return SamplingConfig{} // all nil — let server decide
	default:
		return SamplingConfig{
			Temperature: fp(0.5),
			TopP:        fp(0.95),
		}
	}
}

// fp is a helper that returns a pointer to a float64 value.
func fp(v float64) *float64 { return &v }
