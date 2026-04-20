package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/agent/core/prompts"
	coretools "github.com/user/agent/core/tools"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/prompt"
	tools "github.com/user/agent/sdk/tools"
)

// Plan mode template content.
const (
	planModePreamble = `You are a task planner. Decompose the user's task into a DAG (directed acyclic graph) of execution steps.

Each step should be atomic and executable by a single agent with access to tools.
Steps can depend on other steps (DependsOn) and can be parallelizable.

## Granularity

Prefer fewer, broader steps over many granular ones. Each step should represent meaningful progress, not a single tool call.

- Simple tasks (complexity 1-2): 1-2 steps
- Medium tasks (complexity 3): 2-4 steps
- Complex tasks (complexity 4-5): 3-7 steps

Limit plans to MAX-STEPS steps maximum. If a task seems to require more, combine related work into broader steps.
`

	planModeDomainAssignment = `
Domain controls how the agent's context window is compacted during long executions:

- "code" → sliding window (keeps recent file edits visible)
- "research" → summarization (condenses findings into key points)
- "general" → sliding window; switches to hierarchical if plan complexity ≥ 4

Choose the domain that matches the **primary activity** of the step, not its subject matter:

- A step that _reads and analyzes_ source code to produce a report is "research" (primary activity: information gathering).
- A step that _modifies_ source files or runs build/test commands is "code" (primary activity: file mutation).
- Use "general" only when a step genuinely mixes activities and cannot be split further.

**Wrong domain → wrong compaction → degraded context quality.** A research step with domain "code" will lose synthesized findings to sliding window eviction. A coding step with domain "research" will lose recent edits to summarization.

For each step:
1. Identify the primary activity (reading/analyzing vs modifying files vs mixed)
2. Match to the domain that fits the primary activity
3. Prefer a specific domain ("code" or "research") over "general" when the activity is clear
`

	planModeAgentProfiles = `
Assign specialized profiles when it adds clear value. Omit profile for simple tasks.

Tool Priority for ALL profiles:
1. codebase-memory-mcp tools (search_graph, trace_path, get_code_snippet) + semantic_search — ALWAYS use first for code exploration
2. ripgrep, glob — ONLY for exact text/pattern matching
3. read_file, write_file, edit_file — for reading and modifying specific files
4. bash_exec — fallback for build/run/test commands

Profiles:
- "researcher": information gathering, analysis (primary: search_graph, trace_path, get_code_snippet, semantic_search; secondary: ripgrep, glob, read_file, list_directory; web: web_search, web_fetch)
- "coder": implementation, file operations (primary: search_graph, semantic_search, read_file, write_file, edit_file; secondary: ripgrep, glob, list_directory; bash_exec for build/run/test)
- "tester": test execution, verification (primary: bash_exec for test runs; discovery: search_graph, semantic_search, ripgrep, glob, read_file)
- "executor": general purpose (default, all tools — follow tool priority order above)`

	planModeExtraSections = `
## Step Description Format

Each step MUST include two text fields:

- **summary**: A condensed label of 5-7 words STRICTLY. Used only for UI display. Must capture the essence of the step. MUST NOT be empty.
- **description**: A detailed specification following the What-How-Where format:
  - What: What needs to be done in this step.
  - How: The approach, techniques, patterns, or algorithms to use.
  - Where: Specific files, functions, modules, or components involved.
  - Acceptance Criteria: Concrete, verifiable conditions that must be satisfied for this step to be considered complete. Each criterion should be testable.

Example:
  "summary": "Add JWT auth middleware",
  "description": "What: Implement JWT-based authentication middleware for all protected API endpoints.\nHow: Create a middleware function that extracts and validates JWT tokens from the Authorization header using the existing auth package. Use RS256 signature verification.\nWhere: backend/middleware/auth.go (new file), backend/routes/api.go (wire middleware)\nAcceptance Criteria:\n- All protected endpoints return 401 for missing or invalid tokens\n- Valid tokens allow request processing with user context\n- Token expiration is properly handled with appropriate error messages"

## Output Expectations

- "researcher" / "tester": Pass all results through the finish tool. Write files ONLY for final deliverables.
- "coder": Write code/config files as needed. Summarize what was done through finish.
- "executor": Files only when the file IS the deliverable.

## Parallelization

Steps are parallelizable when they have NO data dependencies — step B can run in parallel with step A only if B does not need A's output. If B needs A's output, B MUST list A in depends_on.

## Fields

- ` + "`estimated_tools`" + `: Informational hint about likely tools. Not a constraint — the executor may use any available tool.

## Tool Priority for Step Executors

Step executors have access to codebase-memory-mcp and semantic_search tools. When writing step descriptions, direct executors to:
1. Use search_graph, trace_path, get_code_snippet, semantic_search FIRST for understanding code
2. Use ripgrep/glob ONLY for exact string/pattern matches
3. Use read_file to examine specific files after discovery
4. Use bash_exec as fallback for build/test/git commands

Do NOT write steps like "use ripgrep to find..." when the intent is conceptual code exploration — use "use search_graph/semantic_search to find..." instead.
`

	planModeTail = "REFLECTIONS\n"

	planModeJSONExample = `{"steps": [{"id": "step_1", "summary": "Implement auth middleware", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec"], "domain": "code"}}]}`
)

