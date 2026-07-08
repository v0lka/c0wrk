package memory

import (
	"strings"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
)

// stepsToMessages converts a slice of Steps to LLM messages.
// Each standalone step (ResponseGroup == 0) produces:
// 1. An assistant message with Thought as content and Action as ToolCalls
// 2. A tool message with Observation as content
// 3. A user message with UserNudge, when present (mirrors buildNudgeMsg)
// Steps with matching ResponseGroup > 0 are merged into a single assistant message
// with multiple tool_calls, followed by individual tool result messages; the
// UserNudge of the group's last step is emitted after the tool results.
// Nudge-only steps (no thought, no action, non-empty nudge) produce only the
// user message — no empty "(proceeding)" assistant placeholder.
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
			// UserNudge only on last step of group (mirrors buildGroupedMessages)
			nudge := strings.TrimRight(groupSteps[len(groupSteps)-1].UserNudge, invisibleChars)
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				if nudge == "" {
					assistantMsg.Content = "(proceeding)"
					messages = append(messages, assistantMsg)
				}
				// Nudge-only group: skip the empty assistant placeholder.
			} else {
				messages = append(messages, assistantMsg)
			}

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

			if nudge != "" {
				messages = append(messages, llm.Message{Role: "user", Content: nudge})
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
			nudge := strings.TrimRight(step.UserNudge, invisibleChars)
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				if nudge == "" {
					assistantMsg.Content = "(proceeding)"
					messages = append(messages, assistantMsg)
				}
				// Nudge-only step: skip the empty assistant placeholder.
			} else {
				messages = append(messages, assistantMsg)
			}

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

			if nudge != "" {
				messages = append(messages, llm.Message{Role: "user", Content: nudge})
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
	return strutil.TruncateUTF8(text, maxChars) + "\n[... truncated for summarization ...]"
}
