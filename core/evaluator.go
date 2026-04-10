package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/tools"
)

// compile-time check: Evaluator implements orchestration.Evaluator.
var _ orchestration.Evaluator = (*Evaluator)(nil)

// maxResultSummaryChars is the maximum character length for the result summary
// passed to each evaluator ReAct agent (~500 tokens at 4 chars/token).
const maxResultSummaryChars = 2000


// evaluatorToolAllowlist defines the tools evaluator agents can use.
// read_evidence and report_verdict are always added separately since they need blackboard context.
// file_ops is included but restricted to read-only actions via schema filtering
// and the readOnlyToolExecutor runtime guard.
var evaluatorToolAllowlist = map[string]bool{
	"file_ops":        true,
	"ripgrep":         true,
	"glob":            true,
	"read_evidence":   true,
	"report_verdict":  true,
}

// fileOpsReadOnlyActions lists the actions that are safe for evaluation mode.
var fileOpsReadOnlyActions = map[string]bool{
	"read_file":      true,
	"list_directory": true,
	"search_files":   true,
	"search_content": true,
}

// fileOpsWriteActions lists the actions blocked in evaluation mode.
var fileOpsWriteActions = map[string]bool{
	"write_file":       true,
	"edit_file":        true,
	"create_directory": true,
	"delete_directory": true,
	"delete_file":      true,
}

// Evaluator checks execution results against acceptance criteria.
// Each llm_judge criterion is evaluated by an independent ReAct agent
// with its own context window, fetching evidence on-demand from the Blackboard.
type Evaluator struct {
	tools            ToolExecutor // for programmatic checks (bash_exec)
	llm              LLMCaller
	toolRegistry     *tools.ToolRegistry
	tokenCounter     llm.TokenCounter
	contextFactory   ContextManagerFactory
	maxSteps         int
	logger           *slog.Logger
	emitter          Emitter
	toolResultBudget ToolResultBudget
}

// NewEvaluator creates a new Evaluator with full ReAct agent support.
func NewEvaluator(
	toolExec ToolExecutor,
	llmCaller LLMCaller,
	toolRegistry *tools.ToolRegistry,
	tokenCounter llm.TokenCounter,
	contextFactory ContextManagerFactory,
	logger *slog.Logger,
	emitter Emitter,
	toolResultBudget ToolResultBudget,
) *Evaluator {
	return &Evaluator{
		tools:            toolExec,
		llm:              llmCaller,
		toolRegistry:     toolRegistry,
		tokenCounter:     tokenCounter,
		contextFactory:   contextFactory,
		maxSteps:         8, // evaluator agent budget
		logger:           logger,
		emitter:          emitter,
		toolResultBudget: toolResultBudget,
	}
}

// evaluatorTaskTemplate is the task description template for evaluator agents.
const evaluatorTaskTemplate = `## Acceptance Criterion
ID: CRITERION_ID
Description: CRITERION_DESCRIPTION

## Final Result Summary
RESULT_SUMMARY

## Available Evidence
Use the read_evidence tool to list and inspect step results from the execution.
Use file_ops, ripgrep, glob to inspect the actual workspace state.
Start by listing available evidence, then fetch relevant steps to evaluate this criterion.
When done, call report_verdict with your determination.`

