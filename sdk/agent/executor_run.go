package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
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
	implicitFinishNudgeCount  int
	toolCallSyntaxNudgeCount  int
	wrapUpNudgeAttempted      bool
	reactiveCompactAttempted  bool
	preCompactionNudgeEmitted bool
	unlimitedSteps            bool
	effectiveMaxSteps         int
	finishResult              *ExecutorResult
	stepStartTime             time.Time
	circuitBreakerTriggered   bool
	responseGroup             int64
	checklistAvailable        bool
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
// isSuccessfulMutationStep reports whether a single step executed a mutating
// tool successfully: the action is a mutating tool, the call was not rejected
// by HITL ("[Tool call rejected" observation), and the tool did not report an
// error (ToolResult.IsError, e.g. a non-matching edit_file or a write_file to
// an invalid path made no real change).
//
// Centralized so the two mutation-detection helpers —
// hasMutatingToolExecuted (full-history scan) and recentSuccessfulMutation
// (lookback-window scan) — share one definition of "successful mutation"
// instead of duplicating the mutating-tools + rejected + failed checks.
func isSuccessfulMutationStep(s Step) bool {
	if s.Action.Name == "" {
		return false
	}
	if _, ok := mutatingTools[s.Action.Name]; !ok {
		return false
	}
	if strings.HasPrefix(s.Observation, "[Tool call rejected") {
		return false
	}
	if s.IsError {
		return false
	}
	return true
}

// hasMutatingToolExecuted checks whether any mutating tool (write_file,
// edit_file, create_directory, delete_file, delete_directory) was
// successfully executed during this step by scanning the accumulated steps.
func (e *Executor) hasMutatingToolExecuted(state *runState) bool {
	for _, s := range state.allSteps {
		if isSuccessfulMutationStep(s) {
			return true
		}
	}
	return false
}

// recentSuccessfulMutation reports whether a mutating tool was successfully
// executed within the last `lookback` tool-call steps. It scans state.allSteps
// backwards, skipping steps without an Action.Name (nudges) so only real tool
// calls count toward the window. Used by the wrap-up nudge to distinguish
// active progress (encourage continuation, preserving the path to OnStepLimit)
// from a stalled task (wrap up and finish).
func (e *Executor) recentSuccessfulMutation(state *runState, lookback int) bool {
	seen := 0
	for i := len(state.allSteps) - 1; i >= 0 && seen < lookback; i-- {
		s := state.allSteps[i]
		if s.Action.Name == "" {
			continue // skip nudge-only steps
		}
		seen++
		if isSuccessfulMutationStep(s) {
			return true
		}
	}
	return false
}

// hasChecklistUpdate reports whether update_checklist was successfully called
// at least once during this execution.
func (e *Executor) hasChecklistUpdate(state *runState) bool {
	for _, s := range state.allSteps {
		if s.Action.Name == "update_checklist" && !s.IsError {
			return true
		}
	}
	return false
}

// countProductiveToolCalls returns the number of tool calls (excluding finish,
// nudges, and meta-steps without an Action) executed so far. Used to determine
// whether a step is trivial enough to skip the checklist gate.
func (e *Executor) countProductiveToolCalls(state *runState) int {
	count := 0
	for _, s := range state.allSteps {
		if s.Action.Name == "" || s.Action.Name == "finish" {
			continue
		}
		count++
	}
	return count
}

// lastChecklistUnchecked returns the number of unchecked items in the most
// recent successful update_checklist call. Returns 0 if there is no checklist
// or all items are checked.
func (e *Executor) lastChecklistUnchecked(state *runState) int {
	for i := len(state.allSteps) - 1; i >= 0; i-- {
		s := state.allSteps[i]
		if s.Action.Name != "update_checklist" || s.IsError || s.Observation == "" {
			continue
		}
		m := checklistDoneRe.FindStringSubmatch(s.Observation)
		if len(m) == 3 {
			done, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			return total - done
		}
		return 0
	}
	return 0
}

