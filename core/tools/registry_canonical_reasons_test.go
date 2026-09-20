package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// canonicalJudgeCase is one real-judge observation fed to the drift guard
// below. The platform shell judge cases are supplied by platformShellJudgeCases
// (registry_canonical_reasons_unix_test.go / _windows_test.go) because the
// bash_exec and posh_exec constructors live behind opposing build tags in
// sp4rk, and the two judges do not expose identical hard stages.
type canonicalJudgeCase struct {
	name          string
	outcome       sdktools.JudgeOutcome
	wantCanonical bool
}

// TestCanonicalHardReasonCodes_FromRealBuiltinJudges is the drift guard for
// the cross-repository contract behind the Smart Approve backstop (ADR-026):
// isCanonicalHardReason keys off sdktools.JudgeReasonCode, so the backstop is
// only as strong as the codes the REAL sp4rk builtin judges attach. This test
// drives the real judges — not prose copies in mocks — so a reworded reason or
// a dropped ReasonCode in sp4rk fails here instead of silently making a fired
// security control clearable by the strict judge. The platform shell judge
// (bash_exec on Unix, posh_exec on Windows) is driven through
// platformShellJudgeCases so this file never references a platform-only
// constructor.
func TestCanonicalHardReasonCodes_FromRealBuiltinJudges(t *testing.T) {
	ctx := context.Background()

	webfetchTool := builtins.NewWebFetchTool(builtins.WebFetchLimits{})
	readFileTool := builtins.NewReadFileTool()
	writeFileTool := builtins.NewWriteFileTool()

	ws := t.TempDir()
	gitTarget, err := json.Marshal(map[string]string{
		"path":    filepath.Join(ws, ".git", "config"),
		"content": "evil",
	})
	if err != nil {
		t.Fatal(err)
	}
	wsCtx := sdktools.WithWorkspacePath(ctx, ws)

	tests := append([]canonicalJudgeCase{
		{
			name:          "web_fetch unassessable URL",
			outcome:       webfetchTool.Judge(ctx, json.RawMessage(`{}`)),
			wantCanonical: true,
		},
		{
			name:          "web_fetch SSRF private address",
			outcome:       webfetchTool.Judge(ctx, json.RawMessage(`{"url":"http://127.0.0.1:9/internal"}`)),
			wantCanonical: true,
		},
		{
			name:          "read_file unassessable path",
			outcome:       readFileTool.Judge(ctx, json.RawMessage(`{}`)),
			wantCanonical: true,
		},
		{
			name:          "write_file git-internal target",
			outcome:       writeFileTool.Judge(wsCtx, gitTarget),
			wantCanonical: true,
		},
	}, platformShellJudgeCases(t)...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.outcome.Allow {
				t.Fatalf("real judge outcome = allowed, want an escalation: %+v", tt.outcome)
			}
			if tt.outcome.Severity != sdktools.JudgeSeverityHard {
				t.Errorf("real judge severity = %v, want hard (outcome %+v)", tt.outcome.Severity, tt.outcome)
			}
			if tt.outcome.ReasonCode == "" {
				t.Errorf("real judge attached no ReasonCode (prose %q); an unclassified hard reason is invisible to the backstop — classify it in sp4rk/tools/safety.go", tt.outcome.Reason)
			}
			if got := isCanonicalHardReason(tt.outcome.ReasonCode); got != tt.wantCanonical {
				t.Errorf("isCanonicalHardReason(%q) = %v, want %v (prose: %q)", tt.outcome.ReasonCode, got, tt.wantCanonical, tt.outcome.Reason)
			}
		})
	}
}

