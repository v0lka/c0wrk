// Package memory provides working memory management, compaction strategies, and procedural memory for agent sessions.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/security"
	"github.com/v0lka/sp4rk/strutil"
)

// CompactionThresholds configures when context compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int // Context fill % that triggers predictive compaction
	WarningPercent    int // Context fill % that triggers warning-level compaction
	EmergencyPercent  int // Context fill % that triggers emergency compaction
}

// ToolOutputPruning configures selective pruning of old tool outputs.
type ToolOutputPruning struct {
	KeepLastN        int
	ProtectedTools   []string
	PlaceholderText  string
	ThresholdPercent float64      // Context fill % below which pruning is skipped (default: 50)
	Logger           *slog.Logger // Optional logger for pruning diagnostics
}

// HistoryMutation configures regular (non-emergency) mutation of step history
// to reduce O(n²) replay cost. Unlike emergency compaction (triggered by fill
// %), history mutation runs on every BuildPrompt call and replaces old tool
// results with cache references, evicts bookkeeping outputs, and deduplicates
// repeated reads. Information is preserved via ToolResultCache — the LLM can
// retrieve evicted content via tool_result_read.
type HistoryMutation struct {
	// ToolResultEvictionStep is the number of steps after which a tool result
	// is replaced with a cache reference. 0 disables eviction (full results
	// kept indefinitely, subject to pruning/compaction).
	ToolResultEvictionStep int
	// EvictStepStatus enables immediate eviction of update_checklist results
	// (pure bookkeeping, no information loss). Also matches the legacy
	// set_step_status name for old session history.
	EvictStepStatus bool
	// DedupRepeatedReads replaces duplicate file-read results (same path +
	// mtime) with a reference to the earlier result's cache hash.
	DedupRepeatedReads bool
	// Logger is an optional logger for mutation diagnostics.
	Logger *slog.Logger
}

// evictionReferenceText returns the placeholder text for an evicted tool result.
func evictionReferenceText(hash string) string {
	return fmt.Sprintf("[Result evicted to cache. Use tool_result_read(hash=%q, start_line=1, num_lines=N) to retrieve the full content.]", hash)
}

// stepStatusEvictedText is the placeholder for evicted checklist results.
const stepStatusEvictedText = "[checklist update — evicted]"

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
	mutation     HistoryMutation
	safetyMargin int // percentage of context window reserved as safety margin (default: 5)

	// priorConversation holds messages from previous exchanges (prior
	// user/assistant turns) that should appear in the prompt before the
	// current task content. This lets a top-level agent (e.g. the Conductor)
	// see the dialogue context leading up to the current message — without
	// it, a follow-up like "implement variant a" has no referent.
	priorConversation []llm.Message

	// injectionDefenseEnabled gates the prompt injection defense wrapping (<untrusted-content> tags).
	injectionDefenseEnabled bool

	// compactedMessages stores the frozen prefix produced by the last Compact() call.
	// When non-nil, buildStepMessages prepends this instead of converting
	// steps[:compactedThroughIndex]. Steps appended after that point (the
	// "tail") are still rendered normally and combined with this prefix, so
	// that adding a step never forces a full reversion to the raw,
	// unbounded step history — see compactedThroughIndex.
	compactedMessages []llm.Message

	// compactedThroughIndex is the number of leading entries in steps that are
	// already represented by compactedMessages. Only steps[compactedThroughIndex:]
	// need to be converted on subsequent BuildPrompt calls, until the next Compact().
	compactedThroughIndex int
}

// noopCounter is a zero-cost TokenCounter used when no tracker is provided.
type noopCounter struct{}

func (n *noopCounter) Count(string) int                { return 0 }
func (n *noopCounter) CountMessages([]llm.Message) int { return 0 }

// defaultSafetyMargin is the default percentage of context window reserved as safety margin.
const defaultSafetyMargin = 5 // 5% of context window

