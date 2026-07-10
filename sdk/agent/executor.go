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
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/tools"
)

const executorNudge = "[System] You have tools available that can help answer this request. Before finishing, try using relevant tools to discover the answer. Do NOT say you cannot determine something without first attempting to use your tools."

const executorToolCallSyntaxNudge = "[System] You printed tool-call syntax (e.g. ```bash_exec) as text in your response instead of invoking the tool through the tool_use block. This is an error — tools must be called via the tool_use mechanism, not typed as text. Re-issue your tool call correctly using the tool_use block."

// toolCallSyntaxRe matches a fenced code block at the start of a line whose
// language tag looks like a sp4rk tool name (lowercase words joined by
// underscores, e.g. ```bash_exec, ```read_file, ```edit_file). When the model
// enters a failure-mode where it "prints" a tool invocation as prose instead
// of emitting a tool_use block, this pattern is a reliable signal.
var toolCallSyntaxRe = regexp.MustCompile("(?m)^\\s*```\\w+_\\w+")

// DetectToolCallSyntaxInContent reports whether content contains tool-call
// syntax printed as text — a failure-mode sign where the model writes a
// fenced code block with a tool-name language tag (e.g. ```bash_exec) instead
// of emitting a proper tool_use block. Callers use this to avoid treating
// such output as a legitimate "implicit finish".
func DetectToolCallSyntaxInContent(content string) bool {
	return toolCallSyntaxRe.MatchString(content)
}

// defaultNonCacheableTools is the set of sp4rk-provided tool names whose results
// are NOT cached. These are internal meta-tools or produce tiny outputs where
// caching adds overhead.
//
// NOTE: Tools listed here will not receive Stage‑2 hash hints either, because
// the caching step (which produces the hash) is skipped. The exclusion from
// both caching and hash hints is a deliberate dual-purpose: if a tool's results
// are not worth caching, they are also not worth offering for fragment retrieval.
//
// This set contains only tools that sp4rk itself provides. Consumers that
// register additional non-cacheable tools (e.g. application-layer meta-tools
// like delegate, declare_plan, reflect) should add them via
// Executor.AddNonCacheableTools.
var defaultNonCacheableTools = map[string]struct{}{
	"tool_result_read":  {},
	"finish":            {},
	"read_step_output":  {},
	"list_step_outputs": {},
	"read_final_result": {},
	"store_fact":        {},
	"search_facts":      {},
	"update_checklist":  {},
	tools.ToolBatch:     {},
}

// executorWrapUpNudge is injected when the agent approaches the step budget
// without evidence of recent productive mutations. It recommends wrapping up
// but — unlike a hard "finish NOW" imperative — leaves the door open to keep
// working: the agent may continue, in which case the user is asked (via
// OnStepLimit) to grant additional iterations when the limit is reached.
const executorWrapUpNudge = "[System] You are running low on tool call iterations. You have %d iteration(s) remaining. Prioritize wrapping up your work: summarize your findings and call finish, and do not start new explorations. However, if your task is still in progress and you genuinely need more iterations to complete it, you may continue working instead of finishing — the user will be asked to grant additional iterations if you exceed the limit."

// executorWrapUpNudgeActive is injected when the agent approaches the step
// budget but its recent steps show active progress (successful mutating tool
// calls). Instead of pressuring the agent to finish prematurely, it encourages
// continuing so the work in progress can be completed. This preserves the path
// to OnStepLimit: if the agent keeps working past the budget, the user is asked
// whether to extend it, rather than the agent being nudged to abandon the task.
const executorWrapUpNudgeActive = "[System] You are running low on tool call iterations. You have %d iteration(s) remaining, but your recent steps show active progress (successful file changes). Continue completing the work in progress rather than abandoning it. If you need more iterations beyond the limit, keep working — the user will be asked to grant additional iterations when you reach the limit, and you can call finish once the task is done. Do not finish prematurely."

// Trade-off note (intentional): softening the wrap-up nudges above — replacing
// a hard "finish NOW" imperative with a recommendation that leaves the door
// open to keep working — deliberately shifts the final enforcement of the step
// budget onto OnStepLimit. As a consequence, for tasks with active mutations
// the agent is more likely to continue past the budget, which raises the
// frequency at which the user is prompted (via OnStepLimit) to grant
// additional iterations. This is an accepted trade-off: it avoids prematurely
// aborting productive work at the cost of more step-limit prompts. OnStepLimit
// remains the hard stop — AutoApprove governs only tool-call confirmations,
// not the step limit — so runaway execution is bounded by the user's choice at
// the limit prompt.

