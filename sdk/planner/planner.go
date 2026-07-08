// Package planner generates DAG execution plans for complex tasks, breaking
// them into ordered steps with dependency tracking.
package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/strutil"
	tools "github.com/v0lka/sp4rk/tools"
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
type planPromptMode struct {
	preamble      string
	tot           string
	guidance      string
	extraSections string
	tail          string
	jsonExample   string
	maxSteps      string
}

// Plan mode template content.
const (
	planModeTail = "REFLECTIONS\n"

	planModeJSONExample = `{"steps": [{"id": "step_1", "summary": "Implement auth middleware", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec", "semantic_search", "search_graph"], "skills": ["go-testing"], "domain": "code", "keep_last_n": 5, "protected_tools": ["store_fact", "search_facts"]}}]}`

	singleStepJSONExample = `{"steps": [{"id": "step_1", "summary": "5-7 word task label", "description": "## Task Title\n### What:\nFull description of what needs to be done.\n### How:\nConcrete approach, techniques, tool usage order.\n### Where:\nSpecific files, functions, modules.\n### Acceptance Criteria:\n- Verifiable condition 1\n- Verifiable condition 2", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1", "tool2"], "profile": {"role": "executor", "domain": "code"}}]}`

	continuationModeTail        = ""
	continuationModeJSONExample = `{"steps": [{"id": "continuation_1", "summary": "Short 5-7 word label", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "coder", "allowed_tools": ["read_file", "write_file", "edit_file", "list_directory", "ripgrep", "glob", "bash_exec", "semantic_search", "search_graph"], "skills": ["go-testing"], "domain": "code"}}]}`

	continuationSingleStepJSONExample = `{"steps": [{"id": "continuation_1", "summary": "5-7 word continuation label", "description": "## Continuation Title\n### What:\nFull description of what needs to be done.\n### How:\nConcrete approach building on completed work.\n### Where:\nSpecific files, functions, modules.\n### Acceptance Criteria:\n- Verifiable condition 1\n- Verifiable condition 2", "depends_on": ["TERMINAL-STEP-IDS"], "parallelizable": true, "estimated_tools": ["tool1"], "profile": {"role": "executor", "domain": "code"}}]}`
)

// Circuit breaker defaults for the planner's exploration executor.
const (
	defaultRepeatNudgeThreshold     = 3
	defaultRepeatAbortThreshold     = 5
	defaultTruncationAbortThreshold = 3
	defaultParseErrorAbortThreshold = 3
)

// parsePlanMaxRetries is the maximum number of retries when the LLM produces
// a plan response that cannot be parsed as valid JSON.
const parsePlanMaxRetries = 5

// defaultMaxExploreSteps is the default step budget for the planner's exploration loop.
const defaultMaxExploreSteps = 7

// Planner generates DAG execution plans for complex tasks.
type Planner struct {
	llm agent.LLMCaller
	Cfg Config // public so the builder can wire dependencies after creation
}

// Compile-time check: Planner implements orchestration.Planner.
var _ orchestration.Planner = (*Planner)(nil)

// NewPlanner creates a new Planner with the given LLM caller and configuration.
// Returns an error if caller is nil (required dependency).
func NewPlanner(caller agent.LLMCaller, cfg Config) (*Planner, error) {
	if caller == nil {
		return nil, errors.New("planner: caller is required")
	}
	if cfg.MaxExploreSteps <= 0 {
		cfg.MaxExploreSteps = defaultMaxExploreSteps
	}
	return &Planner{
		llm: caller,
		Cfg: cfg,
	}, nil
}

func (p *Planner) log() *slog.Logger {
	if p.Cfg.Logger != nil {
		return p.Cfg.Logger
	}
	return slog.Default()
}

// getFamily resolves the model family, defaulting to "default" if not configured.
func (p *Planner) getFamily(ctx context.Context) string {
	if p.Cfg.ModelRegistry == nil {
		return "default"
	}
	meta, _ := p.Cfg.ModelRegistry.Resolve(ctx, p.Cfg.Model)
	if meta.Family == "" {
		return "default"
	}
	return meta.Family
}

