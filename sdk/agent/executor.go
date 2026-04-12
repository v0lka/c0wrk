package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

const executorNudge = "[System] You have tools available that can help answer this request. Before finishing, try using relevant tools to discover the answer. Do NOT say you cannot determine something without first attempting to use your tools."

const executorWrapUpNudge = "[System] You are running low on tool call iterations. You have %d iteration(s) remaining. Wrap up your work NOW: summarize your findings and finish. Do not start new explorations."

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

	repeatErrorNudgeMessage = "[System] The previous call to this tool with " +
		"identical arguments returned an error. Retrying the same call will produce the " +
		"same error. You must try a different approach: use different arguments, a " +
		"different tool, or call finish if the task cannot be completed."

	// truncationAbortThreshold is the number of consecutive truncated responses
	// before aborting the executor loop.
	truncationAbortThreshold = 3

	truncationMessage = "[System] Your tool call to '%s' was NOT executed because your output " +
		"was cut off by the model's maximum output token limit. The tool call arguments are " +
		"incomplete/truncated. You MUST use a different approach that produces smaller output — " +
		"for example, break large file writes into multiple smaller operations, use file_ops read_file " +
		"with line ranges instead of reading entire files, or reduce the content size."

	// parseErrorAbortThreshold is the number of consecutive parse errors on the
	// same tool before aborting.
	parseErrorAbortThreshold = 3

	parseErrorNudgeMessage = "[System] This tool has now failed to parse input %d times in a row. " +
		"The arguments you are generating are malformed. Try a completely different approach: " +
		"reduce the size of your arguments, use a different tool, or break the operation into " +
		"smaller steps."
)

// Executor runs the ReAct loop: Thought → Action → Observation.
type Executor struct {
	llm                     LLMCaller
	tools                   ToolExecutor
	tokenCounter            llm.TokenCounter
	maxSteps                int
	emitter                 AgentEvents // event emitter (uses NoopEvents if nil)
	suppressAssistantEvents bool        // if true, don't emit AssistantChunk/AssistantDone
	toolResultBudget        ToolResultBudget
	stepLimitFunc           StepLimitFunc // callback when step limit is reached

	// Circuit breaker: detect repeated identical tool calls
	consecutiveRepeatCount int
	lastToolKey            string // "name:" + compactJSON(input) for dedup
	lastToolResultIsError  bool   // whether the last identical tool call returned an error

	// Truncation tracker: detect consecutive max_tokens responses with tool calls
	consecutiveTruncationCount int

	// Parse error tracker: detect consecutive parse failures on the same tool
	consecutiveParseErrorTool  string
	consecutiveParseErrorCount int

	// Plan-step context for structured logging
	planStepID    string // e.g. "step_3" (empty if not plan mode)
	planStepIndex int    // 1-based position in plan (0 if not plan mode)
	planStepTotal int    // total steps in plan (0 if not plan mode)
}

