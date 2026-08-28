package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// waitForCompactionEvent polls the captured events for an event of the given
// type, failing after a timeout.
func waitForCompactionEvent(t *testing.T, events chan Event, eventType string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == eventType {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s event", eventType)
			return Event{}
		case <-time.After(10 * time.Millisecond):
			// keep polling
		}
	}
}

// recordingCompactionStore wraps the standard mock store, recording SaveMessage
// calls so the compaction marker can be asserted.
type recordingCompactionStore struct {
	mockSessionStoreForRestore
	mu       sync.Mutex
	messages []ChatMessage
}

func (s *recordingCompactionStore) SaveMessage(_ context.Context, msg ChatMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *recordingCompactionStore) savedMessages() []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatMessage{}, s.messages...)
}

// newCompactionTestManager creates a manager + session with a compaction-ready
// orchestrator wired in (mirroring liveTestSession's manual patching, since
// the shared test factory returns a nil orchestrator). The conversation
// history holds 30 question/answer pairs (60 messages) — well past the
// sliding-window limits, so a compaction actually compacts.
func newCompactionTestManager(t *testing.T) (*Manager, *Session, *core.Orchestrator, chan Event, *recordingCompactionStore) {
	return newCompactionTestManagerWithHistory(t, 30, nil)
}

// newCompactionTestManagerWithHistory is newCompactionTestManager with a
// controllable history size (pairs of question/answer messages) and an
// optional deps augmentation applied before the orchestrator is built — used
// to inject a spy emitter, a mock context factory and a finish-only LLM for
// resume-deferral tests. A small history (<= KeepFirst+KeepLast messages)
// makes CompactConversationHistory a no-op (ErrNothingCompacted).
func newCompactionTestManagerWithHistory(t *testing.T, pairs int, augment func(*core.OrchestratorDeps)) (*Manager, *Session, *core.Orchestrator, chan Event, *recordingCompactionStore) {
	t.Helper()
	manager, events, _ := testManager(t)
	store := &recordingCompactionStore{mockSessionStoreForRestore: *newMockSessionStore()}
	manager.SetSessionStore(store)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, _ := manager.GetSession(info.ID)
	if sess == nil {
		t.Fatal("session not in manager")
	}

	cfg := core.OrchestratorConfig{Model: "test-model"}
	cfg.Compaction.SlidingWindow.KeepFirst = 2
	cfg.Compaction.SlidingWindow.KeepLast = 4
	deps := core.OrchestratorDeps{
		TokenCounter: llm.NewSimpleTokenCounter(),
		// OutputLimit must be explicit: a partial override inherits the
		// unknown-model fallback (32768), which would swallow the whole
		// 1000-token window in the compaction effective-base math.
		ModelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{"test-model": {ContextWindow: 1000, OutputLimit: 100}}),
	}
	if augment != nil {
		augment(&deps)
	}
	orch := core.NewOrchestrator(cfg, deps)
	history := make([]llm.Message, 0, 2*pairs)
	for i := 0; i < pairs; i++ {
		history = append(history,
			llm.Message{Role: "user", Content: "question " + string(rune('a'+i%26))},
			llm.Message{Role: "assistant", Content: "answer " + string(rune('a'+i%26))},
		)
	}
	orch.SetConversationHistory(history)

	sess.mu.Lock()
	sess.orchestrator = orch
	sess.mu.Unlock()
	return manager, sess, orch, events, store
}

