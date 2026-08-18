package session

import (
	"context"
	"encoding/json"
	"errors"
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
	mu             sync.Mutex
	task           *TaskRecord
	trajectory     json.RawMessage
	loadTrajCalls  int
	completedCalls int
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
func (s *resumeTaskStore) PauseTask(_ context.Context, _ string) error  { return nil }

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
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
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
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
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
	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
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
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
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
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event (empty trajectory fallback)")
	}
}

// TestResumeTask_AppliesModelOverride verifies that a model/reasoning override
// passed to ResumeTask (the Resume button path, which bypasses SendMessage) is
// honored: the resumed orchestrator's active model switches to the override.
// Without ApplyRequestOverrides on the ResumeTask path, the resumed task would
// silently inherit the interrupted task's model.
func TestResumeTask_AppliesModelOverride(t *testing.T) {
	const (
		bareDefault  = "resume-btn-default"
		bareOverride = "resume-btn-override"
		providerName = "test"
		reasoning    = "high"
		finishAnswer = "resumed-btn-done"
	)

	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-override", SessionID: "ignored", OriginalRequest: "interrupted task",
			Status: "in_progress",
		},
		trajectory: trajJSON,
	}

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
	defaultModel := llm.CompositeModelID(providerName, bareDefault)
	overrideModel := llm.CompositeModelID(providerName, bareOverride)

	caller := &recordingScriptedLLM{scriptedLLM: &scriptedLLM{scripted: []*llm.ChatResponse{
		finishResponse(finishAnswer),
	}}}
	eventChan := make(chan Event, 100)
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

	// Sanity: switcher starts on the default model.
	if got := switcher.ActiveModel(); got != defaultModel {
		t.Fatalf("precondition: ActiveModel = %q, want %q", got, defaultModel)
	}

	// Resume with a model + reasoning override (mirrors the Resume button
	// passing the user's current model/reasoning selection).
	if err := mgr.ResumeTask(context.Background(), info.ID, overrideModel, reasoning, ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}

	if got := switcher.ActiveModel(); got != overrideModel {
		t.Errorf("ActiveModel after ResumeTask = %q, want %q (ResumeTask must apply the model override via ApplyRequestOverrides)", got, overrideModel)
	}
	if got := caller.ReasoningEffort(); got != reasoning {
		t.Errorf("LLM reasoning effort after ResumeTask = %q, want %q", got, reasoning)
	}
}

