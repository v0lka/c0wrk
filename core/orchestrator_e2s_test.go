package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// e2sStepResponse builds an LLM response carrying exactly one e2s_step tool
// call with the given patch and action.
func e2sStepResponse(callID, patchJSON, tool, argsJSON string) *llm.ChatResponse {
	input := fmt.Sprintf(`{"state_patch": %s, "action": {"tool": %q, "args": %s}}`, patchJSON, tool, argsJSON)
	return &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:    callID,
				Name:  e2s.StepToolName,
				Input: json.RawMessage(input),
			}},
		},
		StopReason: "tool_use",
	}
}

// e2sFinishResponse builds an e2s_step response that finishes the run.
func e2sFinishResponse(callID, answer string) *llm.ChatResponse {
	return e2sStepResponse(callID, `{}`, e2s.FinishActionName, fmt.Sprintf(`{"answer": %q}`, answer))
}

// mockDelegationLauncher records Launch invocations and returns canned
// results — the injected-launcher seam (Orchestrator.e2sLauncher) test double.
type mockDelegationLauncher struct {
	mu     sync.Mutex
	launch int
	tasks  []tools.DelegationTask
	out    []tools.DelegationResult
}

func (m *mockDelegationLauncher) Launch(_ context.Context, tasks []tools.DelegationTask, registry *tools.DelegationRegistry) []tools.DelegationResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launch++
	m.tasks = append(m.tasks, tasks...)
	// Mirror the real launcher's blocking-delegation bookkeeping: a
	// delegation whose result is returned synchronously is marked completed
	// in the registry, so the E2S finish-join guard (ListPending) does not
	// veto a later finish over already-settled work.
	for _, res := range m.out {
		if res.Status == tools.DelegationStatusCompleted && registry != nil {
			registry.Complete(res.ID, res.Output, nil, nil)
		}
	}
	return m.out
}

func (m *mockDelegationLauncher) CompletedStep(string) (tools.DelegationCompletedStep, bool) {
	return tools.DelegationCompletedStep{}, false
}

// e2sCheckpointStore extends the plain mock task store with the optional
// E2S persistence capability (e2sStatePersister), keeping the persisted
// checkpoints in memory.
type e2sCheckpointStore struct {
	*mockTaskStore
	mu    sync.Mutex
	saved map[string]*e2s.E2SState
}

func newE2SCheckpointStore() *e2sCheckpointStore {
	return &e2sCheckpointStore{mockTaskStore: &mockTaskStore{}, saved: map[string]*e2s.E2SState{}}
}

func (s *e2sCheckpointStore) PersistE2SState(taskID string, state *e2s.E2SState) error {
	cp := *state
	s.mu.Lock()
	s.saved[taskID] = &cp
	s.mu.Unlock()
	return nil
}

func (s *e2sCheckpointStore) LoadE2SState(taskID string) (*e2s.E2SState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if es, ok := s.saved[taskID]; ok {
		cp := *es
		return &cp, nil
	}
	return nil, nil
}

func (s *e2sCheckpointStore) load(taskID string) *e2s.E2SState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if es, ok := s.saved[taskID]; ok {
		return es
	}
	return nil
}

// newE2STestOrchestrator wires an orchestrator for E2S-mode tests. When store
// is non-nil the blackboard factory produces PersistableBlackboards wired to
// it, so checkpoint persistence (pause/resume) can be exercised.
func newE2STestOrchestrator(mockLLM *mockLLMCaller, registry *sdktools.ToolRegistry, emitter Emitter, store TaskPersistence) *Orchestrator {
	deps := OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	}
	if emitter != nil {
		deps.Emitter = emitter
	}
	if store != nil {
		deps.BBFactory = func(taskID string) orchestration.Blackboard {
			return &testPersistableBlackboard{
				MapBlackboard: orchestration.NewMapBlackboard(),
				taskID:        taskID,
				store:         store,
			}
		}
	}
	o := NewOrchestrator(OrchestratorConfig{E2S: E2SSettings{Enabled: true}}, deps)
	if store != nil {
		o.SetTaskStore(store)
	}
	return o
}

// ----------------------------------------------------------------------------
// Pure unit tests: tool stripping + registry adapter
// ----------------------------------------------------------------------------

func TestStripE2SUnavailableTools(t *testing.T) {
	in := []sdktools.ToolDescriptor{
		{Name: "read_file"}, {Name: "bash_exec"}, {Name: "delegate"},
		{Name: "cancel_delegation"}, {Name: "finish"}, {Name: "update_checklist"},
		{Name: "declare_plan"}, {Name: "execute_plan"}, {Name: "declare_step_complete"},
		{Name: "reflect"},
	}
	got := stripE2SUnavailableTools(in)
	want := map[string]bool{
		"read_file": true, "bash_exec": true, "delegate": true,
		"cancel_delegation": true, "finish": true,
	}
	if len(got) != len(want) {
		t.Fatalf("stripped list = %v, want exactly %d entries", got, len(want))
	}
	for _, d := range got {
		if !want[d.Name] {
			t.Errorf("tool %q survived the E2S strip; plan-workflow tools must be removed", d.Name)
		}
	}
}

