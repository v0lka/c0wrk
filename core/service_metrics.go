package core

import (
	"sync"
	"time"
)

// ServiceKind identifies a one-shot auxiliary LLM service call: every member
// rides the shared oneshot service client with per-kind telemetry —
// specs/domains/llm-providers.md (per-kind telemetry).
type ServiceKind string

const (
	ServiceKindTitle             ServiceKind = "title"                // session-title generation
	ServiceKindCommitMessage     ServiceKind = "commit_message"       // commit-message generation
	ServiceKindOptimizeExtract   ServiceKind = "optimize_extract"     // prompt optimizer: translate/extract step
	ServiceKindOptimizeRewrite   ServiceKind = "optimize_rewrite"     // prompt optimizer: rewrite step
	ServiceKindCompactionSummary ServiceKind = "compaction_summarize" // context-compaction summarization
	ServiceKindGoalSuggest       ServiceKind = "goal_suggest"         // post-routing goal-mode suitability classifier (ADR-086)
	ServiceKindPromptSuggest     ServiceKind = "prompt_suggest"       // chat-input follow-up prompt suggestions (ADR-088)
)

// ServiceOutcome classifies the terminal result of a service call.
type ServiceOutcome string

const (
	ServiceOutcomeOK             ServiceOutcome = "ok"              // first-or-retried parse success
	ServiceOutcomeFallback       ServiceOutcome = "fallback"        // nudge loop exhausted, degraded value returned
	ServiceOutcomeError          ServiceOutcome = "error"           // terminal refusal or pre-call cancellation, non-transport
	ServiceOutcomeTransportError ServiceOutcome = "transport_error" // the underlying LLM call failed (the Router owns retry)
)

// ServiceKindMetrics is the aggregate telemetry for one ServiceKind.
//
// Retries = Attempts - Calls holds only for calls that reached the wire: a
// call cancelled before its first attempt records Calls=1 with Attempts=0, so
// consumers must not derive Retries unconditionally.
type ServiceKindMetrics struct {
	Calls           int64         // serviceCall invocations
	Attempts        int64         // underlying LLM calls (first try + nudges)
	Retries         int64         // nudge re-sends; see the invariant caveat above
	OK              int64         // terminal parse success
	Fallback        int64         // nudge loop exhausted, degraded value returned
	Errors          int64         // terminal refusal (non-transport)
	TransportErrors int64         // calls whose transport failed
	TotalDuration   time.Duration // summed wall-clock across Calls
	MaxDuration     time.Duration // slowest single call
}

// ServiceMetrics is the in-process collector for the one-shot service calls'
// per-kind counters and latency; content-free (no prompts, no model output),
// so it is safe to log or expose — specs/domains/llm-providers.md. A nil
// *ServiceMetrics is valid: every method is a no-op on it.
type ServiceMetrics struct {
	mu    sync.Mutex
	kinds map[ServiceKind]*ServiceKindMetrics
}

// newServiceMetrics creates an empty collector.
func newServiceMetrics() *ServiceMetrics {
	return &ServiceMetrics{kinds: make(map[ServiceKind]*ServiceKindMetrics)}
}

// record folds one completed service call into the per-kind aggregate;
// attempts counts underlying LLM calls (>= 1 for a call that reached the wire).
func (m *ServiceMetrics) record(kind ServiceKind, outcome ServiceOutcome, attempts int, dur time.Duration) {
	if m == nil {
		return
	}
	if attempts < 0 {
		attempts = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kinds == nil {
		m.kinds = make(map[ServiceKind]*ServiceKindMetrics)
	}
	km := m.kinds[kind]
	if km == nil {
		km = &ServiceKindMetrics{}
		m.kinds[kind] = km
	}
	km.Calls++
	km.Attempts += int64(attempts)
	if attempts > 0 {
		km.Retries += int64(attempts - 1)
	}
	switch outcome {
	case ServiceOutcomeOK:
		km.OK++
	case ServiceOutcomeFallback:
		km.Fallback++
	case ServiceOutcomeTransportError:
		km.TransportErrors++
	default:
		km.Errors++
	}
	km.TotalDuration += dur
	if dur > km.MaxDuration {
		km.MaxDuration = dur
	}
}

// Snapshot returns a copy of the per-kind aggregates; kinds with no calls are
// absent.
func (m *ServiceMetrics) Snapshot() map[ServiceKind]ServiceKindMetrics {
	out := make(map[ServiceKind]ServiceKindMetrics)
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.kinds {
		out[k] = *v
	}
	return out
}
