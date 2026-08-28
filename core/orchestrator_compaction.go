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

// ErrNothingCompacted is returned by CompactConversationHistory when the
// strategy left the conversation history unchanged (same message count and
// token estimate) — the dialogue already fits within the compaction limits,
// so there is nothing to compact. The history is NOT swapped and no events
// are emitted: nothing changed.
var ErrNothingCompacted = errors.New("orchestrator: conversation history already within compaction limits — nothing to compact")

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
// Percentages are computed in two bases. The EMITTED values
// (ContextCompaction chat card + the refreshed ContextFill status bar) use
// the effective token base — the advertised window minus the model's output
// limit minus the safety margin, the same basis the executor reports (sp4rk
// ContextWindow.EffectiveMax): the session emitter's ContextCompaction
// scales effective-based percentages to the display basis (the real
// advertised window) itself, so pre-scaling those would double-shrink the
// card (~×0.7). The RETURNED values use the display base (the advertised
// window) directly, because the caller persists them into the marker row
// and the compaction_finished event and the frontend renders the marker's
// metadata verbatim on reload — the reloaded card must match the live one.
//
// Error semantics: an empty history returns ErrNothingToCompact; a no-op
// compaction (history unchanged — same message count and token estimate;
// the detection requires a token counter, without which a same-length
// content change would be indistinguishable from a no-op and is therefore
// always treated as a real compaction) returns ErrNothingCompacted without
// swapping or emitting; an unknown strategy or a summarization failure
// returns the error with the history left untouched (sp4rk's
// CompactConversationHistory never mutates its input).
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

	// Token accounting. Two bases share one token count: the effective base
	// (the budget the executor's compaction logic manages against — mirrors
	// sp4rk ContextWindow.EffectiveMax) feeds the EMITTER calls, while the
	// display base (the advertised window) feeds the RETURNED percentages —
	// the session emitter scales effective-based values to the display basis
	// itself, so pre-scaling the emitted ones would double-shrink the card
	// (~×0.7), whereas the persisted marker/compaction_finished numbers are
	// rendered verbatim on reload and must already be display-based.
	effectiveMax, displayMax := o.contextBases()
	beforeTokens, afterTokens := 0, 0
	if o.tokenCounter != nil {
		beforeTokens = o.tokenCounter.CountMessages(o.conversationHistory)
		afterTokens = o.tokenCounter.CountMessages(compacted)
	}

	// No-op detection: the strategy left the history unchanged (same message
	// count, same token estimate) — the dialogue already fits within the
	// compaction limits. Return the sentinel WITHOUT swapping the history or
	// emitting: both are exactly what they were before the call. The token
	// counter is a hard prerequisite: without one the token equality is
	// vacuous (0 == 0) and the check degrades to length-only — a real
	// compaction that only rewrote message CONTENT at the same length would
	// be misdetected as a no-op and silently dropped. So with no counter the
	// detection is skipped and the compacted history is always swapped in
	// (an identical result is a harmless idempotent swap).
	if o.tokenCounter != nil && len(compacted) == len(o.conversationHistory) && afterTokens == beforeTokens {
		return 0, 0, ErrNothingCompacted
	}

	// Percentages in the two bases sharing the one token count above: the
	// emitter basis (effective max) and the return basis (display window).
	// Emission is NOT gated on a known window — an unknown window reports
	// zero percents, the same "unknown" semantics as the fill path.
	beforeEff, afterEff := 0.0, 0.0
	if effectiveMax > 0 {
		beforeEff = float64(beforeTokens) / float64(effectiveMax) * 100
		afterEff = float64(afterTokens) / float64(effectiveMax) * 100
	}
	if displayMax > 0 {
		beforePercent = float64(beforeTokens) / float64(displayMax) * 100
		afterPercent = float64(afterTokens) / float64(displayMax) * 100
	}

	// Swap in the compacted history only after a successful compaction.
	o.conversationHistory = compacted

	// The log carries the display basis — the numbers the user sees.
	o.logInfo("manual context compaction", "strategy", strategy, "before_percent", roundFill(beforePercent), "after_percent", roundFill(afterPercent), "messages", len(compacted))
	o.emitter.ContextCompaction(beforeEff, afterEff, "")
	// Refresh the status bar with the post-compaction fill. maxTokens is the
	// effective max (the executor basis); the emitter's display override
	// recomputes the user-facing percent against the real advertised window
	// when one is known ("ok" — the window just shrank by design).
	o.emitter.ContextFill(afterEff, afterTokens, effectiveMax, "ok", "")
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

// contextBases resolves the two token bases manual-compaction percentages
// are computed against:
//   - the effective base: the model's advertised context window minus its
//     output limit minus the safety margin — the same integer math sp4rk's
//     ContextWindow.EffectiveMax uses (safetyMargin = window * percent /
//     100). Feeds the emitter calls, which the session emitter rescales to
//     the display basis itself.
//   - the display base: the advertised window itself — what the status bar
//     presents and what the persisted marker / compaction_finished
//     percentages must use so the reloaded compaction card matches the live
//     one.
//
// Resolution is the network-free local tier (same as emitInitialContextFill).
// Both are 0 when the window is unknown; the effective base alone is 0 when
// the reserves consume the window entirely — callers then report unknown
// (zero) percentages instead of nonsensical ones.
func (o *Orchestrator) contextBases() (effectiveMax, displayMax int) {
	if o.modelRegistry == nil {
		return 0, 0
	}
	meta, _ := o.modelRegistry.ResolveLocal(o.config.Model)
	window := meta.ContextWindow
	if window <= 0 {
		return 0, 0
	}
	safetyPercent := o.config.Compaction.SafetyMarginPercent
	if safetyPercent <= 0 {
		// Mirror sp4rk's defaultSafetyMargin: a zero config falls back to 5
		// so the effective base matches the executor's ContextWindow.
		safetyPercent = 5
	}
	effective := window - meta.OutputLimit - window*safetyPercent/100
	if effective <= 0 {
		return 0, window
	}
	return effective, window
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
