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
// token estimate) — the dialogue is already under the strategy's structural
// message-count window, so there is nothing to compact. The history is NOT
// swapped and no events are emitted: nothing changed.
var ErrNothingCompacted = errors.New("orchestrator: conversation history already within compaction limits — nothing to compact")

// manualCompactionStrategies is the ordered catalog of manual-compaction
// strategies surfaced to the compact button's menu. The order is stable (it
// matches the frontend COMPACTION_STRATEGIES order) and must stay in sync with
// sp4rk's CompactConversationHistory strategy names.
var manualCompactionStrategies = []string{"sliding_window", "summarization", "hierarchical"}

// CompactionAvailability is the per-strategy prediction surfaced to the UI: is
// this strategy currently available (would it actually shrink the dialogue),
// how many tokens it would reclaim, and whether that reclaim is exact
// (sliding_window — a dry run) or a forecast (LLM-backed strategies).
type CompactionAvailability struct {
	Strategy      string `json:"strategy"`
	Available     bool   `json:"available"`
	ReclaimTokens int    `json:"reclaim_tokens"`
	Exact         bool   `json:"exact"`
}

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
// The strategy runs in sp4rk's PURELY STRUCTURAL mode: each strategy applies
// its configured message-count window exactly, with no token-budget sizing and
// no token trim. A history the strategy leaves unchanged (too short for its
// window) is returned verbatim → ErrNothingCompacted. There is no "target
// fill" to compact toward — compaction reclaims whatever the strategy's window
// reclaims, so the button's availability is predicted per strategy by
// ManualCompactionAvailability (sp4rk PredictCompaction), and the compact menu
// offers exactly the strategies whose prediction says they will shrink the
// dialogue.
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

	// Resolve the token bases BEFORE the compaction call; the effective base
	// feeds the EMITTER calls and the display base the RETURNED percentages.
	effectiveMax, displayMax := o.contextBases()

	// Capture the verbatim-token anchor from the prediction BEFORE compacting:
	// the EWMA calibration needs it to isolate the summarized portion
	// (summarizedInput = before − verbatim) from the kept-verbatim portion.
	pred, predErr := sdkmemory.PredictCompaction(history, strategy, o.manualCompactionConfig(), o.manualCompactionDeps())
	if predErr != nil {
		return 0, 0, fmt.Errorf("orchestrator: predicting compaction: %w", predErr)
	}
	if !pred.WillCompact {
		return 0, 0, ErrNothingCompacted
	}

	compacted, err := sdkmemory.CompactConversationHistory(ctx, history, strategy, o.manualCompactionConfig(), o.manualCompactionDeps())
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
	// count, same token estimate). Return the sentinel WITHOUT swapping the
	// history or emitting: both are exactly what they were before the call.
	// The token counter is a hard prerequisite: without one the token equality
	// is vacuous (0 == 0) and the check degrades to length-only — a real
	// compaction that only rewrote message CONTENT at the same length would be
	// misdetected as a no-op and silently dropped. So with no counter the
	// detection is skipped and the compacted history is always swapped in (an
	// identical result is a harmless idempotent swap).
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

	// Refine the compression-ratio forecast with the observed result (LLM
	// strategies only — sliding_window is exact and needs no calibration).
	o.refineCompactionForecast(strategy, history, compacted, pred.VerbatimTokens)

	// The log carries the display basis — the numbers the user sees.
	o.logInfo("manual context compaction",
		"strategy", strategy,
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

// ManualCompactionAvailability reports, for EVERY manual-compaction strategy,
// whether applying it right now would actually shrink the conversation history
// (a pure prediction — no strategy runs, nothing is swapped, no events fire).
// The session layer surfaces it as
// SessionRuntimeStatus.CompactionAvailability / CompactionFinishedEventData.
// CompactionAvailability so the UI can enable the compact button and disable
// (with a reason) exactly the strategies that would not shrink the dialogue.
//
// Each strategy's Available verdict is EXACT (sp4rk PredictCompaction's
// WillCompact — a non-empty summarized/omitted zone); the ReclaimTokens is an
// exact dry-run for sliding_window and a forecast (from the EWMA-calibrated
// CompactionForecast) for the LLM-backed strategies — the forecast never
// affects availability. Safe to call from any goroutine: the history is read
// via historySnapshot and the forecast via compactionForecastSnapshot.
func (o *Orchestrator) ManualCompactionAvailability() []CompactionAvailability {
	history := o.historySnapshot()
	cfg := o.manualCompactionConfig()
	deps := o.manualCompactionDeps()

	out := make([]CompactionAvailability, 0, len(manualCompactionStrategies))
	for _, strategy := range manualCompactionStrategies {
		avail := CompactionAvailability{Strategy: strategy}
		pred, err := sdkmemory.PredictCompaction(history, strategy, cfg, deps)
		if err == nil {
			avail.Available = pred.WillCompact
			avail.ReclaimTokens = pred.Reclaim
			avail.Exact = pred.Exact
		}
		// On a prediction error (should not happen for the fixed strategy
		// catalog) the strategy stays unavailable — fail-closed, matching
		// CompactConversationHistory's unknown-strategy error.
		out = append(out, avail)
	}
	return out
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
// totals, the shared token counter for block-size bounding, the deterministic
// compaction call purpose, and the EWMA-calibrated compression-ratio forecast
// (prediction-only — CompactConversationHistory ignores it).
func (o *Orchestrator) manualCompactionDeps() sdkmemory.CompactionDeps {
	return sdkmemory.CompactionDeps{
		TokenCounter:       o.tokenCounter,
		MaxSummarizeTokens: o.config.Compaction.MaxSummarizeTokens,
		Forecast:           o.compactionForecastSnapshot(),
		Summarize: func(ctx context.Context, blockText string) (string, error) {
			if o.llm == nil {
				return "", errors.New("compaction summarize: LLM caller not available")
			}
			req := llm.ChatRequest{
				Messages: []llm.Message{
					{Role: "system", Content: coreprompts.CompactionSummarize},
					{Role: "user", Content: blockText},
				},
				ReasoningEffort: o.currentReasoningEffort(),
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
	meta, _ := o.modelRegistry.ResolveLocal(o.currentModel())
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

// resolveCompactionForecast maps a BuilderCompactionForecast seed onto a
// sp4rk CompactionForecast, falling back to sp4rk's conservative defaults for
// zero fields. This runs once at construction (the seed); the live value is
// refined on o.compactionForecast after each manual compaction.
func resolveCompactionForecast(seed BuilderCompactionForecast) sdkmemory.CompactionForecast {
	f := sdkmemory.CompactionForecast{
		SummarizationRatio:       seed.SummarizationRatio,
		HierarchicalDistantRatio: seed.HierarchicalDistantRatio,
		HierarchicalMiddleRatio:  seed.HierarchicalMiddleRatio,
	}
	// Zero fields fall back to the same conservative defaults the prediction
	// applies (0.3 / 0.15 / 0.3), so a zero-valued seed equals "use defaults".
	if f.SummarizationRatio <= 0 {
		f.SummarizationRatio = 0.3
	}
	if f.HierarchicalDistantRatio <= 0 {
		f.HierarchicalDistantRatio = 0.15
	}
	if f.HierarchicalMiddleRatio <= 0 {
		f.HierarchicalMiddleRatio = 0.3
	}
	return f
}

// compactionForecastSnapshot returns the current EWMA-calibrated forecast.
// Safe to call from any goroutine.
func (o *Orchestrator) compactionForecastSnapshot() sdkmemory.CompactionForecast {
	o.forecastMu.Lock()
	defer o.forecastMu.Unlock()
	return o.compactionForecast
}

// CompactionForecast returns the current EWMA-calibrated compression-ratio
// forecast (the session layer persists it after each manual compaction so the
// calibration survives restarts). Safe to call from any goroutine.
func (o *Orchestrator) CompactionForecast() sdkmemory.CompactionForecast {
	return o.compactionForecastSnapshot()
}

// SetCompactionForecast replaces the forecast (the session-restore path loads
// a previously persisted calibration into the orchestrator before it accepts
// requests). Safe to call from any goroutine.
func (o *Orchestrator) SetCompactionForecast(f sdkmemory.CompactionForecast) {
	o.forecastMu.Lock()
	defer o.forecastMu.Unlock()
	o.compactionForecast = f
}

// compactionForecastEWMAAlpha is the EWMA smoothing factor for refining the
// compression-ratio forecast after each manual compaction. A lower value
// tracks the long-run average more slowly (more stable, less reactive to one
// outlier); 0.3 is a conventional default.
const compactionForecastEWMAAlpha = 0.3

// refineCompactionForecast updates the EWMA-calibrated compression-ratio
// forecast from the observed result of a just-completed manual compaction.
// sliding_window is exact (no LLM) and needs no calibration. For the LLM
// strategies each forecast field is calibrated from its OWN zone's observed
// ratio — the summarization strategy from its whole summarized portion, and
// the hierarchical strategy's distant and middle zones independently (the two
// zones compress at different rates: one aggressive summary over a large block
// vs. per-block summaries, so a single aggregate ratio would erase the
// distinction between them). A zone with no summarized input leaves its field
// untouched.
func (o *Orchestrator) refineCompactionForecast(strategy string, history, compacted []llm.Message, verbatimTokens int) {
	if strategy == "sliding_window" || o.tokenCounter == nil {
		return
	}
	afterTokens := o.tokenCounter.CountMessages(compacted)

	o.forecastMu.Lock()
	defer o.forecastMu.Unlock()
	blend := func(current *float64, summarizedInput, summaryOutput int) {
		if summarizedInput <= 0 {
			return // nothing was summarized — nothing to learn
		}
		if summaryOutput < 0 {
			summaryOutput = 0
		}
		// observed ratio = what fraction of the summarized input the summaries
		// occupied. Clamp to (0, 1]: a summary is never smaller than 0 tokens
		// and never larger than its input (a ratio > 1 would predict growth).
		observed := float64(summaryOutput) / float64(summarizedInput)
		if observed <= 0 {
			observed = 0.01
		}
		if observed > 1 {
			observed = 1
		}
		*current += compactionForecastEWMAAlpha * (observed - *current)
	}

	switch strategy {
	case "summarization":
		summarizedInput := o.tokenCounter.CountMessages(history) - verbatimTokens
		summaryOutput := afterTokens - verbatimTokens
		blend(&o.compactionForecast.SummarizationRatio, summarizedInput, summaryOutput)
	case "hierarchical":
		distant, middle := sdkmemory.ConversationHierarchicalZones(len(history), o.manualCompactionConfig())
		// The compacted output is [distant summary (1)] + [middle summaries] +
		// [recent verbatim]: the distant zone always collapses to exactly ONE
		// summary (the first message), so its observed ratio is the first
		// message's tokens over the whole distant zone's tokens. The middle
		// summaries are everything after that first summary and before the
		// verbatim tail — back them out by subtraction rather than re-deriving
		// the block count.
		distantInput := o.tokenCounter.CountMessages(history[:distant])
		distantOutput := o.tokenCounter.CountMessages(compacted[:1])
		middleInput := o.tokenCounter.CountMessages(history[distant : distant+middle])
		middleOutput := afterTokens - verbatimTokens - distantOutput
		blend(&o.compactionForecast.HierarchicalDistantRatio, distantInput, distantOutput)
		blend(&o.compactionForecast.HierarchicalMiddleRatio, middleInput, middleOutput)
	}
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