// multiStepMode builds the multi-step mode configuration from the PromptSet.
func (p *Planner) multiStepMode() planPromptMode {
	return planPromptMode{
		preamble:      p.Cfg.Prompts.PlanPreamble,
		tot:           p.Cfg.Prompts.MultiStepToT,
		guidance:      p.Cfg.Prompts.MultiStepGuidance,
		extraSections: p.Cfg.Prompts.ExtraSections,
		tail:          planModeTail,
		jsonExample:   planModeJSONExample,
		maxSteps:      "10",
	}
}

// singleStepMode builds the single-step mode configuration from the PromptSet.
func (p *Planner) singleStepMode() planPromptMode {
	return planPromptMode{
		preamble:      p.Cfg.Prompts.SingleStepPreamble,
		tot:           p.Cfg.Prompts.SingleStepToT,
		guidance:      p.Cfg.Prompts.SingleStepGuidance,
		extraSections: p.Cfg.Prompts.ExtraSections,
		tail:          planModeTail,
		jsonExample:   singleStepJSONExample,
		maxSteps:      "1",
	}
}

// continuationMultiMode builds the continuation multi-step mode config.
func (p *Planner) continuationMultiMode() planPromptMode {
	return planPromptMode{
		preamble:      p.Cfg.Prompts.ContinuationPreamble,
		tot:           p.Cfg.Prompts.MultiStepToT,
		guidance:      p.Cfg.Prompts.MultiStepGuidance,
		extraSections: p.Cfg.Prompts.ExtraSections,
		tail:          continuationModeTail,
		jsonExample:   continuationModeJSONExample,
		maxSteps:      "10",
	}
}

// continuationSingleMode builds the continuation single-step mode config.
func (p *Planner) continuationSingleMode() planPromptMode {
	return planPromptMode{
		preamble:      p.Cfg.Prompts.ContinuationSingleStep,
		tot:           p.Cfg.Prompts.SingleStepToT,
		guidance:      p.Cfg.Prompts.SingleStepGuidance,
		extraSections: p.Cfg.Prompts.ExtraSections,
		tail:          continuationModeTail,
		jsonExample:   continuationSingleStepJSONExample,
		maxSteps:      "1",
	}
}

// emitService is a nil-safe helper that emits a ServiceWithMeta event if the emitter is set.
func (p *Planner) emitService(content string, meta map[string]any) {
	if p.Cfg.Emitter != nil {
		p.Cfg.Emitter.ServiceWithMeta(content, meta)
	}
}

// ---------------------------------------------------------------------------
// Public plan methods
// ---------------------------------------------------------------------------

// Plan generates an execution plan for the given task.
// conversationHistory carries prior user/assistant exchanges so that plans for
// follow-up requests (e.g. the first message after a backend restart) are
// generated with full dialogue context. It may be nil for genuinely first
// messages.
func (p *Planner) Plan(
	ctx context.Context,
	task string,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
	conversationHistory []llm.Message,
) (*orchestration.Plan, error) {
	mode := p.multiStepMode()
	if singleStep {
		mode = p.singleStepMode()
	}

	domain := p.Cfg.DomainFromContext(ctx)
	plannerTools := p.getPlannerTools()

	complexity := p.Cfg.ComplexityFromContext(ctx)
	if (domain == "general" && complexity < 4) || len(plannerTools) == 0 {
		p.log().Debug("planner: using direct planning", "domain", domain, "planner_tools", len(plannerTools), "singleStep", singleStep)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep, conversationHistory)
	}

	p.log().Debug("planner: using informed exploration planning", "domain", domain, "planner_tools", len(plannerTools), "singleStep", singleStep)
	return p.planWithExploration(ctx, task, mode, availableTools, reflections, plannerTools, availableSkills, singleStep, conversationHistory)
}

