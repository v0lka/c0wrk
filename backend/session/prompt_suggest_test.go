package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// This file pins the backend side of the prompt-suggestion trigger (ADR-088):
// the outcome gate on emitTaskComplete (full/partial fire, cancelled/paused/
// failed/aborted never do), the is_nudge-free generation input with the final
// answer supplemented past the persister race, the 10-session project window,
// the session-scoped event, the zero-cost disabled posture, and the
// readiness-gate ordering shared with the title path.

// suggestionTestStore is a captureStore with the suggestion-path methods
// wired: per-session transcripts, a per-project session list, and recorded
// suggestion saves. Everything else stays a no-op of the embedded store.
type suggestionTestStore struct {
	*captureStore
	mu        sync.Mutex
	history   map[string][]ChatMessage
	byProject map[string][]SessionInfo
	saves     [][]core.PromptSuggestion
	saveErr   error                   // when set, SavePromptSuggestions fails
	active    []core.PromptSuggestion // what ListPromptSuggestions reports
}

func newSuggestionTestStore() *suggestionTestStore {
	return &suggestionTestStore{
		captureStore: &captureStore{},
		history:      make(map[string][]ChatMessage),
		byProject:    make(map[string][]SessionInfo),
	}
}

func (s *suggestionTestStore) LoadSessionHistory(_ context.Context, sessionID string) ([]ChatMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history[sessionID], nil
}

func (s *suggestionTestStore) ListSessionsByProject(_ context.Context, projectID string) ([]SessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byProject[projectID], nil
}

func (s *suggestionTestStore) SavePromptSuggestions(_ context.Context, rows []core.PromptSuggestion) ([]core.PromptSuggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	s.saves = append(s.saves, rows)
	return rows, nil
}

func (s *suggestionTestStore) ListPromptSuggestions(_ context.Context, _ string) ([]core.PromptSuggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, nil
}

func (s *suggestionTestStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saves)
}

// recordingSuggestCaller is an LLMPromptSuggestCaller double: it reports when
// it ran, with which kinds and inputs, and what budget it was handed.
type recordingSuggestCaller struct {
	mu        sync.Mutex
	calls     int
	kinds     []core.SuggestionKind
	inputs    []core.PromptSuggestInput
	deadline  time.Time
	remaining time.Duration
	hasDDL    bool
	live      bool
	firstDone chan struct{}
	onCall    func()
}

func newRecordingSuggestCaller() *recordingSuggestCaller {
	return &recordingSuggestCaller{firstDone: make(chan struct{})}
}

func (c *recordingSuggestCaller) GeneratePromptSuggestions(ctx context.Context, kind core.SuggestionKind, in core.PromptSuggestInput) ([]string, error) {
	c.mu.Lock()
	c.calls++
	c.kinds = append(c.kinds, kind)
	c.inputs = append(c.inputs, in)
	c.deadline, c.hasDDL = ctx.Deadline()
	c.remaining = time.Until(c.deadline)
	c.live = ctx.Err() == nil
	first := c.calls == 1
	c.mu.Unlock()
	if c.onCall != nil {
		c.onCall()
	}
	if first {
		close(c.firstDone)
	}
	return []string{"a suggested prompt", "another suggested prompt"}, nil
}

func (c *recordingSuggestCaller) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *recordingSuggestCaller) snapshot() (calls int, kinds []core.SuggestionKind, inputs []core.PromptSuggestInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]core.SuggestionKind(nil), c.kinds...), append([]core.PromptSuggestInput(nil), c.inputs...)
}

// eventRecorder captures emitted events so tests can assert on the
// prompt_suggestions announcement.
type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) record(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *eventRecorder) ofType(eventType string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Type == eventType {
			out = append(out, e)
		}
	}
	return out
}

