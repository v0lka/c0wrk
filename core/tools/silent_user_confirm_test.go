package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// ── Silent-mode user_confirm sub-policy (tool_confirm refinement) ──────────
//
// A fail-closed (CONFIRM) tool_confirm terminal — a spoken judge CONFIRM, a
// missing judge, a judge error/timeout, or an unparseable verdict — is refined
// by security.silent_mode.user_confirm:
//
//   - "confirm"  — execute the call unattended, audited as an allow (policy
//                  "user_confirm");
//   - "deny"     — the fail-closed auto-denial (the default, unchanged);
//   - "escalate" — fall back to the blocking confirmation card.
//
// A deliberate judge DENY is never governed by this policy.

// newSilentUserConfirmRegistry builds a silent-mode registry with the given
// tool_confirm mode and user_confirm refinement, an optional strict judge, and
// a recording autonomy-decision observer.
func newSilentUserConfirmRegistry(toolConfirm, userConfirm, judgeResponse string, judgeErr error, setJudge bool) (*ToolRegistry, *autonomyDecisionRecorder) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.ApplySecurityState(registry.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{
		ToolConfirm: toolConfirm,
		UserConfirm: userConfirm,
	})
	if setJudge {
		judge, _ := newStrictJudge(judgeResponse, judgeErr)
		registry.SetJudge(judge)
	}
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)
	return registry, rec
}