// ContextWindowConfig configures a ContextWindow. Zero-value fields fall back
// to sensible defaults (see field comments); only ModelMeta is meaningfully
// required in practice.
type ContextWindowConfig struct {
	// SystemPrompt is the system prompt rendered at the start of every
	// prompt build. May contain a prompt.CacheBreakMarker.
	SystemPrompt string

	// ModelMeta describes the target model. If ContextWindow is 0 (unknown
	// model), a fallback of 128000 is used — a zero ContextWindow would
	// disable compaction entirely (EffectiveMax returns 0, CheckFill returns
	// "ok"), causing unbounded conversation growth until the API rejects the
	// request. If OutputLimit is 0, a fallback of 4096 is used.
	ModelMeta llm.ModelMetadata

	// Tracker counts tokens for fill tracking. nil disables token accounting
	// (compaction and fill warnings are silently disabled).
	Tracker *llm.ContextTokenTracker

	// Thresholds configures the compaction trigger percentages.
	Thresholds CompactionThresholds

	// Strategy is the compaction algorithm. nil disables compaction.
	Strategy sdkagent.CompactionStrategy

	// SafetyMarginPercent is the percentage of the context window reserved
	// as safety margin. Values <= 0 use the default (5).
	SafetyMarginPercent int

	// InjectionDefenseEnabled gates prompt-injection defense wrapping
	// (<untrusted-content> tags around tool outputs).
	InjectionDefenseEnabled bool

	// Pruning configures tool-output pruning. Zero value leaves pruning
	// disabled (ThresholdPercent 0) with a default placeholder text.
	Pruning ToolOutputPruning
}

// NewContextWindow creates a new ContextWindow from the given config.
// Use SetHistoryMutation() to configure history mutation separately.
func NewContextWindow(cfg ContextWindowConfig) *ContextWindow {
	if cfg.ModelMeta.ContextWindow == 0 {
		cfg.ModelMeta.ContextWindow = 128000
	}
	if cfg.ModelMeta.OutputLimit == 0 {
		cfg.ModelMeta.OutputLimit = 4096
	}
	tracker := cfg.Tracker
	if tracker == nil {
		// Use a discard tracker to prevent nil dereference panics downstream.
		// Token accounting (compaction, fill warnings) will be silently disabled.
		tracker = llm.NewContextTokenTracker(&noopCounter{})
	}
	cw := &ContextWindow{
		systemPrompt:            cfg.SystemPrompt,
		modelMeta:               cfg.ModelMeta,
		tracker:                 tracker,
		thresholds:              cfg.Thresholds,
		strategy:                cfg.Strategy,
		injectionDefenseEnabled: cfg.InjectionDefenseEnabled,
		pruning:                 cfg.Pruning,
	}
	safetyMarginPercent := cfg.SafetyMarginPercent
	if safetyMarginPercent <= 0 {
		safetyMarginPercent = defaultSafetyMargin
	}
	cw.safetyMargin = safetyMarginPercent
	if cw.pruning.PlaceholderText == "" {
		cw.pruning.PlaceholderText = "[Tool output pruned — not available in context. Use search_facts for stored findings, or re-read the file if needed. Do NOT fabricate content you cannot see.]"
	}
	// Pruning.ThresholdPercent is left at zero-value (disabled) unless
	// explicitly set. The config layer sets the default (e.g. 50%) via
	// config.yaml.
	return cw
}

// SetHistoryMutation configures regular history mutation (tool result eviction,
// step status eviction, dedup). Must be called before the first BuildPrompt.
func (cw *ContextWindow) SetHistoryMutation(m HistoryMutation) {
	cw.mutation = m
}

// EffectiveMax returns the effective maximum token count for the context window,
// accounting for output limit and safety margin.
//
// NOTE: This is the ongoing tracking formula used during the agent loop to manage
// context growth. It differs from Router.validateContextWindow (which is a
// last-chance guard before API submission) — the slight numeric divergence from
// integer vs float rounding is intentional and acceptable.
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

