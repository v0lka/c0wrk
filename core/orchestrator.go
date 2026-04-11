// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
)

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxSteps   int
	KeepFirst  int // for sliding window compaction
	KeepLast   int // for sliding window compaction
	MaxRetries int // max retry attempts after failed evaluation (default: 3)
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager

// BlackboardFactory creates a Blackboard for a new task.
// The taskID is a unique identifier for the orchestration request.
type BlackboardFactory func(taskID string) Blackboard

// Orchestrator coordinates the agent's reasoning cycle.
// It handles c0wrk-specific concerns (routing, PersistentBlackboard,
// conversation history) and delegates the Plan&Execute loop to the SDK engine.
type Orchestrator struct {
	engine              *orchestration.Orchestrator // SDK P&E engine
	router              *Router
	llm                 LLMCaller
	toolRegistry        *tools.ToolRegistry
	config              OrchestratorConfig
	contextFactory      ContextManagerFactory
	logger              *slog.Logger
	emitter             Emitter
	modelRegistry       *llm.ModelRegistry
	bbFactory           BlackboardFactory
	conversationHistory []llm.Message
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(
	router *Router,
	planner *Planner,
	llmCaller LLMCaller,
	toolExec ToolExecutor,
	toolReg *tools.ToolRegistry,
	counter llm.TokenCounter,
	cfg OrchestratorConfig,
	contextFactory ContextManagerFactory,
	reflector *Reflector, // optional, nil-safe
	logger *slog.Logger, // optional, nil-safe
	emitter Emitter, // optional, uses noopEmitter if nil
	modelRegistry *llm.ModelRegistry, // optional, nil-safe
	toolResultBudget ToolResultBudget,
	bbFactory BlackboardFactory, // optional, nil = default MapBlackboard
) *Orchestrator {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 30
	}
	if cfg.KeepFirst == 0 {
		cfg.KeepFirst = 3
	}
	if cfg.KeepLast == 0 {
		cfg.KeepLast = 10
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2 // default: 3 total attempts
	}
	if emitter == nil {
		emitter = &noopEmitter{}
	}

	// Build SDK orchestration config
	sdkCfg := orchestration.Config{
		Planner:       planner,
		LLM:           llmCaller,
		Tools:         toolExec,
		ToolRegistry:  toolReg,
		TokenCounter:  counter,
		ModelRegistry: modelRegistry,
		ContextFactory: func(sys string, meta llm.ModelMetadata, compact string) agent.ContextManager {
			return contextFactory(sys, meta, compact)
		},
		ContextSetup: func(cm agent.ContextManager, taskDesc string) {
			if ccm, ok := cm.(interface{ SetTask(string) }); ok {
				ccm.SetTask(taskDesc)
			}
		},
		Events:           &emitterEventsAdapter{emitter},
		SystemPrompt:     buildSystemPrompt,
		MaxRetries:       cfg.MaxRetries,
		MaxSteps:         cfg.MaxSteps,
		ToolResultBudget: toolResultBudget,
		StepConfigurator: coreStepConfigurator(cfg),
	}

	// Configure state factory to use core's blackboard factory.
	// If the factory produces a PersistentBlackboard, wire the emitter so
	// persistence warnings are surfaced to the user instead of being silently logged.
	if bbFactory != nil {
		capturedEmitter := emitter // capture for closure
		sdkCfg.StateFactory = func(taskID string) orchestration.Blackboard {
			bb := bbFactory(taskID)
			if pbb, ok := bb.(*PersistentBlackboard); ok {
				pbb.SetEmitter(capturedEmitter)
			}
			return bb
		}
	}

	// Wire optional strategies
	if reflector != nil {
		sdkCfg.Reflection = reflector
	}

	engine := orchestration.New(sdkCfg)

	return &Orchestrator{
		engine:         engine,
		router:         router,
		llm:            llmCaller,
		toolRegistry:   toolReg,
		config:         cfg,
		contextFactory: contextFactory,
		logger:         logger,
		emitter:        emitter,
		modelRegistry:  modelRegistry,
		bbFactory:      bbFactory,
	}
}

