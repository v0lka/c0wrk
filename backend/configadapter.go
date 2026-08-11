package backend

import (
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
)

// derefBool safely dereferences a *bool, defaulting to true when nil.
func derefBool(b *bool) bool {
	if b == nil {
		return true
	}
	return *b
}

// ToBuilderConfig converts a *config.Config into a *core.BuilderConfig.
// This is the single conversion point so that core never imports backend/config.
func ToBuilderConfig(cfg *config.Config) *core.BuilderConfig {
	// Build provider configs map from all enabled providers.
	allProviders := cfg.LLM.GetAllProviderConfigs()
	providerConfigs := make(map[string]core.BuilderProviderConfig, len(allProviders))
	for _, p := range allProviders {
		providerConfigs[p.Name] = core.BuilderProviderConfig{
			ProviderType: p.ProviderType,
			APIKey:       p.APIKey,
			BaseURL:      p.BaseURL,
			Models:       p.Models,
		}
	}

	// Convert model overrides.
	models := make(map[string]core.BuilderModelOverride, len(cfg.LLM.Models))
	for name, m := range cfg.LLM.Models {
		models[name] = core.BuilderModelOverride{
			ContextWindow: m.ContextWindow,
			OutputLimit:   m.OutputLimit,
			TokenizerType: m.TokenizerType,
			Family:        m.Family,
			Protocol:      m.Protocol,
			Capabilities:  m.Capabilities,
		}
	}

	// Convert tool policies.
	toolPolicies := make(map[string]core.BuilderToolPolicy, len(cfg.Security.ToolPolicies))
	for name, p := range cfg.Security.ToolPolicies {
		toolPolicies[name] = core.BuilderToolPolicy{
			Policy:    p.Policy,
			Blacklist: p.Blacklist,
		}
	}

	// Convert MCP servers.
	mcpServers := make(map[string]core.BuilderMCPServer, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		mcpServers[name] = core.BuilderMCPServer{
			Transport: srv.Transport,
			Command:   srv.Command,
			Args:      srv.Args,
			Env:       srv.Env,
			URL:       srv.URL,
			Headers:   srv.Headers,
		}
	}

	return &core.BuilderConfig{
		LLM: core.BuilderLLMConfig{
			DefaultModel:    cfg.LLM.DefaultModel,
			ProviderConfigs: providerConfigs,
			Retry: core.BuilderRetryConfig{
				MaxRetries:     cfg.LLM.Retry.MaxRetries,
				InitialBackoff: cfg.LLM.Retry.InitialBackoff,
				MaxBackoff:     cfg.LLM.Retry.MaxBackoff,
			},
			Models: models,
		},

		Router: core.BuilderRouterConfig{
			HistoryWindow: cfg.Router.HistoryWindow,
		},
		Executor: core.BuilderExecutorConfig{
			MaxRetries:         cfg.Executor.MaxRetries,
			OutputTokenReserve: cfg.Executor.OutputTokenReserve,
			Compaction: core.BuilderCompactionConfig{
				SlidingWindow: core.BuilderSlidingWindow{
					KeepFirst: cfg.Executor.Compaction.SlidingWindow.KeepFirst,
					KeepLast:  cfg.Executor.Compaction.SlidingWindow.KeepLast,
				},
				Summarization: core.BuilderSummarization{
					BlockSize: cfg.Executor.Compaction.Summarization.BlockSize,
					KeepLast:  cfg.Executor.Compaction.Summarization.KeepLast,
				},
				Hierarchical: core.BuilderHierarchical{
					EnabledAboveSteps: cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps,
					DistantRatio:      cfg.Executor.Compaction.Hierarchical.DistantRatio,
					MiddleRatio:       cfg.Executor.Compaction.Hierarchical.MiddleRatio,
					RecentRatio:       cfg.Executor.Compaction.Hierarchical.RecentRatio,
				},
				Thresholds: core.BuilderCompactionThresholds{
					PredictivePercent: cfg.Executor.Compaction.Thresholds.PredictivePercent,
					WarningPercent:    cfg.Executor.Compaction.Thresholds.WarningPercent,
					EmergencyPercent:  cfg.Executor.Compaction.Thresholds.EmergencyPercent,
					PreWarningPercent: cfg.Executor.Compaction.Thresholds.PreWarningPercent,
				},
				MaxSummarizeTokens:  cfg.Executor.Compaction.MaxSummarizeTokens,
				ObservationTruncate: cfg.Executor.Compaction.ObservationTruncate,
				SafetyMarginPercent: cfg.Executor.Compaction.SafetyMarginPercent,
			},
			ToolResultBudget: core.BuilderToolResultBudget{
				HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
				MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
				CacheTTLSeconds: cfg.Executor.ToolResultBudget.CacheTTLSeconds,
			},
			ToolOutputPruning: core.BuilderToolOutputPruning{
				KeepLastN:        cfg.Executor.ToolOutputPruning.KeepLastN,
				ProtectedTools:   cfg.Executor.ToolOutputPruning.ProtectedTools,
				ThresholdPercent: cfg.Executor.ToolOutputPruning.ThresholdPercent,
			},
			HistoryMutation: core.BuilderHistoryMutation{
				ToolResultEvictionStep: cfg.Executor.HistoryMutation.ToolResultEvictionStep,
				EvictStepStatus:        cfg.Executor.HistoryMutation.EvictStepStatus,
				DedupRepeatedReads:     cfg.Executor.HistoryMutation.DedupRepeatedReads,
			},
			CircuitBreaker: core.BuilderCircuitBreaker{
				RepeatNudgeThreshold:         cfg.Executor.CircuitBreaker.RepeatNudgeThreshold,
				RepeatAbortThreshold:         cfg.Executor.CircuitBreaker.RepeatAbortThreshold,
				TruncationAbortThreshold:     cfg.Executor.CircuitBreaker.TruncationAbortThreshold,
				ParseErrorAbortThreshold:     cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold,
				FruitlessNudgeThreshold:      cfg.Executor.CircuitBreaker.FruitlessNudgeThreshold,
				FruitlessAbortThreshold:      cfg.Executor.CircuitBreaker.FruitlessAbortThreshold,
				FruitlessMaxResultLen:        cfg.Executor.CircuitBreaker.FruitlessMaxResultLen,
				SameToolRepeatNudgeThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatNudgeThreshold,
				SameToolRepeatAbortThreshold: cfg.Executor.CircuitBreaker.SameToolRepeatAbortThreshold,
				SameToolResultSizeDelta:      cfg.Executor.CircuitBreaker.SameToolResultSizeDelta,
			},
		},
		Security: core.BuilderSecurityConfig{
			JudgeModel:                 cfg.Security.Judge.Model,
			InjectionDefenseEnabled:    derefBool(cfg.Security.InjectionDefense.Enabled),
			ToolPolicies:               toolPolicies,
			DefaultPolicy:              cfg.Security.DefaultPolicy,
			AutoApproveWorkspaceWrites: cfg.Security.AutoApproveWorkspaceWrites,
			SmartApprove:               cfg.Security.SmartApprove,
			AgentsMDMaxBytes:           cfg.Security.AgentsMDMaxBytes,
			AgentsMDSearchPaths:        agentsMDSearchPaths(),
		},
		Skills: core.BuilderSkillsConfig{
			Dirs: cfg.Skills.Dirs,
		},
		Search: core.BuilderSearchConfig{
			Provider: cfg.Search.Provider,
			APIKey:   cfg.Search.APIKey,
		},
		MCP: core.BuilderMCPConfig{
			Servers: mcpServers,
		},
		Orchestration: core.BuilderOrchestrationConfig{
			MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
			MaxJudgeCacheSize:         cfg.Orchestration.MaxJudgeCacheSize,
			MaxRedelegationDepth:      cfg.Orchestration.MaxRedelegationDepth,
		},
		GoalLoop: core.BuilderGoalLoopConfig{
			Verification: cfg.GoalLoop.Verification,
		},
		SmallLLM: core.BuilderSmallLLMConfig{
			Enabled: cfg.SmallLLM.Enabled,
			EssentialTools: core.BuilderSmallLLMEssentialConfig{
				Enabled:       cfg.SmallLLM.EssentialTools.Enabled,
				AlwaysPresent: cfg.SmallLLM.EssentialTools.AlwaysPresent,
				MaxTools:      cfg.SmallLLM.EssentialTools.MaxTools,
			},
			Sampling: core.BuilderSmallLLMSampling{
				Enabled:         cfg.SmallLLM.Sampling.Enabled,
				Temperature:     cfg.SmallLLM.Sampling.Temperature,
				TopP:            cfg.SmallLLM.Sampling.TopP,
				ReasoningEffort: cfg.SmallLLM.Sampling.ReasoningEffort,
			},
			LoopHardening: core.BuilderLoopHardening{
				Enabled:                      cfg.SmallLLM.LoopHardening.Enabled,
				RepeatNudgeThreshold:         cfg.SmallLLM.LoopHardening.RepeatNudgeThreshold,
				ParseErrorAbortThreshold:     cfg.SmallLLM.LoopHardening.ParseErrorAbortThreshold,
				FruitlessNudgeThreshold:      cfg.SmallLLM.LoopHardening.FruitlessNudgeThreshold,
				FruitlessAbortThreshold:      cfg.SmallLLM.LoopHardening.FruitlessAbortThreshold,
				SameToolRepeatNudgeThreshold: cfg.SmallLLM.LoopHardening.SameToolRepeatNudgeThreshold,
			},
			SystemPrompt: core.BuilderSmallLLMSystemPromptConfig{
				Lite:              cfg.SmallLLM.SystemPrompt.Lite,
				FewShot:           cfg.SmallLLM.SystemPrompt.FewShot,
				ReasoningScaffold: cfg.SmallLLM.SystemPrompt.ReasoningScaffold,
			},
		},
		ToolLimits: core.BuilderToolLimitsConfig{
			ReadDefaultLines:    cfg.ToolLimits.ReadDefaultLines,
			WebSearchMaxResults: cfg.ToolLimits.WebSearchMaxResults,
			PerToolTruncation:   convertTruncationMap(cfg.ToolLimits.PerToolTruncation),
		},
		Timeouts: core.BuilderTimeoutsConfig{
			BashMaxTimeout:    cfg.Timeouts.BashMaxTimeout,
			BashWaitDelay:     cfg.Timeouts.BashWaitDelay,
			RipgrepTimeout:    cfg.Timeouts.RipgrepTimeout,
			WebFetchTimeout:   cfg.Timeouts.WebFetchTimeout,
			WebSearchTimeout:  cfg.Timeouts.WebSearchTimeout,
			LLMRequestTimeout: cfg.Timeouts.LLMRequestTimeout,
		},
		Proxy: proxy.Config{
			Enabled:      cfg.Proxy.Enabled,
			URL:          config.ExpandEnvVars(cfg.Proxy.URL),
			BypassList:   cfg.Proxy.BypassList,
			TLSCertDir:   config.ExpandEnvVars(cfg.Proxy.TLSCertDir),
			SetGlobalEnv: derefBool(cfg.Proxy.SetGlobalEnv),
		},
		ExpandEnvVars: config.ExpandEnvVars,
	}
}

