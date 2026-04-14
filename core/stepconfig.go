package core

import (
	"log/slog"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/prompt"
	tools "github.com/user/agent/sdk/tools"
)

// roleSuffixes defines role-specific system prompt suffixes for large models.
// These are appended to the base system prompt when no explicit SystemPrompt is set.
var roleSuffixes = map[string]string{
	"researcher": "## Role: Researcher\nYour primary function is information gathering and analysis. Synthesize findings clearly and pass all results through the finish tool. Do NOT create or modify project files.",
	"coder":      "## Role: Coder\nYour primary function is code implementation. Write clean, well-structured code. Verify your changes compile and work before finishing.",
	"tester":     "## Role: Tester\nYour primary function is verification and testing. Run tests, check builds, and report results clearly. Do NOT modify source code — only test infrastructure if necessary.",
}

// smallRoleSuffixes defines more explicit, directive role suffixes for small models.
// Small models benefit from clearer, more structured instructions.
var smallRoleSuffixes = map[string]string{
	"researcher": "## Role: Researcher\nYou gather information. Follow these rules:\n1. Use search and read tools to find information.\n2. Summarize findings clearly.\n3. Pass ALL results through the finish tool.\n4. Do NOT create or modify project files.\n5. Do NOT write code.",
	"coder":      "## Role: Coder\nYou write code. Follow these rules:\n1. Read existing code before making changes.\n2. Write clean, working code.\n3. Verify your changes compile before finishing.\n4. Use the finish tool when done.",
	"tester":     "## Role: Tester\nYou run tests and verify code. Follow these rules:\n1. Run the specified tests or checks.\n2. Report results clearly: PASS or FAIL.\n3. Do NOT modify source code.\n4. Use the finish tool with your findings.",
}

// coreStepConfigurator resolves AgentProfile from PlanStep.Profile.
// If modelRegistry is provided, role suffixes are selected based on model tier.
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
			// Resolve tier and pick appropriate suffix map
			tier := resolveTierFromRegistry(modelRegistry)
			if logger != nil {
				logger.Debug("orchestrator: model tier resolved", "tier", tier)
			}
			suffixMap := roleSuffixes
			if tier == prompt.TierSmall {
				suffixMap = smallRoleSuffixes
			}
			suffix = suffixMap[profile.Role]
		}

		return orchestration.StepConfig{
			MaxSteps:           profile.MaxSteps,
			AllowedTools:       allowed,
			SystemPrompt:       profile.SystemPrompt,
			SystemPromptSuffix: suffix,
			CompactionStrategy: applyCompactionStrategy(profile.Domain, 3),
		}
	}
}

// resolveTierFromRegistry resolves the model tier from the registry, defaulting to large.
func resolveTierFromRegistry(registry *llm.ModelRegistry) prompt.ModelTier {
	if registry == nil {
		return prompt.TierLarge
	}
	meta, _ := registry.Resolve("")
	tier := prompt.ModelTier(meta.Tier)
	if tier == "" {
		return prompt.TierLarge
	}
	return tier
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

// domainToAgentProfile maps a routing domain to an AgentProfile with appropriate role.
func domainToAgentProfile(domain string, maxSteps int) AgentProfile {
	var role string
	switch domain {
	case "code":
		role = "coder"
	case "research":
		role = "researcher"
	default:
		role = "executor"
	}
	return AgentProfile{
		Role:     role,
		MaxSteps: maxSteps,
		Domain:   domain,
	}
}
