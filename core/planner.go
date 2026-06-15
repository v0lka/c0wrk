package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/skills"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/prompt"
	tools "github.com/v0lka/c0wrk/sdk/tools"
)

// systemMessagesFromPrompt splits a system prompt on CacheBreakMarker
// and returns one llm.Message per part.
func systemMessagesFromPrompt(systemPrompt string) []llm.Message {
	parts := prompt.SplitCacheBreak(systemPrompt)
	msgs := make([]llm.Message, len(parts))
	for i, p := range parts {
		msgs[i] = llm.Message{Role: "system", Content: p}
	}
	return msgs
}

// planPromptMode groups all mode-varying parts of the planner prompt template.
// Each variant (multi-step, single-step, continuation) provides its own config
// while sharing common elements (domain assignment, agent profiles, extra sections).
type planPromptMode struct {
	preamble      string
	tot           string
	guidance      string
	extraSections string
	tail          string
	jsonExample   string
	maxSteps      string
}

// Plan mode template content. The body of each prompt section lives in
// core/prompts/planner_*.md and is loaded via go:embed in core/prompts/prompts.go.
// Inlining the full text here would scatter the planning policy across Go and
// markdown sources; centralizing it next to the other prompts keeps editing
// the planner contract a one-file change.
const (
	planModeTail = "REFLECTIONS\n"

	planModeJSONExample = `{"steps": [{"id": "step_1", "summary": "Implement auth middleware", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec", "semantic_search", "search_graph"], "skills": ["go-testing"], "domain": "code", "keep_last_n": 5, "protected_tools": ["store_fact", "search_facts"]}}]}`

	// singleStepJSONExample provides the JSON format for single-step plans.
	singleStepJSONExample = `{"steps": [{"id": "step_1", "summary": "5-7 word task label", "description": "## Task Title\n### What:\nFull description of what needs to be done.\n### How:\nConcrete approach, techniques, tool usage order.\n### Where:\nSpecific files, functions, modules.\n### Acceptance Criteria:\n- Verifiable condition 1\n- Verifiable condition 2", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1", "tool2"], "profile": {"role": "executor", "domain": "code"}}]}`
)

// Continuation mode template content.
const (
	continuationModeTail        = ""
	continuationModeJSONExample = `{"steps": [{"id": "continuation_1", "summary": "Short 5-7 word label", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec", "semantic_search", "search_graph"], "skills": ["go-testing"], "domain": "code"}}]}`

	// continuationSingleStepJSONExample provides the JSON format for single-step continuations.
	continuationSingleStepJSONExample = `{"steps": [{"id": "continuation_1", "summary": "5-7 word continuation label", "description": "## Continuation Title\n### What:\nFull description of what needs to be done.\n### How:\nConcrete approach building on completed work.\n### Where:\nSpecific files, functions, modules.\n### Acceptance Criteria:\n- Verifiable condition 1\n- Verifiable condition 2", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "executor", "domain": "code"}}]}`
)

// Pre-built mode configurations used by the unified prompt builder. The
// preamble/tot/guidance/extraSections fields read from embedded prompt vars
// in core/prompts so the body is editable as plain markdown.
var (
	multiStepMode = planPromptMode{
		preamble:      prompts.PlannerPlanPreamble,
		tot:           prompts.PlannerMultiStepToT,
		guidance:      prompts.PlannerMultiStepGuidance,
		extraSections: prompts.PlannerExtraSections,
		tail:          planModeTail,
		jsonExample:   planModeJSONExample,
		maxSteps:      "10",
	}

	singleStepMode = planPromptMode{
		preamble:      prompts.PlannerSingleStepPreamble,
		tot:           prompts.PlannerSingleStepToT,
		guidance:      prompts.PlannerSingleStepGuidance,
		extraSections: prompts.PlannerExtraSections,
		tail:          planModeTail,
		jsonExample:   singleStepJSONExample,
		maxSteps:      "1",
	}

	continuationMultiMode = planPromptMode{
		preamble:      prompts.PlannerContinuationPreamble,
		tot:           prompts.PlannerMultiStepToT,
		guidance:      prompts.PlannerMultiStepGuidance,
		extraSections: prompts.PlannerExtraSections,
		tail:          continuationModeTail,
		jsonExample:   continuationModeJSONExample,
		maxSteps:      "10",
	}

	continuationSingleMode = planPromptMode{
		preamble:      prompts.PlannerContinuationSingleStep,
		tot:           prompts.PlannerSingleStepToT,
		guidance:      prompts.PlannerSingleStepGuidance,
		extraSections: prompts.PlannerExtraSections,
		tail:          continuationModeTail,
		jsonExample:   continuationSingleStepJSONExample,
		maxSteps:      "1",
	}
)

