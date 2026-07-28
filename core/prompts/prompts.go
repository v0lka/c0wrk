// Package prompts provides embedded prompt templates used by LLM agents.
package prompts

import _ "embed"

// Planner prompts

//go:embed planner_base.md
var PlannerBase string

//go:embed planner_replan.md
var PlannerReplan string

//go:embed planner_informed.md
var PlannerInformed string

// Planner family-specific prompts

//go:embed planner_default.md
var PlannerDefault string

//go:embed planner_anthropic.md
var PlannerAnthropic string

//go:embed planner_openai_flagship.md
var PlannerOpenAIFlagship string

//go:embed planner_openai_standard.md
var PlannerOpenAIStandard string

//go:embed planner_google.md
var PlannerGoogle string

//go:embed planner_deepseek.md
var PlannerDeepSeek string

//go:embed planner_mistral.md
var PlannerMistral string

//go:embed planner_kimi.md
var PlannerKimi string

//go:embed planner_qwen.md
var PlannerQwen string

//go:embed planner_glm.md
var PlannerGLM string

//go:embed planner_openai_codex.md
var PlannerOpenAICodex string

// Planner mode-specific composable prompt sections used by core.Planner to
// assemble plan/replan/continuation system prompts. Each piece is rendered as
// a separate markdown file so the planning policy lives next to the other
// prompt assets rather than as inline Go string literals.

//go:embed planner_plan_preamble.md
var PlannerPlanPreamble string

//go:embed planner_single_step_preamble.md
var PlannerSingleStepPreamble string

//go:embed planner_multi_step_tot.md
var PlannerMultiStepToT string

//go:embed planner_single_step_tot.md
var PlannerSingleStepToT string

//go:embed planner_multi_step_guidance.md
var PlannerMultiStepGuidance string

//go:embed planner_single_step_guidance.md
var PlannerSingleStepGuidance string

//go:embed planner_domain_assignment.md
var PlannerDomainAssignment string

//go:embed planner_agent_profiles.md
var PlannerAgentProfiles string

//go:embed planner_extra_sections.md
var PlannerExtraSections string

//go:embed planner_continuation_preamble.md
var PlannerContinuationPreamble string

//go:embed planner_incomplete_continuation_preamble.md
var PlannerContinuationIncompletePreamble string

//go:embed planner_continuation_single_step.md
var PlannerContinuationSingleStep string

// Orchestrator prompts

//go:embed orchestrator_system.md
var OrchestratorSystem string

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

// Goal verification — the directive for the isolated read-only/test agent that
// independently confirms or rejects a "met" claim for a declared goal. Used by
// the verification step that runs after an agent emits a "met" verdict via
// declare_goal_status. The {goal_condition}, {goal_verify_clause}, and
// {reported_evidence} placeholders are substituted by GoalVerificationSubstitute
// from the active GoalState and the agent's reported evidence; the
// {shell_tool} placeholder is resolved via SubstituteShellTool (delegated by
// GoalVerificationSubstitute) so the directive names the platform-correct
// shell-execution tool.

//go:embed goal_verification.md
var GoalVerification string

// Goal re-derivation verification — the directive for the isolated agent that
// verifies a "met" claim in re_derivation mode. Instead of running a single
// verify clause, this verifier DELEGATES a fresh, read-only execution of the
// goal's process and confirms only if that fresh run comes back clean. The
// {goal_condition}, {goal_verify_clause}, and {reported_evidence} placeholders
// are substituted by GoalVerificationSubstitute (the same placeholder set as
// the executable directive — re-derivation reuses it), and {shell_tool} is
// resolved via SubstituteShellTool. Used by defaultGoalVerifier when the goal's
// VerificationMode is re_derivation; selection of this directive vs the
// executable one by mode is centralized in GoalVerificationDirectiveByMode.
//
//go:embed goal_rederivation.md
var GoalReDerivation string

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

// Verification mandate — injected into tool-enabled system prompts

//go:embed verification_mandate.md
var VerificationMandate string

// Injection defense — tells LLM to distrust untrusted-tagged tool output

//go:embed injection_defense.md
var InjectionDefense string

// Code review mode — injected into the system prompt when the user submitted
// review feedback (ReviewModeKey). Directs the agent to treat the user's
// message as actionable review comments and edit code to address them, so the
// review loop (specs/domains/review.md) makes progress toward approval.

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
	case "planner":
		return plannerFamilyPrompt(family)
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

func plannerFamilyPrompt(family string) string {
	switch family {
	case "anthropic":
		return PlannerAnthropic
	case "openai_flagship":
		return PlannerOpenAIFlagship
	case "openai_standard":
		return PlannerOpenAIStandard
	case "google":
		return PlannerGoogle
	case "deepseek":
		return PlannerDeepSeek
	case "mistral":
		return PlannerMistral
	case "kimi":
		return PlannerKimi
	case "qwen":
		return PlannerQwen
	case "glm":
		return PlannerGLM
	case "openai_codex":
		return PlannerOpenAICodex
	default:
		return PlannerDefault
	}
}
