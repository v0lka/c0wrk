package backend

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// derefBool safely dereferences a *bool, defaulting to true when nil.
func derefBool(b *bool) bool {
	if b == nil {
		return true
	}
	return *b
}

// loadModelProfilesCatalog loads the full model-profile profile catalog (predefined ∪
// custom), logging store-level warnings. Callers that must surface warnings
// in the UI instead (config load time) use config.LoadModelProfilesCatalog directly —
// see config.ResolveAndLoad.
func loadModelProfilesCatalog(agentDir string, log *slog.Logger) []config.ModelProfile {
	catalog, warnings := config.LoadModelProfilesCatalog(agentDir)
	if log != nil {
		for _, w := range warnings {
			log.Warn("model-profile profile warning", "warning", w)
		}
	}
	return catalog
}

// effectiveModelProfilesConfig resolves the persisted `model_profiles:` section against the given
// profile catalog (predefined ∪ custom — see config.LoadModelProfilesCatalog) and
// returns the effective runtime profile. Model Profiles is NOT gated by the
// experimental-features switch: the master toggle (model_profiles.enabled) is
// the only switch and is carried through verbatim, so the effective profile is
// exactly what the operator persisted. Resolution warnings are returned for
// the caller to surface (load time) or log (runtime re-resolves).
func effectiveModelProfilesConfig(cfg *config.Config, modelProfilesCatalog []config.ModelProfile) (profile config.ModelProfilesConfig, warnings []string) {
	return config.ResolveModelProfilesConfig(cfg.ModelProfiles, modelProfilesCatalog)
}

// activeModelProfile returns the catalog entry the persisted `model_profiles:` section
// resolves to, applying the same soft fallback config.ResolveModelProfilesConfig uses
// for the effective values: an empty or dangling active_profile id falls
// back to the model-agnostic "generic" profile. The entry identifies the
// active profile (id + kind) for agent_metrics annotation — reported even
// when the master toggle is off. The zero entry is returned only when even
// "generic" is missing from the catalog (never the case with the shipped
// predefined catalog); the metrics meta then carries no profile fields.
func activeModelProfile(persist config.ModelProfilesPersistConfig, catalog []config.ModelProfile) config.ModelProfile {
	id := persist.ActiveProfile
	if id == "" {
		id = config.ModelProfilesGenericProfileID
	}
	if p, ok := config.FindModelProfile(catalog, id); ok {
		return p
	}
	generic, _ := config.FindModelProfile(catalog, config.ModelProfilesGenericProfileID)
	return generic
}

