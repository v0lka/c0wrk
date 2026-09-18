package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// autonomyDecisionRecorder captures the AutonomyDecision values the registry hands
// to its observer.
type autonomyDecisionRecorder struct {
	decisions []AutonomyDecision
}

func (r *autonomyDecisionRecorder) observe(_ context.Context, d AutonomyDecision) {
	r.decisions = append(r.decisions, d)
}

// newSilentRegistry builds a registry in silent mode with the given
// tool_confirm mode, an optional strict judge, and a recording autonomy-decision
// observer.
func newSilentRegistry(mode, judgeResponse string, judgeErr error, setJudge bool) (*ToolRegistry, *autonomyDecisionRecorder) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.ApplySecurityState(registry.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{ToolConfirm: mode})
	if setJudge {
		judge, _ := newStrictJudge(judgeResponse, judgeErr)
		registry.SetJudge(judge)
	}
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)
	return registry, rec
}

// TestAutonomyDecisionObserver_RecordsEveryToolConfirmDecision pins that EVERY
// automatic decision the silent terminal takes — an outright denial, an
// unattended execution, and a judge-decided allow/deny (including the two
// fail-closed causes) — is reported to the observer with a reconstructable
// verdict. This is the auditable receipt chain ASI10 requires: a gate a human
// would otherwise have answered must leave a trace.
func TestAutonomyDecisionObserver_RecordsEveryToolConfirmDecision(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		hardReason  string
		code        sdktools.JudgeReasonCode
		judgeResp   string
		judgeErr    error
		setJudge    bool
		wantVerdict string
		wantPolicy  string
		wantJustify string // substring of Justification
		wantToolErr bool
	}{
		{
			name:        "deny mode records a denial",
			mode:        SilentToolConfirmDeny,
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmDeny,
			wantJustify: "denied outright", wantToolErr: true,
		},
		{
			name:        "allow mode records an unattended execution",
			mode:        SilentToolConfirmAllow,
			wantVerdict: autonomyDecisionVerdictAllow, wantPolicy: SilentToolConfirmAllow,
			wantJustify: "ran unattended",
		},
		{
			name: "judge ALLOW records an allow carrying the judge reasoning",
			mode: SilentToolConfirmJudge, setJudge: true, judgeResp: "VERDICT: ALLOW\nREASON: safe and reversible",
			wantVerdict: autonomyDecisionVerdictAllow, wantPolicy: SilentToolConfirmJudge,
			wantJustify: "safe and reversible",
		},
		{
			name: "judge CONFIRM records a denial carrying the judge reasoning",
			mode: SilentToolConfirmJudge, setJudge: true, judgeResp: "VERDICT: CONFIRM\nREASON: destructive write",
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmJudge,
			wantJustify: "destructive write", wantToolErr: true,
		},
		{
			name: "judge DENY records a deliberate judge denial",
			mode: SilentToolConfirmJudge, setJudge: true, judgeResp: "VERDICT: DENY\nREASON: proven exfiltration flow",
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmJudge,
			wantJustify: "proven exfiltration flow", wantToolErr: true,
		},
		{
			name:        "missing judge records a fail-closed denial",
			mode:        SilentToolConfirmJudge,
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmJudge,
			wantJustify: "unavailable", wantToolErr: true,
		},
		{
			name: "judge error records a fail-closed denial",
			mode: SilentToolConfirmJudge, setJudge: true, judgeErr: errors.New("provider down"),
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmJudge,
			wantJustify: "failed", wantToolErr: true,
		},
		{
			name: "allow mode escalates a hard reason to the judge and records its denial",
			mode: SilentToolConfirmAllow, hardReason: "symlink escapes the session roots",
			code: sdktools.ReasonCodeSymlinkEscape, setJudge: true, judgeResp: "VERDICT: CONFIRM\nREASON: escape confirmed",
			wantVerdict: autonomyDecisionVerdictDeny, wantPolicy: SilentToolConfirmAllow,
			wantJustify: "escape confirmed", wantToolErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, rec := newSilentRegistry(tt.mode, tt.judgeResp, tt.judgeErr, tt.setJudge)
			name := "mutating"
			registry.Register(newMockTool("mutating", "mutates"))
			if tt.hardReason != "" {
				// A HARD-reason tool lives in an allow group so the escalation
				// reaches the silent terminal.
				registry.Register(newMockHardJudgerTool("esc_tool", tt.hardReason, tt.code))
				name = "esc_tool"
			}

			res, err := registry.Execute(context.Background(), name, json.RawMessage(`{"input":"x"}`))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if res.IsError != tt.wantToolErr {
				t.Errorf("tool result IsError = %v, want %v (%q)", res.IsError, tt.wantToolErr, res.Content)
			}
			if len(rec.decisions) != 1 {
				t.Fatalf("autonomy decisions = %d, want exactly 1: %+v", len(rec.decisions), rec.decisions)
			}
			d := rec.decisions[0]
			if d.Kind != autonomyDecisionKindToolConfirm {
				t.Errorf("Kind = %q, want %q", d.Kind, autonomyDecisionKindToolConfirm)
			}
			if d.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", d.Verdict, tt.wantVerdict)
			}
			if d.Mode != AutonomyModeSilent {
				t.Errorf("Mode = %q, want %q (the silent posture took the decision)", d.Mode, AutonomyModeSilent)
			}
			if d.Policy != tt.wantPolicy {
				t.Errorf("Policy = %q, want %q", d.Policy, tt.wantPolicy)
			}
			if d.Tool != name {
				t.Errorf("Tool = %q, want %q", d.Tool, name)
			}
			if d.Reason == "" {
				t.Error("Reason must carry why the call was gated (audit trail)")
			}
			if !strings.Contains(d.Justification, tt.wantJustify) {
				t.Errorf("Justification = %q, want it to contain %q", d.Justification, tt.wantJustify)
			}
		})
	}
}