// recordingToolExec captures Execute calls; the adapter test double.
type recordingToolExec struct {
	mu    sync.Mutex
	calls []struct {
		name  string
		input json.RawMessage
	}
}

func (r *recordingToolExec) Execute(_ context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, struct {
		name  string
		input json.RawMessage
	}{name, input})
	r.mu.Unlock()
	return sdktools.ToolResult{Content: "ok:" + name}, nil
}
func (r *recordingToolExec) GetToolSource(string) string { return "core" }
func (r *recordingToolExec) IsToolUntrusted(string) bool { return false }
func (r *recordingToolExec) CacheStrategy(context.Context, string, json.RawMessage) sdktools.CacheMode {
	return sdktools.CacheModeDefault
}

var _ agent.ToolExecutor = (*recordingToolExec)(nil)

func TestE2SRegistryAdapter_CatalogFilteredAndDispatchGated(t *testing.T) {
	inner := &recordingToolExec{}
	descs := []sdktools.ToolDescriptor{
		{Name: "bash_exec"}, {Name: "read_file"}, {Name: "delegate"},
	}
	adapter := newE2SRegistryAdapter(inner, descs)

	// Catalog exposes exactly the filtered descriptors.
	listed := adapter.List()
	if len(listed) != 3 {
		t.Fatalf("List() = %d descriptors, want 3", len(listed))
	}

	// Allowed tool dispatches to the inner executor with name + args intact.
	res, err := adapter.Execute(context.Background(), "bash_exec", json.RawMessage(`{"command":"echo hi"}`))
	if err != nil || res.IsError {
		t.Fatalf("Execute(allowed) = (%v, %v), want clean dispatch", res, err)
	}
	if len(inner.calls) != 1 || inner.calls[0].name != "bash_exec" {
		t.Fatalf("inner executor saw %v, want one bash_exec dispatch", inner.calls)
	}

	// A stripped (hallucinated) plan tool is rejected fail-closed and never
	// reaches the real executor.
	res, err = adapter.Execute(context.Background(), "declare_plan", json.RawMessage(`{"steps":[]}`))
	if err != nil {
		t.Fatalf("Execute(stripped) returned error %v, want error RESULT (not a Go error)", err)
	}
	if !res.IsError {
		t.Fatal("Execute(stripped) must return an error result — the catalog is the contract")
	}
	if len(inner.calls) != 1 {
		t.Fatalf("stripped dispatch reached the real executor: %v", inner.calls)
	}
}

// ----------------------------------------------------------------------------
// Acceptance: E2S=true never calls routeOrContinue / runConductor
// ----------------------------------------------------------------------------

// TestHandleMessage_E2S_SkipsRoutingAndConductor drives HandleMessage with
// E2S=true and a mock LLM whose ONLY call must be the E2S loop's one-shot
// [system, user] dialog. If routeOrContinue ran, the router's classification
// call would arrive first (and a Routing event would be emitted); if
// runConductor ran, the request would carry the full Conductor system prompt
// and tool definitions for every registry tool instead.
func TestHandleMessage_E2S_SkipsRoutingAndConductor(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			// Every call answers the same finish-on-turn-1; the assertions
			// below pin the CALL COUNT to exactly one.
			return e2sFinishResponse("e2s-1", "e2s finished the task"), nil
		},
	}
	emitter := &spyEmitter{}
	o := newE2STestOrchestrator(mockLLM, createTestRegistry(), emitter, nil)

	result, err := o.HandleMessage(context.Background(), "do the thing in e2s mode", "session-e2s-1", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	t.Run("exactly one LLM call, fresh [system,user] dialog", func(t *testing.T) {
		mockLLM.mu.Lock()
		calls := len(mockLLM.calls)
		var first llm.ChatRequest
		if calls > 0 {
			first = mockLLM.calls[0]
		}
		mockLLM.mu.Unlock()
		if calls != 1 {
			t.Fatalf("LLM calls = %d, want exactly 1 (the E2S loop's single turn; a router or Conductor call would add more)", calls)
		}
		if len(first.Messages) != 2 || first.Messages[0].Role != "system" || first.Messages[1].Role != "user" {
			t.Fatalf("request shape = %d messages, want exactly [system, user] (the E2S one-shot dialog)", len(first.Messages))
		}
		if !strings.Contains(first.Messages[0].Content, "## Available Tools") {
			t.Error("system prompt lacks the E2S '## Available Tools' section — this is not the E2S prompt")
		}
		if !strings.Contains(first.Messages[1].Content, "<state>") {
			t.Error("user message lacks the <state> block — this is not the E2S per-turn user message")
		}
	})

	t.Run("no Routing event (routeOrContinue skipped)", func(t *testing.T) {
		if _, _, _, ok := routingCall(emitter); ok {
			t.Error("a Routing event was emitted — routeOrContinue must not run in E2S mode")
		}
	})

	t.Run("e2s_state event emitted", func(t *testing.T) {
		for _, c := range emitter.calls {
			if c.method == "E2SState" {
				return
			}
		}
		t.Error("expected an E2SState event from the loop's state snapshot emission")
	})

	t.Run("finish answer becomes the successful result", func(t *testing.T) {
		if result == nil || result.Status != orchestration.ExecutionStatusSuccess {
			t.Fatalf("HandleResult.Status = %+v, want success", result)
		}
		if result.Output != "e2s finished the task" {
			t.Errorf("HandleResult.Output = %q, want the finish answer", result.Output)
		}
	})
}

