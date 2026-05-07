package llm

// ReasoningEffort represents user-facing reasoning intensity levels.
type ReasoningEffort string

const (
	ReasoningOff     ReasoningEffort = "off"
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
	// OpenAIEffort is the OpenAI reasoning_effort parameter ("low", "medium", "high", "max").
	OpenAIEffort string
	// GeminiThinkingLevel is the Gemini thinking level.
	GeminiThinkingLevel string
	// GeminiThinkingBudget is the Gemini thinking budget in tokens.
	GeminiThinkingBudget int
	// DeepSeekThinking is the DeepSeek thinking toggle ("enabled" or "disabled").
	DeepSeekThinking string
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
	case "openai_codex":
		return resolveOpenAICodexReasoning(effort)
	case "gemini":
		return resolveGeminiReasoning(effort)
	case "deepseek":
		return resolveDeepSeekReasoning(effort)
	default:
		// For families without reasoning support (default/grok, mistral, kimi),
		// return disabled config.
		return ReasoningConfig{Enabled: false}
	}
}

func resolveAnthropicReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningOff:
		return ReasoningConfig{Enabled: false}
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
	case ReasoningOff:
		return ReasoningConfig{Enabled: false}
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

// resolveOpenAICodexReasoning resolves reasoning for OpenAI Codex models (Responses API).
// Codex models always reason; "off" maps to the lowest effort.
func resolveOpenAICodexReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningOff:
		// Codex models always reason; use lowest effort as a proxy for "off"
		return ReasoningConfig{Enabled: true, OpenAIEffort: "low"}
	default:
		return resolveOpenAIReasoning(effort)
	}
}

// resolveDeepSeekReasoning resolves reasoning for DeepSeek models.
// DeepSeek V4 requires an explicit thinking toggle and supports reasoning_effort.
// Effort mapping: low/medium → high, maximum → max (per DeepSeek docs).
func resolveDeepSeekReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningOff:
		return ReasoningConfig{Enabled: false, DeepSeekThinking: "disabled"}
	case ReasoningMinimal, ReasoningLow, ReasoningMedium:
		// DeepSeek maps low/medium to high for compatibility.
		return ReasoningConfig{Enabled: true, OpenAIEffort: "high", DeepSeekThinking: "enabled"}
	case ReasoningHigh:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "high", DeepSeekThinking: "enabled"}
	case ReasoningMaximum:
		// DeepSeek uses "max" for maximum effort (xhigh → max).
		return ReasoningConfig{Enabled: true, OpenAIEffort: "max", DeepSeekThinking: "enabled"}
	default:
		return ReasoningConfig{Enabled: true, OpenAIEffort: "high", DeepSeekThinking: "enabled"}
	}
}

func resolveGeminiReasoning(effort ReasoningEffort) ReasoningConfig {
	switch effort {
	case ReasoningOff:
		return ReasoningConfig{Enabled: false, GeminiThinkingLevel: "none"}
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

// AgentReasoningMode returns the appropriate reasoning effort for an agent role.
// Primary agents (orchestrator, planner, coder, tester, executor) use full reasoning.
// Analytical auxiliary agents (router, reflector, researcher) use reduced reasoning.
// Mechanical auxiliary agents (compaction, title, summary, judge) skip reasoning entirely.
// Returns ReasoningOff when baseEffort is empty or "off", so providers explicitly disable.
func AgentReasoningMode(agentRole string, baseEffort ReasoningEffort) ReasoningEffort {
	if baseEffort == "" || baseEffort == ReasoningOff {
		return ReasoningOff
	}
	switch agentRole {
	case "orchestrator", "planner", "coder", "tester", "executor":
		return baseEffort // full mode
	case "router", "reflector", "researcher":
		// Analytical auxiliary agents use reduced reasoning
		if baseEffort == ReasoningHigh || baseEffort == ReasoningMaximum {
			return ReasoningLow
		}
		return ReasoningMinimal
	case "compaction", "title", "summary", "judge":
		// Mechanical tasks don't benefit from extended thinking
		return ReasoningOff
	default:
		return baseEffort
	}
}

// ResolveAgentReasoningMode is like AgentReasoningMode but checks roleOverrides first.
// If a role has an explicit override, that value is used verbatim (no further adaptation).
// Otherwise, falls back to AgentReasoningMode's default role-based adaptation.
func ResolveAgentReasoningMode(agentRole string, baseEffort ReasoningEffort, roleOverrides map[string]string) ReasoningEffort {
	if roleOverrides != nil {
		if override, ok := roleOverrides[agentRole]; ok && override != "" {
			return ReasoningEffort(override)
		}
	}
	return AgentReasoningMode(agentRole, baseEffort)
}
