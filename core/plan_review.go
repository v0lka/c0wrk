package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/skills"
)

// planFileExt is the file extension for serialized plan metadata (JSON with
// hidden fields that are stripped from the user-visible markdown).
const planFileExt = ".plan.json"

// planReviewSidecar is the struct persisted alongside the plan markdown so
// hidden fields (DependsOn, Profile, etc.) and the routing decision survive
// app restart.
type planReviewSidecar struct {
	Plan  *orchestration.Plan       `json:"plan"`
	Route *router.RoutingDecision   `json:"route,omitempty"`
}

// RandomSuffix generates a 6-character random hex suffix for plan filenames.
func RandomSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use truncated nanosecond timestamp to avoid collisions.
		return fmt.Sprintf("%x", time.Now().UnixNano())[:6]
	}
	return hex.EncodeToString(b)
}

// planDir returns the .c0wrk/plans directory path for a workspace.
func planDir(workspacePath string) string {
	return filepath.Join(workspacePath, ".c0wrk", "plans")
}

// HandlePlanReview generates a plan, serializes it to markdown, saves to
// .c0wrk/plans/, and returns a HandleResult signalling plan review phase
// (no execution). The caller should not call engine.Resume() after this.
func (o *Orchestrator) HandlePlanReview(
	ctx context.Context,
	message string,
	sessionID string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	activeSkills []skills.SkillDescriptor,
	opts HandleOptions,
	routing *router.RoutingDecision,
) (*HandleResult, error) {
	singleStep := o.shouldUseSingleStep(opts.ExecutionMode)
	o.logDebug("orchestrator: HandlePlanReview generating plan", "mode", opts.ExecutionMode, "singleStep", singleStep)

	plan, planErr := o.planner.Plan(ctx, message, availableTools, nil, activeSkills, singleStep)
	if planErr != nil {
		o.logDebug("orchestrator: planning failed", "error", planErr)
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, fmt.Errorf("planning failed: %w", planErr)
	}
	o.logDebug("orchestrator: plan ready for review", "steps", len(plan.Steps))

	// Emit plan step events so frontend knows about the steps
	planStepEvents := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planStepEvents[i] = orchestration.PlanStepEvent{
			ID:          s.ID,
			Summary:     s.Summary,
			Description: s.Description,
			Status:      "pending",
			DependsOn:   s.DependsOn,
		}
	}
	o.emitter.PlanGenerated(len(plan.Steps), planStepEvents)

	// Serialize plan to markdown
	md := SerializePlan(plan)

	// Determine workspace path and save .md file
	workspacePath := sdktools.WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return nil, errors.New("no workspace path in context, cannot save plan")
	}

	plansDir := planDir(workspacePath)
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create plans directory: %w", err)
	}

	// Extract session prefix from sessionID for file naming
	sessionPrefix := "session"
	if len(sessionID) > 8 {
		sessionPrefix = sessionID[:8]
	} else if sessionID != "" {
		sessionPrefix = sessionID
	}

	planPath := filepath.Join(plansDir, fmt.Sprintf("%s_%s.md", sessionPrefix, RandomSuffix()))
	if err := os.WriteFile(planPath, []byte(md), 0o644); err != nil {
		return nil, fmt.Errorf("failed to write plan file: %w", err)
	}
	o.logDebug("orchestrator: plan saved", "path", planPath)

	// Serialize full plan (with hidden fields) and routing decision as JSON
	// for restart survival. The route is needed so ApprovePlan can resume
	// execution with the correct domain/complexity/mode context.
	planJSON, jErr := json.Marshal(planReviewSidecar{
		Plan:  plan,
		Route: routing,
	})
	if jErr != nil {
		o.logDebug("orchestrator: failed to marshal plan sidecar JSON", "error", jErr)
	} else {
		jsonPath := strings.TrimSuffix(planPath, ".md") + planFileExt
		if wErr := os.WriteFile(jsonPath, planJSON, 0o644); wErr != nil {
			o.logDebug("orchestrator: failed to write plan sidecar JSON", "path", jsonPath, "error", wErr)
		}
	}

	// Store plan on blackboard for potential approval
	bb.SetPlan(plan)

	// Emit plan step events so frontend shows step list
	result := &HandleResult{
		Output:          fmt.Sprintf("Plan generated: %d steps. Review in file viewer.", len(plan.Steps)),
		RoutingDecision: routing,
		Plan:            plan,
		Blackboard:      bb,
		PlanReviewPhase: "awaiting_accept",
		PlanReviewPath:  planPath,
	}

	return result, nil
}