// ----------------------------------------------------------------------------
// Acceptance: Goal + E2S → explicit error
// ----------------------------------------------------------------------------

// TestHandleMessage_E2S_GoalMutualExclusion verifies the server-side
// exclusivity defense: arming both mode flags is rejected outright, before
// either mode runs.
func TestHandleMessage_E2S_GoalMutualExclusion(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return e2sFinishResponse("x", "should never run"), nil
		},
	}
	o := newE2STestOrchestrator(mockLLM, createTestRegistry(), nil, nil)

	result, err := o.HandleMessage(context.Background(), "conflicting modes", "session-e2s-conflict", HandleOptions{E2S: true, Goal: true})
	if !errors.Is(err, ErrE2SGoalConflict) {
		t.Fatalf("HandleMessage error = %v, want ErrE2SGoalConflict", err)
	}
	if result != nil {
		t.Errorf("HandleMessage result = %+v, want nil on the conflict", result)
	}
	mockLLM.mu.Lock()
	calls := len(mockLLM.calls)
	mockLLM.mu.Unlock()
	if calls != 0 {
		t.Errorf("LLM calls = %d, want 0 — the conflict must fail before any mode runs", calls)
	}
}

// TestHandleMessage_E2S_DisabledFailsClosed verifies the core-side master
// gate: with the effective E2S toggle off, a HandleOptions.E2S request is
// rejected before the loop starts (defense in depth behind the API gate).
func TestHandleMessage_E2S_DisabledFailsClosed(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return e2sFinishResponse("x", "should never run"), nil
		},
	}
	o := newE2STestOrchestrator(mockLLM, createTestRegistry(), nil, nil)
	o.config.E2S.Enabled = false // effective master toggle off

	result, err := o.HandleMessage(context.Background(), "run e2s", "session-e2s-disabled", HandleOptions{E2S: true})
	if !errors.Is(err, ErrE2SModeDisabled) {
		t.Fatalf("HandleMessage error = %v, want ErrE2SModeDisabled", err)
	}
	if result != nil {
		t.Errorf("HandleMessage result = %+v, want nil when the mode is disabled", result)
	}
	mockLLM.mu.Lock()
	calls := len(mockLLM.calls)
	mockLLM.mu.Unlock()
	if calls != 0 {
		t.Errorf("LLM calls = %d, want 0 — a disabled mode must not start the loop", calls)
	}
}

// TestOrchestrator_SetE2SSettings_OverridesBuildTimeGate pins the runtime
// override contract used by the experimental-features toggle: a setter call
// after Build supersedes config.E2S for both the gate and the loop knobs, so
// an already-built orchestrator does not stay disabled (or keep stale
// thresholds) until an app restart.
func TestOrchestrator_SetE2SSettings_OverridesBuildTimeGate(t *testing.T) {
	o := &Orchestrator{}
	o.config.E2S = E2SSettings{Enabled: false, MaxSteps: 7}

	if got := o.e2sSettings(); got.Enabled || got.MaxSteps != 7 {
		t.Fatalf("e2sSettings() before override = %+v, want the build-time snapshot", got)
	}

	o.SetE2SSettings(E2SSettings{Enabled: true, MaxSteps: 11})
	if got := o.e2sSettings(); !got.Enabled || got.MaxSteps != 11 {
		t.Fatalf("e2sSettings() after override = %+v, want the override applied", got)
	}

	// Disabling via the override must also win over a build-time-enabled gate.
	o.SetE2SSettings(E2SSettings{Enabled: false})
	if got := o.e2sSettings(); got.Enabled {
		t.Fatalf("e2sSettings() after disabling override = %+v, want disabled", got)
	}
}