// Evaluate checks the result against all acceptance criteria.
// Non-LLM criteria (programmatic, intent_verification, unknown) run sequentially first.
// Each llm_judge criterion is then evaluated by an independent ReAct agent
// with a fresh context window, recording verdicts via report_verdict into the Blackboard.
func (e *Evaluator) Evaluate(ctx context.Context, result string, criteria []AcceptanceCriterion, bb Blackboard) (*EvalResult, error) {
	evalResult := &EvalResult{
		Passed:  []EvalDetail{},
		Failed:  []EvalDetail{},
		Unclear: []EvalDetail{},
	}

	// Phase 1: Process non-LLM criteria sequentially and collect llm_judge criteria.
	var llmJudgeCriteria []AcceptanceCriterion
	for _, criterion := range criteria {
		var detail EvalDetail
		var err error

		switch criterion.CheckType {
		case "programmatic":
			detail, err = e.evaluateProgrammatic(ctx, criterion)
		case "llm_judge":
			llmJudgeCriteria = append(llmJudgeCriteria, criterion)
			continue
		case "intent_verification":
			detail = EvalDetail{
				Criterion:  criterion,
				Diagnostic: "SKIPPED:evaluated externally by IntentVerifier",
			}
		default:
			// Unknown check type - mark as unclear
			detail = EvalDetail{
				Criterion:  criterion,
				Diagnostic: "unknown check type: " + criterion.CheckType,
			}
			evalResult.Unclear = append(evalResult.Unclear, detail)
			continue
		}

		if err != nil {
			return nil, err
		}

		categorizeDetail(evalResult, detail)
	}

	// Phase 2: Run llm_judge criteria via per-criterion ReAct agents.
	if len(llmJudgeCriteria) > 0 {
		details, err := e.evaluateLLMJudge(ctx, llmJudgeCriteria, result, bb)
		if err != nil {
			return nil, err
		}
		for _, detail := range details {
			categorizeDetail(evalResult, detail)
		}
	}

	// AllPassed = no failures (unclear is a judge quality issue, not execution failure)
	evalResult.AllPassed = len(evalResult.Failed) == 0

	return evalResult, nil
}

// categorizeDetail appends a detail to the appropriate category in the eval result.
func categorizeDetail(evalResult *EvalResult, detail EvalDetail) {
	switch {
	case detail.Diagnostic != "" && strings.HasPrefix(detail.Diagnostic, "PASSED:"):
		evalResult.Passed = append(evalResult.Passed, detail)
	case detail.Diagnostic != "" && strings.HasPrefix(detail.Diagnostic, "FAILED:"):
		evalResult.Failed = append(evalResult.Failed, detail)
	default:
		evalResult.Unclear = append(evalResult.Unclear, detail)
	}
}

// evaluateProgrammatic runs a programmatic check via bash_exec.
func (e *Evaluator) evaluateProgrammatic(ctx context.Context, criterion AcceptanceCriterion) (EvalDetail, error) {
	// Create JSON input for bash_exec
	input := map[string]string{"command": criterion.CheckCmd}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("failed to marshal bash_exec input: %w", err)
	}

	// Execute the command
	toolResult, err := e.tools.Execute(ctx, "bash_exec", inputJSON)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("bash_exec failed: %w", err)
	}

	detail := EvalDetail{
		Criterion: criterion,
	}

	if toolResult.IsError {
		// Command failed (non-zero exit code)
		detail.Diagnostic = "FAILED:" + toolResult.Content
	} else {
		// Command succeeded (exit code 0)
		detail.Diagnostic = "PASSED:" + toolResult.Content
	}

	return detail, nil
}

