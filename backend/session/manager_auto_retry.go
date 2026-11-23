package session

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	oai "github.com/openai/openai-go"

	"github.com/v0lka/c0wrk/backend/config"
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
// the discriminator that picks the GLOBAL network switch over the
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

// networkAutoRetryScheduleSeconds is the code-owned growing re-send schedule
// for the network class (ADR-134, amending ADR-128 commitment 3): the n-th
// consecutive auto re-send of one task-failure episode waits
// schedule[min(n, len-1)] seconds. Deliberately not configurable — a schedule
// is policy, not tuning, exactly like the class taxonomy ADR-065/ADR-128
// already froze into code — so the network knob's long-standing role
// shrinks to the switch: any positive value arms THIS schedule, 0 stays the
// kill-switch. The steps grow so a healing outage is probed quickly at first
// and a sustained one is re-sent no faster than the old ADR-128 cadence:
// 15 s → 30 s → 60 s, capped at the ADR-128 default of 90 s, which the third
// re-send reaches and every later one holds. The re-arms themselves stay
// unlimited (ADR-065's contract, verbatim — the stop is the kill switch, a
// manual click, or closing the view).
var networkAutoRetryScheduleSeconds = [4]int{15, 30, 60, 90}

// retryAfterSeconds extracts the provider-signposted Retry-After window from
// a rate-limit failure (ADR-134). The openai-compatible transport is the only
// one whose error type carries the HTTP response (openai-go's *Error keeps a
// *http.Response in the errors.As chain sp4rk preserves by wrapping with %w),
// so it is the only transport whose header is reachable here — the anthropic
// and google transports carry no response on their error types and simply
// report absent, which is the documented fallback to the configured interval.
// Both RFC 7231 forms parse: delay-seconds and HTTP-date (a date rounds up to
// whole seconds). An absent, empty, unparseable, or non-positive value (a
// past date included) reports absent — "retry now" is not a window this
// seam can stamp, so the configured interval answers instead. The raw window
// is returned; the caller clamps it.
func retryAfterSeconds(cause error) (int, bool) {
	var oaiErr *oai.Error
	if !errors.As(cause, &oaiErr) || oaiErr.Response == nil || oaiErr.Response.Header == nil {
		return 0, false
	}
	value := strings.TrimSpace(oaiErr.Response.Header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return secs, true
	}
	if date, err := http.ParseTime(value); err == nil {
		secs := int(time.Until(date).Seconds()) + 1
		if secs <= 0 {
			return 0, false
		}
		return secs, true
	}
	return 0, false
}

// SetAutoRetryResolver sets the callback resolving a provider name (the
// logical config key carried by *llm.Error.Provider) to its rate-limit class
// auto-resend interval in seconds; a nil resolver or a 0 answer (a provider
// without a configured interval) keeps the RATE-LIMIT class off — including
// when the provider sent a Retry-After header (ADR-134: a header refines the
// window of an ARMED class, it never arms a disabled one). Fixed providers
// resolve to their own configured interval too (ADR-127), while the
// connection-level class never consults this resolver at all — it rides the
// global network switch (SetNetworkAutoRetryResolver, ADR-128). NO backend
// timer exists (ADR-065, specs/domains/llm-providers.md): the UI owns the
// countdown and the resume.
func (m *Manager) SetAutoRetryResolver(fn func(provider string) int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRetryResolver = fn
}

// SetNetworkAutoRetryResolver sets the callback resolving the GLOBAL
// network-class switch (llm.network_auto_retry_seconds, ADR-128): any
// positive answer arms the class under the code-owned growing schedule
// (networkAutoRetryScheduleSeconds, ADR-134 — the configured value itself no
// longer sets the interval), and 0 stays the kill-switch. The network class
// applies to every provider — the fixed ones included — so unlike the
// rate-limit class there is no per-provider argument. Same atomic-snapshot
// seam and live-only contract as SetAutoRetryResolver.
func (m *Manager) SetNetworkAutoRetryResolver(fn func() int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.networkAutoRetryResolver = fn
}

