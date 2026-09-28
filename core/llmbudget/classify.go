package llmbudget

import (
	"net/url"
	"strings"
	"time"
)

// Class is an LLM provider's timeout class (ADR-071 D3). The class selects the
// floor/ceiling envelope that bounds a model's adaptive budget and the class
// constants the degradation ladder falls back to.
type Class string

const (
	// ClassRemote covers hosted providers — everything that is not the
	// backend-owned embedded provider and does not target loopback.
	ClassRemote Class = "remote"

	// ClassLocal covers providers whose resolved base_url targets loopback
	// (openai_compatible endpoints such as Ollama or LM Studio).
	ClassLocal Class = "local"

	// ClassEmbedded covers the backend-owned `embedded` provider by definition
	// of its reserved name.
	ClassEmbedded Class = "embedded"
)

// EmbeddedProviderName is the backend-owned provider name that classifies as
// embedded by definition (ADR-071 D3); c0wrk reserves it against
// user-created providers.
const EmbeddedProviderName = "embedded"

// Floor returns the minimum adaptive budget for the class (ADR-071 D8).
func (c Class) Floor() time.Duration {
	switch c {
	case ClassLocal:
		return FloorLocal
	case ClassEmbedded:
		return FloorEmbedded
	default:
		return FloorRemote
	}
}

// Ceiling returns the maximum adaptive budget for the class (ADR-071 D8).
func (c Class) Ceiling() time.Duration {
	switch c {
	case ClassLocal:
		return CeilingLocal
	case ClassEmbedded:
		return CeilingEmbedded
	default:
		return CeilingRemote
	}
}

// ParseClass maps a config `timeout_class` value to a Class. The second result
// is false for anything that is not one of the three class names — callers
// treat an unparseable override as absent and fall through to inference.
func ParseClass(s string) (Class, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(ClassRemote):
		return ClassRemote, true
	case string(ClassLocal):
		return ClassLocal, true
	case string(ClassEmbedded):
		return ClassEmbedded, true
	}
	return ClassRemote, false
}

// Classify resolves a provider's class (ADR-071 D3). An explicit per-provider
// `timeout_class` override wins over the heuristic — including the name-based
// embedded rule. Otherwise the backend-owned name "embedded" is class embedded
// by definition, a loopback base_url is local, and everything else is remote.
// An unrecognized override value is ignored (treated as absent) rather than
// failing: a typo must not silently change which envelope bounds the budget.
//
// baseURL may be empty (the provider has no resolved base_url yet); an empty
// or unparseable URL is never loopback.
func Classify(providerName, baseURL, timeoutClass string) Class {
	if c, ok := ParseClass(timeoutClass); ok {
		return c
	}
	if providerName == EmbeddedProviderName {
		return ClassEmbedded
	}
	if isLoopbackBaseURL(baseURL) {
		return ClassLocal
	}
	return ClassRemote
}

// isLoopbackBaseURL reports whether the URL targets the loopback interface.
// The exact host set is 127.0.0.1, ::1 and localhost (any case); a URL that
// fails to parse or names no host is not loopback.
func isLoopbackBaseURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	// Hostname strips the brackets from "[::1]:8080", so the bare "::1" form
	// is what reaches the comparison.
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "::1", "127.0.0.1":
		return true
	}
	return false
}
