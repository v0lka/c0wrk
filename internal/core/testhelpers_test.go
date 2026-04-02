package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// mockLLMCaller is a unified mock implementation of LLMCaller for testing.
// It supports multiple configurations:
//   - responses slice: returns responses in order, cycling through callIdx
//   - callFn: custom function for more complex behavior (takes precedence if set)
//   - err: error to return from all calls (if set and callFn is nil)
type mockLLMCaller struct {
	// responses to return in order (cycles through callIdx)
	responses []*llm.ChatResponse
	callIdx   int

	// recorded calls for assertions
	calls []llm.ChatRequest
	roles []string

	// optional custom call function (takes precedence over responses)
	callFn func(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error)

	// optional error to return
	err error
}

func (m *mockLLMCaller) Call(ctx context.Context, role string, req llm.ChatRequest) (*llm.ChatResponse, error) {
	// Record the call
	m.calls = append(m.calls, req)
	m.roles = append(m.roles, role)

	// If callFn is set, use it
	if m.callFn != nil {
		return m.callFn(ctx, role, req)
	}

	// If error is set, return it
	if m.err != nil {
		return nil, m.err
	}

	// Return from responses slice
	if m.callIdx >= len(m.responses) {
		// Return empty response if we've exhausted responses
		return &llm.ChatResponse{
			Message:    llm.Message{Role: "assistant", Content: ""},
			StopReason: "end_turn",
		}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

// lastCall returns the last recorded call request, or empty if none
func (m *mockLLMCaller) lastCall() llm.ChatRequest {
	if len(m.calls) == 0 {
		return llm.ChatRequest{}
	}
	return m.calls[len(m.calls)-1]
}

// lastRole returns the last recorded role, or empty if none
func (m *mockLLMCaller) lastRole() string {
	if len(m.roles) == 0 {
		return ""
	}
	return m.roles[len(m.roles)-1]
}

// mockToolExecutor is a unified mock implementation of ToolExecutor for testing.
type mockToolExecutor struct {
	// results maps tool names to their results
	results map[string]tools.ToolResult

	// calls records all tool names that were called
	calls []string

	// inputs records all inputs that were passed
	inputs []json.RawMessage

	// optional custom execute function (takes precedence if set)
	executeFn func(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error)
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	// Record the call
	m.calls = append(m.calls, name)
	m.inputs = append(m.inputs, input)

	// If executeFn is set, use it
	if m.executeFn != nil {
		return m.executeFn(ctx, name, input)
	}

	// Return from results map
	if result, ok := m.results[name]; ok {
		return result, nil
	}

	// Default response
	return tools.ToolResult{Content: "mock result for " + name}, nil
}

// mockContextManager is a mock implementation of ContextManager for testing.
type mockContextManager struct {
	// steps records all steps added
	steps []Step

	// reflections set via SetReflections
	reflections []Reflection

	// criteria set via SetTask
	criteria []AcceptanceCriterion

	// strategy set via SetStrategy
	strategy CompactionStrategy

	// configuration flags
	needsCompaction bool
	compactCalled   bool

	// optional prompt content
	systemPrompt   string
	taskDefinition string

	// optional custom BuildPrompt function
	buildPromptFn func() []llm.Message

	// optional custom CheckFill function
	checkFillFn func() FillCheck
}

func (m *mockContextManager) BuildPrompt() []llm.Message {
	if m.buildPromptFn != nil {
		return m.buildPromptFn()
	}

	messages := []llm.Message{}
	if m.systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: m.systemPrompt})
	}
	if m.taskDefinition != "" {
		messages = append(messages, llm.Message{Role: "user", Content: m.taskDefinition})
	}
	return messages
}

func (m *mockContextManager) AddStep(step Step) {
	m.steps = append(m.steps, step)
}

func (m *mockContextManager) NeedsCompaction() bool {
	return m.needsCompaction
}

func (m *mockContextManager) Compact() {
	m.compactCalled = true
}

func (m *mockContextManager) SetReflections(reflections []Reflection) {
	m.reflections = reflections
}

func (m *mockContextManager) SetTask(task string, criteria []AcceptanceCriterion) {
	m.taskDefinition = task
	m.criteria = criteria
}

