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
	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/prompts"
	coretools "github.com/v0lka/c0wrk/core/tools"
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

// gatingLLM is an agent.LLMCaller whose FIRST Call blocks until the test
// releases it, so a test can observe and steer state while the resumed
// executor is mid-flight. The first response is a tool call to firstToolCall
// (a non-terminal step, so the executor loop reaches the next step boundary —
// where a pending pause signal trips; the tool itself need not exist: an
// unknown tool yields an error observation and the loop continues). An empty
// firstToolCall makes the first response a finish call. Subsequent calls
// always finish.
type gatingLLM struct {
	firstToolCall string
	started       chan struct{}
	release       chan struct{}
	once          sync.Once
}

func (g *gatingLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	first := false
	g.once.Do(func() {
		first = true
		close(g.started)
		<-g.release
	})
	msg := llm.Message{Role: "assistant", Content: "Resuming from checkpoint"}
	if first && g.firstToolCall != "" {
		msg.ToolCalls = []llm.ToolCall{{ID: "g1", Name: g.firstToolCall, Input: json.RawMessage(`{}`)}}
	} else {
		msg.ToolCalls = []llm.ToolCall{{ID: "g2", Name: "finish", Input: json.RawMessage(`{"answer":"gated-done"}`)}}
	}
	return &llm.ChatResponse{Message: msg, StopReason: "tool_use"}, nil
}

// resumeTaskStore is a configurable TaskStore for ResumeTask tests. It returns a
// canned TaskRecord (for GetUnfinishedTask + LoadTask) and a canned trajectory,
// and records whether CompleteTask / LoadTrajectory were called. ReactivateTask
// and PauseTask mirror SQLiteSessionStore semantics: they rewrite the canned
// record's status (in_progress / paused) and record the call, so tests can
// observe the task-row lifecycle across a resume.
type resumeTaskStore struct {
	mockTaskStoreForResumable
	mu              sync.Mutex
	task            *TaskRecord
	trajectory      json.RawMessage
	goalState       json.RawMessage
	loadTrajCalls   int
	completedCalls  int
	reactivateCalls int
	reactivateErr   error
	pauseCalls      int
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

// LoadTrajectory returns the canned trajectory, or (nil, nil) when unset.
func (s *resumeTaskStore) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadTrajCalls++
	return s.trajectory, nil
}

// LoadGoalState returns the canned goal-loop state, or (nil, nil) when unset —
// the adapter treats that as "no goal" (a plain resume).
func (s *resumeTaskStore) LoadGoalState(_ context.Context, _ string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalState, nil
}

func (s *resumeTaskStore) CompleteTask(_ context.Context, _, _ string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completedCalls++
	return nil
}

func (s *resumeTaskStore) CancelTask(_ context.Context, _ string) error { return nil }

// PauseTask mirrors SQLiteSessionStore.PauseTask: rewrites the row's status to
// 'paused' and records the call.
func (s *resumeTaskStore) PauseTask(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauseCalls++
	if s.task != nil {
		s.task.Status = "paused"
	}
	return nil
}

// ReactivateTask mirrors SQLiteSessionStore.ReactivateTask: rewrites the row's
// status to 'in_progress' and records the call. reactivateErr, when set, is
// returned instead WITHOUT touching the status (error-injection for the
// warn-not-fatal test).
func (s *resumeTaskStore) ReactivateTask(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactivateCalls++
	if s.reactivateErr != nil {
		return s.reactivateErr
	}
	if s.task != nil {
		s.task.Status = "in_progress"
	}
	return nil
}

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
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "resumed-done"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "reused-routing"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
//
// The task carries a persisted routing decision (a task that was routed but
// produced no trajectory — e.g. it failed before the first step landed): a
// task with neither routing nor trajectory would instead be treated as
// never-started and re-routed (see TestResumeTask_ReroutesNeverStartedTask),
// which requires a routing-capable call, not the finish-only mock wired here.
func TestResumeTask_EmptyTrajectoryFallback(t *testing.T) {
	routing := router.RoutingDecision{Domain: "general", Complexity: 2}
	routingJSON, _ := json.Marshal(routing)
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-3", SessionID: "ignored", OriginalRequest: "no trajectory task",
			Status:          "in_progress",
			RoutingDecision: routingJSON,
		},
		trajectory: nil,
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "fresh-start"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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

