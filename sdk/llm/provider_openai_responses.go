package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// newResponsesClient creates an official OpenAI SDK client for the Responses API.
func newResponsesClient(apiKey, baseURL string, httpClient *http.Client) *oai.Client {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	client := oai.NewClient(opts...)
	return &client
}

// responsesAPICompletion performs a non-streaming Responses API call.
func responsesAPICompletion(ctx context.Context, client *oai.Client, providerName string, req ChatRequest) (*ChatResponse, error) {
	params := buildResponsesParams(req)

	resp, err := client.Responses.New(ctx, params)
	if err != nil {
		return nil, wrapResponsesError(providerName, err)
	}

	return convertResponsesResponse(resp)
}

// buildResponsesParams constructs ResponseNewParams from a ChatRequest.
func buildResponsesParams(req ChatRequest) responses.ResponseNewParams {
	systemPrompt, filteredMsgs := ExtractSystemPrompt(req.Messages)

	params := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: convertToResponsesInput(filteredMsgs),
		},
		Store: param.NewOpt(false),
	}

	if systemPrompt != "" {
		params.Instructions = param.NewOpt(systemPrompt)
	}

	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(req.MaxTokens))
	}

	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}

	if req.ReasoningEffort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(req.ReasoningEffort),
			Summary: shared.ReasoningSummaryAuto,
		}
	}

	if len(req.Tools) > 0 {
		params.Tools = convertToResponsesTools(req.Tools)
	}

	return params
}

// convertToResponsesInput converts internal messages to Responses API input items.
func convertToResponsesInput(messages []Message) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content),
					},
				},
			})

		case "assistant":
			// Re-emit reasoning items first (with their original IDs) so the
			// Responses API can maintain the reasoning chain across turns.
			// This is critical for reasoning models (e.g. Codex): without
			// round-tripping reasoning items, the model loses its committed
			// plan between ReAct iterations and reverts to read-only exploration.
			for _, ri := range msg.ReasoningItems {
				summaryParams := make([]responses.ResponseReasoningItemSummaryParam, 0, 1)
				if ri.Summary != "" {
					summaryParams = append(summaryParams, responses.ResponseReasoningItemSummaryParam{
						Text: ri.Summary,
					})
				}
				items = append(items, responses.ResponseInputItemParamOfReasoning(ri.ID, summaryParams))
			}

			// If assistant has tool calls, add the text message (if any) and then each function_call
			if msg.Content != "" {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role: responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{
							OfString: param.NewOpt(msg.Content),
						},
					},
				})
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfFunctionCall: &responses.ResponseFunctionToolCallParam{
						CallID:    tc.ID,
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}

		case "tool":
			output := msg.Content
			if output == "" {
				output = "(no output)"
			}
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: msg.ToolCallID,
					Output: output,
				},
			})
		}
	}
	return items
}

// convertToResponsesTools converts internal tool definitions to Responses API tools.
func convertToResponsesTools(tools []ToolDefinition) []responses.ToolUnionParam {
	result := make([]responses.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		var params map[string]any
		if len(tool.InputSchema) > 0 {
			sanitized := SanitizeSchemaForOpenAI(tool.InputSchema)
			if err := json.Unmarshal(sanitized, &params); err != nil {
				params = map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"required":             []any{},
					"additionalProperties": false,
				}
			}
		}
		result = append(result, responses.ToolParamOfFunction(
			tool.Name,
			params,
			true,
		))
		// Set description on the just-created tool
		if tool.Description != "" {
			result[len(result)-1].OfFunction.Description = param.NewOpt(tool.Description)
		}
	}
	return result
}

// mapResponsesStopReason maps a Responses API response to a standard stop reason.
func mapResponsesStopReason(resp *responses.Response) string {
	// Check if output contains function calls
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			return "tool_use"
		}
	}

	switch resp.Status {
	case responses.ResponseStatusCompleted:
		return "end_turn"
	case responses.ResponseStatusIncomplete:
		if resp.IncompleteDetails.Reason == "max_output_tokens" {
			return "max_tokens"
		}
		return "end_turn"
	case responses.ResponseStatusFailed:
		return "error"
	case responses.ResponseStatusCancelled:
		return "end_turn"
	default:
		return "end_turn"
	}
}

// convertResponsesResponse converts a Responses API response to our ChatResponse.
func convertResponsesResponse(resp *responses.Response) (*ChatResponse, error) {
	if resp == nil {
		return nil, errors.New("responses API: nil response")
	}

	message := Message{
		Role:    "assistant",
		Content: resp.OutputText(),
	}

	// Extract reasoning items and function_call items from the response output.
	var reasoningParts []string
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			// Collect summary text from the reasoning item.
			var summaryText strings.Builder
			for _, s := range item.Summary {
				if s.Text != "" {
					if summaryText.Len() > 0 {
						summaryText.WriteString("\n")
					}
					summaryText.WriteString(s.Text)
				}
			}
			summary := summaryText.String()
			if summary != "" {
				reasoningParts = append(reasoningParts, summary)
			}
			if item.ID != "" {
				message.ReasoningItems = append(message.ReasoningItems, ReasoningItem{
					ID:      item.ID,
					Summary: summary,
				})
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:    item.CallID,
				Name:  item.Name,
				Input: json.RawMessage(item.Arguments),
			})
		}
	}

	// Populate ReasoningContent and Reasoning from the concatenated reasoning summaries.
	// This makes reasoning visible to the UI and to non-Responses transports.
	if len(reasoningParts) > 0 {
		message.ReasoningContent = strings.Join(reasoningParts, "\n")
	}

	stopReason := mapResponsesStopReason(resp)

	result := &ChatResponse{
		Message:    message,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		},
	}
	result.Reasoning = message.ReasoningContent
	return result, nil
}

// wrapResponsesError wraps errors from the official OpenAI SDK into our error type.
func wrapResponsesError(providerName string, err error) error {
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return WrapProviderError(providerName, apiErr.StatusCode, fmt.Errorf("responses API: %w", err))
	}
	return WrapProviderError(providerName, 0, fmt.Errorf("responses API: %w", err))
}