const executorFinishNudge = "[System] You must call the finish tool to complete your task. Simply responding with text does not count as completion. Call the finish tool now with your final answer."

// executorMutationNudge is injected when a step flagged as requiring mutations
// (e.g. a coder step with domain=code) calls finish without having executed any
// mutating tool. This catches the "false success" pattern where the agent reads
// extensively but finishes without making the required code changes.
const executorMutationNudge = "[System] You are finishing a step that requires code modifications, but you have not made any file changes (no write_file, edit_file, create_directory, delete_file, or delete_directory calls). If the task genuinely requires no changes, explain why explicitly in your finish answer. Otherwise, make the required changes NOW before calling finish."

// executorChecklistMissingNudge is injected when a non-trivial step finishes
// without ever calling update_checklist. The checklist tracks sub-tasks within
// a step and gives the user visibility into progress; skipping it on
// non-trivial steps is treated as an incomplete workflow.
const executorChecklistMissingNudge = "[System] You are finishing a non-trivial step without having called update_checklist. A checklist tracks the sub-tasks of your current step and gives the user visibility into progress. Call update_checklist now with the sub-tasks for this step (mark completed ones as '- [x]'), then continue. If this step is genuinely trivial, explain why in your finish answer."

// executorChecklistUncheckedNudge is injected when the last checklist has
// unchecked items at finish time. The agent must either complete them or
// explicitly justify skipping them.
const executorChecklistUncheckedNudge = "[System] Your last checklist has %d unchecked item(s). Either complete the remaining work, or call update_checklist again with all relevant items checked and explain in your finish answer why any were skipped."

// checklistDoneRe extracts the "N/M done" progress from an update_checklist
// tool result (e.g. "Checklist updated: 2/5 done" or "Checklist updated for
// step_3: 2/5 done").
var checklistDoneRe = regexp.MustCompile(`(\d+)/(\d+)\s+done`)

// checklistTrivialThreshold is the maximum number of productive tool calls
// (excluding finish and nudges) a step may have before the checklist gate
// activates. Steps at or below this threshold are considered trivial and do
// not require a checklist.
const checklistTrivialThreshold = 2

// wrapUpActiveLookback is the number of recent tool-call steps examined by the
// wrap-up nudge to detect active progress (successful mutating calls). When a
// mutation was made within this window, the active wrap-up nudge is used,
// which encourages continuation instead of pressuring the agent to finish —
// preserving the path to the step-limit confirmation (OnStepLimit).
const wrapUpActiveLookback = 5

// mutatingTools is the set of tool names that constitute a filesystem mutation.
// The mutation gate checks whether any of these were successfully executed before
// accepting finish on a step flagged as mutation-required.
var mutatingTools = map[string]struct{}{
	"write_file":       {},
	"edit_file":        {},
	"create_directory": {},
	"delete_file":      {},
	"delete_directory": {},
}

// circuitBreakerExemptTools are excluded from the fruitless-result and
// same-tool-repeat detectors. These tools legitimately produce short,
// similarly-sized successful results in bursts (e.g. batch file edits each
// returning "successfully edited file", or consecutive update_checklist /
// set_step_status confirmations). Counting them as "fruitless" or "repetitive"
// triggers false aborts during normal batch workflows.
//
// store_fact is already handled separately in checkSameToolRepetition and is
// included here for symmetry so checkFruitlessResult also skips it.
var circuitBreakerExemptTools = map[string]struct{}{
	"write_file":       {},
	"edit_file":        {},
	"create_directory": {},
	"delete_file":      {},
	"delete_directory": {},
	"update_checklist": {},
	"set_step_status":  {},
	"store_fact":       {},
}

// Circuit-breaker nudge messages. Kept as Go constants because they use fmt.Sprintf
// with runtime values that would require a template engine if moved to markdown.
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
	emitter                 Events // event emitter (uses NoopEvents if nil)
	suppressAssistantEvents bool   // if true, don't emit AssistantChunk/AssistantDone
	toolResultBudget        ToolResultBudget
	circuitBreaker          CircuitBreakerConfig
	hitl                    HITLHandler // human-in-the-loop hooks (uses NoopHITLHandler if nil)

	// Tool result caching and per-tool truncation (Stage 1).
	toolCache         *ToolResultCache
	perToolTruncation map[string]ToolTruncationConfig

	// nonCacheableTools is the set of tool names whose results are NOT cached.
	// Initialized from defaultNonCacheableTools in NewExecutor; extended via
	// AddNonCacheableTools by consumers that register additional meta-tools.
	nonCacheableTools map[string]struct{}

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

	// Mutation gate: when true, finish is rejected if no mutating tool was
	// successfully executed during this step. Prevents "false success" where
	// the agent reads extensively but finishes without making required changes.
	mutationRequired       bool
	mutationNudgeAttempted bool

	// Checklist gate: when enabled (default), a non-trivial step that finishes
	// without ever calling update_checklist, or with unchecked items in its last
	// checklist, receives a nudge before finish is accepted. Improves checklist
	// adoption and incremental updates. Can be disabled via SetChecklistGateEnabled.
	checklistGateEnabled             bool
	checklistMissingNudgeAttempted   bool
	checklistUncheckedNudgeAttempted bool

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

	// FinishGuard is an optional callback invoked before finish is accepted.
	// If it returns a non-nil error, finish is rejected with a nudge containing
	// the error message. Used by the sp4rk Conductor to prevent abandoning pending
	// async delegations.
	finishGuard func(ctx context.Context) error

	logger *slog.Logger
}