func TestCompactSessionContext_IdleSession(t *testing.T) {
	manager, sess, orch, events, store := newCompactionTestManager(t)

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}

	started := waitForCompactionEvent(t, events, "compaction_started")
	startedData, ok := started.Data.(CompactionStartedEventData)
	if !ok {
		t.Fatalf("unexpected started payload type: %T", started.Data)
	}
	if startedData.Strategy != "sliding_window" {
		t.Errorf("unexpected started payload: %+v", startedData)
	}
	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok {
		t.Fatalf("unexpected finished payload type: %T", finished.Data)
	}
	if !fin.Success || fin.Cancelled || fin.Error != "" {
		t.Errorf("expected success, got %+v", fin)
	}
	if fin.NothingCompacted {
		t.Error("a 60-message history must actually compact — nothing_compacted must be false")
	}
	if fin.DeferredToResume {
		t.Error("no paused task existed — nothing to defer to a resume")
	}
	if fin.Resumed {
		t.Error("no paused task existed — nothing to resume")
	}
	if fin.BeforePercent <= fin.AfterPercent {
		t.Errorf("expected fill reduction in payload: %+v", fin)
	}

	// History compacted in place.
	if got := len(orch.ConversationHistory()); got != 7 {
		t.Errorf("expected 7 compacted messages, got %d", got)
	}
	// Marker persisted with the snapshot.
	msgs := store.savedMessages()
	if len(msgs) != 1 || msgs[0].Role != "context_compaction" {
		t.Fatalf("expected one context_compaction marker, got %+v", msgs)
	}
	var meta struct {
		Messages []llm.Message `json:"messages"`
		Strategy string        `json:"strategy"`
	}
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("marker metadata unparsable: %v", err)
	}
	if meta.Strategy != "sliding_window" || len(meta.Messages) != 7 {
		t.Errorf("unexpected marker metadata: strategy=%s messages=%d", meta.Strategy, len(meta.Messages))
	}
	// Session released.
	if sess.IsCompacting() {
		t.Error("compacting flag must be cleared after the flow finishes")
	}
}

// TestCompactSessionContext_NoOpIdleSession verifies the nothing-to-compact
// outcome on an idle session: a history already within the sliding-window
// limits makes CompactConversationHistory return ErrNothingCompacted, which
// the flow treats as a SUCCESS no-op — nothing_compacted=true, zero
// percentages, no marker row (there is no compacted history to snapshot), and
// no deferral (no task store is configured, so no paused task exists).
func TestCompactSessionContext_NoOpIdleSession(t *testing.T) {
	manager, sess, orch, events, store := newCompactionTestManagerWithHistory(t, 2, nil) // 4 messages — within KeepFirst(2)+KeepLast(4)

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}

	waitForCompactionEvent(t, events, "compaction_started")
	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok {
		t.Fatalf("unexpected finished payload type: %T", finished.Data)
	}
	if !fin.Success || fin.Cancelled || fin.Error != "" {
		t.Errorf("no-op must be a success, got %+v", fin)
	}
	if !fin.NothingCompacted {
		t.Error("expected nothing_compacted=true for a history within the limits")
	}
	if fin.DeferredToResume {
		t.Error("no paused task existed — nothing to defer to a resume")
	}
	if fin.Resumed {
		t.Error("no paused task existed — nothing to resume")
	}
	if fin.BeforePercent != 0 || fin.AfterPercent != 0 {
		t.Errorf("no-op must report zero percentages, got %.1f/%.1f", fin.BeforePercent, fin.AfterPercent)
	}

	// No marker row: nothing changed, so nothing to snapshot or reseed from.
	if msgs := store.savedMessages(); len(msgs) != 0 {
		t.Errorf("no-op must not persist a marker row, got %+v", msgs)
	}
	// History untouched.
	if got := len(orch.ConversationHistory()); got != 4 {
		t.Errorf("history must be untouched by the no-op, got %d messages", got)
	}
	// Session released.
	if sess.IsCompacting() {
		t.Error("compacting flag must be cleared after the flow finishes")
	}
}

func TestCompactSessionContext_UnknownStrategy(t *testing.T) {
	manager, sess, _, _, _ := newCompactionTestManager(t)
	if err := manager.CompactSessionContext(context.Background(), sess.ID, "bogus"); err == nil {
		t.Fatal("expected error for unknown strategy")
	}
	if sess.IsCompacting() {
		t.Error("compacting flag must not be set for a rejected strategy")
	}
}

func TestCompactSessionContext_AlreadyCompacting(t *testing.T) {
	manager, sess, _, _, _ := newCompactionTestManager(t)
	sess.mu.Lock()
	sess.compacting = true
	sess.mu.Unlock()
	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); !errors.Is(err, ErrCompactionInFlight) {
		t.Fatalf("expected ErrCompactionInFlight, got %v", err)
	}
}

