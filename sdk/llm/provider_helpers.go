package llm

import "encoding/json"

// openAIStopReasonMap maps OpenAI-style finish reasons to our standard format.
// Shared by OpenAI and LM Studio (OpenAI-compat mode) providers.
var openAIStopReasonMap = map[string]string{
	"stop":       "end_turn",
	"tool_calls": "tool_use",
	"length":     "max_tokens",
}

// MapStopReason converts a provider-specific stop reason to the standard format
// using the given mapping table. Returns the mapped value if found, the original
// reason if not mapped, or "end_turn" if reason is empty.
func MapStopReason(reason string, mapping map[string]string) string {
	if reason == "" {
		return "end_turn"
	}
	if mapped, ok := mapping[reason]; ok {
		return mapped
	}
	return reason
}

// ExtractSystemPrompt separates system messages from the message list.
// System message contents are concatenated with "\n".
// Returns the combined system prompt and the remaining non-system messages.
func ExtractSystemPrompt(messages []Message) (string, []Message) {
	var systemPrompt string
	filtered := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			if systemPrompt != "" {
				systemPrompt += "\n"
			}
			systemPrompt += msg.Content
			continue
		}
		filtered = append(filtered, msg)
	}
	return systemPrompt, filtered
}

// StreamToolCallAccumulator tracks partial tool calls across streaming chunks.
// OpenAI-style streaming sends tool call data incrementally: first the ID and name,
// then argument fragments across multiple deltas. This accumulator assembles
// complete ToolCall objects from those fragments.
type StreamToolCallAccumulator struct {
	toolCalls map[int]*ToolCall
}

// NewStreamToolCallAccumulator creates a new empty accumulator.
func NewStreamToolCallAccumulator() *StreamToolCallAccumulator {
	return &StreamToolCallAccumulator{
		toolCalls: make(map[int]*ToolCall),
	}
}

// HandleDelta processes a single tool call delta from a streaming chunk.
// index is the tool call index within the response (used to correlate fragments).
// Non-empty id, name, and arguments values update/accumulate into the tracked call.
func (a *StreamToolCallAccumulator) HandleDelta(index int, id, name, arguments string) {
	tc, exists := a.toolCalls[index]
	if !exists {
		tc = &ToolCall{
			ID:    id,
			Name:  name,
			Input: json.RawMessage(""),
		}
		a.toolCalls[index] = tc
	}

	if arguments != "" {
		existing := string(tc.Input)
		tc.Input = json.RawMessage(existing + arguments)
	}
	if name != "" {
		tc.Name = name
	}
	if id != "" {
		tc.ID = id
	}
}

// Emit sends all accumulated tool calls to the channel in index order.
// Should be called when a finish reason is received, regardless of reason type.
func (a *StreamToolCallAccumulator) Emit(chunks chan<- ChatChunk) {
	for i := 0; i < len(a.toolCalls); i++ {
		if tc, ok := a.toolCalls[i]; ok {
			chunks <- ChatChunk{ToolCall: tc}
		}
	}
}

// HasToolCalls returns true if any tool calls have been accumulated.
func (a *StreamToolCallAccumulator) HasToolCalls() bool {
	return len(a.toolCalls) > 0
}

// NormalizeMistralMessages applies Mistral-specific message normalization:
// 1. Truncates tool call IDs to 9 characters
// 2. Inserts dummy assistant messages between tool_result and user messages
func NormalizeMistralMessages(messages []Message) []Message {
	result := make([]Message, 0, len(messages))

	for i, msg := range messages {
		// Truncate tool call IDs to 9 characters
		if msg.ToolCallID != "" && len(msg.ToolCallID) > 9 {
			msg.ToolCallID = msg.ToolCallID[:9]
		}

		// Truncate tool call IDs in assistant tool calls
		if len(msg.ToolCalls) > 0 {
			calls := make([]ToolCall, len(msg.ToolCalls))
			copy(calls, msg.ToolCalls)
			for j := range calls {
				if len(calls[j].ID) > 9 {
					calls[j].ID = calls[j].ID[:9]
				}
			}
			msg.ToolCalls = calls
		}

		result = append(result, msg)

		// Insert dummy assistant message between tool result and user message
		if msg.Role == "tool" && i+1 < len(messages) && messages[i+1].Role == "user" {
			result = append(result, Message{
				Role:    "assistant",
				Content: "I'll continue processing your request.",
			})
		}
	}

	return result
}