// Replan generates a revised plan after a step failure.
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

	plan, err := p.callAndParsePlan(ctx, messages, availableSkills)
	if err != nil {
		return nil, err
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
	conversationHistory []llm.Message,
	taskComplete bool,
) (*orchestration.Plan, error) {
	if existingPlan == nil {
		return nil, errors.New("planner: PlanContinuation requires a non-nil existing plan")
	}
	mode := p.continuationMultiMode()
	if singleStep {
		mode = p.continuationSingleMode()
	}
	// When the task was not fully completed, use the incomplete-continuation
	// preamble that instructs the planner to finish only remaining work and
	// not re-execute already-completed steps.
	if !taskComplete && p.Cfg.Prompts.ContinuationIncompletePreamble != "" {
		mode.preamble = p.Cfg.Prompts.ContinuationIncompletePreamble
	}

	systemPrompt := p.buildContinuationSystemPrompt(ctx, mode, originalRequest, existingPlan, completedSteps, availableTools, availableSkills, conversationHistory)

	messages := systemMessagesFromPrompt(systemPrompt)
	messages = append(messages, llm.Message{Role: "user", Content: newMessage})

	plan, err := p.callAndParsePlan(ctx, messages, availableSkills)
	if err != nil {
		return nil, err
	}

	if singleStep && len(plan.Steps) > 1 {
		p.log().Warn("planner: single-step continuation returned multiple steps, truncating", "count", len(plan.Steps))
		plan.Steps = plan.Steps[:1]
	}

	return plan, nil
}

// ---------------------------------------------------------------------------
// Direct planning (one-shot LLM call)
// ---------------------------------------------------------------------------

func (p *Planner) planDirect(
	ctx context.Context,
	task string,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
	conversationHistory []llm.Message,
) (*orchestration.Plan, error) {
	systemPrompt := p.buildPlanSystemPrompt(ctx, mode, availableTools, reflections, availableSkills, conversationHistory)

	messages := systemMessagesFromPrompt(systemPrompt)
	messages = append(messages, llm.Message{Role: "user", Content: task})

	plan, err := p.callAndParsePlan(ctx, messages, availableSkills)
	if err != nil {
		return nil, err
	}

	if singleStep && len(plan.Steps) > 1 {
		p.log().Warn("planner: single-step mode returned multiple steps, truncating", "count", len(plan.Steps))
		plan.Steps = plan.Steps[:1]
	}

	return plan, nil
}

// ---------------------------------------------------------------------------
// Planner tool filtering
// ---------------------------------------------------------------------------