func TestCompactSessionContext_ActiveSessionWaitsForPause(t *testing.T) {
	manager, sess, orch, events, _ := newCompactionTestManager(t)

	// Simulate a running request: active + done channel, closed by a fake
	// epilogue when the test triggers the "pause landing".
	sess.mu.Lock()
	doneCh := make(chan struct{})
	sess.active = true
	sess.done = doneCh
	sess.mu.Unlock()

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_started")

	// While waiting for the checkpoint the session is locked for sends.
	sess.mu.Lock()
	compacting := sess.compacting
	pausing := sess.pausing
	sess.mu.Unlock()
	if !compacting || !pausing {
		t.Fatalf("expected compacting+pausing while waiting for the pause, got %v/%v", compacting, pausing)
	}

	// The pause lands: mirror the request epilogue (deactivateSessionTask)
	// then close the done channel.
	sess.mu.Lock()
	sess.active = false
	sess.cancel = nil
	sess.done = nil
	sess.pausing = false
	sess.mu.Unlock()
	close(doneCh)

	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok || !fin.Success {
		t.Fatalf("expected successful compaction after pause, got %+v", finished.Data)
	}
	if len(orch.ConversationHistory()) != 7 {
		t.Errorf("history must be compacted after the pause lands, got %d messages", len(orch.ConversationHistory()))
	}
}

func TestCompactSessionContext_CancelDuringPauseWait(t *testing.T) {
	manager, sess, orch, events, _ := newCompactionTestManager(t)

	sess.mu.Lock()
	doneCh := make(chan struct{})
	sess.active = true
	sess.done = doneCh
	sess.mu.Unlock()

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_started")

	// Cancel while the flow is still waiting for the checkpoint. The cancel and
	// the checkpoint landing below race on purpose: the flow must report a
	// cancelled outcome deterministically whichever channel the select picks.
	if err := manager.CancelSessionCompaction(sess.ID); err != nil {
		t.Fatalf("CancelSessionCompaction failed: %v", err)
	}

	// The flow keeps waiting for the checkpoint; only then does it finish as
	// cancelled. Let the pause land.
	sess.mu.Lock()
	sess.active = false
	sess.cancel = nil
	sess.done = nil
	sess.pausing = false
	sess.mu.Unlock()
	close(doneCh)

	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok || !fin.Cancelled || fin.Success {
		t.Fatalf("expected cancelled outcome, got %+v", finished.Data)
	}
	if got := len(orch.ConversationHistory()); got != 60 {
		t.Errorf("history must be untouched after cancellation, got %d messages", got)
	}
	if sess.IsCompacting() {
		t.Error("compacting flag must be cleared after cancellation")
	}
}

// failingResumeTaskStore serves a paused unfinished task (so
// hasPausedUnfinishedTask is true and the flow attempts an auto-resume) but
// fails LoadTrajectory, making ResumeTask error out mid-restore.
type failingResumeTaskStore struct {
	mockTaskStoreForResumable
}

func (s *failingResumeTaskStore) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, errors.New("trajectory load failure")
}

func TestCompactSessionContext_AutoResumeFailureReportsPaused(t *testing.T) {
	manager, sess, orch, events, _ := newCompactionTestManager(t)

	// A paused checkpoint the flow must try to auto-resume; the resume fails
	// on the trajectory load. Set the store after CreateSession like the other
	// task-store tests (the helper wires the orchestrator after creation).
	taskRec := &TaskRecord{ID: "task-paused", SessionID: sess.ID, Status: "paused"}
	manager.SetTaskStore(&failingResumeTaskStore{
		mockTaskStoreForResumable{unfinished: taskRec, loadTaskResult: taskRec},
	})

	// Simulate a running request the flow pauses.
	sess.mu.Lock()
	doneCh := make(chan struct{})
	sess.active = true
	sess.done = doneCh
	sess.mu.Unlock()

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_started")

	// The pause lands (mirror the request epilogue).
	sess.mu.Lock()
	sess.active = false
	sess.cancel = nil
	sess.done = nil
	sess.pausing = false
	sess.mu.Unlock()
	close(doneCh)

	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok || !fin.Success {
		t.Fatalf("expected successful compaction, got %+v", finished.Data)
	}
	if fin.Resumed {
		t.Error("auto-resume failed — resumed must be false")
	}
	if !fin.PausedWithoutResume {
		t.Error("expected paused_without_resume=true when the auto-resume fails")
	}
	if got := len(orch.ConversationHistory()); got != 7 {
		t.Errorf("compaction must still succeed, got %d messages", got)
	}
}