// newPromptSuggestManager wires a manager with the suggestion generator and
// the test store, returning the event recorder alongside.
func newPromptSuggestManager(t *testing.T, store *suggestionTestStore, caller *recordingSuggestCaller) (*Manager, *eventRecorder) {
	t.Helper()
	rec := &eventRecorder{}
	mgr := NewManager(nil, rec.record, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetSessionStore(store)
	mgr.SetPromptSuggestionsGenerator(NewPromptSuggestGenerator(caller))
	return mgr, rec
}

// suggestTestSession registers an in-memory session with the manager.
func suggestTestSession(mgr *Manager, id, projectID string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	mgr.sessions[id] = &Session{ID: id, Name: "Session " + safeSessionPrefix(id), ProjectID: projectID}
}

// TestPromptSuggestionsOutcomeGate pins the D2 outcome gate: full and partial
// finishes each fire exactly one generation pass; cancelled, paused, failed,
// and aborted finishes never fire one — not even the readiness gate runs.
// Cancelled and Paused deliberately share the "partial" completion string
// with a degraded outcome, so the gate must read the typed status.
func TestPromptSuggestionsOutcomeGate(t *testing.T) {
	tests := []struct {
		name       string
		status     orchestration.ExecutionStatus
		completion string
		wantPasses int
	}{
		{"full success triggers", orchestration.ExecutionStatusSuccess, "full", 1},
		{"partial triggers", orchestration.ExecutionStatusPartial, "partial", 1},
		{"cancelled never triggers", orchestration.ExecutionStatusCancelled, "partial", 0},
		{"paused never triggers", orchestration.ExecutionStatusPaused, "partial", 0},
		{"failed never triggers", orchestration.ExecutionStatusFailed, "failed", 0},
		{"aborted never triggers", orchestration.ExecutionStatusAborted, "aborted", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newSuggestionTestStore()
			caller := newRecordingSuggestCaller()
			mgr, rec := newPromptSuggestManager(t, store, caller)
			const id = "gate-session"
			suggestTestSession(mgr, id, project.NoProjectID)
			mgr.SetServiceLLMGate(func(_ context.Context, kind string) error {
				if want := string(core.ServiceKindPromptSuggest); kind != want {
					t.Errorf("gate kind = %q, want %q", kind, want)
				}
				if tc.wantPasses == 0 {
					t.Error("the readiness gate ran for a non-triggering outcome")
				}
				return nil
			})
			if tc.completion != "full" {
				captureManagerDiagnostics(t, mgr, warningDiagnostic("degraded task completion without resumable safety net",
					map[string]string{"session": id, "completion": tc.completion}))
			}

			mgr.emitTaskComplete(id, &core.HandleResult{Output: "out", Status: tc.status}, nil)
			mgr.Shutdown() // join the background pass before asserting

			calls, kinds, _ := caller.snapshot()
			if calls != tc.wantPasses {
				t.Fatalf("generation passes = %d, want %d", calls, tc.wantPasses)
			}
			if tc.wantPasses == 0 {
				if got := rec.ofType("prompt_suggestions"); len(got) != 0 {
					t.Errorf("prompt_suggestions events = %d, want 0", len(got))
				}
				if got := store.saveCount(); got != 0 {
					t.Errorf("store saves = %d, want 0", got)
				}
				return
			}
			// No-project session: exactly the continuation pass ran.
			if len(kinds) != 1 || kinds[0] != core.SuggestionKindGoalAdvance {
				t.Errorf("kinds = %v, want [goal_advance]", kinds)
			}
		})
	}
}

// TestPromptSuggestionsNilResultIsAFullSuccess keeps the taskCompletionInfo
// contract on the trigger path: a nil result completes as "full".
func TestPromptSuggestionsNilResultIsAFullSuccess(t *testing.T) {
	store := newSuggestionTestStore()
	caller := newRecordingSuggestCaller()
	mgr, _ := newPromptSuggestManager(t, store, caller)
	const id = "nil-result-session"
	suggestTestSession(mgr, id, project.NoProjectID)

	mgr.emitTaskComplete(id, nil, nil)
	mgr.Shutdown()

	if got := caller.callCount(); got != 1 {
		t.Fatalf("generation passes = %d, want 1", got)
	}
}