// executorOptions holds the optional configuration for an Executor. The zero
// value is a valid starting point; NewExecutor applies sensible defaults
// (NoopEvents, NoopHITLHandler, DefaultCircuitBreakerConfig) before applying
// caller-supplied options.
type executorOptions struct {
	tokenCounter            llm.TokenCounter
	emitter                 Events
	suppressAssistantEvents bool
	toolResultBudget        ToolResultBudget
	circuitBreaker          CircuitBreakerConfig
	hitl                    HITLHandler
}

// Option configures an Executor created by NewExecutor. Only types from this
// package can implement Option because apply is unexported.
type Option interface {
	apply(*executorOptions)
}

// Compile-time verification that every option type implements Option.
var (
	_ Option = tokenCounterOption{}
	_ Option = eventsOption{}
	_ Option = suppressAssistantEventsOption(false)
	_ Option = toolResultBudgetOption{}
	_ Option = circuitBreakerOption{}
	_ Option = hitlOption{}
)

type tokenCounterOption struct{ Counter llm.TokenCounter }

func (o tokenCounterOption) apply(opts *executorOptions) { opts.tokenCounter = o.Counter }

// WithTokenCounter sets the token counter used for context-fill tracking.
func WithTokenCounter(counter llm.TokenCounter) Option {
	return tokenCounterOption{Counter: counter}
}

type eventsOption struct{ Emitter Events }

func (o eventsOption) apply(opts *executorOptions) { opts.emitter = o.Emitter }

// WithEvents sets the event emitter. If nil, NewExecutor substitutes
// NoopEvents so downstream code never needs nil checks.
func WithEvents(emitter Events) Option { return eventsOption{Emitter: emitter} }

type suppressAssistantEventsOption bool

func (o suppressAssistantEventsOption) apply(opts *executorOptions) {
	opts.suppressAssistantEvents = bool(o)
}

// WithSuppressAssistantEvents disables AssistantChunk/AssistantDone events.
// Set to true for plan-step executors to avoid duplicate assistant messages
// when the orchestrator handles the final output.
func WithSuppressAssistantEvents(suppress bool) Option {
	return suppressAssistantEventsOption(suppress)
}

type toolResultBudgetOption struct{ Budget ToolResultBudget }

func (o toolResultBudgetOption) apply(opts *executorOptions) { opts.toolResultBudget = o.Budget }

// WithToolResultBudget sets the tool result truncation budget.
func WithToolResultBudget(budget ToolResultBudget) Option {
	return toolResultBudgetOption{Budget: budget}
}

type circuitBreakerOption struct{ Config CircuitBreakerConfig }

func (o circuitBreakerOption) apply(opts *executorOptions) { opts.circuitBreaker = o.Config }

// WithCircuitBreaker sets the circuit breaker thresholds. When omitted,
// NewExecutor uses DefaultCircuitBreakerConfig.
func WithCircuitBreaker(cfg CircuitBreakerConfig) Option {
	return circuitBreakerOption{Config: cfg}
}

type hitlOption struct{ Handler HITLHandler }

func (o hitlOption) apply(opts *executorOptions) { opts.hitl = o.Handler }

// WithHITL sets the human-in-the-loop handler. If nil, NewExecutor substitutes
// NoopHITLHandler.
func WithHITL(handler HITLHandler) Option { return hitlOption{Handler: handler} }

