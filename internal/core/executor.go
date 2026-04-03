package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/user/agent/internal/config"
	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

const executorNudge = "[System] You have tools available that can help answer this request. Before finishing, try using relevant tools to discover the answer. Do NOT say you cannot determine something without first attempting to use your tools."

// LLMCaller is the interface Executor needs from the LLM layer.
type LLMCaller interface {
	Call(ctx context.Context, req llm.ChatRequest) (resp *llm.ChatResponse, err error)
}

// ToolExecutor is the interface Executor needs from the tools layer.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (result tools.ToolResult, err error)
}

// CompactionStrategy defines an algorithm for compressing step history.
// This is defined in core to avoid circular imports with memory package.
type CompactionStrategy interface {
	Compact(ctx context.Context, steps []Step, budgetTokens int) []llm.Message
}

// FillCheck represents the result of a context window fill check.
type FillCheck struct {
	Percent float64
	Status  string // "ok", "compact", "warning", "emergency", "reject"
	Used    int
	Max     int
}

// ContextManager is the interface Executor needs for context window management.
type ContextManager interface {
	BuildPrompt() []llm.Message
	AddStep(step Step)
	NeedsCompaction() bool
	Compact(ctx context.Context)
	// SetTask sets the user's task and acceptance criteria into the context window.
	SetTask(task string, criteria []AcceptanceCriterion)
	// SetReflections sets reflections for the retry loop (Phase 3).
	SetReflections(reflections []Reflection)
	// SetStrategy changes the compaction strategy at runtime.
	SetStrategy(strategy CompactionStrategy)
	// CheckFill returns the current fill status of the context window.
	CheckFill() FillCheck
	// CorrectTokenCount updates the token tracker with actual API input tokens.
	CorrectTokenCount(apiInputTokens int)
	// FillPercent returns the current fill percentage.
	FillPercent() float64
	// AvailableTokens returns the number of tokens remaining in the context window.
	AvailableTokens() int
}

// Executor runs the ReAct loop: Thought → Action → Observation.
type Executor struct {
	llm                     LLMCaller
	tools                   ToolExecutor
	tokenCounter            llm.TokenCounter
	maxSteps                int
	logger                  *slog.Logger // structured logger (nil-safe)
	emitter                 Emitter      // event emitter (uses noopEmitter if nil)
	suppressAssistantEvents bool         // if true, don't emit AssistantChunk/AssistantDone
	toolResultBudget        config.ToolResultBudgetConfig
}

// NewExecutor creates a new Executor.
// logger and emitter are optional (nil-safe).
// suppressAssistantEvents should be true for plan-step executors to avoid duplicate events.
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, counter llm.TokenCounter, maxSteps int, logger *slog.Logger, emitter Emitter, suppressAssistantEvents bool, toolResultBudget config.ToolResultBudgetConfig) *Executor {
	// Use noopEmitter if nil to avoid nil checks throughout the code
	if emitter == nil {
		emitter = &noopEmitter{}
	}
	return &Executor{
		llm:                     llmRouter,
		tools:                   toolRegistry,
		tokenCounter:            counter,
		maxSteps:                maxSteps,
		logger:                  logger,
		emitter:                 emitter,
		suppressAssistantEvents: suppressAssistantEvents,
		toolResultBudget:        toolResultBudget,
	}
}

// logInfo logs an INFO level message if logger is not nil.
func (e *Executor) logInfo(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Info(msg, args...)
	}
}

// logDebug logs a DEBUG level message if logger is not nil.
func (e *Executor) logDebug(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Debug(msg, args...)
	}
}

// logWarn logs a WARN level message if logger is not nil.
func (e *Executor) logWarn(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Warn(msg, args...)
	}
}