// Continuation mode template content.
const (
	continuationModePreamble = `You are a planning agent that creates continuation plans for follow-up requests.

A task was completed successfully, and the user has sent a follow-up message. Create a plan with ONLY new steps to address the follow-up.

## Context

Original request:
ORIGINAL-REQUEST

Completed plan (step summaries):
COMPLETED-PLAN-SUMMARY

## Instructions

1. Analyze the new user message to understand what additional work is needed.
2. Create ONLY new steps that address the follow-up request.
3. New step IDs MUST be prefixed with ` + "`continuation_`" + ` (e.g., "continuation_1", "continuation_2").
4. New steps MUST reference the terminal steps of the existing plan in their DependsOn field.
5. Keep the same granularity and style as the original plan.
6. Focus ONLY on new steps that address the follow-up request.

## Terminal Steps

The following steps are the terminal (final) steps of the completed plan. New steps should depend on these:
TERMINAL-STEPS
`

	continuationModeExtraSections = ""
	continuationModeTail          = ""
	continuationModeJSONExample   = `{"steps": [{"id": "continuation_1", "summary": "Short 5-7 word label", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec"], "domain": "code"}}]}`
)

// compile-time check: Planner implements orchestration.Planner.
var _ orchestration.Planner = (*Planner)(nil)

// defaultMaxExploreSteps is the default step budget for the planner's exploration loop.
const defaultMaxExploreSteps = 7

// Circuit breaker defaults for the planner's exploration executor.
const (
	defaultRepeatNudgeThreshold     = 3
	defaultRepeatAbortThreshold     = 5
	defaultTruncationAbortThreshold = 3
	defaultParseErrorAbortThreshold = 3
)

// Planner generates DAG execution plans for complex tasks.
type Planner struct {
	llm             LLMCaller
	logger          *slog.Logger
	modelRegistry   *llm.ModelRegistry
	model           string                  // active model name for Resolve()
	toolRegistry    *coretools.ToolRegistry // to discover available tools
	tokenCounter    llm.TokenCounter        // for context window management
	contextFactory  ContextManagerFactory   // for creating the exploration ContextManager
	callerForStep   func(cm agent.ContextManager) agent.LLMCaller // optional, for context tracker correction
	maxExploreSteps int                     // budget for exploration (default: 7)
	emitter         Emitter                 // for logging/events (optional, nil-safe)
}

// NewPlanner creates a new Planner with the given LLM caller.
func NewPlanner(caller LLMCaller) *Planner {
	return &Planner{
		llm:             caller,
		maxExploreSteps: defaultMaxExploreSteps,
	}
}

// SetLogger sets the logger for the planner. If nil, slog.Default() is used.
func (p *Planner) SetLogger(l *slog.Logger) { p.logger = l }

