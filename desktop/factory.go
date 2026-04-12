package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	// SDK layer
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	sdkmemory "github.com/user/agent/sdk/memory"
	"github.com/user/agent/sdk/orchestration"

	// Core layer
	"github.com/user/agent/core"
	coreprompts "github.com/user/agent/core/prompts"
	"github.com/user/agent/core/tools"

	// Backend layer
	"github.com/user/agent/backend/config"
)

// buildRouter creates a new Router and ModelRegistry from the given config.
// This is extracted from Startup() so it can be called per-session in the factory closure.
func (a *App) buildRouter(cfg *config.Config) (*llm.Router, *llm.ModelRegistry, error) {
	// Create ModelRegistry from config overrides
	overrides := make(map[string]llm.ModelMetadata)
	for name, override := range cfg.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: "approximate",
		}
	}
	modelRegistry := llm.NewModelRegistry(overrides)

	// Initialize LLM Router
	provType, apiKey, baseURL, model := cfg.LLM.GetActiveProviderConfig()
	initialBackoff, err := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	if err != nil && cfg.LLM.Retry.InitialBackoff != "" {
		slog.Warn("invalid initial_backoff, using default", "value", cfg.LLM.Retry.InitialBackoff, "error", err)
	}
	maxBackoff, err := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
	if err != nil && cfg.LLM.Retry.MaxBackoff != "" {
		slog.Warn("invalid max_backoff, using default", "value", cfg.LLM.Retry.MaxBackoff, "error", err)
	}
	routerCfg := llm.RouterConfig{
		ActiveProvider: cfg.LLM.ActiveProvider,
		ProviderType:   provType,
		APIKey:         config.ExpandEnvVars(apiKey),
		BaseURL:        config.ExpandEnvVars(baseURL),
		Model:          model,
		MaxRetries:     cfg.LLM.Retry.MaxRetries,
		InitialBackoff: initialBackoff,
		MaxBackoff:     maxBackoff,
	}
	llmRouter, err := llm.NewRouter(context.Background(), routerCfg, modelRegistry)
	if err != nil {
		return nil, nil, err
	}
	return llmRouter, modelRegistry, nil
}

// buildCoreAgents creates the Phase 2 components (router, planner, reflector)
// from the given LLM router and config. Returns nil values if llmRouter is nil.
// When emitter is non-nil, each component receives a token-tracking wrapper so
// that service-level LLM calls are accumulated in session totals.
// modelRegistry is wired to all components for tier resolution.
func (a *App) buildCoreAgents(llmRouter *llm.Router, registry *tools.ToolRegistry, cfg *config.Config, emitter core.Emitter, logger *slog.Logger, modelRegistry *llm.ModelRegistry) (*core.Router, *core.Planner, *core.Reflector) {
	if llmRouter == nil {
		return nil, nil, nil
	}
	caller := orchestration.NewTokenTrackingCaller(llmRouter, emitter)
	caller = core.NewLoggingCaller(caller, cfg.LLM.ActiveProvider, logger)
	router := core.NewRouter(caller, cfg.Router.HistoryWindow)
	planner := core.NewPlanner(caller)
	reflector := core.NewReflector(caller)

	// Wire model registry to all components for tier resolution
	if modelRegistry != nil {
		router.SetModelRegistry(modelRegistry)
		planner.SetModelRegistry(modelRegistry)
		reflector.SetModelRegistry(modelRegistry)
	}

	return router, planner, reflector
}

// buildOrchestratorConfig creates an OrchestratorConfig from the given config.
// stepLimitFunc is optional; if provided, it will be called when an executor reaches its step limit.
func (a *App) buildOrchestratorConfig(cfg *config.Config, stepLimitFunc agent.StepLimitFunc) core.OrchestratorConfig {
	return core.OrchestratorConfig{
		MaxSteps:      cfg.Executor.MaxReactSteps,
		KeepFirst:     cfg.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:      cfg.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries:    cfg.Executor.MaxRetries,
		StepLimitFunc: stepLimitFunc,
	}
}

