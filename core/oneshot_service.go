package core

import (
	"github.com/v0lka/sp4rk/llm"
)

// The oneshot service-call policy: the fixed per-kind contract for c0wrk's
// auxiliary LLM calls (title, commit message, prompt-optimizer
// extract/rewrite, compaction summarization). Every call in this class goes
// through the sp4rk oneshot client, and its sampling and reasoning come from
// THIS policy — not from the Model Profiles reasoningEffort seed and not from
// ad-hoc per-site values.
//
// Precedence for these calls: oneshot policy > profile reasoningEffort seed >
// family default. The seed (OrchestratorBuilder.reasoningEffort, seeded from
// model_profiles.sampling.reasoning_effort) keeps governing the main
// execution loop (executor, planner, reflector, router); service calls
// deliberately bypass it so a "deep reasoning" profile cannot inflate short
// auxiliary compositions.
//
// Temperatures are policy-pinned explicit values — an explicit value wins
// over any profile, and the Router strips sampling for models that
// authoritatively cannot take the parameter, so the pins are capability-safe
// (no 400s on reasoning-locked endpoints). Compaction summaries stay on the
// router's deterministic profile instead (CallPurposeCompaction, no explicit
// temperature) — the same posture as before this policy existed.
var (
	oneshotTempTitle   = 0.3
	oneshotTempCommit  = 0.3
	oneshotTempExtract = 0.3
	oneshotTempRewrite = 0.5
)

// Reasoning tiers of the oneshot service policy, resolved per active model
// through serviceReasoningEffort:
//   - title / commit message: off — short structured compositions; reasoning
//     adds latency and tokens without improving them, and on effort-seeded
//     families the server default is the strongest effort.
//   - prompt-optimizer extract/rewrite: minimal — the cheapest non-disabled
//     effort; the rewrite benefits from a sliver of deliberation.
//   - compaction summarization (both sites): minimal.
const (
	oneshotTierTitle      = llm.ReasoningTierOff
	oneshotTierCommit     = llm.ReasoningTierOff
	oneshotTierOptimize   = llm.ReasoningTierMinimal
	oneshotTierCompaction = llm.ReasoningTierMinimal
)

// serviceReasoningEffort resolves an oneshot service-call reasoning tier into
// the native wire spelling for the given (bare) model name: catalog-first
// family detection — llm.ResolveBuiltInModel is authoritative for models it
// knows, including architecture aliases a name-based detection cannot see
// (e.g. "Bonsai 2 27B" → qwen) — with llm.DetectFamily as the name-based
// fallback, then llm.ReasoningForCall. It returns "" (send no reasoning field
// at all) for an empty model or an unknown model/family, the same
// fail-closed-no-field contract llm.ReasoningForCall documents.
func serviceReasoningEffort(model string, tier llm.ReasoningTier) string {
	if model == "" || tier == "" {
		return ""
	}
	// Catalog-first family detection; the name-based fallback stands when the
	// catalog does not know the model (or knows it without a family).
	family := string(llm.DetectFamily(model))
	if meta, ok := llm.ResolveBuiltInModel(model); ok && meta.Family != "" {
		family = meta.Family
	}
	return llm.ReasoningForCall(family, model, tier)
}

// bareActiveModel returns the router's active model with any provider prefix
// stripped; "" for a nil router.
func bareActiveModel(router *llm.Router) string {
	if router == nil {
		return ""
	}
	return llm.BareModel(router.ActiveModel())
}

// compactionSummarizeContent passes the summary text through unchanged: any
// content is a valid summary (the compaction strategy owns its quality
// judgment), so the oneshot nudge loop never engages for compaction; a nil
// response yields an empty summary. Transport errors are returned as-is.
func compactionSummarizeContent(resp *llm.ChatResponse) (string, error) {
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}
