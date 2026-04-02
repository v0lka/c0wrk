package memory

import (
	"fmt"
	"strings"

	"github.com/user/agent/internal/config"
	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/llm"
)

// ContextWindow — managed representation of the LLM context window.
type ContextWindow struct {
	systemPrompt   string
	taskDefinition string
	criteria       []core.AcceptanceCriterion
	plan           *core.Plan
	reflections    []core.Reflection
	constitution   []string
	steps          []core.Step
	strategy       core.CompactionStrategy
	tracker        *llm.ContextTokenTracker
	modelMeta      llm.ModelMetadata
	thresholds     config.CompactionThresholds

	// compactedMessages stores the result of compaction.
	// When non-nil, BuildPrompt uses this instead of converting steps.
	compactedMessages []llm.Message
}

// safetyMarginPercent is the percentage of context window reserved as safety margin.
const safetyMarginPercent = 5 // 5% of context window

// FillCheck is an alias for core.FillCheck for backward compatibility.
//
// Deprecated: Use core.FillCheck directly.
type FillCheck = core.FillCheck

// NewContextWindow creates a new ContextWindow.
func NewContextWindow(systemPrompt string, modelMeta llm.ModelMetadata, tracker *llm.ContextTokenTracker, thresholds config.CompactionThresholds, strategy core.CompactionStrategy) *ContextWindow {
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

// SetTask sets the task definition and acceptance criteria.
func (cw *ContextWindow) SetTask(task string, criteria []core.AcceptanceCriterion) {
	cw.taskDefinition = task
	cw.criteria = criteria
}

// SetPlan sets the plan (pinned section).
func (cw *ContextWindow) SetPlan(plan *core.Plan) {
	cw.plan = plan
}

// SetReflections sets the reflections.
func (cw *ContextWindow) SetReflections(reflections []core.Reflection) {
	cw.reflections = reflections
}

// SetConstitution sets the constitution principles.
func (cw *ContextWindow) SetConstitution(principles []string) {
	cw.constitution = principles
}

// AddStep appends a step to the history and updates the token tracker.
func (cw *ContextWindow) AddStep(step core.Step) {
	cw.steps = append(cw.steps, step)
	// Clear compacted messages since we have new steps
	cw.compactedMessages = nil
	// Estimate tokens for this step and add to tracker delta
	stepText := step.Thought + step.Action.Name + string(step.Action.Input) + step.Observation
	cw.tracker.AddDelta(stepText)
}

// SetStrategy changes the compaction strategy.
func (cw *ContextWindow) SetStrategy(s core.CompactionStrategy) {
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

	// 2. User message with task definition + acceptance criteria
	if cw.taskDefinition != "" || len(cw.criteria) > 0 {
		taskContent := cw.formatTaskAndCriteria()
		if taskContent != "" {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: taskContent,
			})
		}
	}

	// 3. System message with plan
	if cw.plan != nil && len(cw.plan.Steps) > 0 {
		planContent := cw.formatPlan()
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: planContent,
		})
	}

	// 4. System message with reflections
	if len(cw.reflections) > 0 {
		reflectionsContent := cw.formatReflections()
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: reflectionsContent,
		})
	}

	// 5. System message with constitution principles
	if len(cw.constitution) > 0 {
		constitutionContent := cw.formatConstitution()
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: constitutionContent,
		})
	}

	// 6. Step history
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
func (cw *ContextWindow) Compact() {
	if cw.strategy == nil || len(cw.steps) == 0 {
		return
	}

	// Use effective max as the budget for compaction
	budgetTokens := cw.EffectiveMax()

	// Compact steps using the strategy
	cw.compactedMessages = cw.strategy.Compact(cw.steps, budgetTokens)
	cw.steps = nil
}

// formatTaskAndCriteria formats the task definition and acceptance criteria.
func (cw *ContextWindow) formatTaskAndCriteria() string {
	var parts []string

	if cw.taskDefinition != "" {
		parts = append(parts, "Task: "+cw.taskDefinition)
	}

	if len(cw.criteria) > 0 {
		parts = append(parts, "\nAcceptance Criteria:")
		for _, ac := range cw.criteria {
			parts = append(parts, fmt.Sprintf("- [%s] %s", ac.ID, ac.Description))
		}
	}

	return strings.Join(parts, "\n")
}

// formatPlan formats the plan.
func (cw *ContextWindow) formatPlan() string {
	if cw.plan == nil || len(cw.plan.Steps) == 0 {
		return ""
	}

	var parts []string
	parts = append(parts, "Plan:")
	for _, step := range cw.plan.Steps {
		deps := ""
		if len(step.DependsOn) > 0 {
			deps = fmt.Sprintf(" (depends on: %s)", strings.Join(step.DependsOn, ", "))
		}
		parts = append(parts, fmt.Sprintf("- [%s] %s%s", step.ID, step.Description, deps))
	}

	return strings.Join(parts, "\n")
}

// formatReflections formats the reflections.
func (cw *ContextWindow) formatReflections() string {
	parts := make([]string, 1, 1+len(cw.reflections)*3)
	parts[0] = "Reflections:"
	for _, r := range cw.reflections {
		parts = append(parts,
			"- Analysis: "+r.FailureAnalysis,
			"  Root Cause: "+r.RootCause,
			"  Action Plan: "+r.ActionPlan,
		)
	}

	return strings.Join(parts, "\n")
}

// formatConstitution formats the constitution principles.
func (cw *ContextWindow) formatConstitution() string {
	parts := make([]string, 1, 1+len(cw.constitution))
	parts[0] = "Constitution Principles:"
	for i, principle := range cw.constitution {
		parts = append(parts, fmt.Sprintf("%d. %s", i+1, principle))
	}

	return strings.Join(parts, "\n")
}