// CheckFill returns the current fill status.
func (cw *ContextWindow) CheckFill() sdkagent.FillCheck {
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

	return sdkagent.FillCheck{Percent: percent, Status: status, Used: used, Max: effectiveMax}
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

// SetPriorConversation sets messages from previous exchanges that should
// appear in the prompt between the system message(s) and the current task
// content. This gives the agent dialogue context for follow-up messages.
// Pass nil or an empty slice to clear prior conversation.
func (cw *ContextWindow) SetPriorConversation(msgs []llm.Message) {
	cw.priorConversation = msgs
}

// AddStep appends a step to the history and updates the token tracker.
// The token estimate is approximate — it concatenates content strings without
// accounting for LLM message framing overhead (~4 tokens/message). The
// ContextTokenTracker.Correct() method compensates by adjusting delta based
// on actual API-reported token usage, so estimates converge over time.
func (cw *ContextWindow) AddStep(step sdkagent.Step) {
	cw.steps = append(cw.steps, step)
	// NOTE: compactedMessages is intentionally left untouched here. It holds a
	// frozen prefix from the last Compact() call; buildStepMessages appends
	// messages for steps[compactedThroughIndex:] (which now includes this new
	// step) on top of that prefix. Clearing it on every step would force
	// BuildPrompt to reconvert the entire, ever-growing step history on the
	// very next call — which re-crosses the compaction threshold almost
	// immediately and causes Compact() to be re-triggered on every step.
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

	// 1. System message(s) with systemPrompt.
	// When the prompt contains a CacheBreakMarker, split into multiple system
	// messages so that providers can apply prompt caching to the stable parts.
	if cw.systemPrompt != "" {
		parts := prompt.SplitCacheBreak(cw.systemPrompt)
		for _, part := range parts {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: part,
			})
		}
	}

	// 2. Prior conversation (previous user/assistant exchanges).
	// Inserted before the current task so the agent sees the dialogue context
	// leading up to the current message. Without this, a follow-up like
	// "implement variant a" has no referent.
	messages = append(messages, cw.priorConversation...)

	// 3. User message with task content (pre-formatted by caller)
	if cw.taskContent != "" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: cw.taskContent,
		})
	}

	// 4. System message with plan (pre-formatted by caller)
	if cw.planContent != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: cw.planContent,
		})
	}

	// 5. Step history
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
// If compaction has occurred, the frozen compactedMessages prefix is combined
// with freshly-built messages for the tail (steps[compactedThroughIndex:]) —
// i.e. everything added since the last Compact() call. Otherwise, all steps
// are converted directly.
func (cw *ContextWindow) buildStepMessages() []llm.Message {
	startIdx := 0
	var messages []llm.Message
	if cw.compactedMessages != nil {
		messages = append(messages, cw.compactedMessages...)
		startIdx = cw.compactedThroughIndex
	}

	// Determine which step indices have tool results and should be pruned
	protectedIndices := cw.computeProtectedIndices()

	if cw.pruning.Logger != nil && cw.pruning.KeepLastN > 0 {
		totalToolSteps := 0
		for _, step := range cw.steps {
			if step.Action.ID != "" {
				totalToolSteps++
			}
		}
		cw.pruning.Logger.Debug("tool output pruning summary",
			"totalSteps", len(cw.steps),
			"totalToolSteps", totalToolSteps,
			"protectedCount", len(protectedIndices),
			"prunedCount", totalToolSteps-len(protectedIndices),
			"keepLastN", cw.pruning.KeepLastN,
			"fillPercent", cw.FillPercent(),
			"thresholdPercent", cw.pruning.ThresholdPercent,
		)
	}

	for i := startIdx; i < len(cw.steps); {
		step := cw.steps[i]

		if step.ResponseGroup > 0 {
			messages = append(messages, cw.buildGroupedMessages(i, protectedIndices)...)
			i += cw.groupSize(i)
		} else {
			messages = append(messages, cw.buildStandaloneMessages(step, i, protectedIndices)...)
			i++
		}
	}
	return messages
}

