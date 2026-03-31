package llm

import (
	"context"
	"encoding/json"
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
		return nil, fmt.Errorf("openai chat completion: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
	}

	choice := resp.Choices[0]
	message := p.convertResponseMessage(choice.Message)
	stopReason := p.mapStopReason(string(choice.FinishReason))

	return &ChatResponse{
		Message:    message,
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
		return nil, fmt.Errorf("openai stream: %w", err)
	}

	chunks := make(chan ChatChunk)

	go func() {
		defer close(chunks)
		defer func() { _ = stream.Close() }()

		// Track tool call state across chunks
		toolCalls := make(map[int]*ToolCall)

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
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

			// Handle tool calls delta
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}

				// Initialize tool call if new
				if _, exists := toolCalls[idx]; !exists {
					toolCalls[idx] = &ToolCall{
						ID:    tc.ID,
						Name:  tc.Function.Name,
						Input: json.RawMessage(""),
					}
				}

				// Accumulate function arguments
				if tc.Function.Arguments != "" {
					existing := string(toolCalls[idx].Input)
					toolCalls[idx].Input = json.RawMessage(existing + tc.Function.Arguments)
				}

				// Update name if provided
				if tc.Function.Name != "" {
					toolCalls[idx].Name = tc.Function.Name
				}

				// Update ID if provided
				if tc.ID != "" {
					toolCalls[idx].ID = tc.ID
				}
			}

			// Handle finish reason
			if choice.FinishReason != "" {
				stopReason := p.mapStopReason(string(choice.FinishReason))

				// If tool_use, send accumulated tool calls
				if stopReason == "tool_use" {
					for i := 0; i < len(toolCalls); i++ {
						if tc, ok := toolCalls[i]; ok {
							chunks <- ChatChunk{ToolCall: tc}
						}
					}
				}

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
	openaiMsg := openai.ChatCompletionMessage{
		Role:    msg.Role,
		Content: msg.Content,
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

// mapStopReason converts OpenAI's finish reason to our standard format.
func (p *OpenAIProvider) mapStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}
