package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/liushuangls/go-anthropic/v2"
)

// AnthropicProviderConfig holds configuration for Anthropic provider.
type AnthropicProviderConfig struct {
	APIKey string
}

// AnthropicProvider implements LLMProvider using Anthropic's Claude API.
type AnthropicProvider struct {
	client *anthropic.Client
	name   string
}

// NewAnthropicProvider creates a new Anthropic provider with the given configuration.
func NewAnthropicProvider(cfg AnthropicProviderConfig) (*AnthropicProvider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: API key is required")
	}

	client := anthropic.NewClient(cfg.APIKey)

	return &AnthropicProvider{
		client: client,
		name:   "anthropic",
	}, nil
}

// Name returns the provider name.
func (p *AnthropicProvider) Name() string {
	return p.name
}

// ChatCompletion sends a request and returns the full response.
func (p *AnthropicProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	anthropicReq, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: failed to build request: %w", err)
	}

	resp, err := p.client.CreateMessages(ctx, *anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: API error: %w", err)
	}

	return p.parseResponse(resp)
}

// StreamChatCompletion sends a request and returns a channel of streaming chunks.
func (p *AnthropicProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	anthropicReq, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: failed to build request: %w", err)
	}

	chunks := make(chan ChatChunk)
	errChan := make(chan error, 1)

	// Track current tool use for accumulating input JSON
	var currentToolID string
	var currentToolName string
	var currentToolInput []byte

	streamReq := anthropic.MessagesStreamRequest{
		MessagesRequest: *anthropicReq,

		OnContentBlockStart: func(data anthropic.MessagesEventContentBlockStartData) {
			if data.ContentBlock.Type == anthropic.MessagesContentTypeToolUse {
				currentToolID = data.ContentBlock.ID
				currentToolName = data.ContentBlock.Name
				currentToolInput = nil
			}
		},

		OnContentBlockDelta: func(data anthropic.MessagesEventContentBlockDeltaData) {
			switch data.Delta.Type {
			case anthropic.MessagesContentTypeText:
				chunks <- ChatChunk{
					Delta: data.Delta.GetText(),
				}
			case anthropic.MessagesContentTypeToolUse, anthropic.MessagesContentTypeInputJsonDelta:
				// Accumulate tool input JSON
				if data.Delta.PartialJson != nil {
					currentToolInput = append(currentToolInput, []byte(*data.Delta.PartialJson)...)
				}
			}
		},

		OnContentBlockStop: func(data anthropic.MessagesEventContentBlockStopData, content anthropic.MessageContent) {
			// If we were accumulating a tool call, emit it now
			if currentToolID != "" {
				chunks <- ChatChunk{
					ToolCall: &ToolCall{
						ID:    currentToolID,
						Name:  currentToolName,
						Input: json.RawMessage(currentToolInput),
					},
				}
				currentToolID = ""
				currentToolName = ""
				currentToolInput = nil
			}
		},

		OnMessageDelta: func(data anthropic.MessagesEventMessageDeltaData) {
			if data.Delta.StopReason != "" {
				chunks <- ChatChunk{
					StopReason: normalizeStopReason(string(data.Delta.StopReason)),
				}
			}
		},

		OnError: func(errResp anthropic.ErrorResponse) {
			errChan <- fmt.Errorf("anthropic stream error: %s", errResp.Error.Message)
		},
	}

	go func() {
		defer close(chunks)
		_, err := p.client.CreateMessagesStream(ctx, streamReq)
		if err != nil {
			// Channel might already be closed, ignore send errors
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	return chunks, nil
}

// buildRequest converts ChatRequest to anthropic.MessagesRequest.
func (p *AnthropicProvider) buildRequest(req ChatRequest) (*anthropic.MessagesRequest, error) {
	// Extract system prompt from messages
	var systemPrompt string
	var messages []anthropic.Message

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += msg.Content
			continue
		}

		anthropicMsg, err := p.convertMessage(msg)
		if err != nil {
			return nil, err
		}
		messages = append(messages, anthropicMsg)
	}

	anthropicReq := &anthropic.MessagesRequest{
		Model:     anthropic.Model(req.Model),
		Messages:  messages,
		MaxTokens: req.MaxTokens,
	}

	if systemPrompt != "" {
		anthropicReq.System = systemPrompt
	}

	if req.Temperature != nil {
		temp := float32(*req.Temperature)
		anthropicReq.Temperature = &temp
	}

	// Convert tools
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolDefinition, len(req.Tools))
		for i, tool := range req.Tools {
			tools[i] = anthropic.ToolDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}
		}
		anthropicReq.Tools = tools
	}

	return anthropicReq, nil
}

// convertMessage converts a Message to anthropic.Message.
func (p *AnthropicProvider) convertMessage(msg Message) (anthropic.Message, error) {
	switch msg.Role {
	case "user":
		return anthropic.Message{
			Role: anthropic.RoleUser,
			Content: []anthropic.MessageContent{
				anthropic.NewTextMessageContent(msg.Content),
			},
		}, nil

	case "assistant":
		var content []anthropic.MessageContent

		// Add text content if present
		if msg.Content != "" {
			content = append(content, anthropic.NewTextMessageContent(msg.Content))
		}

		// Add tool use blocks for tool calls
		for _, tc := range msg.ToolCalls {
			content = append(content, anthropic.NewToolUseMessageContent(tc.ID, tc.Name, tc.Input))
		}

		return anthropic.Message{
			Role:    anthropic.RoleAssistant,
			Content: content,
		}, nil

	case "tool":
		return anthropic.Message{
			Role: anthropic.RoleUser,
			Content: []anthropic.MessageContent{
				anthropic.NewToolResultMessageContent(msg.ToolCallID, msg.Content, false),
			},
		}, nil

	default:
		return anthropic.Message{}, fmt.Errorf("unsupported message role: %s", msg.Role)
	}
}

// parseResponse converts anthropic.MessagesResponse to ChatResponse.
func (p *AnthropicProvider) parseResponse(resp anthropic.MessagesResponse) (*ChatResponse, error) {
	message := Message{
		Role: "assistant",
	}

	// Process content blocks
	for _, block := range resp.Content {
		switch block.Type {
		case anthropic.MessagesContentTypeText:
			if message.Content != "" {
				message.Content += "\n"
			}
			message.Content += block.GetText()

		case anthropic.MessagesContentTypeToolUse:
			if block.MessageContentToolUse != nil {
				message.ToolCalls = append(message.ToolCalls, ToolCall{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
			}
		}
	}

	return &ChatResponse{
		Message:    message,
		StopReason: normalizeStopReason(string(resp.StopReason)),
		Usage: TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

// normalizeStopReason converts Anthropic stop reasons to our standard format.
func normalizeStopReason(reason string) string {
	// Anthropic uses these values which match our format:
	// "end_turn", "tool_use", "max_tokens"
	return reason
}