// TestResumeTask_ReroutesNeverStartedTask is the manager-level acceptance test
// for the pre-routing resume fix: a task that failed BEFORE its original run
// reached routing — no persisted routing decision AND no execution state, the
// shape a fresh send leaves when the router's LLM call fails (e.g. a network
// error) — must be re-classified on resume instead of running the Conductor
// under the default "general" domain. The re-routed decision flows back on the
// result, and the router is actually invoked.
func TestResumeTask_ReroutesNeverStartedTask(t *testing.T) {
	// First call: the router (re-route). Second call: the resumed Conductor.
	caller := &scriptedLLM{scripted: []*llm.ChatResponse{
		routingJSONResponse("research", 4),
		finishResponse("rerouted-done"),
	}}
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-reroute", SessionID: "ignored", OriginalRequest: "study the paper",
			// "failed" is what a routing failure leaves behind (FailTask);
			// no routing decision and no trajectory.
			Status: "failed",
		},
		trajectory: nil,
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(routingFunctionalFactory(caller), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
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
		t.Fatalf("expected successful completion, got completion=%q output=%q", data.Completion, data.Output)
	}
	if data.Output != "rerouted-done" {
		t.Errorf("output = %q, want %q", data.Output, "rerouted-done")
	}
	// The resumed run must carry the RE-ROUTED decision, not the general default.
	if data.RoutingDecision == nil || data.RoutingDecision.Domain != "research" {
		t.Fatalf("resumed routing decision = %+v, want domain research (task must be re-routed)", data.RoutingDecision)
	}
	// The router must have run: route + finish = at least two LLM calls.
	if got := len(caller.Calls()); got < 2 {
		t.Fatalf("expected at least 2 LLM calls (route + finish), got %d", got)
	}
}

// TestResumeTask_StartedTaskIsNotRerouted is the negative companion to
// TestResumeTask_ReroutesNeverStartedTask: a task that DID execute (a persisted
// trajectory) but has no routing decision keeps the pre-existing behavior — it
// is resumed with the "general" default and the router is NOT invoked (the
// factory's router would otherwise consume a script and fail the finish-only
// mock). This guards against re-routing a genuine continuation.
func TestResumeTask_StartedTaskIsNotRerouted(t *testing.T) {
	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-started", SessionID: "ignored", OriginalRequest: "long running task",
			Status: "in_progress",
			// No routing decision, but a trajectory: the task executed, so the
			// missing routing is an anomaly, not a pre-routing failure.
		},
		trajectory: trajJSON,
	}

	eventChan := make(chan Event, 100)
	// functionalOrchestratorFactory deliberately wires NO router: if the resume
	// path tried to route, it would panic — so a clean completion proves the
	// started task was not re-routed.
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "not-rerouted"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
	if !data.Success || data.Output != "not-rerouted" {
		t.Fatalf("expected successful completion %q, got success=%v output=%q", "not-rerouted", data.Success, data.Output)
	}
	if data.RoutingDecision != nil {
		t.Errorf("a started task must not be re-routed, got routing %+v", data.RoutingDecision)
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
	mgr := NewManager(overrideFunctionalFactory(caller, switcher), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
	}, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
	}, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "should-not-run"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
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