func (p *Planner) log() *slog.Logger {
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// SetModelRegistry sets the model registry for family resolution.
func (p *Planner) SetModelRegistry(registry *llm.ModelRegistry) {
	p.modelRegistry = registry
}

// SetModel sets the active model name for ModelRegistry.Resolve() calls.
func (p *Planner) SetModel(model string) {
	p.model = model
}

// SetToolRegistry sets the tool registry for discovering available tools.
func (p *Planner) SetToolRegistry(registry *coretools.ToolRegistry) {
	p.toolRegistry = registry
}

// SetTokenCounter sets the token counter for context window management.
func (p *Planner) SetTokenCounter(counter llm.TokenCounter) {
	p.tokenCounter = counter
}

// SetContextFactory sets the factory for creating the exploration ContextManager.
func (p *Planner) SetContextFactory(factory ContextManagerFactory) {
	p.contextFactory = factory
}

// SetCallerForStep sets a function that returns a step-local LLMCaller wired to the
// ContextManager's ContextTokenTracker. This allows API-reported token counts to
// correct predictive estimates during exploration.
func (p *Planner) SetCallerForStep(fn func(cm agent.ContextManager) agent.LLMCaller) {
	p.callerForStep = fn
}

// SetEmitter sets the emitter for logging/events.
func (p *Planner) SetEmitter(emitter Emitter) {
	p.emitter = emitter
}

// SetMaxExploreSteps sets the maximum number of exploration steps.
// If n <= 0, the default (7) is used.
func (p *Planner) SetMaxExploreSteps(n int) {
	if n > 0 {
		p.maxExploreSteps = n
	}
}

// getFamily resolves the model family, defaulting to "default" if not configured.
func (p *Planner) getFamily() string {
	if p.modelRegistry == nil {
		return "default"
	}
	meta, _ := p.modelRegistry.Resolve("")
	if meta.Family == "" {
		return "default"
	}
	return meta.Family
}

// Plan generates a DAG execution plan for the given task.
// If domain is "general" or no planner tools are available, uses a direct one-shot LLM call.
// Otherwise, runs a bounded ReAct exploration loop before producing the plan.
func (p *Planner) Plan(
	ctx context.Context,
	task string,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) (*Plan, error) {
	domain := DomainFromContext(ctx)
	plannerTools := p.getPlannerTools()

	if domain == "general" || len(plannerTools) == 0 {
		p.log().Debug("planner: using direct planning", "domain", domain, "planner_tools", len(plannerTools))
		return p.planDirect(ctx, task, availableTools, reflections)
	}

	p.log().Debug("planner: using informed exploration planning", "domain", domain, "planner_tools", len(plannerTools))
	return p.planWithExploration(ctx, task, availableTools, reflections, plannerTools)
}

// planDirect performs a one-shot LLM plan generation (original behavior).
// Used for "general" domain or when no exploration tools are available.
func (p *Planner) planDirect(
	ctx context.Context,
	task string,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) (*Plan, error) {
	systemPrompt := p.buildPlanSystemPrompt(ctx, availableTools, reflections)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plan response: %w", err)
	}

	return plan, nil
}

// emitService is a nil-safe helper that emits a ServiceWithMeta event if the emitter is set.
func (p *Planner) emitService(content string, meta map[string]any) {
	if p.emitter != nil {
		p.emitter.ServiceWithMeta(content, meta)
	}
}

