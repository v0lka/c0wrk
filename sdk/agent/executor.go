package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

const executorNudge = "[System] You have tools available that can help answer this request. Before finishing, try using relevant tools to discover the answer. Do NOT say you cannot determine something without first attempting to use your tools."

// nonCacheableTools is the set of tool names whose results are NOT cached.
// These are internal meta-tools or produce tiny outputs where caching adds overhead.
var nonCacheableTools = map[string]struct{}{
	"tool_result_read":  {},
	"finish":            {},
	"read_step_output":  {},
	"list_step_outputs": {},
	"store_fact":        {},
	"search_facts":      {},
	"set_step_status":   {},
	"ask_user":          {},
}

const executorWrapUpNudge = "[System] You are running low on tool call iterations. You have %d iteration(s) remaining. Wrap up your work NOW: summarize your findings and finish. Do not start new explorations."

const executorFinishNudge = "[System] You must call the finish tool to complete your task. Simply responding with text does not count as completion. Call the finish tool now with your final answer."

const (
	repeatNudgeMessage = "[System] You have called the same tool with the same arguments " +
		"multiple times in a row and it keeps failing. Try a different approach: " +
		"use different arguments, a different tool, or call finish if the task cannot be completed."

	repeatErrorNudgeMessage = "[System] The previous call to this tool with " +
		"identical arguments returned an error. Retrying the same call will produce the " +
		"same error. You must try a different approach: use different arguments, a " +
		"different tool, or call finish if the task cannot be completed."

	truncationMessage = "[System] Your tool call to '%s' was NOT executed because your output " +
		"was cut off by the model's maximum output token limit. The tool call arguments are " +
		"incomplete/truncated. You MUST use a different approach that produces smaller output — " +
		"for example, break large file writes into multiple smaller operations, use read_file " +
		"with line ranges instead of reading entire files, or reduce the content size."

	parseErrorNudgeMessage = "[System] This tool has now failed to parse input %d times in a row. " +
		"The arguments you are generating are malformed. Try a completely different approach: " +
		"reduce the size of your arguments, use a different tool, or break the operation into " +
		"smaller steps."

	executorFruitlessNudge = "[System] Your last %d tool calls returned empty or minimal results. Continuing to search with different parameters is unlikely to yield new information. Summarize what you have found so far and call the finish tool."

	executorSameToolRepeatNudge = "[System] You have called '%s' %d times in a row with different arguments but consistently similar results. This suggests the information you are looking for may not exist or requires a fundamentally different approach. Summarize your findings and call the finish tool."
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
	circuitBreaker          CircuitBreakerConfig
	stepLimitFunc           StepLimitFunc // callback when step limit is reached

	// Tool result caching and per-tool truncation (Stage 1).
	toolCache          *ToolResultCache
	perToolTruncation  map[string]ToolTruncationConfig

	// Circuit breaker: detect repeated identical tool calls
	consecutiveRepeatCount int
	lastToolKey            string // "name:" + compactJSON(input) for dedup
	lastToolResultIsError  bool   // whether the last identical tool call returned an error

	// Truncation tracker: detect consecutive max_tokens responses with tool calls
	consecutiveTruncationCount int

	// Parse error tracker: detect consecutive parse failures on the same tool
	consecutiveParseErrorTool  string
	consecutiveParseErrorCount int

	// Fruitless result tracker: detect consecutive minimal-result calls
	consecutiveFruitlessCount int
	fruitlessNudgeAttempted   bool

	// Same-tool repetition tracker: detect same tool with varied args but similar results
	sameToolConsecutiveCount int
	sameToolLastName         string
	sameToolLastResultLen    int
	sameToolNudgeAttempted   bool

	// Finish nudge tracker: ensure explicit finish tool call before accepting implicit finish
	finishNudgeAttempted bool

	// Multi-tool-call response group counter
	responseGroupCounter int64

	// Plan-step context for structured logging
	planStepID    string // e.g. "step_3" (empty if not plan mode)
	planStepIndex int    // 1-based position in plan (0 if not plan mode)
	planStepTotal int    // total steps in plan (0 if not plan mode)

	// Reasoning effort for LLM calls (empty = no reasoning control)
	reasoningEffort llm.ReasoningEffort

	// Pre-compaction nudge: context fill % that triggers store_fact warning (0 = disabled)
	preWarningPercent int

	logger *slog.Logger
}