// applyToolResultBudget truncates a tool result if it exceeds the budget.
// The budget is min(HardCapTokens, AvailableTokens * MaxFillFraction) with a 256-token floor.
// When truncated, a notice is appended to inform the model.
func (e *Executor) applyToolResultBudget(observation string, cw ContextManager) string {
	if e.toolResultBudget.HardCapTokens <= 0 {
		return observation
	}

	// Estimate observation tokens (rough: len/4)
	observationTokens := len(observation) / 4

	// Calculate adaptive cap
	available := cw.AvailableTokens()
	adaptiveCap := int(float64(available) * e.toolResultBudget.MaxFillFraction)
	capTokens := e.toolResultBudget.HardCapTokens
	if adaptiveCap < capTokens {
		capTokens = adaptiveCap
	}
	// Minimum floor to avoid useless truncation
	if capTokens < 256 {
		capTokens = 256
	}

	if observationTokens <= capTokens {
		return observation
	}

	// Truncate to cap (in chars, approx capTokens*4)
	charLimit := capTokens * 4
	if charLimit >= len(observation) {
		return observation
	}

	truncated := observation[:charLimit]
	return truncated + fmt.Sprintf(
		"\n\n[OUTPUT TRUNCATED: showing ~%d of ~%d tokens (%.0f%%). Full output was too large for context window budget.]",
		capTokens, observationTokens, float64(capTokens)/float64(observationTokens)*100,
	)
}