// groupSize returns the number of consecutive steps with the same ResponseGroup
// starting at index start.
func (cw *ContextWindow) groupSize(start int) int {
	group := cw.steps[start].ResponseGroup
	end := start + 1
	for end < len(cw.steps) && cw.steps[end].ResponseGroup == group {
		end++
	}
	return end - start
}

// buildGroupedMessages builds assistant+tool+nudge messages for a ResponseGroup.
func (cw *ContextWindow) buildGroupedMessages(groupStart int, protectedIndices map[int]struct{}) []llm.Message {
	groupEnd := groupStart + cw.groupSize(groupStart)
	groupSteps := cw.steps[groupStart:groupEnd]

	// Build ONE assistant message with all tool calls
	assistantMsg := cw.buildAssistantMsg(groupSteps[0].Thought, groupSteps[0].ReasoningContent, groupSteps[0].ReasoningItems)
	for _, gs := range groupSteps {
		if gs.Action.ID != "" {
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, gs.Action)
		}
	}

	var out []llm.Message
	out = append(out, assistantMsg)

	// Add individual tool result messages
	for gi, gs := range groupSteps {
		idx := groupStart + gi
		if gs.Action.ID != "" {
			out = append(out, cw.buildToolMsg(gs, idx, protectedIndices))
		}
		// UserNudge only on last step of group
		if gi == len(groupSteps)-1 {
			if msg, ok := cw.buildNudgeMsg(gs.UserNudge); ok {
				out = append(out, msg)
			}
		}
	}

	return out
}

// buildStandaloneMessages builds assistant+tool+nudge messages for a single step.
func (cw *ContextWindow) buildStandaloneMessages(step sdkagent.Step, i int, protectedIndices map[int]struct{}) []llm.Message {
	assistantMsg := cw.buildAssistantMsg(step.Thought, step.ReasoningContent, step.ReasoningItems)
	if step.Action.ID != "" {
		assistantMsg.ToolCalls = []llm.ToolCall{step.Action}
	}

	var out []llm.Message
	out = append(out, assistantMsg)

	if step.Action.ID != "" {
		out = append(out, cw.buildToolMsg(step, i, protectedIndices))
	}

	if msg, ok := cw.buildNudgeMsg(step.UserNudge); ok {
		out = append(out, msg)
	}

	return out
}

// buildAssistantMsg creates a normalized assistant message from thought, reasoning,
// and (for the Responses API) reasoning items. Empty content is replaced with a
// placeholder to prevent API 400 errors.
func (cw *ContextWindow) buildAssistantMsg(thought, reasoningContent string, reasoningItems []llm.ReasoningItem) llm.Message {
	msg := llm.Message{
		Role:             "assistant",
		Content:          strings.TrimRight(thought, invisibleChars),
		ReasoningContent: reasoningContent,
		ReasoningItems:   reasoningItems,
	}
	if msg.Content == "" {
		msg.Content = "(proceeding)"
	}
	return msg
}

// buildToolMsg creates a tool-role message with history mutation, pruning,
// and injection defense applied. History mutation (eviction to cache reference,
// step-status eviction, dedup) runs first, then pruning (fill-based placeholder),
// then injection defense wrapping.
func (cw *ContextWindow) buildToolMsg(step sdkagent.Step, idx int, protectedIndices map[int]struct{}) llm.Message {
	observation := strings.TrimRight(step.Observation, invisibleChars)
	if observation == "" {
		observation = "(no output)"
	}

	// Apply history mutation first (age-based, preserves info via cache).
	// Skip mutation for protected indices (they are explicitly kept).
	mutated := false
	if _, protected := protectedIndices[idx]; !protected {
		original := observation
		observation = cw.applyHistoryMutation(step, idx, observation)
		mutated = observation != original
	}

	// Apply pruning: use placeholder for non-protected tool outputs.
	// Skip pruning when history mutation already replaced the content with
	// a cache reference or step-status eviction text — those are compact
	// placeholders that preserve recoverability (the cache hash lets the LLM
	// retrieve evicted content via tool_result_read). Overwriting them with
	// the generic pruning placeholder would destroy the hash and break the
	// information-preservation guarantee of history mutation.
	if _, protected := protectedIndices[idx]; !protected && cw.pruning.KeepLastN > 0 && !mutated {
		observation = cw.pruning.PlaceholderText
	}

	// Apply prompt injection defense: wrap untrusted tool output
	if cw.injectionDefenseEnabled && step.IsUntrusted {
		observation = security.WrapUntrustedContent(observation, step.Action.Name, nil)
	}

	return llm.Message{
		Role:       "tool",
		Content:    observation,
		ToolCallID: step.Action.ID,
	}
}

