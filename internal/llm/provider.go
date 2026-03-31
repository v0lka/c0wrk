package llm

import "context"

// LLMProvider — unified interface for all LLM providers.
// Implementations map ChatRequest/ChatResponse to SDK-specific types.
type LLMProvider interface {
	// ChatCompletion sends a request and returns the full response.
	ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// StreamChatCompletion sends a request and returns a channel of streaming chunks.
	StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)

	// Name returns the provider name for logging (e.g., "openai", "anthropic", "gemini").
	Name() string
}
