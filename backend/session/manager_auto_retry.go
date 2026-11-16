package session

import (
	"errors"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/sp4rk/llm"
)

// isBudgetExpiryCause reports whether the *llm.Error's wrapped chain carries
// the llmbudget transport's own-budget-expiry wrapper — via the EXPORTED
// signature predicate (`llmbudget.IsOwnBudgetExpiry`), the same predicate the
// delegation transient-class seam uses; there is no local re-statement of the
// wrapper's message here (ADR-128: one signature, shared predicate). A
// provider-side timeout (no wrapper) does not match, so the two timeout
// senses stay distinguishable (ADR-128 commitment 4).
func isBudgetExpiryCause(llmErr *llm.Error) bool {
	return llmErr != nil && llmbudget.IsOwnBudgetExpiry(llmErr.Err)
}

// isAutoRetryableNetworkCause reports whether the error carries the
// connection-level (StatusCode 0) subclass of the transient provider class —
// the discriminator that picks the GLOBAL network interval over the
// per-provider rate-limit knob in maybeAutoRetryAt. The subclass predicate is
// core's (`core.IsTransientNetworkClassError`); nothing about the taxonomy is
// re-decided in this layer.
func isAutoRetryableNetworkCause(llmErr *llm.Error) bool {
	return core.IsTransientNetworkClassError(llmErr)
}

// isAutoRetryableCause reports whether an *llm.Error qualifies for automatic
// resend. The transient provider class is defined ONCE —
// core.IsTransientProviderError (the ADR-065/ADR-128 taxonomy: a terminal
// *llm.Error with HTTP 429/529, the rate_limit/overloaded ErrKind, or at
// StatusCode 0 a wrapped chain classifying dns/connect/reset/timeout under
// llm.TransportKind) — and this seam consumes it, adding only the interval
// resolution and deadline stamping (maybeAutoRetryAt below). The explicit
// budget-expiry guard restates the carve-out at this seam, as
// llmbudget.IsOwnBudgetExpiry's contract asks: a budget death escalates
// in-flight (ADR-071's ×2 ladder) and never arms a re-send, even though its
// chained context.DeadlineExceeded would satisfy the timeout class — while a
// provider-side timeout (no wrapper) still arms the deadline.
func isAutoRetryableCause(llmErr *llm.Error) bool {
	if isBudgetExpiryCause(llmErr) {
		return false
	}
	return core.IsTransientProviderError(llmErr)
}

// SetAutoRetryResolver sets the callback resolving a provider name (the
// logical config key carried by *llm.Error.Provider) to its rate-limit class
// auto-resend interval in seconds; a nil resolver or a 0 answer (a provider
// without a configured interval) keeps the RATE-LIMIT class off. Fixed
// providers resolve to their own configured interval too (ADR-127), while the
// connection-level class never consults this resolver at all — it rides the
// global network interval (SetNetworkAutoRetryResolver, ADR-128). NO backend
// timer exists (ADR-065, specs/domains/llm-providers.md): the UI owns the
// countdown and the resume.
func (m *Manager) SetAutoRetryResolver(fn func(provider string) int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRetryResolver = fn
}

// SetNetworkAutoRetryResolver sets the callback resolving the GLOBAL
// network-class auto-resend interval in seconds (llm.network_auto_retry_
// seconds, ADR-128); 0 is the kill-switch. The network class applies to every
// provider — the fixed ones included — so unlike the rate-limit class there
// is no per-provider argument. Same atomic-snapshot seam and live-only
// contract as SetAutoRetryResolver.
func (m *Manager) SetNetworkAutoRetryResolver(fn func() int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networkAutoRetryResolver = fn
}

// autoRetryInterval resolves the rate-limit class auto-resend interval for
// the provider under the manager read lock. Returns 0 when no resolver is
// wired.
func (m *Manager) autoRetryInterval(provider string) int {
	m.mu.RLock()
	fn := m.autoRetryResolver
	m.mu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn(provider)
}

// networkAutoRetryInterval resolves the global network-class interval under
// the manager read lock. Returns 0 when no resolver is wired.
func (m *Manager) networkAutoRetryInterval() int {
	m.mu.RLock()
	fn := m.networkAutoRetryResolver
	m.mu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn()
}

// maybeAutoRetryAt classifies the terminal failure cause and returns the
// auto-resend deadline as unix seconds, or 0 when it does not qualify. The
// class decides the knob (ADR-128): a connection-level failure rides the
// global network interval, the rate-limit class keeps the per-provider knob
// (fixed providers resolve 0 — no deadline). The caller stamps the deadline
// into the task_failed_resumable payload; nothing is scheduled on the backend
// (specs/domains/llm-providers.md, ADR-065, ADR-128).
func (m *Manager) maybeAutoRetryAt(cause error) int64 {
	if cause == nil {
		return 0
	}
	var llmErr *llm.Error
	if !errors.As(cause, &llmErr) {
		return 0
	}
	if !isAutoRetryableCause(llmErr) {
		return 0
	}
	interval := m.autoRetryInterval(llmErr.Provider)
	if isAutoRetryableNetworkCause(llmErr) {
		interval = m.networkAutoRetryInterval()
	}
	if interval <= 0 {
		return 0
	}
	return time.Now().Add(time.Duration(interval) * time.Second).Unix()
}
