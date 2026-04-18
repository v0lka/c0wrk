// Package memory provides working memory management, compaction strategies, and procedural memory for agent sessions.
package memory

import (
	"context"
	"fmt"
	"strings"

	sdkagent "github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
)

// CompactionThresholds configures when context compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int // Context fill % that triggers predictive compaction
	WarningPercent    int // Context fill % that triggers warning-level compaction
	EmergencyPercent  int // Context fill % that triggers emergency compaction
}

// ToolOutputPruning configures selective pruning of old tool outputs.
type ToolOutputPruning struct {
	KeepLastN       int
	ProtectedTools  []string
	PlaceholderText string
}

// ContextWindow — managed representation of the LLM context window.
type ContextWindow struct {
	systemPrompt string
	taskContent  string // formatted task content (user message)
	planContent  string // formatted plan content (system message)
	steps        []sdkagent.Step
	strategy     sdkagent.CompactionStrategy
	tracker      *llm.ContextTokenTracker
	modelMeta    llm.ModelMetadata
	thresholds   CompactionThresholds
	pruning      ToolOutputPruning
	safetyMargin int // percentage of context window reserved as safety margin (default: 5)

	// compactedMessages stores the result of compaction.
	// When non-nil, BuildPrompt uses this instead of converting steps.
	compactedMessages []llm.Message
}

// defaultSafetyMargin is the default percentage of context window reserved as safety margin.
const defaultSafetyMargin = 5 // 5% of context window

// FillCheck is an alias for sdkagent.FillCheck for backward compatibility.
//
// Deprecated: Use sdkagent.FillCheck directly.
type FillCheck = sdkagent.FillCheck

// NewContextWindow creates a new ContextWindow.
// safetyMarginPercent is the percentage of context window reserved as safety margin (default: 5 if 0).
func NewContextWindow(systemPrompt string, modelMeta llm.ModelMetadata, tracker *llm.ContextTokenTracker, thresholds CompactionThresholds, strategy sdkagent.CompactionStrategy, safetyMarginPercent int, pruning ...ToolOutputPruning) *ContextWindow {
	cw := &ContextWindow{
		systemPrompt: systemPrompt,
		modelMeta:    modelMeta,
		tracker:      tracker,
		thresholds:   thresholds,
		strategy:     strategy,
	}
	if safetyMarginPercent <= 0 {
		safetyMarginPercent = defaultSafetyMargin
	}
	cw.safetyMargin = safetyMarginPercent
	if len(pruning) > 0 {
		cw.pruning = pruning[0]
	}
	if cw.pruning.PlaceholderText == "" {
		cw.pruning.PlaceholderText = "[Tool output omitted to save context. Rely on the information already gathered above.]"
	}
	return cw
}

// EffectiveMax returns the effective maximum token count for the context window,
// accounting for output limit and safety margin.
func (cw *ContextWindow) EffectiveMax() int {
	safetyMargin := cw.modelMeta.ContextWindow * cw.safetyMargin / 100
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

// Tracker returns the underlying ContextTokenTracker.
func (cw *ContextWindow) Tracker() *llm.ContextTokenTracker {
	return cw.tracker
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

// invisibleChars is the cutset of trailing invisible characters to trim from message content.
// Includes: spaces, tabs, newlines, carriage returns, null, zero-width space,
// zero-width non-joiner, zero-width joiner, and BOM.
// Contains: ' ', '\t', '\n', '\r', '\x00', U+200B, U+200C, U+200D, U+FEFF.
const invisibleChars = " \t\n\r\x00\u200b\u200c\u200d\ufeff"

// buildStepMessages returns messages for the step history.
// Uses compactedMessages if available, otherwise converts steps.
func (cw *ContextWindow) buildStepMessages() []llm.Message {
	if cw.compactedMessages != nil {
		return cw.compactedMessages
	}

	// Determine which step indices have tool results and should be pruned
	protectedIndices := cw.computeProtectedIndices()

	var messages []llm.Message
	for i := 0; i < len(cw.steps); {
		step := cw.steps[i]

		if step.ResponseGroup > 0 {
			// Collect all consecutive steps with the same ResponseGroup
			groupStart := i
			groupEnd := i + 1
			for groupEnd < len(cw.steps) && cw.steps[groupEnd].ResponseGroup == step.ResponseGroup {
				groupEnd++
			}
			groupSteps := cw.steps[groupStart:groupEnd]

			// Build ONE assistant message with all tool calls
			assistantMsg := llm.Message{
				Role:    "assistant",
				Content: strings.TrimRight(groupSteps[0].Thought, invisibleChars),
			}
			for _, gs := range groupSteps {
				if gs.Action.ID != "" {
					assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, gs.Action)
				}
			}
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				assistantMsg.Content = "(proceeding)"
			}
			messages = append(messages, assistantMsg)

			// Add individual tool result messages
			for gi, gs := range groupSteps {
				idx := groupStart + gi
				if gs.Action.ID != "" {
					observation := strings.TrimRight(gs.Observation, invisibleChars)
					if observation == "" {
						observation = "(no output)"
					}
					// Apply pruning: use placeholder for non-protected tool outputs
					if _, protected := protectedIndices[idx]; !protected && cw.pruning.KeepLastN > 0 {
						observation = cw.pruning.PlaceholderText
					}
					messages = append(messages, llm.Message{
						Role:       "tool",
						Content:    observation,
						ToolCallID: gs.Action.ID,
					})
				}
				// UserNudge only on last step of group
				if gi == len(groupSteps)-1 {
					nudgeContent := strings.TrimRight(gs.UserNudge, invisibleChars)
					if nudgeContent != "" {
						messages = append(messages, llm.Message{Role: "user", Content: nudgeContent})
					}
				}
			}

			i = groupEnd
		} else {
			// Original logic for standalone steps (backward compatible)
			assistantMsg := llm.Message{
				Role:    "assistant",
				Content: strings.TrimRight(step.Thought, invisibleChars),
			}
			if step.Action.ID != "" {
				assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
			}
			// OpenAI API requires assistant messages to have either content or tool_calls.
			// If both are empty, add a placeholder to prevent 400 errors.
			if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
				assistantMsg.Content = "(proceeding)"
			}
			messages = append(messages, assistantMsg)

			// Tool response message with observation
			if step.Action.ID != "" {
				// OpenAI API requires non-empty content for tool-role messages.
				// Use placeholder if observation is empty to prevent 400 errors.
				observation := strings.TrimRight(step.Observation, invisibleChars)
				if observation == "" {
					observation = "(no output)"
				}

				// Apply pruning: use placeholder for non-protected tool outputs
				if _, protected := protectedIndices[i]; !protected && cw.pruning.KeepLastN > 0 {
					observation = cw.pruning.PlaceholderText
				}

				toolMsg := llm.Message{
					Role:       "tool",
					Content:    observation,
					ToolCallID: step.Action.ID,
				}
				messages = append(messages, toolMsg)
			}

			// User nudge message (e.g., step limit extension notifications)
			// Skip if content is empty or only contains whitespace/invisible characters.
			nudgeContent := strings.TrimRight(step.UserNudge, invisibleChars)
			if nudgeContent != "" {
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: nudgeContent,
				})
			}

			i++
		}
	}
	return messages
}