// NewExecutor creates a new Executor.
// emitter is optional (nil-safe).
// suppressAssistantEvents disables AssistantChunk/AssistantDone events; set to true for plan-step
// executors to avoid duplicate assistant messages when the orchestrator handles final output.
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, counter llm.TokenCounter, maxSteps int, emitter AgentEvents, suppressAssistantEvents bool, toolResultBudget ToolResultBudget, circuitBreaker CircuitBreakerConfig) *Executor {
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
		circuitBreaker:          circuitBreaker,
	}
}

// SetLogger sets the logger for the executor.
func (e *Executor) SetLogger(l *slog.Logger) { e.logger = l }

// SetReasoningEffort sets the reasoning effort for LLM calls.
func (e *Executor) SetReasoningEffort(effort llm.ReasoningEffort) { e.reasoningEffort = effort }

// SetPreWarningPercent sets the context fill percentage that triggers the pre-compaction
// store_fact nudge. When fill reaches this threshold (but is below the compaction trigger),
// a warning listing vulnerable tool outputs is appended to the observation.
func (e *Executor) SetPreWarningPercent(percent int) { e.preWarningPercent = percent }

// log returns the executor's logger or slog.Default() if none was set.
func (e *Executor) log() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return slog.Default()
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

// SetToolCache sets the shared tool result cache for this executor.
// All tool results will be stored in this cache before truncation.
func (e *Executor) SetToolCache(cache *ToolResultCache) {
	e.toolCache = cache
}

// SetPerToolTruncation sets per-tool truncation defaults for Stage 1 (line/byte-based).
func (e *Executor) SetPerToolTruncation(cfg map[string]ToolTruncationConfig) {
	e.perToolTruncation = cfg
}

// applyToolResultBudget truncates a tool result if it exceeds the budget.
// The budget is min(HardCapTokens, AvailableTokens * MaxFillFraction) with a 256-token floor.
// When truncated, a notice is appended to inform the model.
func (e *Executor) applyToolResultBudget(observation string, cw ContextManager, toolName string) string {
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

	// Generate context-aware hint based on tool name
	hint := getTruncationHint(toolName)

	return truncated + fmt.Sprintf(
		"\n\n[OUTPUT TRUNCATED: showing ~%d of ~%d tokens (%.0f%%). %s]",
		capTokens, observationTokens, float64(capTokens)/float64(observationTokens)*100, hint,
	)
}

// getTruncationHint returns a context-aware hint based on the tool name.
func getTruncationHint(toolName string) string {
	switch toolName {
	case "read_file":
		return "Re-read the file with start_line/end_line to see specific sections, or use ripgrep to search for specific content."
	case "ripgrep", "grep":
		return "Narrow your search pattern or add path filters to reduce results."
	case "glob":
		return "Use a more specific glob pattern to reduce results."
	case "web_fetch":
		return "The page content was truncated. Ask the user to open the URL directly, or try fetching a more specific page."
	default:
		return "Break into smaller operations or use targeted queries."
	}
}

// applyPerToolTruncation applies Stage 1 line/byte-based truncation from per-tool config.
// Returns the (possibly truncated) content and a boolean indicating whether truncation occurred.
func (e *Executor) applyPerToolTruncation(content, toolName string) (string, bool) {
	if e.perToolTruncation == nil {
		return content, false
	}
	cfg, ok := e.perToolTruncation[toolName]
	if !ok {
		return content, false
	}
	truncated := false

	// Line-based truncation
	if cfg.MaxLines > 0 {
		lines := strings.Split(content, "\n")
		if len(lines) > cfg.MaxLines {
			content = strings.Join(lines[:cfg.MaxLines], "\n")
			truncated = true
		}
	}

	// Byte-based truncation (UTF-8 safe: walk back to last valid codepoint boundary).
	if cfg.MaxBytes > 0 && len(content) > cfg.MaxBytes {
		truncatedContent := content[:cfg.MaxBytes]
		for truncatedContent != "" && !utf8.ValidString(truncatedContent) {
			truncatedContent = truncatedContent[:len(truncatedContent)-1]
		}
		content = truncatedContent
		truncated = true
	}

	return content, truncated
}

