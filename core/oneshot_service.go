package core

import (
	"strings"

	"github.com/v0lka/sp4rk/llm"
)

// serviceOutputTokenBudget raises a sub-2048 positive output ceiling to 2048
// when reasoning stays enabled; zero stays unspecified. See specs/domains/llm-providers.md (service calls).
func serviceOutputTokenBudget(maxTokens int, effort string) int {
	const reasoningOutputFloor = 2048
	if maxTokens > 0 && maxTokens < reasoningOutputFloor && effort != "" &&
		!strings.EqualFold(effort, "off") && !strings.EqualFold(effort, "none") {
		return reasoningOutputFloor
	}
	return maxTokens
}

// The oneshot service-call policy pins sampling and reasoning for every
// auxiliary LLM call, overriding the Model Profiles reasoningEffort seed —
// precedence: oneshot policy > profile seed > family default; temperature pins
// are capability-safe (the Router strips them where the model can't take them).
// See specs/domains/model-profiles.md (oneshot service calls).
var (
	oneshotTempTitle       = 0.3
	oneshotTempCommit      = 0.3
	oneshotTempExtract     = 0.3
	oneshotTempRewrite     = 0.5
	oneshotTempGoalSuggest = 0.2
	// oneshotTempPromptSuggest — creative composition (three diverse prompts),
	// not classification: a warmer pin buys suggestion variety.
	oneshotTempPromptSuggest = 0.7
)

// Reasoning tiers of the oneshot service policy, resolved per active model
// through serviceReasoningEffort: title/commit off; the rest minimal.
// See specs/domains/model-profiles.md (oneshot service calls).
const (
	oneshotTierTitle      = llm.ReasoningTierOff
	oneshotTierCommit     = llm.ReasoningTierOff
	oneshotTierOptimize   = llm.ReasoningTierMinimal
	oneshotTierCompaction = llm.ReasoningTierMinimal
	// oneshotTierGoalSuggest — a sliver of deliberation, mirroring the
	// optimizer; the call sits in the send path.
	oneshotTierGoalSuggest = llm.ReasoningTierMinimal
	// oneshotTierPromptSuggest — same rationale, and the call runs async off
	// the send path (ADR-088 D9), so latency is not user-visible anyway.
	oneshotTierPromptSuggest = llm.ReasoningTierMinimal
)

// serviceReasoningEffort resolves a service tier into the model's native wire
// spelling: catalog-first family detection (llm.ResolveBuiltInModel, then
// llm.DetectFamily) → llm.ReasoningForCall; "" sends no reasoning field.
// See specs/domains/model-profiles.md (oneshot service calls).
func serviceReasoningEffort(model string, tier llm.ReasoningTier) string {
	if model == "" || tier == "" {
		return ""
	}
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

// compactionSummarizeContent passes the summary through unchanged — the
// compaction strategy owns summary quality. The nil branch is defensive only
// (oneshot.Do treats (nil, nil) as a parse failure).
func compactionSummarizeContent(resp *llm.ChatResponse) (string, error) {
	if resp == nil {
		return "", nil
	}
	return resp.Message.Content, nil
}
