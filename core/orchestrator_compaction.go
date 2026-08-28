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
// token estimate) — the dialogue already fits within the manual-compaction
// budget: the target share of the effective context window
// (Compaction.ManualTargetPercent, default 30%) when the window is known, or
// the strategy's message-count limits in the unknown-window fallback. There
// is nothing to compact; the history is NOT swapped and no events are
// emitted: nothing changed.
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
// The strategy runs in sp4rk's token-budget mode whenever the model's
// effective context window is known: the budget is the target share of that
// window (Compaction.ManualTargetPercent, default 30%) — the fill level the
// compaction aims to bring the history down to. A history already within the
// budget is returned verbatim regardless of its message count (→
// ErrNothingCompacted); an over-budget one is compacted so the result fits
// the budget, each strategy sizing its verbatim zones as budget shares (the
// single documented exception: a last message that alone exceeds the budget
// is still kept — the never-remove-last invariant outranks the ceiling). An
// unknown window (or a nil token counter) passes budget 0 and sp4rk applies
// its historical message-count-based behavior unchanged.
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
	// The compaction runs on a private snapshot: this method executes on the
	// manual-compaction flow's goroutine while the history's writers run on
	// the request goroutine (and the restore path), so every read below goes
	// through historyMu (snapshot in, setConversationHistory out).
	history := o.historySnapshot()
	if len(history) == 0 {
		return 0, 0, ErrNothingToCompact
	}
	if strategy == "" {
		return 0, 0, errors.New("orchestrator: compaction strategy is required")
	}

	// Resolve the token bases BEFORE the compaction call: the effective base
	// doubles as the manual-compaction budget — the strategy runs in sp4rk's
	// token-budget mode against the target share of the window the executor
	// manages against. An unknown window (or a target that rounds down to a
	// zero budget on a tiny window) yields budget 0: sp4rk's historical
	// message-count-based fallback, byte-for-byte the pre-budget behavior.
	effectiveMax, displayMax := o.contextBases()
	targetPercent := o.manualCompactionTargetPercent()
	budgetTokens := 0
	if effectiveMax > 0 {
		budgetTokens = effectiveMax * targetPercent / 100
	}

	compacted, err := sdkmemory.CompactConversationHistory(ctx, history, budgetTokens, strategy, o.manualCompactionConfig(), o.manualCompactionDeps())
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
	// rendered verbatim on reload and must already be display-based. (Both
	// bases were resolved above, before the compaction call.)
	beforeTokens, afterTokens := 0, 0
	if o.tokenCounter != nil {
		beforeTokens = o.tokenCounter.CountMessages(history)
		afterTokens = o.tokenCounter.CountMessages(compacted)
	}

	// No-op detection: the strategy left the history unchanged (same message
	// count, same token estimate) — the dialogue already fits within the
	// manual-compaction budget (or, in the unknown-window fallback, the
	// strategy's message-count limits). Return the sentinel WITHOUT swapping the history or
	// emitting: both are exactly what they were before the call. The token
	// counter is a hard prerequisite: without one the token equality is
	// vacuous (0 == 0) and the check degrades to length-only — a real
	// compaction that only rewrote message CONTENT at the same length would
	// be misdetected as a no-op and silently dropped. So with no counter the
	// detection is skipped and the compacted history is always swapped in
	// (an identical result is a harmless idempotent swap).
	if o.tokenCounter != nil && len(compacted) == len(history) && afterTokens == beforeTokens {
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
	o.setConversationHistory(compacted)

	// The log carries the display basis — the numbers the user sees — plus
	// the target the budget was derived from (budget 0 documents the
	// count-based fallback on an unknown window).
	o.logInfo("manual context compaction",
		"strategy", strategy,
		"target_percent", targetPercent,
		"budget_tokens", budgetTokens,
		"before_percent", roundFill(beforePercent),
		"after_percent", roundFill(afterPercent),
		"messages", len(compacted))
	o.emitter.ContextCompaction(beforeEff, afterEff, "")
	// Refresh the status bar with the post-compaction fill. maxTokens is the
	// effective max (the executor basis); the emitter's display override
	// recomputes the user-facing percent against the real advertised window
	// when one is known ("ok" — the window just shrank by design).
	o.emitter.ContextFill(afterEff, afterTokens, effectiveMax, "ok", "")
	return beforePercent, afterPercent, nil
}

// defaultManualTargetPercent is the manual-compaction target fallback for an
// unset (zero) Compaction.ManualTargetPercent — the core mirror carries no
// builder-time default, so the consumer resolves it here, mirroring backend
// config's ApplyDefaults.
const defaultManualTargetPercent = 30

// manualCompactionTargetPercent resolves the manual-compaction target: the
// context-fill percentage of the effective window a user-triggered compaction
// aims to compact the history down to (the SDK budget is effectiveMax ×
// target / 100). Zero (unset) falls back to defaultManualTargetPercent.
func (o *Orchestrator) manualCompactionTargetPercent() int {
	if p := o.config.Compaction.ManualTargetPercent; p > 0 {
		return p
	}
	return defaultManualTargetPercent
}