// TestResumeTask_PausedGoalBlockedByModelProfilesNarrowing verifies the manager-level
// choke point for the paused-goal × narrowing exclusion: resuming a paused
// non-terminal goal while the Model Profiles essential-tools narrowing is active is
// refused with core.ErrGoalBlockedByModelProfiles BEFORE any side effect — so the task row
// stays 'paused' (never flipped to in_progress) and the session is not
// activated. This is what keeps a later plain message on the nudge-resume path
// instead of degrading into a plain continuation with the goal tools stripped.
func TestResumeTask_PausedGoalBlockedByModelProfilesNarrowing(t *testing.T) {
	gsJSON, err := json.Marshal(&goal.GoalState{
		Condition:    "ship the feature",
		VerifyClause: "go test ./...",
		Status:       goal.StatusActive,
		CreatedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal goal state: %v", err)
	}
	store := &resumeTaskStore{
		task:      &TaskRecord{ID: "task-goal-guard", SessionID: "ignored", OriginalRequest: "goal", Status: "paused"},
		goalState: gsJSON,
	}
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "should-not-run"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	sess, ok := mgr.GetSession(info.ID)
	if !ok || sess == nil || sess.orchestrator == nil {
		t.Fatal("session/orchestrator not available after CreateSession")
	}
	// Arm the narrowing on the live orchestrator — the runtime path that
	// SetModelProfilesSettings represents (a settings toggle), leaving the persisted goal
	// row non-terminal.
	sess.orchestrator.SetModelProfilesSettings(core.ModelProfilesSettings{Enabled: true, EssentialTools: core.ModelProfilesEssentialSettings{Enabled: true}})
	drainEvents(eventChan)

	if err = mgr.ResumeTask(context.Background(), info.ID, "", "", ""); !errors.Is(err, core.ErrGoalBlockedByModelProfiles) {
		t.Fatalf("ResumeTask error = %v, want core.ErrGoalBlockedByModelProfiles", err)
	}

	// No side effect: the row must not have been flipped to in_progress, and the
	// session must not be active.
	store.mu.Lock()
	reactivated := store.reactivateCalls
	status := store.task.Status
	store.mu.Unlock()
	if reactivated != 0 {
		t.Errorf("ReactivateTask called %d times, want 0 — the guard must precede it", reactivated)
	}
	if status != "paused" {
		t.Errorf("task status = %q, want %q (a blocked resume must leave the row resumable)", status, "paused")
	}
	sess.mu.Lock()
	active := sess.active
	sess.mu.Unlock()
	if active {
		t.Error("session must not be activated by a blocked resume")
	}
	// The row still reads as a paused unfinished task, so the next plain message
	// re-enters the nudge-resume path (not tryContinueInterruptedTask).
	if !mgr.hasPausedUnfinishedTask(info.ID) {
		t.Error("task should still read as a paused unfinished task after the block")
	}
}

// TestManager_SetModelProfilesSettings_PushesToLiveOrchestrators verifies the runtime ModelProfiles
// push (Issue 1): a live session's already-built orchestrator picks up the
// refreshed settings, so a narrowing toggled after the session was built takes
// effect — and is cleared again — without a rebuild.
func TestManager_SetModelProfilesSettings_PushesToLiveOrchestrators(t *testing.T) {
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "x"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown)
	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, ok := mgr.GetSession(info.ID)
	if !ok || sess.orchestrator == nil {
		t.Fatal("session/orchestrator not available after CreateSession")
	}
	if sess.orchestrator.ModelProfilesNarrowingEnabled() {
		t.Fatal("precondition: narrowing must be off at build time")
	}

	mgr.SetModelProfilesSettings(core.ModelProfilesSettings{Enabled: true, EssentialTools: core.ModelProfilesEssentialSettings{Enabled: true}})
	if !sess.orchestrator.ModelProfilesNarrowingEnabled() {
		t.Fatal("SetModelProfilesSettings did not reach the already-built orchestrator")
	}

	mgr.SetModelProfilesSettings(core.ModelProfilesSettings{})
	if sess.orchestrator.ModelProfilesNarrowingEnabled() {
		t.Fatal("SetModelProfilesSettings did not clear the narrowing on the live orchestrator")
	}
}

// TestResumeTask_ReactivatesPausedRowDuringRun verifies the paused-ghost fix:
// a task persisted as 'paused' must have its row flipped to 'in_progress' for
// the duration of the resumed run. Without the ReactivateTask call in
// ResumeTask, the row stays 'paused' while the task is actively executing —
// the store then claims a paused (idle) task over a live one.
func TestResumeTask_ReactivatesPausedRowDuringRun(t *testing.T) {
	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-reactivate", SessionID: "ignored", OriginalRequest: "paused task",
			Status: "paused",
		},
		trajectory: trajJSON,
	}

	gate := &gatingLLM{started: make(chan struct{}), release: make(chan struct{})}
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(gate), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	// Block until the resumed executor is inside its first LLM call: the task
	// is now mid-execution.
	select {
	case <-gate.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the resumed executor to reach its LLM call")
	}

	store.mu.Lock()
	status := store.task.Status
	reactivations := store.reactivateCalls
	store.mu.Unlock()
	if reactivations != 1 {
		t.Errorf("ReactivateTask calls = %d, want 1 (ResumeTask must flip the paused row before launching the run)", reactivations)
	}
	if status != "in_progress" {
		t.Errorf("task row status during resumed execution = %q, want %q (paused-ghost: the row must be reactivated for the run's duration)", status, "in_progress")
	}

	// Release the executor; the run must complete normally.
	close(gate.release)
	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event")
	}
}