// rearmKey builds the task-failure-episode key of the re-send counter:
// session-scoped so per-session task IDs cannot collide across sessions.
func rearmKey(sessionID, taskID string) string {
	return sessionID + "\x00" + taskID
}

// endTaskAutoRetryEpisode clears the re-send counter of one task-failure
// episode (ADR-134): the ladder is per episode, so a terminal resolution of
// the resumable banner — any decision but "resumed" (cancel, mode-takeover
// abandonment) — starts the next failure of the same task back at the first
// schedule step. A "resumed" resolution deliberately keeps the counter: the
// UI's auto fire and a manual resume are indistinguishable at this seam, and
// the ladder reflects the accumulated transient-failure pressure of the
// episode, not of one attempt. Live-only state, cleared silently.
func (m *Manager) endTaskAutoRetryEpisode(sessionID, taskID string) {
	if taskID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.autoRetryRearms, rearmKey(sessionID, taskID))
}

// endSessionAutoRetryEpisodes clears every re-send counter of one session
// (ADR-134): called when the session's task completes successfully — a
// terminal state the ladder resets on — so a later failure of any task in
// the session starts fresh at the first schedule step.
func (m *Manager) endSessionAutoRetryEpisodes(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.autoRetryRearms {
		if strings.HasPrefix(key, sessionID+"\x00") {
			delete(m.autoRetryRearms, key)
		}
	}
}

// maybeAutoRetryAt classifies the terminal failure cause and returns the
// auto-resend deadline as unix seconds, or 0 when it does not qualify. The
// class decides the knob (ADR-128): a connection-level failure rides the
// global network switch, the rate-limit class keeps the per-provider knob
// (fixed providers resolve 0 — no deadline). ADR-134 makes the resolution
// re-arm-aware: the network class waits schedule[min(n, cap)] seconds, where
// n is the episode's prior stamped re-sends, and a rate-limit failure
// carrying the provider's Retry-After header waits the signposted window
// (clamped to config.MaxAutoRetrySeconds, the same inclusive bound validate()
// enforces) instead of the configured interval — which stays the fallback
// whenever the header is absent or unusable. The kill switches stay
// authoritative in both classes: a configured 0 never arms, on no provider,
// for no header. The counter behind n is per task-failure episode (keyed by
// session+task, live-only in memory), advances only when a deadline is
// actually stamped, and resets when the episode ends (endTaskAutoRetryEpisode
// / endSessionAutoRetryEpisodes / an app restart). The caller stamps the
// deadline into the task_failed_resumable payload; nothing is scheduled on
// the backend (specs/domains/llm-providers.md, ADR-065, ADR-128, ADR-134).
func (m *Manager) maybeAutoRetryAt(sessionID, taskID string, cause error) int64 {
	if cause == nil || taskID == "" {
		return 0
	}
	var llmErr *llm.Error
	if !errors.As(cause, &llmErr) {
		return 0
	}
	if !isAutoRetryableCause(llmErr) {
		return 0
	}
	network := isAutoRetryableNetworkCause(llmErr)

	m.mu.RLock()
	providerFn, networkFn := m.autoRetryResolver, m.networkAutoRetryResolver
	m.mu.RUnlock()

	configured := 0
	if network {
		if networkFn != nil {
			configured = networkFn()
		}
	} else if providerFn != nil {
		configured = providerFn(llmErr.Provider)
	}
	if configured <= 0 {
		return 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	key := rearmKey(sessionID, taskID)
	if m.autoRetryRearms != nil {
		n = m.autoRetryRearms[key]
	}
	interval := configured
	if network {
		interval = networkAutoRetryScheduleSeconds[min(n, len(networkAutoRetryScheduleSeconds)-1)]
	} else if ra, ok := retryAfterSeconds(cause); ok {
		interval = min(ra, config.MaxAutoRetrySeconds())
	}
	if m.autoRetryRearms == nil {
		m.autoRetryRearms = make(map[string]int)
	}
	m.autoRetryRearms[key] = n + 1
	return time.Now().Add(time.Duration(interval) * time.Second).Unix()
}
