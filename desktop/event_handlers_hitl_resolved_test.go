// Tests for the hitl_resolved multi-surface convergence event: every HITL
// resolution funnel (the six response handlers plus the two shared goal
// resolvers) must broadcast exactly one settlement event carrying the kind,
// the prompt id, and the EFFECTIVE (first-responder) decision, so a surface
// that did not perform the resolution retires its card immediately and a
// surface that raced it converges on the outcome that actually drove the
// agent. See emitHitlResolved (event_handlers.go) and
// specs/domains/web-access.md (Multi-surface consistency).

package desktop

import (
	"testing"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// hitlResolvedEvents returns every flattened hitl_resolved event for sess-1
// carried by the recorder snapshot.
func hitlResolvedEvents(t *testing.T, a *App, rec *emitRecorder) []session.HitlResolvedData {
	t.Helper()
	// Deterministic drain: the batcher delivers barrier events (hitl_resolved
	// included) from its flusher goroutine. A plain flushEvents is NOT a
	// barrier: the flusher can win the queue and land its emit AFTER the
	// snapshot below (observed as a rare "got 0 hitl_resolved events" on
	// loaded CI, reproducible under -race). stopEventBatcher joins the
	// flusher goroutine before returning, so every enqueued event has been
	// emitted by the time the snapshot is taken; any later enqueue delivers
	// synchronously through the stopped-batcher path.
	a.stopEventBatcher()
	var out []session.HitlResolvedData
	for _, ev := range flattenEmittedBatches(t, rec.snapshot()) {
		if ev.Name != "session:sess-1:hitl_resolved" {
			continue
		}
		if len(ev.Data) != 1 {
			t.Fatalf("hitl_resolved carries %d data args, want 1", len(ev.Data))
		}
		data, ok := ev.Data[0].(session.HitlResolvedData)
		if !ok {
			t.Fatalf("hitl_resolved payload type = %T, want HitlResolvedData", ev.Data[0])
		}
		out = append(out, data)
	}
	return out
}

// assertSingleResolved asserts exactly one hitl_resolved event with the given
// kind/id/decision.
func assertSingleResolved(t *testing.T, a *App, rec *emitRecorder, kind session.HitlResolvedKind, id, decision string) {
	t.Helper()
	events := hitlResolvedEvents(t, a, rec)
	if len(events) != 1 {
		t.Fatalf("got %d hitl_resolved events, want 1: %+v", len(events), events)
	}
	got := events[0]
	if got.Kind != kind {
		t.Errorf("kind = %q, want %q", got.Kind, kind)
	}
	if got.ID != id {
		t.Errorf("id = %q, want %q", got.ID, id)
	}
	if got.Decision != decision {
		t.Errorf("decision = %q, want %q", got.Decision, decision)
	}
}

// --- emitHitlResolved guard ---

func TestEmitHitlResolved_NoTransportSkipsQuietly(t *testing.T) {
	// A bare &App{} has neither wailsEmit nor a wails context: the guard must
	// skip the emit without panicking (unit tests construct bare Apps; the
	// skip is logged at Debug, below the default slog level, so the test
	// output stays clean).
	a := &App{}
	a.emitHitlResolved("sess-1", session.HitlResolvedToolConfirm, "conf-1", "allow_once")
}

// --- tool_confirm ---

func TestHitlResolved_ToolConfirmAllowOnce(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("conf-1", &pendingConfirmData{
		ch:        ch,
		sessionID: "sess-1",
		toolName:  "bash_exec",
	})
	t.Cleanup(func() { a.pendingConfirmations.Delete("conf-1") })

	a.handleToolConfirmResponse(map[string]any{"confirm_id": "conf-1", "decision": "allow_once"}, silentLogger())

	assertSingleResolved(t, a, rec, session.HitlResolvedToolConfirm, "conf-1", "allow_once")
	if resp := <-ch; resp != sdktools.ConfirmAllowOnce {
		t.Errorf("channel decision = %v, want ConfirmAllowOnce", resp)
	}
}

func TestHitlResolved_ToolConfirmDeny(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("conf-2", &pendingConfirmData{
		ch:        ch,
		sessionID: "sess-1",
		toolName:  "edit_file",
	})
	t.Cleanup(func() { a.pendingConfirmations.Delete("conf-2") })

	a.handleToolConfirmResponse(map[string]any{"confirm_id": "conf-2", "decision": "deny"}, silentLogger())

	assertSingleResolved(t, a, rec, session.HitlResolvedToolConfirm, "conf-2", "deny")
}

func TestHitlResolved_ToolConfirmFirstResponderWins(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan sdktools.ConfirmationResponse, 1)
	a.pendingConfirmations.Store("conf-3", &pendingConfirmData{
		ch:        ch,
		sessionID: "sess-1",
		toolName:  "bash_exec",
	})
	t.Cleanup(func() { a.pendingConfirmations.Delete("conf-3") })

	// Two surfaces answer the same prompt: the FIRST response wins, and the
	// broadcast must carry the winner's decision so the losing surface
	// converges on what actually drove the agent. The late answer finds no
	// pending entry and emits nothing.
	a.handleToolConfirmResponse(map[string]any{"confirm_id": "conf-3", "decision": "allow_once"}, silentLogger())
	a.handleToolConfirmResponse(map[string]any{"confirm_id": "conf-3", "decision": "deny"}, silentLogger())

	events := hitlResolvedEvents(t, a, rec)
	if len(events) != 1 {
		t.Fatalf("got %d hitl_resolved events for two responses, want 1: %+v", len(events), events)
	}
	if events[0].Decision != "allow_once" {
		t.Errorf("decision = %q, want the first responder's allow_once", events[0].Decision)
	}
}

