package core

import (
	"context"
	"log/slog"

	"github.com/v0lka/c0wrk/sdk/skills"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/planner"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	tools "github.com/v0lka/c0wrk/sdk/tools"
)

// roleSuffixes defines role-specific system prompt suffixes for large models.
// These are appended to the base system prompt when no explicit SystemPrompt is set.
var roleSuffixes = map[string]string{
	"researcher": "## Role: Researcher\nYour primary function is information gathering and analysis. Synthesize findings clearly and pass all results through the finish tool. Do NOT create or modify project files.",
	"coder":      "## Role: Coder\nYour primary function is code implementation. Write clean, well-structured code. Verify your changes compile and work before finishing.",
	"tester":     "## Role: Tester\nYour primary function is verification and testing. Run tests, check builds, and report results clearly. Do NOT modify source code — only test infrastructure if necessary.",
}

// rolePruningDefaults defines role-specific tool output pruning parameters.
// Researchers need more retained context and read-tool protection; coders/testers need less.
var rolePruningDefaults = map[string]struct {
	KeepLastN      int
	ProtectedTools []string
}{
	"researcher": {
		KeepLastN:      10,
		ProtectedTools: []string{ToolStoreFact, ToolSearchFacts, ToolReadFile, ToolRipgrep, ToolSemanticSearch},
	},
	"coder": {
		KeepLastN:      5,
		ProtectedTools: []string{ToolStoreFact, ToolSearchFacts},
	},
	"tester": {
		KeepLastN:      5,
		ProtectedTools: []string{ToolStoreFact, ToolSearchFacts, ToolBashExec},
	},
	"executor": {
		KeepLastN:      5,
		ProtectedTools: []string{ToolStoreFact, ToolSearchFacts},
	},
}

// criticalAlwaysAllowedTools are tools that MUST be present whenever AllowedTools
// is non-empty. Without `finish` the executor cannot terminate; store_fact /
// search_facts are required for long-horizon tasks to persist findings across
// step boundaries; ask_user / set_step_status / read_step_output are core
// agent-infrastructure tools that the executor needs to function regardless of
// the planner's step profile. The set is unioned into the filtered list
// regardless of what the planner emitted.
var criticalAlwaysAllowedTools = []string{ToolFinish, ToolStoreFact, ToolSearchFacts, ToolAskUser, ToolSetStepStatus, ToolReadStepOutput, ToolToolResultRead}

// SystemPromptBuilder is the callback signature shared between the SDK (for
// default per-step prompts) and coreStepConfigurator (to synthesize a
// step-local prompt when profile.Skills narrows the active skill pool).
type SystemPromptBuilder func(ctx context.Context, userMessage string, modelMeta llm.ModelMetadata) string

// coreStepConfigurator resolves AgentProfile from PlanStep.Profile.
// Applies tool filtering based on AgentProfile.AllowedTools or role-based ToolProfiles.
//
// When profile.Skills is non-empty and the router-matched pool in taskCtxProvider()
// exposes matching skills, the configurator synthesizes a step-local system prompt
// by narrowing the active skills in ctx before calling sysPromptBuilder. Empty
// profile.Skills leaves StepConfig.SystemPrompt untouched, so the SDK falls back
// to its own cfg.SystemPrompt(ctx, ...) — preserving Normal-mode semantics where
// the full router-matched pool is rendered.
func coreStepConfigurator(
	cfg OrchestratorConfig,
	modelRegistry *llm.ModelRegistry,
	logger *slog.Logger,
	sysPromptBuilder SystemPromptBuilder,
	taskCtxProvider func() context.Context,
	skillMgr *skills.SkillManager,
) orchestration.StepConfigurator {
	return func(step orchestration.PlanStep, defaults orchestration.StepDefaults) orchestration.StepConfig {
		profile := resolveAgentProfile(step, cfg.MaxSteps)

		// Determine allowed tools: explicit profile setting > role-based profile > all tools
		var allowed []tools.ToolDescriptor
		if len(profile.AllowedTools) > 0 {
			// Profile has explicit AllowedTools — use them, unioned with critical
			// tools so the executor can always terminate (finish) and manage facts.
			unioned := unionToolNames(profile.AllowedTools, criticalAlwaysAllowedTools)
			allowed = tools.FilterToolsByProfile(defaults.AllTools, unioned)
		} else if toolProfile, ok := ToolProfiles[profile.Role]; ok {
			// Apply role-based tool profile (e.g., "router", "planner", "reflector")
			allowed = tools.FilterToolsByProfile(defaults.AllTools, toolProfile)
			if logger != nil {
				allowedNames := make([]string, len(allowed))
				for i, t := range allowed {
					allowedNames[i] = t.Name
				}
				// Count MCP tools in the full set that were excluded
				mcpExcluded := 0
				for _, t := range defaults.AllTools {
					if t.SourceCategory == tools.SourceCategoryMCP {
						mcpExcluded++
					}
				}
				mcpIncluded := 0
				for _, t := range allowed {
					if t.SourceCategory == tools.SourceCategoryMCP {
						mcpIncluded++
					}
				}
				mcpExcluded -= mcpIncluded
				logger.Debug("orchestrator: applied role-based tool profile",
					"role", profile.Role,
					"allowed_count", len(allowed),
					"allowed_tools", allowedNames,
					"mcp_included", mcpIncluded,
					"mcp_excluded", mcpExcluded,
				)
			}
		}

		// Only inject role suffix when there's no explicit SystemPrompt override.
		// If someone explicitly set SystemPrompt, they're taking full control.
		var suffix string
		if profile.SystemPrompt == "" {
			suffix = roleSuffixes[profile.Role]
		}

		// Resolve pruning parameters: explicit profile override > role default.
		keepLastN := profile.KeepLastN
		var protectedTools []string
		if profile.ProtectedTools != nil {
			protectedTools = profile.ProtectedTools
		}
		if keepLastN == 0 || protectedTools == nil {
			if roleDefaults, ok := rolePruningDefaults[profile.Role]; ok {
				if keepLastN == 0 {
					keepLastN = roleDefaults.KeepLastN
				}
				if protectedTools == nil {
					protectedTools = roleDefaults.ProtectedTools
				}
			}
		}

		// Resolve the step-local system prompt.
		//
		// Priority:
		//   1. profile.SystemPrompt (explicit override from planner/test)
		//   2. Step-local narrowed skills (if profile.Skills is non-empty)
		//   3. Leave empty → SDK falls back to cfg.SystemPrompt(ctx, ...),
		//      which reads the full task-scope ActiveSkills from request ctx.
		//
		// Option 3b preserves the Normal-mode invariant: single-step plans emit
		// AgentProfile{} (empty Skills) so this branch is a no-op and the full
		// router-matched pool reaches the step via fallback (2).
		//
		// skillMgr is NOT required here: narrowActiveSkills resolves names first
		// against the task-scope pool (router-matched skills live there), and only
		// falls back to the manager for names absent from the pool. Gating on
		// skillMgr != nil would wrongly suppress narrowing whenever the caller
		// does not wire a manager, even though every requested skill is already
		// present in ctx.
		stepSystemPrompt := profile.SystemPrompt
		if stepSystemPrompt == "" && len(profile.Skills) > 0 && sysPromptBuilder != nil && taskCtxProvider != nil {
			taskCtx := taskCtxProvider()
			narrowed := narrowActiveSkills(taskCtx, profile.Skills, skillMgr, logger, step.ID)
			if narrowed != nil && len(narrowed.Skills) > 0 {
				stepCtx := WithActiveSkills(taskCtx, narrowed)
				var modelMeta llm.ModelMetadata
				if modelRegistry != nil {
					modelMeta, _ = modelRegistry.Resolve(stepCtx, cfg.Model)
				}
				stepSystemPrompt = sysPromptBuilder(stepCtx, step.Description, modelMeta)
			}
		}

		return orchestration.StepConfig{
			MaxSteps:           profile.MaxSteps,
			AllowedTools:       allowed,
			SystemPrompt:       stepSystemPrompt,
			SystemPromptSuffix: suffix,
			CompactionStrategy: router.ApplyCompactionStrategy(profile.Domain, complexityForStep(taskCtxProvider)),
			KeepLastN:          keepLastN,
			ProtectedTools:     protectedTools,
			AgentRole:          profile.Role,
		}
	}
}