// coreStepConfigurator resolves AgentProfile from PlanStep.Profile.
func coreStepConfigurator(cfg OrchestratorConfig) orchestration.StepConfigurator {
	return func(step orchestration.PlanStep, defaults orchestration.StepDefaults) orchestration.StepConfig {
		profile := resolveAgentProfile(step, cfg.MaxSteps)
		var allowed []tools.ToolDescriptor
		if len(profile.AllowedTools) > 0 {
			allowSet := make(map[string]bool, len(profile.AllowedTools))
			for _, name := range profile.AllowedTools {
				allowSet[name] = true
			}
			for _, t := range defaults.AllTools {
				if allowSet[t.Name] {
					allowed = append(allowed, t)
				}
			}
		}
		return orchestration.StepConfig{
			MaxSteps:           profile.MaxSteps,
			AllowedTools:       allowed,
			SystemPrompt:       profile.SystemPrompt,
			CompactionStrategy: applyCompactionStrategy(profile.Domain, 3),
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

// logInfo logs an INFO level message if logger is not nil.
func (o *Orchestrator) logInfo(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Info(msg, args...)
	}
}

// logDebug logs a DEBUG level message if logger is not nil.
func (o *Orchestrator) logDebug(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Debug(msg, args...)
	}
}

// Handle executes the agent reasoning cycle for the given user message.
// This is the main entry point. It handles routing and delegates the
// Plan&Execute loop to the SDK engine.
func (o *Orchestrator) Handle(ctx context.Context, userMessage string) (*HandleResult, error) {
	// 0. Emit initial 0% context_fill so the frontend has a baseline before any LLM call.
	o.logDebug("orchestrator: handle started", "messageLength", len(userMessage))
	o.emitInitialContextFill()

	// 1. Get available tools
	availableTools := o.toolRegistry.List()

	// 2. Route the request
	o.logDebug("orchestrator: starting routing")
	o.emitter.ServiceWithMeta("Routing request...", map[string]any{"phase": "orchestration"})
	routing, err := o.router.Route(ctx, userMessage, availableTools, o.conversationHistory)
	if err != nil {
		o.logDebug("orchestrator: routing failed", "error", err)
		return nil, fmt.Errorf("routing failed: %w", err)
	}

	// Emit routing decision
	o.logDebug("orchestrator: routing completed", "domain", routing.Domain, "complexity", routing.Complexity, "needsClarification", routing.NeedsClarification)
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))
	o.logInfo("routing_decision", "domain", routing.Domain, "complexity", routing.Complexity)

	// 3. Handle clarification
	if routing.NeedsClarification {
		o.logDebug("orchestrator: returning clarification request")
		taskID := uuid.New().String()
		var bb Blackboard
		if o.bbFactory != nil {
			bb = o.bbFactory(taskID)
		} else {
			bb = NewMapBlackboard()
		}
		bb.SetOriginalRequest(userMessage)
		return &HandleResult{
			Output:          "I need more information to help you. Could you please clarify your request?",
			RoutingDecision: routing,
			Blackboard:      bb,
		}, nil
	}

	// 4. Delegate to SDK engine
	o.logDebug("orchestrator: invoking SDK engine")
	execResult, err := o.engine.Execute(ctx, userMessage)
	if err != nil {
		o.logDebug("orchestrator: SDK engine returned error", "error", err)
		return nil, err
	}
	o.logDebug("orchestrator: SDK engine completed", "attemptCount", execResult.AttemptCount)

	// 5. Persist routing decision on PersistentBlackboard (post-execution)
	o.logDebug("orchestrator: persisting routing decision")
	if pbb, ok := execResult.Blackboard.(*PersistentBlackboard); ok {
		pbb.SetRouting(routing)
		pbb.CompleteTask(execResult.AttemptCount)
	}

	// 6. Build HandleResult
	result := &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Plan:            execResult.Plan,
		Blackboard:      execResult.Blackboard,
		AttemptCount:    execResult.AttemptCount,
		Reflections:     execResult.Reflections,
	}

	o.logDebug("orchestrator: handle completed", "attemptCount", result.AttemptCount)

	// 9. Accumulate conversation history for future routing context
	o.conversationHistory = append(o.conversationHistory,
		llm.Message{Role: "user", Content: userMessage},
		llm.Message{Role: "assistant", Content: result.Output},
	)
	const maxHistoryMessages = 20
	if len(o.conversationHistory) > maxHistoryMessages {
		o.conversationHistory = o.conversationHistory[len(o.conversationHistory)-maxHistoryMessages:]
	}

	return result, nil
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
func (o *Orchestrator) Resume(ctx context.Context, bb Blackboard, routing *RoutingDecision) (*HandleResult, error) {
	o.logDebug("orchestrator: resume started")
	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(*PersistentBlackboard); ok {
		pbb.SetEmitter(o.emitter)
	}
	plan := bb.GetPlan()
	if plan == nil {
		o.logDebug("orchestrator: resume failed, no plan in blackboard")
		return nil, errors.New("no plan found in restored blackboard")
	}

	// Emit initial context_fill
	o.emitInitialContextFill()

	// Emit routing decision so the frontend can display the resumed context.
	o.emitter.Routing("plan_execute", routing.Domain, strconv.Itoa(routing.Complexity))

	o.logInfo("resume_task", "plan_steps", len(plan.Steps), "reflections", len(bb.GetReflections()))

	// Delegate to SDK engine
	o.logDebug("orchestrator: invoking SDK engine for resume", "planSteps", len(plan.Steps))
	execResult, err := o.engine.Resume(ctx, bb)
	if err != nil {
		o.logDebug("orchestrator: SDK engine resume returned error", "error", err)
		if pbb, ok := bb.(*PersistentBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	o.logDebug("orchestrator: SDK engine resume completed", "attemptCount", execResult.AttemptCount)

	result := &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Plan:            execResult.Plan,
		Blackboard:      execResult.Blackboard,
		AttemptCount:    execResult.AttemptCount,
		Reflections:     execResult.Reflections,
	}

	// Persist task completion if using persistent blackboard.
	if pbb, ok := bb.(*PersistentBlackboard); ok {
		pbb.CompleteTask(execResult.AttemptCount)
	}

	o.logDebug("orchestrator: resume completed", "attemptCount", result.AttemptCount)
	return result, nil
}

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) emitInitialContextFill() {
	var effectiveMax int
	if o.modelRegistry != nil {
		meta, _ := o.modelRegistry.Resolve("")
		if meta.ContextWindow > 0 {
			safetyMargin := meta.ContextWindow * 5 / 100
			effectiveMax = meta.ContextWindow - meta.OutputLimit - safetyMargin
		}
	}
	o.emitter.ContextFill(0, 0, effectiveMax, "ok", "")
}