// planWithExploration runs a bounded ReAct exploration loop, then extracts the plan
// from the executor's finish output.
func (p *Planner) planWithExploration(
	ctx context.Context,
	task string,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
	plannerTools []tools.ToolDescriptor,
) (*Plan, error) {
	// Build the informed planner system prompt
	systemPrompt := p.buildInformedPlanSystemPrompt(ctx, availableTools, reflections)

	// Resolve model metadata for context window management
	var modelMeta llm.ModelMetadata
	if p.modelRegistry != nil {
		modelMeta, _ = p.modelRegistry.Resolve(p.model)
	}
	if modelMeta.ContextWindow == 0 {
		// Sensible defaults if registry is not available or model unknown
		modelMeta.ContextWindow = 200000
		modelMeta.OutputLimit = 16384
		modelMeta.TokenizerType = "approximate"
	}

	// Create a ContextManager for the exploration loop
	if p.contextFactory == nil {
		// Fall back to direct planning if no context factory is available
		p.log().Warn("planner: contextFactory is nil, falling back to direct planning")
		return p.planDirect(ctx, task, availableTools, reflections)
	}
	cm := p.contextFactory(systemPrompt, modelMeta, "sliding")

	// Set the task in the context manager
	if setter, ok := cm.(interface{ SetTask(string) }); ok {
		setter.SetTask(task)
	}

	// Resolve token counter
	tokenCounter := p.tokenCounter
	if tokenCounter == nil {
		tokenCounter = llm.NewSimpleTokenCounter()
	}

	// Resolve emitter for the internal executor
	var executorEmitter agent.AgentEvents
	if p.emitter != nil {
		executorEmitter = p.emitter
	}

	// Resolve the LLM caller for exploration — use callerForStep if available
	// to wire the ContextTokenTracker so API-reported tokens correct predictions.
	execCaller := p.llm
	if p.callerForStep != nil {
		execCaller = p.callerForStep(cm)
	}

	// Create the internal Executor for exploration
	exec := agent.NewExecutor(
		execCaller,
		p.toolRegistry, // ToolExecutor — core ToolRegistry implements this
		tokenCounter,
		p.maxExploreSteps,
		executorEmitter,
		true, // suppressAssistantEvents — no streaming to frontend
		ToolResultBudget{HardCapTokens: 30000, MaxFillFraction: 0.4},
		CircuitBreakerConfig{
			RepeatNudgeThreshold:     defaultRepeatNudgeThreshold,
			RepeatAbortThreshold:     defaultRepeatAbortThreshold,
			TruncationAbortThreshold: defaultTruncationAbortThreshold,
			ParseErrorAbortThreshold: defaultParseErrorAbortThreshold,
		},
	)

	// Run the exploration loop
	p.emitService("Exploring codebase...", map[string]any{"phase": "planning"})
	result, err := exec.Run(ctx, plannerTools, cm)
	if err != nil {
		// Preserve cancellation semantics
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Degrade gracefully for other executor failures
		p.log().Warn("planner: exploration executor failed, falling back to direct planning", "error", err)
		return p.planDirect(ctx, task, availableTools, reflections)
	}

	// Parse the plan from the executor's output
	p.emitService("Generating plan...", map[string]any{"phase": "planning"})
	if result.Output == "" {
		// Exploration exhausted budget without producing a plan — fall back to direct
		p.log().Warn("planner: exploration produced no output, falling back to direct planning")
		return p.planDirect(ctx, task, availableTools, reflections)
	}

	plan, err := p.parsePlanResponse(result.Output)
	if err != nil {
		// If parsing fails, the LLM might have returned free text — fall back
		p.log().Warn("planner: failed to parse exploration plan output, falling back to direct", "error", err)
		return p.planDirect(ctx, task, availableTools, reflections)
	}

	plan.ExplorationContext = summarizeExplorationSteps(result.Steps)
	p.emitService("Plan ready", map[string]any{"phase": "planning", "step_count": len(plan.Steps)})
	return plan, nil
}

// ---------------------------------------------------------------------------
// Planner tool filtering
// ---------------------------------------------------------------------------

// fsToolNames is the set of well-known file-system tool names included for the planner.
var fsToolNames = map[string]bool{
	"list_directory":  true,
	"glob":            true,
	"ripgrep":         true,
	"read_file":       true,
	"search_files":    true,
	"search_content":  true,
	"semantic_search": true,
	"batch":           true,
}