// ManualCompactionWouldNoOp reports whether a manual compaction of the
// CURRENT conversation history is guaranteed to leave it unchanged — the
// dialogue already fits the manual-compaction target (or is too short for
// any strategy to shrink). The session layer surfaces it as
// SessionRuntimeStatus.CompactionNoOp / CompactionFinishedEventData.
// CompactionNoOp so the UI can disable the compact button with an
// explanatory tooltip. It is a pure prediction: no strategy runs, nothing is
// swapped, no events fire. Safe to call from any goroutine (the runtime
// status poll runs on a Wails-RPC goroutine while a request may be finishing
// — the history is read via historySnapshot).
//
// The mode mirrors CompactConversationHistory's budget resolution exactly:
// with a known effective window AND a token counter, the budget is
// effectiveMax × manualCompactionTargetPercent / 100 and a history whose
// token count fits the budget is returned verbatim by every strategy (the
// documented budget-mode contract) — that is a no-op by definition. Any
// other setup falls to the count-mode fallback (unknown window, nil counter,
// or a window so tiny the budget rounds to zero), predicted conservatively
// by length: a history at or below manualCompactionNoOpLength — the largest
// length every strategy returns verbatim — is a guaranteed no-op, while
// longer histories are left enabled (fail-open: a pointless click reports
// the existing nothing_compacted outcome). An empty history trivially
// qualifies — there is nothing to compact at all.
func (o *Orchestrator) ManualCompactionWouldNoOp() bool {
	history := o.historySnapshot()
	if len(history) == 0 {
		return true
	}
	if o.tokenCounter != nil {
		if effectiveMax, _ := o.contextBases(); effectiveMax > 0 {
			budgetTokens := effectiveMax * o.manualCompactionTargetPercent() / 100
			if budgetTokens > 0 {
				return o.tokenCounter.CountMessages(history) <= budgetTokens
			}
		}
	}
	return len(history) <= o.manualCompactionNoOpLength()
}

// manualCompactionNoOpLength is the count-mode fallback bound for
// ManualCompactionWouldNoOp: the largest history length that EVERY strategy
// is guaranteed to return verbatim in sp4rk's fallback mode (budget 0 /
// nil counter), i.e. the minimum of the strategies' verbatim floors — a
// length only one strategy shrinks is not a guaranteed no-op, because the
// user may pick any strategy in the compact menu:
//
//   - sliding_window: verbatim while len ≤ KeepFirst'+KeepLast' (its count
//     window);
//   - summarization: verbatim while len ≤ KeepLast' (everything older is
//     summarized);
//   - hierarchical: verbatim while both summary zones are empty (see
//     hierarchicalNoOpLength).
//
// Primed values use the same zero-value defaults sp4rk applies
// (memory/compaction_conversation.go), so the bound tracks the effective
// strategy config: with c0wrk's defaults it evaluates to 2 (the
// hierarchical floor), and exotic configs (e.g. summarization keep_last: 1)
// tighten it instead of wrongly disabling the button for a compactable
// history. The bound never exceeds any strategy's floor, so claiming a
// no-op at or below it is always sound (fail-open otherwise).
func (o *Orchestrator) manualCompactionNoOpLength() int {
	cc := o.config.Compaction

	keepFirst := cc.SlidingWindow.KeepFirst
	if keepFirst <= 0 {
		keepFirst = 3
	}
	keepLast := cc.SlidingWindow.KeepLast
	if keepLast <= 0 {
		keepLast = 10
	}
	slidingFloor := keepFirst + keepLast

	summarizationKeepLast := cc.Summarization.KeepLast
	if summarizationKeepLast <= 0 {
		summarizationKeepLast = 5
	}

	hierarchicalFloor := hierarchicalNoOpLength(cc.Hierarchical.DistantRatio, cc.Hierarchical.MiddleRatio)

	return min(slidingFloor, summarizationKeepLast, hierarchicalFloor)
}

// hierarchicalNoOpLength mirrors sp4rk's fallback-mode hierarchical zone math
// (memory/compaction_conversation.go: compactConversationHierarchical) to
// find the largest history length whose distant+middle zones are both empty —
// the strategy returns such histories verbatim ("nothing to summarize").
// Zones are int(n·ratio); the zone-shrink clamps cannot empty them for
// n ≥ 2 once either zone count reaches 1, so the first n where
// int(n·distant)+int(n·middle) ≥ 1 marks the boundary (both counts are
// monotone in n). Ratios ≤ 0 fall back to sp4rk's defaults (0.4/0.3), the
// same clamp the SDK applies. A single message is never compacted (the
// clamps empty both zones), so the floor starts at 1; the search is capped —
// a ratio so tiny that no n ≤ cap fills a zone yields cap, which only
// under-claims (fail-open) and is dominated by the other strategies' floors
// in the min() long before that.
func hierarchicalNoOpLength(distantRatio, middleRatio float64) int {
	distant, middle := distantRatio, middleRatio
	if distant <= 0 {
		distant = 0.4
	}
	if middle <= 0 {
		middle = 0.3
	}
	const searchCap = 128
	for n := 2; n <= searchCap; n++ {
		if int(float64(n)*distant)+int(float64(n)*middle) >= 1 {
			return n - 1
		}
	}
	return searchCap
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
