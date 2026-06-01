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

// Orchestrator prompts

//go:embed orchestrator_system.md
var OrchestratorSystem string

//go:embed orchestrator_plan_context.md
var OrchestratorPlanContext string

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

// Compaction summarize prompt

//go:embed compaction_summarize.md
var CompactionSummarize string

// Prompt optimizer prompts

//go:embed prompt_optimize_extract.md
var PromptOptimizeExtract string

//go:embed prompt_optimize_rewrite.md
var PromptOptimizeRewrite string

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
