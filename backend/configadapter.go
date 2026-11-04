package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// derefBool safely dereferences a *bool, defaulting to true when nil.
func derefBool(b *bool) bool {
	if b == nil {
		return true
	}
	return *b
}

// globLimitsExplicit reports whether the resolved glob config is explicitly
// customized — post-ApplyDefaults a non-default value can only be an explicit
// knob (including the explicit "0 disables"). A knob set to its default is
// indistinguishable from an absent one, but both resolve identically; the
// flag only gates the all-zero case, which requires the no-fallback
// constructor (NewGlobToolWithLimitsOverride).
func globLimitsExplicit(maxEntries, maxResults, timeoutSec int) bool {
	def := builtins.DefaultGlobLimits()
	return maxEntries != def.MaxEntries ||
		maxResults != def.MaxResults ||
		timeoutSec != int(def.Timeout/time.Second)
}

// derefInt dereferences a *int with a default: the *int knobs distinguish an
// absent key (nil → default) from an explicit zero ("0 disables"). A
// programmatically built config may bypass ApplyDefaults, so the deref is
// defensive.
func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// loadModelProfilesCatalog loads the full profile catalog (predefined ∪
// custom), logging store-level warnings; callers that must surface warnings
// in the UI instead use config.LoadModelProfilesCatalog directly.
func loadModelProfilesCatalog(agentDir string, log *slog.Logger) []config.ModelProfile {
	catalog, warnings := config.LoadModelProfilesCatalog(agentDir)
	if log != nil {
		for _, w := range warnings {
			log.Warn("model-profile profile warning", "warning", w)
		}
	}
	return catalog
}

// effectiveModelProfilesConfig resolves the persisted `model_profiles:` section
// against the catalog and returns the effective runtime profile. NOT gated by
// the experimental switch — the master toggle is the only switch, carried
// through verbatim (see specs/decisions/044-model-profiles-out-of-experimental.md);
// resolution warnings are returned for the caller to surface.
func effectiveModelProfilesConfig(cfg *config.Config, modelProfilesCatalog []config.ModelProfile) (profile config.ModelProfilesConfig, warnings []string) {
	return config.ResolveModelProfilesConfig(cfg.ModelProfiles, modelProfilesCatalog)
}

// activeModelProfile returns the catalog entry the persisted section resolves
// to, with the same soft fallback and retired-id aliasing
// config.ResolveModelProfilesConfig applies, so the reported identity always
// matches the effective values — reported even when the master toggle is off.
// The zero entry is returned only when even "generic" is missing from the
// catalog.
func activeModelProfile(persist config.ModelProfilesPersistConfig, catalog []config.ModelProfile) config.ModelProfile {
	id := persist.ActiveProfile
	if id == "" {
		id = config.ModelProfilesGenericProfileID
	}
	if p, _, ok := config.FindModelProfileResolvingLegacy(catalog, id); ok {
		return p
	}
	generic, _ := config.FindModelProfile(catalog, config.ModelProfilesGenericProfileID)
	return generic
}

