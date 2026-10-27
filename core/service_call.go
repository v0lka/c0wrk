package core

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// serviceMeteringCaller wraps a oneshot.Caller to count attempts and
// transport failures for the per-kind service metrics.
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

// serviceCall is the single instrumented entry for c0wrk's auxiliary one-shot
// LLM calls (Issue #64): the request, nudge loop and failure policy belong to
// the sp4rk oneshot client; the wrapper adds per-kind metrics (ServiceMetrics)
// and the ONE unified log record — sole emitter (opts.Logger cleared here).
// See specs/domains/llm-providers.md (service calls).
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
	// oneshot.Do refuses a nil caller but sees only this wrapper — a nil inner
	// caller would panic in its first Call; the guard keeps the contract intact.
	if caller == nil {
		var zero T
		return zero, errors.New("service call: nil caller")
	}

	opts.Kind = string(kind)
	opts.Logger = nil

	metered := &serviceMeteringCaller{inner: caller}

	// Wrap the parse so the terminal parse result is observable — the client's
	// fallback policies swallow the parse error (nil error + degraded value).
	var lastParseErr error
	wrapParse := func(resp *llm.ChatResponse) (T, error) {
		v, err := parse(resp)
		lastParseErr = err
		return v, err
	}

	start := time.Now()
	req.MaxTokens = serviceOutputTokenBudget(req.MaxTokens, req.ReasoningEffort)
	result, err := oneshot.Do(ctx, metered, req, wrapParse, opts)
	duration := time.Since(start)

	outcome := classifyServiceOutcome(err, lastParseErr, metered.transportErrors)
	metrics.record(kind, outcome, metered.attempts, duration) // nil-safe
	logServiceCall(logger, kind, model, outcome, metered.attempts, duration)

	return result, err
}

// classifyServiceOutcome maps the client result onto a ServiceOutcome: fallback
// (nil error + failed parse) / ok / transport error / terminal refusal.
// See specs/domains/llm-providers.md (service calls).
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

// logServiceCall emits the unified per-call record — Debug on success, Warn on
// every degraded outcome — carrying only low-cardinality classifier fields:
// no raw output, no provider error text (both echo untrusted input).
// See specs/domains/llm-providers.md (service calls).
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