// TestHandleMessage_E2S_RuntimeDisableFailsClosed verifies that HandleMessage's
// gate reads the runtime override, not the stale build-time snapshot: a
// build-time-enabled orchestrator that a later SetE2SSettings disables must
// reject an E2S request before the loop starts.
func TestHandleMessage_E2S_RuntimeDisableFailsClosed(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return e2sFinishResponse("x", "should never run"), nil
		},
	}
	o := newE2STestOrchestrator(mockLLM, createTestRegistry(), nil, nil)
	o.config.E2S.Enabled = true
	o.SetE2SSettings(E2SSettings{Enabled: false})

	result, err := o.HandleMessage(context.Background(), "run e2s", "session-e2s-runtime-disabled", HandleOptions{E2S: true})
	if !errors.Is(err, ErrE2SModeDisabled) {
		t.Fatalf("HandleMessage error = %v, want ErrE2SModeDisabled", err)
	}
	if result != nil {
		t.Errorf("HandleMessage result = %+v, want nil when the mode is disabled", result)
	}
}

// TestE2SDomainStatus_CanceledLeavesStateResumable pins the shutdown-resume
// contract: a context cancellation persists a NON-terminal (active) status, so
// an app shutdown does not permanently drop an interrupted run's Σ (the
// manager terminalizes only a user cancel via abandonE2SIfUnfinished).
func TestE2SDomainStatus_CanceledLeavesStateResumable(t *testing.T) {
	got := e2sDomainStatus(&e2s.Result{Status: e2s.RunStatusCanceled})
	if got != e2s.StateStatusActive {
		t.Fatalf("canceled run persisted status = %q, want %q (non-terminal so a shutdown-interrupted run resumes with its Σ)", got, e2s.StateStatusActive)
	}
	if !e2sStatusResumable(got) {
		t.Errorf("status %q must be resumable", got)
	}
	// The other terminals keep their mappings (step_limit is covered by
	// TestE2SStepLimitStaysResumable — it maps to the non-terminal active).
	for _, tc := range []struct {
		in   e2s.RunStatus
		want e2s.StateStatus
	}{
		{e2s.RunStatusFinished, e2s.StateStatusMet},
		{e2s.RunStatusPaused, e2s.StateStatusPaused},
		{e2s.RunStatusSpinStop, e2s.StateStatusFailed},
		{e2s.RunStatusFailed, e2s.StateStatusFailed},
	} {
		if got := e2sDomainStatus(&e2s.Result{Status: tc.in}); got != tc.want {
			t.Errorf("e2sDomainStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := e2sDomainStatus(nil); got != e2s.StateStatusFailed {
		t.Errorf("e2sDomainStatus(nil) = %q, want %q", got, e2s.StateStatusFailed)
	}
}

// TestE2SStepLimitStaysResumable pins the budget-exhaustion resume contract:
// a step-limit run persists a NON-terminal (active) domain status and an
// execution status of partial — the task stays resumable, and Resume re-enters
// the loop with the accumulated Σ plus a fresh turn budget instead of
// silently seeding a blank state.
func TestE2SStepLimitStaysResumable(t *testing.T) {
	domain := e2sDomainStatus(&e2s.Result{Status: e2s.RunStatusStepLimit})
	if domain != e2s.StateStatusActive {
		t.Fatalf("step-limit run persisted status = %q, want %q (non-terminal, resumable)", domain, e2s.StateStatusActive)
	}
	if !e2sStatusResumable(domain) {
		t.Errorf("domain status %q must be resumable", domain)
	}
	if got := e2sExecutionStatus(&e2s.Result{Status: e2s.RunStatusStepLimit}); got != orchestration.ExecutionStatusPartial {
		t.Errorf("step-limit execution status = %q, want %q", got, orchestration.ExecutionStatusPartial)
	}
}

// ----------------------------------------------------------------------------
// Acceptance: a delegate action reaches the injected launcher and its output
// becomes the next observation
// ----------------------------------------------------------------------------

func TestRunE2SLoop_DelegateReachesInjectedLauncher(t *testing.T) {
	const delegationOutput = "SUBAGENT-OUTPUT-7f3a"
	launcher := &mockDelegationLauncher{
		out: []tools.DelegationResult{{
			ID:     "del_1",
			Status: tools.DelegationStatusCompleted,
			Output: delegationOutput,
		}},
	}

	var secondUserMessage string
	var mockLLM *mockLLMCaller
	mockLLM = &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mockLLM.mu.Lock()
			n := len(mockLLM.calls)
			mockLLM.mu.Unlock()
			if n == 1 {
				// Turn 1: delegate a self-contained exploration.
				return e2sStepResponse("e2s-d1", `{"findings": []}`, "delegate",
					`{"tasks":[{"id":"del_1","summary":"explore auth module","task":"Find where auth middleware lives and report the file path."}]}`), nil
			}
			// Turn 2: the delegation output must be the observation.
			if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" {
				secondUserMessage = req.Messages[len(req.Messages)-1].Content
			}
			return e2sFinishResponse("e2s-d2", "delegation done"), nil
		},
	}

	o := newE2STestOrchestrator(mockLLM, createTestRegistryWithDelegate(t), &spyEmitter{}, nil)
	o.e2sLauncher = launcher

	result, err := o.HandleMessage(context.Background(), "explore via delegation", "session-e2s-delegate", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	t.Run("launcher received the delegate action", func(t *testing.T) {
		launcher.mu.Lock()
		defer launcher.mu.Unlock()
		if launcher.launch != 1 {
			t.Fatalf("Launch calls = %d, want exactly 1", launcher.launch)
		}
		if len(launcher.tasks) != 1 || launcher.tasks[0].ID != "del_1" {
			t.Fatalf("launched tasks = %+v, want the single del_1 task", launcher.tasks)
		}
	})

	t.Run("launcher output became the next observation", func(t *testing.T) {
		if secondUserMessage == "" {
			t.Fatal("the second LLM call never ran — the loop did not continue past the delegation turn")
		}
		if !strings.Contains(secondUserMessage, delegationOutput) {
			t.Errorf("turn-2 user message does not carry the delegation output %q:\n%s", delegationOutput, secondUserMessage)
		}
	})

	t.Run("run finished successfully", func(t *testing.T) {
		if result == nil || result.Status != orchestration.ExecutionStatusSuccess {
			t.Fatalf("HandleResult.Status = %+v, want success", result)
		}
	})
}

