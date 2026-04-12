package memory

import (
	"strings"

	sdkagent "github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
)

// stepsToMessages converts a slice of Steps to LLM messages.
// Each step produces:
// 1. An assistant message with Thought as content and Action as ToolCalls
// 2. A tool message with Observation as content
func stepsToMessages(steps []sdkagent.Step) []llm.Message {
	var messages []llm.Message
	for _, step := range steps {
		// Assistant message with thought and action
		assistantMsg := llm.Message{
			Role:    "assistant",
			Content: strings.TrimRight(step.Thought, invisibleChars),
		}
		if step.Action.ID != "" {
			assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
		}
		// OpenAI API requires assistant messages to have either content or tool_calls.
		// If both are empty, add a placeholder to prevent 400 errors.
		if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
			assistantMsg.Content = "(proceeding)"
		}
		messages = append(messages, assistantMsg)

		// Tool response message with observation
		if step.Action.ID != "" {
			observation := strings.TrimRight(step.Observation, invisibleChars)
			if observation == "" {
				observation = "(no output)"
			}
			toolMsg := llm.Message{
				Role:       "tool",
				Content:    observation,
				ToolCallID: step.Action.ID,
			}
			messages = append(messages, toolMsg)
		}
	}
	return messages
}

// truncateToTokenBudget truncates text to fit within the token budget.
// Uses a conservative character approximation (3 chars per token).
func truncateToTokenBudget(text string, maxTokens int) string {
	// Conservative estimate: ~3 chars per token to leave room for encoding variance
	maxChars := maxTokens * 3
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "\n[... truncated for summarization ...]"
}
