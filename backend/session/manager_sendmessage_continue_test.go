package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// scriptedLLM returns scripted responses in order, then repeats the last one
// forever. Repeating handles multi-call scenarios (e.g. a checklist-gate nudge
// that re-invokes the executor) without exhausting the script. Every request is
// recorded so tests can assert on the messages that reached the LLM.
type scriptedLLM struct {
	mu       sync.Mutex
	scripted []*llm.ChatResponse
	calls    []llm.ChatRequest
	idx      int
}

func (s *scriptedLLM) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if len(s.scripted) == 0 {
		return nil, errors.New("scriptedLLM: no responses configured")
	}
	if s.idx < len(s.scripted) {
		resp := s.scripted[s.idx]
		s.idx++
		return resp, nil
	}
	// Repeat the last response forever.
	return s.scripted[len(s.scripted)-1], nil
}

func (s *scriptedLLM) Calls() []llm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.ChatRequest, len(s.calls))
	copy(out, s.calls)
	return out
}

// routingJSONResponse builds a router classification response.
func routingJSONResponse(domain string, complexity int) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: `{"domain":"` + domain + `","complexity":` + strconv.Itoa(complexity) + `}`,
		},
		StopReason: "end_turn",
	}
}

// finishResponse builds an executor response that calls the finish tool with the
// given answer (the Conductor extracts answer as the task output).
func finishResponse(answer string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "Task completed",
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "` + answer + `"}`)}},
		},
		StopReason: "tool_use",
	}
}

// capturingFunctionalFactory wraps functionalOrchestratorFactory and stores the
// created *core.Orchestrator in *out so a test can inspect its state (e.g.
// ConversationHistory) after execution.
func capturingFunctionalFactory(caller agent.LLMCaller, out **core.Orchestrator) OrchestratorFactory {
	inner := functionalOrchestratorFactory(caller)
	return func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, tracker *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		orch, err := inner(emitter, logger, workspacePath, bbFactory, dumpWriter, tracker)
		if err == nil && out != nil {
			*out = orch
		}
		return orch, err
	}
}

// routingFunctionalFactory builds a real orchestrator WITH a router (unlike
// functionalOrchestratorFactory), so the normal HandleMessage route → plan →
// execute path can run end-to-end in tests. The same caller backs the router
// and the executor.
func routingFunctionalFactory(caller agent.LLMCaller) OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		rtr := router.New(caller, router.Config{
			SystemPrompt:  prompts.RouterSystem,
			HistoryWindow: 5,
		})
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
			LLM:            caller,
			Router:         rtr,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// lastMessage returns the last message in a request, or the zero value.
func lastMessage(msgs []llm.Message) llm.Message {
	if len(msgs) == 0 {
		return llm.Message{}
	}
	return msgs[len(msgs)-1]
}

// containsSubstr reports whether any message content contains substr.
func containsSubstr(msgs []llm.Message, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}

// TestSendMessage_UnfinishedTask_ContinuesCycle verifies that sending a message
// to a session with an UNFINISHED (interrupted) task continues the ReAct cycle:
// the prior trajectory is seeded into the LLM context and the new message is
// appended as a final user-nudge turn. No routing occurs and no new task is
// created (the task ID is preserved).
func TestSendMessage_UnfinishedTask_ContinuesCycle(t *testing.T) {
	const (
		nudge          = "now implement the second feature"
		finishAnswer   = "resumed-and-finished"
		unfinishedTask = "task-unfinished-1"
	)

	// Two prior ReAct steps persisted as the trajectory.
	trajSteps := []agent.Step{
		{Thought: "step one", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR-1"},
		{Thought: "step two", Action: llm.ToolCall{ID: "pc2", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR-2"},
	}
	trajJSON, _ := json.Marshal(trajSteps)

	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: unfinishedTask, SessionID: "ignored", OriginalRequest: "long running task",
			Status: "in_progress",
		},
		trajectory: trajJSON,
	}

	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		finishResponse(finishAnswer),
	}}
	var orch *core.Orchestrator
	eventChan := make(chan Event, 100)
	mgr := NewManager(capturingFunctionalFactory(caller, &orch), func(e Event) { eventChan <- e }, t.TempDir())
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// The user message must appear in the UI (message_received) regardless of path.
	if err := mgr.SendMessage(context.Background(), info.ID, nudge, nil, "", "", false, ""); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// message_received should fire so the UI shows the user message.
	if _, ok := waitForEvent(eventChan, "message_received", 2*time.Second); !ok {
		t.Fatal("timeout waiting for message_received event")
	}

	complete, ok := waitForEvent(eventChan, "task_complete", 5*time.Second)
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
	if data.Output != finishAnswer {
		t.Errorf("expected output %q, got %q", finishAnswer, data.Output)
	}

	calls := caller.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}

	// The FIRST LLM call is the resumed executor (no router is configured, and
	// Resume never routes). It must contain the seeded trajectory and end with
	// the user nudge.
	execMsgs := calls[0].Messages

	// Trajectory observations must be present (proves the resume path seeded it;
	// a fresh routed task would have none).
	if !containsSubstr(execMsgs, "PRIOR-1") || !containsSubstr(execMsgs, "PRIOR-2") {
		t.Errorf("expected seeded trajectory observations PRIOR-1/PRIOR-2 in executor context, messages=%v", execMsgs)
	}

	// No routing call should have occurred.
	for i, c := range calls {
		if containsSubstr(c.Messages, "Classify this request:") {
			t.Errorf("call %d is a routing call — resume path must NOT route", i)
		}
	}

	// The user nudge must appear as a user-turn AFTER the prior trajectory.
	last := lastMessage(execMsgs)
	if last.Role != "user" || last.Content != nudge {
		t.Errorf("expected last executor message to be the user nudge {user %q}, got {role=%s content=%q}", nudge, last.Role, last.Content)
	}
	// Locate the nudge and the last prior observation; nudge must come after.
	nudgeIdx := -1
	priorIdx := -1
	for i, m := range execMsgs {
		if m.Role == "tool" && strings.Contains(m.Content, "PRIOR-2") {
			priorIdx = i
		}
		if m.Role == "user" && m.Content == nudge {
			nudgeIdx = i
		}
	}
	if nudgeIdx < 0 {
		t.Fatalf("user nudge not found in executor context")
	}
	if priorIdx < 0 || nudgeIdx < priorIdx {
		t.Errorf("expected nudge (idx %d) AFTER prior trajectory observation PRIOR-2 (idx %d)", nudgeIdx, priorIdx)
	}

	// No new task: the task ID must be preserved (the same unfinished task).
	sess, _ := mgr.GetSession(info.ID)
	if sess == nil {
		t.Fatal("session not found after completion")
	}
	sess.mu.Lock()
	gotTaskID := sess.lastCompletedTaskID
	sess.mu.Unlock()
	if gotTaskID != unfinishedTask {
		t.Errorf("expected preserved task ID %q (no new task), got %q", unfinishedTask, gotTaskID)
	}

	// Conversation history must NOT be duplicated: the nudge is a continuation,
	// recorded only in the trajectory, not as a flat conversation-history user
	// turn (recordResumeOutcome appends the assistant side only).
	if orch != nil {
		for i, m := range orch.ConversationHistory() {
			if m.Role == "user" && m.Content == nudge {
				t.Errorf("conversation history must not contain the nudge as a user turn (found at idx %d) — resume records assistant-side only", i)
			}
		}
	}
}

// TestSendMessage_IdleSession_StartsNewTaskWithRouting verifies that sending a
// message to a session with NO unfinished task runs the normal flow as before:
// routing happens and a new task is executed. This is the no-regression guard
// for the resume branch.
func TestSendMessage_IdleSession_StartsNewTaskWithRouting(t *testing.T) {
	const userMsg = "hello world"

	// Task store reports NO unfinished task → resume path is skipped.
	store := &resumeTaskStore{task: nil}

	// Scripted responses: router classification, then a finish.
	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		routingJSONResponse("general", 1),
		finishResponse("done-fresh"),
	}}
	eventChan := make(chan Event, 100)
	mgr := NewManager(routingFunctionalFactory(caller), func(e Event) { eventChan <- e }, t.TempDir())
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := mgr.SendMessage(context.Background(), info.ID, userMsg, nil, "", "", false, ""); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if _, ok := waitForEvent(eventChan, "message_received", 2*time.Second); !ok {
		t.Fatal("timeout waiting for message_received event")
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 5*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}

	calls := caller.Calls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls (router + executor) for a fresh routed task, got %d", len(calls))
	}
	// The first call must be the router — proving routing happened (criterion 3).
	if !containsSubstr(calls[0].Messages, "Classify this request: "+userMsg) {
		t.Errorf("expected first call to be the router classifying the message, got messages=%v", calls[0].Messages)
	}
}

// TestTryContinueInterruptedTask_NoTaskStore_ReturnsFalse verifies the resume
// branch is a no-op when no task store is configured (the common non-persistent
// case): it returns false so the normal HandleMessage flow runs unchanged.
func TestTryContinueInterruptedTask_NoTaskStore_ReturnsFalse(t *testing.T) {
	manager, _, _ := testManager(t)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, _ := manager.GetSession(info.ID)

	// No SetTaskStore → m.taskStore is nil.
	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "anything") {
		t.Error("expected false when no task store is configured")
	}
}

// TestTryContinueInterruptedTask_NoUnfinishedTask_ReturnsFalse verifies the
// resume branch is skipped (and does NOT load the trajectory) when the store
// reports no unfinished task.
func TestTryContinueInterruptedTask_NoUnfinishedTask_ReturnsFalse(t *testing.T) {
	manager, _, _ := testManager(t)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, _ := manager.GetSession(info.ID)

	store := &resumeTaskStore{task: nil} // no unfinished task
	manager.SetTaskStore(store)

	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "anything") {
		t.Error("expected false when there is no unfinished task")
	}
	store.mu.Lock()
	loads := store.loadTrajCalls
	store.mu.Unlock()
	if loads != 0 {
		t.Errorf("expected LoadTrajectory NOT to be called when there is no unfinished task, got %d calls", loads)
	}
}

// TestTryContinueInterruptedTask_NoRouter ensures the helper does not reference
// the orchestrator's router before confirming an unfinished task exists (the
// idle path must work even with a nil-router orchestrator from testManager).
// This is covered by NoUnfinishedTask_ReturnsFalse above; kept as an explicit
// guard that the early return happens before any orchestrator interaction.
func TestTryContinueInterruptedTask_SkipsOrchestratorWhenIdle(t *testing.T) {
	manager, _, _ := testManager(t)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, _ := manager.GetSession(info.ID)
	// testManager builds a nil orchestrator; the helper must return false
	// without dereferencing it when there is no unfinished task.
	manager.SetTaskStore(&resumeTaskStore{task: nil})
	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "msg") {
		t.Error("expected false (idle) and no orchestrator interaction")
	}
}