// convertTruncationMap converts config-level ToolTruncationConfig to builder-level.
func convertTruncationMap(src map[string]config.ToolTruncationConfig) map[string]core.BuilderToolTruncationConfig {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]core.BuilderToolTruncationConfig, len(src))
	for k, v := range src {
		dst[k] = core.BuilderToolTruncationConfig{
			MaxLines: v.MaxLines,
			MaxBytes: v.MaxBytes,
		}
	}
	return dst
}

// agentsMDSearchPaths resolves the extra AGENTS.md search paths (outside the
// workspace) in priority order. The paths are searched ahead of the
// workspace-root AGENTS.md, so the first entry is the global file shared by
// all agents and the second is the c0wrk-specific file:
//
//  1. ~/.agents/AGENTS.md            — global, shared across all agents
//  2. ~/.c0wrk/.agents/AGENTS.md     — c0wrk-specific
//
// Each entry points directly at an AGENTS.md file. Missing files are silently
// skipped at read time (the orchestrator tolerates non-existent paths), so we
// always return both candidates unconditionally.
func agentsMDSearchPaths() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return nil
	}
	return []string{
		filepath.Join(homeDir, ".agents", "AGENTS.md"),
		filepath.Join(homeDir, config.DefaultAgentDir, ".agents", "AGENTS.md"),
	}
}
