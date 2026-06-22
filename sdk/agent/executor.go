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
	"batch":             {},
}

const executorWrapUpNudge = "[System] You are running low on tool call iterations. You have %d iteration(s) remaining. Wrap up your work NOW: summarize your findings and finish. Do not start new explorations."

const executorFinishNudge = "[System] You must call the finish tool to complete your task. Simply responding with text does not count as completion. Call the finish tool now with your final answer."

// Circuit-breaker nudge messages. These are kept as Go constants (rather than
// embedded .md templates) because several use fmt.Sprintf interpolation with
// runtime values (tool name, iteration count). Moving them to markdown would
// require a template engine for a marginal readability gain.
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
//
// Concurrency: Executor.Run must NOT be called concurrently on the same instance.
// Each Executor handles a single execution at a time. The orchestrator creates
// a fresh Executor per step to enforce this.
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
	toolCache         *ToolResultCache
	perToolTruncation map[string]ToolTruncationConfig

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
	reasoningEffort string

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
func (e *Executor) SetReasoningEffort(effort string) { e.reasoningEffort = effort }

// SetPreWarningPercent sets the context fill percentage that triggers the pre-compaction
// store_fact nudge. When fill reaches this threshold (but is below the compaction trigger),
// a warning listing vulnerable tool outputs is appended to the observation.
func (e *Executor) SetPreWarningPercent(percent int) { e.preWarningPercent = percent }

// log returns the executor's logger or a discard logger if none was set.
func (e *Executor) log() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return slog.New(slog.DiscardHandler)
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
	case tools.ToolReadFile:
		return "Re-read the file with start_line/end_line to see specific sections, or use ripgrep to search for specific content."
	case tools.ToolRipgrep, tools.ToolGrep:
		return "Narrow your search pattern or add path filters to reduce results."
	case tools.ToolGlob:
		return "Use a more specific glob pattern to reduce results."
	case tools.ToolWebFetch:
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
	case tools.ToolReadFile, tools.ToolWriteFile, tools.ToolEditFile:
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

	state := &runState{effectiveMaxSteps: e.maxSteps}

	for state.stepNum = 1; state.unlimitedSteps || state.stepNum <= state.effectiveMaxSteps+1; state.stepNum++ {
		// Handle step-limit boundary
		if action := e.handleStepLimitBoundary(ctx, state, cw); action == actionReturn {
			return state.finishResult, nil //nolint:nilerr // intentional: callback error means stop, not fatal
		} else if action == actionBreak {
			break
		}

		// Emit step start
		e.emitter.StepStart(state.stepNum)
		state.stepStartTime = time.Now()

		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Call LLM with reactive compaction on context-exceeded
		resp, action, err := e.callLLMWithReactiveCompaction(ctx, state, cw, toolDefs)
		if err != nil {
			return nil, err
		}
		if action == actionContinue {
			continue
		}

		// Parse response
		thought := resp.Message.Content

		// Emit thought event
		if thought != "" || resp.Reasoning != "" {
			e.emitter.Thought(state.stepNum, thought, resp.Reasoning)
		}

		// No tool calls path
		if len(resp.Message.ToolCalls) == 0 {
			if result, act := e.handleImplicitFinish(resp, thought, state, cw, hasTools); result != nil {
				return result, nil
			} else if act == actionContinue {
				continue
			}
		}

		// Truncation detection: max_tokens with tool calls
		if resp.StopReason == "max_tokens" && len(resp.Message.ToolCalls) > 0 {
			if result, act := e.handleTruncationStopReason(ctx, resp, thought, state, cw); result != nil {
				return result, nil
			} else if act == actionContinue {
				continue
			}
		}

		// Reset truncation counter on any non-truncated response
		e.consecutiveTruncationCount = 0

		// Process tool calls
		if result, act, toolErr := e.processToolCalls(ctx, resp, thought, state, cw); toolErr != nil {
			return nil, toolErr
		} else if result != nil {
			return result, nil
		} else if act == actionContinue {
			continue
		}

		e.emitter.StepComplete(state.stepNum, time.Since(state.stepStartTime))

		// If circuit breaker triggered, continue to next LLM call
		if state.circuitBreakerTriggered {
			state.circuitBreakerTriggered = false
			continue
		}

		e.handleWrapUpNudge(state, cw)

		if compactAction, compactErr := e.handleCompactionAfterStep(ctx, cw, state); compactErr != nil {
			return nil, compactErr
		} else if compactAction == actionContinue {
			continue
		}
	}

	// Max steps reached without finish
	return &ExecutorResult{
		Output:   "",
		Steps:    state.allSteps,
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
//
// Pattern-to-provider mapping (maintained as providers evolve their error messages):
//
//	"context length exceeded"       — Anthropic
//	"maximum context length"        — Anthropic (variant)
//	"context_length_exceeded"       — Anthropic API error code
//	"too many tokens"               — OpenAI
//	"request too large"             — OpenAI (variant)
//	"input is too long"             — OpenAI / generic
//	"prompt is too long"            — OpenAI / generic
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
