package core

import (
	"context"
	"errors"
	"fmt"

	coreprompts "github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/llm"
	sdkmemory "github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/oneshot"
)

// ErrNothingToCompact is returned by CompactConversationHistory for an empty
// history. See specs/domains/memory/compaction.md ("Honest no-op").
var ErrNothingToCompact = errors.New("orchestrator: conversation history is empty — nothing to compact")

// ErrNothingCompacted is returned by CompactConversationHistory when the
// strategy's structural window leaves the history unchanged: nothing is
// swapped and nothing is emitted. See specs/domains/memory/compaction.md
// ("Honest no-op").
var ErrNothingCompacted = errors.New("orchestrator: conversation history already within compaction limits — nothing to compact")

// manualCompactionStrategies is the ordered compact-menu catalog; the order
// must stay in sync with the frontend COMPACTION_STRATEGIES and sp4rk's
// strategy names.
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
// history (o.conversationHistory) in place with the named sp4rk strategy —
// the manual-compaction entry point the UI triggers between requests.
// Structural mode, the effective/display percentage bases, the deliberate
// absence of a ContextCompaction emission, and the error semantics are
// specified in specs/domains/memory/compaction.md ("Manual Context Compaction").
func (o *Orchestrator) CompactConversationHistory(ctx context.Context, strategy string) (beforePercent, afterPercent float64, err error) {
	// This runs on the manual-compaction flow's goroutine while history
	// writers run on the request goroutine: snapshot in, setConversationHistory
	// out, everything under historyMu.
	history := o.historySnapshot()
	if len(history) == 0 {
		return 0, 0, ErrNothingToCompact
	}
	if strategy == "" {
		return 0, 0, errors.New("orchestrator: compaction strategy is required")
	}

	effectiveMax, displayMax := o.contextBases()

	// Verbatim-token anchor captured before compacting: the EWMA calibration
	// needs it to isolate the summarized portion (before − verbatim).
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

	// Two bases share one token count (see contextBases): the effective base
	// feeds the EMITTER calls, the display base the RETURNED percentages.
	beforeTokens, afterTokens := 0, 0
	if o.tokenCounter != nil {
		beforeTokens = o.tokenCounter.CountMessages(history)
		afterTokens = o.tokenCounter.CountMessages(compacted)
	}

	// No-op detection needs a token counter: without one the token equality
	// is vacuous and a same-length content rewrite would be misdetected, so
	// the check is skipped and the (identical) history is harmlessly swapped.
	if o.tokenCounter != nil && len(compacted) == len(history) && afterTokens == beforeTokens {
		return 0, 0, ErrNothingCompacted
	}

	// Returned percents use the display base (persisted into the marker row
	// and compaction_finished); the status-bar refresh uses the effective
	// base and reports zero percents when the window is unknown.
	afterEff := 0.0
	if effectiveMax > 0 {
		afterEff = float64(afterTokens) / float64(effectiveMax) * 100
	}
	if displayMax > 0 {
		beforePercent = float64(beforeTokens) / float64(displayMax) * 100
		afterPercent = float64(afterTokens) / float64(displayMax) * 100
	}

	o.setConversationHistory(compacted)

	// Refine the forecast (LLM strategies only; sliding_window is exact).
	o.refineCompactionForecast(strategy, history, compacted, pred.VerbatimTokens)

	// The log carries the display basis — the numbers the user sees.
	o.logInfo("manual context compaction",
		"strategy", strategy,
		"before_percent", roundFill(beforePercent),
		"after_percent", roundFill(afterPercent),
		"messages", len(compacted))
	// Drop any dynamic step scope first so the refresh cannot inherit a
	// foreign plan_step_id (transient — the conductor re-scopes on start).
	o.resetDynamicStepScope()
	// No ContextCompaction emission (specified in compaction.md): the manual
	// card lives in compaction_finished + the marker row; auto compactions
	// are the only ContextCompaction emitters.
	// Refresh the status bar on the effective basis; the emitter's display
	// override rescales it when the advertised window is known.
	o.emitter.ContextFill(afterEff, afterTokens, effectiveMax, "ok", "")
	return beforePercent, afterPercent, nil
}

