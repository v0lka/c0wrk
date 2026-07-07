package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// --- Mock LLMCaller ---

// mockLLMCaller returns predefined responses in sequence.
type mockLLMCaller struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	errors    []error
	callIdx   int
	calls     []llm.ChatRequest // recorded calls
}

func (m *mockLLMCaller) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	idx := m.callIdx
	m.callIdx++

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	// Default: return end_turn with no tool calls
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "default response"},
		StopReason: "end_turn",
	}, nil
}

// --- Mock ToolExecutor ---

// mockToolExecutor returns predefined results by tool name.
type mockToolExecutor struct {
	mu      sync.Mutex
	results map[string]tools.ToolResult
	errors  map[string]error
	calls   []toolExecCall
}

type toolExecCall struct {
	Name  string
	Input json.RawMessage
}

func newMockToolExecutor() *mockToolExecutor {
	return &mockToolExecutor{
		results: make(map[string]tools.ToolResult),
		errors:  make(map[string]error),
	}
}

func (m *mockToolExecutor) Execute(_ context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, toolExecCall{Name: name, Input: input})
	if err, ok := m.errors[name]; ok {
		return tools.ToolResult{}, err
	}
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return tools.ToolResult{Content: "ok"}, nil
}

func (m *mockToolExecutor) GetToolSource(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.results[name]; ok {
		return "mock"
	}
	return "core"
}

func (m *mockToolExecutor) IsToolUntrusted(name string) bool {
	return false
}

// --- Mock ContextManager ---

type mockContextManager struct {
	mu              sync.Mutex
	messages        []llm.Message
	steps           []Step
	compactCalled   int
	fillCheck       FillCheck
	availableTokens int
	fillPercent     float64
	correctedTokens int
	strategySet     bool
}

func newMockContextManager() *mockContextManager {
	return &mockContextManager{
		messages:        []llm.Message{{Role: "system", Content: "you are helpful"}},
		availableTokens: 100000,
		fillCheck:       FillCheck{Percent: 10, Status: "ok", Used: 1000, Max: 100000},
	}
}

func (m *mockContextManager) BuildPrompt() []llm.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages
}

func (m *mockContextManager) AddStep(step Step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps = append(m.steps, step)
}

func (m *mockContextManager) Compact(_ context.Context) *CompactionResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactCalled++
	return nil
}

func (m *mockContextManager) SetStrategy(_ CompactionStrategy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strategySet = true
}

func (m *mockContextManager) CheckFill() FillCheck {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fillCheck
}

func (m *mockContextManager) CorrectTokenCount(apiInputTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.correctedTokens = apiInputTokens
}

func (m *mockContextManager) FillPercent() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fillPercent
}

func (m *mockContextManager) AvailableTokens() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.availableTokens
}

func (m *mockContextManager) OutputLimit() int {
	return 8192
}

func (m *mockContextManager) VulnerableOutputs() []VulnerableOutput {
	return nil
}

// --- Mock TokenCounter ---

type mockTokenCounter struct{}

func (m *mockTokenCounter) Count(text string) int {
	return len(text) / 4
}

func (m *mockTokenCounter) CountMessages(msgs []llm.Message) int {
	total := 0
	for _, msg := range msgs {
		total += m.Count(msg.Content)
	}
	return total
}

// --- Mock Events (recording) ---

type recordingEvents struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingEvents) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, name)
}

func (r *recordingEvents) StepStart(stepNum int) {
	r.record(fmt.Sprintf("StepStart:%d", stepNum))
}

func (r *recordingEvents) Thought(stepNum int, content, reasoning string) {
	r.record(fmt.Sprintf("Thought:%d", stepNum))
}

func (r *recordingEvents) ToolCall(stepNum, callIdx int, toolName, argsPreview, source string) {
	r.record(fmt.Sprintf("ToolCall:%d:%s:%s", stepNum, toolName, source))
}

func (r *recordingEvents) ToolResult(stepNum, callIdx, resultLen int, preview string, _ bool) {
	r.record(fmt.Sprintf("ToolResult:%d", stepNum))
}

func (r *recordingEvents) StepComplete(stepNum int, _ time.Duration) {
	r.record(fmt.Sprintf("StepComplete:%d", stepNum))
}

