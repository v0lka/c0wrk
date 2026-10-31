// Package prompts provides embedded prompt templates used by LLM agents.
package prompts

import _ "embed"

// Orchestrator prompts

//go:embed orchestrator_system.md
var OrchestratorSystem string

// OrchestratorSystemLite is the compact core directive swapped in when the
// Model Profiles system_prompt Lite variant is active: the behavioral core
// only, with no injection-defense content (injected separately, unchanged).
// See specs/domains/model-profiles.md (Lite variant).
//
//go:embed orchestrator_lite.md
var OrchestratorSystemLite string

// OrchestratorLiteScaffold is the three-step reasoning scaffold (goal, tool
// choice + rationale, exact args) appended after OrchestratorSystemLite under
// the ReasoningScaffold sub-toggle, so a small model reasons in a fixed shape
// instead of free-form drift.
// See specs/domains/model-profiles.md (ReasoningScaffold).
//
//go:embed orchestrator_lite_scaffold.md
var OrchestratorLiteScaffold string

// OrchestratorLiteFewShot is the curated worked-example ReAct block appended
// after OrchestratorSystemLite under the FewShot sub-toggle, demonstrating the
// behaviors a small model most often violates (tool-call format, tool choice,
// error recovery, finish).
// See specs/domains/model-profiles.md (FewShot).
//
//go:embed orchestrator_lite_fewshot.md
var OrchestratorLiteFewShot string

//go:embed orchestrator_plan_context.md
var OrchestratorPlanContext string

// Goal mode — injected into the system prompt when a goal is active. Carries
// the condition, verify clause, evidence mandate, and budget template; the
// {goal_condition}, {goal_verify_clause}, and {goal_budget_line} placeholders
// are substituted by renderGoalModeSection from the active GoalState.

//go:embed goal_mode.md
var GoalMode string

// Goal derivation — the system prompt for the derivation agent that
// investigates a user's request and derives a {condition, verify} goal pair
// for sign-off via propose_goal. Used by (*Orchestrator).deriveGoal.

//go:embed goal_derivation.md
var GoalDerivation string

// GoalDerivationLite is the compact Lite counterpart of GoalDerivation,
// selected by buildSpecializedSystemPromptWithLite for goal derivation runs.
// See specs/domains/model-profiles.md (goal-agent Lite parity).
//
//go:embed goal_derivation_lite.md
var GoalDerivationLite string

// Goal verification — the directive for the isolated read-only verifier that
// independently re-runs a goal's verify clause (executable mode) after the
// agent declares "met". Placeholders are resolved by
// GoalVerificationSubstitute.
// See specs/domains/goal-mode.md (independent verification).

//go:embed goal_verification.md
var GoalVerification string

// GoalVerificationLite is the compact Lite counterpart of GoalVerification;
// the same placeholder set, selected by GoalVerificationLiteDirectiveByMode.
// See specs/domains/model-profiles.md (goal-agent Lite parity).
//
//go:embed goal_verification_lite.md
var GoalVerificationLite string

// Goal re-derivation verification — the directive for the verifier that
// settles a "met" claim by DELEGATING a fresh, read-only execution of the
// goal's process and confirming only on a clean outcome (re_derivation mode).
// Placeholders are resolved by GoalVerificationSubstitute; mode selection is
// centralized in GoalVerificationDirectiveByMode.
// See specs/domains/goal-mode.md (re_derivation mode).
//
//go:embed goal_rederivation.md
var GoalReDerivation string

// GoalReDerivationLite is the compact Lite counterpart of GoalReDerivation,
// selected by GoalVerificationLiteDirectiveByMode.
// See specs/domains/model-profiles.md (goal-agent Lite parity).
//
//go:embed goal_rederivation_lite.md
var GoalReDerivationLite string

// Orchestrator family-specific prompts

//go:embed orchestrator_default.md
var OrchestratorDefault string

//go:embed orchestrator_anthropic.md
var OrchestratorAnthropic string

//go:embed orchestrator_openai_flagship.md
var OrchestratorOpenAIFlagship string

//go:embed orchestrator_openai_standard.md
var OrchestratorOpenAIStandard string

