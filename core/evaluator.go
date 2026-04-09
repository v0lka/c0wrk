package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

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

// evaluatorToolWhitelist defines the read-only tools the evaluator agents can use.
// read_evidence is always added separately since it needs blackboard context.
// file_ops is included but restricted to read-only actions via schema filtering
// and the readOnlyToolExecutor runtime guard.
var evaluatorToolWhitelist = map[string]bool{
	"file_ops":      true,
	"ripgrep":       true,
	"glob":          true,
	"read_evidence": true,
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
		maxSteps:         20, // evaluator agent budget
		logger:           logger,
		emitter:          emitter,
		toolResultBudget: toolResultBudget,
	}
}

// evaluatorTaskTemplate is the task description template for evaluator agents.
const evaluatorTaskTemplate = `## Acceptance Criterion
CRITERION_DESCRIPTION

## Final Result Summary
RESULT_SUMMARY

## Available Evidence
Use the read_evidence tool to list and inspect step results from the execution.
Use file_ops, ripgrep, glob to inspect the actual workspace state.
Start by listing available evidence, then fetch relevant steps to evaluate this criterion.`

// Evaluate checks the result against all acceptance criteria.
// Non-LLM criteria (programmatic, intent_verification, unknown) run sequentially first.
// All llm_judge criteria then run in parallel since each spawns an independent ReAct
// agent with its own context window, and the Blackboard is thread-safe.
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

	// Phase 2: Run llm_judge criteria in parallel.
	if len(llmJudgeCriteria) > 0 {
		type llmJudgeResult struct {
			detail EvalDetail
			err    error
		}
		results := make([]llmJudgeResult, len(llmJudgeCriteria))

		var wg sync.WaitGroup
		for i, criterion := range llmJudgeCriteria {
			wg.Add(1)
			go func(idx int, ac AcceptanceCriterion) {
				defer wg.Done()
				detail, err := e.evaluateLLMJudgeReAct(ctx, ac, result, bb)
				results[idx] = llmJudgeResult{detail: detail, err: err}
			}(i, criterion)
		}
		wg.Wait()

		// Collect results in original criteria order (deterministic).
		for _, r := range results {
			if r.err != nil {
				return nil, r.err
			}
			categorizeDetail(evalResult, r.detail)
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

// evaluateLLMJudgeReAct runs a per-criterion ReAct agent to evaluate an llm_judge criterion.
func (e *Evaluator) evaluateLLMJudgeReAct(ctx context.Context, criterion AcceptanceCriterion, result string, bb Blackboard) (EvalDetail, error) {
	// Guard: if dependencies for ReAct agent are missing, fall back to simple one-shot LLM call
	if e.contextFactory == nil || e.llm == nil {
		return e.evaluateLLMJudgeSimple(ctx, criterion, result)
	}

	// 1. Build task description
	resultSummary := result
	if len(resultSummary) > maxResultSummaryChars {
		resultSummary = resultSummary[:maxResultSummaryChars] + "..."
	}

	taskDescription := evaluatorTaskTemplate
	taskDescription = strings.ReplaceAll(taskDescription, "CRITERION_DESCRIPTION", criterion.Description)
	taskDescription = strings.ReplaceAll(taskDescription, "RESULT_SUMMARY", resultSummary)

	// 2. Filter tools to read-only subset
	var allTools []tools.ToolDescriptor
	if e.toolRegistry != nil {
		allTools = e.toolRegistry.List()
	}
	filteredTools := filterEvaluatorTools(allTools)

	// 3. Model metadata for evaluator agents
	modelMeta := llm.ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}

	// 4. Create context manager
	systemPrompt := prompts.EvaluatorJudge
	cm := e.contextFactory(systemPrompt, modelMeta, "sliding_window")

	// 5. Set task (no acceptance criteria for evaluator agent)
	cm.SetTask(taskDescription, nil)

	// 6. Attach blackboard to context (uses core.WithBlackboard, same package)
	ctx = WithBlackboard(ctx, bb)

	// 7. Create executor — suppress assistant events, use noop emitter.
	// Wrap toolRegistry with read-only guard to block write actions at runtime.
	readOnlyTools := &readOnlyToolExecutor{inner: e.toolRegistry}
	exec := agent.NewExecutor(
		e.llm,
		readOnlyTools,
		e.tokenCounter,
		e.maxSteps,
		e.logger,
		(*agent.NoopEvents)(nil),
		true, // suppressAssistantEvents
		e.toolResultBudget,
	)

	// 8. Run the evaluation agent
	execResult, err := exec.Run(ctx, filteredTools, cm)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("evaluator agent failed for criterion %s: %w", criterion.ID, err)
	}

	// 9. Parse verdict from output
	diagnostic := parseEvalVerdict(execResult.Output)

	return EvalDetail{
		Criterion:  criterion,
		Diagnostic: diagnostic,
	}, nil
}

// filterEvaluatorTools returns only tool descriptors whose Name is in the evaluator whitelist.
// For file_ops, it modifies the schema to expose only read-only actions.
func filterEvaluatorTools(allTools []tools.ToolDescriptor) []tools.ToolDescriptor {
	filtered := make([]tools.ToolDescriptor, 0, len(evaluatorToolWhitelist))
	for _, td := range allTools {
		if !evaluatorToolWhitelist[td.Name] {
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

// evaluateLLMJudgeSimple is a fallback for when the ReAct agent dependencies
// (contextFactory, tokenCounter, etc.) are not available. It performs a simple
// one-shot LLM call, matching the old evaluator behavior.
func (e *Evaluator) evaluateLLMJudgeSimple(ctx context.Context, criterion AcceptanceCriterion, result string) (EvalDetail, error) {
	if e.llm == nil {
		return EvalDetail{
			Criterion:  criterion,
			Diagnostic: "UNCLEAR:evaluator LLM not configured",
		}, nil
	}

	var userMsg strings.Builder
	fmt.Fprintf(&userMsg, "Criterion: %s\n\nResult: %s", criterion.Description, result)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.EvaluatorJudge},
			{Role: "user", Content: userMsg.String()},
		},
	}

	resp, err := e.llm.Call(ctx, req)
	if err != nil {
		return EvalDetail{}, fmt.Errorf("LLM judge call failed: %w", err)
	}

	diagnostic := parseEvalVerdict(resp.Message.Content)
	return EvalDetail{
		Criterion:  criterion,
		Diagnostic: diagnostic,
	}, nil
}

// parseEvalVerdict parses the evaluator agent's output into a diagnostic string.
func parseEvalVerdict(output string) string {
	output = strings.TrimSpace(output)
	upper := strings.ToUpper(output)
	switch {
	case strings.HasPrefix(upper, "YES"):
		return "PASSED:" + output
	case strings.HasPrefix(upper, "NO"):
		return "FAILED:" + output
	default:
		return "UNCLEAR:" + output
	}
}