func (r *recordingEvents) SubAgentLaunch(stepID, description string) {
	r.record("SubAgentLaunch:" + stepID)
}

func (r *recordingEvents) SubAgentComplete(stepID string, success bool, _ time.Duration) {
	r.record(fmt.Sprintf("SubAgentComplete:%s:%v", stepID, success))
}

func (r *recordingEvents) AssistantChunk(content string) {
	r.record("AssistantChunk")
}

func (r *recordingEvents) AssistantDone(content string, inputTokens, outputTokens int) {
	r.record("AssistantDone")
}

func (r *recordingEvents) Finishing(stepNum int, summary string) {
	r.record(fmt.Sprintf("Finishing:%d", stepNum))
}

func (r *recordingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	r.record("ContextFill:" + status)
}

func (r *recordingEvents) ContextCompaction(_, _ float64, _ string) {}

func (r *recordingEvents) ExecutorDiagnostic(_ int, _ string, _ map[string]any) {}

// --- Helper to build LLM responses ---

func llmResponseWithToolCall(thought, toolName string, toolInput json.RawMessage) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: thought,
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: toolName, Input: toolInput},
			},
		},
		StopReason: "tool_use",
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
}

func llmResponseFinish(thought, answer string) *llm.ChatResponse {
	input, _ := json.Marshal(map[string]string{"answer": answer})
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: thought,
			ToolCalls: []llm.ToolCall{
				{ID: "call_finish", Name: "finish", Input: input},
			},
		},
		StopReason: "tool_use",
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
}

func llmResponseEndTurn(content string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: content,
		},
		StopReason: "end_turn",
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
}

// --- Counting ToolExecutor for tests that need varying results ---

// countingToolExecutor returns different results based on call count.
type countingToolExecutor struct {
	mu        sync.Mutex
	callCount int
	results   map[int]tools.ToolResult // callCount -> result
	calls     []toolExecCall
}

func (m *countingToolExecutor) Execute(_ context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.calls = append(m.calls, toolExecCall{Name: name, Input: input})
	if r, ok := m.results[m.callCount]; ok {
		return r, nil
	}
	return tools.ToolResult{Content: "ok"}, nil
}

func (m *countingToolExecutor) GetToolSource(name string) string {
	return "core"
}

func (m *countingToolExecutor) IsToolUntrusted(name string) bool {
	return false
}

// llmResponseWithMultipleToolCalls creates a ChatResponse with multiple tool calls.
func llmResponseWithMultipleToolCalls(thought string, calls []llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   thought,
			ToolCalls: calls,
		},
		StopReason: "tool_use",
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
}

// --- Test HITLHandler adapter providing only OnStepLimit (step-limit-only usage) ---

// testStepLimitAdapter adapts a step-limit-only function to the HITLHandler interface.
// OnToolCall always allows all tool calls; OnStepLimit delegates to the wrapped function.
type testStepLimitAdapter struct {
	fn func(ctx context.Context, currentStep, maxSteps int, reason string) (StepLimitResponse, error)
}

func (a *testStepLimitAdapter) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (*HITLToolDecision, error) {
	d := allowDecisionSentinel
	return &d, nil
}

func (a *testStepLimitAdapter) OnStepLimit(ctx context.Context, currentStep, maxSteps int, reason string) (StepLimitResponse, error) {
	return a.fn(ctx, currentStep, maxSteps, reason)
}

// newExecutorDefaultHITL creates an Executor with a nil HITLHandler (uses NoopHITLHandler default).
// Convenience wrapper for tests that don't need custom HITL behavior.
func newExecutorDefaultHITL(llmCaller LLMCaller, toolRegistry ToolExecutor, counter llm.TokenCounter, maxSteps int, emitter Events, suppressAssistantEvents bool, toolResultBudget ToolResultBudget, circuitBreaker CircuitBreakerConfig) *Executor {
	return NewExecutor(llmCaller, toolRegistry, maxSteps,
		WithTokenCounter(counter),
		WithEvents(emitter),
		WithSuppressAssistantEvents(suppressAssistantEvents),
		WithToolResultBudget(toolResultBudget),
		WithCircuitBreaker(circuitBreaker),
	)
}
