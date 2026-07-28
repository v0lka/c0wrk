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
	coretools "github.com/v0lka/c0wrk/core/tools"
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
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// The user message must appear in the UI (message_received) regardless of path.
	if err := mgr.SendMessage(context.Background(), info.ID, nudge, nil, "", "", false, "", false); err != nil {
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
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := mgr.SendMessage(context.Background(), info.ID, userMsg, nil, "", "", false, "", false); err != nil {
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
	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "anything", "", "", nil) {
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

	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "anything", "", "", nil) {
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
	if manager.tryContinueInterruptedTask(context.Background(), info.ID, sess, "msg", "", "", nil) {
		t.Error("expected false (idle) and no orchestrator interaction")
	}
}

// recordingScriptedLLM wraps scriptedLLM and implements SetReasoningEffort so
// the resume path's ApplyRequestOverrides (which propagates reasoning effort
// to the LLM via a type assertion) can be observed end-to-end.
type recordingScriptedLLM struct {
	*scriptedLLM
	mu              sync.Mutex
	reasoningEffort string
}

func (r *recordingScriptedLLM) SetReasoningEffort(effort string) {
	r.mu.Lock()
	r.reasoningEffort = effort
	r.mu.Unlock()
}

func (r *recordingScriptedLLM) ReasoningEffort() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reasoningEffort
}

// attachmentsTrackingResumeStore wraps resumeTaskStore and records
// SaveAttachments calls so a test can assert pending attachments were flushed
// into the restored blackboard (AddAttachment persists the full list via the
// adapter → SaveAttachments).
type attachmentsTrackingResumeStore struct {
	*resumeTaskStore
	mu           sync.Mutex
	saveAttCalls int
	lastAttData  json.RawMessage
}

func (a *attachmentsTrackingResumeStore) SaveAttachments(_ context.Context, _ string, data json.RawMessage) error {
	a.mu.Lock()
	a.saveAttCalls++
	a.lastAttData = data
	a.mu.Unlock()
	return nil
}