// TestPromptSuggestionsInputAssembly pins the generation input of the
// continuation pass: only user/assistant rows survive, the is_nudge user
// message is dropped entirely while the answer it interrupted stays (D5), and
// the final answer from the result supplements the transcript when the
// event-persister write has not landed yet.
func TestPromptSuggestionsInputAssembly(t *testing.T) {
	store := newSuggestionTestStore()
	store.history["sess-1"] = []ChatMessage{
		{Role: "user", Content: "Fix the parser bug"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "continue", Metadata: json.RawMessage(`{"is_nudge":true}`)},
		{Role: "assistant", Content: "more"},
		{Role: "routing", Content: "never a prompt"},
	}
	caller := newRecordingSuggestCaller()
	mgr, _ := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "the final answer",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	calls, kinds, inputs := caller.snapshot()
	if calls != 1 {
		t.Fatalf("generation passes = %d, want 1", calls)
	}
	if kinds[0] != core.SuggestionKindGoalAdvance {
		t.Fatalf("kind = %q, want goal_advance", kinds[0])
	}
	sessions := inputs[0].Sessions
	if len(sessions) != 1 || sessions[0].ID != "sess-1" {
		t.Fatalf("sessions = %+v, want one sess-1 transcript", sessions)
	}
	want := []core.PromptSuggestMessage{
		{Role: "user", Content: "Fix the parser bug"},
		{Role: "assistant", Content: "done"},
		// the nudge row itself is gone; its answer stayed (D5)
		{Role: "assistant", Content: "more"},
		// the race supplement
		{Role: "assistant", Content: "the final answer"},
	}
	if !reflect.DeepEqual(sessions[0].Messages, want) {
		t.Errorf("messages = %+v, want %+v", sessions[0].Messages, want)
	}
}

// TestPromptSuggestionsFinalAnswerAlreadyPersisted covers the no-race half of
// the supplement: a transcript that already ends with the result output is
// passed through untouched.
func TestPromptSuggestionsFinalAnswerAlreadyPersisted(t *testing.T) {
	store := newSuggestionTestStore()
	store.history["sess-1"] = []ChatMessage{
		{Role: "user", Content: "Fix the parser bug"},
		{Role: "assistant", Content: "the final answer"},
	}
	caller := newRecordingSuggestCaller()
	mgr, _ := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "the final answer",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	_, _, inputs := caller.snapshot()
	want := []core.PromptSuggestMessage{
		{Role: "user", Content: "Fix the parser bug"},
		{Role: "assistant", Content: "the final answer"},
	}
	if !reflect.DeepEqual(inputs[0].Sessions[0].Messages, want) {
		t.Errorf("messages = %+v, want %+v", inputs[0].Sessions[0].Messages, want)
	}
}

// TestPromptSuggestionsFinalAnswerWithoutAssistantRow covers the race half on
// a transcript with NO assistant row yet — the first finished task of a fresh
// session whose final write is still in flight: the answer must still be
// appended, or the transcript's only prompt→answer pair is lost and the pass
// yields nothing.
func TestPromptSuggestionsFinalAnswerWithoutAssistantRow(t *testing.T) {
	store := newSuggestionTestStore()
	store.history["sess-1"] = []ChatMessage{
		{Role: "user", Content: "Fix the parser bug"},
	}
	caller := newRecordingSuggestCaller()
	mgr, _ := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "the final answer",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	_, _, inputs := caller.snapshot()
	want := []core.PromptSuggestMessage{
		{Role: "user", Content: "Fix the parser bug"},
		{Role: "assistant", Content: "the final answer"},
	}
	if !reflect.DeepEqual(inputs[0].Sessions[0].Messages, want) {
		t.Errorf("messages = %+v, want %+v", inputs[0].Sessions[0].Messages, want)
	}
}

// suggestionTs renders the i-th deterministic hour stamp (2025-06-01T00:00:00Z
// plus i hours): distinct, ordered RFC3339 values so recency ordering in
// tests is stable without sleeping.
func suggestionTs(i int) string {
	return fmt.Sprintf("2025-06-%02dT%02d:00:00Z", 1+i/24, i%24)
}

