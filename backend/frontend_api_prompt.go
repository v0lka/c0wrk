package backend

import (
	"context"
	"errors"
	"strings"
)

// OptimizePrompt rewrites the user's prompt to be more specific and
// actionable, optionally enriched with vector-index codebase context.
func (f *FrontendAPI) OptimizePrompt(prompt string) (*OptimizePromptResponse, error) {
	b := f.builder()
	if b == nil {
		return nil, errors.New("application not initialized")
	}

	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return nil, errors.New("prompt is empty")
	}

	// The embedded model must be resident BEFORE this request's budget starts
	// (ADR-066 D13): a cold load is never charged to the request's own
	// timeout, so the worst case is serviceLLMTimeout + the gate's load wait
	// (derived on the constant itself). No-op for other providers; the
	// frontend keeps a spinner and no client-side timeout aborts it. See
	// specs/domains/embedded-llm.md.
	if err := f.serviceEmbeddedGateInteractive(f.ctx()); err != nil {
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