// ToBuilderConfig converts a *config.Config into a *core.BuilderConfig — the
// single conversion point, so core never imports backend/config (see
// specs/architecture/layers.md). modelProfilesCatalog resolves the effective
// model profile; callers with an agent dir should build it via
// config.LoadModelProfilesCatalog so custom profiles apply without a restart.
func ToBuilderConfig(cfg *config.Config, modelProfilesCatalog []config.ModelProfile, logger ...*slog.Logger) *core.BuilderConfig {
	// Optional logger: residual inconsistencies a programmatic (load-bypassing)
	// config can carry are logged instead of silently ignored; the load
	// pipeline already warns for the normal path.
	var log *slog.Logger
	if len(logger) > 0 {
		log = logger[0]
	}
	// Build provider configs map from all enabled providers.
	allProviders := cfg.LLM.GetAllProviderConfigs()
	providerConfigs := make(map[string]core.BuilderProviderConfig, len(allProviders))
	for _, p := range allProviders {
		bpc := core.BuilderProviderConfig{
			ProviderType: p.ProviderType,
			APIKey:       p.APIKey,
			BaseURL:      p.BaseURL,
			Models:       p.Models,

			TLSFingerprint:     p.TLSFingerprint,
			OutputTokenReserve: p.OutputTokenReserve,
			TimeoutClass:       p.TimeoutClass,
		}
		// ChatGPT subscription auth (llm.chatgpt.auth.mode: "oauth"): mark the
		// entry SubscriptionAuth and pin its BaseURL to the Codex backend —
		// subscription credentials are accepted only there. The static
		// api_key still travels but is overridden on the wire; a signed-out
		// user gets an actionable error, never a silent key fallback. See
		// specs/domains/llm-providers.md (ChatGPT Subscription Auth).
		if p.Name == "chatgpt" && cfg.LLM.ChatGPT.Auth.Mode == config.ChatGPTAuthModeOAuth {
			bpc.SubscriptionAuth = true
			bpc.BaseURL = providerauth.ChatGPT().APIBaseURL
		}
		providerConfigs[p.Name] = bpc
	}

	// Convert model overrides.
	models := make(map[string]core.BuilderModelOverride, len(cfg.LLM.Models))
	for name, m := range cfg.LLM.Models {
		models[name] = core.BuilderModelOverride{
			ContextWindow:  m.ContextWindow,
			OutputLimit:    m.OutputLimit,
			TokenizerType:  m.TokenizerType,
			Family:         m.Family,
			Protocol:       m.Protocol,
			Capabilities:   m.Capabilities,
			RequestTimeout: m.RequestTimeout,
		}
	}

	// Convert tool groups (the security schema); entries are copied verbatim
	// — the system group is not configurable and config validation rejects
	// it — while the builder skips anything invalid defensively.
	groups := make(map[string]core.BuilderGroupPolicy, len(cfg.Security.Groups))
	for name, g := range cfg.Security.Groups {
		groups[name] = core.BuilderGroupPolicy{
			Policy:    g.Policy,
			Blocklist: g.Blocklist,
		}
	}
	// The execute blocklist is empty by default (the app ships no predefined
	// patterns), so an unset config list is materialized as an explicit empty
	// list, and the entry is CREATED when absent: core's UpdateShellBlocklist
	// fails closed on a missing execute group or nil blocklist, and an empty
	// list compiles to "no patterns" — the intended default posture.
	if g, ok := groups[config.ToolGroupExecute]; !ok {
		groups[config.ToolGroupExecute] = core.BuilderGroupPolicy{
			Blocklist: []string{},
		}
	} else if g.Blocklist == nil {
		g.Blocklist = []string{}
		groups[config.ToolGroupExecute] = g
	}

	// Convert MCP servers. Capability modes are carried verbatim from
	// capabilities.mcp: the load pipeline folds the legacy per-server mode
	// key in and canonicalizes every value, and the frontend rebuild paths
	// start from that validated config — the adapter needs no normalization
	// of its own (mirroring the timeout contract). A name absent from the
	// section maps to auto in both contexts.
	mcpServers := make(map[string]core.BuilderMCPServer, len(cfg.MCP.Servers))
	for name, srv := range cfg.MCP.Servers {
		caps := cfg.Capabilities.MCP[name]
		mcpServers[name] = core.BuilderMCPServer{
			Transport:   srv.Transport,
			Command:     srv.Command,
			Args:        srv.Args,
			Env:         srv.Env,
			URL:         srv.URL,
			Headers:     srv.Headers,
			ChatMode:    caps.ChatMode,
			CodeMode:    caps.CodeMode,
			Timeout:     parseMCPDuration(srv.Timeout),
			CallTimeout: parseMCPDuration(srv.CallTimeout),
		}
	}

	// The E2S mode has no separate master toggle — its availability is
	// exactly the experimental gate, mapped fail-closed below. The model
	// profile is resolved from the active catalog profile (warnings surface
	// at load time, not here).
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
			InjectionDefenseEnabled:    derefBool(cfg.Security.InjectionDefense.Enabled),
			Groups:                     groups,
			AutoApproveWorkspaceWrites: cfg.Security.AutoApproveWorkspaceWrites,
			AutonomyMode:               cfg.Security.AutonomyMode,
			SilentMode: core.BuilderSilentModeConfig{
				ToolConfirm: cfg.Security.SilentMode.ToolConfirm.Mode,
				UserConfirm: cfg.Security.SilentMode.UserConfirm.Mode,
				StepLimit:   cfg.Security.SilentMode.StepLimit.Mode,
				AskUser:     cfg.Security.SilentMode.AskUser.Mode,
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
			BaseURL:  cfg.Search.BaseURL,
		},
		MCP: core.BuilderMCPConfig{
			Servers: mcpServers,
		},
		// Capability modes for the skill/agent catalogs are carried verbatim
		// from capabilities.skills / capabilities.agents — the same contract
		// as the MCP section above: already canonicalized by the load
		// pipeline; a name absent from a section is auto in both contexts.
		Capabilities: core.BuilderCapabilitiesConfig{
			Skills: capabilityModesToCore(cfg.Capabilities.Skills),
			Agents: capabilityModesToCore(cfg.Capabilities.Agents),
		},
		Orchestration: core.BuilderOrchestrationConfig{
			MaxDependencyContextChars: cfg.Orchestration.MaxDependencyContextChars,
			MaxJudgeCacheSize:         cfg.Orchestration.MaxJudgeCacheSize,
			MaxRedelegationDepth:      cfg.Orchestration.MaxRedelegationDepth,
			MaxParallelSubagents:      cfg.Agents.MaxParallelSubagents,
		},
		GoalLoop: core.BuilderGoalLoopConfig{
			Verification: cfg.GoalLoop.Verification,
			Suggest: core.GoalSuggestSettings{
				Enabled:       cfg.GoalLoop.Suggest.SuggestEnabled(),
				MinComplexity: cfg.GoalLoop.Suggest.MinComplexity,
			},
		},
		E2S: core.BuilderE2SConfig{
			Enabled:                 cfg.Experimental.Enabled,
			MaxSteps:                cfg.E2S.MaxSteps,
			StateByteLimit:          cfg.E2S.StateByteLimit,
			PatchRetries:            cfg.E2S.PatchRetries,
			MaxObservationChars:     cfg.E2S.ObservationTruncate,
			ObservationBudgetTokens: cfg.E2S.ObservationBudgetTokens,
			ObservationFillFraction: cfg.E2S.ObservationFillFraction,
			RepeatNudgeThreshold:    cfg.E2S.RepeatNudgeThreshold,
			RepeatAbortThreshold:    cfg.E2S.RepeatAbortThreshold,
			Tools: core.BuilderE2SToolsConfig{
				Preset: cfg.E2S.Tools.Preset,
				Allow:  cfg.E2S.Tools.Allow,
				Deny:   cfg.E2S.Tools.Deny,
			},
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
			GlobMaxEntries:      derefInt(cfg.ToolLimits.GlobMaxEntries, builtins.DefaultGlobLimits().MaxEntries),
			GlobMaxResults:      derefInt(cfg.ToolLimits.GlobMaxResults, builtins.DefaultGlobLimits().MaxResults),
			GlobLimitsExplicit: globLimitsExplicit(
				derefInt(cfg.ToolLimits.GlobMaxEntries, builtins.DefaultGlobLimits().MaxEntries),
				derefInt(cfg.ToolLimits.GlobMaxResults, builtins.DefaultGlobLimits().MaxResults),
				derefInt(cfg.Timeouts.GlobTimeout, int(builtins.DefaultGlobLimits().Timeout/time.Second))),
			PerToolTruncation: convertTruncationMap(cfg.ToolLimits.PerToolTruncation),
		},
		Timeouts: core.BuilderTimeoutsConfig{
			BashMaxTimeout:  cfg.Timeouts.BashMaxTimeout,
			BashWaitDelay:   cfg.Timeouts.BashWaitDelay,
			RipgrepTimeout:  cfg.Timeouts.RipgrepTimeout,
			GlobTimeout:     derefInt(cfg.Timeouts.GlobTimeout, int(builtins.DefaultGlobLimits().Timeout/time.Second)),
			ToolCallTimeout: derefInt(cfg.Timeouts.ToolCallTimeout, int(agent.DefaultToolCallTimeout/time.Second)),
			// The ceiling's exempt tool-name set: nil (knob absent — the
			// normal load path) keeps sp4rk's built-in default set, resolved
			// at execution time and never persisted; an explicit list —
			// including empty ("no exemptions") — replaces it wholesale.
			ToolCallTimeoutExemptTools: []string(cfg.Timeouts.ToolCallTimeoutExemptTools),
			WebFetchTimeout:            cfg.Timeouts.WebFetchTimeout,
			WebFetchProxyTimeout:       cfg.Timeouts.WebFetchProxyTimeout,
			WebFetchRetries:            cfg.Timeouts.WebFetchRetries,
			WebSearchTimeout:           cfg.Timeouts.WebSearchTimeout,
			LLMRequestTimeout:          cfg.Timeouts.LLMRequestTimeout,
			// Adaptive request budget kill-switch (ADR-071 D11); derefBool
			// defaults an unset nil to the feature's enabled-by-default
			// posture.
			AdaptiveBudgetEnabled: derefBool(cfg.Timeouts.AdaptiveBudget.Enabled),
		},
		ShellExec: convertShellExecConfig(cfg.ShellExec, log),
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

// capabilityModesToCore converts a config-layer per-capability mode map into
// the core-layer mirror, value-for-value (core never imports backend/config).
func capabilityModesToCore(entries map[string]config.CapabilityModes) map[string]core.CapabilityModes {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]core.CapabilityModes, len(entries))
	for name, cm := range entries {
		out[name] = core.CapabilityModes{ChatMode: cm.ChatMode, CodeMode: cm.CodeMode}
	}
	return out
}

