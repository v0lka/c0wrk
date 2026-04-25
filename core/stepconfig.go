package core

import (
	"log/slog"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
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
		ProtectedTools: []string{"store_fact", "search_facts", "read_file", "search_content", "ripgrep", "semantic_search"},
	},
	"coder": {
		KeepLastN:      5,
		ProtectedTools: []string{"store_fact", "search_facts"},
	},
	"tester": {
		KeepLastN:      5,
		ProtectedTools: []string{"store_fact", "search_facts", "bash_exec"},
	},
	"executor": {
		KeepLastN:      5,
		ProtectedTools: []string{"store_fact", "search_facts"},
	},
}

// coreStepConfigurator resolves AgentProfile from PlanStep.Profile.
// Applies tool filtering based on AgentProfile.AllowedTools or role-based ToolProfiles.
func coreStepConfigurator(cfg OrchestratorConfig, modelRegistry *llm.ModelRegistry, logger *slog.Logger) orchestration.StepConfigurator {
	return func(step orchestration.PlanStep, defaults orchestration.StepDefaults) orchestration.StepConfig {
		profile := resolveAgentProfile(step, cfg.MaxSteps)

		// Determine allowed tools: explicit profile setting > role-based profile > all tools
		var allowed []tools.ToolDescriptor
		if len(profile.AllowedTools) > 0 {
			// Profile has explicit AllowedTools - use them
			allowed = tools.FilterToolsByProfile(defaults.AllTools, profile.AllowedTools)
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
					if t.Source != "" && t.Source != "core" {
						mcpExcluded++
					}
				}
				mcpIncluded := 0
				for _, t := range allowed {
					if t.Source != "" && t.Source != "core" {
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

		return orchestration.StepConfig{
			MaxSteps:           profile.MaxSteps,
			AllowedTools:       allowed,
			SystemPrompt:       profile.SystemPrompt,
			SystemPromptSuffix: suffix,
			CompactionStrategy: applyCompactionStrategy(profile.Domain, 3),
			KeepLastN:          keepLastN,
			ProtectedTools:     protectedTools,
		}
	}
}

// resolveAgentProfile returns the effective AgentProfile for a plan step.
func resolveAgentProfile(step orchestration.PlanStep, defaultMaxSteps int) AgentProfile {
	if step.Profile != nil {
		if profile, ok := step.Profile.(*AgentProfile); ok {
			p := *profile
			if p.MaxSteps == 0 {
				p.MaxSteps = defaultMaxSteps
			}
			return p
		}
	}
	return AgentProfile{Role: "executor", MaxSteps: defaultMaxSteps}
}