// TestResumeTask_ReactivationFailureDoesNotAbortResume verifies the best-effort
// contract of the reactivation: when the UPDATE fails, ResumeTask still
// proceeds — the error is logged as a warning, never surfaced, and the resumed
// run executes and completes.
func TestResumeTask_ReactivationFailureDoesNotAbortResume(t *testing.T) {
	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-reactivate-err", SessionID: "ignored", OriginalRequest: "paused task",
			Status: "paused",
		},
		trajectory:    trajJSON,
		reactivateErr: errors.New("database is locked"),
	}

	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&finishLLM{answer: "despite-error"}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	// The resume must run to completion despite the failed reactivation.
	if _, ok := waitForEvent(eventChan, "task_complete", 3*time.Second); !ok {
		t.Fatal("timeout waiting for task_complete event (a reactivation failure must not abort the resume)")
	}

	store.mu.Lock()
	reactivations := store.reactivateCalls
	store.mu.Unlock()
	if reactivations != 1 {
		t.Errorf("ReactivateTask calls = %d, want 1 (the UPDATE must still be attempted)", reactivations)
	}
}

// TestResumeTask_CooperativePauseDuringResumedRunRewritesPaused verifies the
// pause contract survives reactivation: a cooperative pause in the middle of a
// resumed run rewrites the row to 'paused' (persistTaskOutcome → PauseTask),
// so the checkpoint stays resumable instead of being lost behind the
// in_progress flip.
func TestResumeTask_CooperativePauseDuringResumedRunRewritesPaused(t *testing.T) {
	trajJSON, _ := json.Marshal([]agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "pc1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "PRIOR"},
	})
	store := &resumeTaskStore{
		task: &TaskRecord{
			ID: "task-resume-pause-again", SessionID: "ignored", OriginalRequest: "paused task",
			Status: "paused",
		},
		trajectory: trajJSON,
	}

	// The first LLM response is a non-terminal tool call so the executor
	// completes a step and reaches the next step boundary, where the pause
	// signal — flipped while the call was gated — trips.
	gate := &gatingLLM{firstToolCall: "read_file", started: make(chan struct{}), release: make(chan struct{})}
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(gate), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown) // stop the manager before its temp dirs are removed
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	store.mu.Lock()
	store.task.SessionID = info.ID
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	select {
	case <-gate.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the resumed executor to reach its LLM call")
	}

	// Sanity: the row was reactivated for the run.
	store.mu.Lock()
	status := store.task.Status
	store.mu.Unlock()
	if status != "in_progress" {
		t.Fatalf("precondition: task row status during resumed execution = %q, want in_progress", status)
	}

	// Request a cooperative pause while the executor is blocked mid-step,
	// then let the step complete: the next step boundary trips the signal.
	if err := mgr.PauseSession(info.ID); err != nil {
		t.Fatalf("PauseSession failed: %v", err)
	}
	close(gate.release)

	if _, ok := waitForEvent(eventChan, "session_paused", 3*time.Second); !ok {
		t.Fatal("timeout waiting for session_paused event")
	}

	store.mu.Lock()
	status = store.task.Status
	pauses := store.pauseCalls
	store.mu.Unlock()
	if pauses != 1 {
		t.Errorf("PauseTask calls = %d, want 1 (a cooperative pause must persist the checkpoint)", pauses)
	}
	if status != "paused" {
		t.Errorf("task row status after mid-run pause = %q, want %q (the checkpoint must overwrite the reactivated in_progress)", status, "paused")
	}
}

// ---------------------------------------------------------------------------
// Pause mid-plan → resume → the plan completes without errors/re-publication
//
// These tests exercise the FULL plan workflow through the manager: a task
// declares a plan, starts executing it, and is cooperatively paused (or shut
// down) mid-plan; the persisted checkpoint (paused row + plan + step results)
// is then resumed and the conductor continues the SAME plan to completion —
// every step terminal, no re-declared plan, no errors.
// ---------------------------------------------------------------------------

