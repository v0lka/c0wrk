package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// OpenAIProviderConfig contains configuration for OpenAI-compatible providers.
type OpenAIProviderConfig struct {
	Name       string // logical provider name ("openai", "deepseek", "grok", etc.)
	APIKey     string
	BaseURL    string       // empty = default OpenAI; otherwise custom endpoint
	HTTPClient *http.Client // optional proxy-configured HTTP client (nil = default)
}

// OpenAIProvider implements Provider for OpenAI and compatible APIs.
type OpenAIProvider struct {
	client          *oai.Client // official SDK for Chat Completions API
	responsesClient *oai.Client // official SDK for Responses API
	name            string
	baseURL         string // empty = default OpenAI; non-empty = compatible provider
}

// NewOpenAIProvider creates a new OpenAI provider.
// If BaseURL is empty, uses default OpenAI endpoint.
// If BaseURL is set, uses custom endpoint (DeepSeek, Grok, OpenRouter, Ollama, LM-Studio).
//
// Note: APIKey is intentionally not validated here. Local models (LM Studio, Ollama)
// using OpenAI-compatible endpoints do not require authentication. This constructor
// must accept empty keys to support local inference backends.
func NewOpenAIProvider(cfg OpenAIProviderConfig) (*OpenAIProvider, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	client := oai.NewClient(opts...)

	responsesClient := newResponsesClient(cfg.APIKey, cfg.BaseURL, cfg.HTTPClient)

	return &OpenAIProvider{
		client:          &client,
		responsesClient: responsesClient,
		name:            cfg.Name,
		baseURL:         cfg.BaseURL,
	}, nil
}

// Name returns the provider name for logging.
func (p *OpenAIProvider) Name() string {
	return p.name
}

// ChatCompletion sends a request and returns the full response.
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// The Responses API (/v1/responses) is an OpenAI-specific endpoint, not
	// part of the "OpenAI-compatible" standard. Compatible providers (custom
	// baseURL) implement Chat Completions, not the Responses API. Only route
	// to the Responses API for the official OpenAI endpoint where codex-family
	// models require it. Compatible providers serve codex models via Chat
	// Completions instead.
	if p.baseURL == "" && needsResponsesAPI(req.Model) {
		return responsesAPICompletion(ctx, p.responsesClient, p.name, p.baseURL, req)
	}

	params := p.buildChatParams(req)

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, p.wrapError(fmt.Errorf("openai chat completion: %w", err))
	}

	if len(resp.Choices) == 0 {
		return nil, WrapProviderError(p.name, 0, errors.New("no choices in response"))
	}

	choice := resp.Choices[0]
	message := p.convertChatResponseMessage(choice.Message)
	stopReason := MapStopReason(choice.FinishReason, openAIStopReasonMap)

	return &ChatResponse{
		Message:    message,
		Reasoning:  message.ReasoningContent,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}, nil
}

// buildChatParams converts our ChatRequest to OpenAI ChatCompletionNewParams.
func (p *OpenAIProvider) buildChatParams(req ChatRequest) oai.ChatCompletionNewParams {
	messages := make([]oai.ChatCompletionMessageParamUnion, 0, len(req.Messages))
	for _, msg := range req.Messages {
		messages = append(messages, p.convertRequestMessage(msg))
	}

	params := oai.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: messages,
	}

	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = oai.Int(int64(req.MaxTokens))
	}

	if req.Temperature != nil {
		params.Temperature = oai.Float(*req.Temperature)
	}

	// Apply reasoning effort as native provider value
	if req.ReasoningEffort != "" {
		family := req.ModelFamily
		if family == "" {
			family = string(DetectFamily(req.Model))
		}
		switch family {
		case "openai_flagship", "openai_standard", "openai_codex":
			params.ReasoningEffort = oai.ReasoningEffort(req.ReasoningEffort)
		case "deepseek":
			params.SetExtraFields(map[string]any{
				"thinking": map[string]string{"type": req.ReasoningEffort},
			})
		case "qwen":
			params.SetExtraFields(map[string]any{
				"enable_thinking": req.ReasoningEffort == "On",
			})
		case "glm":
			applyGLMReasoning(&params, req.Model, req.ReasoningEffort)
		}
	}

	if len(req.Tools) > 0 {
		tools := make([]oai.ChatCompletionToolParam, len(req.Tools))
		for i, tool := range req.Tools {
			tools[i] = oai.ChatCompletionToolParam{
				Function: oai.FunctionDefinitionParam{
					Name:        tool.Name,
					Description: oai.String(tool.Description),
					Parameters:  p.convertSchemaToMap(SanitizeSchemaForOpenAI(tool.InputSchema)),
				},
			}
		}
		params.Tools = tools
	}

	return params
}

