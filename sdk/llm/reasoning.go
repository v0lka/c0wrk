package llm

// ReasoningEffort represents user-facing reasoning intensity levels.
type ReasoningEffort string

const (
	ReasoningMinimal ReasoningEffort = "minimal"
	ReasoningLow     ReasoningEffort = "low"
	ReasoningMedium  ReasoningEffort = "medium"
	ReasoningHigh    ReasoningEffort = "high"
	ReasoningMaximum ReasoningEffort = "maximum"
)

// ReasoningConfig holds provider-specific reasoning parameters resolved from a ReasoningEffort level.
type ReasoningConfig struct {
	// BudgetTokens is the Anthropic thinking budget in tokens.
	BudgetTokens int
	// OpenAIEffort is the OpenAI reasoning_effort parameter ("low", "medium", "high").
	OpenAIEffort string
	// GeminiThinkingLevel is the Gemini thinking level.
	GeminiThinkingLevel string
	// GeminiThinkingBudget is the Gemini thinking budget in tokens.
	GeminiThinkingBudget int
	// Enabled indicates whether reasoning/thinking is enabled at all.
	Enabled bool
}

// ResolveReasoning maps a user-facing ReasoningEffort level to provider-specific parameters.
// The family parameter determines which provider mapping to use.
func ResolveReasoning(effort ReasoningEffort, family string) ReasoningConfig {
	switch family {
	case "anthropic":
		return resolveAnthropicReasoning(effort)
	case "openai_flagship", "openai_standard":
		return resolveOpenAIReasoning(effort)
	case "gemini":
		return resolveGeminiReasoning(effort)
	default:
		// For families without reasoning support, return disabled config
		return ReasoningConfig{Enabled: false}
	}
}

func resolveAnthropicReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningMinimal:
		return ReasoningConfig{Enabled: true, BudgetTokens: 1024}
	case ReasoningLow:
		return ReasoningConfig{Enabled: true, BudgetTokens: 2000}
	case ReasoningMedium:
		return ReasoningConfig{Enabled: true, BudgetTokens: 5000}
	case ReasoningHigh:
		return ReasoningConfig{Enabled: true, BudgetTokens: 10000}
	case ReasoningMaximum:
		return ReasoningConfig{Enabled: true, BudgetTokens: 32000}
	default:
		return ReasoningConfig{Enabled: true, BudgetTokens: 10000} // default to high
	}
}

func resolveOpenAIReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningMinimal, ReasoningLow:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "low"}
	case ReasoningMedium:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "medium"}
	case ReasoningHigh, ReasoningMaximum:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "high"}
	default:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "high"}
	}
}

func resolveGeminiReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningMinimal:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "minimal", GeminiThinkingBudget: 0}
	case ReasoningLow:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "low", GeminiThinkingBudget: 2048}
	case ReasoningMedium:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "medium", GeminiThinkingBudget: 4096}
	case ReasoningHigh:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "high", GeminiThinkingBudget: 8192}
	case ReasoningMaximum:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "high", GeminiThinkingBudget: 16384}
	default:
		return ReasoningConfig{Enabled: true, GeminiThinkingLevel: "high", GeminiThinkingBudget: 8192}
	}
}

// AgentReasoningMode returns the appropriate reasoning effort for an agent type.
// Main agents (orchestrator, planner) use full reasoning; auxiliary agents use minimal.
func AgentReasoningMode(agentRole string, baseEffort ReasoningEffort) ReasoningEffort {
	switch agentRole {
	case "orchestrator", "planner", "coder", "executor":
		return baseEffort // full mode
	case "router", "reflector", "compaction", "title", "summary":
		// Auxiliary agents use reduced reasoning (small mode)
		if baseEffort == ReasoningHigh || baseEffort == ReasoningMaximum {
			return ReasoningLow
		}
		return ReasoningMinimal
	default:
		return baseEffort
	}
}
