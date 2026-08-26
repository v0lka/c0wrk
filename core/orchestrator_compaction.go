package core

import (
	"context"
	"errors"
	"fmt"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
)

// ErrNothingToCompact is returned by CompactConversationHistory when the
// session's conversation history is empty — there is nothing to compact and
// the caller should surface this to the user rather than report success.
var ErrNothingToCompact = errors.New("orchestrator: conversation history is empty — nothing to compact")

// CompactConversationHistory compacts the session's cross-task conversation
// history (o.conversationHistory — the user/assistant dialogue injected into
// every Conductor run as prior conversation) using the named sp4rk strategy
// ("sliding_window" | "summarization" | "hierarchical").
//
// It is the manual-compaction entry point: the UI triggers it on demand, the
// backend session layer ensures no request is in flight first (pause-wait,
// mirroring the pause flow). The compacted history REPLACES the orchestrator's
// history in place, so the next request routes and plans against the compacted
// dialogue. The UI message history is untouched — compaction affects only what
// the LLM sees.
//
// Before/after fill percentages are computed against the model's advertised
// context window (the display basis used by the status bar, mirroring
// emitInitialContextFill's ResolveLocal resolution). On success the method
// emits ContextCompaction (chat card) and a refreshed ContextFill (status bar)
// with the post-compaction values.
//
// Error semantics: an empty history returns ErrNothingToCompact; an unknown
// strategy or a summarization failure returns the error with the history left
// untouched (sp4rk's CompactConversationHistory never mutates its input).
func (o *Orchestrator) CompactConversationHistory(ctx context.Context, strategy string) (beforePercent, afterPercent float64, err error) {
	if len(o.conversationHistory) == 0 {
		return 0, 0, ErrNothingToCompact
	}
	if strategy == "" {
		return 0, 0, errors.New("orchestrator: compaction strategy is required")
	}

	compacted, err := sdkmemory.CompactConversationHistory(ctx, o.conversationHistory, 0, strategy, o.manualCompactionConfig(), o.manualCompactionDeps())
	if err != nil {
		return 0, 0, fmt.Errorf("orchestrator: compacting conversation history: %w", err)
	}

	// Token accounting against the display window (same resolution tier as
	// emitInitialContextFill — network-free ResolveLocal).
	window := o.displayContextWindow()
	beforeTokens, afterTokens := 0, 0
	if o.tokenCounter != nil {
		beforeTokens = o.tokenCounter.CountMessages(o.conversationHistory)
		afterTokens = o.tokenCounter.CountMessages(compacted)
	}
	if window > 0 {
		beforePercent = float64(beforeTokens) / float64(window) * 100
		afterPercent = float64(afterTokens) / float64(window) * 100
	}

	// Swap in the compacted history only after a successful compaction.
	o.conversationHistory = compacted

	o.logInfo("manual context compaction", "strategy", strategy, "before_percent", roundFill(beforePercent), "after_percent", roundFill(afterPercent), "messages", len(compacted))
	o.emitter.ContextCompaction(beforePercent, afterPercent, "")
	// Refresh the status bar with the post-compaction fill relative to the
	// real advertised window ("ok" — the window just shrank by design).
	o.emitter.ContextFill(afterPercent, afterTokens, window, "ok", "")
	return beforePercent, afterPercent, nil
}

// manualCompactionConfig builds the sp4rk strategy config from the
// orchestrator's executor compaction settings (Small-LLM context overrides
// already applied by the builder — the same values buildContextFactory uses
// for per-executor strategies).
func (o *Orchestrator) manualCompactionConfig() sdkmemory.CompactionConfig {
	cc := o.config.Compaction
	return sdkmemory.CompactionConfig{
		SlidingWindow: struct{ KeepFirst, KeepLast int }{
			KeepFirst: cc.SlidingWindow.KeepFirst,
			KeepLast:  cc.SlidingWindow.KeepLast,
		},
		Summarization: struct {
			BlockSize           int
			KeepLast            int
			ObservationTruncate int
		}{
			BlockSize:           cc.Summarization.BlockSize,
			KeepLast:            cc.Summarization.KeepLast,
			ObservationTruncate: cc.ObservationTruncate,
		},
		Hierarchical: struct{ DistantRatio, MiddleRatio, RecentRatio float64 }{
			DistantRatio: cc.Hierarchical.DistantRatio,
			MiddleRatio:  cc.Hierarchical.MiddleRatio,
			RecentRatio:  cc.Hierarchical.RecentRatio,
		},
	}
}

// manualCompactionDeps builds the summarization dependencies for manual
// compaction, mirroring buildContextFactory: the session's tracking caller
// (via o.llm — the logged wrapper) so compaction tokens are counted in session
// totals, the shared token counter for block-size bounding, and the
// deterministic compaction call purpose.
func (o *Orchestrator) manualCompactionDeps() sdkmemory.CompactionDeps {
	return sdkmemory.CompactionDeps{
		TokenCounter:       o.tokenCounter,
		MaxSummarizeTokens: o.config.Compaction.MaxSummarizeTokens,
		Summarize: func(ctx context.Context, blockText string) (string, error) {
			if o.llm == nil {
				return "", errors.New("compaction summarize: LLM caller not available")
			}
			req := llm.ChatRequest{
				Messages: []llm.Message{
					{Role: "system", Content: coreprompts.CompactionSummarize},
					{Role: "user", Content: blockText},
				},
				ReasoningEffort: o.config.ReasoningEffort,
				// Compaction summaries are deterministic calls: no vendor
				// preset, temperature pinned to the family-safe floor.
				CallPurpose: llm.CallPurposeCompaction,
			}
			resp, err := o.llm.Call(ctx, req)
			if err != nil {
				return "", fmt.Errorf("compaction summarize: %w", err)
			}
			return resp.Message.Content, nil
		},
	}
}

// displayContextWindow resolves the active model's advertised context window
// via the network-free local tier (same as emitInitialContextFill). Returns 0
// when unknown.
func (o *Orchestrator) displayContextWindow() int {
	if o.modelRegistry == nil {
		return 0
	}
	meta, _ := o.modelRegistry.ResolveLocal(o.config.Model)
	return meta.ContextWindow
}

// roundFill clamps a fill percentage to [0, 100] for logging.
func roundFill(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