// applyHistoryMutation replaces old tool results with cache references,
// evicts bookkeeping outputs, and deduplicates repeated reads.
// Returns the (possibly mutated) observation string.
func (cw *ContextWindow) applyHistoryMutation(step sdkagent.Step, idx int, observation string) string {
	// Protected tools are exempt from mutation.
	if cw.isProtectedTool(step.Action.Name) {
		return observation
	}

	toolName := step.Action.Name

	// Evict update_checklist results immediately (pure bookkeeping).
	// Also matches the legacy set_step_status name for old session history.
	if cw.mutation.EvictStepStatus && (toolName == "update_checklist" || toolName == "set_step_status") {
		return stepStatusEvictedText
	}

	// Age-based eviction: replace old tool results with cache reference.
	if cw.mutation.ToolResultEvictionStep > 0 && step.CacheHash != "" {
		age := len(cw.steps) - idx
		if age > cw.mutation.ToolResultEvictionStep {
			if cw.mutation.Logger != nil {
				cw.mutation.Logger.Debug("tool result evicted to cache reference",
					"stepIndex", idx, "age", age, "tool", toolName, "hash", step.CacheHash,
				)
			}
			return evictionReferenceText(step.CacheHash)
		}
	}

	// Dedup repeated reads: if the same file (path+mtime) was read earlier,
	// replace this result with a reference. Detection is via cache hash —
	// identical content produces the same hash, so a repeated read of an
	// unchanged file will have the same CacheHash as the earlier step.
	if cw.mutation.DedupRepeatedReads && step.CacheHash != "" && cw.isEarlierDuplicateHash(idx, step.CacheHash) {
		if cw.mutation.Logger != nil {
			cw.mutation.Logger.Debug("duplicate tool result replaced with reference",
				"stepIndex", idx, "tool", toolName, "hash", step.CacheHash,
			)
		}
		return evictionReferenceText(step.CacheHash)
	}

	return observation
}

// isProtectedTool checks if the tool name is in the pruning ProtectedTools list.
func (cw *ContextWindow) isProtectedTool(toolName string) bool {
	for _, t := range cw.pruning.ProtectedTools {
		if t == toolName {
			return true
		}
	}
	return false
}

// isEarlierDuplicateHash checks if a step before idx has the same CacheHash.
func (cw *ContextWindow) isEarlierDuplicateHash(idx int, hash string) bool {
	for i := 0; i < idx; i++ {
		if cw.steps[i].CacheHash == hash && cw.steps[i].CacheHash != "" {
			return true
		}
	}
	return false
}

