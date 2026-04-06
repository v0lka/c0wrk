// Package memory provides working memory management, compaction strategies, and procedural memory for agent sessions.
package memory

import (
	"context"
	"fmt"

	sdkagent "github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
)

// CompactionThresholds configures when context compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int // Context fill % that triggers predictive compaction
	WarningPercent    int // Context fill % that triggers warning-level compaction
	EmergencyPercent  int // Context fill % that triggers emergency compaction
}

// ContextWindow — managed representation of the LLM context window.
type ContextWindow struct {
	systemPrompt   string
	taskContent    string // formatted task content (user message)
	planContent    string // formatted plan content (system message)
	steps          []sdkagent.Step
	strategy       sdkagent.CompactionStrategy
	tracker        *llm.ContextTokenTracker
	modelMeta      llm.ModelMetadata
	thresholds     CompactionThresholds

	// compactedMessages stores the result of compaction.
	// When non-nil, BuildPrompt uses this instead of converting steps.
	compactedMessages []llm.Message
}

// safetyMarginPercent is the percentage of context window reserved as safety margin.
const safetyMarginPercent = 5 // 5% of context window

// FillCheck is an alias for sdkagent.FillCheck for backward compatibility.
//
// Deprecated: Use sdkagent.FillCheck directly.
type FillCheck = sdkagent.FillCheck

// NewContextWindow creates a new ContextWindow.
func NewContextWindow(systemPrompt string, modelMeta llm.ModelMetadata, tracker *llm.ContextTokenTracker, thresholds CompactionThresholds, strategy sdkagent.CompactionStrategy) *ContextWindow {
	return &ContextWindow{
		systemPrompt: systemPrompt,
		modelMeta:    modelMeta,
		tracker:      tracker,
		thresholds:   thresholds,
		strategy:     strategy,
	}
}

// EffectiveMax returns the effective maximum token count for the context window,
// accounting for output limit and safety margin.
func (cw *ContextWindow) EffectiveMax() int {
	safetyMargin := cw.modelMeta.ContextWindow * safetyMarginPercent / 100
	return cw.modelMeta.ContextWindow - cw.modelMeta.OutputLimit - safetyMargin
}

// FillPercent returns the current fill percentage of the context window.
func (cw *ContextWindow) FillPercent() float64 {
	effectiveMax := cw.EffectiveMax()
	if effectiveMax <= 0 {
		return 100.0
	}
	return float64(cw.tracker.EstimateTotal()) / float64(effectiveMax) * 100
}

// OutputLimit returns the model's maximum output token limit.
func (cw *ContextWindow) OutputLimit() int {
	return cw.modelMeta.OutputLimit
}

// AvailableTokens returns the number of tokens remaining in the context window.
func (cw *ContextWindow) AvailableTokens() int {
	available := cw.EffectiveMax() - cw.tracker.EstimateTotal()
	if available < 0 {
		return 0
	}
	return available
}

// CheckFill returns a FillCheck with the current fill status.
func (cw *ContextWindow) CheckFill() FillCheck {
	used := cw.tracker.EstimateTotal()
	effectiveMax := cw.EffectiveMax()
	percent := float64(0)
	if effectiveMax > 0 {
		percent = float64(used) / float64(effectiveMax) * 100
	}

	status := "ok"
	switch {
	case percent >= 100:
		status = "reject"
	case percent >= float64(cw.thresholds.EmergencyPercent):
		status = "emergency"
	case percent >= float64(cw.thresholds.WarningPercent):
		status = "warning"
	case percent >= float64(cw.thresholds.PredictivePercent):
		status = "compact"
	}

	return FillCheck{Percent: percent, Status: status, Used: used, Max: effectiveMax}
}

// CorrectTokenCount updates the tracker with the actual API input token count.
func (cw *ContextWindow) CorrectTokenCount(apiInputTokens int) {
	cw.tracker.Correct(apiInputTokens)
}

// SetTask sets the task content (user message in prompt).
// The caller is responsible for formatting the task, including any criteria or context.
func (cw *ContextWindow) SetTask(task string) {
	cw.taskContent = task
}

// SetPlan sets the plan content (system message in prompt).
// The caller is responsible for formatting the plan text.
func (cw *ContextWindow) SetPlan(planText string) {
	cw.planContent = planText
}

// AddStep appends a step to the history and updates the token tracker.
func (cw *ContextWindow) AddStep(step sdkagent.Step) {
	cw.steps = append(cw.steps, step)
	// Clear compacted messages since we have new steps
	cw.compactedMessages = nil
	// Estimate tokens for this step and add to tracker delta
	stepText := fmt.Sprintf("%s %s %s %s", step.Thought, step.Action.Name, string(step.Action.Input), step.Observation)
	cw.tracker.AddDelta(stepText)
}

// SetStrategy changes the compaction strategy.
func (cw *ContextWindow) SetStrategy(s sdkagent.CompactionStrategy) {
	cw.strategy = s
}

// BuildPrompt assembles the full prompt in priority order.
func (cw *ContextWindow) BuildPrompt() []llm.Message {
	var messages []llm.Message

	// 1. System message with systemPrompt
	if cw.systemPrompt != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: cw.systemPrompt,
		})
	}

	// 2. User message with task content (pre-formatted by caller)
	if cw.taskContent != "" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: cw.taskContent,
		})
	}

	// 3. System message with plan (pre-formatted by caller)
	if cw.planContent != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: cw.planContent,
		})
	}

	// 4. Step history
	stepMessages := cw.buildStepMessages()
	messages = append(messages, stepMessages...)

	return messages
}

// buildStepMessages returns messages for the step history.
// Uses compactedMessages if available, otherwise converts steps.
func (cw *ContextWindow) buildStepMessages() []llm.Message {
	if cw.compactedMessages != nil {
		return cw.compactedMessages
	}

	var messages []llm.Message
	for _, step := range cw.steps {
		// Assistant message with thought and action
		assistantMsg := llm.Message{
			Role:    "assistant",
			Content: step.Thought,
		}
		if step.Action.ID != "" {
			assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
		}
		messages = append(messages, assistantMsg)

		// Tool response message with observation
		if step.Action.ID != "" {
			// OpenAI API requires non-empty content for tool-role messages.
			// Use placeholder if observation is empty to prevent 400 errors.
			observation := step.Observation
			if observation == "" {
				observation = "(no output)"
			}
			toolMsg := llm.Message{
				Role:       "tool",
				Content:    observation,
				ToolCallID: step.Action.ID,
			}
			messages = append(messages, toolMsg)
		}
	}
	return messages
}

// NeedsCompaction returns true if compaction is needed based on fill status.
// This method is kept for backward compatibility; new code should use CheckFill().
func (cw *ContextWindow) NeedsCompaction() bool {
	fill := cw.CheckFill()
	return fill.Status == "compact" || fill.Status == "warning" || fill.Status == "emergency" || fill.Status == "reject"
}

// Compact compresses the step history using the configured strategy.
func (cw *ContextWindow) Compact(ctx context.Context) {
	if cw.strategy == nil || len(cw.steps) == 0 {
		return
	}

	// Use effective max as the budget for compaction
	budgetTokens := cw.EffectiveMax()

	// Compact steps using the strategy
	cw.compactedMessages = cw.strategy.Compact(ctx, cw.steps, budgetTokens)
	cw.steps = nil
}