// NewExecutor creates a new Executor.
//
// llmRouter, toolRegistry, and maxSteps are required. Optional configuration
// is supplied via Option values (see WithTokenCounter, WithEvents,
// WithSuppressAssistantEvents, WithToolResultBudget, WithCircuitBreaker, and
// WithHITL). Defaults: nil emitter → NoopEvents, nil HITL → NoopHITLHandler,
// circuit breaker → DefaultCircuitBreakerConfig.
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, maxSteps int, opts ...Option) *Executor {
	o := executorOptions{
		circuitBreaker: DefaultCircuitBreakerConfig(),
	}
	for _, opt := range opts {
		opt.apply(&o)
	}
	// Use NoopEvents if nil to avoid nil checks throughout the code.
	if o.emitter == nil {
		o.emitter = &NoopEvents{}
	}
	if o.hitl == nil {
		o.hitl = &NoopHITLHandler{}
	}
	return &Executor{
		llm:                     llmRouter,
		tools:                   toolRegistry,
		tokenCounter:            o.tokenCounter,
		maxSteps:                maxSteps,
		emitter:                 o.emitter,
		suppressAssistantEvents: o.suppressAssistantEvents,
		toolResultBudget:        o.toolResultBudget,
		circuitBreaker:          o.circuitBreaker,
		hitl:                    o.hitl,
		checklistGateEnabled:    true,
		nonCacheableTools:       copyNonCacheableTools(defaultNonCacheableTools),
	}
}

// SetLogger sets the logger for the executor.
func (e *Executor) SetLogger(l *slog.Logger) { e.logger = l }

// SetReasoningEffort sets the reasoning effort for LLM calls.
func (e *Executor) SetReasoningEffort(effort string) { e.reasoningEffort = effort }

// SetMutationRequired configures the mutation gate. When true, the executor
// will not accept a finish call unless at least one mutating tool (write_file,
// edit_file, create_directory, delete_file, delete_directory) was successfully
// executed during this step. A nudge is injected on the first attempt; on the
// second attempt the step is marked as not finished (Finished: false).
func (e *Executor) SetMutationRequired(required bool) { e.mutationRequired = required }

// SetChecklistGateEnabled enables or disables the checklist gate. When enabled
// (the default), a non-trivial step that finishes without having called
// update_checklist, or whose last checklist has unchecked items, receives a
// nudge prompting the agent to maintain or complete its checklist. The gate is
// a soft nudge: after one nudge attempt, finish is accepted regardless.
func (e *Executor) SetChecklistGateEnabled(enabled bool) { e.checklistGateEnabled = enabled }

// SetPreWarningPercent sets the context fill percentage that triggers the pre-compaction
// store_fact nudge. When fill reaches this threshold (but is below the compaction trigger),
// a warning listing vulnerable tool outputs is appended to the observation.
func (e *Executor) SetPreWarningPercent(percent int) { e.preWarningPercent = percent }

// SetFinishGuard sets an optional callback invoked before finish is accepted.
// If the callback returns a non-nil error, finish is rejected with a nudge
// containing the error message. Used by the sp4rk Conductor to prevent
// abandoning pending async delegations.
func (e *Executor) SetFinishGuard(fn func(ctx context.Context) error) { e.finishGuard = fn }

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

