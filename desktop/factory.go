package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	// SDK layer
	"github.com/user/agent/sdk/llm"
	sdkmemory "github.com/user/agent/sdk/memory"

	// Core layer
	"github.com/user/agent/core"
	coreprompts "github.com/user/agent/core/prompts"
	"github.com/user/agent/core/tools"

	// Backend layer
	"github.com/user/agent/backend/config"
)

// buildLLMRouter creates a new LLMRouter and ModelRegistry from the given config.
// This is extracted from Startup() so it can be called per-session in the factory closure.
func (a *App) buildLLMRouter(cfg *config.Config) (*llm.LLMRouter, *llm.ModelRegistry, error) {
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
	initialBackoff, _ := time.ParseDuration(cfg.LLM.Retry.InitialBackoff)
	maxBackoff, _ := time.ParseDuration(cfg.LLM.Retry.MaxBackoff)
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
	llmRouter, err := llm.NewLLMRouter(context.Background(), routerCfg, modelRegistry)
	if err != nil {
		return nil, nil, err
	}
	return llmRouter, modelRegistry, nil
}

// buildCoreAgents creates the Phase 2 components (router, acExtractor, planner, evaluator, reflector)
// from the given LLM router and config. Returns nil values if llmRouter is nil.
// When emitter is non-nil, each component receives a token-tracking wrapper so
// that service-level LLM calls are accumulated in session totals.
func (a *App) buildCoreAgents(llmRouter *llm.LLMRouter, registry *tools.ToolRegistry, cfg *config.Config, emitter core.Emitter, logger *slog.Logger) (*core.Router, *core.ACExtractor, *core.Planner, *core.Evaluator, *core.Reflector) {
	if llmRouter == nil {
		return nil, nil, nil, nil, nil
	}
	caller := core.NewTokenTrackingCaller(llmRouter, emitter)
	router := core.NewRouter(caller, cfg.Router.HistoryWindow)
	acExtractor := core.NewACExtractor(caller)
	planner := core.NewPlanner(caller)
	evaluatorCounter, _ := llm.NewTokenCounter("approximate") // "approximate" always succeeds
	evaluator := core.NewEvaluator(
		registry,                              // ToolExecutor (for programmatic/bash_exec)
		caller,                                // LLMCaller
		registry.ToolRegistry,                 // *sdktools.ToolRegistry (for tool filtering)
		evaluatorCounter,                      // llm.TokenCounter
		a.buildContextFactory(llmRouter, cfg), // ContextManagerFactory
		logger,                                // *slog.Logger
		emitter,                               // Emitter
		core.ToolResultBudget{
			HardCapTokens:   cfg.Executor.ToolResultBudget.HardCapTokens,
			MaxFillFraction: cfg.Executor.ToolResultBudget.MaxFillFraction,
		},
	)
	reflector := core.NewReflector(caller)
	return router, acExtractor, planner, evaluator, reflector
}

// buildOrchestratorConfig creates an OrchestratorConfig from the given config.
func (a *App) buildOrchestratorConfig(cfg *config.Config) core.OrchestratorConfig {
	return core.OrchestratorConfig{
		MaxSteps:   cfg.Executor.MaxReactSteps,
		KeepFirst:  cfg.Executor.Compaction.SlidingWindow.KeepFirst,
		KeepLast:   cfg.Executor.Compaction.SlidingWindow.KeepLast,
		MaxRetries: cfg.Executor.MaxRetries,
	}
}

// buildContextFactory creates a ContextManagerFactory from the given LLM router and config.
func (a *App) buildContextFactory(llmRouter *llm.LLMRouter, cfg *config.Config) core.ContextManagerFactory {
	return func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) core.ContextManager {
		counter, err := llm.NewTokenCounter(modelMeta.TokenizerType)
		if err != nil {
			slog.Warn("token counter fallback", "tokenizer", modelMeta.TokenizerType, "error", err)
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