// parseMCPDuration resolves an MCP server duration string (timeout /
// call_timeout) into a time.Duration for the sp4rk ServerEntry. Empty
// resolves to 0 (the sp4rk default); unparseable or non-positive fails soft
// to 0, so a bad duration can never prevent a server from starting. It
// deliberately logs nothing — config.LoadWithResult is the single surfacing
// point for an invalid value, and the UI save path rejects one up front, so
// this branch only covers a hand-edited file or a programmatic config.
func parseMCPDuration(raw string) time.Duration {
	d, err := config.ParseMCPDuration(raw)
	if err != nil {
		return 0
	}
	return d
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
// workspace, searched ahead of the workspace-root file) in priority order:
// ~/.agents/AGENTS.md (global), then ~/.c0wrk/.agents/AGENTS.md
// (c0wrk-specific). Missing files are silently skipped at read time, so both
// candidates are returned unconditionally.
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
// guarantees an active override carries a valid template and declared kind;
// residual inconsistency fails soft to the built-in default.
func convertShellExecConfig(sec config.ShellExecConfig, log *slog.Logger) core.BuilderShellExecConfig {
	return core.BuilderShellExecConfig{
		BashExec: convertShellExecTool("bash_exec", sec.BashExec, log),
		PoshExec: convertShellExecTool("posh_exec", sec.PoshExec, log),
	}
}

// convertShellExecTool converts one tool's override; nil = no override. A
// residual inconsistency — a programmatic config that bypassed
// normalizeShellExec — is logged when a logger is provided rather than dropped
// silently.
func convertShellExecTool(name string, tool config.ShellExecToolConfig, log *slog.Logger) *sdktools.ShellInvocation {
	if !tool.OverrideActive() {
		return nil
	}
	kind, err := sdktools.ParseShellKind(tool.Shell)
	if err != nil {
		if log != nil {
			log.Warn("shell_exec override dropped: undeclared shell kind (falling back to the built-in launch shape)",
				"tool", name, "shell", tool.Shell, "error", err)
		}
		return nil
	}
	inv := sdktools.ShellInvocation{
		Binary: tool.Command[0],
		Args:   append([]string(nil), tool.Command[1:]...),
		Kind:   kind,
	}
	if err := inv.Validate(); err != nil {
		if log != nil {
			log.Warn("shell_exec override dropped: invalid invocation (falling back to the built-in launch shape)",
				"tool", name, "error", err)
		}
		return nil
	}
	return &inv
}
