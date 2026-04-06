package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sashabaranov/go-openai"
)

// OpenAIProviderConfig contains configuration for OpenAI-compatible providers.
type OpenAIProviderConfig struct {
	Name    string // logical provider name ("openai", "deepseek", "grok", etc.)
	APIKey  string
	BaseURL string // empty = default OpenAI; otherwise custom endpoint
}

// OpenAIProvider implements LLMProvider for OpenAI and compatible APIs.
type OpenAIProvider struct {
	client *openai.Client
	name   string
}

// NewOpenAIProvider creates a new OpenAI provider.
// If BaseURL is empty, uses default OpenAI endpoint.
// If BaseURL is set, uses custom endpoint (DeepSeek, Grok, OpenRouter, Ollama, LM-Studio).
func NewOpenAIProvider(cfg OpenAIProviderConfig) (*OpenAIProvider, error) {
	var client *openai.Client

	if cfg.BaseURL == "" {
		client = openai.NewClient(cfg.APIKey)
	} else {
		config := openai.DefaultConfig(cfg.APIKey)
		config.BaseURL = cfg.BaseURL
		client = openai.NewClientWithConfig(config)
	}

	return &OpenAIProvider{
		client: client,
		name:   cfg.Name,
	}, nil
}

// Name returns the provider name for logging.
func (p *OpenAIProvider) Name() string {
	return p.name
}

// ChatCompletion sends a request and returns the full response.
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	openaiReq := p.buildRequest(req)

	resp, err := p.client.CreateChatCompletion(ctx, openaiReq)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("openai chat completion: %w", err))
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: no choices in response")
	}

	choice := resp.Choices[0]
	message := p.convertResponseMessage(choice.Message)
	stopReason := MapStopReason(string(choice.FinishReason), openAIStopReasonMap)

	// Extract reasoning content (supported by o1/o3 and DeepSeek-reasoner models)
	reasoning := choice.Message.ReasoningContent

	return &ChatResponse{
		Message:    message,
		Reasoning:  reasoning,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}, nil
}

// StreamChatCompletion sends a request and returns a channel of streaming chunks.
func (p *OpenAIProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	openaiReq := p.buildRequest(req)
	openaiReq.Stream = true

	stream, err := p.client.CreateChatCompletionStream(ctx, openaiReq)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("openai stream: %w", err))
	}

	chunks := make(chan ChatChunk)

	go func() {
		defer close(chunks)
		defer func() { _ = stream.Close() }()

		// Track tool call state across chunks
		acc := NewStreamToolCallAccumulator()

		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				// Send error as final chunk with stop reason
				chunks <- ChatChunk{StopReason: "error"}
				return
			}

			if len(resp.Choices) == 0 {
				continue
			}

			choice := resp.Choices[0]
			delta := choice.Delta

			// Handle content delta
			if delta.Content != "" {
				chunks <- ChatChunk{Delta: delta.Content}
			}

			// Handle reasoning content delta (o1/o3, DeepSeek-reasoner)
			if delta.ReasoningContent != "" {
				chunks <- ChatChunk{Reasoning: delta.ReasoningContent}
			}

			// Handle tool calls delta
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc.HandleDelta(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}

			// Handle finish reason
			if choice.FinishReason != "" {
				stopReason := MapStopReason(string(choice.FinishReason), openAIStopReasonMap)
				acc.Emit(chunks)
				chunks <- ChatChunk{StopReason: stopReason}
			}
		}
	}()

	return chunks, nil
}

// buildRequest converts our ChatRequest to OpenAI's request format.
func (p *OpenAIProvider) buildRequest(req ChatRequest) openai.ChatCompletionRequest {
	messages := make([]openai.ChatCompletionMessage, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = p.convertRequestMessage(msg)
	}

	openaiReq := openai.ChatCompletionRequest{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: req.MaxTokens,
	}

	if req.Temperature != nil {
		openaiReq.Temperature = float32(*req.Temperature)
	}

	if len(req.Tools) > 0 {
		openaiReq.Tools = make([]openai.Tool, len(req.Tools))
		for i, tool := range req.Tools {
			openaiReq.Tools[i] = openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
		}
	}

	return openaiReq
}

// convertRequestMessage converts our Message to OpenAI's message format.
func (p *OpenAIProvider) convertRequestMessage(msg Message) openai.ChatCompletionMessage {
	// OpenAI API requires non-empty content for tool-role messages.
	// This is a safety net to prevent 400 errors.
	content := msg.Content
	if msg.Role == "tool" && content == "" {
		content = "(no output)"
	}

	openaiMsg := openai.ChatCompletionMessage{
		Role:    msg.Role,
		Content: content,
	}

	// Handle tool call ID for tool responses
	if msg.ToolCallID != "" {
		openaiMsg.ToolCallID = msg.ToolCallID
	}

	// Handle tool calls for assistant messages
	if len(msg.ToolCalls) > 0 {
		openaiMsg.ToolCalls = make([]openai.ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			openaiMsg.ToolCalls[i] = openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Input),
				},
			}
		}
	}

	return openaiMsg
}

// convertResponseMessage converts OpenAI's message to our Message format.
func (p *OpenAIProvider) convertResponseMessage(msg openai.ChatCompletionMessage) Message {
	result := Message{
		Role:    msg.Role,
		Content: msg.Content,
	}

	if len(msg.ToolCalls) > 0 {
		result.ToolCalls = make([]ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			result.ToolCalls[i] = ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			}
		}
	}

	return result
}

// wrapError maps OpenAI SDK error types to *LLMError.
func (p *OpenAIProvider) wrapError(err error) error {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return WrapProviderError(p.name, apiErr.HTTPStatusCode, err)
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return WrapProviderError(p.name, reqErr.HTTPStatusCode, err)
	}
	// Fallback: check for net errors directly
	return WrapProviderError(p.name, 0, err)
}