// computeProtectedIndices returns a set of step indices that should NOT be pruned.
// Protected indices include:
//   - The last KeepLastN steps that have tool results
//   - Any step whose tool name is in ProtectedTools
//   - All steps in a ResponseGroup if any step in the group is protected
func (cw *ContextWindow) computeProtectedIndices() map[int]struct{} {
	protected := make(map[int]struct{})

	if cw.pruning.KeepLastN <= 0 {
		return protected // No pruning, nothing is protected (everything is kept)
	}

	// First pass: collect indices of steps with tool results
	var toolResultIndices []int
	for i, step := range cw.steps {
		if step.Action.ID != "" {
			toolResultIndices = append(toolResultIndices, i)
		}
	}

	// Build protected set from last KeepLastN tool-result steps
	start := len(toolResultIndices) - cw.pruning.KeepLastN
	if start < 0 {
		start = 0
	}
	for _, idx := range toolResultIndices[start:] {
		protected[idx] = struct{}{}
	}

	// Add protected tools (always keep these regardless of position)
	protectedToolSet := make(map[string]struct{})
	for _, tool := range cw.pruning.ProtectedTools {
		protectedToolSet[tool] = struct{}{}
	}
	for i, step := range cw.steps {
		if step.Action.ID != "" {
			if _, isProtected := protectedToolSet[step.Action.Name]; isProtected {
				protected[i] = struct{}{}
			}
		}
	}

	// Protect entire response groups: if any step in a group is protected, protect all.
	// This prevents partial pruning which would produce malformed API messages
	// (assistant message with N tool_calls but fewer tool results).
	groupProtection := make(map[int64]bool)
	for i, step := range cw.steps {
		if step.ResponseGroup > 0 {
			if _, isProtected := protected[i]; isProtected {
				groupProtection[step.ResponseGroup] = true
			}
		}
	}
	for i, step := range cw.steps {
		if step.ResponseGroup > 0 && groupProtection[step.ResponseGroup] {
			protected[i] = struct{}{}
		}
	}

	return protected
}

// NeedsCompaction returns true if compaction is needed based on fill status.
// This method is kept for backward compatibility; new code should use CheckFill().
func (cw *ContextWindow) NeedsCompaction() bool {
	fill := cw.CheckFill()
	return fill.Status == "compact" || fill.Status == "warning" || fill.Status == "emergency" || fill.Status == "reject"
}

// CompactionResult is an alias for sdkagent.CompactionResult.
type CompactionResult = sdkagent.CompactionResult

// Compact compresses the step history using the configured strategy.
// Returns a CompactionResult with before/after fill percentages, or nil if no compaction occurred.
func (cw *ContextWindow) Compact(ctx context.Context) *CompactionResult {
	if cw.strategy == nil || len(cw.steps) == 0 {
		return nil
	}

	beforeFill := cw.CheckFill()

	// Use effective max as the budget for compaction
	budgetTokens := cw.EffectiveMax()

	// Compact steps using the strategy
	cw.compactedMessages = cw.strategy.Compact(ctx, cw.steps, budgetTokens)
	cw.steps = nil

	// Estimate after-compaction fill
	effectiveMax := cw.EffectiveMax()
	afterPercent := float64(0)
	if effectiveMax > 0 {
		baseTokens := cw.tracker.EstimateMessages([]llm.Message{
			{Role: "system", Content: cw.systemPrompt},
			{Role: "user", Content: cw.taskContent},
		})
		if cw.planContent != "" {
			baseTokens += cw.tracker.EstimateMessages([]llm.Message{
				{Role: "system", Content: cw.planContent},
			})
		}
		compactedTokens := cw.tracker.EstimateMessages(cw.compactedMessages)
		afterPercent = float64(baseTokens+compactedTokens) / float64(effectiveMax) * 100
	}

	return &CompactionResult{
		BeforePercent: beforeFill.Percent,
		AfterPercent:  afterPercent,
	}
}