// compile-time check: Planner's public method surface is adapted to
// orchestration.Planner by plannerSDKAdapter (see orchestrator.go). The core
// planner's methods intentionally diverge from the SDK interface to thread
// availableSkills alongside availableTools.

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
	llm                 agent.LLMCaller
	logger              *slog.Logger
	modelRegistry       *llm.ModelRegistry
	model               string                                        // active model name for Resolve()
	toolRegistry        *coretools.ToolRegistry                       // to discover available tools
	tokenCounter        llm.TokenCounter                              // for context window management
	contextFactory      ContextManagerFactory                         // for creating the exploration ContextManager
	callerForStep       func(cm agent.ContextManager) agent.LLMCaller // optional, for context tracker correction
	maxExploreSteps     int                                           // budget for exploration (default: 7)
	emitter             Emitter                                       // for logging/events (optional, nil-safe)
	baseReasoningEffort llm.ReasoningEffort                           // reasoning effort for LLM calls
	roleOverrides       map[string]string                             // per-role reasoning overrides
}

// NewPlanner creates a new Planner with the given LLM caller.
func NewPlanner(caller agent.LLMCaller) *Planner {
	return &Planner{
		llm:             caller,
		maxExploreSteps: defaultMaxExploreSteps,
	}
}

// SetLogger sets the logger for the planner. If nil, slog.Default() is used.
func (p *Planner) SetLogger(l *slog.Logger) { p.logger = l }

// SetBaseReasoningEffort sets the base reasoning effort for the planner.
func (p *Planner) SetBaseReasoningEffort(effort llm.ReasoningEffort) { p.baseReasoningEffort = effort }

// SetRoleOverrides sets the per-role reasoning effort overrides.
func (p *Planner) SetRoleOverrides(overrides map[string]string) { p.roleOverrides = overrides }

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
// This intentionally resolves using the active planner model (p.model) so that
// model-family-specific prompt overlays (planner_anthropic.md, planner_openai_flagship.md,
// etc.) are applied correctly. The previous behavior of always calling Resolve(ctx, "")
// was a bug: it ignored SetModel and always fell back to the registry's default family,
// meaning model-specific prompt tuning was never activated for the planner.
func (p *Planner) getFamily(ctx context.Context) string {
	if p.modelRegistry == nil {
		return "default"
	}
	meta, _ := p.modelRegistry.Resolve(ctx, p.model)
	if meta.Family == "" {
		return "default"
	}
	return meta.Family
}

// Plan generates an execution plan for the given task.
// When singleStep is true, produces exactly 1 step (normal mode).
// When singleStep is false, produces a multi-step DAG (advanced mode).
// If domain is "general" and complexity < 4, or no planner tools are available, uses a direct one-shot LLM call.
// Otherwise, runs a bounded ReAct exploration loop before producing the plan.
// availableSkills is the router-matched skill pool for this turn; the planner may assign
// a subset per step via profile.skills. Empty = full pool reused at executor time.
func (p *Planner) Plan(
	ctx context.Context,
	task string,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
) (*orchestration.Plan, error) {
	mode := multiStepMode
	if singleStep {
		mode = singleStepMode
	}

	domain := DomainFromContext(ctx)
	plannerTools := p.getPlannerTools()

	complexity := ComplexityFromContext(ctx)
	if (domain == "general" && complexity < 4) || len(plannerTools) == 0 {
		p.log().Debug("planner: using direct planning", "domain", domain, "planner_tools", len(plannerTools), "singleStep", singleStep)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep)
	}

	p.log().Debug("planner: using informed exploration planning", "domain", domain, "planner_tools", len(plannerTools), "singleStep", singleStep)
	return p.planWithExploration(ctx, task, mode, availableTools, reflections, plannerTools, availableSkills, singleStep)
}

