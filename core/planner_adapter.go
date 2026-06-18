package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/planner"
	"github.com/v0lka/c0wrk/sdk/skills"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// c0wrkPromptSet returns the planner PromptSet wired with c0wrk prompt templates.
func c0wrkPromptSet() planner.PromptSet {
	return planner.PromptSet{
		BasePrompt:           prompts.PlannerBase,
		InformedPrompt:       prompts.PlannerInformed,
		ReplanPrompt:         prompts.PlannerReplan,
		PlanPreamble:         prompts.PlannerPlanPreamble,
		SingleStepPreamble:   prompts.PlannerSingleStepPreamble,
		MultiStepToT:         prompts.PlannerMultiStepToT,
		SingleStepToT:        prompts.PlannerSingleStepToT,
		MultiStepGuidance:    prompts.PlannerMultiStepGuidance,
		SingleStepGuidance:   prompts.PlannerSingleStepGuidance,
		ContinuationPreamble: prompts.PlannerContinuationPreamble,
		ContinuationSingleStep: prompts.PlannerContinuationSingleStep,
		DomainAssignment:     prompts.PlannerDomainAssignment,
		AgentProfiles:        prompts.PlannerAgentProfiles,
		ExtraSections:        prompts.PlannerExtraSections,
		FamilyPrompt:         prompts.FamilyPrompt,
		VerificationMandate:  prompts.VerificationMandate,
	}
}

// plannerPromptExcludedTools is the set of infrastructure tool names that
// must NOT appear in the planner's prompt tool list. These are
// executor-internal tools (finish, set_step_status, ask_user, etc.) that
// the planner should never plan for executors. Listing them under
// "Available Executor Tools" causes LLMs trained on tool calling to
// output tool-call-like text instead of the expected plan JSON.
var plannerPromptExcludedTools = map[string]bool{
	ToolFinish:         true,
	ToolAskUser:        true,
	ToolSetStepStatus:  true,
	ToolReadStepOutput: true,
	ToolListStepOutput: true,
	ToolToolResultRead: true,
	ToolReadSkillRes:   true,
}

// filterPlannerPromptTools removes executor-internal tools from the tool list
// shown in the planner's prompt, keeping only tools relevant for step planning.
func filterPlannerPromptTools(all []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	filtered := make([]sdktools.ToolDescriptor, 0, len(all))
	for _, t := range all {
		if !plannerPromptExcludedTools[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// plannerToolNames is the set of tool names available for planner exploration.
var plannerToolNames = map[string]bool{
	ToolListDirectory:  true,
	ToolGlob:           true,
	ToolRipgrep:        true,
	ToolReadFile:       true,
	ToolSemanticSearch: true,
}

// newCorePlanner creates an SDK planner wired with c0wrk-specific prompts,
// context functions, and tool configuration.
func newCorePlanner(caller agent.LLMCaller, reg *tools.ToolRegistry) *planner.Planner {
	cfg := planner.Config{
		Prompts:               c0wrkPromptSet(),
		DomainFromContext:     DomainFromContext,
		ComplexityFromContext: ComplexityFromContext,
		UserSkillsFromContext: UserSkillsFromContext,
		FormatSkillList:       formatSkillListForPlanner,
		FormatWorkspacePath:   formatWorkspacePath,
		AppendContextSections: appendPlannerContextSections,
		ToolRegistry:          reg,
		PlannerToolNames:      plannerToolNames,
	}
	return planner.NewPlanner(caller, cfg)
}

// formatSkillListForPlanner formats the skill list for planner prompts,
// distinguishing user-activated (mandatory) skills from router-matched ones.
func formatSkillListForPlanner(ctx context.Context, availableSkills []skills.SkillDescriptor) string {
	if len(availableSkills) == 0 {
		return "None"
	}

	userSkills := UserSkillsFromContext(ctx)
	if len(userSkills) == 0 {
		return formatSkillList(availableSkills)
	}

	userSet := make(map[string]bool, len(userSkills))
	for _, name := range userSkills {
		userSet[name] = true
	}

	var sb strings.Builder
	sb.WriteString("The user explicitly activated the following skills. The plan MUST be built around executing these skills — they define the primary task, not optional context:\n")
	for _, s := range availableSkills {
		if userSet[s.Name] {
			sb.WriteString("- " + s.Name + " [MANDATORY]: " + s.Description + "\n")
		}
	}
	var hasOptional bool
	for _, s := range availableSkills {
		if !userSet[s.Name] {
			if !hasOptional {
				sb.WriteString("\nAdditional available skills (optional):\n")
				hasOptional = true
			}
			sb.WriteString("- " + s.Name + ": " + s.Description + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatSkillList formats a flat list of skills for embedding in prompts.
func formatSkillList(availableSkills []skills.SkillDescriptor) string {
	if len(availableSkills) == 0 {
		return "None"
	}
	var sb strings.Builder
	for i, s := range availableSkills {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("- " + s.Name + ": " + s.Description)
	}
	return sb.String()
}

// formatWorkspacePath returns the workspace instruction block if set.
func formatWorkspacePath(ctx context.Context) string {
	wp := sdktools.WorkspacePathFrom(ctx)
	if wp == "" {
		return ""
	}
	return fmt.Sprintf("Session workspace: %s\nWhen steps produce file artifacts, they must be created inside this workspace unless the task explicitly specifies an external location.", wp)
}
