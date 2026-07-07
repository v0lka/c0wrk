package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/strutil"
)

// SummarizationStrategy groups the oldest steps into blocks and uses an LLM
// to summarize each block into a compact summary message.
type SummarizationStrategy struct {
	blockSize           int // number of steps per summary block (default: 10)
	keepLast            int // number of recent steps to preserve verbatim
	observationTruncate int // max chars for observations in summary blocks (default: 500)
	summarizer          func(ctx context.Context, text string) (string, error)
	tokenCounter        llm.TokenCounter
	maxSummarizeTokens  int
	logger              *slog.Logger
}

// SetLogger sets the logger for the strategy. If nil, slog.Default() is used.
func (s *SummarizationStrategy) SetLogger(l *slog.Logger) { s.logger = l }

func (s *SummarizationStrategy) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.New(slog.DiscardHandler)
}

// NewSummarizationStrategy creates a new SummarizationStrategy.
// blockSize is the number of steps per summary block.
// keepLast is the number of recent steps to preserve verbatim.
// observationTruncate is the max chars for observations in summary blocks (default: 500 if <= 0).
// summarizer is the function to call for LLM summarization.
// tokenCounter is optional; if provided, blocks are truncated to maxSummarizeTokens.
// maxSummarizeTokens defaults to 16000 if zero.
func NewSummarizationStrategy(blockSize, keepLast, observationTruncate int, summarizer func(ctx context.Context, text string) (string, error), tokenCounter llm.TokenCounter, maxSummarizeTokens int) *SummarizationStrategy {
	if blockSize <= 0 {
		blockSize = 10
	}
	if keepLast <= 0 {
		keepLast = 5
	}
	if observationTruncate <= 0 {
		observationTruncate = 500
	}
	if maxSummarizeTokens <= 0 {
		maxSummarizeTokens = 16000
	}
	return &SummarizationStrategy{
		blockSize:           blockSize,
		keepLast:            keepLast,
		observationTruncate: observationTruncate,
		summarizer:          summarizer,
		tokenCounter:        tokenCounter,
		maxSummarizeTokens:  maxSummarizeTokens,
	}
}

// Compact takes steps that need compaction, groups oldest into blocks of blockSize,
// summarizes each block via the LLM summarizer, returns compacted steps.
// Recent steps (within keepLast) are preserved verbatim.
// Summary blocks become single messages with Role="system" and Content=summarized text.
func (s *SummarizationStrategy) Compact(ctx context.Context, steps []sdkagent.Step, budgetTokens int) []llm.Message {
	// If no compaction needed, convert all steps to messages
	if len(steps) <= s.keepLast {
		return stepsToMessages(steps)
	}

	var messages []llm.Message

	// Determine which steps to summarize vs. keep
	numToSummarize := len(steps) - s.keepLast
	stepsToSummarize := steps[:numToSummarize]
	stepsToKeep := steps[numToSummarize:]

	// Group steps into blocks and summarize each
	for i := 0; i < len(stepsToSummarize); i += s.blockSize {
		end := i + s.blockSize
		if end > len(stepsToSummarize) {
			end = len(stepsToSummarize)
		}
		block := stepsToSummarize[i:end]

		// Build text representation of the block
		blockText := s.buildBlockText(block)

		// Truncate to token budget before sending to LLM
		if s.tokenCounter != nil && s.maxSummarizeTokens > 0 {
			tokenCount := s.tokenCounter.Count(blockText)
			if tokenCount > s.maxSummarizeTokens {
				blockText = truncateToTokenBudget(blockText, s.maxSummarizeTokens)
			}
		}

		// Summarize the block
		var summary string
		if s.summarizer != nil {
			var err error
			summary, err = s.summarizer(ctx, blockText)
			if err != nil {
				s.log().Error("summary compaction: summarization failed", "error", err)
				// Fallback to a simple indicator if summarization fails
				summary = fmt.Sprintf("[Summary of steps %d-%d failed: %v]", i+1, end, err)
			}
		} else {
			// No summarizer provided, use a simple placeholder
			summary = fmt.Sprintf("[... %d steps summarized ...]", end-i)
		}

		// Add summary as a system message
		summaryMsg := llm.Message{
			Role:    "system",
			Content: summary,
		}
		messages = append(messages, summaryMsg)
	}

	// Append the recent steps verbatim
	messages = append(messages, stepsToMessages(stepsToKeep)...)

	return messages
}

// buildBlockText creates a text representation of a block of steps for summarization.
func (s *SummarizationStrategy) buildBlockText(steps []sdkagent.Step) string {
	parts := make([]string, 0, len(steps))
	for i, step := range steps {
		stepText := fmt.Sprintf("Step %d:\n", i+1)
		if step.Thought != "" {
			stepText += fmt.Sprintf("  Thought: %s\n", step.Thought)
		}
		if step.Action.Name != "" {
			stepText += fmt.Sprintf("  Action: %s\n", step.Action.Name)
		}
		if step.Observation != "" {
			// Truncate long observations
			obs := step.Observation
			if len(obs) > s.observationTruncate {
				obs = strutil.TruncateUTF8(obs, s.observationTruncate) + "..."
			}
			stepText += fmt.Sprintf("  Observation: %s\n", obs)
		}
		parts = append(parts, stepText)
	}
	return strings.Join(parts, "\n")
}