// buildSystemPrompt creates the system prompt for executors.
func buildSystemPrompt(ctx context.Context, userMessage string) string {
	// Build workspace context string
	var workspaceCtxStr string
	if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
		workspaceCtxStr = "## Workspace\nYour session workspace is: " + wsPath + "\nAll artifacts you create (files, directories, temporary files) MUST be placed strictly inside this workspace directory, unless the task explicitly requires creating artifacts at a specific external location."
	}

	// Apply template substitutions
	result := prompts.OrchestratorSystem
	result = strings.ReplaceAll(result, "WORKSPACE-CONTEXT", workspaceCtxStr)

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// Run is a backwards-compatible method that calls Handle.
// Kept for compatibility with Phase 1 code.
func (o *Orchestrator) Run(ctx context.Context, userMessage string) (*HandleResult, error) {
	return o.Handle(ctx, userMessage)
}

// RunSubAgent is a backward-compatible wrapper around agent.RunSubAgent.
// It accepts a TaskDefinition (c0wrk-specific) and extracts tools/description for the SDK call.
func RunSubAgent(ctx context.Context, stepID string, executor *agent.Executor, cm ContextManager, task TaskDefinition, emitter Emitter) <-chan SubAgentResult {
	return agent.RunSubAgent(ctx, stepID, executor, cm, task.Tools, task.Task, emitter)
}
