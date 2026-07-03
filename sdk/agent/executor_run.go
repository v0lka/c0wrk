package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// loopAction is a typed enum so helpers can signal control flow back to the main loop.
type loopAction int

const (
	actionNone     loopAction = iota // no special action
	actionContinue                   // continue outer loop (skip to next stepNum)
	actionBreak                      // break outer loop (exit Run)
	actionReturn                     // return result
)

// batchIndexBase is the base multiplier for batch sub-call indices.
// Multiplied by callIdx in processBatchTool to create unique emitter indices
// that cannot collide with standalone tool call indices (which are sequential, 0..N-1).
const batchIndexBase = 10000

// runState holds all loop-local state for a single Run invocation.
type runState struct {
	stepNum                   int
	allSteps                  []Step
	nudgeAttempted            bool
	wrapUpNudgeAttempted      bool
	reactiveCompactAttempted  bool
	preCompactionNudgeEmitted bool
	unlimitedSteps            bool
	effectiveMaxSteps         int
	finishResult              *ExecutorResult
	stepStartTime             time.Time
	circuitBreakerTriggered   bool
	responseGroup             int64
}

// handleStepLimitBoundary handles the step-limit boundary logic (when stepNum > effectiveMaxSteps).
func (e *Executor) handleStepLimitBoundary(ctx context.Context, state *runState, cw ContextManager) loopAction {
	if state.unlimitedSteps || state.stepNum <= state.effectiveMaxSteps {
		return actionNone
	}
	// At the boundary: when we've just exceeded effectiveMaxSteps
	resp, err := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, "")
	if err != nil {
		// Treat callback errors as deny - exit cleanly without propagating the error
		state.finishResult = &ExecutorResult{
			Output:   "",
			Steps:    state.allSteps,
			Finished: false,
		}
		return actionReturn
	}
	switch resp {
	case StepLimitAllowOnce:
		state.effectiveMaxSteps++ // allow exactly one more
		// Inject nudge for LLM
		nudgeStep := Step{
			UserNudge: "[System] The user granted you exactly ONE additional tool call iteration. " +
				"Use it wisely to wrap up your work. The user may deny further extensions.",
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
	case StepLimitAllowAlways:
		state.unlimitedSteps = true
		// Inject nudge for LLM
		nudgeStep := Step{
			UserNudge: "[System] The user granted you unlimited tool call iterations for this step. " +
				"You have the freedom to make as many tool calls as needed to complete your work.",
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
	case StepLimitDeny:
		return actionBreak
	default:
		return actionBreak
	}
	return actionNone
}

// callLLMWithReactiveCompaction calls the LLM and handles reactive compaction on context-exceeded errors.
func (e *Executor) callLLMWithReactiveCompaction(ctx context.Context, state *runState, cw ContextManager, toolDefs []llm.ToolDefinition) (*llm.ChatResponse, loopAction, error) {
	// Build messages from context window
	messages := cw.BuildPrompt()

	// Create chat request
	req := llm.ChatRequest{
		Messages:        messages,
		Tools:           toolDefs,
		MaxTokens:       cw.OutputLimit(),
		ReasoningEffort: e.reasoningEffort,
	}

	// Call LLM
	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		if isContextExceededError(err) && !state.reactiveCompactAttempted {
			state.reactiveCompactAttempted = true
			if result := cw.Compact(ctx); result != nil {
				e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
			}
			e.emitter.ExecutorDiagnostic(state.stepNum, "reactive_compaction_api_error", map[string]any{"error": err.Error()})
			return nil, actionContinue, nil
		}
		return nil, actionNone, err
	}

	if resp == nil {
		return nil, actionNone, fmt.Errorf("llm returned empty response at step %d", state.stepNum)
	}

	return resp, actionNone, nil
}

// hasMutatingToolExecuted checks whether any mutating tool (write_file,
// edit_file, create_directory, delete_file, delete_directory) was
// successfully executed during this step. It scans the accumulated steps
// for Action.Name in the mutatingTools set, ignoring steps that carry
// an error observation (rejected calls, circuit-breaker intercepts).
func (e *Executor) hasMutatingToolExecuted(state *runState) bool {
	for _, s := range state.allSteps {
		if s.Action.Name == "" {
			continue
		}
		if _, ok := mutatingTools[s.Action.Name]; ok {
			// A step with an Observation starting with "[Tool call rejected"
			// was denied by HITL — don't count it as a mutation.
			if strings.HasPrefix(s.Observation, "[Tool call rejected") {
				continue
			}
			return true
		}
	}
	return false
}