// TestAutonomyDecision_DenyJustificationDistinguishesJudgeDenyFromFailClosed
// pins the audit-trail distinction between the two denial causes in silent
// mode: a deliberate strict-judge DENY (the judge positively assessed the
// call as dangerous) is tagged "strict judge verdict DENY", while the
// fail-closed outcomes (CONFIRM, a missing judge, an error/timeout, an
// unparseable verdict) carry the fail-closed prefix — an operator reading the
// trajectory must be able to tell WHO refused the call.
func TestAutonomyDecision_DenyJustificationDistinguishesJudgeDenyFromFailClosed(t *testing.T) {
	run := func(judgeResp string, setJudge bool, judgeErr error) string {
		t.Helper()
		registry, rec := newSilentRegistry(SilentToolConfirmJudge, judgeResp, judgeErr, setJudge)
		registry.Register(newMockTool("mutating", "mutates"))
		if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`)); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(rec.decisions) != 1 {
			t.Fatalf("decisions = %d, want exactly 1: %+v", len(rec.decisions), rec.decisions)
		}
		return rec.decisions[0].Justification
	}

	judgeDeny := run("VERDICT: DENY\nREASON: proven exfiltration", true, nil)
	if !strings.HasPrefix(judgeDeny, "strict judge verdict DENY: ") {
		t.Errorf("a deliberate judge DENY justification = %q, want the \"strict judge verdict DENY\" tag", judgeDeny)
	}

	failClosed := map[string]string{
		"missing judge": run("", false, nil),
		"judge error":   run("", true, errors.New("provider down")),
		"judge confirm": run("VERDICT: CONFIRM\nREASON: destructive", true, nil),
	}
	for name, justification := range failClosed {
		if !strings.HasPrefix(justification, "fail-closed auto-denial") {
			t.Errorf("%s: justification = %q, want the fail-closed prefix (distinct from a judge DENY)", name, justification)
		}
	}
}

// TestAutonomyDecisionObserver_NotInvokedWhenSilentOff verifies the observer is
// silent-mode-only: with the posture off, the ordinary interactive confirmation
// path runs and no automatic decision is recorded.
func TestAutonomyDecisionObserver_NotInvokedWhenSilentOff(t *testing.T) {
	registry := NewToolRegistry()
	setDefaultGroupPolicies(registry)
	registry.Register(newMockTool("mutating", "mutates"))
	judge, _ := newStrictJudge("VERDICT: ALLOW\nREASON: n/a", nil)
	registry.SetJudge(judge)
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		return sdktools.ConfirmAllowOnce, nil
	})

	if _, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(rec.decisions) != 0 {
		t.Errorf("silent mode off must record no autonomy decision, got %+v", rec.decisions)
	}
}

// TestAutonomyDecisionObserver_DenyGroupRecordsNothing pins that a gate which
// runs BEFORE the confirmation funnel (a deny-group block) is not reported as
// an automatic decision — the audit trail covers the gates silent mode actually
// answers, not the ones it never reaches.
func TestAutonomyDecisionObserver_DenyGroupRecordsNothing(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysDeny,
	})
	registry.ApplySecurityState(registry.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{ToolConfirm: SilentToolConfirmAllow})
	registry.Register(newMockTool("mutating", "mutates"))
	rec := &autonomyDecisionRecorder{}
	registry.SetAutonomyDecisionObserver(rec.observe)

	res, err := registry.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !res.IsError {
		t.Error("a deny group must stay blocked under silent mode")
	}
	if len(rec.decisions) != 0 {
		t.Errorf("a deny-group block precedes the funnel and must not be recorded: %+v", rec.decisions)
	}
}

// TestAutonomyDecisionObserver_ClonePreservesObserver verifies session clones
// inherit the observer — it is wired once on the shared builder registry and
// every per-session registry clone must report its own automatic decisions.
func TestAutonomyDecisionObserver_ClonePreservesObserver(t *testing.T) {
	parent := NewToolRegistry()
	setDefaultGroupPolicies(parent)
	parent.ApplySecurityState(parent.GroupPolicies(), false, AutonomyModeSilent, SilentModeState{ToolConfirm: SilentToolConfirmDeny})
	parent.Register(newMockTool("mutating", "mutates"))
	rec := &autonomyDecisionRecorder{}
	parent.SetAutonomyDecisionObserver(rec.observe)

	child := parent.Clone()
	if _, err := child.Execute(context.Background(), "mutating", json.RawMessage(`{"input":"x"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(rec.decisions) != 1 {
		t.Fatalf("session clone must inherit the autonomy-decision observer, decisions = %d", len(rec.decisions))
	}
	if rec.decisions[0].Kind != autonomyDecisionKindToolConfirm {
		t.Errorf("Kind = %q, want %q", rec.decisions[0].Kind, autonomyDecisionKindToolConfirm)
	}
}

// TestAutonomyDecision_JSONContract pins the wire shape of an
// AutonomyDecision: the host serializes it verbatim into the
// `autonomy_decision` session event, and the frontend rebuilds its notice
// from these keys.
func TestAutonomyDecision_JSONContract(t *testing.T) {
	d := AutonomyDecision{
		Kind:          autonomyDecisionKindToolConfirm,
		Mode:          AutonomyModeSilent,
		Policy:        SilentToolConfirmJudge,
		Verdict:       autonomyDecisionVerdictDeny,
		Tool:          "bash_exec",
		Source:        "core",
		Reason:        "runs a shell command",
		Justification: "ASI05: downloads and executes unverified code",
		Category:      "circuit_breaker",
		CurrentStep:   4,
		MaxSteps:      4,
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{
		`"kind":"tool_confirm"`, `"mode":"silent"`, `"policy":"judge"`, `"verdict":"deny"`, `"tool":"bash_exec"`,
		`"source":"core"`, `"reason":"runs a shell command"`,
		`"justification":"ASI05: downloads and executes unverified code"`,
		`"category":"circuit_breaker"`, `"current_step":4`, `"max_steps":4`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("marshalled %s\nmissing %s", raw, key)
		}
	}
}