// ----------------------------------------------------------------------------
// Acceptance: pause mid-run → ExecutionStatusPaused + Σ persisted; Resume
// continues from the same Σ
// ----------------------------------------------------------------------------

const e2sSigmaMarker = "ALPHA-FINDING-9c2e"

func TestRunE2SLoop_PausePersistsSigmaAndResumeContinues(t *testing.T) {
	store := newE2SCheckpointStore()

	var resumedUserMessage string
	var o *Orchestrator
	var mockLLM *mockLLMCaller
	mockLLM = &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mockLLM.mu.Lock()
			n := len(mockLLM.calls)
			mockLLM.mu.Unlock()
			switch n {
			case 1:
				// Turn 1 of the initial run: patch Σ with a distinctive
				// finding and dispatch a tool; arm the pause signal so the
				// next step boundary checkpoints the run cooperatively.
				o.PauseSession()
				return e2sStepResponse("e2s-p1",
					fmt.Sprintf(`{"findings": [%q]}`, e2sSigmaMarker),
					"bash_exec", `{"command":"echo hi"}`), nil
			default:
				// Resumed run: the first user message must carry the SAME Σ
				// (the persisted finding), proving resume seeded it verbatim.
				if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" {
					resumedUserMessage = req.Messages[len(req.Messages)-1].Content
				}
				return e2sFinishResponse("e2s-p2", "resumed and finished"), nil
			}
		},
	}

	emitter := &spyEmitter{}
	o = newE2STestOrchestrator(mockLLM, createTestRegistry(), emitter, store)

	paused, err := o.HandleMessage(context.Background(), "work until paused", "session-e2s-pause", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var taskID string
	if pbb, ok := paused.Blackboard.(PersistableBlackboard); ok {
		taskID = pbb.TaskID()
	}
	if taskID == "" {
		t.Fatal("the paused blackboard carries no task id — persistence cannot be asserted (BBFactory wiring broken)")
	}

	t.Run("paused at the boundary with ExecutionStatusPaused", func(t *testing.T) {
		if paused.Status != orchestration.ExecutionStatusPaused {
			t.Fatalf("HandleResult.Status = %q, want paused", paused.Status)
		}
		if pbb, ok := paused.Blackboard.(*testPersistableBlackboard); ok && !pbb.paused {
			t.Error("blackboard was not persisted as paused (PauseTask not called)")
		}
	})

	t.Run("Σ persisted with the turn-1 finding", func(t *testing.T) {
		es := store.load(taskID)
		if es == nil {
			t.Fatal("no E2S state was persisted for the paused task")
		}
		if es.Status != e2s.StateStatusPaused {
			t.Errorf("persisted status = %q, want paused (resumable)", es.Status)
		}
		if es.TurnCount != 1 {
			t.Errorf("persisted TurnCount = %d, want 1 (one patch applied before the pause)", es.TurnCount)
		}
		findings, _ := es.Sigma["findings"].([]any)
		if len(findings) != 1 || findings[0] != e2sSigmaMarker {
			t.Errorf("persisted Σ findings = %#v, want [%q] — the turn-1 patch must be in the checkpoint", es.Sigma["findings"], e2sSigmaMarker)
		}
	})

	// --- Resume: re-enter with the SAME Σ and a user follow-up ---
	const resumeNudge = "also verify the resume nudge reaches the model"
	resumed, err := o.Resume(context.Background(), paused.Blackboard, nil, "", nil, nil, resumeNudge)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	t.Run("resume continues from the same Σ", func(t *testing.T) {
		if resumedUserMessage == "" {
			t.Fatal("the resumed run never made an LLM call")
		}
		if !strings.Contains(resumedUserMessage, e2sSigmaMarker) {
			t.Errorf("resumed turn-1 user message does not carry the persisted Σ finding %q:\n%s", e2sSigmaMarker, resumedUserMessage)
		}
	})

	t.Run("resume delivers the user nudge as the turn-1 observation", func(t *testing.T) {
		// Σ precedence would otherwise drop the follow-up: it must reach the
		// model as the first observation instead of being silently ignored.
		if !strings.Contains(resumedUserMessage, resumeNudge) {
			t.Errorf("resumed turn-1 user message does not carry the nudge %q — the follow-up was dropped:\n%s", resumeNudge, resumedUserMessage)
		}
	})

	t.Run("resumed run finishes successfully", func(t *testing.T) {
		if resumed.Status != orchestration.ExecutionStatusSuccess {
			t.Fatalf("resumed HandleResult.Status = %q, want success", resumed.Status)
		}
		if resumed.Output != "resumed and finished" {
			t.Errorf("resumed output = %q, want the finish answer", resumed.Output)
		}
	})
}

