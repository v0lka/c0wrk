package core

import (
	"context"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/prompt"
	tools "github.com/user/agent/sdk/tools"
)

// buildSystemPrompt creates the system prompt for executors.
func buildSystemPrompt(ctx context.Context, userMessage string, modelMeta llm.ModelMetadata) string {
	// Build workspace context string
	var workspaceCtxStr string
	if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
		workspaceCtxStr = "## Workspace\nYour session workspace is: " + wsPath + "\nAll artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location."
		if tempDir := tools.TempDirFrom(ctx); tempDir != "" {
			workspaceCtxStr += "\nYour session temp directory is: " + tempDir + "\nUse this directory for ANY intermediate files — drafts, partial results, scratch data, inter-step artifacts. These files are NOT part of the final deliverable and will be cleaned up when the session ends."
		}
	}

	// Resolve model family for prompt adaptation
	family := modelMeta.Family
	if family == "" {
		family = "default"
	}

	// Build base prompt: system core + family-specific overlay
	result := prompt.NewBuilder().
		Core(prompts.OrchestratorSystem).
		Core(prompts.FamilyPrompt("orchestrator", family)).
		Replace("WORKSPACE-CONTEXT", workspaceCtxStr).
		Build()

	// Append mode-specific context.
	if ctx.Value(PlanModeKey) != nil {
		result += "\n\n" + prompts.OrchestratorPlanContext
	} else {
		// ReAct mode: reinforce finish tool requirement since there's no plan context
		// to naturally motivate its use.
		result += "\n\n## Completion\nYou are operating in single-step mode. When you have completed your work, you MUST call the `finish` tool with your final answer. Do not simply respond with text — the system only recognizes task completion through an explicit `finish` tool call."
	}

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// terminalSteps returns the IDs of steps that have no dependents in the plan.
// These are the leaf nodes of the DAG - steps that no other step depends on.
func terminalSteps(plan *Plan) []string {
	if plan == nil || len(plan.Steps) == 0 {
		return nil
	}

	// Build set of all steps that are dependencies of other steps
	dependedOn := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, depID := range step.DependsOn {
			dependedOn[depID] = true
		}
	}

	// Terminal steps are those not depended on by any other step
	var terminals []string
	for _, step := range plan.Steps {
		if !dependedOn[step.ID] {
			terminals = append(terminals, step.ID)
		}
	}
	return terminals
}

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *agent.Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}