// TestCanonicalHardReasonCodes_ClassificationTable pins the classification of
// every published reason code. Codes that cannot be triggered
// deterministically through a real judge (ReasonCodeSSRFDegraded requires a
// broken CIDR-list initialization) are covered here, so removing one from the
// canonical set is a visible, reviewable change rather than a silent policy
// shift.
func TestCanonicalHardReasonCodes_ClassificationTable(t *testing.T) {
	tests := []struct {
		code          sdktools.JudgeReasonCode
		wantCanonical bool
	}{
		{sdktools.ReasonCodeCommandBlacklist, true},
		{sdktools.ReasonCodeCommandExfilFlow, true},
		{sdktools.ReasonCodeCommandPrivilegeEscalation, true},
		{sdktools.ReasonCodeCommandSystemWrite, true},
		{sdktools.ReasonCodeCommandDestructiveOutsideRoots, true},
		{sdktools.ReasonCodeCommandDownloadCradle, true},
		// The deterministic analyzer could not run at all: fail closed — a
		// fired control-like reason, never auto-overridable.
		{sdktools.ReasonCodeCommandAnalysisUnavailable, true},
		{sdktools.ReasonCodeSSRFPrivateAddress, true},
		{sdktools.ReasonCodeSSRFDegraded, true},
		{sdktools.ReasonCodeUnassessableURL, true},
		{sdktools.ReasonCodeUnassessablePath, true},
		{sdktools.ReasonCodeSymlinkEscape, true},
		{sdktools.ReasonCodeGitInternal, true},
		// The flowsh ⊤ limitation: hard but deliberately clearable — an
		// analysis limitation the strict judge may positively clear, not a
		// fired control.
		{sdktools.ReasonCodeCommandUnboundedAnalysis, false},
		// The flowsh soft scope question: non-canonical by construction.
		{sdktools.ReasonCodeCredentialAccess, false},
		{sdktools.ReasonCodeUnresolvablePathToken, false},
		{sdktools.ReasonCodeSymlinkSuspicious, false},
		{sdktools.ReasonCodeOutsideSessionRoots, false},
		{"", false}, // unclassified hard reasons stay clearable-by-judge by design
	}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := isCanonicalHardReason(tt.code); got != tt.wantCanonical {
				t.Errorf("isCanonicalHardReason(%q) = %v, want %v", tt.code, got, tt.wantCanonical)
			}
		})
	}
}

// TestSplitSafetyReasons_CanonicalSymlinkNotMaskedByNonCanonicalJudge pins the
// canonicality-aware fold: when the tool-local judge reports a HARD but
// NON-canonical reason (the flowsh ⊤ limitation) while the symlink gate
// independently reports a CANONICAL escape, the folded reason must carry the
// canonical symlink code so isCanonicalHardReason — the deterministic
// backstop consulted by smartApproveOrConfirm and ExecuteUnattended — still
// sees the canonical signal. Every other fold combination is unchanged.
func TestSplitSafetyReasons_CanonicalSymlinkNotMaskedByNonCanonicalJudge(t *testing.T) {
	const (
		symlinkReason = "symlink escapes the session roots"
		judgeReason   = "command could not be bounded"
	)
	tests := []struct {
		name          string
		judge         sdktools.JudgeOutcome
		symlinkReason string
		symlinkCode   sdktools.JudgeReasonCode
		wantHard      string
		wantCode      sdktools.JudgeReasonCode
	}{
		{
			name: "canonical symlink survives a non-canonical judge hard reason",
			judge: sdktools.JudgeOutcome{
				Allow:      false,
				Reason:     judgeReason,
				Severity:   sdktools.JudgeSeverityHard,
				ReasonCode: sdktools.ReasonCodeCommandUnboundedAnalysis,
			},
			symlinkReason: symlinkReason,
			symlinkCode:   sdktools.ReasonCodeSymlinkEscape,
			wantHard:      symlinkReason,
			wantCode:      sdktools.ReasonCodeSymlinkEscape,
		},
		{
			name: "canonical judge hard reason still wins over a canonical symlink",
			judge: sdktools.JudgeOutcome{
				Allow:      false,
				Reason:     "command matches blacklist pattern: mkfs",
				Severity:   sdktools.JudgeSeverityHard,
				ReasonCode: sdktools.ReasonCodeCommandBlacklist,
			},
			symlinkReason: symlinkReason,
			symlinkCode:   sdktools.ReasonCodeSymlinkEscape,
			wantHard:      "command matches blacklist pattern: mkfs",
			wantCode:      sdktools.ReasonCodeCommandBlacklist,
		},
		{
			name: "non-canonical judge hard reason wins over a non-canonical symlink reason",
			judge: sdktools.JudgeOutcome{
				Allow:      false,
				Reason:     judgeReason,
				Severity:   sdktools.JudgeSeverityHard,
				ReasonCode: sdktools.ReasonCodeCommandUnboundedAnalysis,
			},
			symlinkReason: "unresolvable path token",
			symlinkCode:   sdktools.ReasonCodeUnresolvablePathToken,
			wantHard:      judgeReason,
			wantCode:      sdktools.ReasonCodeCommandUnboundedAnalysis,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSafetyReasons(tt.judge, tt.symlinkReason, tt.symlinkCode)
			if got.hard != tt.wantHard || got.hardCode != tt.wantCode {
				t.Errorf("hard = (%q, %q), want (%q, %q)", got.hard, got.hardCode, tt.wantHard, tt.wantCode)
			}
		})
	}
}

