package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// finishLLM is a minimal agent.LLMCaller that always returns a finish tool
// call. It is the stand-in for the Conductor's executor LLM in resume tests.
type finishLLM struct {
	answer string
}

func (f *finishLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "Resuming from checkpoint",
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer":"` + f.answer + `"}`)}},
		},
		StopReason: "tool_use",
	}, nil
}

// resumeTaskStore is a configurable TaskStore for ResumeTask tests. It returns a
// canned TaskRecord (for GetUnfinishedTask + LoadTask) and a canned trajectory,
// and records whether CompleteTask / LoadTrajectory were called.
type resumeTaskStore struct {
	mockTaskStoreForResumable
	mu               sync.Mutex
	task             *TaskRecord
	trajectory       json.RawMessage
	loadTrajCalls    int
	completedCalls   int
}

func (s *resumeTaskStore) GetUnfinishedTask(_ context.Context, _ string) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil {
		return nil, nil
	}
	// Return a copy so the caller can't mutate the canned record.
	cp := *s.task
	return &cp, nil
}

func (s *resumeTaskStore) LoadTask(_ context.Context, _ string) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil {
		return nil, nil
	}
	cp := *s.task
	return &cp, nil
}

func (s *resumeTaskStore) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadTrajCalls++
	return s.trajectory, nil
}

func (s *resumeTaskStore) CompleteTask(_ context.Context, _, _ string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedCalls++
	return nil
}

func (s *resumeTaskStore) CancelTask(_ context.Context, _ string) error { return nil }

// functionalOrchestratorFactory builds a real *core.Orchestrator wired with a
// mock LLM and a real (sp4rk) ContextWindow so the full Resume → Conductor path
// executes end-to-end. The factory closes over the LLM so each session gets a
// fresh orchestrator backed by the same mock.
func functionalOrchestratorFactory(llmCaller agent.LLMCaller) OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		return core.NewOrchestrator(core.OrchestratorConfig{MaxSteps: 10}, core.OrchestratorDeps{
			LLM:            llmCaller,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// waitForEvent blocks until an event of the given type arrives or the timeout
// elapses. Returns the event and true on success.
func waitForEvent(ch chan Event, eventType string, timeout time.Duration) (Event, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case e := <-ch:
			if e.Type == eventType {
				return e, true
			}
		case <-deadline:
			return Event{}, false
		}
	}
}

// TestResumeTask_LoadsTrajectoryAndResumesWithoutPlan verifies the central
// manager-level acceptance criterion: ResumeTask loads the persisted trajectory,
// passes it to the orchestrator, and resumes successfully WITHOUT a plan or
// routing decision. Previously this returned an error; now the Conductor runs
// the plan-less task.
func TestResumeTask_LoadsTrajectoryAndResumesWithoutPlan(t *testing.T) {
	// Canned trajectory: two prior ReAct steps.
	trajSteps := []agent.Step{
		{Thought: "step one", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR-1"},
		{Thought: "step two", Action: llm.ToolCall{ID: "pc2", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR-2"},
	}
	trajJSON, _ := json.Marshal(trajSteps)

	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-1", SessionID: "ignored", OriginalRequest: "long running task",
			Status: "in_progress",
			// RoutingDecision and Plan intentionally empty (nil) — no routing, no plan.
		},
		trajectory: trajJSON,
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "resumed-done"}), func(e Event) { eventChan <- e }, t.TempDir())
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	// Point the canned task at the real session ID.
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// ResumeTask must NOT error without routing/plan.
	if err := mgr.ResumeTask(context.Background(), info.ID); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	// The trajectory must have been loaded.
	store.mu.Lock()
	loads := store.loadTrajCalls
	store.mu.Unlock()
	if loads != 1 {
		t.Fatalf("expected LoadTrajectory called once, got %d", loads)
	}

	// Wait for task completion.
	complete, ok := waitForEvent(eventChan, "task_complete", 3*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete event")
	}
	data, ok := complete.Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", complete.Data)
	}
	if !data.Success {
		t.Errorf("expected successful completion, got completion=%q output=%q", data.Completion, data.Output)
	}
	if data.Output != "resumed-done" {
		t.Errorf("expected output %q, got %q", "resumed-done", data.Output)
	}
}

// TestResumeTask_ReusesRoutingDecision verifies that a persisted routing
// decision flows through to the resumed execution (it is reused, not
// re-routed) and does not block resume.
func TestResumeTask_ReusesRoutingDecision(t *testing.T) {
	routing := router.RoutingDecision{Domain: "research", Complexity: 5}
	routingJSON, _ := json.Marshal(routing)

	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-2", SessionID: "ignored", OriginalRequest: "research task",
			Status:          "in_progress",
			RoutingDecision: routingJSON,
			// Plan intentionally empty.
		},
		trajectory: nil, // no trajectory — fallback to fresh start
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "reused-routing"}), func(e Event) { eventChan <- e }, t.TempDir())
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	complete, ok := waitForEvent(eventChan, "task_complete", 3*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete event")
	}
	data, _ := complete.Data.(TaskCompleteData)
	if !data.Success {
		t.Errorf("expected successful completion with reused routing, got %q", data.Completion)
	}
	// The RoutingDecision round-tripped through the store should appear on the
	// result. With a persisted routing, Resume reuses it for the result.
	if data.RoutingDecision == nil || data.RoutingDecision.Domain != "research" {
		t.Errorf("expected reused routing domain research, got %+v", data.RoutingDecision)
	}
}

// TestResumeTask_EmptyTrajectoryFallback verifies that a resume with no
// persisted trajectory degrades to a fresh-start executor (no error).
func TestResumeTask_EmptyTrajectoryFallback(t *testing.T) {
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-3", SessionID: "ignored", OriginalRequest: "no trajectory task",
			Status: "in_progress",
		},
		trajectory: nil,
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "fresh-start"}), func(e Event) { eventChan <- e }, t.TempDir())
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event (empty trajectory fallback)")
	}
}