// Run executes the ReAct loop for the given task.
func (e *Executor) Run(ctx context.Context, task TaskDefinition, cw ContextManager) (*ExecutorResult, error) {
	// Build tool definitions from task.Tools
	toolDefs := e.buildToolDefinitions(task.Tools)

	// Track if we have meaningful tools (beyond just finish)
	hasTools := len(task.Tools) > 0

	var allSteps []Step
	nudgeAttempted := false
	reactiveCompactAttempted := false

	for stepNum := 1; stepNum <= e.maxSteps; stepNum++ {
		// Emit step start
		e.emitter.StepStart(stepNum)
		stepStartTime := time.Now()

		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Build messages from context window
		messages := cw.BuildPrompt()

		// Create chat request
		req := llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		}

		// Call LLM
		resp, err := e.llm.Call(ctx, req)
		if err != nil {
			if isContextExceededError(err) && !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				cw.Compact(ctx)
				e.logWarn("reactive_compaction_api_error", "step", stepNum, "error", err)
				continue
			}
			return nil, err
		}

		// Parse response
		thought := resp.Message.Content
		e.logDebug("llm_thought", "step", stepNum, "thought", thought)

		// Emit thought event
		if thought != "" {
			e.emitter.Thought(stepNum, thought)
		}

		// Check for implicit finish (no tool calls with end_turn)
		if len(resp.Message.ToolCalls) == 0 && resp.StopReason == "end_turn" {
			// Nudge mechanism: if this is early in execution and tools are available,
			// give the LLM a second chance to use tools before accepting implicit finish
			if hasTools && !nudgeAttempted {
				nudgeAttempted = true
				// Create a nudge step to encourage tool usage
				nudgeStep := Step{
					Thought:     thought,
					Observation: executorNudge,
					TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.logInfo("executor_nudge", "step", stepNum, "reason", "no_tools_used_on_step_1")
				continue // retry with nudge in context
			}

			step := Step{
				Thought:    thought,
				TokensUsed: resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)

			e.logInfo("executor_step", "step", stepNum, "thought", thought, "action", "implicit_finish", "observation_len", 0)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

			// Emit assistant response events (unless suppressed)
			if !e.suppressAssistantEvents {
				e.emitter.AssistantChunk(thought)
				e.emitter.AssistantDone(thought, resp.Usage.InputTokens, resp.Usage.OutputTokens)
			}

			return &ExecutorResult{
				Output:   thought,
				Steps:    allSteps,
				Finished: true,
			}, nil
		}

		// Take the first tool call
		if len(resp.Message.ToolCalls) == 0 {
			// No tool calls but not end_turn — apply nudge if not attempted
			if hasTools && !nudgeAttempted {
				nudgeAttempted = true
				nudgeStep := Step{
					Thought:     thought,
					Observation: executorNudge,
					TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.logInfo("executor_nudge", "step", stepNum, "reason", "no_tools_no_end_turn_on_step_1")
				continue
			}

			// No tool calls but not end_turn — treat as implicit finish anyway
			step := Step{
				Thought:    thought,
				TokensUsed: resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)

			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

			// Emit assistant response events (unless suppressed)
			if !e.suppressAssistantEvents {
				e.emitter.AssistantChunk(thought)
				e.emitter.AssistantDone(thought, resp.Usage.InputTokens, resp.Usage.OutputTokens)
			}

			return &ExecutorResult{
				Output:   thought,
				Steps:    allSteps,
				Finished: true,
			}, nil
		}

		action := resp.Message.ToolCalls[0]

		// Emit tool call
		argsPreview := string(action.Input)
		if len(argsPreview) > 80 {
			argsPreview = argsPreview[:77] + "..."
		}
		e.emitter.ToolCall(stepNum, action.Name, argsPreview)
		e.logInfo("step_start", "step", stepNum, "tool", action.Name)
		e.logDebug("tool_call_args", "step", stepNum, "tool", action.Name, "args", string(action.Input))

		// Check for finish tool
		if action.Name == "finish" {
			// Parse answer from input
			var params struct {
				Answer string `json:"answer"`
			}
			if err := json.Unmarshal(action.Input, &params); err != nil {
				params.Answer = string(action.Input) // fallback
			}

			step := Step{
				Thought:    thought,
				Action:     action,
				TokensUsed: resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)

			e.logInfo("step_complete", "step", stepNum, "tool", action.Name, "observation_len", 0)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

			return &ExecutorResult{
				Output:   params.Answer,
				Steps:    allSteps,
				Finished: true,
			}, nil
		}

		// Enrich context with task description for the judge
		ctx = tools.WithTaskContext(ctx, task.Task)

		// Execute the tool
		result, err := e.tools.Execute(ctx, action.Name, action.Input)
		if err != nil {
			// Infrastructure error
			return nil, err
		}

		observation := result.Content
		// Ensure non-empty observation for tool messages (OpenAI API requirement)
		if observation == "" {
			observation = "(no output)"
		}

		// Apply tool result budget
		observation = e.applyToolResultBudget(observation, cw)

		// Emit tool result
		resultPreview := observation
		if len(resultPreview) > 2000 {
			resultPreview = resultPreview[:2000] + "..."
		}
		e.emitter.ToolResult(stepNum, len(observation), resultPreview)
		observationPreview := observation
		if len(observationPreview) > 500 {
			observationPreview = observationPreview[:500] + "..."
		}
		e.logDebug("tool_observation", "step", stepNum, "tool", action.Name, "observation", observationPreview)

		// Create step
		step := Step{
			Thought:     thought,
			Action:      action,
			Observation: observation,
			TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
		allSteps = append(allSteps, step)

		// Add step to context window
		cw.AddStep(step)

		e.logInfo("step_complete", "step", stepNum, "tool", action.Name, "observation_len", len(observation))
		e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

		// Correct token count with actual API usage
		if resp.Usage.InputTokens > 0 {
			cw.CorrectTokenCount(resp.Usage.InputTokens)
		}

		// Check for compaction using threshold-based logic
		fill := cw.CheckFill()

		// Emit context fill status
		e.emitter.ContextFill(fill.Percent, fill.Used, fill.Max, fill.Status)

		switch fill.Status {
		case "compact", "warning":
			cw.Compact(ctx)
			e.logDebug("compaction", "step", stepNum, "action", "context_compacted", "fill_percent", fill.Percent)
			reactiveCompactAttempted = false
		case "emergency":
			cw.Compact(ctx)
			e.logWarn("emergency_compaction", "step", stepNum, "fill_percent", fill.Percent)
			reactiveCompactAttempted = false
		case "reject":
			if !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				cw.Compact(ctx)
				e.logWarn("reactive_compaction_reject", "step", stepNum, "fill_percent", fill.Percent)
				continue
			}
			return nil, fmt.Errorf("context window full after reactive compaction (%.1f%% of %d tokens)", fill.Percent, fill.Max)
		default:
			// Reset the flag on successful step completion so future steps can attempt reactive compaction
			reactiveCompactAttempted = false
		}
	}

	// Max steps reached without finish
	return &ExecutorResult{
		Output:   "",
		Steps:    allSteps,
		Finished: false,
	}, nil
}

// buildToolDefinitions converts ToolDescriptors to LLM ToolDefinitions.
func (e *Executor) buildToolDefinitions(taskTools []tools.ToolDescriptor) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(taskTools)+1)

	// Track if finish tool is already present
	hasFinish := false

	// Add task tools
	for _, t := range taskTools {
		defs = append(defs, llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
		if t.Name == "finish" {
			hasFinish = true
		}
	}

	// Add the finish tool only if not already present
	if !hasFinish {
		finishTool := NewFinishTool()
		defs = append(defs, llm.ToolDefinition{
			Name:        finishTool.Name(),
			Description: finishTool.Description(),
			InputSchema: finishTool.InputSchema(),
		})
	}

	return defs
}
