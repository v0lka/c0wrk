// Package reflector analyzes execution trajectories to produce structured
// self-correction insights (retry, replan, or abort recommendations).
package reflector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/tools"
)

// compile-time check: Reflector implements orchestration.Reflector.
var _ orchestration.Reflector = (*Reflector)(nil)

const defaultAnalyzeFooter = "Please analyze this execution and provide a structured reflection."

// Config holds the configuration for a Reflector.
type Config struct {
	// SystemPrompt is the reflection system prompt.
	SystemPrompt string
	// AnalyzeFooter is appended to the user message. Defaults to a standard analysis request.
	AnalyzeFooter string
}

// Reflector analyzes execution trajectory to produce structured self-correction insights.
type Reflector struct {
	llm             agent.LLMCaller
	systemPrompt    string
	analyzeFooter   string
	reasoningEffort string
}

// New creates a new Reflector with the given caller and config.
func New(caller agent.LLMCaller, cfg Config) *Reflector {
	footer := cfg.AnalyzeFooter
	if footer == "" {
		footer = defaultAnalyzeFooter
	}
	return &Reflector{
		llm:           caller,
		systemPrompt:  cfg.SystemPrompt,
		analyzeFooter: footer,
	}
}

// SetReasoningEffort sets the reasoning effort for the reflector.
func (r *Reflector) SetReasoningEffort(effort string) {
	r.reasoningEffort = effort
}

// Reflect analyzes execution trajectory to produce structured self-correction insights.
func (r *Reflector) Reflect(
	ctx context.Context,
	trajectory []agent.Step,
	plan *orchestration.Plan,
	prevReflections []orchestration.Reflection,
) (reflection *orchestration.Reflection, err error) {
	systemPrompt := prompt.NewBuilder().
		Core(r.systemPrompt).
		Build()

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
		Messages:        messages,
		ReasoningEffort: r.reasoningEffort,
	}

	resp, err := r.llm.Call(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("reflector LLM call failed: %w", err)
	}
	if resp == nil {
		return nil, errors.New("reflector LLM call returned nil response")
	}

	reflection, err = r.parseReflectionResponse(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reflection response: %w", err)
	}

	reflection.Timestamp = time.Now()

	return reflection, nil
}

// buildUserMessage constructs the user message containing all context for reflection.
func (r *Reflector) buildUserMessage(
	trajectory []agent.Step,
	plan *orchestration.Plan,
	prevReflections []orchestration.Reflection,
) string {
	var sb strings.Builder

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

	sb.WriteString(r.analyzeFooter)

	return sb.String()
}

// parseReflectionResponse extracts a Reflection from the LLM response content.
func (r *Reflector) parseReflectionResponse(content string) (*orchestration.Reflection, error) {
	jsonStr := llm.ExtractJSON(content)

	var reflection orchestration.Reflection
	if err := json.Unmarshal([]byte(jsonStr), &reflection); err != nil {
		return nil, fmt.Errorf("failed to unmarshal reflection JSON: %w", err)
	}

	switch reflection.SuggestedAction {
	case "retry", "replan", "abort":
		// Valid
	case "":
		reflection.SuggestedAction = "retry"
	default:
		reflection.SuggestedAction = "retry"
	}

	if reflection.Summary == "" {
		reflection.Summary = "Execution analysis unavailable"
	}

	return &reflection, nil
}