// --- Deferred-to-resume scaffolding ----------------------------------------
//
// The deferral test must observe the one-shot resume-compaction flag from
// OUTSIDE the core package (it deliberately has no public getter), so it
// watches the flag's EFFECT: an orchestrator armed via RequestResumeCompaction
// force-compacts the merged trajectory at the START of the resumed Conductor
// run, before its first LLM call. The scaffolding below wires a spy emitter
// (records ContextCompaction emissions), a stub context factory (records the
// run's compaction strategy) and a finish-only LLM (ends the resumed run
// after a single call) into the session's orchestrator.

// resumeCompactionSpyEmitter is a core.Emitter recording ContextCompaction
// emissions; everything else is a no-op (the agent.Events half via the
// embedded NoopEvents, the c0wrk-specific half explicitly below).
type resumeCompactionSpyEmitter struct {
	*agent.NoopEvents
	mu          sync.Mutex
	compactions int
}

func (s *resumeCompactionSpyEmitter) ContextCompaction(_, _ float64, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactions++
}

func (s *resumeCompactionSpyEmitter) compactionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactions
}

// c0wrk-specific Emitter methods: no-ops for this test.
func (s *resumeCompactionSpyEmitter) Routing(_, _, _ string)                                       {}
func (s *resumeCompactionSpyEmitter) PlanGenerated(_ int, _ []orchestration.PlanStepEvent)         {}
func (s *resumeCompactionSpyEmitter) PlanStepStart(_, _, _ string)                                 {}
func (s *resumeCompactionSpyEmitter) PlanStepComplete(_ string, _ bool, _ time.Duration, _ string) {}
func (s *resumeCompactionSpyEmitter) Reflection(_ *orchestration.Reflection, _, _ int)             {}
func (s *resumeCompactionSpyEmitter) Retry(_, _ int)                                               {}
func (s *resumeCompactionSpyEmitter) StepRetry(_ string, _, _ int)                                 {}
func (s *resumeCompactionSpyEmitter) Service(_ string)                                             {}
func (s *resumeCompactionSpyEmitter) ServiceWithMeta(_ string, _ map[string]any)                   {}
func (s *resumeCompactionSpyEmitter) GoalStatus(_ map[string]any)                                  {}
func (s *resumeCompactionSpyEmitter) GoalProgress(_ map[string]any)                                {}
func (s *resumeCompactionSpyEmitter) ReplanFailed(_ error)                                         {}
func (s *resumeCompactionSpyEmitter) SkillsActivated(_ []string)                                   {}
func (s *resumeCompactionSpyEmitter) ToolsAssigned(_ []string)                                     {}
func (s *resumeCompactionSpyEmitter) StepTodoUpdate(_ string, _ []agent.TodoItem)                  {}
func (s *resumeCompactionSpyEmitter) MemoryRead(_ int, _ string)                                   {}

// stubResumeCM is a minimal core.ContextManager whose Compact always reports
// a real compaction (non-nil result), so the resumed run's forced
// start-of-run pass emits exactly one ContextCompaction event. The run ends
// at the first LLM call (finish-only mock), so the step plumbing is inert.
type stubResumeCM struct {
	systemPrompt string
}

func (m *stubResumeCM) BuildPrompt() []llm.Message {
	if m.systemPrompt == "" {
		return nil
	}
	return []llm.Message{{Role: "system", Content: m.systemPrompt}}
}
func (m *stubResumeCM) AddStep(agent.Step)                   {}
func (m *stubResumeCM) SetTask(string)                       {}
func (m *stubResumeCM) SetStrategy(agent.CompactionStrategy) {}
func (m *stubResumeCM) Compact(context.Context) *agent.CompactionResult {
	return &agent.CompactionResult{BeforePercent: 91, AfterPercent: 33}
}
func (m *stubResumeCM) CheckFill() agent.FillCheck {
	return agent.FillCheck{Percent: 10, Status: "ok", Used: 100, Max: 1000}
}
func (m *stubResumeCM) CorrectTokenCount(int)                       {}
func (m *stubResumeCM) FillPercent() float64                        { return 10 }
func (m *stubResumeCM) AvailableTokens() int                        { return 900 }
func (m *stubResumeCM) OutputLimit() int                            { return 100 }
func (m *stubResumeCM) VulnerableOutputs() []agent.VulnerableOutput { return nil }

