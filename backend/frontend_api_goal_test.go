package backend

import (
	"io"
	"log/slog"
	"testing"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// goalTestApp builds a minimal Application + FrontendAPI wired with a real
// session.Manager (whose orchestrator factory returns nil — the goal RPCs
// don't execute tasks, they only resolve pending proposals). The manager's
// goal-proposal resolver can be set by the test to capture arguments.
func goalTestApp(t *testing.T) *FrontendAPI {
	t.Helper()
	emitFunc := func(session.Event) {}
	factory := func(coreEmitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}
	mgr := session.NewManager(factory, emitFunc, t.TempDir())
	app := &Application{manager: mgr}
	return &FrontendAPI{app: app}
}

// TestConfirmGoal_DelegatesToResolver verifies ConfirmGoal forwards the
// approve decision with the condition/verify/verification_mode values.
func TestConfirmGoal_DelegatesToResolver(t *testing.T) {
	f := goalTestApp(t)
	var gotDecision, gotCond, gotVerify, gotMode string
	f.app.manager.SetGoalProposalResolver(func(_, decision, cond, verify, mode, _ string) bool {
		gotDecision, gotCond, gotVerify, gotMode = decision, cond, verify, mode
		return true
	})
	if err := f.ConfirmGoal("s", "req-1", "cond", "ver", "executable"); err != nil {
		t.Fatalf("ConfirmGoal error = %v", err)
	}
	if gotDecision != "approve" || gotCond != "cond" || gotVerify != "ver" || gotMode != "executable" {
		t.Errorf("resolver got (%q,%q,%q,%q), want (approve,cond,ver,executable)", gotDecision, gotCond, gotVerify, gotMode)
	}
}

// TestClarifyGoal_DelegatesToResolver verifies the ClarifyGoal RPC forwards
// decision="clarify" with the clarification string. This is the backend half
// of Fix #5 — completing the clarification round-trip the UI needs.
func TestClarifyGoal_DelegatesToResolver(t *testing.T) {
	f := goalTestApp(t)
	var gotDecision, gotClarif string
	f.app.manager.SetGoalProposalResolver(func(_, decision, _, _, _, clarif string) bool {
		gotDecision, gotClarif = decision, clarif
		return true
	})
	if err := f.ClarifyGoal("s", "req-2", "which scope?"); err != nil {
		t.Fatalf("ClarifyGoal error = %v", err)
	}
	if gotDecision != "clarify" || gotClarif != "which scope?" {
		t.Errorf("resolver got (%q,%q), want (clarify,which scope?)", gotDecision, gotClarif)
	}
}

// TestCancelGoal_DelegatesToResolver verifies CancelGoal forwards decision=cancel.
func TestCancelGoal_DelegatesToResolver(t *testing.T) {
	f := goalTestApp(t)
	var gotDecision string
	f.app.manager.SetGoalProposalResolver(func(_, decision, _, _, _, _ string) bool {
		gotDecision = decision
		return true
	})
	if err := f.CancelGoal("s", "req-3"); err != nil {
		t.Fatalf("CancelGoal error = %v", err)
	}
	if gotDecision != "cancel" {
		t.Errorf("resolver decision = %q, want cancel", gotDecision)
	}
}

// TestGoalRPCs_NoResolverReturnsError verifies the goal RPCs surface an error
// when the resolver finds no pending proposal (returns false).
func TestGoalRPCs_NoResolverReturnsError(t *testing.T) {
	f := goalTestApp(t)
	// No resolver installed — ResolveGoalProposal returns false.
	if err := f.ConfirmGoal("s", "req-x", "c", "v", "executable"); err == nil {
		t.Error("ConfirmGoal expected error for unresolved proposal")
	}
	if err := f.CancelGoal("s", "req-x"); err == nil {
		t.Error("CancelGoal expected error for unresolved proposal")
	}
	if err := f.ClarifyGoal("s", "req-x", "q"); err == nil {
		t.Error("ClarifyGoal expected error for unresolved proposal")
	}
}

// TestGoalRPCs_EmptyRequestID verifies the validation guard.
func TestGoalRPCs_EmptyRequestID(t *testing.T) {
	f := goalTestApp(t)
	if err := f.ConfirmGoal("s", "", "c", "v", "executable"); err == nil {
		t.Error("ConfirmGoal with empty requestID should error")
	}
	if err := f.CancelGoal("s", ""); err == nil {
		t.Error("CancelGoal with empty requestID should error")
	}
	if err := f.ClarifyGoal("s", "", "q"); err == nil {
		t.Error("ClarifyGoal with empty requestID should error")
	}
}

// TestGoalRPCs_NoManagerReturnsError verifies the guard when the app/manager
// isn't initialized.
func TestGoalRPCs_NoManagerReturnsError(t *testing.T) {
	f := &FrontendAPI{} // no app
	for _, err := range []error{
		f.ConfirmGoal("s", "r", "c", "v", "executable"),
		f.CancelGoal("s", "r"),
		f.ClarifyGoal("s", "r", "q"),
	} {
		if err == nil {
			t.Error("expected error when manager is not initialized")
		}
	}
}

// TestGoalRPCs_PauseResumeClearDelegateToManager verifies the lifecycle-control
// RPCs (Pause/Resume/Clear) delegate to the session manager without panicking.
// A non-existent session yields an error from Pause (session not found), while
// Resume/Clear tolerate it (Resume with no task returns nil; Clear is a no-op).
func TestGoalRPCs_PauseResumeClearDelegateToManager(t *testing.T) {
	f := goalTestApp(t)

	// Pause delegates to manager.PauseGoal → getOrRestoreSession finds no
	// session (no session store configured) → "session not found".
	if err := f.PauseGoal("nonexistent"); err == nil {
		t.Error("PauseGoal expected error for non-existent session")
	}
	// Resume delegates to manager.ResumeGoal → ResumeTask, which needs a
	// resumable task; with no task store it returns nil (nothing to resume).
	if err := f.ResumeGoal("nonexistent", "", ""); err != nil {
		t.Errorf("ResumeGoal returned unexpected error (nil expected for no resumable task): %v", err)
	}
	// Clear delegates to manager.ClearGoal → CancelTask → getOrrestoreSession
	// finds no session → "session not found". This proves delegation works.
	if err := f.ClearGoal("nonexistent"); err == nil {
		t.Error("ClearGoal expected error for non-existent session")
	}
}