// getPlannerTools assembles the two-tier tool set for the planner.
// Returns nil if ToolRegistry is nil.
func (p *Planner) getPlannerTools() []tools.ToolDescriptor {
	if p.Cfg.ToolRegistry == nil || len(p.Cfg.PlannerToolNames) == 0 {
		return nil
	}

	allTools := p.Cfg.ToolRegistry.List()
	var result []tools.ToolDescriptor

	for _, t := range allTools {
		if p.Cfg.PlannerToolNames[t.Name] {
			result = append(result, t)
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Informed planner system prompt (exploration loop)
// ---------------------------------------------------------------------------

func (p *Planner) buildInformedPlanSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	conversationHistory []llm.Message,
) string {
	return p.buildSystemPromptFromMode(ctx, mode, p.Cfg.Prompts.InformedPrompt, availableTools, reflections, availableSkills, conversationSubstitutions(conversationHistory))
}

func (p *Planner) planWithExploration(
	ctx context.Context,
	task string,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	plannerTools []tools.ToolDescriptor,
	availableSkills []skills.SkillDescriptor,
	singleStep bool,
	conversationHistory []llm.Message,
) (*orchestration.Plan, error) {
	systemPrompt := p.buildInformedPlanSystemPrompt(ctx, mode, availableTools, reflections, availableSkills, conversationHistory)

	var modelMeta llm.ModelMetadata
	if p.Cfg.ModelRegistry != nil {
		modelMeta, _ = p.Cfg.ModelRegistry.Resolve(ctx, p.Cfg.Model)
	}
	if modelMeta.ContextWindow == 0 {
		modelMeta.ContextWindow = 200000
		modelMeta.OutputLimit = 16384
		modelMeta.TokenizerType = "approximate"
	}

	if p.Cfg.ContextFactory == nil {
		p.log().Warn("planner: ContextFactory is nil, falling back to direct planning")
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep, conversationHistory)
	}
	cm := p.Cfg.ContextFactory(systemPrompt, modelMeta, "sliding_window")

	if setter, ok := cm.(interface{ SetTask(string) }); ok {
		setter.SetTask(task)
	}

	tokenCounter := p.Cfg.TokenCounter
	if tokenCounter == nil {
		tokenCounter = llm.NewSimpleTokenCounter()
	}

	execCaller := p.llm
	if p.Cfg.CallerForStep != nil {
		execCaller = p.Cfg.CallerForStep(cm, "planner-exploration")
	}

	var executorEmitter agent.Events
	if p.Cfg.Emitter != nil {
		executorEmitter = p.Cfg.Emitter
	}

	exec := agent.NewExecutor(
		execCaller,
		p.Cfg.ToolRegistry,
		p.Cfg.MaxExploreSteps,
		agent.WithTokenCounter(tokenCounter),
		agent.WithEvents(executorEmitter),
		agent.WithSuppressAssistantEvents(true),
		agent.WithToolResultBudget(agent.DefaultToolResultBudget()),
		agent.WithCircuitBreaker(agent.CircuitBreakerConfig{
			RepeatNudgeThreshold:     defaultRepeatNudgeThreshold,
			RepeatAbortThreshold:     defaultRepeatAbortThreshold,
			TruncationAbortThreshold: defaultTruncationAbortThreshold,
			ParseErrorAbortThreshold: defaultParseErrorAbortThreshold,
		}),
	)
	exec.SetReasoningEffort(p.Cfg.ReasoningEffort)

	ctx = agent.WithStepID(ctx, "planner-exploration")
	p.emitService("Exploring codebase...", map[string]any{"phase": "planning"})
	result, err := exec.Run(ctx, plannerTools, cm)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p.log().Warn("planner: exploration executor failed, falling back to direct planning", "error", err)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep, conversationHistory)
	}

	p.emitService("Generating plan...", map[string]any{"phase": "planning"})
	if result.Output == "" {
		p.log().Warn("planner: exploration produced no output, falling back to direct planning")
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep, conversationHistory)
	}

	plan, err := p.parsePlanResponse(result.Output, availableSkills)
	if err != nil {
		p.log().Warn("planner: failed to parse exploration plan output, falling back to direct", "error", err)
		return p.planDirect(ctx, task, mode, availableTools, reflections, availableSkills, singleStep, conversationHistory)
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
// Unified prompt builder
// ---------------------------------------------------------------------------

func (p *Planner) buildSystemPromptFromMode(
	ctx context.Context,
	mode planPromptMode,
	baseTemplate string,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	extraSubstitutions map[string]string,
) string {
	availableToolsStr := agent.BuildGroupedToolList(availableTools)

	// Trusted template-on-template substitutions: static prompt fragments
	// that may legitimately contain other placeholders (e.g.
	// RECENT-CONVERSATION inside MODE-PREAMBLE, REFLECTIONS inside
	// MODE-TAIL). Resolved iteratively.
	substitutions := map[string]string{
		"MODE-PREAMBLE":       mode.preamble,
		"MODE-TOT":            mode.tot,
		"MODE-GUIDANCE":       mode.guidance,
		"DOMAIN-ASSIGNMENT":   p.Cfg.Prompts.DomainAssignment,
		"AGENT-PROFILES":      p.Cfg.Prompts.AgentProfiles,
		"MODE-EXTRA-SECTIONS": mode.extraSections,
		"MODE-TAIL":           mode.tail,
		"MODE-JSON-EXAMPLE":   mode.jsonExample,
		"MAX-STEPS":           mode.maxSteps,
	}

	// Untrusted dynamic/external content: substituted LAST in a single pass
	// (Builder.ReplaceData) so placeholder names occurring inside these
	// values are never expanded — placeholder-injection protection. Covers
	// LLM-generated reflections, tool/skill lists, workspace paths, and
	// caller-provided extras (conversation history, user requests, plan
	// summaries, etc.).
	dataSubstitutions := map[string]string{
		"REFLECTIONS":      formatPlanReflections(reflections),
		"AVAILABLE-TOOLS":  availableToolsStr,
		"AVAILABLE-SKILLS": p.Cfg.FormatSkillList(ctx, availableSkills),
		"WORKSPACE-PATH":   p.Cfg.FormatWorkspacePath(ctx),
	}
	for k, v := range extraSubstitutions {
		dataSubstitutions[k] = v
	}

	result := prompt.NewBuilder().
		Core(baseTemplate).
		Core(p.familyPrompt(ctx)).
		Core(p.Cfg.Prompts.VerificationMandate).
		CacheBreak().
		ReplaceAll(substitutions).
		ReplaceDataAll(dataSubstitutions).
		Build()

	return p.Cfg.AppendContextSections(ctx, result)
}

func (p *Planner) buildPlanSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	availableTools []tools.ToolDescriptor,
	reflections []orchestration.Reflection,
	availableSkills []skills.SkillDescriptor,
	conversationHistory []llm.Message,
) string {
	return p.buildSystemPromptFromMode(ctx, mode, p.Cfg.Prompts.BasePrompt, availableTools, reflections, availableSkills, conversationSubstitutions(conversationHistory))
}