// finishResumedLLM answers every call with a finish tool call, so the resumed
// Conductor run ends successfully after a single LLM call — after (never
// before) the forced start-of-run compaction the test observes.
type finishResumedLLM struct{}

func (finishResumedLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "Task completed",
			ToolCalls: []llm.ToolCall{{
				ID:    "c1",
				Name:  "finish",
				Input: json.RawMessage(`{"answer":"resumed after deferred compaction"}`),
			}},
		},
		StopReason: "tool_use",
	}, nil
}

// waitFor polls cond until it holds or the timeout expires, failing the test
// with what on timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCompactSessionContext_NoOpDefersCompactionToResume is the acceptance
// test for the deferred-to-resume path: a no-op manual compaction of a
// session with a PAUSED unfinished task must arm the orchestrator's one-shot
// resume-compaction request BEFORE the auto-resume, so the resumed run
// force-compacts the merged trajectory up front — observed here as the run's
// single ContextCompaction emission carrying the user-selected strategy. The
// flow reports nothing_compacted + deferred_to_resume, persists no marker
// row, and leaves the conversation history untouched.
func TestCompactSessionContext_NoOpDefersCompactionToResume(t *testing.T) {
	spy := &resumeCompactionSpyEmitter{NoopEvents: &agent.NoopEvents{}}
	strategyCh := make(chan string, 4)
	manager, sess, orch, events, store := newCompactionTestManagerWithHistory(t, 2, func(deps *core.OrchestratorDeps) {
		deps.Emitter = spy
		deps.LLM = finishResumedLLM{}
		deps.ToolRegistry = sdktools.NewToolRegistry() // Resume lists tools off the registry; nil would panic
		deps.ContextFactory = func(_ string, _ llm.ModelMetadata, strategy string, _ ...orchestration.PruningOverride) core.ContextManager {
			select {
			case strategyCh <- strategy:
			default:
			}
			return &stubResumeCM{}
		}
	})

	// A paused unfinished task: the flow must defer the no-op compaction to
	// its resume and auto-resume the checkpoint.
	taskRec := &TaskRecord{ID: "task-paused", SessionID: sess.ID, Status: "paused", OriginalRequest: "long running task"}
	manager.SetTaskStore(&mockTaskStoreForResumable{unfinished: taskRec, loadTaskResult: taskRec})

	// Simulate a running request the flow pauses.
	sess.mu.Lock()
	doneCh := make(chan struct{})
	sess.active = true
	sess.done = doneCh
	sess.mu.Unlock()

	if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
		t.Fatalf("CompactSessionContext failed: %v", err)
	}
	waitForCompactionEvent(t, events, "compaction_started")

	// The pause lands (mirror the request epilogue).
	sess.mu.Lock()
	sess.active = false
	sess.cancel = nil
	sess.done = nil
	sess.pausing = false
	sess.mu.Unlock()
	close(doneCh)

	finished := waitForCompactionEvent(t, events, "compaction_finished")
	fin, ok := finished.Data.(CompactionFinishedEventData)
	if !ok {
		t.Fatalf("unexpected finished payload type: %T", finished.Data)
	}
	if !fin.Success || fin.Cancelled || fin.Error != "" {
		t.Errorf("deferred no-op must be a success, got %+v", fin)
	}
	if !fin.NothingCompacted || !fin.DeferredToResume {
		t.Errorf("expected nothing_compacted+deferred_to_resume, got %+v", fin)
	}
	if !fin.Resumed {
		t.Error("the paused task must be auto-resumed by the flow")
	}
	if sess.IsCompacting() {
		t.Error("compacting flag must be cleared after the flow finishes")
	}

	// No marker row: the executor's context_compaction card from the resumed
	// run is the user-facing record of the deferred compaction instead.
	for _, msg := range store.savedMessages() {
		if msg.Role == compactMarkerRole {
			t.Errorf("deferred no-op must not persist a marker row, got %+v", msg)
		}
	}

	// The armed flag reached the RESUMED run (arming after ResumeTask would
	// have missed it): its Conductor performed exactly one forced compaction
	// of the merged trajectory...
	waitFor(t, 5*time.Second, "the resumed run's forced compaction", func() bool {
		return spy.compactionCount() == 1
	})
	// ...with the user-selected strategy, not a routing-derived one.
	select {
	case s := <-strategyCh:
		if s != "sliding_window" {
			t.Errorf("resumed run used strategy %q, want the user-selected sliding_window", s)
		}
	default:
		t.Error("the resumed run's context factory was never invoked with a strategy")
	}

	// Let the resumed run finish so the history assertion below sees the
	// settled state (recordResumeOutcome appends the assistant exchange
	// before task_complete is emitted).
	waitForCompactionEvent(t, events, "task_complete")

	// The no-op left the original four history messages untouched (only the
	// resumed run's own outcome exchange is appended after them).
	hist := orch.ConversationHistory()
	if len(hist) < 4 {
		t.Fatalf("history must keep the original 4 messages, got %d", len(hist))
	}
	for i := 0; i < 4; i++ {
		want := "question "
		if i%2 == 1 {
			want = "answer "
		}
		if !strings.HasPrefix(hist[i].Content, want) {
			t.Errorf("history[%d] changed: %q (want prefix %q)", i, hist[i].Content, want)
		}
	}
}

