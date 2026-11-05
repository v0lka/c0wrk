package session

import (
	"errors"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// autoRetryStatusCodes: the HTTP statuses making a failed task eligible for
// automatic resend — 429 (rate limit) and 529 (provider overload).
// Deliberately backend-local (specs/domains/llm-providers.md, ADR-065).
var autoRetryStatusCodes = []int{429, 529}

func isAutoRetryableStatus(code int) bool {
	for _, c := range autoRetryStatusCodes {
		if code == c {
			return true
		}
	}
	return false
}

// isAutoRetryableCause reports whether an *llm.Error qualifies for automatic
// resend; the class rides two transports — the HTTP status (429/529) and the
// ErrKind classification the anthropic SDK derives from the JSON error body
// (APIError carries no status) — see specs/domains/llm-providers.md.
func isAutoRetryableCause(llmErr *llm.Error) bool {
	if isAutoRetryableStatus(llmErr.StatusCode) {
		return true
	}
	return llmErr.ErrKind == llm.ErrKindRateLimit || llmErr.ErrKind == llm.ErrKindOverloaded
}

// SetAutoRetryResolver sets the callback resolving a provider name (the
// logical config key carried by *llm.Error.Provider) to its auto-resend
// interval in seconds; a nil resolver or a 0 answer (fixed providers have no
// auto_retry_seconds) disables the auto-resend entirely. NO backend timer
// exists (ADR-065, specs/domains/llm-providers.md): the UI owns the countdown
// and the resume.
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

// maybeAutoRetryAt classifies the terminal failure cause and returns the
// auto-resend deadline as unix seconds, or 0 when it does not qualify. The
// caller stamps the deadline into the task_failed_resumable payload; nothing
// is scheduled on the backend (specs/domains/llm-providers.md, ADR-065).
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