// getPlannerTools assembles the two-tier tool set for the planner.
// Returns nil if toolRegistry is nil.
func (p *Planner) getPlannerTools() []tools.ToolDescriptor {
	if p.toolRegistry == nil {
		return nil
	}

	allTools := p.toolRegistry.List()
	var result []tools.ToolDescriptor

	for _, t := range allTools {
		// Tier 1: codebase-memory MCP tools
		if strings.HasPrefix(t.Source, "codebase-memory") {
			result = append(result, t)
			continue
		}
		// Tier 2: well-known FS tools (core)
		if fsToolNames[t.Name] {
			result = append(result, t)
		}
	}

	return result
}

// hasCodebaseMemoryTools checks if codebase-memory-mcp tools are registered.
func (p *Planner) hasCodebaseMemoryTools() bool {
	if p.toolRegistry == nil {
		return false
	}
	for _, t := range p.toolRegistry.List() {
		if strings.HasPrefix(t.Source, "codebase-memory") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Informed planner system prompt
// ---------------------------------------------------------------------------

// buildInformedPlanSystemPrompt constructs the system prompt for the informed planning exploration.
func (p *Planner) buildInformedPlanSystemPrompt(
	ctx context.Context,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) string {
	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Build reflections string
	var reflectionsStr string
	if len(reflections) > 0 {
		var rb strings.Builder
		rb.WriteString("Reflections from past attempts (learn from them):\n")
		for i, r := range reflections {
			fmt.Fprintf(&rb, "%d. Failure: %s | Root cause: %s | Action plan: %s\n",
				i+1, r.FailureAnalysis, r.RootCause, r.ActionPlan)
		}
		reflectionsStr = rb.String()
	}

	// Resolve MODE-TAIL
	resolvedTail := strings.ReplaceAll(planModeTail, "REFLECTIONS", reflectionsStr)

	substitutions := map[string]string{
		"MODE-PREAMBLE":       planModePreamble,
		"DOMAIN-ASSIGNMENT":   planModeDomainAssignment,
		"AGENT-PROFILES":      planModeAgentProfiles,
		"MODE-EXTRA-SECTIONS": planModeExtraSections,
		"MODE-TAIL":           resolvedTail,
		"MODE-JSON-EXAMPLE":   planModeJSONExample,
		"AVAILABLE-TOOLS":     availableToolsStr,
		"WORKSPACE-PATH":      formatWorkspacePath(ctx),
		"MAX-STEPS":           "10",
	}

	result := prompt.NewBuilder().
		Core(prompts.PlannerInformed).
		Core(prompts.FamilyPrompt("planner", p.getFamily())).
		Core(prompts.VerificationMandate).
		ReplaceAll(substitutions).
		Build()

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	// Append auto-RAG hints when available.
	result += formatVectorSearchHints(ctx)

	return result
}

// Replan generates an updated plan after a step failure.
func (p *Planner) Replan(
	ctx context.Context,
	originalPlan *Plan,
	completedSteps []CompletedStep,
	failedStep CompletedStep,
	reflection *Reflection,
	sessionReflections []Reflection,
) (*Plan, error) {
	p.emitService("Refining plan...", map[string]any{"phase": "planning"})
	systemPrompt := p.buildReplanSystemPrompt(ctx, replanContext{
		originalPlan:       originalPlan,
		completedSteps:     completedSteps,
		failedStep:         failedStep,
		reflection:         reflection,
		sessionReflections: sessionReflections,
	})

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "Please provide the updated plan."},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner replan LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse replan response: %w", err)
	}

	p.emitService("Plan refined", map[string]any{"phase": "planning", "step_count": len(plan.Steps)})
	return plan, nil
}

// PlanContinuation generates a continuation plan for follow-up requests after task completion.
func (p *Planner) PlanContinuation(
	ctx context.Context,
	originalRequest string,
	existingPlan *Plan,
	completedSteps []CompletedStep,
	newMessage string,
	availableTools []tools.ToolDescriptor,
) (*Plan, error) {
	systemPrompt := p.buildContinuationSystemPrompt(ctx, originalRequest, existingPlan, completedSteps, availableTools)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: newMessage},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner continuation LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse continuation plan: %w", err)
	}

	return plan, nil
}