// handleImplicitFinish handles the "no tool calls" branches: syntax-nudge → finish-nudge → implicit finish.
//
// The model can enter a failure-mode where it prints tool-call syntax
// (```bash_exec, ```read_file) as text instead of emitting a tool_use block.
// This is NOT a legitimate finish — the model is stuck. DetectToolCallSyntaxInContent
// catches this; we apply a dedicated nudge up to 3 times, then abort with
// Finished=false so the caller (subagent) treats it as a failure, not a success.
//
// For the general "no tool calls with end_turn" case, a text-only end_turn is
// always treated as a deliberate conversational turn: the model chose to respond
// with text instead of using tools. This is the expected behavior for
// conversational skills (explore, etc.) where the agent asks clarifying questions
// and yields control to the user. The text is emitted as a permanent assistant
// message via AssistantChunk/AssistantDone (when not suppressed), and the
// executor returns Finished=true so the orchestrator waits for the next user
// message.
//
// For "no tool calls but NOT end_turn" (max_tokens, stop_sequence), the model
// did not intentionally stop — we nudge up to 2 times before accepting an
// implicit finish.
func (e *Executor) handleImplicitFinish(resp *llm.ChatResponse, thought string, state *runState, cw ContextManager, hasTools bool) (*ExecutorResult, loopAction) {
	// Failure-mode: model printed tool-call syntax as text. This is not an
	// implicit finish — the model is stuck. Apply a dedicated nudge up to 3
	// times, then abort. Applies regardless of stop_reason (end_turn or not).
	if hasTools && DetectToolCallSyntaxInContent(thought) {
		if state.toolCallSyntaxNudgeCount < 3 {
			state.toolCallSyntaxNudgeCount++
			nudgeStep := Step{
				Thought:          thought,
				UserNudge:        executorToolCallSyntaxNudge,
				ReasoningContent: resp.Message.ReasoningContent,
				ReasoningItems:   resp.Message.ReasoningItems,
				TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "tool_call_syntax_nudge", map[string]any{"reason": "tool_call_syntax_as_text", "attempt": state.toolCallSyntaxNudgeCount})
			return nil, actionContinue
		}
		e.emitter.ExecutorDiagnostic(state.stepNum, "tool_call_syntax_abort", map[string]any{"attempts": state.toolCallSyntaxNudgeCount})
		return &ExecutorResult{
			Output:   "Aborted: model repeatedly printed tool-call syntax as text instead of using tool_use blocks",
			Steps:    state.allSteps,
			Finished: false,
		}, actionNone
	}

	// Check for implicit finish (no tool calls with end_turn).
	//
	// A text-only end_turn is always accepted as a deliberate finish — the
	// model chose to respond with text. This allows conversational skills
	// (explore, etc.) to ask questions and yield control to the user.
	// The "model printed tool-call syntax as text" failure mode is already
	// caught by DetectToolCallSyntaxInContent above.
	if resp.StopReason == "end_turn" {
		// Finish nudge: require explicit finish tool call before accepting completion
		// Only needed in plan-step execution where output needs structured capture
		if e.suppressAssistantEvents && !e.finishNudgeAttempted {
			e.finishNudgeAttempted = true
			nudgeStep := Step{
				Thought:          thought,
				UserNudge:        executorFinishNudge,
				ReasoningContent: resp.Message.ReasoningContent,
				ReasoningItems:   resp.Message.ReasoningItems,
				TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "executor_finish_nudge", map[string]any{"reason": "implicit_finish_without_tool"})
			return nil, actionContinue // retry — LLM should now call finish explicitly
		}

		step := Step{
			Thought:          thought,
			ReasoningContent: resp.Message.ReasoningContent,
			ReasoningItems:   resp.Message.ReasoningItems,
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

	// No tool calls but not end_turn — apply nudge if attempts remain
	if hasTools && state.implicitFinishNudgeCount < 2 {
		state.implicitFinishNudgeCount++
		nudgeStep := Step{
			Thought:          thought,
			UserNudge:        executorNudge,
			ReasoningContent: resp.Message.ReasoningContent,
			ReasoningItems:   resp.Message.ReasoningItems,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		e.emitter.ExecutorDiagnostic(state.stepNum, "executor_nudge", map[string]any{"reason": "no_tools_no_end_turn_on_step_1", "attempt": state.implicitFinishNudgeCount})
		return nil, actionContinue
	}

	// Finish nudge: require explicit finish tool call before accepting completion
	// Only needed in plan-step execution where output needs structured capture
	if e.suppressAssistantEvents && !e.finishNudgeAttempted {
		e.finishNudgeAttempted = true
		nudgeStep := Step{
			Thought:          thought,
			UserNudge:        executorFinishNudge,
			ReasoningContent: resp.Message.ReasoningContent,
			ReasoningItems:   resp.Message.ReasoningItems,
			TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
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
		ReasoningItems:   resp.Message.ReasoningItems,
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
	if e.circuitBreaker.TruncationAbortThreshold > 0 && e.consecutiveTruncationCount >= e.circuitBreaker.TruncationAbortThreshold {
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
		// Finish guard: allow the caller (e.g. sp4rk Conductor) to reject
		// finish when preconditions are not met (e.g. pending async
		// delegations). If the guard returns an error, inject a nudge and
		// retry instead of accepting finish.
		if e.finishGuard != nil {
			if guardErr := e.finishGuard(ctx); guardErr != nil {
				nudgeStep := Step{
					Thought:        thought,
					UserNudge:      guardErr.Error(),
					ReasoningItems: resp.Message.ReasoningItems,
					TokensUsed:     resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.ExecutorDiagnostic(state.stepNum, "finish_guard_rejected", map[string]any{"reason": guardErr.Error()})
				return nil, actionBreak, nil //nolint:nilerr // guard rejection is a nudge, not a propagated error
			}
		}

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
				Thought:        thought,
				UserNudge:      executorMutationNudge,
				ReasoningItems: resp.Message.ReasoningItems,
				TokensUsed:     resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "mutation_gate_nudge", map[string]any{
				"reason": "finish_without_mutation",
			})
			return nil, actionBreak, nil // retry — LLM should now make changes or justify
		}

		// Checklist gate (Enf-1): a non-trivial step must have at least one
		// update_checklist call. Trivial steps (≤ checklistTrivialThreshold
		// productive tool calls) are exempt. The gate is a soft nudge: after
		// one attempt, finish is accepted regardless.
		if e.checklistGateEnabled && state.checklistAvailable &&
			!e.hasChecklistUpdate(state) &&
			e.countProductiveToolCalls(state) > checklistTrivialThreshold &&
			!e.checklistMissingNudgeAttempted {
			e.checklistMissingNudgeAttempted = true
			nudgeStep := Step{
				Thought:        thought,
				UserNudge:      executorChecklistMissingNudge,
				ReasoningItems: resp.Message.ReasoningItems,
				TokensUsed:     resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			state.allSteps = append(state.allSteps, nudgeStep)
			cw.AddStep(nudgeStep)
			e.emitter.ExecutorDiagnostic(state.stepNum, "checklist_gate_nudge", map[string]any{
				"reason": "finish_without_checklist",
			})
			return nil, actionBreak, nil // retry — LLM should call update_checklist or justify
		}

		// Checklist gate (Enf-2): if the last checklist has unchecked items,
		// nudge the agent to complete them or explicitly justify skipping.
		if e.checklistGateEnabled && state.checklistAvailable &&
			!e.checklistUncheckedNudgeAttempted {
			if unchecked := e.lastChecklistUnchecked(state); unchecked > 0 {
				e.checklistUncheckedNudgeAttempted = true
				nudgeStep := Step{
					Thought:        thought,
					UserNudge:      fmt.Sprintf(executorChecklistUncheckedNudge, unchecked),
					ReasoningItems: resp.Message.ReasoningItems,
					TokensUsed:     resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				state.allSteps = append(state.allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.ExecutorDiagnostic(state.stepNum, "checklist_unchecked_nudge", map[string]any{
					"reason":    "finish_with_unchecked_checklist",
					"unchecked": unchecked,
				})
				return nil, actionBreak, nil // retry — LLM should complete or justify
			}
		}

		stepThought := ""
		stepReasoning := ""
		var stepReasoningItems []llm.ReasoningItem
		if callIdx == 0 {
			stepThought = thought
			stepReasoning = resp.Message.ReasoningContent
			stepReasoningItems = resp.Message.ReasoningItems
		}
		step := Step{
			Thought:          stepThought,
			ReasoningContent: stepReasoning,
			ReasoningItems:   stepReasoningItems,
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
			IsError:       true,
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
	var cacheHash string
	observation, cacheHash = e.processToolResult(execCtx, observation, result.Content, action.Name, action.Input, cw)

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
	var stepReasoningItems []llm.ReasoningItem
	if callIdx == 0 {
		stepThought = thought
		stepReasoning = resp.Message.ReasoningContent
		stepReasoningItems = resp.Message.ReasoningItems
	}
	step := Step{
		Thought:          stepThought,
		ReasoningContent: stepReasoning,
		ReasoningItems:   stepReasoningItems,
		Action:           action,
		Observation:      observation,
		IsUntrusted:      isUntrusted,
		IsError:          result.IsError,
		CacheHash:        cacheHash,
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
			IsError:          true,
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
			IsError:          true,
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
		// The ID must be unique across the whole conversation history: these
		// steps are replayed to the API as assistant tool_calls matched with
		// tool results by ID, and duplicate IDs (e.g. from two batch calls in
		// different steps) can cause provider-side 400 errors. Include the
		// step number and the batch call index to guarantee uniqueness.
		subCall := llm.ToolCall{
			ID:    fmt.Sprintf("batch_%d_%d_sub_%d", state.stepNum, callIdx, subIdx),
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
				IsError:       true,
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
					IsError:       true,
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
		var batchCacheHash string
		observation, batchCacheHash = e.processToolResult(execCtx, observation, result.Content, subCall.Name, subCall.Input, cw)

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
			IsError:          result.IsError,
			CacheHash:        batchCacheHash,
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
// to avoid duplicated pipeline logic. Returns the processed observation and
// the ToolResultCache hash (empty if the tool is non-cacheable or cache is nil).
func (e *Executor) processToolResult(
	execCtx context.Context,
	observation string,
	fullResult string,
	toolName string,
	input json.RawMessage,
	cw ContextManager,
) (processedObservation, cacheHash string) {
	// --- Stage 1: Per-tool truncation + optional caching ---
	truncated, wasTruncated := e.applyPerToolTruncation(observation, toolName)
	if wasTruncated {
		observation = truncated
	}

	if e.toolCache != nil {
		if _, isNonCacheable := e.nonCacheableTools[toolName]; !isNonCacheable {
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
			} else if meta.FileBacked {
				// File-backed entries (read_file) get a nudge even without
				// Stage 1 truncation — tool_result_read serves the token-economy
				// use case (LLM reads fragments on demand), not just truncation
				// recovery.
				observation += formatFileBackedNudge(cacheHash)
			}
		}
	}

	// --- Stage 2: Token-budget truncation (preserve nudge) ---
	const stage1NudgePrefix = "\n\n[This output was truncated to"
	var nudge string
	if idx := strings.Index(observation, stage1NudgePrefix); idx >= 0 {
		nudge = observation[idx:]
		observation = observation[:idx]
	} else if idx := strings.Index(observation, fileBackedNudgePrefix); idx >= 0 {
		nudge = observation[idx:]
		observation = observation[:idx]
	}
	// Suppress the Stage‑2 hash hint when a nudge containing the cache hash
	// was already appended — avoids redundant "Use tool_result_read…" instructions.
	budgetHash := cacheHash
	if nudge != "" {
		budgetHash = ""
	}
	observation = e.applyToolResultBudget(observation, cw, toolName, budgetHash)
	observation += nudge

	processedObservation = observation
	return
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

	if abortThreshold > 0 && e.consecutiveRepeatCount >= abortThreshold {
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

	if nudgeThreshold > 0 && e.consecutiveRepeatCount >= nudgeThreshold {
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
	// Skip exempt tools (mutating tools, meta-tools) whose short successful
	// results are legitimate in bursts and must not be counted as fruitless.
	if _, exempt := circuitBreakerExemptTools[action.Name]; exempt {
		e.consecutiveFruitlessCount = 0
		return actionNone, nil, nil
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
		e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, isError)
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
			UserNudge: fmt.Sprintf(executorFruitlessNudge, e.consecutiveFruitlessCount),
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		state.circuitBreakerTriggered = true
		return actionBreak, nil, nil
	}

	return actionNone, nil, nil
}

// checkSameToolRepetition detects repetitive calls to the same tool with
// varied arguments but similar-sized results, excluding tools in
// circuitBreakerExemptTools (store_fact, mutating tools, meta-tools) whose
// short similarly-sized successful results are legitimate in bursts.
func (e *Executor) checkSameToolRepetition(
	ctx context.Context,
	action llm.ToolCall,
	callIdx int,
	observation string,
	result tools.ToolResult,
	state *runState,
	cw ContextManager,
) (loopAction, *ExecutorResult, error) {
	// Skip exempt tools: it's legitimate to call them many times in a row
	// with similarly-sized (short) successful results (e.g. batch file edits
	// each returning "successfully edited file", or consecutive store_fact /
	// update_checklist confirmations).
	if _, exempt := circuitBreakerExemptTools[action.Name]; exempt {
		// Reset tracker so the next non-exempt tool starts fresh.
		e.sameToolConsecutiveCount = 0
		e.sameToolLastName = ""
		e.sameToolLastResultLen = 0
		return actionNone, nil, nil
	}
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
		e.emitter.ToolResult(state.stepNum, callIdx, len(observation), observation, result.IsError)
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
			UserNudge: fmt.Sprintf(executorSameToolRepeatNudge, action.Name, e.sameToolConsecutiveCount),
		}
		state.allSteps = append(state.allSteps, nudgeStep)
		cw.AddStep(nudgeStep)
		state.circuitBreakerTriggered = true
		return actionBreak, nil, nil
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

		if e.circuitBreaker.ParseErrorAbortThreshold > 0 && e.consecutiveParseErrorCount >= e.circuitBreaker.ParseErrorAbortThreshold {
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
		remaining := state.effectiveMaxSteps - state.stepNum
		// If the agent has been actively making progress (a successful mutating
		// call within the recent lookback window), use the continuation-oriented
		// nudge instead of the wrap-up nudge. This avoids pressuring the agent
		// into a premature finish and keeps the path to OnStepLimit open: if it
		// keeps working past the budget, the user is asked to extend it.
		mode := "default"
		wrapUpMsg := fmt.Sprintf(executorWrapUpNudge, remaining)
		if e.recentSuccessfulMutation(state, wrapUpActiveLookback) {
			mode = "active_progress"
			wrapUpMsg = fmt.Sprintf(executorWrapUpNudgeActive, remaining)
		}
		wrapUpStep := Step{
			UserNudge: wrapUpMsg,
		}
		state.allSteps = append(state.allSteps, wrapUpStep)
		cw.AddStep(wrapUpStep)
		e.emitter.ExecutorDiagnostic(state.stepNum, "executor_wrapup_nudge", map[string]any{
			"remaining": remaining,
			"mode":      mode,
		})
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