// planDirect performs a one-shot LLM plan generation.
// Used for "general" domain or when no exploration tools are available.
func (p *Planner) planDirect(
	ctx context.Context,
	task string,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
) (*orchestration.Plan, error) {
	systemPrompt := p.buildPlanSystemPrompt(ctx, mode, availableTools, reflections, availableSkills)

	messages := systemMessagesFromPrompt(systemPrompt)
	messages = append(messages, llm.Message{Role: "user", Content: task})

	req := llm.ChatRequest{
		Messages:        messages,
		ReasoningEffort: llm.ResolveAgentReasoningMode("planner", p.baseReasoningEffort, p.roleOverrides),
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content, availableSkills)
	if err != nil {
		return nil, fmt.Errorf("failed to parse plan response: %w", err)
	}

	// Enforce single-step constraint: if the planner returned more than 1 step
	// in single-step mode, truncate to the first step.
	if singleStep && len(plan.Steps) > 1 {
		p.log().Warn("planner: single-step mode returned multiple steps, truncating", "count", len(plan.Steps))
		plan.Steps = plan.Steps[:1]
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
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	plannerTools []tools.ToolDescriptor,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
) (*orchestration.Plan, error) {
	// Build the informed planner system prompt
	systemPrompt := p.buildInformedPlanSystemPrompt(ctx, mode, availableTools, reflections, availableSkills)

	// Resolve model metadata for context window management
	var modelMeta llm.ModelMetadata
	if p.modelRegistry != nil {
		modelMeta, _ = p.modelRegistry.Resolve(ctx, p.model)
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
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep)
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
		agent.ToolResultBudget{HardCapTokens: 30000, MaxFillFraction: 0.4},
		agent.CircuitBreakerConfig{
			RepeatNudgeThreshold:     defaultRepeatNudgeThreshold,
			RepeatAbortThreshold:     defaultRepeatAbortThreshold,
			TruncationAbortThreshold: defaultTruncationAbortThreshold,
			ParseErrorAbortThreshold: defaultParseErrorAbortThreshold,
		},
	)
	exec.SetReasoningEffort(llm.ResolveAgentReasoningMode("researcher", p.baseReasoningEffort, p.roleOverrides))

	// Run the exploration loop with a placeholder step ID so context-aware tools
	// (e.g. set_step_status) know which logical step they belong to.
	ctx = agent.WithStepID(ctx, "planner-exploration")
	p.emitService("Exploring codebase...", map[string]any{"phase": "planning"})
	result, err := exec.Run(ctx, plannerTools, cm)
	if err != nil {
		// Preserve cancellation semantics
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Degrade gracefully for other executor failures
		p.log().Warn("planner: exploration executor failed, falling back to direct planning", "error", err)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep)
	}

	// Parse the plan from the executor's output
	p.emitService("Generating plan...", map[string]any{"phase": "planning"})
	if result.Output == "" {
		// Exploration exhausted budget without producing a plan — fall back to direct
		p.log().Warn("planner: exploration produced no output, falling back to direct planning")
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep)
	}

	plan, err := p.parsePlanResponse(result.Output, availableSkills)
	if err != nil {
		// If parsing fails, the LLM might have returned free text — fall back
		p.log().Warn("planner: failed to parse exploration plan output, falling back to direct", "error", err)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep)
	}

	if singleStep && len(plan.Steps) > 1 {
		p.log().Warn("planner: single-step mode returned multiple steps from exploration, truncating", "count", len(plan.Steps))
		plan.Steps = plan.Steps[:1]
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
	ToolListDirectory:  true,
	ToolGlob:           true,
	ToolRipgrep:        true,
	ToolReadFile:       true,
	ToolSemanticSearch: true,
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
		// Well-known FS tools (core)
		if fsToolNames[t.Name] {
			result = append(result, t)
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Informed planner system prompt
// ---------------------------------------------------------------------------

// buildInformedPlanSystemPrompt constructs the system prompt for the informed planning exploration.
func (p *Planner) buildInformedPlanSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
) string {
	return p.buildSystemPromptFromMode(ctx, mode, prompts.PlannerInformed, availableTools, reflections, availableSkills, nil)
}
func (p *Planner) Replan(
	ctx context.Context,
	originalPlan *orchestration.Plan,
	completedSteps []orchestration.CompletedStep,
	failedStep orchestration.CompletedStep,
	reflection *orchestration.Reflection,
	sessionReflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
) (*orchestration.Plan, error) {
	p.emitService("Refining plan...", map[string]any{"phase": "planning"})
	systemPrompt := p.buildReplanSystemPrompt(ctx, replanContext{
		originalPlan:       originalPlan,
		completedSteps:     completedSteps,
		failedStep:         failedStep,
		reflection:         reflection,
		sessionReflections: sessionReflections,
		availableSkills:    availableSkills,
	})

	messages := systemMessagesFromPrompt(systemPrompt)
	messages = append(messages, llm.Message{Role: "user", Content: "Please provide the updated plan."})

	req := llm.ChatRequest{
		Messages:        messages,
		ReasoningEffort: llm.ResolveAgentReasoningMode("planner", p.baseReasoningEffort, p.roleOverrides),
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner replan LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content, availableSkills)
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
	existingPlan *orchestration.Plan,
	completedSteps []orchestration.CompletedStep,
	newMessage string,
	availableTools []tools.ToolDescriptor,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
) (*orchestration.Plan, error) {
	mode := continuationMultiMode
	if singleStep {
		mode = continuationSingleMode
	}

	systemPrompt := p.buildContinuationSystemPrompt(ctx, mode, originalRequest, existingPlan, completedSteps, availableTools, availableSkills)

	messages := systemMessagesFromPrompt(systemPrompt)
	messages = append(messages, llm.Message{Role: "user", Content: newMessage})

	req := llm.ChatRequest{
		Messages:        messages,
		ReasoningEffort: llm.ResolveAgentReasoningMode("planner", p.baseReasoningEffort, p.roleOverrides),
	}

	resp, err := p.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner continuation LLM call failed: %w", err)
	}

	plan, err := p.parsePlanResponse(resp.Message.Content, availableSkills)
	if err != nil {
		return nil, fmt.Errorf("failed to parse continuation plan: %w", err)
	}

	if singleStep && len(plan.Steps) > 1 {
		p.log().Warn("planner: single-step continuation returned multiple steps, truncating", "count", len(plan.Steps))
		plan.Steps = plan.Steps[:1]
	}

	return plan, nil
}