// buildPlanSystemPrompt constructs the system prompt for initial planning.
func (p *Planner) buildPlanSystemPrompt(
	ctx context.Context,
	availableTools []tools.ToolDescriptor,
	reflections []Reflection,
) string {
	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Build reflections string
	var reflectionsStr string
	if len(reflections) > 0 {
		var rb strings.Builder
		rb.WriteString("Reflections from past attempts (learn from them):\n")
		for i, r := range reflections {
			fmt.Fprintf(&rb, "%d. Failure: %s | Root cause: %s | Action plan: %s\n",
				i+1, r.FailureAnalysis, r.RootCause, r.ActionPlan)
		}
		reflectionsStr = rb.String()
	}

	// Resolve MODE-TAIL: it contains the REFLECTIONS placeholder, so pre-substitute
	// to avoid order-dependent substitution issues in the builder.
	resolvedTail := strings.ReplaceAll(planModeTail, "REFLECTIONS", reflectionsStr)

	// Build substitutions for template placeholders
	substitutions := map[string]string{
		"MODE-PREAMBLE":       planModePreamble,
		"DOMAIN-ASSIGNMENT":   planModeDomainAssignment,
		"AGENT-PROFILES":      planModeAgentProfiles,
		"MODE-EXTRA-SECTIONS": planModeExtraSections,
		"MODE-TAIL":           resolvedTail,
		"MODE-JSON-EXAMPLE":   planModeJSONExample,
		"AVAILABLE-TOOLS":     availableToolsStr,
		"WORKSPACE-PATH":      formatWorkspacePath(ctx),
		"MAX-STEPS":           "10",
	}

	// Use prompt builder with family-specific adapters
	result := prompt.NewBuilder().
		Core(prompts.PlannerBase).
		Core(prompts.FamilyPrompt("planner", p.getFamily())).
		Core(prompts.VerificationMandate).
		ReplaceAll(substitutions).
		Build()

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	// Append auto-RAG hints when available.
	result += formatVectorSearchHints(ctx)

	return result
}

// replanContext groups the parameters needed for replan prompt construction.
type replanContext struct {
	originalPlan       *Plan
	completedSteps     []CompletedStep
	failedStep         CompletedStep
	reflection         *Reflection
	sessionReflections []Reflection
}