// TestPromptSuggestionsProjectWindowCapsAtTen pins the next_task window (D3):
// exactly the 10 most recent project sessions, chronological (triggering
// session last), each transcript is_nudge-free, and the triggering session's
// transcript carries the race-supplemented final answer. The store list
// arrives in its pinned-first order — a long-idle pinned session on top — and
// must be read in pure recency: the pinned session neither enters the window
// nor displaces a recent one.
func TestPromptSuggestionsProjectWindowCapsAtTen(t *testing.T) {
	store := newSuggestionTestStore()
	// The store's pinned-first activity order: the pinned, long-idle session
	// sorts on top, the recent sessions follow newest-first.
	store.byProject["proj-1"] = append(store.byProject["proj-1"], SessionInfo{
		ID: "s-pinned", ProjectID: "proj-1", Pinned: true,
		LastActiveAt: suggestionTs(0), CreatedAt: suggestionTs(0),
	})
	// Twelve recent sessions, newest-first.
	for i := 12; i >= 1; i-- {
		id := fmt.Sprintf("s-%02d", i)
		store.byProject["proj-1"] = append(store.byProject["proj-1"], SessionInfo{
			ID: id, ProjectID: "proj-1", LastActiveAt: suggestionTs(i), CreatedAt: suggestionTs(i),
		})
		store.history[id] = []ChatMessage{
			{Role: "user", Content: "ask " + id},
			// a nudge that must never reach the window (D5)
			{Role: "user", Content: "continue", Metadata: json.RawMessage(`{"is_nudge":true}`)},
			{Role: "assistant", Content: "ans " + id},
		}
	}
	caller := newRecordingSuggestCaller()
	mgr, _ := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "s-12", "proj-1")

	mgr.emitTaskComplete("s-12", &core.HandleResult{
		Output: "the final answer",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	calls, kinds, inputs := caller.snapshot()
	if calls != 2 {
		t.Fatalf("generation passes = %d, want 2 (continuation + new-session)", calls)
	}
	if kinds[0] != core.SuggestionKindGoalAdvance || kinds[1] != core.SuggestionKindNextTask {
		t.Fatalf("kinds = %v, want [goal_advance next_task]", kinds)
	}
	window := inputs[1].Sessions
	if len(window) != 10 {
		t.Fatalf("next_task window = %d sessions, want 10", len(window))
	}
	if window[0].ID != "s-03" || window[len(window)-1].ID != "s-12" {
		t.Errorf("window ends = %q..%q, want s-03..s-12 (chronological, triggering last)", window[0].ID, window[len(window)-1].ID)
	}
	for _, s := range window {
		if s.ID == "s-pinned" {
			t.Errorf("the long-idle pinned session must not enter the recency window")
		}
		if s.ID == "s-01" || s.ID == "s-02" {
			t.Errorf("session %s is older than the window and must be dropped", s.ID)
		}
	}
	// s-04 stands in for the window's per-session shape: nudge-free pairs.
	want := []core.PromptSuggestMessage{
		{Role: "user", Content: "ask s-04"},
		{Role: "assistant", Content: "ans s-04"},
	}
	if !reflect.DeepEqual(window[1].Messages, want) {
		t.Errorf("s-04 messages = %+v, want %+v", window[1].Messages, want)
	}
	// The triggering session ends with the supplemented final answer.
	last := window[len(window)-1].Messages
	if len(last) == 0 || last[len(last)-1].Content != "the final answer" || last[len(last)-1].Role != "assistant" {
		t.Errorf("triggering session transcript tail = %+v, want a final assistant answer", last)
	}
}

// TestPromptSuggestionsEventEmitted pins the announcement: each persisted
// pass emits the session-scoped prompt_suggestions event carrying the FULL
// active set as read back from the store.
func TestPromptSuggestionsEventEmitted(t *testing.T) {
	store := newSuggestionTestStore()
	store.active = []core.PromptSuggestion{
		{ID: "row-1", SessionID: "sess-1", Kind: core.SuggestionKindGoalAdvance, Text: "a suggested prompt", Status: SuggestionStatusActive},
		{ID: "row-2", SessionID: "sess-1", Kind: core.SuggestionKindGoalAdvance, Text: "another suggested prompt", Status: SuggestionStatusActive},
	}
	caller := newRecordingSuggestCaller()
	mgr, rec := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "out",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	events := rec.ofType("prompt_suggestions")
	if len(events) == 0 {
		t.Fatal("no prompt_suggestions event was emitted")
	}
	for _, e := range events {
		if e.SessionID != "sess-1" {
			t.Errorf("event session = %q, want sess-1", e.SessionID)
		}
		data, ok := e.Data.(PromptSuggestionsData)
		if !ok {
			t.Fatalf("event data type = %T, want PromptSuggestionsData", e.Data)
		}
		if data.SessionID != "sess-1" {
			t.Errorf("data session = %q, want sess-1", data.SessionID)
		}
		if !reflect.DeepEqual(data.Suggestions, store.active) {
			t.Errorf("data suggestions = %+v, want the store's active set %+v", data.Suggestions, store.active)
		}
	}
}

// TestPromptSuggestionsDisabledCostsNothing pins the disabled posture: with
// prompt_suggestions.enabled false the outcome gate never fires — zero LLM
// calls, zero gate runs, zero store writes, zero events, for BOTH triggering
// outcomes. The generator-less default posture costs the same.
func TestPromptSuggestionsDisabledCostsNothing(t *testing.T) {
	cases := []struct {
		name   string
		status orchestration.ExecutionStatus
	}{
		{"full success", orchestration.ExecutionStatusSuccess},
		{"partial", orchestration.ExecutionStatusPartial},
	}
	for _, tc := range cases {
		t.Run("flag off / "+tc.name, func(t *testing.T) {
			store := newSuggestionTestStore()
			caller := newRecordingSuggestCaller()
			mgr, rec := newPromptSuggestManager(t, store, caller)
			mgr.SetPromptSuggestionsEnabled(false)
			suggestTestSession(mgr, "sess-1", "proj-1")
			mgr.SetServiceLLMGate(func(context.Context, string) error {
				t.Error("the readiness gate ran for a disabled feature")
				return nil
			})
			if tc.status == orchestration.ExecutionStatusPartial {
				captureManagerDiagnostics(t, mgr, warningDiagnostic("degraded task completion without resumable safety net",
					map[string]string{"session": "sess-1", "completion": "partial"}))
			}

			mgr.emitTaskComplete("sess-1", &core.HandleResult{Output: "out", Status: tc.status}, nil)
			mgr.Shutdown()

			if got := caller.callCount(); got != 0 {
				t.Errorf("generation passes = %d, want 0 with the flag off", got)
			}
			if got := store.saveCount(); got != 0 {
				t.Errorf("store saves = %d, want 0 with the flag off", got)
			}
			if got := rec.ofType("prompt_suggestions"); len(got) != 0 {
				t.Errorf("prompt_suggestions events = %d, want 0 with the flag off", len(got))
			}
		})
	}

	t.Run("no generator installed", func(t *testing.T) {
		rec := &eventRecorder{}
		mgr := NewManager(nil, rec.record, runtimeTempDir(t))
		t.Cleanup(mgr.Shutdown)
		store := newSuggestionTestStore()
		mgr.SetSessionStore(store)
		suggestTestSession(mgr, "sess-1", "proj-1")

		mgr.emitTaskComplete("sess-1", &core.HandleResult{
			Output: "out",
			Status: orchestration.ExecutionStatusSuccess,
		}, nil)
		mgr.Shutdown()

		if got := store.saveCount(); got != 0 {
			t.Errorf("store saves = %d, want 0 without a generator", got)
		}
		if got := rec.ofType("prompt_suggestions"); len(got) != 0 {
			t.Errorf("prompt_suggestions events = %d, want 0 without a generator", len(got))
		}
	})
}

// TestPromptSuggestionsGateFailureSkipsGeneration is the fail-closed half: a
// model that cannot be loaded must not be sent a request, and nothing is
// persisted or announced.
func TestPromptSuggestionsGateFailureSkipsGeneration(t *testing.T) {
	gateErr := errors.New("the embedded model could not be loaded")
	store := newSuggestionTestStore()
	caller := newRecordingSuggestCaller()
	mgr, rec := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)
	mgr.SetServiceLLMGate(func(context.Context, string) error { return gateErr })

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "out",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	if got := caller.callCount(); got != 0 {
		t.Errorf("generation passes = %d, want 0 after a failed gate", got)
	}
	if got := store.saveCount(); got != 0 {
		t.Errorf("store saves = %d, want 0 after a failed gate", got)
	}
	if got := rec.ofType("prompt_suggestions"); len(got) != 0 {
		t.Errorf("prompt_suggestions events = %d, want 0 after a failed gate", len(got))
	}
}