// buildSystemPromptFromMode constructs the system prompt using mode-specific configuration.
// It is the unified builder underlying all planner prompt variants (plan, informed, continuation).
// baseTemplate is the embedded markdown template (PlannerBase or PlannerInformed).
// extraSubstitutions are merged on top of mode-derived substitutions (used for continuation context).
func (p *Planner) buildSystemPromptFromMode(
	ctx context.Context,
	mode planPromptMode,
	baseTemplate string,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	extraSubstitutions map[string]string,
) string {
	// Build available tools string (grouped by priority tier)
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Resolve MODE-TAIL: it may contain the REFLECTIONS placeholder, so pre-substitute
	// to avoid order-dependent substitution issues in the builder.
	resolvedTail := mode.tail
	if strings.Contains(resolvedTail, "REFLECTIONS") {
		resolvedTail = strings.ReplaceAll(resolvedTail, "REFLECTIONS", formatPlanReflections(reflections))
	}

	substitutions := map[string]string{
		"MODE-PREAMBLE":       mode.preamble,
		"MODE-TOT":            mode.tot,
		"MODE-GUIDANCE":       mode.guidance,
		"DOMAIN-ASSIGNMENT":   prompts.PlannerDomainAssignment,
		"AGENT-PROFILES":      prompts.PlannerAgentProfiles,
		"MODE-EXTRA-SECTIONS": mode.extraSections,
		"MODE-TAIL":           resolvedTail,
		"MODE-JSON-EXAMPLE":   mode.jsonExample,
		"AVAILABLE-TOOLS":     availableToolsStr,
		"AVAILABLE-SKILLS":    formatSkillListForPlanner(ctx, availableSkills),
		"WORKSPACE-PATH":      formatWorkspacePath(ctx),
		"MAX-STEPS":           mode.maxSteps,
	}

	// Merge extra substitutions (e.g. continuation-specific placeholders)
	for k, v := range extraSubstitutions {
		substitutions[k] = v
	}

	result := prompt.NewBuilder().
		Core(baseTemplate).
		Core(prompts.FamilyPrompt("planner", p.getFamily(ctx))).
		Core(prompts.VerificationMandate).
		CacheBreak().
		ReplaceAll(substitutions).
		Build()

	return appendPlannerContextSections(ctx, result)
}