// evaluateLLMJudge evaluates all llm_judge criteria using per-criterion ReAct agents.
// Each criterion gets a fresh context window. Verdicts are recorded into the Blackboard
// via the report_verdict tool and collected after all criteria are processed.
func (e *Evaluator) evaluateLLMJudge(ctx context.Context, criteria []AcceptanceCriterion, result string, bb Blackboard) ([]EvalDetail, error) {
	// Guard: if dependencies for ReAct agent are missing, mark all as UNCLEAR.
	if e.contextFactory == nil || e.llm == nil {
		details := make([]EvalDetail, len(criteria))
		for i, ac := range criteria {
			details[i] = EvalDetail{
				Criterion:  ac,
				Diagnostic: "UNCLEAR:evaluator dependencies not configured",
			}
		}
		return details, nil
	}

	// Build result summary (shared across all criteria).
	resultSummary := result
	if len(resultSummary) > maxResultSummaryChars {
		resultSummary = resultSummary[:maxResultSummaryChars] + "..."
	}

	// Filter tools once (shared across all criteria).
	var allTools []tools.ToolDescriptor
	if e.toolRegistry != nil {
		allTools = e.toolRegistry.List()
	}
	filteredTools := filterEvaluatorTools(allTools)

	// Model metadata for evaluator agents.
	modelMeta := llm.ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}

	// Evaluate each criterion with a fresh context window.
	for _, criterion := range criteria {
		// Emit eval step start event
		if e.emitter != nil {
			e.emitter.EvalStepStart(criterion.ID, criterion.Description)
		}
		evalStartTime := time.Now()

		// 1. Build task description.
		taskDescription := evaluatorTaskTemplate
		taskDescription = strings.ReplaceAll(taskDescription, "CRITERION_ID", criterion.ID)
		taskDescription = strings.ReplaceAll(taskDescription, "CRITERION_DESCRIPTION", criterion.Description)
		taskDescription = strings.ReplaceAll(taskDescription, "RESULT_SUMMARY", resultSummary)

		// 2. Create fresh context manager.
		systemPrompt := prompts.EvaluatorJudge

		// Append compact environment context (time, timezone, OS) for evaluator.
		if envBlock := tools.FormatCompactEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
			systemPrompt += "\n\n" + envBlock
		}

		cm := e.contextFactory(systemPrompt, modelMeta, "sliding_window")
		cm.SetTask(taskDescription, nil)

		// 3. Attach blackboard to context.
		evalCtx := WithBlackboard(ctx, bb)

		// 4. Create executor with read-only tool guard and criterion-scoped emitter.
		readOnlyTools := &readOnlyToolExecutor{inner: e.toolRegistry}
		
		// Create a criterion-scoped emitter for this evaluation step
		var execEvents agent.AgentEvents = (*agent.NoopEvents)(nil)
		if e.emitter != nil {
			if scopable, ok := e.emitter.(CriterionScopable); ok {
				scopedEmitter := scopable.WithCriterionID(criterion.ID)
				execEvents = &evalStepEventsAdapter{emitter: scopedEmitter}
			}
		}
		
		exec := agent.NewExecutor(
			e.llm,
			readOnlyTools,
			e.tokenCounter,
			e.maxSteps,
			execEvents,
			true, // suppressAssistantEvents
			e.toolResultBudget,
		)

		// 5. Run the evaluation agent.
		_, err := exec.Run(evalCtx, filteredTools, cm)
		
		// Emit eval step complete event
		if e.emitter != nil {
			e.emitter.EvalStepComplete(criterion.ID, err == nil, time.Since(evalStartTime))
		}
		
		if err != nil {
			e.log("evaluator agent failed for criterion %s: %v", criterion.ID, err)
			// Non-fatal: continue evaluating remaining criteria.
			continue
		}
	}

	// Collect verdicts from blackboard.
	verdicts := bb.GetEvalVerdicts()

	// Map verdicts to EvalDetail in original criteria order.
	details := make([]EvalDetail, len(criteria))
	for i, ac := range criteria {
		v, ok := verdicts[ac.ID]
		if !ok {
			details[i] = EvalDetail{
				Criterion:  ac,
				Diagnostic: "UNCLEAR:no verdict reported by evaluator agent",
			}
			continue
		}
		switch strings.ToUpper(v.Verdict) {
		case "YES":
			details[i] = EvalDetail{Criterion: ac, Diagnostic: "PASSED:" + v.Explanation}
		case "NO":
			details[i] = EvalDetail{Criterion: ac, Diagnostic: "FAILED:" + v.Explanation}
		default:
			details[i] = EvalDetail{Criterion: ac, Diagnostic: "UNCLEAR:" + v.Explanation}
		}
	}

	return details, nil
}

// log logs a formatted message if the logger is non-nil.
func (e *Evaluator) log(format string, args ...any) {
	if e.logger != nil {
		e.logger.Warn(fmt.Sprintf(format, args...))
	}
}

// filterEvaluatorTools returns only tool descriptors whose Name is in the evaluator allowlist.
// For file_ops, it modifies the schema to expose only read-only actions.
func filterEvaluatorTools(allTools []tools.ToolDescriptor) []tools.ToolDescriptor {
	filtered := make([]tools.ToolDescriptor, 0, len(evaluatorToolAllowlist))
	for _, td := range allTools {
		if !evaluatorToolAllowlist[td.Name] {
			continue
		}
		if td.Name == "file_ops" {
			td = filterFileOpsDescriptor(td)
		}
		filtered = append(filtered, td)
	}
	return filtered
}

