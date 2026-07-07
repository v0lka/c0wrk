package agent

import (
	"context"
	"log/slog"

	"github.com/v0lka/sp4rk/llm"
)

// loggingCaller wraps an LLMCaller to log request/response details via a
// session-specific logger. Logging is performed at DEBUG level so it can be
// toggled via the log-level configuration without any code changes.
type loggingCaller struct {
	inner    LLMCaller
	provider string
	logger   *slog.Logger
}

// NewLoggingLLMCaller wraps an LLMCaller so that every Call logs request details
// and per-response token usage via the given logger.
//
// provider is the logical provider name (e.g. "openai", "anthropic") used
// in the log record for identification.
// If logger is nil, returns inner unchanged.
func NewLoggingLLMCaller(inner LLMCaller, provider string, logger *slog.Logger) LLMCaller {
	if logger == nil {
		return inner
	}
	return &loggingCaller{inner: inner, provider: provider, logger: logger}
}

// Call delegates to the wrapped LLMCaller, logging the request before the call
// and token usage / errors after. Streaming is not supported.
func (l *loggingCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	l.logger.Debug("llm: request",
		"provider", l.provider,
		"model", req.Model,
		"messageCount", len(req.Messages),
		"toolCount", len(req.Tools),
	)

	resp, err := l.inner.Call(ctx, req)
	if err != nil {
		l.logger.Debug("llm: call failed",
			"provider", l.provider,
			"error", err,
		)
		return resp, err
	}
	if resp != nil {
		l.logger.Debug("llm: token usage",
			"provider", l.provider,
			"model", req.Model,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", resp.Usage.InputTokens+resp.Usage.OutputTokens,
			"stopReason", resp.StopReason,
			"toolCallCount", len(resp.Message.ToolCalls),
		)
	}
	return resp, err
}
