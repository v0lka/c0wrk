package session

import (
	"testing"
)

// TestEmitToolConfirm_RoutesThroughSessionEmitter verifies the tool-confirm
// event flows through the live session's emitter — so it reaches the UI AND
// the activity tracker that backs the runtime-status snapshot read on session
// switches ("Awaiting confirmation..." instead of a stale "Running tool: ...").
func TestEmitToolConfirm_RoutesThroughSessionEmitter(t *testing.T) {
	manager, _, _ := testManager(t)

	var captured []Event
	emitter := NewEventEmitter("sess-confirm", func(e Event) { captured = append(captured, e) })
	manager.mu.Lock()
	manager.sessions["sess-confirm"] = &Session{ID: "sess-confirm", emitter: emitter}
	manager.mu.Unlock()

	payload := ToolConfirmPayload{ConfirmID: "c1", Tool: "bash_exec", Args: `{"command":"ls"}`}
	manager.EmitToolConfirm("sess-confirm", payload)

	if len(captured) != 1 {
		t.Fatalf("expected 1 emitted event, got %d: %+v", len(captured), captured)
	}
	evt := captured[0]
	if evt.Type != "tool_confirm" || evt.SessionID != "sess-confirm" {
		t.Errorf("event = %s/%s, want tool_confirm/sess-confirm", evt.Type, evt.SessionID)
	}
	if got, ok := evt.Data.(ToolConfirmPayload); !ok || got != payload {
		t.Errorf("payload = %+v (ok=%v), want %+v", evt.Data, ok, payload)
	}
	if got := emitter.LastActivity(); got != "Awaiting confirmation..." {
		t.Errorf("activity = %q, want %q", got, "Awaiting confirmation...")
	}
}

// TestEmitToolConfirm_FallsBackToRawPipeline verifies that a session without
// a live emitter (e.g. a confirmation racing a session restore) still
// delivers the event through the raw pipeline so the frontend receives the
// confirmation card.
func TestEmitToolConfirm_FallsBackToRawPipeline(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	payload := ToolConfirmPayload{ConfirmID: "c2", Tool: "edit_file", Args: "{}"}
	manager.EmitToolConfirm("missing-session", payload)

	select {
	case evt := <-eventChan:
		if evt.Type != "tool_confirm" || evt.SessionID != "missing-session" {
			t.Errorf("event = %s/%s, want tool_confirm/missing-session", evt.Type, evt.SessionID)
		}
		if got, ok := evt.Data.(ToolConfirmPayload); !ok || got != payload {
			t.Errorf("payload = %+v (ok=%v), want %+v", evt.Data, ok, payload)
		}
	default:
		t.Fatal("expected tool_confirm on the raw pipeline, got nothing")
	}
}
