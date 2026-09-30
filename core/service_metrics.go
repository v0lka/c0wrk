package core

import (
	"sync"
	"time"
)

// ServiceKind identifies a one-shot auxiliary LLM service call. Every member of
// this class rides the shared oneshot service client (see service_call.go) and
// carries its own per-kind telemetry (Issue #64).
type ServiceKind string

const (
	// ServiceKindTitle is session-title generation (backend/session/title.go).
	ServiceKindTitle ServiceKind = "title"
	// ServiceKindCommitMessage is commit-message generation
	// (backend/frontend_api_git.go).
	ServiceKindCommitMessage ServiceKind = "commit_message"
	// ServiceKindOptimizeExtract is the prompt optimizer's translate/extract step.
	ServiceKindOptimizeExtract ServiceKind = "optimize_extract"
	// ServiceKindOptimizeRewrite is the prompt optimizer's rewrite step.
	ServiceKindOptimizeRewrite ServiceKind = "optimize_rewrite"
	// ServiceKindCompactionSummary is context-compaction summarization.
	ServiceKindCompactionSummary ServiceKind = "compaction_summarize"
)

// ServiceOutcome classifies the terminal result of a service call.
type ServiceOutcome string

const (
	// ServiceOutcomeOK is a first-or-retried parse success.
	ServiceOutcomeOK ServiceOutcome = "ok"
	// ServiceOutcomeFallback is a degraded value returned by the oneshot
	// client after the nudge loop (OnFailureFallback / OnFailureFailSafe).
	ServiceOutcomeFallback ServiceOutcome = "fallback"
	// ServiceOutcomeError is a terminal refusal (or a pre-call context
	// cancellation) that did not come from the transport.
	ServiceOutcomeError ServiceOutcome = "error"
	// ServiceOutcomeTransportError is a failure of the underlying LLM call
	// itself (never retried by the oneshot client — the Router owns retry).
	ServiceOutcomeTransportError ServiceOutcome = "transport_error"
)

// ServiceKindMetrics is the aggregate telemetry for one ServiceKind. All
// counters are monotonic; the durations are wall-clock and cover the whole
// client exchange (including any nudge re-sends).
//
// Retries = Attempts - Calls holds only for calls that reached the wire
// (nudge re-sends); a call cancelled before its first attempt records
// Calls=1 with Attempts=0, so consumers must not derive Retries from the
// difference unconditionally.
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

// ServiceMetrics is the thread-safe, in-process collector for the one-shot
// service calls' per-kind counters and latency (Issue #64). It is
// low-cardinality and content-free — it holds no prompts and no model output —
// so it is safe to log or expose.
//
// A nil *ServiceMetrics is valid and every method is a no-op on it, so call
// sites (and the many tests that build a bare OrchestratorBuilder / Orchestrator
// literal) never have to guard against an unstaged collector.
type ServiceMetrics struct {
	mu    sync.Mutex
	kinds map[ServiceKind]*ServiceKindMetrics
}

// newServiceMetrics creates an empty collector.
func newServiceMetrics() *ServiceMetrics {
	return &ServiceMetrics{kinds: make(map[ServiceKind]*ServiceKindMetrics)}
}

// record folds one completed service call into the per-kind aggregate.
// attempts is the number of underlying LLM calls the client issued (>= 1 for a
// call that reached the wire).
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

// Snapshot returns a stable copy of the per-kind aggregates. Kinds with no
// calls are absent. Safe to call concurrently with record.
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