// SetHITLHandler sets the human-in-the-loop handler for tool call interception and step limit decisions.
func (e *Executor) SetHITLHandler(h HITLHandler) {
	if h == nil {
		h = &NoopHITLHandler{}
	}
	e.hitl = h
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

// AddNonCacheableTools adds tool names to the set of tools whose results are
// not cached. This extends the sp4rk-provided defaults (see defaultNonCacheableTools)
// with consumer-specific meta-tools. Tools already in the set are no-ops.
// Must be called before Run.
func (e *Executor) AddNonCacheableTools(names ...string) {
	if e.nonCacheableTools == nil {
		e.nonCacheableTools = copyNonCacheableTools(defaultNonCacheableTools)
	}
	for _, n := range names {
		e.nonCacheableTools[n] = struct{}{}
	}
}

// copyNonCacheableTools returns a shallow copy of the given map so that each
// Executor owns an independent set that can be extended via AddNonCacheableTools
// without mutating the package-level default.
func copyNonCacheableTools(src map[string]struct{}) map[string]struct{} {
	dst := make(map[string]struct{}, len(src))
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}

// applyToolResultBudget truncates a tool result if it exceeds the budget.
// The budget is min(HardCapTokens, AvailableTokens * MaxFillFraction) with a 256-token floor.
// When truncated, a notice is appended to inform the model. If cacheHash is non-empty,
// the notice includes the hash and an instruction to use tool_result_read.
func (e *Executor) applyToolResultBudget(observation string, cw ContextManager, toolName, cacheHash string) string {
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

	// UTF-8 safe: walk back to the last valid codepoint boundary so the
	// truncated observation never ends with a split multi-byte rune.
	truncated := observation[:charLimit]
	for truncated != "" && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}

	// Generate context-aware hint based on tool name
	hint := getTruncationHint(toolName)

	// Include hash and tool_result_read instruction when available
	hashHint := ""
	if cacheHash != "" {
		hashHint = fmt.Sprintf(
			" This output was truncated by token budget. Hash: %s. "+
				"Use tool_result_read(hash=\"%s\", start_line=N, num_lines=M) to read the full cached result in fragments.",
			cacheHash, cacheHash,
		)
	}

	return truncated + fmt.Sprintf(
		"\n\n[OUTPUT TRUNCATED: showing ~%d of ~%d tokens (%.0f%%).%s %s]",
		capTokens, observationTokens, float64(capTokens)/float64(observationTokens)*100, hashHint, hint,
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
// When maxSliceHint is 0, the truncation was triggered by a byte limit
// (MaxLines was 0); the message is adjusted accordingly.
func formatFragmentationNudge(hash, toolName string, maxSliceHint int) string {
	if maxSliceHint == 0 {
		return fmt.Sprintf(
			"\n\n[This output was truncated to the configured byte limit for '%s'. "+
				"The full result is cached with hash: %s. "+
				"Use tool_result_read(hash=\"%s\", start_line=1, num_lines=N) to read fragments. "+
				"num_lines must not exceed 2000.]",
			toolName, hash, hash,
		)
	}
	return fmt.Sprintf(
		"\n\n[This output was truncated to %d lines for '%s'. "+
			"The full result is cached with hash: %s. "+
			"Use tool_result_read(hash=\"%s\", start_line=1, num_lines=N) to read fragments. "+
			"num_lines must not exceed %d.]",
		maxSliceHint, toolName, hash, hash, maxSliceHint,
	)
}

// fileBackedNudgePrefix is the prefix of nudge messages appended for file-backed
// cache entries (read_file). Used by processToolResult to extract the nudge
// before Stage 2 token-budget truncation and re-append it afterwards.
const fileBackedNudgePrefix = "\n\n[File content cached with hash:"

// formatFileBackedNudge returns a message informing the LLM that the file
// content is cached with the given hash and that additional fragments can be
// read via tool_result_read. Unlike formatFragmentationNudge, this is appended
// even when Stage 1 truncation did not fire — it serves the token-economy use
// case (LLM reads fragments on demand) rather than truncation recovery.
func formatFileBackedNudge(hash string) string {
	return fmt.Sprintf(
		"\n\n[File content cached with hash: %s. "+
			"Use tool_result_read(hash=\"%s\", start_line=N, num_lines=M) to read additional fragments.]",
		hash, hash,
	)
}

// buildCacheMeta extracts file metadata from tool input for file-based tools.
// Returns ToolCacheMeta with FilePath/FileMtime/FileSize set for file tools,
// and IsMCP set for MCP-sourced tools. For read_file, FileBacked is set to
// true so the cache entry references the file on disk instead of storing
// content in memory.
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
				// read_file uses the file on disk as its cache backing store.
				// write_file and edit_file produce new content that must be
				// cached in memory (they are mutation tools, not reads).
				if toolName == tools.ToolReadFile {
					meta.FileBacked = true
				}
			}
		}
	}

	return meta
}

// isPathWithinWorkspace checks whether the given absolute path lies within the workspace root.
// It delegates to pathutil.IsWithinPath for symlink-aware containment validation.
// On error (e.g. paths on different volumes) it fails safe by returning false.
func isPathWithinWorkspace(path, workspaceRoot string) bool {
	within, err := pathutil.IsWithinPath(workspaceRoot, path)
	if err != nil {
		return false
	}
	return within
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

	// Determine whether update_checklist is available to this executor. The
	// checklist gate only activates when the agent can actually call it.
	state.checklistAvailable = false
	for _, t := range taskTools {
		if t.Name == "update_checklist" {
			state.checklistAvailable = true
			break
		}
	}

	for state.stepNum = 1; state.unlimitedSteps || state.stepNum <= state.effectiveMaxSteps+1; state.stepNum++ {
		// Sync trajectory to the store so tools (e.g. reflect) can access it.
		if ts := TrajectoryStoreFrom(ctx); ts != nil {
			ts.Sync(state.allSteps)
		}

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

	// seen deduplicates by tool name, keeping the first occurrence. Some
	// providers (DeepSeek) reject requests with HTTP 400 "Tool names must
	// be unique." — this guards against any upstream source of duplicates.
	seen := make(map[string]struct{}, len(taskTools)+1)

	// Track if finish tool is already present
	hasFinish := false

	// Add task tools
	for _, t := range taskTools {
		if _, ok := seen[t.Name]; ok {
			continue
		}
		seen[t.Name] = struct{}{}
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