// buildReplanSystemPrompt constructs the system prompt for replanning after failure.
func (p *Planner) buildReplanSystemPrompt(
	ctx context.Context,
	rc replanContext,
) string {
	// Build original plan string
	var originalPlanStr string
	planJSON, err := json.MarshalIndent(rc.originalPlan, "", "  ")
	if err != nil {
		// Fallback to Go's default formatting if JSON marshaling fails
		originalPlanStr = fmt.Sprintf("%+v", rc.originalPlan)
	} else {
		originalPlanStr = string(planJSON)
	}

	// Build completed steps string
	var completedBuilder strings.Builder
	for _, cs := range rc.completedSteps {
		fmt.Fprintf(&completedBuilder, "- %s: %s\n", cs.StepID, cs.Output)
	}
	completedStepsStr := completedBuilder.String()

	// Build failed step string
	var failedStepBuilder strings.Builder
	failedStepBuilder.WriteString(rc.failedStep.StepID + "\n")
	if rc.failedStep.Error != nil {
		failedStepBuilder.WriteString("Error: " + rc.failedStep.Error.Error() + "\n")
	}
	failedStepBuilder.WriteString("Output: " + rc.failedStep.Output)
	failedStepStr := failedStepBuilder.String()

	// Build reflection string
	var reflectionStr string
	if rc.reflection != nil {
		reflectionStr = fmt.Sprintf(`Reflection on failure:
- Failure analysis: %s
- Root cause: %s
- Action plan: %s
`, rc.reflection.FailureAnalysis, rc.reflection.RootCause, rc.reflection.ActionPlan)
	}

	// Build previous session reflections string (cross-attempt pattern visibility)
	var prevReflectionsStr string
	if len(rc.sessionReflections) > 0 {
		var prb strings.Builder
		prb.WriteString("Previous session reflections (showing cross-attempt failure patterns):\n")
		for i, r := range rc.sessionReflections {
			fmt.Fprintf(&prb, "%d. Summary: %s | Root cause: %s | Action plan: %s | Suggested: %s\n",
				i+1, r.Summary, r.RootCause, r.ActionPlan, r.SuggestedAction)
		}
		prevReflectionsStr = prb.String()
	}

	// Build substitutions for template placeholders.
	// PREVIOUS-SESSION-REFLECTIONS must be replaced before CURRENT-REFLECTION
	// to avoid substring collision, but the builder handles all substitutions
	// at once, so we use the full placeholder names which are distinct.
	substitutions := map[string]string{
		"ORIGINAL-PLAN":                originalPlanStr,
		"COMPLETED-STEPS":              completedStepsStr,
		"FAILED-STEP":                  failedStepStr,
		"PREVIOUS-SESSION-REFLECTIONS": prevReflectionsStr,
		"CURRENT-REFLECTION":           reflectionStr,
		"WORKSPACE-PATH":               formatWorkspacePath(ctx),
	}

	// Use prompt builder with family-specific adapters
	result := prompt.NewBuilder().
		Core(prompts.PlannerReplan).
		Core(prompts.FamilyPrompt("planner", p.getFamily())).
		ReplaceAll(substitutions).
		Build()

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// buildContinuationSystemPrompt constructs the system prompt for continuation planning.
func (p *Planner) buildContinuationSystemPrompt(
	ctx context.Context,
	originalRequest string,
	existingPlan *Plan,
	completedSteps []CompletedStep,
	availableTools []tools.ToolDescriptor,
) string {
	// Build completed plan summary (step IDs + descriptions + summaries)
	var planSummaryBuilder strings.Builder
	for _, step := range existingPlan.Steps {
		// Find the completed step result for this step
		var summary string
		for _, cs := range completedSteps {
			if cs.StepID == step.ID {
				if cs.Output != "" {
					summary = cs.Output
				}
				break
			}
		}
		fmt.Fprintf(&planSummaryBuilder, "- %s: %s", step.ID, step.Description)
		if summary != "" {
			fmt.Fprintf(&planSummaryBuilder, " → %s", summary)
		}
		planSummaryBuilder.WriteString("\n")
	}
	completedPlanSummary := planSummaryBuilder.String()

	// Find terminal steps (steps that have no dependents)
	terminalSteps := FindTerminalSteps(existingPlan)
	terminalStepsStr := strings.Join(terminalSteps, ", ")

	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Build substitutions for template placeholders
	substitutions := map[string]string{
		"MODE-PREAMBLE":          continuationModePreamble,
		"DOMAIN-ASSIGNMENT":      planModeDomainAssignment,
		"AGENT-PROFILES":         planModeAgentProfiles,
		"MODE-EXTRA-SECTIONS":    continuationModeExtraSections,
		"MODE-TAIL":              continuationModeTail,
		"MODE-JSON-EXAMPLE":      continuationModeJSONExample,
		"ORIGINAL-REQUEST":       originalRequest,
		"COMPLETED-PLAN-SUMMARY": completedPlanSummary,
		"TERMINAL-STEPS":         terminalStepsStr,
		"AVAILABLE-TOOLS":        availableToolsStr,
		"WORKSPACE-PATH":         formatWorkspacePath(ctx),
	}

	// Use prompt builder with family-specific adapters
	result := prompt.NewBuilder().
		Core(prompts.PlannerBase).
		Core(prompts.FamilyPrompt("planner", p.getFamily())).
		ReplaceAll(substitutions).
		Build()

	// Append environment context if available.
	if envBlock := tools.FormatFullEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		result += "\n\n" + envBlock
	}

	return result
}

