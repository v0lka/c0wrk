package memory

import (
	"fmt"

	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/llm"
)

// SlidingWindowStrategy keeps the first K and last N steps, removing the middle.
type SlidingWindowStrategy struct {
	keepFirst int
	keepLast  int
}

// NewSlidingWindowStrategy creates a new SlidingWindowStrategy.
func NewSlidingWindowStrategy(keepFirst, keepLast int) *SlidingWindowStrategy {
	return &SlidingWindowStrategy{
		keepFirst: keepFirst,
		keepLast:  keepLast,
	}
}

// Compact implements CompactionStrategy. It keeps the first K and last N steps,
// inserting a summary message in between for any omitted steps.
func (s *SlidingWindowStrategy) Compact(steps []core.Step, budgetTokens int) []llm.Message {
	// If no compaction needed, convert all steps to messages
	if len(steps) <= s.keepFirst+s.keepLast {
		return s.stepsToMessages(steps)
	}

	// Each step produces 2 messages (assistant + tool)
	messages := make([]llm.Message, 0, len(steps)*2)

	// Keep first K steps
	firstSteps := steps[:s.keepFirst]
	messages = append(messages, s.stepsToMessages(firstSteps)...)

	// Insert summary message for omitted steps
	omittedCount := len(steps) - s.keepFirst - s.keepLast
	summaryMsg := llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("[... %d steps omitted ...]", omittedCount),
	}
	messages = append(messages, summaryMsg)

	// Keep last N steps
	lastSteps := steps[len(steps)-s.keepLast:]
	messages = append(messages, s.stepsToMessages(lastSteps)...)

	return messages
}

// stepsToMessages converts a slice of Steps to LLM messages.
// Each step produces:
// 1. An assistant message with Thought as content and Action as ToolCalls
// 2. A tool message with Observation as content
func (s *SlidingWindowStrategy) stepsToMessages(steps []core.Step) []llm.Message {
	var messages []llm.Message
	for _, step := range steps {
		// Assistant message with thought and action
		assistantMsg := llm.Message{
			Role:    "assistant",
			Content: step.Thought,
		}
		if step.Action.ID != "" {
			assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
		}
		messages = append(messages, assistantMsg)

		// Tool response message with observation
		if step.Action.ID != "" {
			toolMsg := llm.Message{
				Role:       "tool",
				Content:    step.Observation,
				ToolCallID: step.Action.ID,
			}
			messages = append(messages, toolMsg)
		}
	}
	return messages
}