// TestDiscardUnfinishedTask_ClearsDeferredResumeCompaction verifies the
// cancel/abandon wiring for the armed one-shot resume compaction: a no-op
// manual compaction on a session with a paused unfinished task (here a
// USER-paused session — the flow pauses nothing, so the deferral carries
// deferred_to_resume=true WITHOUT an auto-resume) arms the flag; discarding
// the unfinished task (user discard / archival via CancelUnfinishedTask, or
// goal-mode takeover via abandonUnfinishedTaskForGoal) must DROP the flag —
// otherwise a later task resuming on the same orchestrator would inherit a
// forced compaction chosen for the discarded one.
func TestDiscardUnfinishedTask_ClearsDeferredResumeCompaction(t *testing.T) {
	runScenario := func(t *testing.T, discard func(m *Manager, sessionID string)) {
		spy := &resumeCompactionSpyEmitter{NoopEvents: &agent.NoopEvents{}}
		manager, sess, _, events, _ := newCompactionTestManagerWithHistory(t, 2, func(deps *core.OrchestratorDeps) {
			deps.Emitter = spy
			deps.LLM = finishResumedLLM{}
			deps.ToolRegistry = sdktools.NewToolRegistry() // Resume lists tools off the registry; nil would panic
			deps.ContextFactory = func(_ string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
				return &stubResumeCM{}
			}
		})

		// A paused unfinished task; the session itself stays idle (the
		// user-paused scenario: the flow pauses nothing and auto-resumes
		// nothing).
		taskRec := &TaskRecord{ID: "task-paused", SessionID: sess.ID, Status: "paused", OriginalRequest: "long running task"}
		manager.SetTaskStore(&mockTaskStoreForResumable{unfinished: taskRec, loadTaskResult: taskRec})

		if err := manager.CompactSessionContext(context.Background(), sess.ID, "sliding_window"); err != nil {
			t.Fatalf("CompactSessionContext failed: %v", err)
		}
		finEvent := waitForCompactionEvent(t, events, "compaction_finished")
		fin, ok := finEvent.Data.(CompactionFinishedEventData)
		if !ok {
			t.Fatalf("unexpected finished payload type: %T", finEvent.Data)
		}
		if !fin.DeferredToResume {
			t.Fatalf("an idle session with a paused unfinished task must defer the no-op compaction, got %+v", fin)
		}
		if fin.Resumed {
			t.Error("the flow paused nothing on an idle session, so nothing may be auto-resumed")
		}

		// Discard/abandon the unfinished task — this must drop the armed flag.
		discard(manager, sess.ID)

		// Resume the task anyway (the mock store still reports it): with the
		// flag cleared the resumed run must NOT force a compaction — the
		// stub context manager reports a 10% fill, so any ContextCompaction
		// emission can only come from a lingering armed flag's CompactOnStart
		// pass.
		if err := manager.ResumeTask(context.Background(), sess.ID, "", "", ""); err != nil {
			t.Fatalf("ResumeTask failed: %v", err)
		}
		waitForCompactionEvent(t, events, "task_complete")
		if got := spy.compactionCount(); got != 0 {
			t.Errorf("resumed run emitted %d forced compactions after the unfinished task was discarded, want 0", got)
		}
	}

	t.Run("cancel unfinished task", func(t *testing.T) {
		runScenario(t, func(m *Manager, sessionID string) {
			if err := m.CancelUnfinishedTask(sessionID); err != nil {
				t.Fatalf("CancelUnfinishedTask failed: %v", err)
			}
		})
	})

	t.Run("goal abandonment", func(t *testing.T) {
		runScenario(t, func(m *Manager, sessionID string) { m.abandonUnfinishedTaskForGoal(sessionID) })
	})
}