// PlanWithFeedback regenerates a plan incorporating user feedback on a
// previously rejected plan. The previous plan and feedback are included as
// context in the planner's user message. Plan step events are emitted
// internally so callers do not need to access the emitter directly.
func (o *Orchestrator) PlanWithFeedback(
	ctx context.Context,
	originalMessage string,
	previousPlanMD string,
	feedback string,
	availableTools []sdktools.ToolDescriptor,
	activeSkills []skills.SkillDescriptor,
	singleStep bool,
) (*orchestration.Plan, error) {
	// Build enriched user message
	var enrichedMsg strings.Builder
	enrichedMsg.WriteString("Original request: ")
	enrichedMsg.WriteString(originalMessage)
	enrichedMsg.WriteString("\n\nPrevious plan (rejected by user):\n")
	enrichedMsg.WriteString(previousPlanMD)
	enrichedMsg.WriteString("\n\nUser feedback on the rejected plan:\n")
	enrichedMsg.WriteString(feedback)
	enrichedMsg.WriteString("\n\nPlease generate an improved plan addressing all feedback points above. Preserve valid parts of the previous plan and only modify what the user asked to change.")

	o.logDebug("orchestrator: PlanWithFeedback", "originalMsgLen", len(originalMessage), "feedbackLen", len(feedback))

	plan, err := o.planner.Plan(ctx, enrichedMsg.String(), availableTools, nil, activeSkills, singleStep)
	if err != nil {
		return nil, fmt.Errorf("replanning with feedback failed: %w", err)
	}

	// Emit plan step events so the frontend shows the new step structure.
	// This is emitted internally rather than requiring callers to access
	// the Emitter directly.
	planStepEvents := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		planStepEvents[i] = orchestration.PlanStepEvent{
			ID:          s.ID,
			Summary:     s.Summary,
			Description: s.Description,
			Status:      "pending",
			DependsOn:   s.DependsOn,
		}
	}
	o.emitter.PlanGenerated(len(plan.Steps), planStepEvents)

	return plan, nil
}

// SemanticValidatePlan uses an LLM call to validate that the edited plan still
// addresses the original user request, has no unnecessary steps, and is
// internally consistent. Returns a list of issue descriptions (empty = valid).
func (o *Orchestrator) SemanticValidatePlan(
	ctx context.Context,
	originalMessage string,
	planMD string,
) ([]string, error) {
	systemPrompt := `You are a plan validator. Compare the execution plan to the original user request.
Evaluate three dimensions:

1. COVERAGE: Does the plan address all aspects of the user's request? Are any critical steps missing?
2. RELEVANCE: Does the plan introduce work unrelated to the request? Are any steps unnecessary?
3. INTERNAL CONSISTENCY: Do the steps form a logically coherent sequence? Are there contradictions between steps? Are step boundaries clean (no overlapping responsibilities)?

Respond with a JSON object:
{"valid": true/false, "issues": [{"severity": "error|warning", "description": "..."}]}

Only report issues that genuinely prevent successful execution. Minor stylistic differences are not issues.`

	userPrompt := fmt.Sprintf("Original user request:\n%s\n\nPlan to validate:\n%s", originalMessage, planMD)

	resp, err := o.llm.Call(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("semantic validation LLM call failed: %w", err)
	}

	// Parse response for issues — use proper JSON unmarshalling with
	// markdown code fence stripping, then fall back to raw content as a
	// warning if the response cannot be parsed.
	content := resp.Message.Content
	issues, parseErr := parseValidationResponse(content)
	if parseErr != nil {
		// Could not parse — treat as a non-blocking warning with raw content.
		return []string{fmt.Sprintf("validation response could not be parsed: %s (raw: %s)", parseErr, content)}, nil
	}
	return issues, nil
}

// validationResponse mirrors the expected JSON structure from the LLM.
type validationResponse struct {
	Valid  bool `json:"valid"`
	Issues []struct {
		Severity    string `json:"severity"`
		Description string `json:"description"`
	} `json:"issues"`
}

// parseValidationResponse attempts to parse an LLM validation response as JSON,
// stripping markdown code fences if present. Returns parsed issues or an error.
func parseValidationResponse(content string) ([]string, error) {
	clean := strings.TrimSpace(content)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	var resp validationResponse
	if err := json.Unmarshal([]byte(clean), &resp); err != nil {
		return nil, fmt.Errorf("json parse: %w", err)
	}

	if resp.Valid {
		return nil, nil
	}

	var issues []string

	// Defensive: treat Valid==false as blocking even when the LLM returns
	// no specific issues (hallucination / empty response edge case).
	if len(resp.Issues) == 0 {
		return []string{"[error] Plan was flagged as invalid by the validator but no specific issues were returned"}, nil
	}

	for _, iss := range resp.Issues {
		sev := iss.Severity
		if sev == "" {
			sev = "unknown"
		}
		issues = append(issues, fmt.Sprintf("[%s] %s", sev, iss.Description))
	}
	return issues, nil
}
