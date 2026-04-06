package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

const executorNudge = "[System] You have tools available that can help answer this request. Before finishing, try using relevant tools to discover the answer. Do NOT say you cannot determine something without first attempting to use your tools."

const (
	// repeatNudgeThreshold is the number of consecutive identical tool calls
	// before injecting a system message to try a different approach.
	repeatNudgeThreshold = 3
	// repeatAbortThreshold is the number of consecutive identical tool calls
	// before aborting the executor loop.
	repeatAbortThreshold = 4

	repeatNudgeMessage = "[System] You have called the same tool with the same arguments " +
		"multiple times in a row and it keeps failing. Try a different approach: " +
		"use different arguments, a different tool, or call finish if the task cannot be completed."
)

// Executor runs the ReAct loop: Thought → Action → Observation.
type Executor struct {
	llm                     LLMCaller
	tools                   ToolExecutor
	tokenCounter            llm.TokenCounter
	maxSteps                int
	logger                  *slog.Logger // structured logger (nil-safe)
	emitter                 AgentEvents  // event emitter (uses NoopEvents if nil)
	suppressAssistantEvents bool         // if true, don't emit AssistantChunk/AssistantDone
	toolResultBudget        ToolResultBudget

	// Circuit breaker: detect repeated identical tool calls
	consecutiveRepeatCount int
	lastToolKey            string // "name:" + string(input) for dedup

	// Plan-step context for structured logging
	planStepID    string // e.g. "step_3" (empty if not plan mode)
	planStepIndex int    // 1-based position in plan (0 if not plan mode)
	planStepTotal int    // total steps in plan (0 if not plan mode)
}

// NewExecutor creates a new Executor.
// logger and emitter are optional (nil-safe).
// suppressAssistantEvents disables AssistantChunk/AssistantDone events; set to true for plan-step
// executors to avoid duplicate assistant messages when the orchestrator handles final output.
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, counter llm.TokenCounter, maxSteps int, logger *slog.Logger, emitter AgentEvents, suppressAssistantEvents bool, toolResultBudget ToolResultBudget) *Executor {
	// Use NoopEvents if nil to avoid nil checks throughout the code
	if emitter == nil {
		emitter = &NoopEvents{}
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

// SetPlanContext sets plan-step metadata for structured logging.
// Call this before Run() when the executor is handling a plan step.
func (e *Executor) SetPlanContext(stepID string, index, total int) {
	e.planStepID = stepID
	e.planStepIndex = index
	e.planStepTotal = total
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

// Run executes the ReAct loop for the given task tools and context manager.
// The caller is responsible for setting the task context (via tools.WithTaskContext)
// before calling Run.
func (e *Executor) Run(ctx context.Context, taskTools []tools.ToolDescriptor, cw ContextManager) (*ExecutorResult, error) {
	// Build tool definitions from taskTools
	toolDefs := e.buildToolDefinitions(taskTools)

	// Track if we have meaningful tools (beyond just finish)
	hasTools := len(taskTools) > 0

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

		if resp == nil {
			return nil, fmt.Errorf("LLM returned empty response at step %d", stepNum)
		}

		// Parse response
		thought := resp.Message.Content
		e.logDebug("llm_thought", "step", stepNum, "thought", thought)

		// Always accumulate tokens regardless of suppressAssistantEvents
		e.emitter.TokensUsed(resp.Usage.InputTokens, resp.Usage.OutputTokens)

		// Emit thought event
		if thought != "" || resp.Reasoning != "" {
			e.emitter.Thought(stepNum, thought, resp.Reasoning)
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
		e.emitter.ToolCall(stepNum, action.Name, string(action.Input))
		if e.planStepID != "" {
			e.logInfo("step_start", "step", stepNum, "tool", action.Name, "plan_step", e.planStepID, "plan_pos", fmt.Sprintf("%d/%d", e.planStepIndex, e.planStepTotal))
		} else {
			e.logInfo("step_start", "step", stepNum, "tool", action.Name)
		}
		e.logDebug("tool_call_args", "step", stepNum, "tool", action.Name, "args", string(action.Input))

		// --- Circuit breaker: detect repeated identical tool calls ---
		toolKey := action.Name + ":" + string(action.Input)
		if toolKey == e.lastToolKey {
			e.consecutiveRepeatCount++
		} else {
			e.consecutiveRepeatCount = 1
			e.lastToolKey = toolKey
		}

		if e.consecutiveRepeatCount >= repeatAbortThreshold {
			e.logWarn("repeated_tool_call_abort", "step", stepNum, "tool", action.Name, "repeat_count", e.consecutiveRepeatCount)
			return &ExecutorResult{
				Output:   fmt.Sprintf("Aborted: tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount),
				Steps:    allSteps,
				Finished: false,
			}, nil
		}

		if e.consecutiveRepeatCount >= repeatNudgeThreshold {
			e.logWarn("repeated_tool_call_nudge", "step", stepNum, "tool", action.Name, "repeat_count", e.consecutiveRepeatCount)
			step := Step{
				Thought:     thought,
				Action:      action,
				Observation: repeatNudgeMessage,
				TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)
			cw.AddStep(step)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
			continue
		}
		// --- End circuit breaker ---

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
			e.emitter.ToolResult(stepNum, len(params.Answer), params.Answer)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

			return &ExecutorResult{
				Output:   params.Answer,
				Steps:    allSteps,
				Finished: true,
			}, nil
		}

		// Execute the tool (task context should already be set by the caller)
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
		e.emitter.ToolResult(stepNum, len(observation), observation)
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

		if e.planStepID != "" {
			e.logInfo("step_complete", "step", stepNum, "tool", action.Name, "observation_len", len(observation), "plan_step", e.planStepID, "plan_pos", fmt.Sprintf("%d/%d", e.planStepIndex, e.planStepTotal))
		} else {
			e.logInfo("step_complete", "step", stepNum, "tool", action.Name, "observation_len", len(observation))
		}
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

// isContextExceededError checks if an error indicates the context window was exceeded.
// This can happen when our token estimation is inaccurate and the API rejects the request.
func isContextExceededError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	patterns := []string{
		"context length exceeded",
		"maximum context length",
		"context_length_exceeded",
		"too many tokens",
		"request too large",
		"input is too long",
		"prompt is too long",
	}
	for _, p := range patterns {
		if strings.Contains(errStr, p) {
			return true
		}
	}
	return false
}