// TestRunE2SLoop_ResumeWithImagesKeepsSigmaReachable pins the image-resume
// regression: an image-bearing E2S task resumed must restore its images via
// IMAGE-ONLY content blocks. The loop sends Σ + the observation as the user
// message's Content and relies on the provider prepending Content as a text
// block when the blocks carry no text; a text block in the list would make
// llm.NormalizeContentBlocks return the blocks unchanged, so the provider
// would render the blocks INSTEAD of Content and silently drop the model's
// entire memory on every resumed turn.
func TestRunE2SLoop_ResumeWithImagesKeepsSigmaReachable(t *testing.T) {
	store := newE2SCheckpointStore()

	const request = "work on the screenshot until paused"
	var resumedMsg llm.Message
	var o *Orchestrator
	var mockLLM *mockLLMCaller
	mockLLM = &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mockLLM.mu.Lock()
			n := len(mockLLM.calls)
			mockLLM.mu.Unlock()
			switch n {
			case 1:
				o.PauseSession()
				return e2sStepResponse("e2s-img1",
					fmt.Sprintf(`{"findings": [%q]}`, e2sSigmaMarker),
					"bash_exec", `{"command":"echo hi"}`), nil
			default:
				if len(req.Messages) > 0 {
					resumedMsg = req.Messages[len(req.Messages)-1]
				}
				return e2sFinishResponse("e2s-img2", "done"), nil
			}
		},
	}

	o = newE2STestOrchestrator(mockLLM, createTestRegistry(), &spyEmitter{}, store)
	// The restored history carries the ORIGINAL request with image content
	// blocks (what convertChatMessagesToLLM rebuilds for a vision task).
	o.SetConversationHistory([]llm.Message{{
		Role:    "user",
		Content: request,
		ContentBlocks: []llm.ContentBlock{
			{Type: "text", Text: request},
			{Type: "image", ImageB64: "aGVsbG8=", MediaType: "image/png"},
		},
	}})

	paused, err := o.HandleMessage(context.Background(), request, "session-e2s-img-resume", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if paused.Status != orchestration.ExecutionStatusPaused {
		t.Fatalf("HandleResult.Status = %q, want paused", paused.Status)
	}

	if _, err := o.Resume(context.Background(), paused.Blackboard, nil, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if resumedMsg.Role != "user" {
		t.Fatalf("no resumed user message captured (last role = %q)", resumedMsg.Role)
	}
	// The image must have been restored...
	hasImage := false
	for _, blk := range resumedMsg.ContentBlocks {
		if blk.Type == "image" {
			hasImage = true
		}
		if blk.Type == "text" {
			t.Errorf("resumed user message carries a text content block %+v — the provider renders blocks instead of Content, dropping Σ + the observation", blk)
		}
	}
	if !hasImage {
		t.Fatalf("resumed user message lost the image content blocks: %+v", resumedMsg.ContentBlocks)
	}
	// ...and normalizing must still surface Σ as the effective text block, so
	// the model's memory reaches the provider.
	var sb strings.Builder
	for _, blk := range llm.NormalizeContentBlocks(resumedMsg) {
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	if effectiveText := sb.String(); !strings.Contains(effectiveText, e2sSigmaMarker) {
		t.Errorf("normalized resumed user message does not carry the persisted Σ finding %q — the model's memory was dropped:\n%q", e2sSigmaMarker, effectiveText)
	}
}

// TestE2SResumeNote_InformsBudgetRefresh pins the plain-resume note: without
// a user nudge the model is still told the budget was refreshed and Σ is the
// continuation point (a resumed run's [turn N of M] counts the NEW budget).
func TestE2SResumeNote_InformsBudgetRefresh(t *testing.T) {
	note := e2sResumeNote("")
	if !strings.Contains(note, "fresh turn budget") || !strings.Contains(note, "continuation point") {
		t.Errorf("plain-resume note must explain the budget refresh and Σ precedence: %q", note)
	}
	nudged := e2sResumeNote("please also check tests")
	if !strings.Contains(nudged, "please also check tests") || !strings.Contains(nudged, "budget was also refreshed") {
		t.Errorf("nudged resume note must carry the follow-up and the budget note: %q", nudged)
	}
}

// ----------------------------------------------------------------------------
// Review-fix regressions: UserAgents directive, spin_stop output
// ----------------------------------------------------------------------------

// TestRunE2SLoop_UserAgentsRenderRequestedSection pins #agent-mention
// parity: an explicit UserAgents request must reach the E2S system prompt as
// the same mandatory "## Requested Subagents" directive the Conductor
// renders, instead of being silently dropped by the E2S early return.
func TestRunE2SLoop_UserAgentsRenderRequestedSection(t *testing.T) {
	var systemPrompt string
	var mockLLM *mockLLMCaller
	mockLLM = &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			mockLLM.mu.Lock()
			n := len(mockLLM.calls)
			mockLLM.mu.Unlock()
			if n == 1 && len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				systemPrompt = req.Messages[0].Content
			}
			return e2sFinishResponse("e2s-f1", "done"), nil
		},
	}
	o := newE2STestOrchestrator(mockLLM, createTestRegistryWithDelegate(t), &spyEmitter{}, nil)
	if _, err := o.HandleMessage(context.Background(), "review the auth module", "session-e2s-agents",
		HandleOptions{E2S: true, UserAgents: []string{"code-reviewer"}}); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if !strings.Contains(systemPrompt, "## Requested Subagents") {
		t.Errorf("E2S system prompt missing the Requested Subagents section:\n%s", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "code-reviewer") {
		t.Errorf("E2S system prompt missing the requested agent name:\n%s", systemPrompt)
	}
}