// FindTerminalSteps returns the IDs of steps that have no dependents (terminal steps in the DAG).
func FindTerminalSteps(plan *Plan) []string {
	// Track which steps are depended on
	dependedOn := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, dep := range step.DependsOn {
			dependedOn[dep] = true
		}
	}

	// Terminal steps are those not depended on by any other step
	var terminal []string
	for _, step := range plan.Steps {
		if !dependedOn[step.ID] {
			terminal = append(terminal, step.ID)
		}
	}
	return terminal
}

// formatWorkspacePath returns the workspace instruction block if a workspace path is set.
func formatWorkspacePath(ctx context.Context) string {
	wp := tools.WorkspacePathFrom(ctx)
	if wp == "" {
		return ""
	}
	return fmt.Sprintf("Session workspace: %s\nWhen steps produce file artifacts, they must be created inside this workspace unless the task explicitly specifies an external location.", wp)
}

// formatVectorSearchHints returns a prompt section with auto-RAG file hints,
// or an empty string when no hints are available in the context.
func formatVectorSearchHints(ctx context.Context) string {
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil || len(hints.Files) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Relevant Project Files (auto-detected)\n")
	sb.WriteString("Based on the task, these files may be relevant:\n")
	for _, h := range hints.Files {
		sb.WriteString("- " + h.FilePath)
		if h.Summary != "" {
			sb.WriteString(": " + h.Summary)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// parsePlanResponse extracts a Plan from the LLM response content.
func (p *Planner) parsePlanResponse(content string) (*Plan, error) {
	// Try to find JSON in the response
	content = strings.TrimSpace(content)

	// Handle markdown code blocks
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		if idx := strings.Index(content, "```"); idx != -1 {
			content = content[:idx]
		}
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		if idx := strings.Index(content, "```"); idx != -1 {
			content = content[:idx]
		}
	}

	content = strings.TrimSpace(content)

	// Find JSON object boundaries
	startIdx := strings.Index(content, "{")
	endIdx := strings.LastIndex(content, "}")

	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return nil, errors.New("no valid JSON object found in response")
	}

	jsonContent := content[startIdx : endIdx+1]

	var plan Plan
	if err := json.Unmarshal([]byte(jsonContent), &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}
	p.log().Debug("planner: parsePlanResponse parsed", "steps", len(plan.Steps), "firstStepSummary", func() string {
		if len(plan.Steps) > 0 {
			return plan.Steps[0].Summary
		}
		return ""
	}())
	return &plan, nil
}

// summarizeExplorationSteps extracts a concise summary from exploration ReAct steps.
// It collects the planner's Thought from each step (the LLM's synthesis of tool results)
// along with the tool name for reference. The output is capped at maxExplorationContextLen
// characters to prevent bloating step task descriptions.
func summarizeExplorationSteps(steps []agent.Step) string {
	const maxExplorationContextLen = 4000

	var b strings.Builder
	for _, s := range steps {
		thought := strings.TrimSpace(s.Thought)
		if thought == "" {
			continue
		}
		toolName := s.Action.Name
		if toolName != "" {
			fmt.Fprintf(&b, "- %s (via %s)\n", thought, toolName)
		} else {
			fmt.Fprintf(&b, "- %s\n", thought)
		}
		if b.Len() >= maxExplorationContextLen {
			break
		}
	}

	result := b.String()
	if len(result) > maxExplorationContextLen {
		result = result[:maxExplorationContextLen]
		// Trim to last complete line to avoid broken output.
		if idx := strings.LastIndex(result, "\n"); idx > 0 {
			result = result[:idx+1]
		}
	}
	return result
}

// CreateSyntheticPlan creates a minimal 1-step plan without LLM calls.
// Used for simple tasks where full planning is unnecessary overhead.
func (p *Planner) CreateSyntheticPlan(task, domain string) *Plan {
	return &Plan{
		Steps: []PlanStep{
			{
				ID:             "step_1",
				Summary:        Truncate(task, 50),
				Description:    task,
				DependsOn:      []string{},
				Parallelizable: true,
				Profile: AgentProfile{
					Role:   "executor",
					Domain: domain,
				},
			},
		},
	}
}

// Truncate truncates a string to maxLen characters, adding "..." if truncated.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
