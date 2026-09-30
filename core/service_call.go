package core

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// serviceMeteringCaller wraps a oneshot.Caller to count attempts and transport
// failures for the per-kind service metrics. It is transparent: it forwards the
// request and response unchanged.
type serviceMeteringCaller struct {
	inner           oneshot.Caller
	attempts        int
	transportErrors int
}

// Call implements oneshot.Caller. Every invocation is one attempt (the first
// try or a nudge re-send); a non-nil error is a transport failure.
func (c *serviceMeteringCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.attempts++
	resp, err := c.inner.Call(ctx, req)
	if err != nil {
		c.transportErrors++
	}
	return resp, err
}

// serviceCall is the single instrumented entry point for c0wrk's auxiliary
// one-shot LLM calls (Issue #64): session title, commit message, the prompt
// optimizer's extract/rewrite steps, and compaction summarization.
//
// It delegates the call itself — the request, the parse-failure nudge loop, and
// the failure policy — to the sp4rk oneshot client, so no helper owns its own
// retry loop, and adds the one piece of shared observability the client cannot
// own across kinds:
//
//   - per-kind counters/latency (ServiceMetrics), and
//   - one consistent structured log record per call, carrying service_kind,
//     model, outcome, attempts and duration.
//
// serviceCall is the sole log emitter for this record: opts.Logger is cleared
// here, so the client's own logging is suppressed — the fields never drift
// between helpers, and c0wrk and the client never log the same record twice
// (matching the "one shared client" contract in Issue #64). Domain-level
// diagnostics at the call sites (the commit-message validation warning, the
// per-attempt rewrite warnings) are separate records by design and unaffected.
//
// Transport failures are never retried here (the Router owns provider retry);
// the client's terminal refusal and fallback values pass through unchanged.
func serviceCall[T any](
	ctx context.Context,
	metrics *ServiceMetrics,
	logger *slog.Logger,
	kind ServiceKind,
	model string,
	caller oneshot.Caller,
	req llm.ChatRequest,
	parse oneshot.Parse[T],
	opts oneshot.Options[T],
) (T, error) {
	// Re-assert the client's nil-caller contract here: oneshot.Do refuses a
	// nil caller, but it sees only the serviceMeteringCaller wrapper, so a nil
	// inner caller would panic inside the wrapper's first Call instead of
	// returning an error. Every current call site pre-checks its caller; this
	// guard keeps the contract intact for the next one.
	if caller == nil {
		var zero T
		return zero, errors.New("service call: nil caller")
	}

	// c0wrk, not the client, labels and logs the record: force the kind and
	// suppress the client's own logger so exactly one record is emitted.
	opts.Kind = string(kind)
	opts.Logger = nil

	metered := &serviceMeteringCaller{inner: caller}

	// Wrap the parse so the terminal parse result is observable. It is the only
	// way to tell a genuine success from a fallback value: the client's
	// OnFailureFallback / OnFailureFailSafe policies swallow the parse error and
	// return a nil error alongside the degraded value.
	var lastParseErr error
	wrapParse := func(resp *llm.ChatResponse) (T, error) {
		v, err := parse(resp)
		lastParseErr = err
		return v, err
	}

	start := time.Now()
	result, err := oneshot.Do(ctx, metered, req, wrapParse, opts)
	duration := time.Since(start)

	outcome := classifyServiceOutcome(err, lastParseErr, metered.transportErrors)
	metrics.record(kind, outcome, metered.attempts, duration) // nil-safe
	logServiceCall(logger, kind, model, outcome, metered.attempts, duration)

	return result, err
}

// classifyServiceOutcome maps the client result onto a ServiceOutcome. A nil
// error with a non-nil lastParseErr means the client substituted a fallback
// value; a non-nil error whose transport failed is a transport error, else a
// terminal refusal.
func classifyServiceOutcome(err, lastParseErr error, transportErrors int) ServiceOutcome {
	switch {
	case err == nil && lastParseErr != nil:
		return ServiceOutcomeFallback
	case err == nil:
		return ServiceOutcomeOK
	case transportErrors > 0:
		return ServiceOutcomeTransportError
	default:
		return ServiceOutcomeError
	}
}

// logServiceCall emits the unified per-call record: successes at Debug, every
// degraded outcome (fallback, terminal refusal, transport failure) at Warn. It
// deliberately carries no raw model output and no provider error text — both
// can echo untrusted input — only the low-cardinality classifier fields.
//
// The optimize-extract fallback logs at Warn even though it is a normal
// by-design outcome (the historical behavior returns the original prompt after
// the nudge loop): surfacing every degraded call is a deliberate spec decision,
// to be revisited — lowered to Info for that kind+outcome pair — only if
// release telemetry shows regular noise.
func logServiceCall(logger *slog.Logger, kind ServiceKind, model string, outcome ServiceOutcome, attempts int, duration time.Duration) {
	if logger == nil {
		return
	}
	attrs := []any{
		"service_kind", string(kind),
		"model", model,
		"outcome", string(outcome),
		"attempts", attempts,
		"duration", duration,
	}
	if outcome == ServiceOutcomeOK {
		logger.Debug("service call", attrs...)
		return
	}
	logger.Warn("service call", attrs...)
}
