// Package llm provides LLM provider abstractions, model registry, and routing for multi-provider inference.
package llm

import "context"

// Caller can make a single LLM call. Structurally identical to agent.LLMCaller
// to ensure duck-typing compatibility without circular imports.
type Caller interface {
	Call(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// Provider — unified interface for all LLM providers.
// Implementations map ChatRequest/ChatResponse to SDK-specific types.
type Provider interface {
	// ChatCompletion sends a request and returns the full response.
	ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// Name returns the provider name for logging (e.g., "openai", "anthropic").
	Name() string
}
