package desktop

import (
	"log/slog"
	"testing"
)

// TestResolveGoalProposal_HappyPath verifies a registered pending proposal is
// resolved: the response is delivered to the channel and the entry is deleted.
func TestResolveGoalProposal_HappyPath(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-1", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-1",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("req-1") })

	ok := a.resolveGoalProposal("req-1", "approve", "cond", "ver", "executable")
	if !ok {
		t.Fatal("resolveGoalProposal returned false for a pending proposal")
	}
	select {
	case resp := <-ch:
		if resp.Decision != "approve" || resp.Condition != "cond" || resp.Verify != "ver" || resp.VerificationMode != "executable" {
			t.Errorf("channel received %+v, want {approve,cond,ver,executable,}", resp)
		}
	default:
		t.Error("response was not delivered to the channel")
	}
	// Entry must be deleted after resolution.
	if _, stillThere := a.pendingGoalProposals.Load("req-1"); stillThere {
		t.Error("pending proposal entry was not deleted after resolution")
	}
}

func TestResolveGoalProposal_EmptyRequestID(t *testing.T) {
	a := &App{}
	if a.resolveGoalProposal("", "approve", "c", "v", "executable") {
		t.Error("expected false for empty requestID")
	}
}

func TestResolveGoalProposal_UnknownRequestID(t *testing.T) {
	a := &App{}
	if a.resolveGoalProposal("nonexistent", "approve", "c", "v", "executable") {
		t.Error("expected false for unknown requestID")
	}
}

// TestHandleGoalProposalResponse_Dispatches verifies a valid payload dispatches
// to resolveGoalProposal.
func TestHandleGoalProposalResponse_Dispatches(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-3", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-3",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("req-3") })

	a.handleGoalProposalResponse(map[string]any{
		"request_id": "req-3",
		"decision":   "approve",
		"condition":  "c",
		"verify":     "v",
	}, slog.Default().WithGroup("test"))

	resp := <-ch
	if resp.Decision != "approve" {
		t.Errorf("expected approve dispatched, got %q", resp.Decision)
	}
}

// TestHandleGoalProposalResponse_MissingRequestID verifies a payload without a
// request_id is skipped (the handler returns without dispatching).
func TestHandleGoalProposalResponse_MissingRequestID(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-4", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-4",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("req-4") })

	// No request_id in the payload — should be a no-op (channel stays empty).
	a.handleGoalProposalResponse(map[string]any{
		"decision": "approve",
	}, slog.Default().WithGroup("test"))

	select {
	case <-ch:
		t.Error("expected no dispatch for payload missing request_id")
	default:
		// pass: channel empty
	}
}

// TestHandleGoalProposalResponse_MissingDecision verifies a payload with a
// request_id but no decision is skipped.
func TestHandleGoalProposalResponse_MissingDecision(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-5", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-5",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("req-5") })

	a.handleGoalProposalResponse(map[string]any{
		"request_id": "req-5",
	}, slog.Default().WithGroup("test"))

	select {
	case <-ch:
		t.Error("expected no dispatch for payload missing decision")
	default:
		// pass: channel empty
	}
}

// TestHandleGoalProposalResponse_ForwardsVerificationMode verifies the
// event-based resolution path threads the user's verification_mode choice
// from the payload back into the goalProposalResponse so it round-trips into
// GoalState via the desktop adapter. This is the end-to-end acceptance check
// for step_4: the mode chosen at derivation reaches the frontend payload and
// the user's edit makes it back to the resolver.
func TestHandleGoalProposalResponse_ForwardsVerificationMode(t *testing.T) {
	a := &App{}
	ch := make(chan goalProposalResponse, 1)
	a.pendingGoalProposals.Store("req-vm", &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: "sess-vm",
	})
	t.Cleanup(func() { a.pendingGoalProposals.Delete("req-vm") })

	a.handleGoalProposalResponse(map[string]any{
		"request_id":        "req-vm",
		"decision":          "approve",
		"condition":         "edited cond",
		"verify":            "edited ver",
		"verification_mode": "re_derivation",
	}, slog.Default().WithGroup("test"))

	resp := <-ch
	if resp.Decision != "approve" {
		t.Errorf("expected approve dispatched, got %q", resp.Decision)
	}
	if resp.VerificationMode != "re_derivation" {
		t.Errorf("verification_mode = %q, want re_derivation", resp.VerificationMode)
	}
}