// conversationSubstitutions builds the RECENT-CONVERSATION substitution used
// by plan-mode preambles so first-message plans see the dialogue context.
func conversationSubstitutions(conversationHistory []llm.Message) map[string]string {
	return map[string]string{
		"RECENT-CONVERSATION": formatConversationHistory(conversationHistory),
	}
}

// ---------------------------------------------------------------------------
// Replan context
// ---------------------------------------------------------------------------

type replanContext struct {
	originalPlan       *orchestration.Plan
	completedSteps     []orchestration.CompletedStep
	failedStep         orchestration.CompletedStep
	reflection         *orchestration.Reflection
	sessionReflections []orchestration.Reflection
	availableSkills    []skills.SkillDescriptor
}

func (p *Planner) buildReplanSystemPrompt(ctx context.Context, rc replanContext) string {
	var originalPlanStr string
	planJSON, err := json.MarshalIndent(rc.originalPlan, "", "  ")
	if err != nil {
		originalPlanStr = fmt.Sprintf("%+v", rc.originalPlan)
	} else {
		originalPlanStr = string(planJSON)
	}

	var completedBuilder strings.Builder
	for _, cs := range rc.completedSteps {
		fmt.Fprintf(&completedBuilder, "- %s: %s\n", cs.StepID, cs.Output)
	}
	completedStepsStr := completedBuilder.String()

	var failedStepBuilder strings.Builder
	failedStepBuilder.WriteString(rc.failedStep.StepID + "\n")
	if rc.failedStep.Error != nil {
		failedStepBuilder.WriteString("Error: " + rc.failedStep.Error.Error() + "\n")
	}
	failedStepBuilder.WriteString("Output: " + rc.failedStep.Output)
	failedStepStr := failedStepBuilder.String()

	var reflectionStr string
	if rc.reflection != nil {
		reflectionStr = fmt.Sprintf(`Reflection on failure:
- Failure analysis: %s
- Root cause: %s
- Action plan: %s
`, rc.reflection.FailureAnalysis, rc.reflection.RootCause, rc.reflection.ActionPlan)
	}

	// All of these values are derived from dynamic/external content
	// (LLM-generated plans, step outputs, reflections) — substituted via
	// ReplaceData so placeholder names inside them are never expanded.
	substitutions := map[string]string{
		"ORIGINAL-PLAN":                originalPlanStr,
		"COMPLETED-STEPS":              completedStepsStr,
		"FAILED-STEP":                  failedStepStr,
		"PREVIOUS-SESSION-REFLECTIONS": formatSessionReflections(rc.sessionReflections),
		"CURRENT-REFLECTION":           reflectionStr,
		"AVAILABLE-SKILLS":             p.Cfg.FormatSkillList(ctx, rc.availableSkills),
		"WORKSPACE-PATH":               p.Cfg.FormatWorkspacePath(ctx),
	}

	result := prompt.NewBuilder().
		Core(p.Cfg.Prompts.ReplanPrompt).
		Core(p.familyPrompt(ctx)).
		Core(p.Cfg.Prompts.VerificationMandate).
		CacheBreak().
		ReplaceDataAll(substitutions).
		Build()

	return p.Cfg.AppendContextSections(ctx, result)
}

// ---------------------------------------------------------------------------
// Continuation prompt
// ---------------------------------------------------------------------------

