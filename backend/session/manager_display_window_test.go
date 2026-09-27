package session

import (
	"testing"
	"time"
)

// SetDisplayContextWindowForModel is the session-manager half of a runtime
// context-window correction (the embedded LLM read-back). The emitter already
// pins the method-level behaviour (see emitter_test.go); these tests pin the
// FAN-OUT: every live session's emitter receives the correction, model-scoped,
// and an idle session's status bar heals via the re-broadcast context_fill
// without waiting for the next message.
func TestManagerSetDisplayContextWindowForModel(t *testing.T) {
	manager, events, _ := testManager(t)

	first, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	second, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	emitterOf := func(id string) *EventEmitter {
		manager.mu.RLock()
		s := manager.sessions[id]
		manager.mu.RUnlock()
		if s == nil {
			t.Fatalf("session %q not found", id)
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.emitter
	}

	// Session one runs the embedded model and has already reported a fill on
	// the STALE (larger) display basis; session two switched to another model.
	firstEmitter := emitterOf(first.ID)
	firstEmitter.SetLastModel("Bonsai 2 27B", "qwen")
	firstEmitter.SetDisplayContextWindow(262144)
	firstEmitter.ContextFill(25.0, 65536, 98304, "ok", "")

	secondEmitter := emitterOf(second.ID)
	secondEmitter.SetLastModel("glm-5.3", "glm")
	secondEmitter.SetDisplayContextWindow(200000)
	secondEmitter.ContextFill(10.0, 20000, 180000, "ok", "")

	drainEvents(events)

	manager.SetDisplayContextWindowForModel("Bonsai 2 27B", 131072)

	// The matching idle session must have re-broadcast a corrected fill on the
	// new basis; the model-scoped guard must have dropped the other session.
	deadline := time.After(2 * time.Second)
	var rebroadcast bool
	var secondSessionsEvents int
	for !rebroadcast {
		select {
		case ev := <-events:
			if ev.Type != "context_fill" {
				continue
			}
			switch ev.SessionID {
			case first.ID:
				data, ok := ev.Data.(ContextFillEventData)
				if !ok {
					t.Fatalf("context_fill payload type %T", ev.Data)
				}
				if data.MaxTokens != 131072 {
					t.Errorf("re-broadcast MaxTokens = %d, want 131072", data.MaxTokens)
				}
				if data.UsedTokens != 65536 {
					t.Errorf("re-broadcast UsedTokens = %d, want 65536", data.UsedTokens)
				}
				want := float64(65536) / float64(131072) * 100
				if data.FillPercent != want {
					t.Errorf("re-broadcast FillPercent = %v, want %v", data.FillPercent, want)
				}
				rebroadcast = true
			case second.ID:
				secondSessionsEvents++
			}
		case <-deadline:
			t.Fatal("no re-broadcast context_fill for the matching session")
		}
	}
	if secondSessionsEvents != 0 {
		t.Errorf("model-scoped guard leaked %d events into the switched session", secondSessionsEvents)
	}

	// The swap persists: the next executor fill on the corrected session is
	// presented on the new basis.
	firstEmitter.ContextFill(40.0, 52428, 98304, "ok", "")
	select {
	case ev := <-events:
		if ev.Type != "context_fill" || ev.SessionID != first.ID {
			t.Fatalf("expected a context_fill for the corrected session, got %v for %q", ev.Type, ev.SessionID)
		}
		data, ok := ev.Data.(ContextFillEventData)
		if !ok {
			t.Fatalf("context_fill payload type %T", ev.Data)
		}
		if data.MaxTokens != 131072 {
			t.Errorf("post-push fill MaxTokens = %d, want 131072", data.MaxTokens)
		}
	default:
		t.Fatal("no post-push fill event observed")
	}
}

// A correction for a model the session never ran must leave every emitter
// untouched — no re-broadcast, no basis swap.
func TestManagerSetDisplayContextWindowForModel_UnknownModelDropped(t *testing.T) {
	manager, events, _ := testManager(t)

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	manager.mu.RLock()
	s := manager.sessions[info.ID]
	manager.mu.RUnlock()
	s.mu.RLock()
	emitter := s.emitter
	s.mu.RUnlock()

	emitter.SetLastModel("Bonsai 2 27B", "qwen")
	emitter.SetDisplayContextWindow(262144)
	emitter.ContextFill(25.0, 65536, 98304, "ok", "")
	drainEvents(events)

	manager.SetDisplayContextWindowForModel("glm-5.3", 131072)

	select {
	case ev := <-events:
		t.Fatalf("unexpected event %q for a non-matching model correction", ev.Type)
	default:
	}

	// And the basis is unchanged.
	emitter.ContextFill(30.0, 65536, 98304, "ok", "")
	select {
	case ev := <-events:
		data, ok := ev.Data.(ContextFillEventData)
		if !ok {
			t.Fatalf("context_fill payload type %T", ev.Data)
		}
		if data.MaxTokens != 262144 {
			t.Errorf("unrelated correction moved the display basis: MaxTokens = %d, want 262144", data.MaxTokens)
		}
	default:
		t.Fatal("no post-correction fill event observed")
	}
}

// A no-op correction (empty model, non-positive window) must be rejected
// before any session is touched.
func TestManagerSetDisplayContextWindowForModel_NoOpArguments(t *testing.T) {
	manager, events, _ := testManager(t)

	if _, err := manager.CreateSession(testProjectID, testWorkspacePath(t)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	drainEvents(events)

	manager.SetDisplayContextWindowForModel("", 131072)
	manager.SetDisplayContextWindowForModel("Bonsai 2 27B", 0)
	manager.SetDisplayContextWindowForModel("Bonsai 2 27B", -1)

	select {
	case ev := <-events:
		t.Fatalf("no-op correction emitted %q", ev.Type)
	default:
	}
}
