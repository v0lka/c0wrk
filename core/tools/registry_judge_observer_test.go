package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// observerRecorder collects the judge phases a JudgeObserver receives.
type observerRecorder struct {
	phases []JudgePhase
	tools  []string
}

func (o *observerRecorder) observe(_ context.Context, phase JudgePhase, toolName string) {
	o.phases = append(o.phases, phase)
	o.tools = append(o.tools, toolName)
}

// newSmartApproveRegistry builds a registry with a user_confirm tool, a
// scripted strict judge, smart approve enabled, and a confirm func that
// records whether the manual-confirmation path was reached.
func newSmartApproveRegistry(t *testing.T, judgeResponse string, judgeErr error) (*ToolRegistry, *observerRecorder, *bool) {
	t.Helper()
	registry := NewToolRegistry()
	registry.Register(newMockTool("mutating", "A mutating tool"))
	registry.SetSmartApprove(true)
	judge, _ := newStrictJudge(judgeResponse, judgeErr)
	registry.SetJudge(judge)

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})

	rec := &observerRecorder{}
	registry.SetJudgeObserver(rec.observe)
	return registry, rec, &confirmCalled
}

// TestSmartApproveObserverPhases verifies the observer fires in
// started/finished pairs around the strict judge's LLM call when Smart
// Approve evaluates an escalated call — the hook that lets the UI show an
// honest "judge is working" status while no confirmation card exists yet.
func TestSmartApproveObserverPhases(t *testing.T) {
	registry, rec, confirmCalled := newSmartApproveRegistry(t, "VERDICT: ALLOW\nREASON: benign test command", nil)

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.phases) != 2 || rec.phases[0] != JudgePhaseStarted || rec.phases[1] != JudgePhaseFinished {
		t.Fatalf("expected [started finished], got %v", rec.phases)
	}
	for _, tool := range rec.tools {
		if tool != "mutating" {
			t.Errorf("observer tool = %q, want %q", tool, "mutating")
		}
	}
	// Strict ALLOW executes without UI — no manual confirmation.
	if *confirmCalled {
		t.Error("strict ALLOW must not reach the manual confirmation path")
	}
}

// TestSmartApproveObserverPhasesOnConfirmVerdict verifies the pair also fires
// when the verdict falls back to manual confirmation (CONFIRM verdict).
func TestSmartApproveObserverPhasesOnConfirmVerdict(t *testing.T) {
	registry, rec, confirmCalled := newSmartApproveRegistry(t, "VERDICT: CONFIRM\nREASON: needs human eyes", nil)

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.phases) != 2 || rec.phases[0] != JudgePhaseStarted || rec.phases[1] != JudgePhaseFinished {
		t.Fatalf("expected [started finished] around a CONFIRM verdict, got %v", rec.phases)
	}
	if !*confirmCalled {
		t.Error("CONFIRM verdict must fall back to manual confirmation")
	}
}

// TestSmartApproveObserverPhasesOnJudgeError verifies the finished phase
// fires even when the judge call itself errors (the verdict degrades to
// CONFIRM, but the observer pair must stay balanced so a "judge working"
// status never sticks).
func TestSmartApproveObserverPhasesOnJudgeError(t *testing.T) {
	registry, rec, _ := newSmartApproveRegistry(t, "", errors.New("llm unavailable"))

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.phases) != 2 || rec.phases[1] != JudgePhaseFinished {
		t.Fatalf("expected finished phase even on judge error, got %v", rec.phases)
	}
}

// TestSmartApproveObserverNotInvokedWithoutSmartApprove verifies the observer
// stays silent when Smart Approve is off — the plain confirm path never runs
// the strict judge.
func TestSmartApproveObserverNotInvokedWithoutSmartApprove(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(newMockTool("mutating", "A mutating tool"))
	judge, _ := newStrictJudge("VERDICT: ALLOW\nREASON: benign", nil)
	registry.SetJudge(judge)
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmDeny, nil
	})
	rec := &observerRecorder{}
	registry.SetJudgeObserver(rec.observe)

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.phases) != 0 {
		t.Errorf("observer must not fire when smart approve is off, got %v", rec.phases)
	}
}

// TestClonePreservesJudgeObserver verifies session clones inherit the
// observer — the observer is wired once on the shared registry in
// backend/application.go and every per-session registry clone must report
// judge phases for its own session.
func TestClonePreservesJudgeObserver(t *testing.T) {
	parent := NewToolRegistry()
	parent.SetSmartApprove(true)
	parent.Register(newMockTool("mutating", "A mutating tool"))
	judge, _ := newStrictJudge("VERDICT: ALLOW\nREASON: benign", nil)
	parent.SetJudge(judge)
	rec := &observerRecorder{}
	parent.SetJudgeObserver(rec.observe)

	child := parent.Clone()
	if _, err := child.Execute(context.Background(), "mutating", json.RawMessage(`{"data":"x"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rec.phases) != 2 || rec.phases[0] != JudgePhaseStarted || rec.phases[1] != JudgePhaseFinished {
		t.Fatalf("clone must inherit the observer (expected [started finished]), got %v", rec.phases)
	}
}
