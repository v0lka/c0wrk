package backend

import (
	"context"
	"errors"
	"strings"
)

// OptimizePrompt rewrites the user's prompt to be more specific and actionable
// for the AI coding agent, optionally enriched with codebase context from the
// vector index.
func (f *FrontendAPI) OptimizePrompt(prompt string) (*OptimizePromptResponse, error) {
	b := f.builder()
	if b == nil {
		return nil, errors.New("application not initialized")
	}

	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return nil, errors.New("prompt is empty")
	}

	// The embedded model must be resident BEFORE this request's budget starts:
	// a cold weight load takes longer than the service timeout, and charging it
	// to the request would fail the call instead of serving it. A no-op for
	// every other provider.
	if err := f.ensureEmbeddedReadyForLLMRequest(f.ctx()); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(f.ctx(), f.serviceLLMTimeout())
	defer cancel()

	result, err := b.OptimizePrompt(ctx, trimmed)
	if err != nil {
		return nil, err
	}

	return &OptimizePromptResponse{
		OptimizedPrompt: result.OptimizedPrompt,
		Keywords:        result.Keywords,
		UsedContext:     result.UsedContext,
	}, nil
}