func TestSendMessage_RejectedWhileCompacting(t *testing.T) {
	manager, sess, _, _, _ := newCompactionTestManager(t)
	sess.mu.Lock()
	sess.compacting = true
	sess.mu.Unlock()

	err := manager.SendMessage(context.Background(), sess.ID, "hello", nil, nil, "", "", false, "", false)
	if !errors.Is(err, ErrSessionCompacting) {
		t.Fatalf("expected ErrSessionCompacting, got %v", err)
	}
}

func TestValidateLiveSend_RejectedWhileCompacting(t *testing.T) {
	manager, _, _, _, _ := newCompactionTestManager(t)
	sess := &Session{active: false}
	sess.mu.Lock()
	sess.compacting = true
	sess.mu.Unlock()
	manager.mu.Lock()
	manager.sessions["compact-live"] = sess
	manager.mu.Unlock()

	if err := manager.ValidateLiveSend("compact-live", false, "hi", nil, nil); !errors.Is(err, ErrSessionCompacting) {
		t.Fatalf("expected ErrSessionCompacting, got %v", err)
	}
}

func TestConvertChatMessages_CompactionMarkerReseedsHistory(t *testing.T) {
	m := &Manager{}
	markerMeta, err := json.Marshal(map[string]any{
		"strategy":       "sliding_window",
		"before_percent": 90.0,
		"after_percent":  30.0,
		"messages": []llm.Message{
			{Role: "system", Content: "[... 40 earlier messages omitted ...]"},
			{Role: "user", Content: "recent question"},
			{Role: "assistant", Content: "recent answer"},
		},
	})
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}

	msgs := []ChatMessage{
		{Role: "user", Content: "old question 1"},
		{Role: "assistant", Content: "old answer 1"},
		{Role: "user", Content: "old question 2"},
		{Role: "assistant", Content: "old answer 2"},
		{Role: compactMarkerRole, Content: "Context compacted from 90% to 30%", Metadata: markerMeta},
		{Role: "user", Content: "after compaction"},
		{Role: "assistant", Content: "post-compaction answer"},
	}

	history := m.convertChatMessagesToLLM(msgs, "")
	if len(history) != 5 {
		t.Fatalf("expected 5 messages (3 snapshot + 2 post-marker), got %d: %+v", len(history), history)
	}
	if history[0].Role != "system" || history[0].Content != "[... 40 earlier messages omitted ...]" {
		t.Errorf("snapshot must reseed the history, got %+v", history[0])
	}
	if history[3].Content != "after compaction" || history[4].Content != "post-compaction answer" {
		t.Errorf("post-marker exchanges must append after the snapshot: %+v", history[3:])
	}
}

func TestConvertChatMessages_MarkerWithoutSnapshotIsNoop(t *testing.T) {
	m := &Manager{}
	msgs := []ChatMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
		{Role: compactMarkerRole, Content: "Context compacted from 90% to 30%", Metadata: json.RawMessage(`{"strategy":"sliding_window"}`)},
	}
	history := m.convertChatMessagesToLLM(msgs, "")
	if len(history) != 2 {
		t.Fatalf("snapshot-less marker must be a no-op, got %d messages", len(history))
	}
}
