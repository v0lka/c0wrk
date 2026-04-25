package backend

import (
	"github.com/user/agent/backend/config"
	"github.com/user/agent/core"
)

// ToBuilderConfig converts a *config.Config into a *core.BuilderConfig.
// This is the single conversion point so that core never imports backend/config.
func ToBuilderConfig(cfg *config.Config) *core.BuilderConfig {
	provType, apiKey, baseURL, model := cfg.LLM.GetActiveProviderConfig()

	// Convert model overrides.
	models := make(map[string]core.BuilderModelOverride, len(cfg.LLM.Models))
	for name, m := range cfg.LLM.Models {
		models[name] = core.BuilderModelOverride{
			ContextWindow: m.ContextWindow,
			OutputLimit:   m.OutputLimit,
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
			ActiveProvider: cfg.LLM.ActiveProvider,
			ProviderType:   provType,
			APIKey:         apiKey,
			BaseURL:        baseURL,
			Model:          model,
			Retry: core.BuilderRetryConfig{
				MaxRetries:     cfg.LLM.Retry.MaxRetries,
				InitialBackoff: cfg.LLM.Retry.InitialBackoff,
				MaxBackoff:     cfg.LLM.Retry.MaxBackoff,
			},
			Models:              models,
			ChatGPTAPIKey:       cfg.LLM.ChatGPT.APIKey,
			OpenAICompatBaseURL: cfg.LLM.OpenAICompatible.BaseURL,
			OpenAICompatAPIKey:  cfg.LLM.OpenAICompatible.APIKey,
			LMStudioBaseURL:     cfg.LLM.LMStudio.BaseURL,
			LMStudioAPIKey:      cfg.LLM.LMStudio.APIKey,
		},
		Router: core.BuilderRouterConfig{
			HistoryWindow: cfg.Router.HistoryWindow,
		},
		Executor: core.BuilderExecutorConfig{
			MaxReactSteps:      cfg.Executor.MaxReactSteps,
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
					DistantRatio: cfg.Executor.Compaction.Hierarchical.DistantRatio,
					MiddleRatio:  cfg.Executor.Compaction.Hierarchical.MiddleRatio,
					RecentRatio:  cfg.Executor.Compaction.Hierarchical.RecentRatio,
				},
				Thresholds: core.BuilderCompactionThresholds{
					PredictivePercent: cfg.Executor.Compaction.Thresholds.PredictivePercent,
					WarningPercent:    cfg.Executor.Compaction.Thresholds.WarningPercent,
					EmergencyPercent:  cfg.Executor.Compaction.Thresholds.EmergencyPercent,
				},
				MaxSummarizeTokens:  cfg.Executor.Compaction.MaxSummarizeTokens,
				ObservationTruncate: cfg.Executor.Compaction.ObservationTruncate,
				SafetyMarginPercent: cfg.Executor.Compaction.SafetyMarginPercent,
			},
			ToolResultBudget: core.BuilderToolResultBudget{
				HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
				MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
			},
			ToolOutputPruning: core.BuilderToolOutputPruning{
				KeepLastN:        cfg.Executor.ToolOutputPruning.KeepLastN,
				ProtectedTools:   cfg.Executor.ToolOutputPruning.ProtectedTools,
				ThresholdPercent: cfg.Executor.ToolOutputPruning.ThresholdPercent,
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
			JudgeModel:    cfg.Security.Judge.Model,
			ToolPolicies:  toolPolicies,
			DefaultPolicy: cfg.Security.DefaultPolicy,
		},
		Search: core.BuilderSearchConfig{
			Provider: cfg.Search.Provider,
			APIKey:   cfg.Search.APIKey,
		},
		MCP: core.BuilderMCPConfig{
			Servers: mcpServers,
		},
		Orchestration: core.BuilderOrchestrationConfig{
			MaxHistoryMessages:        cfg.Orchestration.MaxHistoryMessages,
			MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
			MaxJudgeCacheSize:         cfg.Orchestration.MaxJudgeCacheSize,
			MaxPlannerExploreSteps:    cfg.Orchestration.MaxPlannerExploreSteps,
			SyntheticPlanThreshold:    cfg.Orchestration.SyntheticPlanThreshold,
		},
		ToolLimits: core.BuilderToolLimitsConfig{
			ReadDefaultLines:     cfg.ToolLimits.ReadDefaultLines,
			ReadMaxLineLength:    cfg.ToolLimits.ReadMaxLineLength,
			ReadMaxBytes:         cfg.ToolLimits.ReadMaxBytes,
			FileSearchMaxMatches: cfg.ToolLimits.FileSearchMaxMatches,
			RipgrepMaxResults:    cfg.ToolLimits.RipgrepMaxResults,
			RipgrepMaxLineLength: cfg.ToolLimits.RipgrepMaxLineLength,
			GlobMaxResults:       cfg.ToolLimits.GlobMaxResults,
			WebSearchMaxResults:  cfg.ToolLimits.WebSearchMaxResults,
			WebFetchMaxBodySize:  cfg.ToolLimits.WebFetchMaxBodySize,
		},
		Timeouts: core.BuilderTimeoutsConfig{
			BashMaxTimeout:   cfg.Timeouts.BashMaxTimeout,
			BashWaitDelay:    cfg.Timeouts.BashWaitDelay,
			RipgrepTimeout:   cfg.Timeouts.RipgrepTimeout,
			WebFetchTimeout:  cfg.Timeouts.WebFetchTimeout,
			WebSearchTimeout: cfg.Timeouts.WebSearchTimeout,
		},
		ExpandEnvVars: config.ExpandEnvVars,
	}
}