// TestSilentUserConfirm_ConfirmExecutesFailClosedTerminals pins that every
// fail-closed CONFIRM cause — a spoken CONFIRM, a missing judge, a judge
// error, a timeout, an unparseable verdict, and (in allow mode) a hard reason
// whose judge CONFIRMs — executes unattended under user_confirm=confirm, with
// a tool_confirm autonomy_decision (mode silent, policy user_confirm, verdict
// allow) and no confirmation card.
func TestSilentUserConfirm_ConfirmExecutesFailClosedTerminals(t *testing.T) {
	tests := []struct {
		name        string
		toolConfirm string
		hardReason  string
		code        sdktools.JudgeReasonCode
		judgeResp   string
		judgeErr    error
		setJudge    bool
	}{
		{name: "spoken CONFIRM", toolConfirm: SilentToolConfirmJudge, judgeResp: "VERDICT: CONFIRM\nREASON: destructive write", setJudge: true},
		{name: "missing judge", toolConfirm: SilentToolConfirmJudge},
		{name: "judge error", toolConfirm: SilentToolConfirmJudge, judgeErr: errors.New("provider down"), setJudge: true},
		{name: "judge timeout", toolConfirm: SilentToolConfirmJudge, judgeErr: context.DeadlineExceeded, setJudge: true},
		{name: "judge unparseable", toolConfirm: SilentToolConfirmJudge, judgeResp: "probably fine", setJudge: true},
		{
			name: "allow mode with a hard reason whose judge CONFIRMs", toolConfirm: SilentToolConfirmAllow,
			hardReason: "symlink escapes the session roots", code: sdktools.ReasonCodeSymlinkEscape,
			judgeResp: "VERDICT: CONFIRM\nREASON: escape confirmed", setJudge: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, rec := newSilentUserConfirmRegistry(tt.toolConfirm, SilentUserConfirmConfirm, tt.judgeResp, tt.judgeErr, tt.setJudge)
			confirmCalls := 0
			registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
				confirmCalls++
				return sdktools.ConfirmAllowOnce, nil
			})

			name := "mutating"
			if tt.hardReason != "" {
				registry.Register(newMockHardJudgerTool("esc_tool", tt.hardReason, tt.code))
				name = "esc_tool"
			} else {
				registry.Register(newMockTool("mutating", "mutates"))
			}

			res, err := registry.Execute(context.Background(), name, json.RawMessage(`{"input":"hello"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.IsError {
				t.Fatalf("user_confirm=confirm must execute a fail-closed CONFIRM unattended, got %q", res.Content)
			}
			if confirmCalls != 0 {
				t.Errorf("user_confirm=confirm must never open a confirmation card (calls = %d)", confirmCalls)
			}
			if len(rec.decisions) != 1 {
				t.Fatalf("autonomy decisions = %d, want exactly 1: %+v", len(rec.decisions), rec.decisions)
			}
			d := rec.decisions[0]
			if d.Kind != autonomyDecisionKindToolConfirm {
				t.Errorf("Kind = %q, want %q", d.Kind, autonomyDecisionKindToolConfirm)
			}
			if d.Mode != AutonomyModeSilent {
				t.Errorf("Mode = %q, want %q", d.Mode, AutonomyModeSilent)
			}
			if d.Policy != autonomyDecisionPolicyUserConfirm {
				t.Errorf("Policy = %q, want %q", d.Policy, autonomyDecisionPolicyUserConfirm)
			}
			if d.Verdict != autonomyDecisionVerdictAllow {
				t.Errorf("Verdict = %q, want %q", d.Verdict, autonomyDecisionVerdictAllow)
			}
			if d.Tool != name {
				t.Errorf("Tool = %q, want %q", d.Tool, name)
			}
			if !strings.Contains(d.Justification, SilentUserConfirmConfirm) {
				t.Errorf("Justification = %q, want it to name the user_confirm=confirm policy", d.Justification)
			}
		})
	}
}

// TestSilentUserConfirm_DefaultAutoDeniesUnchanged pins that the "deny" default
// — the empty value and any unrecognized value included — keeps today's
// fail-closed auto-denial byte-for-byte: the silent denial result, the
// fail-closed justification prefix, the tool_confirm mode as the deciding
// policy, and no confirmation card.
func TestSilentUserConfirm_DefaultAutoDeniesUnchanged(t *testing.T) {
	tests := []struct {
		name        string
		userConfirm string
	}{
		{name: "empty (default)", userConfirm: ""},
		{name: "explicit deny", userConfirm: SilentUserConfirmDeny},
		{name: "unrecognized value", userConfirm: "unrecognized-mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, rec := newSilentUserConfirmRegistry(SilentToolConfirmJudge, tt.userConfirm, "VERDICT: CONFIRM\nREASON: destructive write", nil, true)
			confirmCalls := 0
			registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
				confirmCalls++
				return sdktools.ConfirmAllowOnce, nil
			})
			registry.Register(newMockTool("mutating", "mutates"))

			res, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !res.IsError {
				t.Fatalf("the default/deny user_confirm must fail closed to a denial, got %q", res.Content)
			}
			if !strings.HasPrefix(res.Content, "Automatic denial (silent mode): ") {
				t.Errorf("denial content = %q, want the silent auto-denial prefix (identical to today)", res.Content)
			}
			if confirmCalls != 0 {
				t.Errorf("the fail-closed default must not open a card (calls = %d)", confirmCalls)
			}
			if len(rec.decisions) != 1 {
				t.Fatalf("decisions = %d, want exactly 1", len(rec.decisions))
			}
			d := rec.decisions[0]
			if d.Verdict != autonomyDecisionVerdictDeny {
				t.Errorf("Verdict = %q, want deny", d.Verdict)
			}
			if d.Policy != SilentToolConfirmJudge {
				t.Errorf("Policy = %q, want %q (the tool_confirm mode decides the fail-closed default)", d.Policy, SilentToolConfirmJudge)
			}
			if !strings.HasPrefix(d.Justification, "fail-closed auto-denial") {
				t.Errorf("Justification = %q, want the fail-closed prefix", d.Justification)
			}
		})
	}
}

// TestSilentUserConfirm_EscalateOpensCard pins that user_confirm=escalate falls
// back to the blocking confirmation card: the confirmation function is invoked
// (the card), no autonomy_decision is emitted (a human decides, not the
// registry), the tool does not run unattended, and the advisory Ask Agent
// action is disabled whenever the strict judge actually ran — a spoken CONFIRM
// (hard reason or not) — but left available when the judge was missing or
// failed (no judgment exists to duplicate), matching the assisted path.
func TestSilentUserConfirm_EscalateOpensCard(t *testing.T) {
	tests := []struct {
		name             string
		judgeResp        string
		judgeErr         error
		setJudge         bool
		hardReason       string
		code             sdktools.JudgeReasonCode
		wantReasoning    string
		wantDisableJudge bool
	}{
		{name: "spoken CONFIRM disables the advisory judge", judgeResp: "VERDICT: CONFIRM\nREASON: needs a human", setJudge: true, wantReasoning: "needs a human", wantDisableJudge: true},
		{name: "hard reason disables the advisory judge", judgeResp: "VERDICT: CONFIRM\nREASON: needs a human", setJudge: true, hardReason: "symlink escapes the session roots", code: sdktools.ReasonCodeSymlinkEscape, wantReasoning: "needs a human", wantDisableJudge: true},
		{name: "missing judge keeps the advisory judge", setJudge: false, wantReasoning: "Strict judge is unavailable", wantDisableJudge: false},
		{name: "judge failure keeps the advisory judge", judgeErr: errors.New("provider down"), setJudge: true, wantReasoning: "Strict judge evaluation failed", wantDisableJudge: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, rec := newSilentUserConfirmRegistry(SilentToolConfirmJudge, SilentUserConfirmEscalate, tt.judgeResp, tt.judgeErr, tt.setJudge)
			var gotReq sdktools.ConfirmationRequest
			calls := 0
			registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
				calls++
				gotReq = req
				return sdktools.ConfirmDeny, nil
			})

			name := "mutating"
			if tt.hardReason != "" {
				registry.Register(newMockHardJudgerTool("esc_tool", tt.hardReason, tt.code))
				name = "esc_tool"
			} else {
				registry.Register(newMockTool("mutating", "mutates"))
			}

			res, err := registry.Execute(context.Background(), name, json.RawMessage(`{"input":"x"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if calls != 1 {
				t.Fatalf("escalate must invoke the confirmation function exactly once (the card), calls = %d", calls)
			}
			if len(rec.decisions) != 0 {
				t.Errorf("escalate defers to a human — no autonomy_decision must be emitted, got %+v", rec.decisions)
			}
			if !res.IsError {
				t.Error("the user denied the card, so the tool must not run")
			}
			if gotReq.DisableJudge != tt.wantDisableJudge {
				t.Errorf("ConfirmationRequest.DisableJudge = %v, want %v", gotReq.DisableJudge, tt.wantDisableJudge)
			}
			if !strings.Contains(gotReq.JudgeReasoning, tt.wantReasoning) {
				t.Errorf("card reasoning = %q, want it to contain %q", gotReq.JudgeReasoning, tt.wantReasoning)
			}
		})
	}
}