// buildPlanSystemPrompt constructs the system prompt for initial planning.
func (p *Planner) buildPlanSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
) string {
	return p.buildSystemPromptFromMode(ctx, mode, prompts.PlannerBase, availableTools, reflections, availableSkills, nil)
}

// replanContext groups the parameters needed for replan prompt construction.
type replanContext struct {
	originalPlan       *orchestration.Plan
	completedSteps     []orchestration.CompletedStep
	failedStep         orchestration.CompletedStep
	reflection         *orchestration.Reflection
	sessionReflections []orchestration.Reflection
	availableSkills    []skills.SkillDescriptor
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

	// Build substitutions for template placeholders.
	// PREVIOUS-SESSION-REFLECTIONS must be replaced before CURRENT-REFLECTION
	// to avoid substring collision, but the builder handles all substitutions
	// at once, so we use the full placeholder names which are distinct.
	substitutions := map[string]string{
		"ORIGINAL-PLAN":                originalPlanStr,
		"COMPLETED-STEPS":              completedStepsStr,
		"FAILED-STEP":                  failedStepStr,
		"PREVIOUS-SESSION-REFLECTIONS": formatSessionReflections(rc.sessionReflections),
		"CURRENT-REFLECTION":           reflectionStr,
		"AVAILABLE-SKILLS":             formatSkillListForPlanner(ctx, rc.availableSkills),
		"WORKSPACE-PATH":               formatWorkspacePath(ctx),
	}

	// Use prompt builder with family-specific adapters
	result := prompt.NewBuilder().
		Core(prompts.PlannerReplan).
		Core(prompts.FamilyPrompt("planner", p.getFamily(ctx))).
		Core(prompts.VerificationMandate).
		CacheBreak().
		ReplaceAll(substitutions).
		Build()

	return appendPlannerContextSections(ctx, result)
}

// buildContinuationSystemPrompt constructs the system prompt for continuation planning.
func (p *Planner) buildContinuationSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	originalRequest string,
	existingPlan *orchestration.Plan,
	completedSteps []orchestration.CompletedStep,
	availableTools []tools.ToolDescriptor,
	availableSkills []skills.SkillDescriptor,
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

	extraSubs := map[string]string{
		"ORIGINAL-REQUEST":       originalRequest,
		"COMPLETED-PLAN-SUMMARY": completedPlanSummary,
		"TERMINAL-STEPS":         terminalStepsStr,
	}

	return p.buildSystemPromptFromMode(ctx, mode, prompts.PlannerBase, availableTools, nil, availableSkills, extraSubs)
}

