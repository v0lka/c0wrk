package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/llm"
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
// the shared test factory returns a nil orchestrator).
func newCompactionTestManager(t *testing.T) (*Manager, *Session, *core.Orchestrator, chan Event, *recordingCompactionStore) {
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
	orch := core.NewOrchestrator(cfg, core.OrchestratorDeps{
		TokenCounter:  llm.NewSimpleTokenCounter(),
		ModelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{"test-model": {ContextWindow: 1000}}),
	})
	history := make([]llm.Message, 0, 60)
	for i := 0; i < 30; i++ {
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
