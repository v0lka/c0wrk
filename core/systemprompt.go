package core

import (
	"context"
	"strings"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/core/skills"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/prompt"
	tools "github.com/user/agent/sdk/tools"
)

// vectorSearchHintsKeyType is the context key for auto-RAG hints.
type vectorSearchHintsKeyType struct{}

var vectorSearchHintsKey = vectorSearchHintsKeyType{}

// VectorSearchHints represents auto-RAG results to inject into prompts.
type VectorSearchHints struct {
	Files []VectorSearchHint
}

// VectorSearchHint is a single file hint from the vector index.
type VectorSearchHint struct {
	FilePath string
	Summary  string // first line or chunk preview
}

// WithVectorSearchHints returns a context with vector search hints attached.
func WithVectorSearchHints(ctx context.Context, hints *VectorSearchHints) context.Context {
	return context.WithValue(ctx, vectorSearchHintsKey, hints)
}

// VectorSearchHintsFromContext extracts vector search hints from the context.
// Returns nil if not present.
func VectorSearchHintsFromContext(ctx context.Context) *VectorSearchHints {
	if hints, ok := ctx.Value(vectorSearchHintsKey).(*VectorSearchHints); ok {
		return hints
	}
	return nil
}

// agentsMDKeyType is the context key for AGENTS.md project instructions.
type agentsMDKeyType struct{}

var agentsMDKey = agentsMDKeyType{}

// AgentsMD holds the full content of the project's AGENTS.md file.
type AgentsMD struct {
	Content string
}

// WithAgentsMD returns a context with AGENTS.md content attached.
func WithAgentsMD(ctx context.Context, amd *AgentsMD) context.Context {
	return context.WithValue(ctx, agentsMDKey, amd)
}

// AgentsMDFromContext extracts AGENTS.md content from the context.
// Returns nil if not present.
func AgentsMDFromContext(ctx context.Context) *AgentsMD {
	if amd, ok := ctx.Value(agentsMDKey).(*AgentsMD); ok {
		return amd
	}
	return nil
}

// activeSkillsKeyType is the context key for activated Agent Skills.
type activeSkillsKeyType struct{}

var activeSkillsKey = activeSkillsKeyType{}

// ActiveSkills holds the full skill data for skills matched to the current task.
type ActiveSkills struct {
	Skills []*skills.Skill
}

// WithActiveSkills returns a context with active skills attached.
func WithActiveSkills(ctx context.Context, as *ActiveSkills) context.Context {
	return context.WithValue(ctx, activeSkillsKey, as)
}

// ActiveSkillsFromContext extracts active skills from the context.
// Returns nil if not present.
func ActiveSkillsFromContext(ctx context.Context) *ActiveSkills {
	if as, ok := ctx.Value(activeSkillsKey).(*ActiveSkills); ok {
		return as
	}
	return nil
}

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

	// Build base prompt: system core + family-specific overlay + verification mandate (stable).
	// CacheBreak separates stable content from dynamic per-session context below.
	result := prompt.NewBuilder().
		Core(prompts.OrchestratorSystem).
		Core(prompts.FamilyPrompt("orchestrator", family)).
		Core(prompts.VerificationMandate).
		CacheBreak().
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

	// Append auto-RAG vector search hints when available.
	if hints, ok := ctx.Value(vectorSearchHintsKey).(*VectorSearchHints); ok && hints != nil && len(hints.Files) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n## Relevant Project Files (auto-detected)\n")
		sb.WriteString("Based on your query, these files may be relevant:\n")
		for _, h := range hints.Files {
			sb.WriteString("- " + h.FilePath)
			if h.Summary != "" {
				sb.WriteString(": " + h.Summary)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\nUse semantic_search tool for deeper investigation.")
		result += sb.String()
	}

	// Append active Agent Skills when available.
	if activeSkills, ok := ctx.Value(activeSkillsKey).(*ActiveSkills); ok && activeSkills != nil && len(activeSkills.Skills) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n## Active Skills\n")
		sb.WriteString("The following skills have been activated for this task. Follow their instructions carefully.\n\n")
		for _, s := range activeSkills.Skills {
			sb.WriteString("### Skill: " + s.Metadata.Name + "\n")
			if s.Metadata.Description != "" {
				sb.WriteString("Description: " + s.Metadata.Description + "\n")
			}
			if len(s.Metadata.AllowedToolList()) > 0 {
				sb.WriteString("Allowed tools: " + s.Metadata.AllowedTools + "\n")
			}
			sb.WriteString("\n" + s.Body + "\n\n")
		}
		result += sb.String()
	}

	return result
}

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *agent.Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}