func (p *Planner) buildContinuationSystemPrompt(
	ctx context.Context,
	mode planPromptMode,
	originalRequest string,
	existingPlan *orchestration.Plan,
	completedSteps []orchestration.CompletedStep,
	availableTools []tools.ToolDescriptor,
	availableSkills []skills.SkillDescriptor,
	conversationHistory []llm.Message,
) string {
	// Build a map for O(1) lookup of completed steps by ID.
	completedMap := make(map[string]orchestration.CompletedStep, len(completedSteps))
	for _, cs := range completedSteps {
		completedMap[cs.StepID] = cs
	}

	var planSummaryBuilder strings.Builder
	for _, step := range existingPlan.Steps {
		cs, hasResult := completedMap[step.ID]

		var status string
		switch {
		case hasResult && cs.Error == nil:
			status = "[COMPLETED]"
		case hasResult && cs.Error != nil:
			status = "[FAILED]"
		default:
			status = "[PENDING]"
		}

		planSummaryBuilder.WriteString("- ")
		planSummaryBuilder.WriteString(status)
		fmt.Fprintf(&planSummaryBuilder, " %s: %s", step.ID, step.Description)

		if hasResult && cs.Output != "" {
			fmt.Fprintf(&planSummaryBuilder, " → %s", cs.Output)
		}
		if hasResult && cs.Error != nil {
			fmt.Fprintf(&planSummaryBuilder, " [Error: %v]", cs.Error)
		}
		planSummaryBuilder.WriteString("\n")
	}
	completedPlanSummary := planSummaryBuilder.String()

	terminalSteps := findTerminalSteps(existingPlan)
	terminalStepsStr := strings.Join(terminalSteps, ", ")

	extraSubs := map[string]string{
		"ORIGINAL-REQUEST":       originalRequest,
		"COMPLETED-PLAN-SUMMARY": completedPlanSummary,
		"TERMINAL-STEPS":         terminalStepsStr,
		"RECENT-CONVERSATION":    formatConversationHistory(conversationHistory),
	}

	return p.buildSystemPromptFromMode(ctx, mode, p.Cfg.Prompts.BasePrompt, availableTools, nil, availableSkills, extraSubs)
}

