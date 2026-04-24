package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
)

// mockLLMCaller is a unified mock implementation of LLMCaller for testing.
// It supports multiple configurations:
//   - responses slice: returns responses in order, cycling through callIdx
//   - callFn: custom function for more complex behavior (takes precedence if set)
//   - err: error to return from all calls (if set and callFn is nil)
type mockLLMCaller struct {
	mu sync.Mutex

	// responses to return in order (cycles through callIdx)
	responses []*llm.ChatResponse
	callIdx   int

	// recorded calls for assertions
	calls []llm.ChatRequest

	// optional custom call function (takes precedence over responses)
	callFn func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)

	// optional error to return
	err error
}

func (m *mockLLMCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	// Record the call
	m.mu.Lock()
	m.calls = append(m.calls, req)
	m.mu.Unlock()

	// If callFn is set, use it
	if m.callFn != nil {
		return m.callFn(ctx, req)
	}

	// If error is set, return it
	if m.err != nil {
		return nil, m.err
	}

	// Return from responses slice
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return llm.ChatRequest{}
	}
	return m.calls[len(m.calls)-1]
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

func (m *mockToolExecutor) GetToolSource(name string) string {
	if _, ok := m.results[name]; ok {
		return "mock"
	}
	return "core"
}

// mockContextManager is a mock implementation of ContextManager for testing.
type mockContextManager struct {
	// steps records all steps added
	steps []Step

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

func (m *mockContextManager) Compact(ctx context.Context) *CompactionResult {
	m.compactCalled = true
	return nil
}

func (m *mockContextManager) SetTask(task string) {
	m.taskDefinition = task
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

func (m *mockContextManager) OutputLimit() int {
	return 8192
}

// mockEmitter is a mock implementation of Emitter for testing.
// It tracks all calls for assertion purposes.
type mockEmitter struct {
	assistantChunks []string
	assistantDones  []struct {
		content                   string
		inputTokens, outputTokens int
	}
	planStepStarts    []struct{ stepID, description, summary string }
	planStepCompletes []struct {
		stepID   string
		success  bool
		duration time.Duration
	}
}

func (m *mockEmitter) Routing(_, _, _ string)                 {}
func (m *mockEmitter) PlanGenerated(_ int, _ []PlanStepEvent) {}
func (m *mockEmitter) PlanStepStart(stepID, description, summary string) {
	m.planStepStarts = append(m.planStepStarts, struct{ stepID, description, summary string }{stepID, description, summary})
}
func (m *mockEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	m.planStepCompletes = append(m.planStepCompletes, struct {
		stepID   string
		success  bool
		duration time.Duration
	}{stepID, success, duration})
}
func (m *mockEmitter) StepStart(_ int)                                    {}
func (m *mockEmitter) Thought(_ int, _, _ string)                         {}
func (m *mockEmitter) ToolCall(_, _ int, _, _, _ string)                  {}
func (m *mockEmitter) ToolResult(_, _, _ int, _ string)                   {}
func (m *mockEmitter) StepComplete(_ int, _ time.Duration)                {}
func (m *mockEmitter) SubAgentLaunch(_, _ string)                         {}
func (m *mockEmitter) SubAgentComplete(_ string, _ bool, _ time.Duration) {}
func (m *mockEmitter) Reflection(_ *orchestration.Reflection, _, _ int)   {}
func (m *mockEmitter) Retry(_, _ int)                                     {}
func (m *mockEmitter) StepRetry(_ string, _, _ int)                       {}
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
func (m *mockEmitter) ContextFill(_ float64, _, _ int, _, _ string) {}
func (m *mockEmitter) ContextCompaction(_, _ float64, _ string)     {}
func (m *mockEmitter) Service(_ string)                             {}
func (m *mockEmitter) ServiceWithMeta(_ string, _ map[string]any)   {}

func (m *mockEmitter) ReplanFailed(_ error)                                 {}
func (m *mockEmitter) FileRollbackError(_ string, _ error)                  {}
func (m *mockEmitter) SkillsActivated(_ []string)                            {}
func (m *mockEmitter) ExecutorDiagnostic(_ int, _ string, _ map[string]any) {}
func (m *mockEmitter) Finishing(_ int, _ string)                            {}

// ---------------------------------------------------------------------------
// testPersistableBlackboard — a minimal PersistableBlackboard for core tests
// ---------------------------------------------------------------------------

// testPersistableBlackboard wraps a MapBlackboard and records persistence calls.
// Used by orchestrator tests that exercise continuation/restore flows.
type testPersistableBlackboard struct {
	*MapBlackboard
	taskID string
	store  TaskPersistence

	reactivated bool
	completed   bool
	failed      bool
}

var _ PersistableBlackboard = (*testPersistableBlackboard)(nil)

func (t *testPersistableBlackboard) SetEmitter(_ Emitter) {}
func (t *testPersistableBlackboard) SetRouting(routing *RoutingDecision) {
	if t.store != nil {
		_ = t.store.PersistRouting(t.taskID, routing)
	}
}
func (t *testPersistableBlackboard) CompleteTask(attemptCount int) {
	t.completed = true
	if t.store != nil {
		_ = t.store.PersistCompletion(t.taskID, t.GetFinalResult(), attemptCount)
	}
}
func (t *testPersistableBlackboard) FailTask() {
	t.failed = true
	if t.store != nil {
		_ = t.store.PersistFailure(t.taskID)
	}
}
func (t *testPersistableBlackboard) ReactivateTask() {
	t.reactivated = true
	if t.store != nil {
		_ = t.store.ReactivateTask(t.taskID)
	}
}
func (t *testPersistableBlackboard) TaskID() string { return t.taskID }

// testBlackboardRestoreFunc returns a BlackboardRestoreFunc that creates
// a testPersistableBlackboard from the mock store.
func testBlackboardRestoreFunc() BlackboardRestoreFunc {
	return func(taskID, sessionID string, store TaskPersistence, _ *slog.Logger, opts ...MapBlackboardOption) (PersistableBlackboard, error) {
		state, err := store.LoadTaskState(taskID)
		if err != nil {
			return nil, fmt.Errorf("failed to load task state: %w", err)
		}
		if state == nil {
			return nil, nil
		}

		mb := NewMapBlackboard(opts...)
		mb.SetOriginalRequest(state.OriginalRequest)
		if state.Plan != nil {
			mb.SetPlan(state.Plan)
		}
		for stepID, sr := range state.StepResults {
			mb.SetStepResultRaw(stepID, sr)
		}
		for _, r := range state.Reflections {
			mb.AddReflection(r)
		}
		for stepID, changes := range state.FileChanges {
			mb.SetStepFileChanges(stepID, changes)
		}
		if len(state.Facts) > 0 {
			mb.SetFacts(state.Facts)
		}
		if state.FinalOutput != "" {
			mb.SetFinalResult(state.FinalOutput)
		}

		return &testPersistableBlackboard{
			MapBlackboard: mb,
			taskID:        taskID,
			store:         store,
		}, nil
	}
}