// overrideFunctionalFactory builds a real *core.Orchestrator wired with a mock
// LLM, a real ContextWindow, and — critically — a ModelSwitcher (an *llm.Router)
// so the resume path's ApplyRequestOverrides can switch the active model
// end-to-end. The Orchestrator's Router field (the message classifier) is
// intentionally left nil: the resume path never routes, so the classifier is
// not needed (and a nil Router is safe because routeOrContinue/routeAndActivate
// Skills are never reached on this path).
func overrideFunctionalFactory(caller agent.LLMCaller, switcher *llm.Router) OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		return core.NewOrchestrator(core.OrchestratorConfig{Model: "resume-default"}, core.OrchestratorDeps{
			LLM:            caller,
			ModelSwitcher:  switcher,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// TestSendMessage_ResumePath_AppliesOverridesAndFlushesAttachments verifies the
// session manager's tryContinueInterruptedTask resume path applies the
// per-request model + reasoning overrides and flushes pending attachments into
// the restored blackboard. Because this path bypasses HandleMessage (whose step
// 0 would otherwise apply the overrides and whose setupBlackboard would flush
// attachments), both must be wired explicitly in tryContinueInterruptedTask.
//
// Setup: an interrupted task with a persisted trajectory, a pending attachment
// staged on the session, and a SendMessage that BOTH continues the task AND
// requests a model + reasoning-effort override.
//
// Asserts:
//  1. Model override applied — the model switcher's ActiveModel() flips from
//     the default to the override.
//  2. Reasoning-effort override applied — the LLM's SetReasoningEffort was
//     called with the override value.
//  3. Pending attachments flushed into the restored blackboard — SaveAttachments
//     was called with the staged attachment's marker content.
//  4. Pending attachments cleared on the session — attachments_changed event
//     fired and session.pendingAttachments is empty after the send.
//
// On the OLD code (overrides/attachments applied only inside HandleMessage,
// which the resume path bypasses), assertions 1, 2, and 3 fail.
func TestSendMessage_ResumePath_AppliesOverridesAndFlushesAttachments(t *testing.T) {
	const (
		nudge          = "resume with the new settings"
		finishAnswer   = "resumed-with-overrides"
		unfinishedTask = "task-resume-overrides-1"
		attMarker      = "RESUME-ATTACHMENT-MARKER-5e8c"
		attName        = "resumed-spec.md"
		// ProviderEntry.Models lists BARE model names; ActiveModel() reports the
		// composite "provider/bare-model" ID, so override assertions compare
		// against the composite IDs derived below.
		bareDefault  = "resume-default"
		bareOverride = "resume-override"
		providerName = "test"
		reasoning    = "high"
	)

	// A single prior ReAct step persisted as the trajectory.
	trajSteps := []agent.Step{
		{Thought: "step one", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR-1"},
	}
	trajJSON, _ := json.Marshal(trajSteps)

	store := &attachmentsTrackingResumeStore{
		resumeTaskStore: &resumeTaskStore{
			task: &TaskRecord{
				ID: unfinishedTask, SessionID: "ignored", OriginalRequest: "long running task",
				Status: "in_progress",
			},
			trajectory: trajJSON,
		},
	}

	// Model switcher with two models so SetModel has a target.
	switcher, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: providerName, ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{bareDefault, bareOverride}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("build llm router: %v", err)
	}
	// NewRouter selects the first provider's first model as the active model by
	// default, so ActiveModel() starts at CompositeModelID(provider, bareDefault).
	// SendMessage's resume path overrides it to bareOverride via SetModel.
	defaultModel := llm.CompositeModelID(providerName, bareDefault)
	overrideModel := llm.CompositeModelID(providerName, bareOverride)

	caller := &recordingScriptedLLM{scriptedLLM: &scriptedLLM{scripted: []*llm.ChatResponse{
		finishResponse(finishAnswer),
	}}}
	// A recorder goroutine buffers ALL events into a slice (sync.Mutex-guarded)
	// so assertions can scan the full sequence without being consumed by
	// waitForEvent (which drains events up to its target).
	var (
		allEventsMu sync.Mutex
		allEvents   []Event
	)
	eventChan := make(chan Event, 100)
	go func() {
		for e := range eventChan {
			allEventsMu.Lock()
			allEvents = append(allEvents, e)
			allEventsMu.Unlock()
		}
	}()
	mgr := NewManager(overrideFunctionalFactory(caller, switcher), func(e Event) { eventChan <- e }, t.TempDir())
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// Stage a pending attachment directly on the session (mirrors what
	// AttachFiles leaves behind before the next SendMessage consumes it).
	attachment := orchestration.Attachment{
		ID:              "att-resume-1",
		OriginalName:    attName,
		Format:          "md",
		MarkdownContent: "# Spec\n" + attMarker,
		AttachedAt:      time.Now(),
	}
	sess, _ := mgr.GetSession(info.ID)
	if sess == nil {
		t.Fatal("session not found after creation")
	}
	sess.mu.Lock()
	sess.pendingAttachments = append(sess.pendingAttachments, attachment)
	sess.mu.Unlock()

	// Sanity: the switcher starts on the default model.
	if got := switcher.ActiveModel(); got != defaultModel {
		t.Fatalf("precondition: ActiveModel = %q, want %q", got, defaultModel)
	}

	// Send a message that BOTH continues the interrupted task AND overrides the
	// model + reasoning effort.
	if err := mgr.SendMessage(context.Background(), info.ID, nudge, nil, overrideModel, reasoning, false, "", false); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Wait for the task_complete event to appear in the buffered sequence
	// (scans the recorder slice rather than draining the channel, so the
	// attachments_changed event emitted earlier in the send is preserved).
	if !waitForBufferedEvent(&allEventsMu, &allEvents, "message_received", 2*time.Second) {
		t.Fatal("timeout waiting for message_received event")
	}
	if !waitForBufferedEvent(&allEventsMu, &allEvents, "task_complete", 5*time.Second) {
		t.Fatal("timeout waiting for task_complete event")
	}

	t.Run("model override applied to the resumed orchestrator", func(t *testing.T) {
		if got := switcher.ActiveModel(); got != overrideModel {
			t.Errorf("ActiveModel after resume = %q, want %q (ApplyRequestOverrides must call SetModel on the resume path)", got, overrideModel)
		}
	})

	t.Run("reasoning-effort override applied to the resumed LLM", func(t *testing.T) {
		if got := caller.ReasoningEffort(); got != reasoning {
			t.Errorf("LLM reasoning effort after resume = %q, want %q (ApplyRequestOverrides must propagate reasoning effort on the resume path)", got, reasoning)
		}
	})

	t.Run("pending attachment flushed into the restored blackboard", func(t *testing.T) {
		store.mu.Lock()
		calls := store.saveAttCalls
		data := store.lastAttData
		store.mu.Unlock()
		if calls == 0 {
			t.Fatal("SaveAttachments was never called — the pending attachment was not flushed into the restored blackboard on the resume path")
		}
		if !strings.Contains(string(data), attMarker) {
			t.Errorf("flushed attachment data does not contain the marker %q; got %s", attMarker, string(data))
		}
	})

	t.Run("pending attachments cleared on the session (attachments_changed fired)", func(t *testing.T) {
		// Scan the buffered event sequence for the attachments_changed emission
		// (fired by the snapshot/clear step in SendMessage).
		var sawAttachmentsChanged bool
		allEventsMu.Lock()
		for _, e := range allEvents {
			if e.Type == "attachments:changed" {
				sawAttachmentsChanged = true
				break
			}
		}
		allEventsMu.Unlock()
		if !sawAttachmentsChanged {
			t.Error("expected an attachments_changed event — the snapshot/clear fires it when there were pending attachments")
		}
		sess2, _ := mgr.GetSession(info.ID)
		if sess2 == nil {
			t.Fatal("session not found after send")
		}
		sess2.mu.Lock()
		pending := sess2.pendingAttachments
		sess2.mu.Unlock()
		if len(pending) != 0 {
			t.Errorf("expected session.pendingAttachments cleared after the send, got %d", len(pending))
		}
	})

	// NOTE: the recorder goroutine is intentionally left running (the channel is
	// buffered and the emitter may fire events during t.Cleanup's
	// mgr.Shutdown); closing it would risk a send-on-closed-channel panic.
}

// waitForBufferedEvent polls a mutex-guarded event slice until an event of the
// given type appears or the timeout elapses. Unlike waitForEvent (which drains
// a channel), this preserves all events in the buffer for later scanning.
func waitForBufferedEvent(mu *sync.Mutex, events *[]Event, eventType string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, e := range *events {
			if e.Type == eventType {
				mu.Unlock()
				return true
			}
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// cancelTrackingResumeStore wraps resumeTaskStore and records CancelTask +
// LoadTrajectory calls so a test can assert that a goal request abandoned the
// interrupted task (CancelTask invoked) WITHOUT taking the resume path
// (LoadTrajectory NOT invoked).
type cancelTrackingResumeStore struct {
	*resumeTaskStore
	mu              sync.Mutex
	cancelCalls     int
	cancelledTaskID string
	loadTrajCalls   int
}

func (c *cancelTrackingResumeStore) CancelTask(_ context.Context, taskID string) error {
	c.mu.Lock()
	c.cancelCalls++
	c.cancelledTaskID = taskID
	c.mu.Unlock()
	return nil
}

func (c *cancelTrackingResumeStore) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	c.mu.Lock()
	c.loadTrajCalls++
	c.mu.Unlock()
	return c.trajectory, nil
}

// autoApproveProposer is a GoalProposer that auto-approves every goal proposal
// so derivation completes without blocking on a real user-confirmation flow.
type autoApproveProposer struct{}

func (autoApproveProposer) Propose(_ context.Context, p coretools.GoalProposal) (coretools.GoalProposalResponse, error) {
	return coretools.GoalProposalResponse{
		Decision:  "approve",
		Condition: p.Condition,
		Verify:    p.Verify,
	}, nil
}

// proposeGoalResponse builds an LLM response that calls propose_goal with a
// minimal {condition, verify} pair (derivation turn 1).
func proposeGoalResponse(condition, verify string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:    "pg1",
				Name:  "propose_goal",
				Input: json.RawMessage(`{"condition":"` + condition + `","verify":"` + verify + `","needs_clarification":false}`),
			}},
		},
		StopReason: "tool_use",
	}
}