// formatConversationHistory formats a conversation history slice for inclusion
// in the continuation planner prompt. Returns a human-readable dialogue.
func formatConversationHistory(history []llm.Message) string {
	if len(history) == 0 {
		return "(no previous conversation)"
	}
	var b strings.Builder
	for _, msg := range history {
		b.WriteString(msg.Role)
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Plan parsing
// ---------------------------------------------------------------------------

// callAndParsePlan calls the LLM with the given messages, parses the response
// as a plan, and retries with error feedback if parsing fails or the plan has
// zero steps. Retries up to parsePlanMaxRetries times. The initialMessages
// slice is not mutated.
func (p *Planner) callAndParsePlan(
	ctx context.Context,
	initialMessages []llm.Message,
	availableSkills []skills.SkillDescriptor,
) (*orchestration.Plan, error) {
	messages := make([]llm.Message, len(initialMessages))
	copy(messages, initialMessages)
	req := llm.ChatRequest{
		Messages:        messages,
		ReasoningEffort: p.Cfg.ReasoningEffort,
	}

	var lastParseErr error
	for attempt := 0; attempt < parsePlanMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		resp, err := p.llm.Call(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("planner LLM call failed: %w", err)
		}
		if resp == nil {
			return nil, errors.New("planner LLM call returned nil response")
		}

		plan, err := p.parsePlanResponse(resp.Message.Content, availableSkills)
		if err == nil {
			// Defense-in-depth: reject plans with zero steps — the LLM
			// may return valid JSON with an empty steps array ({"steps": []}),
			// which causes the orchestrator to complete silently with no
			// output. Treat this as a retryable error with an explicit
			// prompt instructing the model to generate at least one step.
			if len(plan.Steps) == 0 {
				err = errors.New("plan has zero steps — at least one step is required")
				lastParseErr = err
				p.log().Warn("planner: empty plan returned, retrying",
					"attempt", attempt+1,
					"max_retries", parsePlanMaxRetries,
					"error", err,
				)
				req.Messages = append(req.Messages,
					llm.Message{Role: "assistant", Content: resp.Message.Content},
					llm.Message{Role: "user", Content: fmt.Sprintf(
						"Your response was valid JSON but contained zero steps. Error: %s\n\n"+
							"The plan MUST contain at least one step with a summary, description, "+
							"and dependencies. Decompose the user's task into at least one concrete, "+
							"executable step. Respond ONLY with a valid JSON object containing a "+
							"non-empty \"steps\" array.",
						err,
					)},
				)
				continue
			}
			return plan, nil
		}

		lastParseErr = err
		p.log().Warn("planner: parse failed, retrying",
			"attempt", attempt+1,
			"max_retries", parsePlanMaxRetries,
			"error", err,
		)

		req.Messages = append(req.Messages,
			llm.Message{Role: "assistant", Content: resp.Message.Content},
			llm.Message{Role: "user", Content: fmt.Sprintf(
				"Your response was invalid JSON. Error: %s\n\n"+
					"Respond ONLY with a valid JSON object. "+
					"Do NOT use markdown code fences (```json ... ```). "+
					"Do NOT include any text, HTML, or commentary before or after the JSON. "+
					"Your entire response must start with { and end with } "+
					"and be parseable by a standard JSON parser.",
				err,
			)},
		)
	}

	return nil, fmt.Errorf("failed to parse plan response after %d attempts: %w", parsePlanMaxRetries, lastParseErr)
}

func (p *Planner) parsePlanResponse(content string, availableSkills []skills.SkillDescriptor) (*orchestration.Plan, error) {
	// Robust JSON extraction: handles markdown code fences and surrounding
	// prose, and finds the last valid JSON object in the response.
	jsonContent := llm.ExtractJSON(content)
	if !strings.HasPrefix(strings.TrimSpace(jsonContent), "{") {
		return nil, errors.New("no valid JSON object found in response")
	}

	var plan orchestration.Plan
	if err := json.Unmarshal([]byte(jsonContent), &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}

	if err := validatePlanDAG(&plan); err != nil {
		return nil, err
	}

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

// validatePlanDAG performs structural validation of a parsed plan:
//  1. Step IDs are unique.
//  2. Every depends_on entry references an existing step ID.
//  3. The dependency graph contains no cycles.
//
// The returned error is descriptive so it can be fed back to the model in
// the callAndParsePlan retry loop.
func validatePlanDAG(plan *orchestration.Plan) error {
	ids := make(map[string]bool, len(plan.Steps))
	for _, step := range plan.Steps {
		if ids[step.ID] {
			return fmt.Errorf("invalid plan: duplicate step ID %q — every step must have a unique ID", step.ID)
		}
		ids[step.ID] = true
	}

	for _, step := range plan.Steps {
		for _, dep := range step.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("invalid plan: step %q depends on unknown step ID %q — depends_on may only reference IDs of steps in this plan", step.ID, dep)
			}
		}
	}

	// Cycle detection via iterative DFS with three-color marking.
	const (
		white = 0 // unvisited
		gray  = 1 // in progress
		black = 2 // done
	)
	color := make(map[string]int, len(plan.Steps))
	deps := make(map[string][]string, len(plan.Steps))
	for _, step := range plan.Steps {
		deps[step.ID] = step.DependsOn
	}

	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("invalid plan: dependency cycle detected involving step %q — the depends_on graph must be acyclic", id)
		case black:
			return nil
		}
		color[id] = gray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for _, step := range plan.Steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Shared formatting helpers
// ---------------------------------------------------------------------------

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

// familyPrompt returns the family-specific prompt or the base prompt as fallback.
// Guards against nil FamilyPrompt function pointer.
func (p *Planner) familyPrompt(ctx context.Context) string {
	if p.Cfg.Prompts.FamilyPrompt == nil {
		return ""
	}
	return p.Cfg.Prompts.FamilyPrompt("planner", p.getFamily(ctx))
}

// findTerminalSteps returns the IDs of steps that have no dependents (terminal steps in the DAG).
// Returns nil for a nil plan.
func findTerminalSteps(plan *orchestration.Plan) []string {
	if plan == nil {
		return nil
	}
	dependedOn := make(map[string]bool)
	for _, step := range plan.Steps {
		for _, dep := range step.DependsOn {
			dependedOn[dep] = true
		}
	}
	var terminal []string
	for _, step := range plan.Steps {
		if !dependedOn[step.ID] {
			terminal = append(terminal, step.ID)
		}
	}
	return terminal
}

// summarizeExplorationSteps extracts a concise summary from exploration ReAct steps.
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
	result = strutil.TruncateUTF8AtLineBoundary(result, maxExplorationContextLen)
	return result
}