// inMemoryTaskStore is a stateful TaskStore fake mirroring the observable
// semantics of SQLiteSessionStore: upserted task rows, per-task step records
// (replace by step id), trajectory/facts/attachments/goal-state blobs, and
// GetUnfinishedTask matching in_progress/paused/failed ordered by creation.
// Unlike the canned resumeTaskStore, it supports a REAL run persisting into
// it and a later restore reading it back — the persistence half of the
// pause→resume scenario.
type inMemoryTaskStore struct {
	mu           sync.Mutex
	tasks        map[string]TaskRecord
	steps        map[string][]TaskStepRecord // taskID → records (replace by StepID)
	trajectories map[string]json.RawMessage
	facts        map[string]json.RawMessage
	attachments  map[string]json.RawMessage
	goalStates   map[string]json.RawMessage
	e2sStates    map[string]json.RawMessage
	delegSpecs   map[string][]TaskDelegationRecord // taskID → records (replace by DelegationID)

	pauseCalls      int
	reactivateCalls int
	completeCalls   int
}

func newInMemoryTaskStore() *inMemoryTaskStore {
	return &inMemoryTaskStore{
		tasks:        make(map[string]TaskRecord),
		steps:        make(map[string][]TaskStepRecord),
		trajectories: make(map[string]json.RawMessage),
		facts:        make(map[string]json.RawMessage),
		attachments:  make(map[string]json.RawMessage),
		goalStates:   make(map[string]json.RawMessage),
		e2sStates:    make(map[string]json.RawMessage),
		delegSpecs:   make(map[string][]TaskDelegationRecord),
	}
}

func (s *inMemoryTaskStore) SaveTask(_ context.Context, rec TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[rec.ID] = rec
	return nil
}

func (s *inMemoryTaskStore) UpdateTaskPlan(_ context.Context, taskID string, plan json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.Plan = plan
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) UpdateTaskRouting(_ context.Context, taskID string, routing json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.RoutingDecision = routing
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) SaveTaskStep(_ context.Context, taskID string, step TaskStepRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.steps[taskID]
	for i := range records {
		if records[i].StepID == step.StepID {
			records[i] = step // INSERT OR REPLACE semantics
			return nil
		}
	}
	s.steps[taskID] = append(records, step)
	return nil
}

func (s *inMemoryTaskStore) AddTaskReflection(_ context.Context, taskID string, reflectionJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	list := make([]json.RawMessage, 0, 1)
	if len(rec.Reflections) > 0 {
		_ = json.Unmarshal(rec.Reflections, &list)
	}
	list = append(list, reflectionJSON)
	updated, err := json.Marshal(list)
	if err != nil {
		return err
	}
	rec.Reflections = updated
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) CompleteTask(_ context.Context, taskID, finalOutput string, attemptCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	now := time.Now().UTC()
	rec.Status = "completed"
	rec.FinalOutput = finalOutput
	rec.AttemptCount = attemptCount
	rec.CompletedAt = &now
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) FailTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.Status = "failed"
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) CancelTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.Status = "cancelled"
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) PauseTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauseCalls++
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.Status = "paused"
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) ReactivateTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactivateCalls++
	rec, ok := s.tasks[taskID]
	if !ok {
		return errors.New("task not found: " + taskID)
	}
	rec.Status = "in_progress"
	s.tasks[taskID] = rec
	return nil
}

func (s *inMemoryTaskStore) LoadTask(_ context.Context, taskID string) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return nil, nil
	}
	cp := rec
	return &cp, nil
}

func (s *inMemoryTaskStore) LoadTaskSteps(_ context.Context, taskID string) ([]TaskStepRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskStepRecord, len(s.steps[taskID]))
	copy(out, s.steps[taskID])
	return out, nil
}

func (s *inMemoryTaskStore) SaveFacts(_ context.Context, taskID string, factsJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts[taskID] = factsJSON
	return nil
}

func (s *inMemoryTaskStore) LoadFacts(_ context.Context, taskID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.facts[taskID], nil
}

func (s *inMemoryTaskStore) SaveAttachments(_ context.Context, taskID string, attachmentsJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[taskID] = attachmentsJSON
	return nil
}

func (s *inMemoryTaskStore) LoadAttachments(_ context.Context, taskID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachments[taskID], nil
}

func (s *inMemoryTaskStore) SaveTrajectory(_ context.Context, taskID string, stepsJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trajectories[taskID] = stepsJSON
	return nil
}

func (s *inMemoryTaskStore) LoadTrajectory(_ context.Context, taskID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trajectories[taskID], nil
}

func (s *inMemoryTaskStore) SaveGoalState(_ context.Context, taskID string, goalStateJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalStates[taskID] = goalStateJSON
	return nil
}

