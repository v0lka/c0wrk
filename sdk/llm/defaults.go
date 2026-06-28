package llm

import "time"

// DefaultRouterConfig returns sensible defaults for Router configuration.
// The Providers field must be set by the caller since there is no sensible
// default for provider selection. Use this as a starting point and override as needed.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{
		MaxRetries:          3,
		InitialBackoff:      1 * time.Second,
		MaxBackoff:          30 * time.Second,
		SafetyMarginPercent: 5,
		OutputTokenReserve:  4096,
	}
}
