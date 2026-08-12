package session

import (
	"context"
	"errors"
	"testing"
)

// TestCancelTask_ErrNoActiveTaskIsSentinel verifies the refactor that turned
// the literal error into the exported ErrNoActiveTask sentinel, so callers
// can match it via errors.Is.
func TestCancelTask_ErrNoActiveTaskIsSentinel(t *testing.T) {
	manager, _, _ := testManager(t)

	sess := &Session{ID: "sess-idle"}
	manager.mu.Lock()
	manager.sessions["sess-idle"] = sess
	manager.mu.Unlock()

	err := manager.CancelTask("sess-idle")
	if !errors.Is(err, ErrNoActiveTask) {
		t.Errorf("CancelTask on idle session: err = %v, want errors.Is(ErrNoActiveTask)", err)
	}
}

// --- Goal proposal resolver tests ---

// TestResolveGoalProposal_NoResolverWired verifies that without a resolver
// installed, ResolveGoalProposal returns false (no resolution happened).
func TestResolveGoalProposal_NoResolverWired(t *testing.T) {
	manager, _, _ := testManager(t)
	if manager.ResolveGoalProposal("req-1", "approve", "c", "v", "executable") {
		t.Error("ResolveGoalProposal returned true with no resolver wired")
	}
}

// TestResolveGoalProposal_ForwardsDecision verifies the resolver callback is
// invoked with the exact arguments for an approve decision.
func TestResolveGoalProposal_ForwardsDecision(t *testing.T) {
	manager, _, _ := testManager(t)

	var gotReq, gotDecision, gotCond, gotVerify, gotMode string
	manager.SetGoalProposalResolver(func(req, decision, cond, verify, mode string) bool {
		gotReq, gotDecision, gotCond, gotVerify, gotMode = req, decision, cond, verify, mode
		return true
	})

	if !manager.ResolveGoalProposal("req-9", "approve", "cond", "ver", "executable") {
		t.Fatal("expected resolver to return true")
	}
	if gotReq != "req-9" || gotDecision != "approve" || gotCond != "cond" || gotVerify != "ver" || gotMode != "executable" {
		t.Errorf("resolver received (%q,%q,%q,%q,%q), want (req-9,approve,cond,ver,executable)", gotReq, gotDecision, gotCond, gotVerify, gotMode)
	}
}

// TestResolveGoalProposal_ForwardsVerificationMode verifies the approve path
// forwards the user's chosen verification mode through the resolver so the
// derivation agent's GoalState reflects the sign-off edit.
func TestResolveGoalProposal_ForwardsVerificationMode(t *testing.T) {
	manager, _, _ := testManager(t)

	var gotDecision, gotMode string
	manager.SetGoalProposalResolver(func(_, decision, _, _, mode string) bool {
		gotDecision, gotMode = decision, mode
		return true
	})

	if !manager.ResolveGoalProposal("req-vm", "approve", "cond", "ver", "re_derivation") {
		t.Fatal("expected resolver to return true")
	}
	if gotDecision != "approve" || gotMode != "re_derivation" {
		t.Errorf("resolver got decision=%q mode=%q, want (approve,re_derivation)", gotDecision, gotMode)
	}
}

// --- PauseSession / ResumeSession tests ---

// TestPauseSession_NilOrchestratorReturnsError verifies PauseSession finds an
// in-memory session but returns an error when the session has no orchestrator
// (the test factory returns nil orchestrators).
func TestPauseSession_NilOrchestratorReturnsError(t *testing.T) {
	manager, _, _ := testManager(t)

	sess := &Session{ID: "sess-pause"}
	manager.mu.Lock()
	manager.sessions["sess-pause"] = sess
	manager.mu.Unlock()

	if err := manager.PauseSession("sess-pause"); err == nil {
		t.Error("expected error for nil orchestrator, got nil")
	}
}

// TestPauseSession_UnknownSessionReturnsError verifies PauseSession returns an
// error for a session that doesn't exist.
func TestPauseSession_UnknownSessionReturnsError(t *testing.T) {
	manager, _, _ := testManager(t)
	if err := manager.PauseSession("does-not-exist"); err == nil {
		t.Error("expected error for unknown session, got nil")
	}
}

// TestResumeSession_NoTaskStoreReturnsNil verifies ResumeSession delegates to
// ResumeTask, which returns nil when there is no resumable task (no task store
// configured).
func TestResumeSession_NoTaskStoreReturnsNil(t *testing.T) {
	manager, _, _ := testManager(t)
	if err := manager.ResumeSession(context.Background(), "sess-resume", "", "", "nudge"); err != nil {
		t.Errorf("ResumeSession with no task store should return nil, got %v", err)
	}
}