// TestResumeTask_ModelSwitchRebasesContextWindow verifies that switching the
// model while a task is paused and resuming it rebases the CONTEXT WINDOW to
// the newly-selected model: both the display basis (initial context_fill
// MaxTokens) and the compaction basis (the ModelMetadata the ContextFactory
// receives) must reflect the new model's window, not the previous model's.
// The existing override test only asserts the model NAME; the window was
// previously untested (test factories hardcoded 128000).
func TestResumeTask_ModelSwitchRebasesContextWindow(t *testing.T) {
	const (
		bigModel   = "resume-win-big"
		smallModel = "resume-win-small"
		provider   = "test"
	)
	const (
		bigWindow   = 200_000
		smallWindow = 32_768
	)

	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-window", SessionID: "ignored", OriginalRequest: "interrupted task",
			Status: "in_progress",
		},
		trajectory: trajJSON,
	}

	switcher, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: provider, ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{bigModel, smallModel}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("build llm router: %v", err)
	}

	// Registry knows both models with DIFFERENT context windows.
	modelReg := llm.NewModelRegistry(map[string]llm.ModelMetadata{
		bigModel:   {ContextWindow: bigWindow, OutputLimit: 8192, TokenizerType: "approximate"},
		smallModel: {ContextWindow: smallWindow, OutputLimit: 4096, TokenizerType: "approximate"},
	})

	// Capturing context factory records the ModelMetadata window each run
	// receives (the compaction basis) while building a real ContextWindow.
	var factoryMu sync.Mutex
	var factoryWindows []int
	cf := func(systemPrompt string, meta llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
		factoryMu.Lock()
		factoryWindows = append(factoryWindows, meta.ContextWindow)
		factoryMu.Unlock()
		cw := memory.NewContextWindow(memory.ContextWindowConfig{
			SystemPrompt: systemPrompt,
			ModelMeta:    meta,
		})
		return core.NewCoreContextManager(cw)
	}

	factory := func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		return core.NewOrchestrator(core.OrchestratorConfig{Model: bigModel}, core.OrchestratorDeps{
			LLM:            &finishLLM{answer: "resumed-window-done"},
			ModelSwitcher:  switcher,
			ModelRegistry:  modelReg,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}

	var eventsMu sync.Mutex
	var allEvents []Event
	eventChan := make(chan Event, 100)
	mgr := NewManager(factory, func(e Event) {
		eventsMu.Lock()
		allEvents = append(allEvents, e)
		eventsMu.Unlock()
		eventChan <- e
	}, t.TempDir())
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// Sanity: starts on the big model.
	if got := switcher.ActiveModel(); got != llm.CompositeModelID(provider, bigModel) {
		t.Fatalf("precondition: ActiveModel = %q, want %q", got, llm.CompositeModelID(provider, bigModel))
	}

	// Resume with the SMALL-window model override (user switched while paused).
	if err := mgr.ResumeTask(context.Background(), info.ID, llm.CompositeModelID(provider, smallModel), "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}

	// 1. Display basis: the initial context_fill must carry the new window.
	var firstFill *ContextFillEventData
	eventsMu.Lock()
	for i := range allEvents {
		if allEvents[i].Type == "context_fill" {
			if data, ok := allEvents[i].Data.(ContextFillEventData); ok {
				d := data
				firstFill = &d
				break
			}
		}
	}
	eventsMu.Unlock()
	if firstFill == nil {
		t.Fatal("expected at least one context_fill event")
	}
	if firstFill.MaxTokens != smallWindow {
		t.Errorf("initial context_fill MaxTokens = %d, want %d (display window must rebase to the resumed model)", firstFill.MaxTokens, smallWindow)
	}

	// 2. Compaction basis: the ContextFactory must receive the new window.
	factoryMu.Lock()
	windows := append([]int(nil), factoryWindows...)
	factoryMu.Unlock()
	if len(windows) == 0 {
		t.Fatal("context factory was never invoked")
	}
	for _, w := range windows {
		if w != smallWindow {
			t.Errorf("ContextFactory received ContextWindow = %d, want %d on every resumed run (compaction basis must rebase to the resumed model)", w, smallWindow)
			break
		}
	}
}

// TestResumeTask_ModelSwitchToCatalogModel_WindowFollowsCatalog reproduces the
// reported bug scenario EXACTLY: a session started on a model UNKNOWN to the
// registry (fallback 128000 window) is paused, the user switches to a
// catalog-known model (glm-5.2, 1M window in the built-in sp4rk catalog), and
// resumes. The display basis (initial context_fill MaxTokens) and the
// compaction basis (ModelMetadata handed to the ContextFactory) must follow
// the catalog — 1000000, not the previous task's 128000 fallback.
//
// Uses a pure catalog registry (NewModelRegistry(nil)) like the real app, so
// glm-5.2 resolves through the built-in tier rather than an override.
func TestResumeTask_ModelSwitchToCatalogModel_WindowFollowsCatalog(t *testing.T) {
	const (
		provider     = "Z-ai"
		unknownModel = "some-unknown-model"
		knownModel   = "glm-5.2"
		wantWindow   = 1000000
	)

	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-catalog", SessionID: "ignored", OriginalRequest: "interrupted task",
			Status: "in_progress",
		},
		trajectory: trajJSON,
	}

	switcher, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: provider, ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{unknownModel, knownModel}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("build llm router: %v", err)
	}

	// Pure catalog registry — exactly what the real app builds for a session
	// without llm.models overrides. glm-5.2 must resolve via the built-in tier.
	modelReg := llm.NewModelRegistry(nil)
	if meta, _ := modelReg.ResolveLocal(knownModel); meta.ContextWindow != wantWindow {
		t.Fatalf("precondition: catalog must know %s with %d window, got %d", knownModel, wantWindow, meta.ContextWindow)
	}

	var factoryMu sync.Mutex
	var factoryWindows []int
	cf := func(systemPrompt string, meta llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
		factoryMu.Lock()
		factoryWindows = append(factoryWindows, meta.ContextWindow)
		factoryMu.Unlock()
		cw := memory.NewContextWindow(memory.ContextWindowConfig{
			SystemPrompt: systemPrompt,
			ModelMeta:    meta,
		})
		return core.NewCoreContextManager(cw)
	}

	factory := func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		return core.NewOrchestrator(core.OrchestratorConfig{Model: unknownModel}, core.OrchestratorDeps{
			LLM:            &finishLLM{answer: "resumed-catalog-done"},
			ModelSwitcher:  switcher,
			ModelRegistry:  modelReg,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}

	var eventsMu sync.Mutex
	var allEvents []Event
	eventChan := make(chan Event, 100)
	mgr := NewManager(factory, func(e Event) {
		eventsMu.Lock()
		allEvents = append(allEvents, e)
		eventsMu.Unlock()
		eventChan <- e
	}, t.TempDir())
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	// First task on the UNKNOWN model: initial fill must carry the 128000
	// fallback — the state the status bar was left in when the task paused.
	if err := mgr.ResumeTask(context.Background(), info.ID, llm.CompositeModelID(provider, unknownModel), "", ""); err != nil {
		t.Fatalf("ResumeTask (unknown model) failed: %v", err)
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event (unknown model run)")
	}

	// Switch during pause to the catalog-known model and resume.
	if err := mgr.ResumeTask(context.Background(), info.ID, llm.CompositeModelID(provider, knownModel), "", ""); err != nil {
		t.Fatalf("ResumeTask (known model) failed: %v", err)
	}
	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event (known model run)")
	}

	// Walk every context_fill in order; all fills AFTER the model switch must
	// carry the catalog window (1M), never the stale 128000 fallback.
	eventsMu.Lock()
	fills := make([]ContextFillEventData, 0, len(allEvents))
	for i := range allEvents {
		if allEvents[i].Type == "context_fill" {
			if data, ok := allEvents[i].Data.(ContextFillEventData); ok {
				fills = append(fills, data)
			}
		}
	}
	eventsMu.Unlock()
	if len(fills) == 0 {
		t.Fatal("expected context_fill events")
	}

	// The last fill before the switch belongs to the unknown-model task and
	// legitimately reports 128000. Find the first fill of the second task:
	// it must be 1M.
	sawKnown := false
	for _, f := range fills {
		if f.Model == knownModel || f.Model == llm.CompositeModelID(provider, knownModel) {
			sawKnown = true
			if f.MaxTokens != wantWindow {
				t.Errorf("context_fill for %s: MaxTokens = %d, want %d (catalog window must replace the fallback after the switch)", knownModel, f.MaxTokens, wantWindow)
			}
		}
	}
	if !sawKnown {
		t.Fatal("no context_fill carried the switched-to model")
	}

	// Compaction basis: every ContextFactory invocation of the second task
	// must use the catalog window.
	factoryMu.Lock()
	windows := append([]int(nil), factoryWindows...)
	factoryMu.Unlock()
	knownFactoryWindows := 0
	for _, w := range windows {
		if w == wantWindow {
			knownFactoryWindows++
		}
	}
	if knownFactoryWindows == 0 {
		t.Errorf("ContextFactory never received the %d catalog window (got %v) — compaction basis stayed on the old model's window", wantWindow, windows)
	}
}

// TestManager_ResumeTask_ArchivedRejected verifies that an archived session
// cannot resume an interrupted task. The guard must fire before the task store
// is consulted, so no trajectory/blackboard work occurs.
func TestManager_ResumeTask_ArchivedRejected(t *testing.T) {
	store := &resumeTaskStore{
		task: &TaskRecord{ID: "task-archived", SessionID: "ignored", Status: "in_progress"},
	}
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "should-not-run"}), func(e Event) { eventChan <- e }, t.TempDir())
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Archive the session (toggles Archived=true in-memory).
	if err := mgr.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}
	drainEvents(eventChan) // session_created + session_archived

	// ResumeTask must be rejected with the sentinel error.
	err = mgr.ResumeTask(context.Background(), info.ID, "", "", "")
	if !errors.Is(err, ErrSessionArchived) {
		t.Errorf("ResumeTask on archived session should return ErrSessionArchived, got %v", err)
	}

	// The guard fires before the task store is touched, so no trajectory load
	// should have occurred.
	store.mu.Lock()
	loads := store.loadTrajCalls
	store.mu.Unlock()
	if loads != 0 {
		t.Errorf("ResumeTask should not consult the task store for an archived session, loadTrajCalls=%d", loads)
	}
}