// formatFragmentationNudge returns a message instructing the LLM how to read
// truncated output in fragments via tool_result_read.
func formatFragmentationNudge(hash, toolName string, maxLines int) string {
	return fmt.Sprintf(
		"\n\n[This output was truncated to %d lines for '%s'. "+
			"The full result is cached with hash: %s. "+
			"Use tool_result_read(hash=\"%s\", start_line=1, num_lines=N) to read fragments. "+
			"num_lines must not exceed %d.]",
		maxLines, toolName, hash, hash, maxLines,
	)
}

// buildCacheMeta extracts file metadata from tool input for file-based tools.
// Returns ToolCacheMeta with FilePath/FileMtime/FileSize set for file tools,
// and IsMCP set for MCP-sourced tools.
func (e *Executor) buildCacheMeta(ctx context.Context, toolName string, input json.RawMessage) ToolCacheMeta {
	var meta ToolCacheMeta

	// Detect MCP tools via source.
	if source := e.tools.GetToolSource(toolName); source != "" && source != "core" {
		meta.IsMCP = true
		return meta // MCP tools don't get file coherence metadata
	}

	// Extract file path for file-based tools.
	switch toolName {
	case "read_file", "write_file", "edit_file":
		var params struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &params); err == nil && params.Path != "" {
			absPath := params.Path
			if !filepath.IsAbs(absPath) {
				if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
					absPath = filepath.Join(wsPath, absPath)
				}
			}
			// Validate path is within workspace boundary before stat.
			if wsPath := tools.WorkspacePathFrom(ctx); wsPath != "" {
				if !isPathWithinWorkspace(absPath, wsPath) {
					return meta
				}
			}
			if info, err := os.Stat(absPath); err == nil {
				meta.FilePath = absPath
				meta.FileMtime = info.ModTime().UnixNano()
				meta.FileSize = info.Size()
			}
		}
	}

	return meta
}