// TestRunE2SLoop_SpinStopOutputNotUserEcho pins the empty-answer termination
// contract: a run aborted by the anti-spin detector must surface an explicit
// honest outcome, never the user's own message echoed as the final answer.
func TestRunE2SLoop_SpinStopOutputNotUserEcho(t *testing.T) {
	spinArgs := `{"path":"same.txt"}`
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			// The same anchored action forever: nudge at 3, abort at 5.
			return e2sStepResponse("e2s-s1", `{}`, "read_file", spinArgs), nil
		},
	}
	o := newE2STestOrchestrator(mockLLM, createTestRegistryWithDelegate(t), &spyEmitter{}, nil)
	o.logger = expectedCoreLogger(t, expectedCoreDiagnostic{"WARN", "e2s: anti-spin abort", map[string]string{"tool": "read_file", "repeat_count": "5"}})
	result, err := o.HandleMessage(context.Background(), "read same.txt forever", "session-e2s-spin",
		HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if result == nil {
		t.Fatal("nil HandleResult")
	}
	if result.Output == "read same.txt forever" {
		t.Fatal("spin_stop output echoes the user message — the frontend would render the user's own words as the assistant answer")
	}
	if !strings.Contains(result.Output, "anti-spin") {
		t.Errorf("spin_stop output should name the anti-spin abort:\n%s", result.Output)
	}
}

