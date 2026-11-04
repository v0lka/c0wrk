package llmbudget

import (
	"net/url"
	"strings"
	"time"
)

// Class is an LLM provider's timeout class (ADR-071 D3): it selects the
// floor/ceiling envelope bounding a model's budget and the ladder's fallback.
type Class string

const (
	// ClassRemote covers hosted providers — neither loopback nor embedded.
	ClassRemote Class = "remote"

	// ClassLocal covers providers whose resolved base_url targets loopback
	// (openai_compatible endpoints such as Ollama or LM Studio).
	ClassLocal Class = "local"

	// ClassEmbedded covers the backend-owned `embedded` provider by reserved name.
	ClassEmbedded Class = "embedded"
)

// EmbeddedProviderName is the backend-owned provider name that classifies as
// embedded by definition (ADR-071 D3); c0wrk reserves it against user-created providers.
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

// ParseClass maps a config `timeout_class` value to a Class; ok is false for
// anything not one of the three names (callers treat it as absent → inference).
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

// Classify resolves a provider's class (ADR-071 D3): an explicit
// `timeout_class` override wins — even over the name-based embedded rule —
// else the reserved name "embedded" is embedded, loopback local, rest remote.
// An unrecognized override counts as absent (the config load gate rejects
// invalid values; fail-soft only). baseURL may be empty (never loopback).
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

// isLoopbackBaseURL reports whether the URL targets loopback: exactly
// 127.0.0.1, ::1 and localhost (any case); unparseable or hostless is not.
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