func (s *inMemoryTaskStore) LoadGoalState(_ context.Context, taskID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalStates[taskID], nil
}

func (s *inMemoryTaskStore) SaveE2SState(_ context.Context, taskID string, e2sStateJSON json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.e2sStates[taskID] = e2sStateJSON
	return nil
}

func (s *inMemoryTaskStore) LoadE2SState(_ context.Context, taskID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e2sStates[taskID], nil
}

func (s *inMemoryTaskStore) SaveDelegationSpec(_ context.Context, taskID string, rec TaskDelegationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.delegSpecs[taskID]
	replaced := false
	for i := range existing {
		if existing[i].DelegationID == rec.DelegationID {
			existing[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, rec)
	}
	s.delegSpecs[taskID] = existing
	return nil
}

func (s *inMemoryTaskStore) LoadDelegationSpecs(_ context.Context, taskID string) ([]TaskDelegationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskDelegationRecord, len(s.delegSpecs[taskID]))
	copy(out, s.delegSpecs[taskID])
	return out, nil
}

func (s *inMemoryTaskStore) GetUnfinishedTask(_ context.Context, sessionID string) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *TaskRecord
	for _, rec := range s.tasks {
		if rec.SessionID != sessionID {
			continue
		}
		switch rec.Status {
		case "in_progress", "paused", "failed":
			if best == nil || rec.CreatedAt.After(best.CreatedAt) {
				cp := rec
				best = &cp
			}
		}
	}
	return best, nil
}

func (s *inMemoryTaskStore) GetLatestTaskID(_ context.Context, sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *TaskRecord
	for _, rec := range s.tasks {
		if rec.SessionID != sessionID {
			continue
		}
		if best == nil || rec.CreatedAt.After(best.CreatedAt) {
			cp := rec
			best = &cp
		}
	}
	if best == nil {
		return "", nil
	}
	return best.ID, nil
}

// passthroughTool is a minimal always-allow registry tool for session-level
// plan-workflow tests: the plan-step subagents execute it as real work.
type passthroughTool struct {
	*sdktools.BaseTool
}

func newPassthroughBashExec() *passthroughTool {
	return &passthroughTool{BaseTool: &sdktools.BaseTool{
		ToolGroup:       sdktools.GroupExecute,
		ToolName:        "bash_exec",
		ToolDescription: "Execute bash commands (test stub)",
		Schema:          json.RawMessage(`{"type":"object"}`),
		Policy:          sdktools.PolicyAlwaysAllow,
	}}
}

func (t *passthroughTool) Execute(_ context.Context, _ json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: "PASSED:command ran"}, nil
}

// planWorkflowFactory builds a real orchestrator WITH a router (SendMessage
// path) and the real declare_plan/execute_plan tools registered alongside a
// stub bash_exec, so the complete declare → execute → pause → resume plan
// workflow runs end-to-end through the manager.
func planWorkflowFactory(caller agent.LLMCaller, bash *passthroughTool) OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, bbFactory core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		registry.Register(bash)
		registry.Register(coretools.NewDeclarePlanTool(nil))
		registry.Register(coretools.NewExecutePlanTool())
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
			BBFactory:      bbFactory,
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
			CircuitBreaker: agent.CircuitBreakerConfig{RepeatNudgeThreshold: 3, RepeatAbortThreshold: 4},
		}), nil
	}
}

// scriptedPlanStep is one entry of a plan-workflow LLM script; gate/started
// coordinate the mid-plan pause with the test (see pauseScriptStep in core).
type scriptedPlanStep struct {
	respond *llm.ChatResponse
	started chan struct{}
	gate    chan struct{}
}

// scriptedPlanLLM plays a fixed call sequence across the router, the
// Conductor loop, and every plan-step subagent (they all share the caller).
type scriptedPlanLLM struct {
	mu     sync.Mutex
	script []scriptedPlanStep
	idx    int
}

func (s *scriptedPlanLLM) Call(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	i := s.idx
	if i >= len(s.script) {
		s.mu.Unlock()
		return nil, errors.New("scriptedPlanLLM: script exhausted — the run deviated from the pause/resume choreography")
	}
	step := s.script[i]
	s.idx++
	s.mu.Unlock()
	if step.started != nil {
		close(step.started)
	}
	if step.gate != nil {
		// Hold the caller until the test releases the gate OR the run's
		// context is cancelled — the shutdown scenario releases exactly when
		// Shutdown cancels the task, with no timing window to get wrong.
		select {
		case <-step.gate:
		case <-ctx.Done():
		}
	}
	return step.respond, nil
}