// handleImplicitFinish handles the "no tool calls" branches: nudge → finish-nudge → implicit finish.
func (e *Executor) handleImplicitFinish(resp *llm.ChatResponse, thought string, state *runState, cw ContextManager, hasTools bool) (*ExecutorResult, loopAction) {
	// Check for implicit finish (no tool calls with end_turn)
	if resp.StopReason == "end_turn" {
		// Nudge mechanism: if this is early in execution and tools are available,
		// give the LLM a second chance to use tools before accepting implicit finish
		if hasTools && !state.nudgeAttempted {
			state.nudgeAttempted = true
			// Create a nudge step to encourage tool usage
			nudgeStep := Step{
				Thought:     thought,
				Observation: executorNudge,
				TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "executor_nudge", map[string]any{"reason": "no_tools_used_on_step_1"})
			return nil, actionContinue // retry with nudge in context
		}

		// Finish nudge: require explicit finish tool call before accepting completion
		// Only needed in plan-step execution where output needs structured capture
		if e.suppressAssistantEvents && !e.finishNudgeAttempted {
			e.finishNudgeAttempted = true
			nudgeStep := Step{
				Thought:     thought,
				Observation: executorFinishNudge,
				TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "executor_finish_nudge", map[string]any{"reason": "implicit_finish_without_tool"})
			return nil, actionContinue // retry — LLM should now call finish explicitly
		}

		step := Step{
			Thought:          thought,
			ReasoningContent: resp.Message.ReasoningContent,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
		state.allSteps = append(state.allSteps, step)

		e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))

		// Emit assistant response events (unless suppressed)
		if !e.suppressAssistantEvents {
			e.emitter.AssistantChunk(thought)
			e.emitter.AssistantDone(thought, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		}

		return &ExecutorResult{
			Output:   thought,
			Steps:    state.allSteps,
			Finished: true,
		}, actionNone
	}

	// No tool calls but not end_turn — apply nudge if not attempted
	if hasTools && !state.nudgeAttempted {
		state.nudgeAttempted = true
		nudgeStep := Step{
			Thought:     thought,
			Observation: executorNudge,
			TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		e.emitter.ExecutorDiagnostic(state.stepNum, "executor_nudge", map[string]any{"reason": "no_tools_no_end_turn_on_step_1"})
		return nil, actionContinue
	}

	// Finish nudge: require explicit finish tool call before accepting completion
	// Only needed in plan-step execution where output needs structured capture
	if e.suppressAssistantEvents && !e.finishNudgeAttempted {
		e.finishNudgeAttempted = true
		nudgeStep := Step{
			Thought:     thought,
			Observation: executorFinishNudge,
			TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		e.emitter.ExecutorDiagnostic(state.stepNum, "executor_finish_nudge", map[string]any{"reason": "implicit_finish_without_tool"})
		return nil, actionContinue // retry — LLM should now call finish explicitly
	}

	// No tool calls but not end_turn — treat as implicit finish anyway
	step := Step{
		Thought:          thought,
		ReasoningContent: resp.Message.ReasoningContent,
		TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}
	state.allSteps = append(state.allSteps, step)

	e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))

	// Emit assistant response events (unless suppressed)
	if !e.suppressAssistantEvents {
		e.emitter.AssistantChunk(thought)
		e.emitter.AssistantDone(thought, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}

	return &ExecutorResult{
		Output:   thought,
		Steps:    state.allSteps,
		Finished: true,
	}, actionNone
}

// handleTruncationStopReason handles the max_tokens stop reason with tool calls.
func (e *Executor) handleTruncationStopReason(ctx context.Context, resp *llm.ChatResponse, thought string, state *runState, cw ContextManager) (*ExecutorResult, loopAction) {
	truncAction := resp.Message.ToolCalls[0]
	e.emitter.ToolCall(state.stepNum, 0, truncAction.Name, string(truncAction.Input), e.tools.GetToolSource(truncAction.Name))

	e.consecutiveTruncationCount++
	if e.consecutiveTruncationCount >= e.circuitBreaker.TruncationAbortThreshold {
		e.emitter.ExecutorDiagnostic(state.stepNum, "truncation_abort", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})
		abortReason := fmt.Sprintf("Tool '%s' output was truncated %d times consecutively by max output token limit", truncAction.Name, e.consecutiveTruncationCount)
		slResp, slErr := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, abortReason)
		if slErr == nil {
			switch slResp {
			case StepLimitAllowOnce:
				e.consecutiveTruncationCount = 0
				nudgeStep := Step{
					UserNudge: "[System] The user acknowledged the truncation circuit breaker and granted you ONE more chance. " +
						"You MUST use smaller operations to avoid hitting the output token limit.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))
				return nil, actionContinue
			case StepLimitAllowAlways:
				e.consecutiveTruncationCount = 0
				e.circuitBreaker.TruncationAbortThreshold = 1 << 30 // disable
				nudgeStep := Step{
					UserNudge: "[System] The user has overridden the truncation circuit breaker. " +
						"You may continue, but try to produce smaller outputs.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))
				return nil, actionContinue
			default:
				// StepLimitDeny or empty — fall through to abort
			}
		}
		return &ExecutorResult{
			Output:   fmt.Sprintf("Aborted: tool '%s' output was truncated %d times consecutively by max output token limit", truncAction.Name, e.consecutiveTruncationCount),
			Steps:    state.allSteps,
			Finished: false,
		}, actionNone
	}

	truncObs := fmt.Sprintf(truncationMessage, truncAction.Name)
	e.emitter.ExecutorDiagnostic(state.stepNum, "truncation_detected", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})

	step := Step{
		Thought:          thought,
		ReasoningContent: resp.Message.ReasoningContent,
		Action:           truncAction,
		Observation:      truncObs,
		TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}
	state.allSteps = append(state.allSteps, step)
	cw.AddStep(step)
	e.emitter.ToolResult(state.stepNum, 0, len(truncObs), truncObs, false)
	e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))
	return nil, actionContinue
}

// processToolCalls processes the entire tool call loop including all circuit breakers.
func (e *Executor) processToolCalls(ctx context.Context, resp *llm.ChatResponse, thought string, state *runState, cw ContextManager) (*ExecutorResult, loopAction, error) {
	toolCalls := resp.Message.ToolCalls

	// Generate ResponseGroup ID for multi-call responses
	var responseGroup int64
	if len(toolCalls) > 1 {
		e.responseGroupCounter++
		responseGroup = e.responseGroupCounter
	}
	state.responseGroup = responseGroup

	state.finishResult = nil
	state.circuitBreakerTriggered = false

	for callIdx, action := range toolCalls {
		result, act, err := e.processSingleToolCall(ctx, action, callIdx, toolCalls, resp, thought, state, cw)
		if err != nil {
			return nil, actionNone, err
		}
		if result != nil {
			return result, actionNone, nil
		}
		if act == actionBreak {
			break
		}
	} // end tool call loop

	// If finish was encountered, return
	if state.finishResult != nil {
		e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))
		return state.finishResult, actionNone, nil
	}

	return nil, actionNone, nil
}