// FindTerminalSteps returns the IDs of steps that have no dependents (terminal steps in the DAG).
func FindTerminalSteps(plan *orchestration.Plan) []string {
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

// formatPlanReflections formats plan-attempt reflections as a numbered list
// using failure analysis, root cause, and action plan fields.
// Returns "" for nil/empty input.
func formatPlanReflections(reflections []orchestration.Reflection) string {
	if len(reflections) == 0 {
		return ""
	}
	var rb strings.Builder
	rb.WriteString("Reflections from past attempts (learn from them):\n")
	for i, r := range reflections {
		fmt.Fprintf(&rb, "%d. Failure: %s | Root cause: %s | Action plan: %s\n",
			i+1, r.FailureAnalysis, r.RootCause, r.ActionPlan)
	}
	return rb.String()
}

// formatSessionReflections formats cross-attempt session reflections as a numbered list
// using summary, root cause, action plan, and suggested action fields.
// Returns "" for nil/empty input.
func formatSessionReflections(reflections []orchestration.Reflection) string {
	if len(reflections) == 0 {
		return ""
	}
	var prb strings.Builder
	prb.WriteString("Previous session reflections (showing cross-attempt failure patterns):\n")
	for i, r := range reflections {
		fmt.Fprintf(&prb, "%d. Summary: %s | Root cause: %s | Action plan: %s | Suggested: %s\n",
			i+1, r.Summary, r.RootCause, r.ActionPlan, r.SuggestedAction)
	}
	return prb.String()
}

// formatWorkspacePath returns the workspace instruction block if a workspace path is set.
func formatWorkspacePath(ctx context.Context) string {
	wp := tools.WorkspacePathFrom(ctx)
	if wp == "" {
		return ""
	}
	return fmt.Sprintf("Session workspace: %s\nWhen steps produce file artifacts, they must be created inside this workspace unless the task explicitly specifies an external location.", wp)
}

// formatSkillListForPlanner formats the skill list for planner prompts,
// distinguishing user-activated (mandatory) skills from router-matched ones.
// User-activated skills are marked as mandatory and the planner is instructed
// to build the plan around their execution.
func formatSkillListForPlanner(ctx context.Context, availableSkills []skills.SkillDescriptor) string {
	if len(availableSkills) == 0 {
		return "None"
	}

	userSkills := UserSkillsFromContext(ctx)
	if len(userSkills) == 0 {
		return formatSkillList(availableSkills)
	}

	userSet := make(map[string]bool, len(userSkills))
	for _, name := range userSkills {
		userSet[name] = true
	}

	var sb strings.Builder
	sb.WriteString("The user explicitly activated the following skills. The plan MUST be built around executing these skills — they define the primary task, not optional context:\n")
	for _, s := range availableSkills {
		if userSet[s.Name] {
			sb.WriteString("- " + s.Name + " [MANDATORY]: " + s.Description + "\n")
		}
	}
	// Add any router-matched (non-mandatory) skills
	var hasOptional bool
	for _, s := range availableSkills {
		if !userSet[s.Name] {
			if !hasOptional {
				sb.WriteString("\nAdditional available skills (optional):\n")
				hasOptional = true
			}
			sb.WriteString("- " + s.Name + ": " + s.Description + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parsePlanResponse extracts a Plan from the LLM response content.
// availableSkills is the router-matched pool used to validate per-step profile.skills;
// unknown skill names are dropped (with a debug log). Pass nil to skip validation.
func (p *Planner) parsePlanResponse(content string, availableSkills []skills.SkillDescriptor) (*orchestration.Plan, error) {
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

	var plan orchestration.Plan
	if err := json.Unmarshal([]byte(jsonContent), &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}

	// Normalize per-step Profile: after json.Unmarshal, `Profile any` becomes
	// a map[string]any. Re-marshal into *AgentProfile so coreStepConfigurator's
	// type-assert at resolveAgentProfile succeeds. Validate profile.Skills against
	// the router-matched pool and drop any name the router did not surface.
	var skillAllowed map[string]bool
	if len(availableSkills) > 0 {
		skillAllowed = make(map[string]bool, len(availableSkills))
		for _, s := range availableSkills {
			skillAllowed[s.Name] = true
		}
	}
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step.Profile == nil {
			continue
		}
		profileMap, isMap := step.Profile.(map[string]any)
		if !isMap {
			// Already a concrete type (e.g. set programmatically)
			continue
		}
		raw, err := json.Marshal(profileMap)
		if err != nil {
			p.log().Debug("planner: re-marshal profile failed", "step", step.ID, "error", err)
			step.Profile = nil
			continue
		}
		var profile AgentProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			p.log().Debug("planner: decode profile failed", "step", step.ID, "error", err)
			step.Profile = nil
			continue
		}
		if len(profile.Skills) > 0 && skillAllowed != nil {
			kept := profile.Skills[:0]
			for _, name := range profile.Skills {
				if skillAllowed[name] {
					kept = append(kept, name)
				} else {
					p.log().Debug("planner: dropping unknown skill from step profile", "step", step.ID, "skill", name)
				}
			}
			profile.Skills = kept
		}
		step.Profile = &profile
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
		// Truncate to maxExplorationContextLen bytes, but avoid splitting
		// a multi-byte UTF-8 character.
		truncIdx := maxExplorationContextLen
		for truncIdx > 0 && result[truncIdx]&0xC0 == 0x80 {
			truncIdx--
		}
		result = result[:truncIdx]
		// Trim to last complete line to avoid broken output.
		if idx := strings.LastIndex(result, "\n"); idx > 0 {
			result = result[:idx+1]
		}
	}
	return result
}
