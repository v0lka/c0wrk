package llm

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

// ExtractSystemPromptParts collects each system message's content as a separate part.
// Returns the parts in order and the remaining non-system messages.
// This preserves the multi-part structure needed for Anthropic prompt caching.
func ExtractSystemPromptParts(messages []Message) (parts []string, filtered []Message) {
	filtered = make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			if msg.Content != "" {
				parts = append(parts, msg.Content)
			}
			continue
		}
		filtered = append(filtered, msg)
	}
	return parts, filtered
}