// TestSendMessage_GoalOnResume_AbandonsInterruptedTaskAndRunsGoal verifies that
// a goal request on a session with an UNFINISHED task does NOT take the resume
// path. Instead, the interrupted task is cancelled (abandonUnfinishedTaskForGoal)
// and the goal loop runs fresh. On the OLD code, goal was silently ignored on
// the resume path and the WIP was resumed as a normal task.
//
// Setup: an interrupted task (in_progress) + a SendMessage with goal=true.
// LLM: router classification → propose_goal → finish.
// Asserts:
//  1. CancelTask was called on the interrupted task (abandoned).
//  2. LoadTrajectory was NOT called (resume path skipped).
//  3. The goal loop ran — propose_goal was invoked (derivation ran).
//  4. A "service" event about the abandoned task was emitted.
func TestSendMessage_GoalOnResume_AbandonsInterruptedTaskAndRunsGoal(t *testing.T) {
	const (
		goalCondition     = "review all code"
		goalVerify        = "no issues found"
		finishAnswer      = "goal completed"
		interruptedTaskID = "task-interrupted-goal-1"
	)

	trajSteps := []agent.Step{
		{Thought: "prior step", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	}
	trajJSON, _ := json.Marshal(trajSteps)

	store := &cancelTrackingResumeStore{
		resumeTaskStore: &resumeTaskStore{
			task: &TaskRecord{
				ID: interruptedTaskID, SessionID: "ignored", OriginalRequest: "long running task",
				Status: "in_progress",
			},
			trajectory: trajJSON,
		},
	}

	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		routingJSONResponse("general", 1),              // call 1: router classification
		proposeGoalResponse(goalCondition, goalVerify), // call 2: derivation → propose_goal
		finishResponse(finishAnswer),                   // call 3+: goal turn → finish (declares met)
		finishResponse(finishAnswer),                   // extra safety: repeat
	}}

	var (
		allEventsMu sync.Mutex
		allEvents   []Event
	)
	eventChan := make(chan Event, 100)
	go func() {
		for e := range eventChan {
			allEventsMu.Lock()
			allEvents = append(allEvents, e)
			allEventsMu.Unlock()
		}
	}()

	mgr := NewManager(routingFunctionalFactory(caller), func(e Event) { eventChan <- e }, t.TempDir())
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// Inject an auto-approving goal proposer (the session manager's factory does
	// not wire one; in production the Application layer does via SetGoalProposer).
	sess, _ := mgr.GetSession(info.ID)
	if sess == nil {
		t.Fatal("session not found after creation")
	}
	sess.orchestrator.SetGoalProposer(autoApproveProposer{})

	// Send a goal request that ALSO has an unfinished task to resume.
	if err := mgr.SendMessage(context.Background(), info.ID, goalCondition, nil, "", "", true, "", false); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if !waitForBufferedEvent(&allEventsMu, &allEvents, "task_complete", 5*time.Second) {
		t.Fatal("timeout waiting for task_complete event")
	}

	t.Run("interrupted task was cancelled (abandoned for goal)", func(t *testing.T) {
		store.mu.Lock()
		cancels := store.cancelCalls
		cancelled := store.cancelledTaskID
		store.mu.Unlock()
		if cancels == 0 {
			t.Error("expected CancelTask to be called on the interrupted task (abandonUnfinishedTaskForGoal), got 0 calls")
		}
		if cancelled != interruptedTaskID {
			t.Errorf("cancelled task = %q, want %q", cancelled, interruptedTaskID)
		}
	})

	t.Run("resume path was skipped (LoadTrajectory not called)", func(t *testing.T) {
		store.mu.Lock()
		loads := store.loadTrajCalls
		store.mu.Unlock()
		if loads != 0 {
			t.Errorf("expected LoadTrajectory NOT called (goal supersedes resume), got %d calls", loads)
		}
	})

	t.Run("goal loop ran (propose_goal invoked)", func(t *testing.T) {
		// The goal loop's derivation calls propose_goal, which our auto-approving
		// proposer accepts. The router call (call 1) and propose_goal call (call 2)
		// both hit the LLM; if the goal loop did NOT run, only the router call
		// would fire (or none, if the resume path took over).
		calls := caller.Calls()
		if len(calls) < 2 {
			t.Errorf("expected at least 2 LLM calls (router + propose_goal), got %d — goal loop may not have run", len(calls))
		}
	})

	t.Run("service event about abandoned task was emitted", func(t *testing.T) {
		allEventsMu.Lock()
		defer allEventsMu.Unlock()
		for _, e := range allEvents {
			if e.Type != "service" {
				continue
			}
			if content, ok := e.Data.(map[string]any)["content"].(string); ok && strings.Contains(content, "abandoned") {
				return
			}
		}
		t.Error("expected a 'service' event mentioning the abandoned task, not found")
	})

	// NOTE: the recorder goroutine is intentionally left running (the channel is
	// buffered and the emitter may fire events during t.Cleanup's Shutdown).
}