// declarePlanCall builds the declare_plan tool call for the shared two-step
// roadmap used by the mid-plan pause tests.
func declarePlanCall(id string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "declaring the plan",
			ToolCalls: []llm.ToolCall{{ID: id, Name: "declare_plan", Input: json.RawMessage(`{"tasks":[
				{"id":"s1","summary":"Do the groundwork","description":"s1: do the groundwork"},
				{"id":"s2","summary":"Finish the build","description":"s2: finish the build","depends_on":["s1"]}]}`)}},
		},
		StopReason: "tool_use",
	}
}

// executePlanCall builds the execute_plan tool call.
func executePlanCall(id string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "executing the plan",
			ToolCalls: []llm.ToolCall{{ID: id, Name: "execute_plan", Input: json.RawMessage(`{}`)}},
		},
		StopReason: "tool_use",
	}
}

// bashExecCall builds the plan-step subagent's work tool call.
func bashExecCall(id string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "doing the groundwork",
			ToolCalls: []llm.ToolCall{{ID: id, Name: "bash_exec", Input: json.RawMessage(`{"command":"echo groundwork","timeout":"5s"}`)}},
		},
		StopReason: "tool_use",
	}
}

// TestResumeTask_PausedMidPlan_ResumeCompletesAllStepsTerminal covers the
// manager-level pause→resume scenario end-to-end: SendMessage declares a
// two-step plan and starts executing it; the user pauses mid-plan while s1's
// subagent is mid-work (its partial trajectory is checkpointed, s2 is
// untouched); ResumeTask then restores the blackboard (plan + paused step
// result) and the conductor continues the SAME plan — s1 re-runs from its
// checkpoint, s2 finally runs, the row completes, and the plan is never
// re-declared (no second plan_generated).
func TestResumeTask_PausedMidPlan_ResumeCompletesAllStepsTerminal(t *testing.T) {
	caller := &scriptedPlanLLM{script: []scriptedPlanStep{
		{respond: routingJSONResponse("general", 2)}, // run 1: router classification
		{respond: declarePlanCall("c1")},             // run 1: declare the roadmap
		{respond: executePlanCall("c2")},             // run 1: start executing it
		// s1's subagent is gated mid-work: the test pauses the session while
		// it is blocked here, then releases it to return real work.
		{respond: bashExecCall("g1"), started: make(chan struct{}), gate: make(chan struct{})},
		// Run 2 (ResumeTask): execute_plan continues — NO declare_plan, NO
		// re-routing (Resume reuses the persisted routing decision).
		{respond: executePlanCall("c3")},
		{respond: finishResponse("s1 resumed done")}, // s1 subagent, resumed
		{respond: finishResponse("s2 done")},         // s2 subagent
		{respond: finishResponse("plan finished")},   // Conductor finishes
	}}
	bash := newPassthroughBashExec()
	store := newInMemoryTaskStore()

	eventChan := make(chan Event, 200)
	mgr := NewManager(planWorkflowFactory(caller, bash), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	ws := runtimeTempDir(t)
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := mgr.SendMessage(context.Background(), info.ID, "build the widget in two planned steps", nil, nil, "", "", false, "", false, false); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Wait until s1's subagent is blocked in its (gated) LLM call.
	select {
	case <-caller.script[3].started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the s1 subagent to reach its (gated) LLM call")
	}

	// User pause mid-plan: the signal is armed while the subagent is blocked;
	// releasing it lets the step finish and the next boundary trips the pause.
	if err := mgr.PauseSession(info.ID); err != nil {
		t.Fatalf("PauseSession failed: %v", err)
	}
	close(caller.script[3].gate)

	if _, ok := waitForEvent(eventChan, "session_paused", 5*time.Second); !ok {
		t.Fatal("timeout waiting for session_paused event")
	}

	// The mid-plan checkpoint is fully persisted: a paused row whose state
	// carries the plan, s1's paused checkpoint (with its partial trajectory),
	// and NO result for the never-started s2.
	adapter := NewTaskStoreAdapter(store)
	taskID, err := adapter.GetUnfinishedTaskID(info.ID)
	if err != nil || taskID == "" {
		t.Fatalf("GetUnfinishedTaskID after pause = %q, %v — want the paused task", taskID, err)
	}
	state, err := adapter.LoadTaskState(taskID)
	if err != nil || state == nil {
		t.Fatalf("LoadTaskState after pause = %+v, %v", state, err)
	}
	if state.Status != "paused" {
		t.Errorf("checkpointed row status = %q, want paused", state.Status)
	}
	if state.Plan == nil || len(state.Plan.Steps) != 2 {
		t.Fatalf("checkpointed plan = %+v, want the declared two-step plan", state.Plan)
	}
	if sr, ok := state.StepResults["s1"]; !ok || sr.Error == nil || sr.Error.Error() != agent.ErrPaused.Error() || len(sr.Steps) != 1 {
		t.Fatalf("checkpointed s1 result = %+v (ok=%v), want the paused checkpoint with its partial trajectory", sr, ok)
	}
	if _, ok := state.StepResults["s2"]; ok {
		t.Error("s2 must have no persisted result (never started before the pause)")
	}
	traj, err := adapter.LoadTrajectory(taskID)
	if err != nil || len(traj) == 0 {
		t.Fatalf("persisted trajectory after pause is empty (%v) — the checkpoint must survive a restart", err)
	}

	// Drain run-1 events so the resume run's assertions start clean.
	drainEvents(eventChan)

	// Resume: RestoreBlackboard + ResumeTask — the conductor continues the
	// SAME plan without errors and without re-publication.
	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}
	complete, ok := waitForEvent(eventChan, "task_complete", 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete event")
	}
	data, ok := complete.Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", complete.Data)
	}
	if !data.Success || data.Output != "plan finished" {
		t.Errorf("resumed completion = success=%v output=%q, want success with %q", data.Success, data.Output, "plan finished")
	}

	// No re-publication: the resume must not emit a single plan_generated.
	if n := countEvents(eventChan, "plan_generated"); n != 0 {
		t.Errorf("resume emitted %d plan_generated event(s) — the approved plan must be continued, not re-declared", n)
	}

	// Every step reached a terminal, error-free state and the row completed.
	finalState, err := adapter.LoadTaskState(taskID)
	if err != nil || finalState == nil {
		t.Fatalf("LoadTaskState after resume = %+v, %v", finalState, err)
	}
	if finalState.Status != "completed" {
		t.Errorf("task row status after resume = %q, want completed", finalState.Status)
	}
	sr1, ok := finalState.StepResults["s1"]
	if !ok || sr1.Error != nil || sr1.FullOutput != "s1 resumed done" {
		t.Errorf("s1 after resume = %+v (ok=%v), want a successful step with the resumed output", sr1, ok)
	}
	sr2, ok := finalState.StepResults["s2"]
	if !ok || sr2.Error != nil || sr2.FullOutput != "s2 done" {
		t.Errorf("s2 after resume = %+v (ok=%v), want a successful step", sr2, ok)
	}
	// The approved plan was never replaced: same step IDs and summaries.
	if finalState.Plan == nil || len(finalState.Plan.Steps) != 2 ||
		finalState.Plan.Steps[0].ID != "s1" || finalState.Plan.Steps[1].ID != "s2" ||
		finalState.Plan.Steps[0].Summary != "Do the groundwork" {
		t.Errorf("plan after resume = %+v, want the originally declared two-step plan (append-only)", finalState.Plan)
	}
}

// e2sRecordingLLM records every ChatRequest it serves and always answers with
// a valid e2s_step finish call. It is the E2S stand-in for finishLLM: the
// recorded user message carries the loop's <state> block, so the test can
// assert exactly which Σ the resumed run saw.
type e2sRecordingLLM struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (e *e2sRecordingLLM) Call(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "checkpoint carried over",
			ToolCalls: []llm.ToolCall{{
				ID:    "e2s-resume-1",
				Name:  e2s.StepToolName,
				Input: json.RawMessage(`{"state_patch":{},"action":{"tool":"finish","args":{"answer":"e2s resumed with restored sigma"}}}`),
			}},
		},
		StopReason: "tool_use",
	}, nil
}

// userMessages returns the user-role message contents of the recorded
// requests, in call order.
func (e *e2sRecordingLLM) userMessages() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.requests))
	for _, r := range e.requests {
		for _, m := range r.Messages {
			if m.Role == "user" {
				out = append(out, m.Content)
			}
		}
	}
	return out
}