// ToBuilderConfig converts a *config.Config into a *core.BuilderConfig.
// This is the single conversion point so that core never imports backend/config.
// modelProfilesCatalog is the profile catalog (predefined ∪ custom) used to resolve the
// effective model-profile profile; callers that have an agent dir should build it
// via config.LoadModelProfilesCatalog so custom profiles apply without a restart.
func ToBuilderConfig(cfg *config.Config, modelProfilesCatalog []config.ModelProfile) *core.BuilderConfig {
	// Build provider configs map from all enabled providers.
	allProviders := cfg.LLM.GetAllProviderConfigs()
	providerConfigs := make(map[string]core.BuilderProviderConfig, len(allProviders))
	for _, p := range allProviders {
		providerConfigs[p.Name] = core.BuilderProviderConfig{
			ProviderType: p.ProviderType,
			APIKey:       p.APIKey,
			BaseURL:      p.BaseURL,
			Models:       p.Models,

			TLSFingerprint:     p.TLSFingerprint,
			OutputTokenReserve: p.OutputTokenReserve,
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

	// Convert tool groups (the security schema). The system group is not
	// configurable and config validation already rejects it; entries are
	// copied verbatim — the builder skips anything invalid defensively.
	groups := make(map[string]core.BuilderGroupPolicy, len(cfg.Security.Groups))
	for name, g := range cfg.Security.Groups {
		groups[name] = core.BuilderGroupPolicy{
			Policy:    g.Policy,
			Blocklist: g.Blocklist,
		}
	}
	// Effective view for the core layer: the execute blocklist is empty by
	// default (the app ships no predefined patterns), so an unset (nil)
	// config list is materialized as an explicit empty list, and the entry
	// is CREATED when absent. core's UpdateShellBlocklist fails closed on a
	// missing execute group or a nil blocklist (it cannot distinguish
	// "unset" from an incomplete programmatic config), so the conversion
	// always hands it a non-nil list; an empty list compiles to "no
	// patterns" — the intended default posture.
	if g, ok := groups[config.ToolGroupExecute]; !ok {
		groups[config.ToolGroupExecute] = core.BuilderGroupPolicy{
			Blocklist: []string{},
		}
	} else if g.Blocklist == nil {
		g.Blocklist = []string{}
		groups[config.ToolGroupExecute] = g
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

	// Effective view for the core layer: the E2S mode has no separate master
	// toggle — its availability is exactly the experimental gate, so
	// BuilderE2SConfig.Enabled is mapped from cfg.Experimental.Enabled below
	// (fail-closed). The section's numeric knobs are always seeded so tuning
	// never requires a rebuild.
	//
	// The model-profile profile is resolved from the active profile in the
	// catalog (resolution warnings are surfaced at load time, not here). It is
	// not gated by the experimental-features switch.
	modelProfiles, _ := effectiveModelProfilesConfig(cfg, modelProfilesCatalog)
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
				Forecast: core.BuilderCompactionForecast{
					SummarizationRatio:       cfg.Executor.Compaction.Forecast.SummarizationRatio,
					HierarchicalDistantRatio: cfg.Executor.Compaction.Forecast.HierarchicalDistantRatio,
					HierarchicalMiddleRatio:  cfg.Executor.Compaction.Forecast.HierarchicalMiddleRatio,
				},
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
			VerifyOnEdit: core.BuilderVerifyOnEditConfig{
				Enabled:        cfg.Executor.VerifyOnEdit.Enabled,
				Command:        cfg.Executor.VerifyOnEdit.Command,
				Timeout:        cfg.Executor.VerifyOnEdit.Timeout,
				MaxOutputChars: cfg.Executor.VerifyOnEdit.MaxOutputChars,
			},
		},
		Security: core.BuilderSecurityConfig{
			JudgeModel:                 cfg.Security.Judge.Model,
			InjectionDefenseEnabled:    derefBool(cfg.Security.InjectionDefense.Enabled),
			Groups:                     groups,
			AutoApproveWorkspaceWrites: cfg.Security.AutoApproveWorkspaceWrites,
			AutonomyMode:               cfg.Security.AutonomyMode,
			SilentMode: core.BuilderSilentModeConfig{
				ToolConfirm:  cfg.Security.SilentMode.ToolConfirm.Mode,
				StepLimit:    cfg.Security.SilentMode.StepLimit.Mode,
				AskUser:      cfg.Security.SilentMode.AskUser.Mode,
				ReviewPrompt: cfg.Security.SilentMode.ReviewPrompt.Mode,
			},
			AgentsMDMaxBytes:    cfg.Security.AgentsMDMaxBytes,
			AgentsMDSearchPaths: agentsMDSearchPaths(),
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
			MaxParallelSubagents:      cfg.Agents.MaxParallelSubagents,
		},
		GoalLoop: core.BuilderGoalLoopConfig{
			Verification: cfg.GoalLoop.Verification,
		},
		E2S: core.BuilderE2SConfig{
			Enabled:              cfg.Experimental.Enabled,
			MaxSteps:             cfg.E2S.MaxSteps,
			StateByteLimit:       cfg.E2S.StateByteLimit,
			PatchRetries:         cfg.E2S.PatchRetries,
			MaxObservationChars:  cfg.E2S.ObservationTruncate,
			RepeatNudgeThreshold: cfg.E2S.RepeatNudgeThreshold,
			RepeatAbortThreshold: cfg.E2S.RepeatAbortThreshold,
		},
		ModelProfiles: core.BuilderModelProfilesConfig{
			Enabled: modelProfiles.Enabled,
			EssentialTools: core.BuilderModelProfilesEssentialConfig{
				Enabled:             modelProfiles.EssentialTools.Enabled,
				AlwaysPresent:       modelProfiles.EssentialTools.AlwaysPresent,
				CompactDescriptions: modelProfiles.EssentialTools.CompactDescriptions,
			},
			Sampling: core.BuilderModelProfilesSampling{
				Enabled:           modelProfiles.Sampling.Enabled,
				Temperature:       modelProfiles.Sampling.Temperature,
				TopP:              modelProfiles.Sampling.TopP,
				TopK:              modelProfiles.Sampling.TopK,
				RepetitionPenalty: modelProfiles.Sampling.RepetitionPenalty,
				PresencePenalty:   modelProfiles.Sampling.PresencePenalty,
				ReasoningEffort:   modelProfiles.Sampling.ReasoningEffort,
			},
			LoopHardening: core.BuilderLoopHardening{
				Enabled:                      modelProfiles.LoopHardening.Enabled,
				RepeatNudgeThreshold:         modelProfiles.LoopHardening.RepeatNudgeThreshold,
				ParseErrorAbortThreshold:     modelProfiles.LoopHardening.ParseErrorAbortThreshold,
				FruitlessNudgeThreshold:      modelProfiles.LoopHardening.FruitlessNudgeThreshold,
				FruitlessAbortThreshold:      modelProfiles.LoopHardening.FruitlessAbortThreshold,
				SameToolRepeatNudgeThreshold: modelProfiles.LoopHardening.SameToolRepeatNudgeThreshold,
			},
			SystemPrompt: core.BuilderModelProfilesSystemPromptConfig{
				Lite:              modelProfiles.SystemPrompt.Lite,
				FewShot:           modelProfiles.SystemPrompt.FewShot,
				ReasoningScaffold: modelProfiles.SystemPrompt.ReasoningScaffold,
			},
			Context: core.BuilderModelProfilesContext{
				Enabled: modelProfiles.Context.Enabled,
				Compaction: core.BuilderModelProfilesCompaction{
					KeepLast:       modelProfiles.Context.Compaction.KeepLast,
					BlockSize:      modelProfiles.Context.Compaction.BlockSize,
					TriggerPercent: modelProfiles.Context.Compaction.TriggerPercent,
				},
				ToolOutputKeepLastN: modelProfiles.Context.ToolOutputKeepLastN,
				OutputTokenReserve:  modelProfiles.Context.OutputTokenReserve,
			},
		},
		ToolLimits: core.BuilderToolLimitsConfig{
			ReadDefaultLines:    cfg.ToolLimits.ReadDefaultLines,
			WebSearchMaxResults: cfg.ToolLimits.WebSearchMaxResults,
			PerToolTruncation:   convertTruncationMap(cfg.ToolLimits.PerToolTruncation),
		},
		Timeouts: core.BuilderTimeoutsConfig{
			BashMaxTimeout:       cfg.Timeouts.BashMaxTimeout,
			BashWaitDelay:        cfg.Timeouts.BashWaitDelay,
			RipgrepTimeout:       cfg.Timeouts.RipgrepTimeout,
			WebFetchTimeout:      cfg.Timeouts.WebFetchTimeout,
			WebFetchProxyTimeout: cfg.Timeouts.WebFetchProxyTimeout,
			WebFetchRetries:      cfg.Timeouts.WebFetchRetries,
			WebSearchTimeout:     cfg.Timeouts.WebSearchTimeout,
			LLMRequestTimeout:    cfg.Timeouts.LLMRequestTimeout,
		},
		ShellExec: convertShellExecConfig(cfg.ShellExec),
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

// convertShellExecConfig maps the validated config `shell_exec` section onto
// the builder's per-tool launch-shape invocations. The load pipeline
// (config.normalizeShellExec) guarantees an active override carries a valid
// template and a declared kind; any residual inconsistency (a
// programmatically-built config that bypassed load) fails soft to the
// built-in default rather than erroring the whole conversion.
func convertShellExecConfig(sec config.ShellExecConfig) core.BuilderShellExecConfig {
	return core.BuilderShellExecConfig{
		BashExec: convertShellExecTool(sec.BashExec),
		PoshExec: convertShellExecTool(sec.PoshExec),
	}
}

// convertShellExecTool converts one tool's override; nil = no override.
func convertShellExecTool(tool config.ShellExecToolConfig) *sdktools.ShellInvocation {
	if !tool.OverrideActive() {
		return nil
	}
	kind, err := sdktools.ParseShellKind(tool.Shell)
	if err != nil {
		return nil
	}
	inv := sdktools.ShellInvocation{
		Binary: tool.Command[0],
		Args:   append([]string(nil), tool.Command[1:]...),
		Kind:   kind,
	}
	if err := inv.Validate(); err != nil {
		return nil
	}
	return &inv
}
