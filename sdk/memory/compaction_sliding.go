package memory

import (
	"context"
	"fmt"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
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
func (s *SlidingWindowStrategy) Compact(ctx context.Context, steps []sdkagent.Step, budgetTokens int) []llm.Message {
	_ = ctx // unused, for interface compliance
	// If no compaction needed, convert all steps to messages
	if len(steps) <= s.keepFirst+s.keepLast {
		return stepsToMessages(steps)
	}

	// Each step produces 2 messages (assistant + tool)
	messages := make([]llm.Message, 0, len(steps)*2)

	// Keep first K steps
	firstSteps := steps[:s.keepFirst]
	messages = append(messages, stepsToMessages(firstSteps)...)

	// Insert summary message for omitted steps
	omittedCount := len(steps) - s.keepFirst - s.keepLast
	summaryMsg := llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("[... %d steps omitted ...]", omittedCount),
	}
	messages = append(messages, summaryMsg)

	// Keep last N steps
	lastSteps := steps[len(steps)-s.keepLast:]
	messages = append(messages, stepsToMessages(lastSteps)...)

	return messages
}