// TestServiceLLMGateRunsBeforePromptSuggestionRequest is the ordering
// property, shared with the title path: the gate completes before the LLM
// call is issued, and the call's own budget is armed only afterwards — so a
// slow load cannot spend it.
func TestServiceLLMGateRunsBeforePromptSuggestionRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// In-memory state only: no factory, no dump handles, no real transport.
		mgr := NewManager(nil, func(Event) {}, "")
		const id = "suggest-session"
		mgr.mu.Lock()
		mgr.sessions[id] = &Session{ID: id, ProjectID: project.NoProjectID}
		mgr.mu.Unlock()
		mgr.SetServiceLLMTimeout(testServiceLLMBudget)
		mgr.SetSessionStore(newSuggestionTestStore())
		caller := newRecordingSuggestCaller()
		order := &orderLog{}
		caller.onCall = func() { order.record("prompt_suggest request") }
		mgr.SetPromptSuggestionsGenerator(NewPromptSuggestGenerator(caller))
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		defer func() {
			releaseOnce.Do(func() { close(release) })
			synctest.Wait()
			mgr.Shutdown()
		}()
		mgr.SetServiceLLMGate(func(ctx context.Context, _ string) error {
			order.record("gate")
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

		mgr.maybeSpawnPromptSuggestions(id, &core.HandleResult{
			Output: "out",
			Status: orchestration.ExecutionStatusSuccess,
		})
		synctest.Wait()
		<-entered
		time.Sleep(testGateDelay)
		synctest.Wait()
		if got := caller.callCount(); got != 0 {
			t.Fatalf("calls before gate release = %d, want 0", got)
		}
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
		select {
		case <-caller.firstDone:
		default:
			t.Fatal("prompt_suggest request missing after gate release")
		}
		if !mgr.bg.closeAndWait(mgr.stopTimeout) {
			t.Fatal("prompt-suggestion worker did not join")
		}
		if got := order.snapshot(); len(got) != 2 || got[0] != "gate" || got[1] != "prompt_suggest request" {
			t.Errorf("order = %v, want [gate, prompt_suggest request]", got)
		}
		caller.mu.Lock()
		defer caller.mu.Unlock()
		if caller.calls != 1 {
			t.Errorf("calls = %d, want 1", caller.calls)
		}
		if !caller.hasDDL || !caller.live || caller.remaining != testServiceLLMBudget {
			t.Errorf("request entry: deadline=%t live=%t remaining=%v, want true/true/%v",
				caller.hasDDL, caller.live, caller.remaining, testServiceLLMBudget)
		}
	})
}

// TestPromptSuggestionsSaveFailureStaysSilent pins the persistence failure
// half of D9: a store write error degrades to no event, never a panic or a
// surfaced failure.
func TestPromptSuggestionsSaveFailureStaysSilent(t *testing.T) {
	store := newSuggestionTestStore()
	store.saveErr = errors.New("disk full")
	caller := newRecordingSuggestCaller()
	mgr, rec := newPromptSuggestManager(t, store, caller)
	suggestTestSession(mgr, "sess-1", project.NoProjectID)

	mgr.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "out",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)
	mgr.Shutdown()

	if got := caller.callCount(); got != 1 {
		t.Errorf("generation passes = %d, want 1 (the LLM call itself still ran)", got)
	}
	if got := rec.ofType("prompt_suggestions"); len(got) != 0 {
		t.Errorf("prompt_suggestions events = %d, want 0 when the save fails", len(got))
	}
}
