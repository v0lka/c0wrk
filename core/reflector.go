package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/user/agent/core/prompts"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	"github.com/user/agent/sdk/prompt"
	"github.com/user/agent/sdk/tools"
)

// compile-time check: Reflector implements orchestration.Reflector.
var _ orchestration.Reflector = (*Reflector)(nil)

const reflectorAnalyzeFooter = "Please analyze this execution and provide a structured reflection."

// Reflector analyzes execution trajectory to produce
// structured self-correction insights per AD 4.6.
type Reflector struct {
	llm           LLMCaller
	modelRegistry *llm.ModelRegistry
}

// NewReflector creates a new Reflector with the given LLM caller.
func NewReflector(caller LLMCaller) *Reflector {
	return &Reflector{llm: caller}
}

// SetModelRegistry sets the model registry for model metadata resolution.
func (r *Reflector) SetModelRegistry(registry *llm.ModelRegistry) {
	r.modelRegistry = registry
}

// Reflect analyzes execution trajectory to produce structured self-correction insights.
// trajectory = the steps executed,
// plan = the plan (if plan_execute mode), prevReflections = past reflections for this task
func (r *Reflector) Reflect(
	ctx context.Context,
	trajectory []Step,
	plan *Plan,
	prevReflections []Reflection,
) (reflection *Reflection, err error) {
	systemPrompt := r.buildSystemPrompt()

	// Append compact environment context for reflection analysis.
	if envBlock := tools.FormatCompactEnvBlock(tools.EnvInfoFrom(ctx)); envBlock != "" {
		systemPrompt += "\n\n" + envBlock
	}

	userMessage := r.buildUserMessage(trajectory, plan, prevReflections)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	req := llm.ChatRequest{
		Messages: messages,
	}

	resp, err := r.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("reflector LLM call failed: %w", err)
	}

	reflection, err = r.parseReflectionResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reflection response: %w", err)
	}

	// Set timestamp
	reflection.Timestamp = time.Now()

	return reflection, nil
}

// buildSystemPrompt constructs the system prompt for the reflector role.
func (r *Reflector) buildSystemPrompt() string {
	return prompt.NewBuilder().
		Core(prompts.ReflectorSystem).
		Core(prompts.ReflectorInstructions).
		Build()
}

// buildUserMessage constructs the user message containing all context for reflection.
func (r *Reflector) buildUserMessage(
	trajectory []Step,
	plan *Plan,
	prevReflections []Reflection,
) string {
	var sb strings.Builder

	// Add execution trajectory
	sb.WriteString("## Execution Trajectory\n\n")
	if len(trajectory) == 0 {
		sb.WriteString("No steps executed.\n\n")
	} else {
		for i, step := range trajectory {
			fmt.Fprintf(&sb, "### Step %d\n", i+1)
			fmt.Fprintf(&sb, "**Thought:** %s\n", step.Thought)
			if step.Action.Name != "" {
				fmt.Fprintf(&sb, "**Action:** %s\n", step.Action.Name)
				if len(step.Action.Input) > 0 {
					fmt.Fprintf(&sb, "**Input:** %s\n", string(step.Action.Input))
				}
			}
			if step.Observation != "" {
				fmt.Fprintf(&sb, "**Observation:** %s\n", step.Observation)
			}
			sb.WriteString("\n")
		}
	}

	// Add plan if available
	if plan != nil && len(plan.Steps) > 0 {
		sb.WriteString("## Plan\n\n")
		for _, step := range plan.Steps {
			fmt.Fprintf(&sb, "- %s: %s\n", step.ID, step.Description)
			if len(step.DependsOn) > 0 {
				fmt.Fprintf(&sb, "  Depends on: %v\n", step.DependsOn)
			}
		}
		sb.WriteString("\n")
	}

	// Add previous reflections if any
	if len(prevReflections) > 0 {
		sb.WriteString("## Previous Reflections\n\n")
		sb.WriteString("(Learn from these to avoid repeating the same mistakes)\n\n")
		for i, ref := range prevReflections {
			fmt.Fprintf(&sb, "### Reflection %d\n", i+1)
			fmt.Fprintf(&sb, "- Summary: %s\n", ref.Summary)
			fmt.Fprintf(&sb, "- Root Cause: %s\n", ref.RootCause)
			fmt.Fprintf(&sb, "- Action Plan: %s\n", ref.ActionPlan)
			fmt.Fprintf(&sb, "- Suggested Action: %s\n", ref.SuggestedAction)
			sb.WriteString("\n")
		}
	}

	sb.WriteString(reflectorAnalyzeFooter)

	return sb.String()
}

// parseReflectionResponse extracts a Reflection from the LLM response content.
func (r *Reflector) parseReflectionResponse(content string) (*Reflection, error) {
	// Use the same extractJSON pattern as router.go
	jsonStr := extractJSON(content)

	var reflection Reflection
	if err := json.Unmarshal([]byte(jsonStr), &reflection); err != nil {
		return nil, fmt.Errorf("failed to unmarshal reflection JSON: %w", err)
	}

	// Validate suggested action
	switch reflection.SuggestedAction {
	case "retry", "replan", "abort":
		// Valid
	case "":
		reflection.SuggestedAction = "retry" // Default to retry if not specified
	default:
		reflection.SuggestedAction = "retry" // Default to retry for unknown values
	}

	// Set default summary if empty
	if reflection.Summary == "" {
		reflection.Summary = "Execution analysis unavailable"
	}

	return &reflection, nil
}