// TestSymlinkHardReason_ReturnsTypedEscapeCode drives the real host-side
// symlink gate (sp4rk's walker + this registry's escape classification) and
// verifies the hard reason carries the canonical ReasonCodeSymlinkEscape the
// backstop keys off — the prose alone is not a contract.
func TestSymlinkHardReason_ReturnsTypedEscapeCode(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir() // a different root: outside the workspace
	link := filepath.Join(ws, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	readFileTool := builtins.NewReadFileTool()
	registry := NewToolRegistry()
	registry.Register(readFileTool)

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(link, "file.txt")})
	if err != nil {
		t.Fatal(err)
	}

	reason, code := registry.symlinkHardReason(ctx, "read_file", readFileTool, input)
	if reason == "" {
		t.Fatal("symlinkHardReason() = empty reason, want an escape escalation")
	}
	if code != sdktools.ReasonCodeSymlinkEscape {
		t.Errorf("symlinkHardReason() code = %q, want %q (reason: %q)", code, sdktools.ReasonCodeSymlinkEscape, reason)
	}
	if !isCanonicalHardReason(code) {
		t.Errorf("isCanonicalHardReason(%q) = false, want true: a symlink escape must never be auto-approved", code)
	}
}

// TestSilentMode_JudgeTerminalCanonicalAllowExecutes pins the canonical-hard-
// reason behavior across BOTH autonomy paths after decision 1c removed the
// silent backstop:
//
//   - the assisted path KEEPS the rule-2 backstop: a strict-judge ALLOW on a
//     canonical hard reason (isCanonicalHardReason — a fired security
//     control or an unassessable input) is overridden to a user
//     confirmation; a canonical fired control must never become
//     judge-waivable while a human is available to confirm;
//   - the silent judge terminal has NO backstop: the operator delegated the
//     decision to the judge and no human can overrule it either way, so the
//     canonical ALLOW EXECUTES — and the audited autonomy_decision event
//     carries the judge's justification for allowing a fired control, so the
//     trajectory stays reconstructable (ASI10).
func TestSilentMode_JudgeTerminalCanonicalAllowExecutes(t *testing.T) {
	const canonical = sdktools.ReasonCodeCommandBlacklist
	if !isCanonicalHardReason(canonical) {
		t.Fatalf("precondition: %q must be canonical", canonical)
	}

	newRegistry := func(mode string) (*ToolRegistry, *scriptedJudgeProvider, *bool) {
		registry := NewToolRegistry()
		registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
			sdktools.GroupLocalRead: sdktools.PolicyAlwaysAllow,
		})
		registry.ApplySecurityState(
			registry.GroupPolicies(),
			false,
			mode,
			SilentModeState{ToolConfirm: SilentToolConfirmJudge},
		)
		registry.Register(newMockHardJudgerTool("esc_tool", "command matches blacklist pattern: mkfs", canonical))
		judge, provider := newStrictJudge("VERDICT: ALLOW\nREASON: false positive, cleared", nil)
		registry.SetJudge(judge)
		confirmCalled := false
		registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
			confirmCalled = true
			return sdktools.ConfirmDeny, nil
		})
		return registry, provider, &confirmCalled
	}

	// Assisted mode: the rule-2 backstop still forces a confirmation for a
	// canonical hard reason even on a strict ALLOW.
	registry, provider, confirmCalled := newRegistry(AutonomyModeAssisted)
	res, err := registry.Execute(context.Background(), "esc_tool", json.RawMessage(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !*confirmCalled {
		t.Error("assisted mode must force a confirmation for a canonical hard reason even on ALLOW (rule 2)")
	}
	if !res.IsError {
		t.Error("denying the forced confirmation must surface an error result")
	}
	if got := provider.callCount(); got != 1 {
		t.Errorf("assisted strict judge calls = %d, want 1", got)
	}

	// Silent judge mode: the canonical ALLOW executes (no backstop, no
	// confirmation card), and the decision is audited with the judge's
	// justification.
	silentReg, silentProvider, silentConfirm := newRegistry(AutonomyModeSilent)
	rec := &autonomyDecisionRecorder{}
	silentReg.SetAutonomyDecisionObserver(rec.observe)
	res, err = silentReg.Execute(context.Background(), "esc_tool", json.RawMessage(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if *silentConfirm {
		t.Error("silent mode must not open a confirmation card")
	}
	if res.IsError {
		t.Errorf("silent judge mode must EXECUTE a canonical hard reason on a strict ALLOW (decision 1c), got %q", res.Content)
	}
	if got := silentProvider.callCount(); got != 1 {
		t.Errorf("silent strict judge calls = %d, want 1", got)
	}
	if len(rec.decisions) != 1 {
		t.Fatalf("audit events = %d, want exactly 1: %+v", len(rec.decisions), rec.decisions)
	}
	d := rec.decisions[0]
	if d.Verdict != autonomyDecisionVerdictAllow {
		t.Errorf("audit Verdict = %q, want %q", d.Verdict, autonomyDecisionVerdictAllow)
	}
	if !strings.Contains(d.Justification, "false positive, cleared") {
		t.Errorf("audit Justification = %q, want it to carry the judge's reasoning for allowing a fired control", d.Justification)
	}
}
