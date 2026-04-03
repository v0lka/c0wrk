// Package session provides session-scoped event emission for the desktop UI.
package session

import (
	"sync"
	"time"

	"github.com/user/agent/internal/core"
)

// Event represents a structured event emitted during agent execution.
type Event struct {
	SessionID string      `json:"session_id"`
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
}

// EventEmitter implements core.Emitter and routes events to a callback function.
type EventEmitter struct {
	sessionID string
	emit      func(Event)
	mu        sync.Mutex
}

// NewEventEmitter creates a new EventEmitter for a session.
func NewEventEmitter(sessionID string, emit func(Event)) *EventEmitter {
	return &EventEmitter{
		sessionID: sessionID,
		emit:      emit,
	}
}

// ensure EventEmitter implements core.Emitter at compile time.
var _ core.Emitter = (*EventEmitter)(nil)

// Routing emits a routing decision event.
func (e *EventEmitter) Routing(mode, domain, complexity string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "routing",
		Data: map[string]string{
			"mode":       mode,
			"domain":     domain,
			"complexity": complexity,
		},
	})
}

// PlanGenerated emits a plan generation event.
func (e *EventEmitter) PlanGenerated(stepCount int, steps []core.PlanStepEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "plan_generated",
		Data: map[string]interface{}{
			"step_count": stepCount,
			"steps":      steps,
		},
	})
}

// PlanStepStart emits a plan step start event.
func (e *EventEmitter) PlanStepStart(stepID, description string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_start",
		Data: map[string]string{
			"step_id":     stepID,
			"description": description,
		},
	})
}

// PlanStepComplete emits a plan step completion event.
func (e *EventEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_complete",
		Data: map[string]interface{}{
			"step_id":  stepID,
			"success":  success,
			"duration": duration.Milliseconds(),
		},
	})
}

// StepStart emits a step start event.
func (e *EventEmitter) StepStart(stepNum int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "step_start",
		Data: map[string]int{
			"step_num": stepNum,
		},
	})
}

// Thought emits a thought event for LLM reasoning.
func (e *EventEmitter) Thought(stepNum int, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "thought",
		Data: map[string]interface{}{
			"step_num": stepNum,
			"content":  content,
		},
	})
}

// ToolCall emits a tool call event.
func (e *EventEmitter) ToolCall(stepNum int, toolName, argsPreview string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "tool_call",
		Data: map[string]interface{}{
			"step": stepNum,
			"tool": toolName,
			"args": argsPreview,
		},
	})
}

// ToolResult emits a tool result event.
func (e *EventEmitter) ToolResult(stepNum, resultLen int, preview string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "tool_result",
		Data: map[string]interface{}{
			"step":           stepNum,
			"result_len":     resultLen,
			"result_preview": preview,
		},
	})
}

// StepComplete emits a step completion event.
func (e *EventEmitter) StepComplete(stepNum int, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "step_complete",
		Data: map[string]interface{}{
			"step_num": stepNum,
			"duration": duration.Milliseconds(),
		},
	})
}

// SubAgentLaunch emits a subagent launch event.
func (e *EventEmitter) SubAgentLaunch(stepID, description string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "subagent_launch",
		Data: map[string]string{
			"step_id":     stepID,
			"description": description,
		},
	})
}

// SubAgentComplete emits a subagent completion event.
func (e *EventEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "subagent_complete",
		Data: map[string]interface{}{
			"step_id":  stepID,
			"success":  success,
			"duration": duration.Milliseconds(),
		},
	})
}

// Evaluation emits an evaluation event.
func (e *EventEmitter) Evaluation(passed, total int, criteria []core.EvalCriterionEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "evaluation",
		Data: map[string]interface{}{
			"passed":   passed,
			"total":    total,
			"criteria": criteria,
		},
	})
}

// Reflection emits a reflection event.
func (e *EventEmitter) Reflection(summary string, insights []string, attempt, maxAttempts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "reflection",
		Data: map[string]interface{}{
			"summary":      summary,
			"insights":     insights,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		},
	})
}

// Retry emits a retry event.
func (e *EventEmitter) Retry(attempt, maxAttempts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "retry",
		Data: map[string]int{
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		},
	})
}

// Escalation emits an escalation event.
func (e *EventEmitter) Escalation(fromMode, toMode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "escalation",
		Data: map[string]string{
			"from_mode": fromMode,
			"to_mode":   toMode,
		},
	})
}

// ACExtracted emits an acceptance criteria extraction event.
func (e *EventEmitter) ACExtracted(count int, criteria []core.EvalCriterionEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "ac_extracted",
		Data: map[string]interface{}{
			"count":    count,
			"criteria": criteria,
		},
	})
}

// AssistantChunk emits an assistant response chunk for streaming.
func (e *EventEmitter) AssistantChunk(content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "assistant_chunk",
		Data: map[string]string{
			"content": content,
		},
	})
}

// AssistantDone emits an assistant response completion event.
func (e *EventEmitter) AssistantDone(fullContent string, inputTokens, outputTokens int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "assistant_done",
		Data: map[string]interface{}{
			"content":       fullContent,
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	})
}

// ContextFill emits a context fill status event.
func (e *EventEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "context_fill",
		Data: map[string]interface{}{
			"fill_percent": fillPercent,
			"used_tokens":  usedTokens,
			"max_tokens":   maxTokens,
			"status":       status,
		},
	})
}

// Service emits a general service message without metadata.
func (e *EventEmitter) Service(content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "service",
		Data: map[string]interface{}{
			"content": content,
		},
	})
}

// ServiceWithMeta emits a service message with metadata for frontend filtering.
func (e *EventEmitter) ServiceWithMeta(content string, meta map[string]interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data := map[string]interface{}{
		"content": content,
	}
	for k, v := range meta {
		data[k] = v
	}
	e.emit(Event{
		SessionID: e.sessionID,
		Type:      "service",
		Data:      data,
	})
}