// filterFileOpsDescriptor returns a copy of the file_ops descriptor with write
// actions removed from the action enum in the JSON schema, and description
// updated to reflect read-only nature.
func filterFileOpsDescriptor(td tools.ToolDescriptor) tools.ToolDescriptor {
	// Parse the schema
	var schema map[string]any
	if err := json.Unmarshal(td.InputSchema, &schema); err != nil {
		return td // return unmodified on parse error
	}

	// Navigate to properties.action.enum
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return td
	}
	actionProp, ok := props["action"].(map[string]any)
	if !ok {
		return td
	}
	enumVals, ok := actionProp["enum"].([]any)
	if !ok {
		return td
	}

	// Filter to read-only actions only
	readOnly := make([]any, 0, len(fileOpsReadOnlyActions))
	for _, v := range enumVals {
		if s, ok := v.(string); ok && fileOpsReadOnlyActions[s] {
			readOnly = append(readOnly, s)
		}
	}
	actionProp["enum"] = readOnly

	// Update description to reflect read-only nature
	if _, hasDesc := actionProp["description"]; hasDesc {
		actionProp["description"] = "The read-only file operation to perform (write operations are not available in evaluation mode)"
	}

	newSchema, err := json.Marshal(schema)
	if err != nil {
		return td
	}

	return tools.ToolDescriptor{
		Name:        td.Name,
		Description: "File operations (read-only: read_file, list_directory, search_files, search_content)",
		InputSchema: newSchema,
		Source:      td.Source,
	}
}

// readOnlyToolExecutor wraps a ToolExecutor and blocks write actions on file_ops.
// This is a runtime safety net in case the LLM ignores schema restrictions.
type readOnlyToolExecutor struct {
	inner agent.ToolExecutor
}

// Execute delegates to the inner executor, but rejects file_ops write actions.
func (r *readOnlyToolExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	if name == "file_ops" {
		var parsed struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(input, &parsed); err == nil && fileOpsWriteActions[parsed.Action] {
			return tools.ToolResult{
				Content: fmt.Sprintf("write operations are not allowed in evaluation mode (action: %s)", parsed.Action),
				IsError: true,
			}, nil
		}
	}
	return r.inner.Execute(ctx, name, input)
}

// evalStepEventsAdapter wraps an Emitter to implement agent.AgentEvents for eval steps.
// The emitter should already be scoped with criterion_id via WithCriterionID.
type evalStepEventsAdapter struct {
	emitter Emitter
}

// ensure evalStepEventsAdapter implements agent.AgentEvents.
var _ agent.AgentEvents = (*evalStepEventsAdapter)(nil)

func (a *evalStepEventsAdapter) StepStart(stepNum int) {
	// Step start is handled by EvalStepStart, not needed here
}

func (a *evalStepEventsAdapter) Thought(stepNum int, content, reasoning string) {
	// Thoughts during evaluation are emitted as thought events
	// The criterion_id is already injected by the scoped emitter
	if a.emitter != nil && content != "" {
		a.emitter.Thought(stepNum, content, reasoning)
	}
}

func (a *evalStepEventsAdapter) ToolCall(stepNum int, toolName, argsPreview string) {
	if a.emitter != nil {
		// Prefix the tool name to indicate it's from evaluation
		a.emitter.ToolCall(stepNum, toolName, argsPreview)
	}
}

func (a *evalStepEventsAdapter) ToolResult(stepNum, resultLen int, preview string) {
	if a.emitter != nil {
		a.emitter.ToolResult(stepNum, resultLen, preview)
	}
}

func (a *evalStepEventsAdapter) StepComplete(stepNum int, duration time.Duration) {
	// Step completion is handled by EvalStepComplete
}

func (a *evalStepEventsAdapter) SubAgentLaunch(stepID, description string) {
	// Not used in evaluator
}

func (a *evalStepEventsAdapter) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	// Not used in evaluator
}

func (a *evalStepEventsAdapter) AssistantChunk(content string) {
	// Suppressed in evaluator
}

func (a *evalStepEventsAdapter) AssistantDone(content string, inputTokens, outputTokens int) {
	// Suppressed in evaluator
}

func (a *evalStepEventsAdapter) TokensUsed(inputTokens, outputTokens int) {
	if a.emitter != nil {
		a.emitter.TokensUsed(inputTokens, outputTokens)
	}
}

func (a *evalStepEventsAdapter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	if a.emitter != nil {
		a.emitter.ContextFill(fillPercent, usedTokens, maxTokens, status, stepID)
	}
}

func (a *evalStepEventsAdapter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	// Diagnostics are logged but not emitted to UI for cleaner experience
}