// rebuildJudge recreates the ToolJudge from current config and sets it on the registry.
// If the judge cannot be created (disabled, no provider, no model), it keeps the existing judge.
// router is optional; if nil, a new router is built from config.
func (a *App) rebuildJudge(cfg *config.Config, router *llm.Router, logger *slog.Logger) {
	if a.toolRegistry == nil {
		return
	}

	if cfg.Security.Judge.Enabled == nil || !*cfg.Security.Judge.Enabled {
		a.toolRegistry.SetJudge(nil)
		if logger != nil {
			logger.Info("tool judge disabled by configuration")
		}
		return
	}

	_, _, _, defaultModel := cfg.LLM.GetActiveProviderConfig()

	var judgeProvider llm.Provider
	if router != nil {
		judgeProvider = router.GetDefaultProvider()
	} else {
		// Try building a new router from current config
		newRouter, _, err := a.buildRouter(cfg)
		if err == nil && newRouter != nil {
			judgeProvider = newRouter.GetDefaultProvider()
		} else if logger != nil {
			logger.Warn("rebuildJudge: failed to build LLM router for judge", "error", err)
		}
	}

	judge := tools.NewToolJudgeFromConfig(tools.JudgeConfig{
		Enabled:      true,
		Model:        cfg.Security.Judge.Model,
		DefaultModel: defaultModel,
		Provider:     judgeProvider,
	}, logger)

	if judge != nil {
		a.toolRegistry.SetJudge(judge)
		if logger != nil {
			logger.Info("tool judge rebuilt successfully")
		}
	} else if logger != nil {
		logger.Warn("tool judge rebuild failed: keeping existing judge (provider or model unavailable)")
	}
}

// buildContextFactory creates a ContextManagerFactory from the given LLM router and config.
func (a *App) buildContextFactory(llmRouter *llm.Router, cfg *config.Config) core.ContextManagerFactory {
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) core.ContextManager {
		counter, err := llm.NewTokenCounter(modelMeta.TokenizerType)
		if err != nil {
			slog.Warn("token counter fallback", "tokenizer", modelMeta.TokenizerType, "error", err)
			counter = llm.NewSimpleTokenCounter()
		}
		tracker := llm.NewContextTokenTracker(counter)

		strategy := sdkmemory.NewCompactionStrategy(compactionStrategy, sdkmemory.CompactionConfig{
			SlidingWindow: struct{ KeepFirst, KeepLast int }{
				KeepFirst: cfg.Executor.Compaction.SlidingWindow.KeepFirst,
				KeepLast:  cfg.Executor.Compaction.SlidingWindow.KeepLast,
			},
			Summarization: struct{ BlockSize, KeepLast int }{
				BlockSize: cfg.Executor.Compaction.Summarization.BlockSize,
				KeepLast:  5,
			},
			Hierarchical: struct{ DistantRatio, MiddleRatio, RecentRatio float64 }{
				DistantRatio: 0.4,
				MiddleRatio:  0.3,
				RecentRatio:  0.3,
			},
		}, sdkmemory.CompactionDeps{
			TokenCounter: counter,
			Summarize: func(ctx context.Context, blockText string) (string, error) {
				if llmRouter == nil {
					return "", errors.New("compaction summarize: LLM router not available")
				}
				req := llm.ChatRequest{
					Messages: []llm.Message{
						{Role: "system", Content: coreprompts.CompactionSummarize},
						{Role: "user", Content: blockText},
					},
				}
				resp, err := llmRouter.Call(ctx, req)
				if err != nil {
					return "", fmt.Errorf("compaction summarize: %w", err)
				}
				return resp.Message.Content, nil
			},
		})

		thresholds := sdkmemory.CompactionThresholds{
			PredictivePercent: cfg.Executor.Compaction.Thresholds.PredictivePercent,
			WarningPercent:    cfg.Executor.Compaction.Thresholds.WarningPercent,
			EmergencyPercent:  cfg.Executor.Compaction.Thresholds.EmergencyPercent,
		}

		cw := sdkmemory.NewContextWindow(systemPrompt, modelMeta, tracker, thresholds, strategy)
		return core.NewCoreContextManager(cw)
	}
}
