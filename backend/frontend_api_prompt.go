package backend

import (
	"context"
	"errors"
	"strings"
	"time"
)

// OptimizePrompt rewrites the user's prompt to be more specific and actionable
// for the AI coding agent, optionally enriched with codebase context from the
// vector index.
func (f *FrontendAPI) OptimizePrompt(prompt string) (*OptimizePromptResponse, error) {
	if f.app == nil || f.app.Builder() == nil {
		return nil, errors.New("application not initialized")
	}

	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return nil, errors.New("prompt is empty")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), 2*time.Minute)
	defer cancel()

	result, err := f.app.Builder().OptimizePrompt(ctx, trimmed)
	if err != nil {
		return nil, err
	}

	return &OptimizePromptResponse{
		OptimizedPrompt: result.OptimizedPrompt,
		Keywords:        result.Keywords,
		UsedContext:     result.UsedContext,
	}, nil
}
