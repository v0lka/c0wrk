package memory

import (
	"context"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// CompactionConfig holds configuration for compaction strategies.
type CompactionConfig struct {
	SlidingWindow struct {
		KeepFirst int
		KeepLast  int
	}
	Summarization struct {
		BlockSize           int
		KeepLast            int
		ObservationTruncate int // max chars for observations in summary blocks (default: 500)
	}
	Hierarchical struct {
		DistantRatio float64
		MiddleRatio  float64
		RecentRatio  float64
	}
}

// CompactionDeps — external dependencies needed by some strategies.
type CompactionDeps struct {
	TokenCounter llm.TokenCounter
	// Summarize calls the LLM to summarize a block of text.
	// Used by SummarizationStrategy and HierarchicalStrategy.
	Summarize func(ctx context.Context, text string) (string, error)
	// MaxSummarizeTokens is the maximum token count for text sent to summarization.
	// Defaults to 16000 if zero.
	MaxSummarizeTokens int
}

// NewCompactionStrategy creates a CompactionStrategy by name.
// Returns an sdkagent.CompactionStrategy for use with ContextWindow.SetStrategy.
func NewCompactionStrategy(name string, cfg CompactionConfig, deps CompactionDeps) sdkagent.CompactionStrategy {
	// Default maxSummarizeTokens if not set
	maxTokens := deps.MaxSummarizeTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}

	switch name {
	case "sliding_window":
		return NewSlidingWindowStrategy(cfg.SlidingWindow.KeepFirst, cfg.SlidingWindow.KeepLast)
	case "summarization":
		blockSize := cfg.Summarization.BlockSize
		if blockSize <= 0 {
			blockSize = 10
		}
		keepLast := cfg.Summarization.KeepLast
		if keepLast <= 0 {
			keepLast = 5
		}
		obsTruncate := cfg.Summarization.ObservationTruncate
		if obsTruncate <= 0 {
			obsTruncate = 500
		}
		return NewSummarizationStrategy(blockSize, keepLast, obsTruncate, deps.Summarize, deps.TokenCounter, maxTokens)
	case "hierarchical":
		distant := cfg.Hierarchical.DistantRatio
		if distant <= 0 {
			distant = 0.4
		}
		middle := cfg.Hierarchical.MiddleRatio
		if middle <= 0 {
			middle = 0.3
		}
		recent := cfg.Hierarchical.RecentRatio
		if recent <= 0 {
			recent = 0.3
		}
		obsTruncateHier := cfg.Summarization.ObservationTruncate
		if obsTruncateHier <= 0 {
			obsTruncateHier = 500
		}
		return NewHierarchicalStrategy(distant, middle, recent, obsTruncateHier, deps.Summarize, deps.TokenCounter, maxTokens)
	default:
		return NewSlidingWindowStrategy(cfg.SlidingWindow.KeepFirst, cfg.SlidingWindow.KeepLast)
	}
}