// complexityForStep extracts the routing complexity from the task context
// (set by HandleMessage via WithComplexity). Defaults to 3 — the previous
// hardcoded value — when the context is unavailable so step configurators
// running outside a normal request flow keep their existing behavior.
func complexityForStep(taskCtxProvider func() context.Context) int {
	if taskCtxProvider == nil {
		return 3
	}
	ctx := taskCtxProvider()
	if ctx == nil {
		return 3
	}
	if c := ComplexityFromContext(ctx); c > 0 {
		return c
	}
	return 3
}

// narrowActiveSkills returns an *ActiveSkills containing only the skills listed
// in allowedNames, preserving order. Unknown names are logged at debug level and
// dropped. Skills are resolved first from the task-scope ActiveSkills in taskCtx
// (so a router-matched *Skill is reused) and fall back to the SkillManager by
// name when not present in ctx. Returns nil if the intersection is empty so the
// caller can fall through to the task-scope pool.
func narrowActiveSkills(taskCtx context.Context, allowedNames []string, skillMgr *skills.SkillManager, logger *slog.Logger, stepID string) *ActiveSkills {
	if len(allowedNames) == 0 {
		return nil
	}
	taskPool := ActiveSkillsFromContext(taskCtx)
	byName := map[string]*skills.Skill{}
	if taskPool != nil {
		for _, s := range taskPool.Skills {
			byName[s.Metadata.Name] = s
		}
	}

	kept := make([]*skills.Skill, 0, len(allowedNames))
	for _, name := range allowedNames {
		if s, ok := byName[name]; ok {
			kept = append(kept, s)
			continue
		}
		if skillMgr != nil {
			if s, ok := skillMgr.Get(name); ok {
				kept = append(kept, s)
				continue
			}
		}
		if logger != nil {
			logger.Debug("orchestrator: step profile references unknown skill", "step", stepID, "skill", name)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return &ActiveSkills{Skills: kept}
}

// unionToolNames returns the union of two name slices, preserving the order of
// the primary slice and appending any additional names from extra that are not
// already present. Used by Task 3 (always-include critical tools).
func unionToolNames(primary, extra []string) []string {
	seen := make(map[string]bool, len(primary)+len(extra))
	out := make([]string, 0, len(primary)+len(extra))
	for _, n := range primary {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range extra {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// resolveAgentProfile returns the effective AgentProfile for a plan step.
func resolveAgentProfile(step orchestration.PlanStep, defaultMaxSteps int) planner.AgentProfile {
	if step.Profile != nil {
		if profile, ok := step.Profile.(*planner.AgentProfile); ok {
			p := *profile
			if p.MaxSteps == 0 {
				p.MaxSteps = defaultMaxSteps
			}
			return p
		}
	}
	return planner.AgentProfile{Role: "executor", MaxSteps: defaultMaxSteps}
}
