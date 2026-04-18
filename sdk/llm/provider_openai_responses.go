package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// responseEventPrefix is the common prefix for Responses API streaming event types.
const responseEventPrefix = "response."

// newResponsesClient creates an official OpenAI SDK client for the Responses API.
func newResponsesClient(apiKey, baseURL string) *oai.Client {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := oai.NewClient(opts...)
	return &client
}

// responsesAPICompletion performs a non-streaming Responses API call.
func responsesAPICompletion(ctx context.Context, client *oai.Client, req ChatRequest) (*ChatResponse, error) {
	params := buildResponsesParams(req)

	resp, err := client.Responses.New(ctx, params)
	if err != nil {
		return nil, wrapResponsesError("openai", err)
	}

	return convertResponsesResponse(resp)
}

// responsesAPIStream performs a streaming Responses API call.
func responsesAPIStream(ctx context.Context, client *oai.Client, req ChatRequest) (<-chan ChatChunk, error) {
	params := buildResponsesParams(req)

	stream := client.Responses.NewStreaming(ctx, params)

	chunks := make(chan ChatChunk)

	go func() {
		defer close(chunks)
		defer func() { _ = stream.Close() }()

		acc := NewStreamToolCallAccumulator()
		toolIdx := 0

		for stream.Next() {
			event := stream.Current()

			switch event.Type {
			case responseEventPrefix + "output_text.delta":
				if event.Delta.OfString != "" {
					chunks <- ChatChunk{Delta: event.Delta.OfString}
				}

			case responseEventPrefix + "reasoning_summary_text.delta":
				if event.Delta.OfString != "" {
					chunks <- ChatChunk{Reasoning: event.Delta.OfString}
				}

			case responseEventPrefix + "function_call_arguments.delta":
				acc.HandleDelta(toolIdx, event.ItemID, "", event.Delta.OfString)

			case responseEventPrefix + "function_call_arguments.done":
				// The item is complete; pick up the name from the output_item.added event
				// which was handled earlier. Just ensure the arguments are finalized.
				acc.HandleDelta(toolIdx, "", "", "")

			case responseEventPrefix + "output_item.added":
				if event.Item.Type == "function_call" {
					acc.HandleDelta(toolIdx, event.Item.CallID, event.Item.Name, "")
				}

			case responseEventPrefix + "output_item.done":
				if event.Item.Type == "function_call" {
					toolIdx++
				}

			case responseEventPrefix + "completed":
				acc.Emit(chunks)
				stopReason := mapResponsesStopReason(&event.Response)
				chunks <- ChatChunk{
					StopReason: stopReason,
					Usage: &TokenUsage{
						InputTokens:  int(event.Response.Usage.InputTokens),
						OutputTokens: int(event.Response.Usage.OutputTokens),
					},
				}

			case responseEventPrefix + "failed", responseEventPrefix + "incomplete":
				acc.Emit(chunks)
				stopReason := mapResponsesStopReason(&event.Response)
				chunks <- ChatChunk{StopReason: stopReason}

			case "error":
				chunks <- ChatChunk{StopReason: "error", Delta: event.Message}
			}
		}

		if err := stream.Err(); err != nil {
			select {
			case chunks <- ChatChunk{StopReason: "error"}:
			default:
			}
		}
	}()

	return chunks, nil
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
		rc := ResolveReasoning(req.ReasoningEffort, "openai_flagship")
		if rc.Enabled && rc.OpenAIEffort != "" {
			params.Reasoning = shared.ReasoningParam{
				Effort:  shared.ReasoningEffort(rc.OpenAIEffort),
				Summary: shared.ReasoningSummaryAuto,
			}
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

	// Extract function_call items as tool calls
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:    item.CallID,
				Name:  item.Name,
				Input: json.RawMessage(item.Arguments),
			})
		}
	}

	stopReason := mapResponsesStopReason(resp)

	return &ChatResponse{
		Message:    message,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		},
	}, nil
}

// wrapResponsesError wraps errors from the official OpenAI SDK into our error type.
func wrapResponsesError(providerName string, err error) error {
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		return WrapProviderError(providerName, apiErr.StatusCode, fmt.Errorf("responses API: %w", err))
	}
	return WrapProviderError(providerName, 0, fmt.Errorf("responses API: %w", err))
}