// TestRunE2SLoop_ModelProfilesNudgeOverrideCannotBypassOrdering pins the guard on
// the profile path: a Model Profiles loop-hardening repeat-nudge override at or
// above the configured E2S abort threshold is ignored, falling back to the
// validated nudge. Without the guard the override (9 >= abort 5) would make
// Config.withDefaults silently raise the effective abort to nudge+1 (10),
// diverging from the configured repeat_abort_threshold — the same silent
// divergence the e2s config validation rejects up front.
func TestRunE2SLoop_ModelProfilesNudgeOverrideCannotBypassOrdering(t *testing.T) {
	spinArgs := `{"path":"same.txt"}`
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return e2sStepResponse("e2s-sl1", `{}`, "read_file", spinArgs), nil
		},
	}
	emitter := &spyEmitter{}
	o := newE2STestOrchestrator(mockLLM, createTestRegistryWithDelegate(t), emitter, nil)
	o.config.E2S.RepeatNudgeThreshold = 3
	o.config.E2S.RepeatAbortThreshold = 5
	// A misconfigured profile override: request a nudge at 9, at/above the
	// abort threshold — it must be ignored rather than diverge.
	o.config.ModelProfiles = ModelProfilesSettings{
		Enabled:       true,
		LoopHardening: ModelProfilesLoopHardeningSettings{Enabled: true, RepeatNudgeThreshold: 9},
	}

	o.logger = expectedCoreLogger(t, expectedCoreDiagnostic{"WARN", "e2s_loop: ignoring Model Profiles repeat-nudge override — not strictly below the configured abort threshold", map[string]string{"override": "9", "abort_threshold": "5", "using_nudge": "3"}}, expectedCoreDiagnostic{"WARN", "e2s: anti-spin abort", map[string]string{"tool": "read_file", "repeat_count": "5"}})
	result, err := o.HandleMessage(context.Background(), "read same.txt forever", "session-e2s-sl", HandleOptions{E2S: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if result == nil {
		t.Fatal("nil HandleResult")
	}

	nudged, aborted := false, false
	firstNudge := 0
	for _, c := range emitter.calls {
		if c.method != "ExecutorDiagnostic" || len(c.args) < 3 {
			continue
		}
		kind, _ := c.args[1].(string)
		switch kind {
		case "spin_nudge":
			if !nudged {
				nudged = true
				if m, ok := c.args[2].(map[string]any); ok {
					firstNudge, _ = m["repeat_count"].(int)
				}
			}
		case "spin_stop":
			aborted = true
		}
	}
	if !nudged {
		t.Error("no spin_nudge was emitted — the Model Profiles override (9 >= abort 5) fired the abort first, bypassing the validated ordering")
	} else if firstNudge != 3 {
		t.Errorf("first spin_nudge at repeat_count %d, want 3 (the validated nudge, not the ignored 9 override)", firstNudge)
	}
	if !aborted {
		t.Error("run did not abort with spin_stop")
	}
}

// TestHandleMessage_E2S_MCPModeGating drives the E2S branch end-to-end over
// the per-server MCP mode gate: without a mention the manual server's tool is
// absent from the prompt catalog (and no directive renders); with a mention it
// joins the catalog and the soft "Requested MCP Servers" section renders. The
// disabled server never surfaces in either case.
func TestHandleMessage_E2S_MCPModeGating(t *testing.T) {
	build := func() (*sdktools.ToolRegistry, *Orchestrator, *mockLLMCaller) {
		reg := sdktools.NewToolRegistry()
		_ = reg.RegisterWithSourceCategory(&mockSubagentTool{name: "auto_query", group: sdktools.GroupRemoteMCP}, "auto-srv", sdktools.SourceCategoryMCP)
		_ = reg.RegisterWithSourceCategory(&mockSubagentTool{name: "manual_query", group: sdktools.GroupRemoteMCP}, "manual-srv", sdktools.SourceCategoryMCP)
		_ = reg.RegisterWithSourceCategory(&mockSubagentTool{name: "stale_query", group: sdktools.GroupRemoteMCP}, "disabled-srv", sdktools.SourceCategoryMCP)
		mockLLM := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return e2sFinishResponse("mcp-e2s", "done"), nil
		}}
		deps := OrchestratorDeps{
			Router:         newCoreRouter(mockLLM, 5),
			LLM:            mockLLM,
			ToolExec:       reg,
			ToolRegistry:   reg,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: testContextFactory,
			CircuitBreaker: defaultCircuitBreakerConfig,
		}
		o := NewOrchestrator(OrchestratorConfig{
			E2S: E2SSettings{Enabled: true},
			MCPServerModes: map[string]string{
				"auto-srv":     "auto",
				"manual-srv":   "manual",
				"disabled-srv": "disabled",
			},
		}, deps)
		return reg, o, mockLLM
	}

	firstPrompt := func(m *mockLLMCaller) string {
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.calls) == 0 {
			t.Fatal("E2S loop made no LLM call")
		}
		return fmt.Sprintf("%v", m.calls[0])
	}

	_, o, mockLLM := build()
	if _, err := o.HandleMessage(context.Background(), "query things", "sess-mcp-1", HandleOptions{E2S: true}); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	prompt := firstPrompt(mockLLM)
	if !strings.Contains(prompt, "auto_query") {
		t.Fatal("auto server's tool must be in the E2S catalog")
	}
	if strings.Contains(prompt, "manual_query") || strings.Contains(prompt, "stale_query") {
		t.Fatal("unmentioned manual / disabled server tools must be gated out of the E2S catalog")
	}
	if strings.Contains(prompt, "Requested MCP Servers") {
		t.Fatal("no mention must render no Requested MCP Servers section")
	}

	_, o, mockLLM = build()
	if _, err := o.HandleMessage(context.Background(), "query things", "sess-mcp-2", HandleOptions{E2S: true, UserMCPServers: []string{"manual-srv"}}); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	prompt = firstPrompt(mockLLM)
	if !strings.Contains(prompt, "manual_query") {
		t.Fatal("a mentioned manual server's tool must join the E2S catalog")
	}
	if !strings.Contains(prompt, "## Requested MCP Servers") || !strings.Contains(prompt, "manual-srv") {
		t.Fatal("a mention must render the Requested MCP Servers section naming the server")
	}
	if strings.Contains(prompt, "stale_query") {
		t.Fatal("disabled server tools must never surface, mention or not")
	}
}