// --- ask_user ---

func TestHitlResolved_AskUser(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan coretools.AskUserResponse, 1)
	a.pendingAskUser.Store("au-1", &pendingAskUserEntry{
		ch:        ch,
		sessionID: "sess-1",
		payload:   session.AskUserPayload{RequestID: "au-1"},
	})
	t.Cleanup(func() { a.pendingAskUser.Delete("au-1") })

	a.handleAskUserResponse(map[string]any{"request_id": "au-1", "answers": []any{}}, silentLogger())

	// The answers are agent-owned payload, not UI state: the event settles
	// the card with an empty decision.
	assertSingleResolved(t, a, rec, session.HitlResolvedAskUser, "au-1", "")
}

// --- step_limit ---

func TestHitlResolved_StepLimitDeny(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan agent.StepLimitResponse, 1)
	a.pendingStepLimit.Store("sl-1", &pendingStepLimitEntry{
		ch:        ch,
		sessionID: "sess-1",
		payload:   session.StepLimitPayload{RequestID: "sl-1", CurrentStep: 10, MaxSteps: 10},
	})
	t.Cleanup(func() { a.pendingStepLimit.Delete("sl-1") })

	a.handleStepLimitResponse(map[string]any{"request_id": "sl-1", "response": "deny"}, silentLogger())

	assertSingleResolved(t, a, rec, session.HitlResolvedStepLimit, "sl-1", "deny")
	if resp := <-ch; resp != agent.StepLimitDeny {
		t.Errorf("channel decision = %v, want StepLimitDeny", resp)
	}
}

// --- plan_review ---

func TestHitlResolved_PlanApproval(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan planApprovalResponse, 1)
	a.pendingPlanApprovals.Store("pa-1", &pendingPlanApprovalEntry{
		ch:        ch,
		sessionID: "sess-1",
		payload:   session.PlanApprovalPayload{RequestID: "pa-1"},
	})
	t.Cleanup(func() { a.pendingPlanApprovals.Delete("pa-1") })

	a.handlePlanApprovalResponse(map[string]any{"request_id": "pa-1", "decision": "approve"}, silentLogger())

	assertSingleResolved(t, a, rec, session.HitlResolvedPlanReview, "pa-1", "approve")
}

// --- goal_proposal (shared resolver: covers the event AND the RPC path) ---

func TestHitlResolved_GoalProposalViaSharedResolver(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("gp-1", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-1",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("gp-1") })

	if !a.resolveGoalProposal("gp-1", "approve", "cond", "ver", "executable") {
		t.Fatal("resolveGoalProposal returned false for a pending proposal")
	}

	assertSingleResolved(t, a, rec, session.HitlResolvedGoalProposal, "gp-1", "approve")
}

// --- goal_suggestion (shared resolver: covers the event AND the RPC path) ---

func TestHitlResolved_GoalSuggestionViaSharedResolver(t *testing.T) {
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan goalSuggestionResponse, 1)
	a.pendingGoalSuggestions.Store("gs-1", &pendingGoalSuggestionEntry{
		ch:        ch,
		sessionID: "sess-1",
	})
	t.Cleanup(func() { a.pendingGoalSuggestions.Delete("gs-1") })

	if !a.resolveGoalSuggestion("gs-1", "accept") {
		t.Fatal("resolveGoalSuggestion returned false for a pending suggestion")
	}

	assertSingleResolved(t, a, rec, session.HitlResolvedGoalSuggestion, "gs-1", "accept")
}

// --- dropped response (full channel) ---

func TestHitlResolved_DroppedResponseEmitsNothing(t *testing.T) {
	// A response dropped on a full channel never became the effective
	// decision: the pending entry is cleaned up either way, but no
	// hitl_resolved event may broadcast a decision the agent never saw.
	a := &App{}
	rec := &emitRecorder{}
	a.wailsEmit = rec.emit

	ch := make(chan sdktools.ConfirmationResponse, 1)
	ch <- sdktools.ConfirmDeny // no receiver: the buffer is already full
	a.pendingConfirmations.Store("conf-4", &pendingConfirmData{
		ch:        ch,
		sessionID: "sess-1",
		toolName:  "bash_exec",
	})
	t.Cleanup(func() { a.pendingConfirmations.Delete("conf-4") })

	a.handleToolConfirmResponse(map[string]any{"confirm_id": "conf-4", "decision": "allow_once"}, silentLogger())

	if events := hitlResolvedEvents(t, a, rec); len(events) != 0 {
		t.Fatalf("got %d hitl_resolved events for a dropped response, want 0: %+v", len(events), events)
	}
	if _, ok := a.pendingConfirmations.Load("conf-4"); ok {
		t.Error("pending entry must still be cleaned up on the drop path")
	}
}

// --- immediate flush ---

func TestHitlResolved_IsImmediateFlushEvent(t *testing.T) {
	// A settled prompt retires a card that was blocking a task: like the
	// prompts themselves, the settlement must not wait for the ~16ms batch
	// tick or it reads as a ghost card.
	if !backend.IsImmediateFlushEvent("hitl_resolved") {
		t.Error("hitl_resolved must force an immediate flush")
	}
}