// ManualCompactionAvailability predicts, per manual-compaction strategy,
// whether it would shrink the current history — a pure prediction (nothing
// runs, swaps, or emits) surfaced to the compact menu. Verdict/reclaim
// semantics: specs/domains/memory/compaction.md ("Per-strategy availability").
// Goroutine-safe via historySnapshot/compactionForecastSnapshot.
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
		// Prediction error: fail closed to unavailable (fixed strategy catalog).
		out = append(out, avail)
	}
	return out
}

// manualCompactionConfig builds the sp4rk strategy config from the executor
// compaction settings — the same builder-applied values (incl. Model Profiles
// context overrides) buildContextFactory uses per executor.
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

// manualCompactionDeps builds the summarization dependencies mirroring
// buildContextFactory: session tracking caller (tokens counted in session
// totals), shared token counter, deterministic call purpose, and the
// EWMA-calibrated forecast (prediction-only).
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
				// Oneshot service policy: minimal tier, resolved from the
				// session's ACTIVE model — profile seeds don't reach service calls.
				ReasoningEffort: serviceReasoningEffort(o.currentModel(), oneshotTierCompaction),
				// Compaction summaries are deterministic calls: no vendor
				// preset, temperature pinned to the family-safe floor.
				CallPurpose: llm.CallPurposeCompaction,
			}
			summary, err := serviceCall(ctx, o.serviceMetrics, o.logger, ServiceKindCompactionSummary, o.currentModel(), o.llm, req, compactionSummarizeContent, oneshot.Options[string]{})
			if err != nil {
				return "", fmt.Errorf("compaction summarize: %w", err)
			}
			return summary, nil
		},
	}
}

// contextBases resolves the two token bases manual-compaction percentages are
// computed against: the effective base (advertised window − output limit −
// safety margin, sp4rk ContextWindow.EffectiveMax math) for the emitter calls,
// and the display base (the advertised window) for the persisted/returned
// percentages. Basis rationale: specs/domains/memory/compaction.md
// ("Manual Context Compaction"). Network-free local resolution; both are 0
// when the window is unknown, the effective base alone when the reserves
// consume it entirely (callers then report zero percents).
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

// resolveCompactionForecast maps the config seed onto a sp4rk forecast at
// construction (zero fields take the conservative defaults); the live value
// is refined on o.compactionForecast after each manual compaction.
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

// compactionForecastSnapshot returns the current forecast under forecastMu.
func (o *Orchestrator) compactionForecastSnapshot() sdkmemory.CompactionForecast {
	o.forecastMu.Lock()
	defer o.forecastMu.Unlock()
	return o.compactionForecast
}

// CompactionForecast returns the current EWMA-calibrated forecast (persisted
// by the session layer after each manual compaction). Goroutine-safe.
func (o *Orchestrator) CompactionForecast() sdkmemory.CompactionForecast {
	return o.compactionForecastSnapshot()
}

// SetCompactionForecast replaces the forecast (session-restore loads the
// persisted calibration). Goroutine-safe.
func (o *Orchestrator) SetCompactionForecast(f sdkmemory.CompactionForecast) {
	o.forecastMu.Lock()
	defer o.forecastMu.Unlock()
	o.compactionForecast = f
}

// compactionForecastEWMAAlpha is the EWMA smoothing factor for forecast
// refinement (lower = more stable, less outlier-reactive); 0.3 is conventional.
const compactionForecastEWMAAlpha = 0.3

// refineCompactionForecast calibrates the forecast from a just-completed
// manual compaction (EWMA; per-zone observed ratios — sliding_window is exact
// and never calibrated). Zone mechanics: specs/domains/memory/compaction.md.
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