// buildNudgeMsg creates a user message for step-limit/retry nudges.
// Returns the message and true if the nudge has non-empty content.
func (cw *ContextWindow) buildNudgeMsg(nudge string) (llm.Message, bool) {
	content := strings.TrimRight(nudge, invisibleChars)
	if content == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: "user", Content: content}, true
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

	// Skip pruning when context fill is below threshold — all tool outputs preserved.
	if cw.pruning.ThresholdPercent > 0 && cw.FillPercent() < cw.pruning.ThresholdPercent {
		if cw.pruning.Logger != nil {
			cw.pruning.Logger.Debug("pruning skipped: context fill below threshold",
				"fillPercent", cw.FillPercent(),
				"thresholdPercent", cw.pruning.ThresholdPercent,
			)
		}
		for i, step := range cw.steps {
			if step.Action.ID != "" {
				protected[i] = struct{}{}
			}
		}
		return protected
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

// VulnerableOutputs returns the list of tool outputs that will be pruned on the
// next pruning cycle. These are non-protected outputs outside the KeepLastN window.
// Returns nil when pruning is inactive (KeepLastN <= 0, below threshold, or no steps).
func (cw *ContextWindow) VulnerableOutputs() []sdkagent.VulnerableOutput {
	if cw.pruning.KeepLastN <= 0 || len(cw.steps) == 0 {
		return nil
	}

	// If fill is below pruning threshold, nothing is vulnerable.
	if cw.pruning.ThresholdPercent > 0 && cw.FillPercent() < cw.pruning.ThresholdPercent {
		return nil
	}

	protected := cw.computeProtectedIndices()

	var vulnerable []sdkagent.VulnerableOutput
	for i, step := range cw.steps {
		if step.Action.ID == "" {
			continue // not a tool-result step
		}
		if _, isProtected := protected[i]; isProtected {
			continue
		}
		vulnerable = append(vulnerable, sdkagent.VulnerableOutput{
			ToolName:  step.Action.Name,
			InputHint: extractInputHint(step.Action.Input),
		})
	}
	return vulnerable
}

// extractInputHint extracts a human-readable summary from tool input JSON.
// Tries common parameter names, falls back to first short string value.
func extractInputHint(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}

	// Try common parameter names in priority order.
	for _, key := range []string{"path", "file_path", "file", "pattern", "query", "command", "url"} {
		if v, ok := m[key]; ok {
			s := fmt.Sprint(v)
			if len(s) > 60 {
				// TruncateUTF8 avoids splitting a multi-byte rune at the
				// byte boundary, which would produce invalid UTF-8.
				return strutil.TruncateUTF8(s, 57) + "..."
			}
			return s
		}
	}

	// Fallback: first short string value (sorted for determinism).
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" && len(s) <= 60 {
			return s
		}
	}
	return ""
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

	// Compact steps using the strategy.
	// Steps are preserved after compaction so that diagnostics (VulnerableOutputs,
	// computeProtectedIndices) remain functional. compactedMessages becomes the
	// frozen prefix that buildStepMessages combines with any steps added
	// afterward (see compactedThroughIndex), so re-compaction is only needed
	// once the new tail grows the fill back past the threshold — not on every
	// subsequent step.
	cw.compactedMessages = cw.strategy.Compact(ctx, cw.steps, budgetTokens)
	cw.compactedThroughIndex = len(cw.steps)

	// Estimate after-compaction fill
	effectiveMax := cw.EffectiveMax()
	afterPercent := float64(0)
	baseTokens := cw.tracker.EstimateMessages([]llm.Message{
		{Role: "system", Content: cw.systemPrompt},
		{Role: "user", Content: cw.taskContent},
	})
	if cw.planContent != "" {
		baseTokens += cw.tracker.EstimateMessages([]llm.Message{
			{Role: "system", Content: cw.planContent},
		})
	}
	if len(cw.priorConversation) > 0 {
		baseTokens += cw.tracker.EstimateMessages(cw.priorConversation)
	}
	compactedTokens := cw.tracker.EstimateMessages(cw.compactedMessages)
	estimatedTotal := baseTokens + compactedTokens
	if effectiveMax > 0 {
		afterPercent = float64(estimatedTotal) / float64(effectiveMax) * 100
	}

	// Sync the tracker with the post-compaction estimate so that CheckFill()
	// immediately reflects the reduced context size instead of the stale
	// pre-compaction value (lastKnownUsed from the last API call plus any
	// pendingDelta accumulated since). Without this, callers checking fill
	// right after Compact() (e.g. reactive/emergency compaction handling)
	// would see the old fill and could falsely conclude the context is still
	// full or re-trigger compaction. Correct() overwrites lastKnownUsed and
	// resets pendingDelta; the next real API-reported usage will overwrite
	// this estimate through the normal correction path.
	cw.tracker.Correct(estimatedTotal)

	return &CompactionResult{
		BeforePercent: beforeFill.Percent,
		AfterPercent:  afterPercent,
	}
}
