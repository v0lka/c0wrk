package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core"
)

// Live-send tests: a message sent while a task is already running is queued
// into the running request (delivered at the executor's next step boundary),
// not a new task. The pausing window rejects sends; the request epilogue
// resets pausing and converts undelivered leftovers into a follow-up task.

// liveTestSession creates an active session with a real (zero-dep)
// orchestrator so the live-send branch has a queue to push into. The
// orchestrator never runs a request in these tests — only its queue is used.
func liveTestSession(t *testing.T) (*Manager, *Session, *core.Orchestrator, chan Event) {
	t.Helper()
	manager, events, _ := testManager(t)
	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sess, _ := manager.GetSession(info.ID)
	if sess == nil {
		t.Fatal("session not in manager")
	}

	orch := core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{})
	sess.mu.Lock()
	sess.orchestrator = orch
	sess.active = true
	sess.mu.Unlock()
	return manager, sess, orch, events
}

// TestSendMessage_LiveQueuesIntoRunningTask verifies the core live-send
// contract: with the session active and NOT pausing, SendMessage queues the
// text on the running request's orchestrator, emits message_received, and
// returns nil (no error, no task started).
func TestSendMessage_LiveQueuesIntoRunningTask(t *testing.T) {
	manager, sess, orch, events := liveTestSession(t)

	err := manager.SendMessage(context.Background(), sess.ID, "steer left", nil, nil, "", "", false, "", false)
	if err != nil {
		t.Fatalf("live SendMessage returned error: %v", err)
	}

	if got := orch.DrainLiveUserMessages(); got != "steer left" {
		t.Errorf("drained %q, want the live message", got)
	}
	if got := orch.DrainLiveUserMessages(); got != "" {
		t.Errorf("drained %q on second poll, want empty", got)
	}

	// message_received must have been emitted so non-optimistic listeners see
	// the message.
	found := false
	for {
		select {
		case evt := <-events:
			if evt.Type == "message_received" {
				found = true
			}
		default:
			goto done
		}
	}
done:
	if !found {
		t.Error("message_received was not emitted for a live send")
	}
}

// TestSendMessage_LiveRejectedWhilePausing verifies the pausing window: once
// the session is flagged pausing, a live send fails with ErrPausePending and
// nothing is queued.
func TestSendMessage_LiveRejectedWhilePausing(t *testing.T) {
	manager, sess, orch, _ := liveTestSession(t)

	sess.mu.Lock()
	sess.pausing = true
	sess.mu.Unlock()

	err := manager.SendMessage(context.Background(), sess.ID, "too late", nil, nil, "", "", false, "", false)
	if !errors.Is(err, ErrPausePending) {
		t.Fatalf("expected ErrPausePending, got %v", err)
	}
	if got := orch.DrainLiveUserMessages(); got != "" {
		t.Errorf("queue holds %q after a rejected send, want empty", got)
	}
}

// TestSendMessage_LiveRejectedForGoal verifies goal requests never take the
// live path (they supersede running work and need an idle session).
func TestSendMessage_LiveRejectedForGoal(t *testing.T) {
	manager, sess, orch, _ := liveTestSession(t)

	err := manager.SendMessage(context.Background(), sess.ID, "/goal chase it", nil, nil, "", "", true, "", false)
	if err == nil {
		t.Fatal("expected an error for a goal request into a running task")
	}
	if got := orch.DrainLiveUserMessages(); got != "" {
		t.Errorf("queue holds %q after a rejected goal send, want empty", got)
	}
}

// TestSendMessage_LiveRejectedForSkills verifies skill/agent references never
// take the live path (they reshape task context at HandleMessage time).
func TestSendMessage_LiveRejectedForSkills(t *testing.T) {
	manager, sess, orch, _ := liveTestSession(t)

	err := manager.SendMessage(context.Background(), sess.ID, "run the checker", []string{"reviewer"}, nil, "", "", false, "", false)
	if err == nil {
		t.Fatal("expected an error for a skill send into a running task")
	}
	if got := orch.DrainLiveUserMessages(); got != "" {
		t.Errorf("queue holds %q after a rejected skill send, want empty", got)
	}
}

// TestPauseSession_FlagsPausingWindow verifies PauseSession sets the pausing
// flag on an active session (the request epilogue clears it).
func TestPauseSession_FlagsPausingWindow(t *testing.T) {
	manager, sess, _, _ := liveTestSession(t)

	if err := manager.PauseSession(sess.ID); err != nil {
		t.Fatalf("PauseSession failed: %v", err)
	}
	sess.mu.Lock()
	pausing := sess.pausing
	sess.mu.Unlock()
	if !pausing {
		t.Error("expected session.pausing to be set by PauseSession on an active session")
	}
}

// TestPauseSession_IdleSessionDoesNotOpenWindow verifies PauseSession on an
// idle session (no active task) leaves the pausing flag off: there is no
// request in flight, so no window to close.
func TestPauseSession_IdleSessionDoesNotOpenWindow(t *testing.T) {
	manager, sess, _, _ := liveTestSession(t)
	sess.mu.Lock()
	sess.active = false
	sess.mu.Unlock()

	if err := manager.PauseSession(sess.ID); err != nil {
		t.Fatalf("PauseSession failed: %v", err)
	}
	sess.mu.Lock()
	pausing := sess.pausing
	sess.mu.Unlock()
	if pausing {
		t.Error("expected session.pausing to stay false when no task is active")
	}
}

// TestFinishLiveLeftover_FailureEmitsServiceNotice locks the channel contract
// for a failed follow-up launch: the notice goes out as a `service` event,
// never as `error`. `error` is reserved for terminal events the UI treats as
// "the run ended" (it clears task/streaming state and the unfinished-task
// flag), while this failure does not settle the just-finished task.
func TestFinishLiveLeftover_FailureEmitsServiceNotice(t *testing.T) {
	manager, sess, _, events := liveTestSession(t)
	drainEvents(events)

	// Force the internal sendMessage relaunch to fail: an archived session is
	// read-only, so the follow-up launch is rejected before any task starts.
	// active=false mirrors the deactivated state at the epilogue call site.
	sess.mu.Lock()
	sess.Archived = true
	sess.active = false
	sess.mu.Unlock()

	manager.finishLiveLeftover(context.Background(), sess.ID, sess, []string{"queued text"})

	var got []Event
	for {
		select {
		case evt := <-events:
			got = append(got, evt)
		default:
			goto done
		}
	}
done:
	if len(got) != 1 {
		t.Fatalf("expected exactly one notice event, got %d (%v)", len(got), got)
	}
	evt := got[0]
	if evt.Type != "service" {
		t.Fatalf("expected a service notice, got %q (error must stay terminal-only)", evt.Type)
	}
	data, ok := evt.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", evt.Data)
	}
	if data["phase"] != "orchestration" {
		t.Errorf("phase = %v, want orchestration", data["phase"])
	}
	if content, _ := data["content"].(string); !strings.HasPrefix(content, "Queued message could not start a follow-up task") {
		t.Errorf("content = %q, want the follow-up failure notice", content)
	}
}