// isPathWithinWorkspace checks whether the given absolute path lies within the workspace root.
func isPathWithinWorkspace(path, workspaceRoot string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(workspaceRoot)
	// filepath.HasPrefix is not available; use strings.HasPrefix with trailing separator.
	rootWithSep := cleanRoot
	if !strings.HasSuffix(rootWithSep, string(filepath.Separator)) {
		rootWithSep += string(filepath.Separator)
	}
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, rootWithSep)
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
	preCompactionNudgeEmitted := false
	unlimitedSteps := false
	effectiveMaxSteps := e.maxSteps // local copy to avoid mutating struct field

	for stepNum := 1; unlimitedSteps || stepNum <= effectiveMaxSteps+1; stepNum++ {
		// At the boundary: when we've just exceeded effectiveMaxSteps
		if !unlimitedSteps && stepNum > effectiveMaxSteps {
			if e.stepLimitFunc == nil {
				break // no callback configured: exit silently at step limit
			}
			resp, err := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, "")
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
				effectiveMaxSteps++ // allow exactly one more
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
			Messages:        messages,
			Tools:           toolDefs,
			MaxTokens:       cw.OutputLimit(),
			ReasoningEffort: e.reasoningEffort,
		}

		// Call LLM
		resp, err := e.llm.Call(ctx, req)
		if err != nil {
			if isContextExceededError(err) && !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				if result := cw.Compact(ctx); result != nil {
					e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
				}
				e.emitter.ExecutorDiagnostic(stepNum, "reactive_compaction_api_error", map[string]any{"error": err.Error()})
				continue
			}
			return nil, err
		}

		if resp == nil {
			return nil, fmt.Errorf("llm returned empty response at step %d", stepNum)
		}

		// Parse response
		thought := resp.Message.Content

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

			// Finish nudge: require explicit finish tool call before accepting completion
			// Only needed in plan-step execution where output needs structured capture
			if e.suppressAssistantEvents && !e.finishNudgeAttempted {
				e.finishNudgeAttempted = true
				nudgeStep := Step{
					Thought:     thought,
					Observation: executorFinishNudge,
					TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.ExecutorDiagnostic(stepNum, "executor_finish_nudge", map[string]any{"reason": "implicit_finish_without_tool"})
				continue // retry — LLM should now call finish explicitly
			}

			step := Step{
				Thought:          thought,
				ReasoningContent: resp.Message.ReasoningContent,
				TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
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

			// Finish nudge: require explicit finish tool call before accepting completion
			// Only needed in plan-step execution where output needs structured capture
			if e.suppressAssistantEvents && !e.finishNudgeAttempted {
				e.finishNudgeAttempted = true
				nudgeStep := Step{
					Thought:     thought,
					Observation: executorFinishNudge,
					TokensUsed:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				e.emitter.ExecutorDiagnostic(stepNum, "executor_finish_nudge", map[string]any{"reason": "implicit_finish_without_tool"})
				continue // retry — LLM should now call finish explicitly
			}

			// No tool calls but not end_turn — treat as implicit finish anyway
			step := Step{
				Thought:          thought,
				ReasoningContent: resp.Message.ReasoningContent,
				TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
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
			e.emitter.ToolCall(stepNum, 0, truncAction.Name, string(truncAction.Input), e.tools.GetToolSource(truncAction.Name))

			e.consecutiveTruncationCount++
			if e.consecutiveTruncationCount >= e.circuitBreaker.TruncationAbortThreshold {
				e.emitter.ExecutorDiagnostic(stepNum, "truncation_abort", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})
				abortReason := fmt.Sprintf("Tool '%s' output was truncated %d times consecutively by max output token limit", truncAction.Name, e.consecutiveTruncationCount)
				if e.stepLimitFunc != nil {
					slResp, slErr := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, abortReason)
					if slErr == nil {
						switch slResp {
						case StepLimitAllowOnce:
							e.consecutiveTruncationCount = 0
							nudgeStep := Step{
								UserNudge: "[System] The user acknowledged the truncation circuit breaker and granted you ONE more chance. " +
									"You MUST use smaller operations to avoid hitting the output token limit.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
							continue
						case StepLimitAllowAlways:
							e.consecutiveTruncationCount = 0
							e.circuitBreaker.TruncationAbortThreshold = 1 << 30 // disable
							nudgeStep := Step{
								UserNudge: "[System] The user has overridden the truncation circuit breaker. " +
									"You may continue, but try to produce smaller outputs.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
							continue
						default:
							// StepLimitDeny or empty — fall through to abort
						}
					}
				}
				return &ExecutorResult{
					Output:   fmt.Sprintf("Aborted: tool '%s' output was truncated %d times consecutively by max output token limit", truncAction.Name, e.consecutiveTruncationCount),
					Steps:    allSteps,
					Finished: false,
				}, nil
			}

			truncObs := fmt.Sprintf(truncationMessage, truncAction.Name)
			e.emitter.ExecutorDiagnostic(stepNum, "truncation_detected", map[string]any{"tool": truncAction.Name, "consecutive": e.consecutiveTruncationCount})

			step := Step{
				Thought:          thought,
				ReasoningContent: resp.Message.ReasoningContent,
				Action:           truncAction,
				Observation:      truncObs,
				TokensUsed:       resp.Usage.InputTokens + resp.Usage.OutputTokens,
			}
			allSteps = append(allSteps, step)
			cw.AddStep(step)
			e.emitter.ToolResult(stepNum, 0, len(truncObs), truncObs)
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
			continue
		}

		// Reset truncation counter on any non-truncated response
		e.consecutiveTruncationCount = 0

		// --- Process ALL tool calls from the response ---
		toolCalls := resp.Message.ToolCalls

		// Generate ResponseGroup ID for multi-call responses
		var responseGroup int64
		if len(toolCalls) > 1 {
			e.responseGroupCounter++
			responseGroup = e.responseGroupCounter
		}

		var finishResult *ExecutorResult
		circuitBreakerTriggered := false

		for callIdx, action := range toolCalls {
			// Emit tool call
			toolDisplayName := action.Name
			// For tool_result_read: display as "original_tool (cached)" in chat UI
			if action.Name == "tool_result_read" && e.toolCache != nil {
				var trParams struct{ Hash string `json:"hash"` }
				if json.Unmarshal(action.Input, &trParams) == nil && trParams.Hash != "" {
					if entry, ok := e.toolCache.Get(trParams.Hash); ok {
						toolDisplayName = entry.ToolName + " (cached)"
					}
				}
			}
			e.emitter.ToolCall(stepNum, callIdx, toolDisplayName, string(action.Input), e.tools.GetToolSource(action.Name))

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
			nudgeThreshold := e.circuitBreaker.RepeatNudgeThreshold
			abortThreshold := e.circuitBreaker.RepeatAbortThreshold
			if e.lastToolResultIsError {
				nudgeThreshold = e.circuitBreaker.RepeatNudgeThreshold - 1
				abortThreshold = e.circuitBreaker.RepeatAbortThreshold - 1
			}

			if e.consecutiveRepeatCount >= abortThreshold {
				e.emitter.ExecutorDiagnostic(stepNum, "repeated_tool_call_abort", map[string]any{"tool": action.Name, "repeat_count": e.consecutiveRepeatCount})
				abortReason := fmt.Sprintf("Tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount)
				if e.stepLimitFunc != nil {
					slResp, slErr := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, abortReason)
					if slErr == nil {
						switch slResp {
						case StepLimitAllowOnce:
							e.consecutiveRepeatCount = 0
							nudgeStep := Step{
								UserNudge: "[System] The user acknowledged the circuit breaker and granted you ONE more chance. " +
									"You MUST change your approach immediately — do NOT repeat the same tool call.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							circuitBreakerTriggered = true
						case StepLimitAllowAlways:
							e.consecutiveRepeatCount = 0
							e.circuitBreaker.RepeatAbortThreshold = 1 << 30 // disable
							nudgeStep := Step{
								UserNudge: "[System] The user has overridden the circuit breaker. You may continue, " +
									"but try to vary your approach to avoid repeating the same failing pattern.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							circuitBreakerTriggered = true
						default:
							// StepLimitDeny or empty — fall through to abort
						}
					}
				}
				if circuitBreakerTriggered {
					break
				}
				abortMsg := fmt.Sprintf("Aborted: tool '%s' called %d times consecutively with identical arguments", action.Name, e.consecutiveRepeatCount)
				e.emitter.ToolResult(stepNum, callIdx, len(abortMsg), abortMsg)
				return &ExecutorResult{
					Output:   abortMsg,
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
				e.emitter.ToolResult(stepNum, callIdx, len(nudgeMsg), nudgeMsg)
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
				allSteps = append(allSteps, step)
				cw.AddStep(step)
				circuitBreakerTriggered = true
				break
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

				// Emit finishing event so the frontend can show "Finishing..." status
				// instead of "Running tool: finish".
				e.emitter.Finishing(stepNum, params.Answer)

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
				allSteps = append(allSteps, step)

				e.emitter.ToolResult(stepNum, callIdx, len(params.Answer), params.Answer)

				finishResult = &ExecutorResult{
					Output:   params.Answer,
					Steps:    allSteps,
					Finished: true,
				}
				break // stop processing further tool calls
			}

			// Execute the tool (task context should already be set by the caller)
			// Inject tool result cache into context so tool_result_read can access it.
			// Also inject per-tool truncation config for num_lines enforcement.
			execCtx := ctx
			if e.toolCache != nil {
				execCtx = WithToolResultCache(ctx, e.toolCache)
				execCtx = WithPerToolTruncation(execCtx, e.perToolTruncation)
			}
			result, err := e.tools.Execute(execCtx, action.Name, action.Input)
			if err != nil {
				// Infrastructure error
				return nil, err
			}

			observation := result.Content
			e.lastToolResultIsError = result.IsError

			// Determine if the tool output is from an untrusted external source
			// for prompt injection defense wrapping in BuildPrompt().
			isUntrusted := e.tools.IsToolUntrusted(action.Name)

			// --- Fruitless result detector: consecutive minimal-result calls ---
			// A result is "fruitless" if it's small AND not an error (errors have their own tracking)
			fruitlessMaxLen := e.circuitBreaker.FruitlessMaxResultLen
			if fruitlessMaxLen == 0 {
				fruitlessMaxLen = 32 // default
			}
			isFruitless := !result.IsError && len(result.Content) <= fruitlessMaxLen
			if isFruitless {
				e.consecutiveFruitlessCount++
			} else if !result.IsError {
				// Reset on non-fruitless, non-error result
				e.consecutiveFruitlessCount = 0
			}

			// Check fruitless thresholds (skip if threshold is 0 = disabled)
			if e.circuitBreaker.FruitlessAbortThreshold > 0 && e.consecutiveFruitlessCount >= e.circuitBreaker.FruitlessAbortThreshold {
				e.emitter.ExecutorDiagnostic(stepNum, "fruitless_abort", map[string]any{"consecutive": e.consecutiveFruitlessCount})
				e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)
				abortReason := fmt.Sprintf("%d consecutive tool calls returned empty or minimal results", e.consecutiveFruitlessCount)
				if e.stepLimitFunc != nil {
					slResp, slErr := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, abortReason)
					if slErr == nil {
						switch slResp {
						case StepLimitAllowOnce:
							e.consecutiveFruitlessCount = 0
							e.fruitlessNudgeAttempted = false
							nudgeStep := Step{
								UserNudge: "[System] The user acknowledged the fruitless-results circuit breaker and granted you ONE more chance. " +
									"Try a fundamentally different approach to find the information you need.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							circuitBreakerTriggered = true
						case StepLimitAllowAlways:
							e.consecutiveFruitlessCount = 0
							e.fruitlessNudgeAttempted = false
							e.circuitBreaker.FruitlessAbortThreshold = 0 // disable
							nudgeStep := Step{
								UserNudge: "[System] The user has overridden the fruitless-results circuit breaker. " +
									"You may continue searching, but consider varying your approach.",
							}
							allSteps = append(allSteps, nudgeStep)
							cw.AddStep(nudgeStep)
							circuitBreakerTriggered = true
						default:
							// StepLimitDeny or empty — fall through to abort
						}
					}
				}
				if circuitBreakerTriggered {
					break
				}
				return &ExecutorResult{
					Output:   fmt.Sprintf("Aborted: %d consecutive tool calls returned empty or minimal results", e.consecutiveFruitlessCount),
					Steps:    allSteps,
					Finished: false,
				}, nil
			}

			if e.circuitBreaker.FruitlessNudgeThreshold > 0 && e.consecutiveFruitlessCount >= e.circuitBreaker.FruitlessNudgeThreshold && !e.fruitlessNudgeAttempted {
				e.fruitlessNudgeAttempted = true
				e.emitter.ExecutorDiagnostic(stepNum, "fruitless_nudge", map[string]any{"consecutive": e.consecutiveFruitlessCount})
				e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)
				nudgeStep := Step{
					Observation: fmt.Sprintf(executorFruitlessNudge, e.consecutiveFruitlessCount),
				}
				allSteps = append(allSteps, nudgeStep)
				cw.AddStep(nudgeStep)
				circuitBreakerTriggered = true
				break
			}
			// --- End fruitless result detector ---

			// --- Same-tool repetition detector: same tool, varied args, similar results ---
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
					e.emitter.ExecutorDiagnostic(stepNum, "same_tool_repeat_abort", map[string]any{"tool": action.Name, "consecutive": e.sameToolConsecutiveCount})
					e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)
					abortReason := fmt.Sprintf("Tool '%s' called %d times in a row with different arguments but similar results", action.Name, e.sameToolConsecutiveCount)
					if e.stepLimitFunc != nil {
						slResp, slErr := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, abortReason)
						if slErr == nil {
							switch slResp {
							case StepLimitAllowOnce:
								e.sameToolConsecutiveCount = 0
								e.sameToolNudgeAttempted = false
								nudgeStep := Step{
									UserNudge: "[System] The user acknowledged the same-tool circuit breaker and granted you ONE more chance. " +
										"Try a completely different tool or approach instead of repeating the same tool.",
								}
								allSteps = append(allSteps, nudgeStep)
								cw.AddStep(nudgeStep)
								circuitBreakerTriggered = true
							case StepLimitAllowAlways:
								e.sameToolConsecutiveCount = 0
								e.sameToolNudgeAttempted = false
								e.circuitBreaker.SameToolRepeatAbortThreshold = 0 // disable
								nudgeStep := Step{
									UserNudge: "[System] The user has overridden the same-tool circuit breaker. " +
										"You may continue, but consider using different tools or approaches.",
								}
								allSteps = append(allSteps, nudgeStep)
								cw.AddStep(nudgeStep)
								circuitBreakerTriggered = true
							default:
								// StepLimitDeny or empty — fall through to abort
							}
						}
					}
					if circuitBreakerTriggered {
						break
					}
					return &ExecutorResult{
						Output:   fmt.Sprintf("Aborted: tool '%s' called %d times in a row with different arguments but similar results", action.Name, e.sameToolConsecutiveCount),
						Steps:    allSteps,
						Finished: false,
					}, nil
				}

				if e.circuitBreaker.SameToolRepeatNudgeThreshold > 0 && e.sameToolConsecutiveCount >= e.circuitBreaker.SameToolRepeatNudgeThreshold && !e.sameToolNudgeAttempted {
					e.sameToolNudgeAttempted = true
					e.emitter.ExecutorDiagnostic(stepNum, "same_tool_repeat_nudge", map[string]any{"tool": action.Name, "consecutive": e.sameToolConsecutiveCount})
					e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)
					nudgeStep := Step{
						Observation: fmt.Sprintf(executorSameToolRepeatNudge, action.Name, e.sameToolConsecutiveCount),
					}
					allSteps = append(allSteps, nudgeStep)
					cw.AddStep(nudgeStep)
					circuitBreakerTriggered = true
					break
				}
			} else {
				// Reset tracker when store_fact is used so the next non-store_fact tool starts fresh
				e.sameToolConsecutiveCount = 0
				e.sameToolLastName = ""
				e.sameToolLastResultLen = 0
			}
			// --- End same-tool repetition detector ---

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

				if e.consecutiveParseErrorCount >= e.circuitBreaker.ParseErrorAbortThreshold {
					e.emitter.ExecutorDiagnostic(stepNum, "parse_error_abort", map[string]any{"tool": action.Name, "consecutive_parse_errors": e.consecutiveParseErrorCount})
					e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)
					abortReason := fmt.Sprintf("Tool '%s' failed to parse input %d times consecutively", action.Name, e.consecutiveParseErrorCount)
					if e.stepLimitFunc != nil {
						slResp, slErr := e.stepLimitFunc(ctx, stepNum, effectiveMaxSteps, abortReason)
						if slErr == nil {
							switch slResp {
							case StepLimitAllowOnce:
								e.consecutiveParseErrorCount = 0
								nudgeStep := Step{
									UserNudge: "[System] The user acknowledged the parse-error circuit breaker and granted you ONE more chance. " +
										"You MUST fix your tool call arguments — they are malformed. Try a simpler approach.",
								}
								allSteps = append(allSteps, nudgeStep)
								cw.AddStep(nudgeStep)
								circuitBreakerTriggered = true
							case StepLimitAllowAlways:
								e.consecutiveParseErrorCount = 0
								e.circuitBreaker.ParseErrorAbortThreshold = 1 << 30 // disable
								nudgeStep := Step{
									UserNudge: "[System] The user has overridden the parse-error circuit breaker. " +
										"You may continue, but fix your tool call argument formatting.",
								}
								allSteps = append(allSteps, nudgeStep)
								cw.AddStep(nudgeStep)
								circuitBreakerTriggered = true
							default:
								// StepLimitDeny or empty — fall through to abort
							}
						}
					}
					if circuitBreakerTriggered {
						break
					}
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

			// --- Stage 1: Cache full result and apply per-tool truncation ---
			if e.toolCache != nil {
				if _, isNonCacheable := nonCacheableTools[action.Name]; !isNonCacheable {
					meta := e.buildCacheMeta(execCtx, action.Name, action.Input)
					hash := e.toolCache.Store(action.Name, result.Content, meta)

					// Apply per-tool line/byte truncation (Stage 1).
					truncated, wasTruncated := e.applyPerToolTruncation(observation, action.Name)
					if wasTruncated {
						maxLines := 0
						if e.perToolTruncation != nil {
							if cfg, ok := e.perToolTruncation[action.Name]; ok {
								maxLines = cfg.MaxLines
							}
						}
						nudge := formatFragmentationNudge(hash, action.Name, maxLines)
						observation = truncated + nudge
					}
				}
			}
			// --- End Stage 1 caching / truncation ---

			// --- Stage 2: Token-budget truncation (preserve Stage 1 nudge) ---
			// Extract any Stage 1 nudge so it isn't stripped by token-budget truncation.
			const stage1NudgePrefix = "\n\n[This output was truncated to"
			var stage1Nudge string
			if idx := strings.Index(observation, stage1NudgePrefix); idx >= 0 {
				stage1Nudge = observation[idx:]
				observation = observation[:idx]
			}
			observation = e.applyToolResultBudget(observation, cw, action.Name)
			observation += stage1Nudge
			// --- End Stage 2 ---

			// Emit tool result
			e.emitter.ToolResult(stepNum, callIdx, len(observation), observation)

			// Pre-compaction nudge: warn LLM when context pressure enters danger zone.
			// NOTE: The nudge is appended AFTER ToolResult emission intentionally — it is
			// only for LLM context (stored in Step.Observation), not for frontend display.
			// Only on the last tool call in the response, so the nudge appears once at the end.
			if callIdx == len(toolCalls)-1 && e.preWarningPercent > 0 && !preCompactionNudgeEmitted {
				fill := cw.CheckFill()
				if fill.Status == "ok" && fill.Percent >= float64(e.preWarningPercent) {
					if vulnerable := cw.VulnerableOutputs(); len(vulnerable) > 0 {
						observation += "\n\n" + formatPreCompactionNudge(fill.Percent, vulnerable)
						preCompactionNudgeEmitted = true
						e.emitter.ExecutorDiagnostic(stepNum, "pre_compaction_nudge", map[string]any{
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
			allSteps = append(allSteps, step)

			// Add step to context window
			cw.AddStep(step)
		} // end tool call loop

		// If finish was encountered, return
		if finishResult != nil {
			e.emitter.StepComplete(stepNum, time.Since(stepStartTime))
			return finishResult, nil
		}

		e.emitter.StepComplete(stepNum, time.Since(stepStartTime))

		// If circuit breaker triggered, continue to next LLM call
		if circuitBreakerTriggered {
			continue
		}

		// Wrap-up nudge: warn LLM when approaching budget limit
		// Only applies when the budget is large enough for the nudge to be meaningful.
		if effectiveMaxSteps > 3 && stepNum >= effectiveMaxSteps-3 && !wrapUpNudgeAttempted {
			wrapUpNudgeAttempted = true
			wrapUpMsg := fmt.Sprintf(executorWrapUpNudge, effectiveMaxSteps-stepNum)
			wrapUpStep := Step{
				Thought:     "",
				Observation: wrapUpMsg,
				TokensUsed:  0,
			}
			allSteps = append(allSteps, wrapUpStep)
			cw.AddStep(wrapUpStep)
			e.emitter.ExecutorDiagnostic(stepNum, "executor_wrapup_nudge", map[string]any{"remaining": effectiveMaxSteps - stepNum})
		}

		// Check for compaction using threshold-based logic
		fill := cw.CheckFill()

		// Emit context fill status
		e.emitter.ContextFill(fill.Percent, fill.Used, fill.Max, fill.Status, e.planStepID)

		switch fill.Status {
		case "compact", "warning":
			if result := cw.Compact(ctx); result != nil {
				e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
			}
			reactiveCompactAttempted = false
			preCompactionNudgeEmitted = false
		case "emergency":
			if result := cw.Compact(ctx); result != nil {
				e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
			}
			reactiveCompactAttempted = false
			preCompactionNudgeEmitted = false
		case "reject":
			if !reactiveCompactAttempted {
				reactiveCompactAttempted = true
				if result := cw.Compact(ctx); result != nil {
					e.emitter.ContextCompaction(result.BeforePercent, result.AfterPercent, e.planStepID)
				}
				preCompactionNudgeEmitted = false
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
		desc := t.Description
		if t.SourceCategory == tools.SourceCategoryMCP {
			desc = "[MCP] " + t.Description
		}
		defs = append(defs, llm.ToolDefinition{
			Name:        t.Name,
			Description: desc,
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

	toolNames := make([]string, len(defs))
	for i, d := range defs {
		toolNames[i] = d.Name
	}
	e.log().Debug("executor: tool definitions built for LLM", "count", len(defs), "tools", toolNames)

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

// formatPreCompactionNudge formats the context pressure warning message
// listing vulnerable tool outputs that will be pruned.
func formatPreCompactionNudge(fillPercent float64, vulnerable []VulnerableOutput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- CONTEXT PRESSURE WARNING ---\nContext is %.0f%% full. The following tool outputs will be pruned within the next few steps:\n", fillPercent)
	for _, v := range vulnerable {
		if v.InputHint != "" {
			fmt.Fprintf(&sb, "- %s(%q)\n", v.ToolName, v.InputHint)
		} else {
			sb.WriteString("- ")
			sb.WriteString(v.ToolName)
			sb.WriteByte('\n')
		}
	}
	sb.WriteString("If you need information from these outputs later, call store_fact NOW to preserve key findings.\n")
	sb.WriteString("After pruning, only search_facts or re-reading the source will recover this information.")
	return sb.String()
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
