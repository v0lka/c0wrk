package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/liushuangls/go-anthropic/v2"
)

// defaultAnthropicMaxTokens is the fallback max_tokens sent to the Anthropic
// Messages API when the caller does not specify one. The Anthropic API
// requires max_tokens to be present and > 0 — omitting it (or sending 0)
// results in a 400 "Missing key ['max_tokens']" error. Several callers
// build ChatRequests without MaxTokens, relying on the provider to supply a
// safe default. 8192 is the minimum OutputLimit across all Anthropic models
// in the built-in registry, so it is accepted by every supported model.
const defaultAnthropicMaxTokens = 8192

// anthropicToolIDPattern matches characters not allowed in Anthropic tool call IDs.
// Anthropic only allows [a-zA-Z0-9_-] in tool call IDs.
var anthropicToolIDPattern = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeAnthropicToolID ensures tool call IDs only contain characters allowed by Anthropic API.
func sanitizeAnthropicToolID(id string) string {
	return anthropicToolIDPattern.ReplaceAllString(id, "_")
}

// AnthropicProviderConfig holds configuration for Anthropic provider.
type AnthropicProviderConfig struct {
	Name       string // logical provider name ("anthropic" default; custom name for Anthropic-compatible providers)
	APIKey     string
	BaseURL    string       // empty = default Anthropic; otherwise custom endpoint (Anthropic-compatible proxy)
	HTTPClient *http.Client // optional proxy-configured HTTP client (nil = default)
}

// AnthropicProvider implements LLM Provider using Anthropic's Claude API.
type AnthropicProvider struct {
	client *anthropic.Client
	name   string
}

// NewAnthropicProvider creates a new Anthropic provider with the given configuration.
//
// If BaseURL is empty, uses the default Anthropic endpoint; otherwise uses the
// custom endpoint (an Anthropic-compatible proxy or gateway).
//
// Note: APIKey is intentionally not validated here. The official Anthropic API
// always requires a key, but local Anthropic-compatible servers may not. An
// empty key for the official endpoint fails at call time with a 401, consistent
// with NewOpenAIProvider's handling of local OpenAI-compatible backends.
func NewAnthropicProvider(cfg AnthropicProviderConfig) (*AnthropicProvider, error) {
	var opts []anthropic.ClientOption
	if cfg.BaseURL != "" {
		opts = append(opts, anthropic.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, anthropic.WithHTTPClient(cfg.HTTPClient))
	}
	client := anthropic.NewClient(cfg.APIKey, opts...)

	name := cfg.Name
	if name == "" {
		name = "anthropic"
	}

	return &AnthropicProvider{
		client: client,
		name:   name,
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
		return nil, p.wrapError(fmt.Errorf("anthropic: API error: %w", err))
	}

	return p.parseResponse(resp)
}

// buildRequest converts ChatRequest to anthropic.MessagesRequest.
func (p *AnthropicProvider) buildRequest(req ChatRequest) (*anthropic.MessagesRequest, error) {
	// Extract system prompt parts from messages (preserves multi-part for caching)
	systemParts, filteredMsgs := ExtractSystemPromptParts(req.Messages)
	var messages []anthropic.Message

	for _, msg := range filteredMsgs {
		// Skip messages with empty content (Anthropic API rejects them)
		if msg.Content == "" && len(msg.ToolCalls) == 0 && msg.ToolCallID == "" {
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
	if anthropicReq.MaxTokens <= 0 {
		anthropicReq.MaxTokens = defaultAnthropicMaxTokens
	}

	// Set system prompt: use MultiSystem with cache control when multiple parts exist
	if len(systemParts) > 1 {
		multiSystem := make([]anthropic.MessageSystemPart, len(systemParts))
		for i, part := range systemParts {
			multiSystem[i] = anthropic.MessageSystemPart{
				Type: "text",
				Text: part,
			}
			// Mark all parts except the last as cacheable (stable content)
			if i < len(systemParts)-1 {
				multiSystem[i].CacheControl = &anthropic.MessageCacheControl{
					Type: anthropic.CacheControlTypeEphemeral,
				}
			}
		}
		anthropicReq.MultiSystem = multiSystem
	} else if len(systemParts) == 1 {
		anthropicReq.System = systemParts[0]
	}

	if req.Temperature != nil {
		temp := float32(*req.Temperature)
		anthropicReq.Temperature = &temp
	}

	// Apply reasoning effort: "On" enables thinking with budget 32000.
	// The Anthropic API requires max_tokens to be strictly greater than
	// thinking.budget_tokens, and the budget itself must be >= 1024. Clamp
	// the budget for small max_tokens values (e.g. the 8192 fallback) and
	// skip thinking entirely when no valid budget fits.
	if req.ReasoningEffort == "On" {
		budget := 32000
		if anthropicReq.MaxTokens <= budget {
			budget = anthropicReq.MaxTokens / 2
		}
		if budget >= 1024 {
			anthropicReq.Thinking = &anthropic.Thinking{
				Type:         anthropic.ThinkingTypeEnabled,
				BudgetTokens: budget,
			}
			// Anthropic requires temperature to be unset (or 1.0) when thinking is enabled
			anthropicReq.Temperature = nil
		}
	}

	// Convert tools
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolDefinition, len(req.Tools))
		for i, tool := range req.Tools {
			tools[i] = anthropic.ToolDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: SanitizeSchemaForAnthropic(tool.InputSchema),
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
			content = append(content, anthropic.NewToolUseMessageContent(sanitizeAnthropicToolID(tc.ID), tc.Name, tc.Input))
		}

		return anthropic.Message{
			Role:    anthropic.RoleAssistant,
			Content: content,
		}, nil

	case "tool":
		return anthropic.Message{
			Role: anthropic.RoleUser,
			Content: []anthropic.MessageContent{
				anthropic.NewToolResultMessageContent(sanitizeAnthropicToolID(msg.ToolCallID), msg.Content, false),
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

	var reasoning string

	// Process content blocks
	for _, block := range resp.Content {
		switch block.Type {
		case anthropic.MessagesContentTypeText:
			if message.Content != "" {
				message.Content += "\n"
			}
			message.Content += block.GetText()

		case anthropic.MessagesContentTypeThinking:
			// Extended thinking content block
			if block.MessageContentThinking != nil {
				if reasoning != "" {
					reasoning += "\n"
				}
				reasoning += block.Thinking
			}

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
		Reasoning:  reasoning,
		StopReason: string(resp.StopReason),
		Usage: TokenUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}, nil
}

// wrapError maps Anthropic SDK error types to *Error.
func (p *AnthropicProvider) wrapError(err error) error {
	var apiErr *anthropic.APIError
	if errors.As(err, &apiErr) {
		retryable := apiErr.IsRateLimitErr() || apiErr.IsOverloadedErr() || apiErr.IsApiErr()
		return NewError(p.name, 0, retryable, err)
	}
	var reqErr *anthropic.RequestError
	if errors.As(err, &reqErr) {
		return WrapProviderError(p.name, reqErr.StatusCode, err)
	}
	return WrapProviderError(p.name, 0, err)
}