func (m *mockContextManager) SetStrategy(s CompactionStrategy) {
	m.strategy = s
}

func (m *mockContextManager) CheckFill() FillCheck {
	if m.checkFillFn != nil {
		return m.checkFillFn()
	}
	if m.needsCompaction {
		return FillCheck{Percent: 85, Status: "compact", Used: 85000, Max: 100000}
	}
	return FillCheck{Percent: 0, Status: "ok", Used: 0, Max: 100000}
}

func (m *mockContextManager) CorrectTokenCount(apiInputTokens int) {}

func (m *mockContextManager) FillPercent() float64 { return 0 }

func (m *mockContextManager) AvailableTokens() int {
	return 100000 // large default so existing tests aren't affected
}

// mockEmitter is a mock implementation of Emitter for testing.
// It tracks all calls for assertion purposes.
type mockEmitter struct {
	assistantChunks   []string
	assistantDones    []struct{ content string; inputTokens, outputTokens int }
	planStepStarts    []struct{ stepID, description string }
	planStepCompletes []struct{ stepID string; success bool; duration time.Duration }
}

func (m *mockEmitter) Routing(_, _, _ string)                                              {}
func (m *mockEmitter) PlanGenerated(_ int, _ []PlanStepEvent)                              {}
func (m *mockEmitter) PlanStepStart(stepID, description string) {
	m.planStepStarts = append(m.planStepStarts, struct{ stepID, description string }{stepID, description})
}
func (m *mockEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration) {
	m.planStepCompletes = append(m.planStepCompletes, struct {
		stepID   string
		success  bool
		duration time.Duration
	}{stepID, success, duration})
}
func (m *mockEmitter) StepStart(_ int)                                                     {}
func (m *mockEmitter) Thought(_ int, _ string)                                             {}
func (m *mockEmitter) ToolCall(_ int, _, _ string)                                         {}
func (m *mockEmitter) ToolResult(_, _ int, _ string)                                       {}
func (m *mockEmitter) StepComplete(_ int, _ time.Duration)                                 {}
func (m *mockEmitter) SubAgentLaunch(_, _ string)                                          {}
func (m *mockEmitter) SubAgentComplete(_ string, _ bool, _ time.Duration)                  {}
func (m *mockEmitter) Evaluation(_, _ int, _ []EvalCriterionEvent)                         {}
func (m *mockEmitter) Reflection(_ string, _ []string, _, _ int)                           {}
func (m *mockEmitter) Retry(_, _ int)                                                      {}
func (m *mockEmitter) Escalation(_, _ string)                                              {}
func (m *mockEmitter) ACExtracted(_ int)                                                   {}
func (m *mockEmitter) AssistantChunk(content string) {
	m.assistantChunks = append(m.assistantChunks, content)
}
func (m *mockEmitter) AssistantDone(content string, inputTokens, outputTokens int) {
	m.assistantDones = append(m.assistantDones, struct {
		content      string
		inputTokens  int
		outputTokens int
	}{content, inputTokens, outputTokens})
}
func (m *mockEmitter) ContextFill(_ float64, _, _ int, _ string)                           {}

// routerCallTracker helps track the three-phase AC extraction flow in tests.
// It distinguishes between:
//   1. ExtractRaw (Phase 1) - first call, returns []RawCriterion
//   2. Route - second call, returns RoutingDecision
//   3. Enrich (Phase 2) - subsequent calls with "Domain:", returns []AcceptanceCriterion
type routerCallTracker struct {
	callCount int
}

// nextCall determines what type of router call this is based on call count and message content.
// Returns one of: "extract_raw", "route", "enrich"
func (t *routerCallTracker) nextCall(req llm.ChatRequest) string {
	t.callCount++
	
	// First call is always ExtractRaw
	if t.callCount == 1 {
		return "extract_raw"
	}
	
	// Check if this has "Domain:" in message (indicates Enrich)
	hasDomain := false
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "Domain:") {
			hasDomain = true
			break
		}
	}
	
	if hasDomain {
		return "enrich"
	}
	
	// Otherwise it's Route
	return "route"
}