// TestSilentUserConfirm_JudgeDenyNeverGovernedByPolicy pins that a deliberate
// strict-judge DENY auto-denies under EVERY user_confirm mode: it is never
// auto-approved ("confirm") and never escalated to a card ("escalate"). The
// emitted decision names the judge (the tool_confirm mode) as the decider.
func TestSilentUserConfirm_JudgeDenyNeverGovernedByPolicy(t *testing.T) {
	for _, userConfirm := range []string{"", SilentUserConfirmConfirm, SilentUserConfirmDeny, SilentUserConfirmEscalate} {
		t.Run("user_confirm="+userConfirm, func(t *testing.T) {
			registry, rec := newSilentUserConfirmRegistry(SilentToolConfirmJudge, userConfirm, "VERDICT: DENY\nREASON: proven exfiltration flow", nil, true)
			calls := 0
			registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
				calls++
				return sdktools.ConfirmAllowOnce, nil
			})
			registry.Register(newMockTool("mutating", "mutates"))

			res, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !res.IsError {
				t.Fatalf("a judge DENY must auto-deny under every user_confirm mode, got %q", res.Content)
			}
			if calls != 0 {
				t.Errorf("a judge DENY must never open a card (calls = %d)", calls)
			}
			if len(rec.decisions) != 1 {
				t.Fatalf("decisions = %d, want exactly 1", len(rec.decisions))
			}
			d := rec.decisions[0]
			if d.Verdict != autonomyDecisionVerdictDeny {
				t.Errorf("Verdict = %q, want deny", d.Verdict)
			}
			if d.Policy != SilentToolConfirmJudge {
				t.Errorf("Policy = %q, want %q (the judge is the decider on a DENY)", d.Policy, SilentToolConfirmJudge)
			}
			if !strings.HasPrefix(d.Justification, "strict judge verdict DENY: ") {
				t.Errorf("Justification = %q, want the judge-DENY tag", d.Justification)
			}
		})
	}
}