// processSingleToolCall handles a single tool call within the tool call loop.
// It returns:
//   - (*ExecutorResult, actionNone, nil): caller should return the result immediately
//   - (nil, actionBreak, nil): caller should break the loop
//   - (nil, actionNone, nil): caller should continue to the next iteration
//   - (nil, actionNone, err): caller should propagate the error
func (e *Executor) processSingleToolCall(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	toolCalls []llm.ToolCall,
	resp *llm.ChatResponse,
	thought string,
	state *runState,
	cw ContextManager,
) (*ExecutorResult, loopAction, error) {
	responseGroup := state.responseGroup

	// --- Batch meta-tool: execute sub-calls sequentially ---
	// Must be FIRST — batch is intercepted before ToolCall emission so no
	// phantom "batch" tool card appears in the frontend.
	if action.Name == tools.ToolBatch {
		return e.processBatchTool(ctx, action, callIdx, toolCalls, resp, thought, state, cw)
	}
	// --- End batch handling ---

	// Check for finish tool (also before ToolCall emission).
	if action.Name == "finish" {
		// Parse answer from input
		var params struct {
			Answer string `json:"answer"`
		}
		if err := json.Unmarshal(action.Input, &params); err != nil {
			params.Answer = string(action.Input) // fallback
		}

		// Emit finishing event so the frontend can show "Finishing..." status
		// instead of "Running tool: finish".
		e.emitter.Finishing(state.stepNum, params.Answer)

		// Mutation gate: if this step requires mutations, check whether any
		// mutating tool was successfully executed. If not, inject a nudge on
		// the first attempt and reject finish. On the second attempt, accept
		// finish but mark the step as not finished (Finished: false) so the
		// orchestrator triggers reflection/replan instead of recording success.
		if e.mutationRequired && !e.hasMutatingToolExecuted(state) && !e.mutationNudgeAttempted {
			e.mutationNudgeAttempted = true
			nudgeStep := Step{
				Thought:     thought,
				Observation: executorMutationNudge,
				TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "mutation_gate_nudge", map[string]any{
				"reason": "finish_without_mutation",
			})
			return nil, actionBreak, nil // retry — LLM should now make changes or justify
		}

		stepThought := ""
		stepReasoning := ""
		if callIdx == 0 {
			stepThought = thought
			stepReasoning = resp.Message.ReasoningContent
		}
		step := Step{
			Thought:          stepThought,
			ReasoningContent: stepReasoning,
			Action:           action,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup:    responseGroup,
		}
		state.allSteps = append(state.allSteps, step)

		e.emitter.ToolResult(state.stepNum, callIdx, len(params.Answer), params.Answer, false)

		// If mutation gate was triggered (nudge attempted) but still no mutation,
		// mark as not finished so the orchestrator treats this as a failure.
		finished := true
		if e.mutationRequired && !e.hasMutatingToolExecuted(state) {
			finished = false
			e.emitter.ExecutorDiagnostic(state.stepNum, "mutation_gate_rejected", map[string]any{
				"reason": "finish_without_mutation_after_nudge",
			})
		}

		state.finishResult = &ExecutorResult{
			Output:   params.Answer,
			Steps:    state.allSteps,
			Finished: finished,
		}
		return nil, actionBreak, nil // stop processing further tool calls
	}

	// Emit tool call (AFTER batch/finish checks — meta-tools handle their own events).
	toolDisplayName := action.Name
	// For tool_result_read: display as "original_tool (cached)" in chat UI
	if action.Name == "tool_result_read" && e.toolCache != nil {
		var trParams struct {
			Hash string `json:"hash"`
		}
		if json.Unmarshal(action.Input, &trParams) == nil && trParams.Hash != "" {
			if entry, ok := e.toolCache.Get(trParams.Hash); ok {
				toolDisplayName = entry.ToolName + " (cached)"
			}
		}
	}
	e.emitter.ToolCall(state.stepNum, callIdx, toolDisplayName, string(action.Input), e.tools.GetToolSource(action.Name))

	// --- Circuit breaker: detect repeated identical tool calls ---
	// Placed AFTER batch/finish checks so meta-tools aren't subject to
	// repeat-detection — processBatchTool handles per-sub-call circuit breakers.
	if loopAct, execResult, err := e.checkRepeatIdenticalTool(ctx, action, callIdx, thought, resp, state, cw); loopAct != actionNone || execResult != nil {
		return execResult, loopAct, err
	}
	// --- End circuit breaker ---

	// --- HITL: allow consumer to intercept/modify tool calls ---
	input := action.Input
	if decision, decErr := e.hitl.OnToolCall(ctx, action.Name, input); decErr != nil {
		return nil, actionNone, fmt.Errorf("HITL handler error for tool %q: %w", action.Name, decErr)
	} else if decision != nil && !decision.Allow {
		reason := decision.Reason
		if reason == "" {
			reason = "rejected by user"
		}
		obs := fmt.Sprintf("[Tool call rejected: %s]", reason)
		e.emitter.ToolResult(state.stepNum, callIdx, len(obs), obs, true)
		step := Step{
			Thought:       thought,
			Action:        action,
			Observation:   obs,
			TokensUsed:    resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup: responseGroup,
		}
		state.allSteps = append(state.allSteps, step)
		cw.AddStep(step)
		state.circuitBreakerTriggered = true
		return nil, actionNone, nil
	} else if decision != nil && len(decision.ModifiedInput) > 0 {
		input = decision.ModifiedInput
	}

	// Execute the tool (task context should already be set by the caller)
	// Inject tool result cache into context so tool_result_read can access it.
	// Also inject per-tool truncation config for num_lines enforcement.
	execCtx := ctx
	if e.toolCache != nil {
		execCtx = WithToolResultCache(ctx, e.toolCache)
		execCtx = WithPerToolTruncation(execCtx, e.perToolTruncation)
	}
	result, err := e.tools.Execute(execCtx, action.Name, input)
	if err != nil {
		// Infrastructure error
		return nil, actionNone, err
	}

	observation := result.Content
	e.lastToolResultIsError = result.IsError

	// Determine if the tool output is from an untrusted external source
	// for prompt injection defense wrapping in BuildPrompt().
	isUntrusted := e.tools.IsToolUntrusted(action.Name)

	// --- Fruitless result detector: consecutive minimal-result calls ---
	if loopAct, execResult, err := e.checkFruitlessResult(ctx, action, callIdx, observation, result.IsError, state, cw); loopAct != actionNone || execResult != nil {
		return execResult, loopAct, err
	}
	// --- End fruitless result detector ---

	// --- Same-tool repetition detector: same tool, varied args, similar results ---
	if loopAct, execResult, err := e.checkSameToolRepetition(ctx, action, callIdx, observation, result, state, cw); loopAct != actionNone || execResult != nil {
		return execResult, loopAct, err
	}
	// --- End same-tool repetition detector ---

	// Ensure non-empty observation for tool messages (OpenAI API requirement)
	if observation == "" {
		observation = "(no output)"
	}

	// --- Parse error tracker ---
	var parseAction loopAction
	var parseResult *ExecutorResult
	observation, parseAction, parseResult, err = e.checkParseErrors(ctx, action, callIdx, observation, result, state, cw)
	if err != nil {
		return nil, actionNone, err
	}
	if parseAction != actionNone {
		return parseResult, parseAction, nil
	}
	if parseResult != nil {
		return parseResult, actionNone, nil
	}
	// --- End parse error tracker ---

	// Stage 1 + 2: truncation, caching, token budget (shared helper).
	observation = e.processToolResult(execCtx, observation, result.Content, action.Name, action.Input, cw)

	// Emit tool result
	e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, result.IsError)

	// Pre-compaction nudge: warn LLM when context pressure enters danger zone.
	// NOTE: The nudge is appended AFTER ToolResult emission intentionally — it is
	// only for LLM context (stored in Step.Observation), not for frontend display.
	// Only on the last tool call in the response, so the nudge appears once at the end.
	if callIdx == len(toolCalls)-1 && e.preWarningPercent > 0 && !state.preCompactionNudgeEmitted {
		fill := cw.CheckFill()
		if fill.Status == "ok" && fill.Percent >= float64(e.preWarningPercent) {
			if vulnerable := cw.VulnerableOutputs(); len(vulnerable) > 0 {
				observation += "\n\n" + formatPreCompactionNudge(fill.Percent, vulnerable)
				state.preCompactionNudgeEmitted = true
				e.emitter.ExecutorDiagnostic(state.stepNum, "pre_compaction_nudge", map[string]any{
					"fill_percent":     fill.Percent,
					"vulnerable_count": len(vulnerable),
				})
			}
		}
	}

	// Create step - only first tool call in the group carries the Thought
	stepThought := ""
	stepReasoning := ""
	if callIdx == 0 {
		stepThought = thought
		stepReasoning = resp.Message.ReasoningContent
	}
	step := Step{
		Thought:          stepThought,
		ReasoningContent: stepReasoning,
		Action:           action,
		Observation:      observation,
		IsUntrusted:      isUntrusted,
		TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
		ResponseGroup:    responseGroup,
	}
	state.allSteps = append(state.allSteps, step)

	// Add step to context window
	cw.AddStep(step)

	return nil, actionNone, nil
}

