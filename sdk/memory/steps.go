package memory

import (
	"strings"

	sdkagent "github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
)

// stepsToMessages converts a slice of Steps to LLM messages.
// Each standalone step (ResponseGroup == 0) produces:
// 1. An assistant message with Thought as content and Action as ToolCalls
// 2. A tool message with Observation as content
// Steps with matching ResponseGroup > 0 are merged into a single assistant message
// with multiple tool_calls, followed by individual tool result messages.
func stepsToMessages(steps []sdkagent.Step) []llm.Message {
	var messages []llm.Message
	for i := 0; i < len(steps); {
		step := steps[i]

		if step.ResponseGroup > 0 {
			// Collect all consecutive steps with the same ResponseGroup
			groupEnd := i + 1
			for groupEnd < len(steps) && steps[groupEnd].ResponseGroup == step.ResponseGroup {
				groupEnd++
			}
			groupSteps := steps[i:groupEnd]

			// Build ONE assistant message with all tool calls
			assistantMsg := llm.Message{
				Role:             "assistant",
				Content:          strings.TrimRight(groupSteps[0].Thought, invisibleChars),
				ReasoningContent: groupSteps[0].ReasoningContent,
			}
			for _, gs := range groupSteps {
				if gs.Action.ID != "" {
					assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, gs.Action)
				}
			}
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				assistantMsg.Content = "(proceeding)"
			}
			messages = append(messages, assistantMsg)

			// Add individual tool result messages
			for _, gs := range groupSteps {
				if gs.Action.ID != "" {
					observation := strings.TrimRight(gs.Observation, invisibleChars)
					if observation == "" {
						observation = "(no output)"
					}
					messages = append(messages, llm.Message{
						Role:       "tool",
						Content:    observation,
						ToolCallID: gs.Action.ID,
					})
				}
			}

			i = groupEnd
		} else {
			// Original logic for standalone steps
			assistantMsg := llm.Message{
				Role:             "assistant",
				Content:          strings.TrimRight(step.Thought, invisibleChars),
				ReasoningContent: step.ReasoningContent,
			}
			if step.Action.ID != "" {
				assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
			}
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				assistantMsg.Content = "(proceeding)"
			}
			messages = append(messages, assistantMsg)

			if step.Action.ID != "" {
				observation := strings.TrimRight(step.Observation, invisibleChars)
				if observation == "" {
					observation = "(no output)"
				}
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    observation,
					ToolCallID: step.Action.ID,
				})
			}

			i++
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
