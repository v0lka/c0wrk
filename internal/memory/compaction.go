package memory

import (
	"context"

	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/llm"
)

// CompactionStrategy defines an algorithm for compressing step history.
type CompactionStrategy interface {
	Compact(steps []core.Step, budgetTokens int) []llm.Message
}

// CompactionConfig holds configuration for compaction strategies.
type CompactionConfig struct {
	SlidingWindow struct {
		KeepFirst int
		KeepLast  int
	}
	Summarization struct {
		BlockSize int
		KeepLast  int
	}
	Hierarchical struct {
		DistantRatio float64
		MiddleRatio  float64
		RecentRatio  float64
	}
}

// CompactionDeps — external dependencies needed by some strategies.
type CompactionDeps struct {
	LLM          interface{} // will be *LLMRouter, use interface{} to avoid circular import for now
	TokenCounter llm.TokenCounter
	// Summarize calls the LLM to summarize a block of text.
	// Used by SummarizationStrategy and HierarchicalStrategy.
	Summarize func(ctx context.Context, text string) (string, error)
}

// NewCompactionStrategy creates a CompactionStrategy by name.
func NewCompactionStrategy(name string, cfg CompactionConfig, deps CompactionDeps) CompactionStrategy {
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
		return NewSummarizationStrategy(blockSize, keepLast, deps.Summarize)
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
		return NewHierarchicalStrategy(distant, middle, recent, deps.Summarize)
	default:
		return NewSlidingWindowStrategy(cfg.SlidingWindow.KeepFirst, cfg.SlidingWindow.KeepLast)
	}
}