// processBatchTool handles the batch meta-tool by executing each sub-call
// sequentially through the full tool execution pipeline.
func (e *Executor) processBatchTool(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	toolCalls []llm.ToolCall,
	resp *llm.ChatResponse,
	thought string,
	state *runState,
	cw ContextManager,
) (*ExecutorResult, loopAction, error) {
	responseGroup := state.responseGroup

	// Parse batch input.
	var batchInput struct {
		Calls []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(action.Input, &batchInput); err != nil {
		obs := fmt.Sprintf("batch parse error: %v", err)
		e.emitter.ToolCall(state.stepNum, callIdx, action.Name, string(action.Input), e.tools.GetToolSource(action.Name))
		e.emitter.ToolResult(state.stepNum, callIdx, len(obs), obs, true)
		step := Step{
			Thought:          thought,
			ReasoningContent: resp.Message.ReasoningContent,
			Action:           action,
			Observation:      obs,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup:    responseGroup,
		}
		state.allSteps = append(state.allSteps, step)
		cw.AddStep(step)
		return nil, actionNone, nil
	}

	// Empty calls array — emit a result instead of silently doing nothing.
	if len(batchInput.Calls) == 0 {
		obs := "batch: no calls provided (empty calls array)"
		e.emitter.ToolCall(state.stepNum, callIdx, action.Name, string(action.Input), e.tools.GetToolSource(action.Name))
		e.emitter.ToolResult(state.stepNum, callIdx, len(obs), obs, true)
		step := Step{
			Thought:          thought,
			ReasoningContent: resp.Message.ReasoningContent,
			Action:           action,
			Observation:      obs,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup:    responseGroup,
		}
		state.allSteps = append(state.allSteps, step)
		cw.AddStep(step)
		return nil, actionNone, nil
	}

	// Use unique index space for batch sub-calls to avoid collisions
	// with standalone tool call indices in the emitter's localToolIDs map.
	baseIdx := callIdx * batchIndexBase

	for subIdx, sub := range batchInput.Calls {
		effectiveIdx := baseIdx + subIdx
		subCall := llm.ToolCall{
			ID:    fmt.Sprintf("batch_sub_%d", subIdx),
			Name:  sub.Tool,
			Input: sub.Input,
		}

		// Explicit nested-batch guard — produce a clear error rather than
		// letting the registry return an implementation-internal message.
		if subCall.Name == tools.ToolBatch {
			obs := "error: batch cannot be nested inside another batch call"
			batchedName := "batch (batched)"
			e.emitter.ToolCall(state.stepNum, effectiveIdx, batchedName, string(sub.Input), e.tools.GetToolSource(sub.Tool))
			e.emitter.ToolResult(state.stepNum, effectiveIdx, len(obs), obs, true)
			step := Step{
				Thought:       thought,
				Action:        subCall,
				Observation:   obs,
				IsUntrusted:   false,
				TokensUsed:    resp.Usage.InputTokens + resp.Usage.OutputTokens,
				ResponseGroup: responseGroup,
			}
			state.allSteps = append(state.allSteps, step)
			cw.AddStep(step)
			continue
		}

		// Emit tool call with "(batched)" suffix.
		batchedName := sub.Tool + " (batched)"
		e.emitter.ToolCall(state.stepNum, effectiveIdx, batchedName, string(sub.Input), e.tools.GetToolSource(sub.Tool))

		// Circuit breaker: repeat identical tool call.
		if loopAct, execResult, err := e.checkRepeatIdenticalTool(ctx, subCall, effectiveIdx, thought, resp, state, cw); loopAct != actionNone || execResult != nil {
			return execResult, loopAct, err
		}

		// Execute via full policy pipeline.
		execCtx := ctx
		if e.toolCache != nil {
			execCtx = WithToolResultCache(ctx, e.toolCache)
			execCtx = WithPerToolTruncation(execCtx, e.perToolTruncation)
		}

		// --- HITL: allow consumer to intercept/modify batch sub-calls ---
		var result tools.ToolResult
		var execErr error
		subInput := subCall.Input
		if decision, decErr := e.hitl.OnToolCall(ctx, subCall.Name, subInput); decErr != nil {
			result = tools.ToolResult{Content: fmt.Sprintf("HITL handler error for tool %q: %v", subCall.Name, decErr), IsError: true}
		} else if decision != nil {
			if !decision.Allow {
				reason := decision.Reason
				if reason == "" {
					reason = "rejected by user"
				}
				obs := fmt.Sprintf("[Tool call rejected: %s]", reason)
				e.emitter.ToolResult(state.stepNum, effectiveIdx, len(obs), obs, true)
				step := Step{
					Thought:       thought,
					Action:        subCall,
					Observation:   obs,
					IsUntrusted:   false,
					TokensUsed:    resp.Usage.InputTokens + resp.Usage.OutputTokens,
					ResponseGroup: responseGroup,
				}
				state.allSteps = append(state.allSteps, step)
				cw.AddStep(step)
				state.circuitBreakerTriggered = true
				continue
			}
			if len(decision.ModifiedInput) > 0 {
				subInput = decision.ModifiedInput
			}
		}
		if !result.IsError && result.Content == "" {
			result, execErr = e.tools.Execute(execCtx, subCall.Name, subInput)
		}
		if execErr != nil {
			// Infrastructure error — capture as error result, continue.
			result = tools.ToolResult{Content: fmt.Sprintf("error executing %q: %v", subCall.Name, execErr), IsError: true}
		}

		observation := result.Content
		e.lastToolResultIsError = result.IsError

		// Fruitless result detector.
		if loopAct, execResult, err := e.checkFruitlessResult(ctx, subCall, effectiveIdx, observation, result.IsError, state, cw); loopAct != actionNone || execResult != nil {
			return execResult, loopAct, err
		}

		// Same-tool repetition detector.
		if loopAct, execResult, err := e.checkSameToolRepetition(ctx, subCall, effectiveIdx, observation, result, state, cw); loopAct != actionNone || execResult != nil {
			return execResult, loopAct, err
		}

		// Ensure non-empty observation (OpenAI API requirement).
		if observation == "" {
			observation = "(no output)"
		}

		isUntrusted := e.tools.IsToolUntrusted(subCall.Name)

		// Stage 1 + 2: truncation, caching, token budget (shared helper).
		observation = e.processToolResult(execCtx, observation, result.Content, subCall.Name, subCall.Input, cw)

		// Emit tool result.
		e.emitter.ToolResult(state.stepNum, effectiveIdx, len(observation), observation, result.IsError)

		// Pre-compaction nudge for last sub-call (only for LLM context, after emission).
		if subIdx == len(batchInput.Calls)-1 && callIdx == len(toolCalls)-1 && e.preWarningPercent > 0 && !state.preCompactionNudgeEmitted {
			fill := cw.CheckFill()
			if fill.Status == "ok" && fill.Percent >= float64(e.preWarningPercent) {
				if vulnerable := cw.VulnerableOutputs(); len(vulnerable) > 0 {
					observation += "\n\n" + formatPreCompactionNudge(fill.Percent, vulnerable)
					state.preCompactionNudgeEmitted = true
					e.emitter.ExecutorDiagnostic(state.stepNum, "pre_compaction_nudge", map[string]any{
						"fill_percent":     fill.Percent,
						"vulnerable_count": len(vulnerable),
					})
				}
			}
		}

		// Create step — only first sub-call in the first response group call carries thought.
		stepThought := ""
		stepReasoning := ""
		if subIdx == 0 && callIdx == 0 {
			stepThought = thought
			stepReasoning = resp.Message.ReasoningContent
		}
		step := Step{
			Thought:          stepThought,
			ReasoningContent: stepReasoning,
			Action:           subCall,
			Observation:      observation,
			IsUntrusted:      isUntrusted,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup:    responseGroup,
		}
		state.allSteps = append(state.allSteps, step)
		cw.AddStep(step)
	}

	return nil, actionNone, nil
}

// processToolResult applies Stage 1 (per-tool truncation + caching +
// fragmentation nudge) and Stage 2 (token-budget truncation preserving
// the Stage 1 nudge). Shared by processSingleToolCall and processBatchTool
// to avoid duplicated pipeline logic.
func (e *Executor) processToolResult(
	execCtx context.Context,
	observation string,
	fullResult string,
	toolName string,
	input json.RawMessage,
	cw ContextManager,
) string {
	// --- Stage 1: Per-tool truncation + optional caching ---
	truncated, wasTruncated := e.applyPerToolTruncation(observation, toolName)
	if wasTruncated {
		observation = truncated
	}

	var cacheHash string
	if e.toolCache != nil {
		if _, isNonCacheable := nonCacheableTools[toolName]; !isNonCacheable {
			meta := e.buildCacheMeta(execCtx, toolName, input)
			cacheHash = e.toolCache.Store(toolName, fullResult, meta)

			if wasTruncated {
				maxSliceHint := 0
				if e.perToolTruncation != nil {
					if cfg, ok := e.perToolTruncation[toolName]; ok {
						maxSliceHint = cfg.MaxLines
					}
				}
				nudge := formatFragmentationNudge(cacheHash, toolName, maxSliceHint)
				observation += nudge
			}
		}
	}

	// --- Stage 2: Token-budget truncation (preserve Stage 1 nudge) ---
	const stage1NudgePrefix = "\n\n[This output was truncated to"
	var stage1Nudge string
	if idx := strings.Index(observation, stage1NudgePrefix); idx >= 0 {
		stage1Nudge = observation[idx:]
		observation = observation[:idx]
	}
	// Suppress the Stage‑2 hash hint when Stage‑1 already appended a
	// fragmentation nudge containing the cache hash — avoids redundant
	// "Use tool_result_read…" instructions.
	budgetHash := cacheHash
	if stage1Nudge != "" {
		budgetHash = ""
	}
	observation = e.applyToolResultBudget(observation, cw, toolName, budgetHash)
	observation += stage1Nudge

	return observation
}

// checkRepeatIdenticalTool detects repeated identical tool calls and applies
// the repeat circuit breaker: nudge the LLM or abort the step based on thresholds.
func (e *Executor) checkRepeatIdenticalTool(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	thought string,
	resp *llm.ChatResponse,
	state *runState,
	cw ContextManager,
) (loopAction, *ExecutorResult, error) {
	responseGroup := state.responseGroup
	toolKey := action.Name + ":" + compactJSON(action.Input)
	if toolKey == e.lastToolKey {
		e.consecutiveRepeatCount++
	} else {
		e.consecutiveRepeatCount = 1
		e.lastToolKey = toolKey
		e.lastToolResultIsError = false
	}

	// Use lower thresholds when the previous identical call produced an error.
	// Guard against zero-value (disabled) thresholds to prevent integer underflow.
	nudgeThreshold := e.circuitBreaker.RepeatNudgeThreshold
	abortThreshold := e.circuitBreaker.RepeatAbortThreshold
	if e.lastToolResultIsError {
		if e.circuitBreaker.RepeatNudgeThreshold > 0 {
			nudgeThreshold = e.circuitBreaker.RepeatNudgeThreshold - 1
		}
		if e.circuitBreaker.RepeatAbortThreshold > 0 {
			abortThreshold = e.circuitBreaker.RepeatAbortThreshold - 1
		}
	}

	if e.consecutiveRepeatCount >= abortThreshold {
		e.emitter.ExecutorDiagnostic(state.stepNum, "repeated_tool_call_abort", map[string]any{"tool": action.Name, "repeat_count": e.consecutiveRepeatCount})
		abortReason := fmt.Sprintf("Tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount)
		slResp, slErr := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, abortReason)
		if slErr == nil {
			switch slResp {
			case StepLimitAllowOnce:
				e.consecutiveRepeatCount = 0
				nudgeStep := Step{
					UserNudge: "[System] The user acknowledged the circuit breaker and granted you ONE more chance. " +
						"You MUST change your approach immediately — do NOT repeat the same tool call.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				state.circuitBreakerTriggered = true
			case StepLimitAllowAlways:
				e.consecutiveRepeatCount = 0
				e.circuitBreaker.RepeatAbortThreshold = 1 << 30 // disable
				nudgeStep := Step{
					UserNudge: "[System] The user has overridden the circuit breaker. You may continue, " +
						"but try to vary your approach to avoid repeating the same failing pattern.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				state.circuitBreakerTriggered = true
			default:
				// StepLimitDeny or empty — fall through to abort
			}
		}
		if state.circuitBreakerTriggered {
			return actionBreak, nil, nil
		}
		abortMsg := fmt.Sprintf("Aborted: tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount)
		e.emitter.ToolResult(state.stepNum, callIdx, len(abortMsg), abortMsg, true)
		return actionNone, &ExecutorResult{
			Output:   abortMsg,
			Steps:    state.allSteps,
			Finished: false,
		}, nil
	}

	if e.consecutiveRepeatCount >= nudgeThreshold {
		nudgeMsg := repeatNudgeMessage
		if e.lastToolResultIsError {
			nudgeMsg = repeatErrorNudgeMessage
		}
		e.emitter.ExecutorDiagnostic(state.stepNum, "repeated_tool_call_nudge", map[string]any{"tool": action.Name, "repeat_count": e.consecutiveRepeatCount})
		e.emitter.ToolResult(state.stepNum, callIdx, len(nudgeMsg), nudgeMsg, false)
		stepThought := ""
		stepReasoning := ""
		if callIdx == 0 {
			stepThought = thought
			stepReasoning = resp.Message.ReasoningContent
		}
		step := Step{
			Thought:          stepThought,
			ReasoningContent: stepReasoning,
			Action:           action,
			Observation:      nudgeMsg,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			ResponseGroup:    responseGroup,
		}
		state.allSteps = append(state.allSteps, step)
		cw.AddStep(step)
		state.circuitBreakerTriggered = true
		return actionBreak, nil, nil
	}

	return actionNone, nil, nil
}

// checkFruitlessResult detects consecutive tool calls that return empty or
// minimal (non-error) results, then applies the fruitless circuit breaker.
func (e *Executor) checkFruitlessResult(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	observation string,
	isError bool,
	state *runState,
	cw ContextManager,
) (loopAction, *ExecutorResult, error) {
	fruitlessMaxLen := e.circuitBreaker.FruitlessMaxResultLen
	if fruitlessMaxLen == 0 {
		fruitlessMaxLen = 32 // default
	}
	isFruitless := !isError && len(observation) <= fruitlessMaxLen
	if isFruitless {
		e.consecutiveFruitlessCount++
	} else if !isError {
		// Reset on non-fruitless, non-error result
		e.consecutiveFruitlessCount = 0
	}

	// Check fruitless thresholds (skip if threshold is 0 = disabled)
	if e.circuitBreaker.FruitlessAbortThreshold > 0 && e.consecutiveFruitlessCount >= e.circuitBreaker.FruitlessAbortThreshold {
		e.emitter.ExecutorDiagnostic(state.stepNum, "fruitless_abort", map[string]any{"consecutive": e.consecutiveFruitlessCount})
		e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, true)
		abortReason := fmt.Sprintf("%d consecutive tool calls returned empty or minimal results", e.consecutiveFruitlessCount)
		slResp, slErr := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, abortReason)
		if slErr == nil {
			switch slResp {
			case StepLimitAllowOnce:
				e.consecutiveFruitlessCount = 0
				e.fruitlessNudgeAttempted = false
				nudgeStep := Step{
					UserNudge: "[System] The user acknowledged the fruitless-results circuit breaker and granted you ONE more chance. " +
						"Try a fundamentally different approach to find the information you need.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				state.circuitBreakerTriggered = true
			case StepLimitAllowAlways:
				e.consecutiveFruitlessCount = 0
				e.fruitlessNudgeAttempted = false
				e.circuitBreaker.FruitlessAbortThreshold = 0 // disable
				nudgeStep := Step{
					UserNudge: "[System] The user has overridden the fruitless-results circuit breaker. " +
						"You may continue searching, but consider varying your approach.",
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				state.circuitBreakerTriggered = true
			default:
				// StepLimitDeny or empty — fall through to abort
			}
		}
		if state.circuitBreakerTriggered {
			return actionBreak, nil, nil
		}
		return actionNone, &ExecutorResult{
			Output:   fmt.Sprintf("Aborted: %d consecutive tool calls returned empty or minimal results", e.consecutiveFruitlessCount),
			Steps:    state.allSteps,
			Finished: false,
		}, nil
	}

	if e.circuitBreaker.FruitlessNudgeThreshold > 0 && e.consecutiveFruitlessCount >= e.circuitBreaker.FruitlessNudgeThreshold && !e.fruitlessNudgeAttempted {
		e.fruitlessNudgeAttempted = true
		e.emitter.ExecutorDiagnostic(state.stepNum, "fruitless_nudge", map[string]any{"consecutive": e.consecutiveFruitlessCount})
		e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, false)
		nudgeStep := Step{
			Observation: fmt.Sprintf(executorFruitlessNudge, e.consecutiveFruitlessCount),
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		state.circuitBreakerTriggered = true
		return actionBreak, nil, nil
	}

	return actionNone, nil, nil
}

// checkSameToolRepetition detects repetitive calls to the same tool with
// varied arguments but similar-sized results (except store_fact).
func (e *Executor) checkSameToolRepetition(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	observation string,
	result tools.ToolResult,
	state *runState,
	cw ContextManager,
) (loopAction, *ExecutorResult, error) {
	// Skip for store_fact: it's legitimate to store many facts in a row with similar-sized confirmations.
	if action.Name != "store_fact" {
		resultLen := len(result.Content)
		sizeDelta := e.circuitBreaker.SameToolResultSizeDelta
		if sizeDelta == 0 {
			sizeDelta = 64 // default
		}
		// Calculate absolute difference without importing math
		lenDiff := resultLen - e.sameToolLastResultLen
		if lenDiff < 0 {
			lenDiff = -lenDiff
		}

		if action.Name == e.sameToolLastName && lenDiff <= sizeDelta {
			e.sameToolConsecutiveCount++
			e.sameToolLastResultLen = resultLen
		} else {
			e.sameToolConsecutiveCount = 1
			e.sameToolLastName = action.Name
			e.sameToolLastResultLen = resultLen
		}

		// Check same-tool thresholds (skip if threshold is 0 = disabled)
		if e.circuitBreaker.SameToolRepeatAbortThreshold > 0 && e.sameToolConsecutiveCount >= e.circuitBreaker.SameToolRepeatAbortThreshold {
			e.emitter.ExecutorDiagnostic(state.stepNum, "same_tool_repeat_abort", map[string]any{"tool": action.Name, "consecutive": e.sameToolConsecutiveCount})
			e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, true)
			abortReason := fmt.Sprintf("Tool '%s' called %d times in a row with different arguments but similar results", action.Name, e.sameToolConsecutiveCount)
			slResp, slErr := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, abortReason)
			if slErr == nil {
				switch slResp {
				case StepLimitAllowOnce:
					e.sameToolConsecutiveCount = 0
					e.sameToolNudgeAttempted = false
					nudgeStep := Step{
						UserNudge: "[System] The user acknowledged the same-tool circuit breaker and granted you ONE more chance. " +
							"Try a completely different tool or approach instead of repeating the same tool.",
					}
					state.allSteps = append(state.allSteps, nudgeStep)
					cw.AddStep(nudgeStep)
					state.circuitBreakerTriggered = true
				case StepLimitAllowAlways:
					e.sameToolConsecutiveCount = 0
					e.sameToolNudgeAttempted = false
					e.circuitBreaker.SameToolRepeatAbortThreshold = 0 // disable
					nudgeStep := Step{
						UserNudge: "[System] The user has overridden the same-tool circuit breaker. " +
							"You may continue, but consider using different tools or approaches.",
					}
					state.allSteps = append(state.allSteps, nudgeStep)
					cw.AddStep(nudgeStep)
					state.circuitBreakerTriggered = true
				default:
					// StepLimitDeny or empty — fall through to abort
				}
			}
			if state.circuitBreakerTriggered {
				return actionBreak, nil, nil
			}
			return actionNone, &ExecutorResult{
				Output:   fmt.Sprintf("Aborted: tool '%s' called %d times in a row with different arguments but similar results", action.Name, e.sameToolConsecutiveCount),
				Steps:    state.allSteps,
				Finished: false,
			}, nil
		}

		if e.circuitBreaker.SameToolRepeatNudgeThreshold > 0 && e.sameToolConsecutiveCount >= e.circuitBreaker.SameToolRepeatNudgeThreshold && !e.sameToolNudgeAttempted {
			e.sameToolNudgeAttempted = true
			e.emitter.ExecutorDiagnostic(state.stepNum, "same_tool_repeat_nudge", map[string]any{"tool": action.Name, "consecutive": e.sameToolConsecutiveCount})
			e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, false)
			nudgeStep := Step{
				Observation: fmt.Sprintf(executorSameToolRepeatNudge, action.Name, e.sameToolConsecutiveCount),
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			state.circuitBreakerTriggered = true
			return actionBreak, nil, nil
		}
	} else {
		// Reset tracker when store_fact is used so the next non-store_fact tool starts fresh
		e.sameToolConsecutiveCount = 0
		e.sameToolLastName = ""
		e.sameToolLastResultLen = 0
	}

	return actionNone, nil, nil
}

// checkParseErrors detects consecutive parse errors for the same tool and
// applies the parse-error circuit breaker (abort with step limit override, or nudge).
// Returns the potentially-augmented observation string.
func (e *Executor) checkParseErrors(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	observation string,
	result tools.ToolResult,
	state *runState,
	cw ContextManager,
) (string, loopAction, *ExecutorResult, error) {
	if result.IsError && isParseError(observation) {
		if action.Name == e.consecutiveParseErrorTool {
			e.consecutiveParseErrorCount++
		} else {
			e.consecutiveParseErrorTool = action.Name
			e.consecutiveParseErrorCount = 1
		}

		if e.consecutiveParseErrorCount >= e.circuitBreaker.ParseErrorAbortThreshold {
			e.emitter.ExecutorDiagnostic(state.stepNum, "parse_error_abort", map[string]any{"tool": action.Name, "consecutive_parse_errors": e.consecutiveParseErrorCount})
			e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, true)
			abortReason := fmt.Sprintf("Tool '%s' failed to parse input %d times consecutively", action.Name, e.consecutiveParseErrorCount)
			slResp, slErr := e.hitl.OnStepLimit(ctx, state.stepNum, state.effectiveMaxSteps, abortReason)
			if slErr == nil {
				switch slResp {
				case StepLimitAllowOnce:
					e.consecutiveParseErrorCount = 0
					nudgeStep := Step{
						UserNudge: "[System] The user acknowledged the parse-error circuit breaker and granted you ONE more chance. " +
							"You MUST fix your tool call arguments — they are malformed. Try a simpler approach.",
					}
					state.allSteps = append(state.allSteps, nudgeStep)
					cw.AddStep(nudgeStep)
					state.circuitBreakerTriggered = true
				case StepLimitAllowAlways:
					e.consecutiveParseErrorCount = 0
					e.circuitBreaker.ParseErrorAbortThreshold = 1 << 30 // disable
					nudgeStep := Step{
						UserNudge: "[System] The user has overridden the parse-error circuit breaker. " +
							"You may continue, but fix your tool call argument formatting.",
					}
					state.allSteps = append(state.allSteps, nudgeStep)
					cw.AddStep(nudgeStep)
					state.circuitBreakerTriggered = true
				default:
					// StepLimitDeny or empty — fall through to abort
				}
			}
			if state.circuitBreakerTriggered {
				return observation, actionBreak, nil, nil
			}
			return observation, actionNone, &ExecutorResult{
				Output:   fmt.Sprintf("Aborted: tool '%s' failed to parse input %d times consecutively", action.Name, e.consecutiveParseErrorCount),
				Steps:    state.allSteps,
				Finished: false,
			}, nil
		}

		observation += "\n\n" + fmt.Sprintf(parseErrorNudgeMessage, e.consecutiveParseErrorCount)
	} else if !result.IsError {
		// Reset parse error tracker on successful execution
		e.consecutiveParseErrorTool = ""
		e.consecutiveParseErrorCount = 0
	}

	return observation, actionNone, nil, nil
}

// handleWrapUpNudge emits the wrap-up nudge when approaching the budget limit.
func (e *Executor) handleWrapUpNudge(state *runState, cw ContextManager) {
	// Wrap-up nudge: warn LLM when approaching budget limit
	// Only applies when the budget is large enough for the nudge to be meaningful.
	if state.effectiveMaxSteps > 3 && state.stepNum >= state.effectiveMaxSteps-3 && !state.wrapUpNudgeAttempted {
		state.wrapUpNudgeAttempted = true
		wrapUpMsg := fmt.Sprintf(executorWrapUpNudge, state.effectiveMaxSteps-state.stepNum)
		wrapUpStep := Step{
			Thought:     "",
			Observation: wrapUpMsg,
			TokensUsed:  0,
		}
		state.allSteps = append(state.allSteps, wrapUpStep)
		cw.AddStep(wrapUpStep)
		e.emitter.ExecutorDiagnostic(state.stepNum, "executor_wrapup_nudge", map[string]any{"remaining": state.effectiveMaxSteps - state.stepNum})
	}
}

// handleCompactionAfterStep handles post-step compaction logic.
func (e *Executor) handleCompactionAfterStep(ctx context.Context, cw ContextManager, state *runState) (loopAction, error) {
	// Check for compaction using threshold-based logic
	fill := cw.CheckFill()

	// Emit context fill status
	e.emitter.ContextFill(fill.Percent, fill.Used, fill.Max, fill.Status, e.planStepID)

	switch fill.Status {
	case "compact", "warning":
		if result := cw.Compact(ctx); result != nil {
			e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
		}
		state.reactiveCompactAttempted = false
		state.preCompactionNudgeEmitted = false
	case "emergency":
		if result := cw.Compact(ctx); result != nil {
			e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
		}
		state.reactiveCompactAttempted = false
		state.preCompactionNudgeEmitted = false
	case "reject":
		if !state.reactiveCompactAttempted {
			state.reactiveCompactAttempted = true
			if result := cw.Compact(ctx); result != nil {
				e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
			}
			state.preCompactionNudgeEmitted = false
			return actionContinue, nil
		}
		return actionNone, fmt.Errorf("context window full after reactive compaction (%.1f%% of %d tokens)", fill.Percent, fill.Max)
	default:
		// Reset the flag on successful step completion so future steps can attempt reactive compaction
		state.reactiveCompactAttempted = false
	}
	return actionNone, nil
}