// NewExecutor creates a new Executor.
// emitter is optional (nil-safe).
// suppressAssistantEvents disables AssistantChunk/AssistantDone events; set to true for plan-step
// executors to avoid duplicate assistant messages when the orchestrator handles final output.
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, counter llm.TokenCounter, maxSteps int, emitter AgentEvents, suppressAssistantEvents bool, toolResultBudget ToolResultBudget) *Executor {
	// Use NoopEvents if nil to avoid nil checks throughout the code
	if emitter == nil {
		emitter = &NoopEvents{}
	}
	return &Executor{
		llm:                     llmRouter,
		tools:                   toolRegistry,
		tokenCounter:            counter,
		maxSteps:                maxSteps,
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

// SetStepLimitFunc sets the callback invoked when the executor reaches its step limit.
func (e *Executor) SetStepLimitFunc(fn StepLimitFunc) {
	e.stepLimitFunc = fn
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
	wrapUpNudgeAttempted := false
	reactiveCompactAttempted := false
	unlimitedSteps := false

	for stepNum := 1; unlimitedSteps || stepNum <= e.maxSteps+1; stepNum++ {
		// At the boundary: when we've just exceeded maxSteps
		if !unlimitedSteps && stepNum > e.maxSteps {
			if e.stepLimitFunc == nil {
				break // backward compat: no callback means silent exit
			}
			resp, err := e.stepLimitFunc(ctx, stepNum, e.maxSteps)
			if err != nil {
				// Treat callback errors as deny - exit cleanly without propagating the error
				return &ExecutorResult{ //nolint:nilerr // intentional: callback error means stop, not fatal
					Output:   "",
					Steps:    allSteps,
					Finished: false,
				}, nil
			}
			switch resp {
			case StepLimitAllowOnce:
				e.maxSteps++ // allow exactly one more
				// Inject nudge for LLM
				nudgeStep := Step{
					UserNudge: "[System] The user granted you exactly ONE additional tool call iteration. " +
						"Use it wisely to wrap up your work. The user may deny further extensions.",
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
			case StepLimitAllowAlways:
				unlimitedSteps = true
				// Inject nudge for LLM
				nudgeStep := Step{
					UserNudge: "[System] The user granted you unlimited tool call iterations for this step. " +
						"You have the freedom to make as many tool calls as needed to complete your work.",
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
			case StepLimitDeny:
				break // will fall through to the post-loop return
			default:
				break
			}
			// If deny, we need to actually exit the loop
			if resp == StepLimitDeny || resp == "" {
				break
			}
		}

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
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: cw.OutputLimit(),
		}

		// Call LLM
		resp, err := e.llm.Call(ctx, req)
		if err != nil {
			if isContextExceededError(err) && !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				cw.Compact(ctx)
				e.emitter.ExecutorDiagnostic(stepNum, "reactive_compaction_api_error", map[string]any{"error": err.Error()})
				continue
			}
			return nil, err
		}

		if resp == nil {
			return nil, fmt.Errorf("LLM returned empty response at step %d", stepNum)
		}

		// Parse response
		thought := resp.Message.Content

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
				e.emitter.ExecutorDiagnostic(stepNum, "executor_nudge", map[string]any{"reason": "no_tools_used_on_step_1"})
				continue // retry with nudge in context
			}

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
				e.emitter.ExecutorDiagnostic(stepNum, "executor_nudge", map[string]any{"reason": "no_tools_no_end_turn_on_step_1"})
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

		// --- Truncation detection: max_tokens with tool calls ---
		if resp.StopReason == "max_tokens" && len(resp.Message.ToolCalls) > 0 {
			truncAction := resp.Message.ToolCalls[0]
			e.emitter.ToolCall(stepNum, truncAction.Name, string(truncAction.Input))

			e.consecutiveTruncationCount++
			if e.consecutiveTruncationCount >= truncationAbortThreshold {
				e.emitter.ExecutorDiagnostic(stepNum, "truncation_abort", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})
				return &ExecutorResult{
					Output:   fmt.Sprintf("Aborted: tool '%s' output was truncated %d times consecutively by max output token limit", truncAction.Name, e.consecutiveTruncationCount),
					Steps:    allSteps,
					Finished: false,
				}, nil
			}

			truncObs := fmt.Sprintf(truncationMessage, truncAction.Name)
			e.emitter.ExecutorDiagnostic(stepNum, "truncation_detected", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})

			step := Step{
				Thought:     thought,
				Action:      truncAction,
				Observation: truncObs,
				TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)
			cw.AddStep(step)
			e.emitter.ToolResult(stepNum, len(truncObs), truncObs)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
			continue
		}

		// Reset truncation counter on any non-truncated response
		e.consecutiveTruncationCount = 0

		action := resp.Message.ToolCalls[0]

		// Emit tool call
		e.emitter.ToolCall(stepNum, action.Name, string(action.Input))

		// --- Circuit breaker: detect repeated identical tool calls ---
		toolKey := action.Name + ":" + compactJSON(action.Input)
		if toolKey == e.lastToolKey {
			e.consecutiveRepeatCount++
		} else {
			e.consecutiveRepeatCount = 1
			e.lastToolKey = toolKey
			e.lastToolResultIsError = false
		}

		// Use lower thresholds when the previous identical call produced an error
		nudgeThreshold := repeatNudgeThreshold
		abortThreshold := repeatAbortThreshold
		if e.lastToolResultIsError {
			nudgeThreshold = 2
			abortThreshold = 3
		}

		if e.consecutiveRepeatCount >= abortThreshold {
			e.emitter.ExecutorDiagnostic(stepNum, "repeated_tool_call_abort", map[string]any{"tool": action.Name, "repeat_count": e.consecutiveRepeatCount})
			return &ExecutorResult{
				Output:   fmt.Sprintf("Aborted: tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount),
				Steps:    allSteps,
				Finished: false,
			}, nil
		}

		if e.consecutiveRepeatCount >= nudgeThreshold {
			nudgeMsg := repeatNudgeMessage
			if e.lastToolResultIsError {
				nudgeMsg = repeatErrorNudgeMessage
			}
			e.emitter.ExecutorDiagnostic(stepNum, "repeated_tool_call_nudge", map[string]any{"tool": action.Name, "repeat_count": e.consecutiveRepeatCount})
			step := Step{
				Thought:     thought,
				Action:      action,
				Observation: nudgeMsg,
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
		e.lastToolResultIsError = result.IsError
		// Ensure non-empty observation for tool messages (OpenAI API requirement)
		if observation == "" {
			observation = "(no output)"
		}

		// --- Parse error tracker ---
		if result.IsError && isParseError(observation) {
			if action.Name == e.consecutiveParseErrorTool {
				e.consecutiveParseErrorCount++
			} else {
				e.consecutiveParseErrorTool = action.Name
				e.consecutiveParseErrorCount = 1
			}

			if e.consecutiveParseErrorCount >= parseErrorAbortThreshold {
				e.emitter.ExecutorDiagnostic(stepNum, "parse_error_abort", map[string]any{"tool": action.Name, "consecutive_parse_errors": e.consecutiveParseErrorCount})
				return &ExecutorResult{
					Output:   fmt.Sprintf("Aborted: tool '%s' failed to parse input %d times consecutively", action.Name, e.consecutiveParseErrorCount),
					Steps:    allSteps,
					Finished: false,
				}, nil
			}

			observation += "\n\n" + fmt.Sprintf(parseErrorNudgeMessage, e.consecutiveParseErrorCount)
		} else if !result.IsError {
			// Reset parse error tracker on successful execution
			e.consecutiveParseErrorTool = ""
			e.consecutiveParseErrorCount = 0
		}
		// --- End parse error tracker ---

		// Apply tool result budget
		observation = e.applyToolResultBudget(observation, cw)

		// Emit tool result
		e.emitter.ToolResult(stepNum, len(observation), observation)

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

		e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

		// Wrap-up nudge: warn LLM when approaching budget limit
		// Only applies when the budget is large enough for the nudge to be meaningful.
		if e.maxSteps > 3 && stepNum >= e.maxSteps-3 && !wrapUpNudgeAttempted {
			wrapUpNudgeAttempted = true
			wrapUpMsg := fmt.Sprintf(executorWrapUpNudge, e.maxSteps-stepNum)
			wrapUpStep := Step{
				Thought:     "",
				Observation: wrapUpMsg,
				TokensUsed:  0,
			}
			allSteps = append(allSteps, wrapUpStep)
			cw.AddStep(wrapUpStep)
			e.emitter.ExecutorDiagnostic(stepNum, "executor_wrapup_nudge", map[string]any{"remaining": e.maxSteps - stepNum})
		}

		// Correct token count with actual API usage
		if resp.Usage.InputTokens > 0 {
			cw.CorrectTokenCount(resp.Usage.InputTokens)
		}

		// Check for compaction using threshold-based logic
		fill := cw.CheckFill()

		// Emit context fill status
		e.emitter.ContextFill(fill.Percent, fill.Used, fill.Max, fill.Status, e.planStepID)

		switch fill.Status {
		case "compact", "warning":
			cw.Compact(ctx)
			reactiveCompactAttempted = false
		case "emergency":
			cw.Compact(ctx)
			reactiveCompactAttempted = false
		case "reject":
			if !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				cw.Compact(ctx)
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

// compactJSON normalizes JSON by removing insignificant whitespace.
// This ensures semantically identical JSON strings produce the same output
// regardless of formatting differences from different LLM responses.
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw) // fallback to raw if malformed
	}
	return buf.String()
}

// isParseError checks if a tool result content indicates a JSON parse failure.
func isParseError(content string) bool {
	return strings.Contains(content, "failed to parse input")
}

// isContextExceededError checks if an error indicates the context window was exceeded.
// This can happen when our token estimation is inaccurate and the API rejects the request.
func isContextExceededError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrContextWindowExceeded) {
		return true
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
