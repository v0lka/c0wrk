package session

import (
	"errors"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// autoRetryStatusCodes lists the HTTP status codes whose LLM failures make a
// failed task eligible for automatic resend (ADR-065): rate limiting (429)
// and transient provider overload (529 — Anthropic-specific, mapped by
// errTypeForStatus). The list is deliberately backend-local and unexported —
// surfacing it as a user-facing (frontend/config) knob is a future
// extension; today the only per-provider tuning is auto_retry_seconds on
// compatible providers.
var autoRetryStatusCodes = []int{429, 529}

// isAutoRetryableStatus reports whether the HTTP status code carried by an
// *llm.Error qualifies for automatic resend.
func isAutoRetryableStatus(code int) bool {
	for _, c := range autoRetryStatusCodes {
		if code == c {
			return true
		}
	}
	return false
}

// isAutoRetryableCause reports whether an *llm.Error qualifies for
// automatic resend. Two independent transports carry the retryable class:
// the HTTP status code (openai_compatible, and anthropic gateways that
// return a non-JSON body so the SDK surfaces RequestError) and the
// transport-independent ErrKind classification (anthropic_compatible: the
// SDK parses the JSON error body into APIError, which carries NO status, so
// ErrKind=rate_limit / overloaded derived from the provider's own
// "rate_limit_error" / "overloaded_error" type fields is the only signal).
func isAutoRetryableCause(llmErr *llm.Error) bool {
	if isAutoRetryableStatus(llmErr.StatusCode) {
		return true
	}
	return llmErr.ErrKind == llm.ErrKindRateLimit || llmErr.ErrKind == llm.ErrKindOverloaded
}

// SetAutoRetryResolver sets the callback that resolves a provider name (as
// carried by *llm.Error.Provider — the logical config key of a compatible
// provider) to its configured auto-resend interval in seconds. The backend
// Application wires this to the live LLM config (an immutable snapshot —
// see Application.publishAutoRetryIntervals), so Settings saves take
// effect for the NEXT failure that surfaces a deadline. A nil resolver (or
// a resolver returning 0 — fixed providers have no auto_retry_seconds
// field) disables the auto-resend entirely.
//
// There is NO backend timer (ADR-065): the backend only classifies the
// terminal failure and stamps the deadline into the task_failed_resumable
// payload; the UI owns the countdown and the resume-on-zero (resumeTask).
func (m *Manager) SetAutoRetryResolver(fn func(provider string) int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRetryResolver = fn
}

// autoRetryInterval resolves the auto-resend interval for the provider
// under the manager read lock. Returns 0 when no resolver is wired.
func (m *Manager) autoRetryInterval(provider string) int {
	m.mu.RLock()
	fn := m.autoRetryResolver
	m.mu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn(provider)
}

// maybeAutoRetryAt inspects the terminal failure cause and, when it
// qualifies (a classified *llm.Error with a retryable class — rate limit
// or overload — from a provider whose resolved interval is > 0), returns
// the auto-resend deadline as unix seconds (now + the provider's interval).
// Returns 0 when the cause does not qualify: the banner renders without a
// deadline and the user's manual Resume/Cancel decision is the only path.
// The caller stamps the returned deadline into the task_failed_resumable
// payload; NOTHING is scheduled on the backend — the UI owns the countdown.
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
	if interval <= 0 {
		return 0
	}
	return time.Now().Add(time.Duration(interval) * time.Second).Unix()
}