// applyGLMReasoning sets the reasoning-related extra fields for GLM models.
//
// GLM 5.2+ supports the reasoning_effort parameter (values "max"/"high") which
// is honored when thinking is enabled:
//
//   - "none": thinking disabled, no reasoning_effort
//   - "max":  thinking enabled,  reasoning_effort=max
//   - "high": thinking enabled,  reasoning_effort=high
//
// An empty effort (the UI "Auto"/Default selection) is left unset: GLM 5.2
// enables thinking by default with reasoning_effort=max, so Auto == "max".
//
// Older GLM models (< 5.2) keep the legacy thinking on/off control, passing the
// native effort value ("On"/"Off") directly as thinking.type.
func applyGLMReasoning(params *oai.ChatCompletionNewParams, model, effort string) {
	if IsGLM52OrLater(model) {
		switch effort {
		case "none":
			params.SetExtraFields(map[string]any{
				"thinking": map[string]string{"type": "disabled"},
			})
		case "max":
			params.SetExtraFields(map[string]any{
				"thinking":         map[string]string{"type": "enabled"},
				"reasoning_effort": "max",
			})
		case "high":
			params.SetExtraFields(map[string]any{
				"thinking":         map[string]string{"type": "enabled"},
				"reasoning_effort": "high",
			})
		}
		return
	}
	params.SetExtraFields(map[string]any{
		"thinking": map[string]string{"type": effort},
	})
}

// convertSchemaToMap converts JSON schema bytes to a map[string]any.
func (p *OpenAIProvider) convertSchemaToMap(schema []byte) map[string]any {
	if len(schema) == 0 {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []any{},
			"additionalProperties": false,
		}
	}
	var params map[string]any
	if err := json.Unmarshal(schema, &params); err != nil {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []any{},
			"additionalProperties": false,
		}
	}
	return params
}

// convertRequestMessage converts our Message to OpenAI's message format.
func (p *OpenAIProvider) convertRequestMessage(msg Message) oai.ChatCompletionMessageParamUnion {
	// Safety net: OpenAI API requires non-empty content for tool-role messages.
	// The context layer should already guarantee this, but we keep this as a defensive measure.
	content := msg.Content
	if msg.Role == "tool" && content == "" {
		content = "(no output)"
	}

	switch msg.Role {
	case "system":
		return oai.SystemMessage(content)
	case "user":
		return oai.UserMessage(content)
	case "assistant":
		assistantParam := oai.ChatCompletionAssistantMessageParam{
			Content: oai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: oai.String(content),
			},
		}
		if len(msg.ToolCalls) > 0 {
			toolCalls := make([]oai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				toolCalls[i] = oai.ChatCompletionMessageToolCallParam{
					ID:   tc.ID,
					Type: "function",
					Function: oai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				}
			}
			assistantParam.ToolCalls = toolCalls
		}
		// DeepSeek V4 requires reasoning_content to be echoed back for ALL
		// assistant messages in thinking mode, even when empty. Constructed
		// assistant messages (e.g., nudges without tool calls) must also
		// include the field to avoid 400 errors.
		assistantParam.SetExtraFields(map[string]any{
			"reasoning_content": msg.ReasoningContent,
		})
		return oai.ChatCompletionMessageParamUnion{
			OfAssistant: &assistantParam,
		}
	case "tool":
		return oai.ToolMessage(content, msg.ToolCallID)
	default:
		return oai.UserMessage(content)
	}
}

// convertChatResponseMessage converts OpenAI's message to our Message format.
func (p *OpenAIProvider) convertChatResponseMessage(msg oai.ChatCompletionMessage) Message {
	result := Message{
		Role:    string(msg.Role),
		Content: msg.Content,
	}

	// Extract reasoning_content from raw JSON (DeepSeek extension).
	result.ReasoningContent = extractReasoningContent(msg.RawJSON())

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

// extractReasoningContent extracts the "reasoning_content" field from raw JSON.
// This is a DeepSeek-specific extension to the OpenAI chat completions format.
func extractReasoningContent(rawJSON string) string {
	if rawJSON == "" {
		return ""
	}
	var payload struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return ""
	}
	return payload.ReasoningContent
}

// wrapError maps OpenAI SDK error types to *Error.
func (p *OpenAIProvider) wrapError(err error) error {
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return WrapProviderError(p.name, apiErr.StatusCode, err)
	}
	// Fallback: check for net errors directly
	return WrapProviderError(p.name, 0, err)
}

// needsResponsesAPI returns true if the model requires the Responses API
// (e.g., Codex models use /v1/responses instead of /v1/chat/completions).
func needsResponsesAPI(model string) bool {
	return DetectFamily(model) == FamilyOpenAICodex
}