//go:embed orchestrator_google.md
var OrchestratorGoogle string

//go:embed orchestrator_deepseek.md
var OrchestratorDeepSeek string

//go:embed orchestrator_mistral.md
var OrchestratorMistral string

//go:embed orchestrator_kimi.md
var OrchestratorKimi string

//go:embed orchestrator_qwen.md
var OrchestratorQwen string

//go:embed orchestrator_glm.md
var OrchestratorGLM string

//go:embed orchestrator_openai_codex.md
var OrchestratorOpenAICodex string

// Reflector prompt (auxiliary agent — fixed prompt, no family variants)

//go:embed reflector_system.md
var ReflectorSystem string

// Router prompt (auxiliary agent — fixed prompt, no family variants)

//go:embed router_system.md
var RouterSystem string

// Goal-mode suitability prompt (post-routing classifier — decides whether to
// offer goal mode to the user; see core/goal_suggest.go)

//go:embed goal_suggest.md
var GoalSuggest string

// Prompt-suggestion prompts (chat-input follow-up prompts; see
// core/prompt_suggest.go and specs/domains/prompt-suggestions.md). Both answer
// in the supplied history's dominant language, at most three plain lines — one
// suggestion per line. PromptSuggestContinuation feeds goal_advance (keep
// driving the active goal); PromptSuggestNewSession feeds next_task (the
// natural next task after a finished one).

//go:embed prompt_suggest_continuation.md
var PromptSuggestContinuation string

//go:embed prompt_suggest_new_session.md
var PromptSuggestNewSession string

// Verification mandate — injected into tool-enabled system prompts

//go:embed verification_mandate.md
var VerificationMandate string

// Injection defense — tells LLM to distrust untrusted-tagged tool output

//go:embed injection_defense.md
var InjectionDefense string

// E2S system directive — the compact core prompt for the E2S execution loop:
// the e2s_step protocol, state discipline, acting/finishing rules; core/e2s's
// prompt builder appends the surrounding sections around it.
// See specs/domains/e2s.md.

//go:embed e2s.md
var E2SSystem string

// E2SSystemLite is the Model Profiles Lite-swap counterpart of E2SSystem,
// selected for the E2S core directive under the same profile gates; the
// surrounding sections are appended unchanged.
// See specs/domains/e2s.md (Model Profiles).

//go:embed e2s-lite.md
var E2SSystemLite string

// Code review mode — injected when the user submitted review feedback
// (ReviewModeKey): treat the message as actionable review comments and edit
// code to address them (specs/domains/review.md).

//go:embed code_review_mode.md
var CodeReviewMode string

// Compaction summarize prompt

//go:embed compaction_summarize.md
var CompactionSummarize string

// Prompt optimizer prompts

//go:embed prompt_optimize_extract.md
var PromptOptimizeExtract string

//go:embed prompt_optimize_rewrite.md
var PromptOptimizeRewrite string

// Commit message generator prompt (conventional commits)

//go:embed commit_message.md
var CommitMessage string

// FamilyPrompt returns the family-specific prompt for the given agent and family.
// Falls back to the "default" family if no specific prompt exists.
// Returns empty string if the agent has no family-specific prompts (auxiliary agents).
func FamilyPrompt(agent, family string) string {
	switch agent {
	case "orchestrator":
		return orchestratorFamilyPrompt(family)
	default:
		return ""
	}
}

func orchestratorFamilyPrompt(family string) string {
	switch family {
	case "anthropic":
		return OrchestratorAnthropic
	case "openai_flagship":
		return OrchestratorOpenAIFlagship
	case "openai_standard":
		return OrchestratorOpenAIStandard
	case "google":
		return OrchestratorGoogle
	case "deepseek":
		return OrchestratorDeepSeek
	case "mistral":
		return OrchestratorMistral
	case "kimi":
		return OrchestratorKimi
	case "qwen":
		return OrchestratorQwen
	case "glm":
		return OrchestratorGLM
	case "openai_codex":
		return OrchestratorOpenAICodex
	default:
		return OrchestratorDefault
	}
}
