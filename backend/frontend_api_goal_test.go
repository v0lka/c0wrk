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
	f.app.manager.SetGoalProposalResolver(func(_, decision, cond, verify, mode string) bool {
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

// TestCancelGoal_DelegatesToResolver verifies CancelGoal forwards decision=cancel.
func TestCancelGoal_DelegatesToResolver(t *testing.T) {
	f := goalTestApp(t)
	var gotDecision string
	f.app.manager.SetGoalProposalResolver(func(_, decision, _, _, _ string) bool {
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
}

// TestGoalRPCs_NoManagerReturnsError verifies the guard when the app/manager
// isn't initialized.
func TestGoalRPCs_NoManagerReturnsError(t *testing.T) {
	f := &FrontendAPI{} // no app
	for _, err := range []error{
		f.ConfirmGoal("s", "r", "c", "v", "executable"),
		f.CancelGoal("s", "r"),
	} {
		if err == nil {
			t.Error("expected error when manager is not initialized")
		}
	}
}
