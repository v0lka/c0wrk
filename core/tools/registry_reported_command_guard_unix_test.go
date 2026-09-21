//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// reportedCommand is the exact command a user reported as being needlessly
// escalated: an approved command substitution (the "PKGS=…" package list) must
// not smear a safety reason onto the whole call. It is included verbatim so the
// regression this guards is reproducible from the report alone.
const reportedCommand = `cd /Users/vkochetkov/Repositories/c0wrk && PKGS=$(go list ./... | grep -v node_modules) && echo "$PKGS" | wc -l && time go test -race -run=^$ -count=1 $PKGS 2>&1 | tail -40`

// reportedCommandWorkspace is the repository root the reported command cds
// into. Driving the judge with THIS directory as the session root makes the
// command's absolute `cd` target in-root BY CONSTRUCTION (the session root and
// the token are the same string, so IsWithinRoot is true independently of the
// host filesystem) — the guard is about the substitution flattening, not about
// whether this particular checkout happens to exist.
const reportedCommandWorkspace = "/Users/vkochetkov/Repositories/c0wrk"

// TestShellJudgeAndSymlinkGate_ReportedCommandStaysClean is the user-visible
// guard for the binding fix: the reported command must produce NO safety reason
// at either gate that could force a confirmation under SmartApprove=false —
// neither the REAL sp4rk bash judge (builtins.NewBashExecTool, driven with the
// flowsh analysis attached exactly as the registry's Execute path does — see
// AttachShellAnalysis) nor the host registry's symlink gate
// (ToolRegistry.symlinkHardReason). Its inner command substitutions must stay
// genuinely assessed, so the negative controls below still escalate: an
// approved binding must lower nothing that was ever dangerous.
//
// Table-driven and driven against the real builtins (not prose copies in
// mocks), mirroring registry_canonical_reasons_unix_test.go and the bash-judge
// cases in registry_bashexec_unix_test.go. The canonical-reason tests are left
// untouched.
//
// Under the flowsh contract a clean judge outcome is an EXPLICIT ALLOW
// (ShellJudgeOutcome returns the analysis's verdict; the empty zero outcome
// only appears when no analysis is attached). Reads of SYSTEM paths
// (/etc/passwd) no longer escalate — only writes to them do (C3) — and the
// old in-judge unresolvable-token stage is gone (unbounded shapes land on the
// non-canonical C6 instead); the soft scope question survives as C8 for
// non-system out-of-root effects.
func TestShellJudgeAndSymlinkGate_ReportedCommandStaysClean(t *testing.T) {
	bashTool, err := builtins.NewBashExecTool([]string{`rm\s+-rf\s+/`})
	if err != nil {
		t.Fatalf("NewBashExecTool() error = %v", err)
	}

	// The session root is the directory the reported command targets, so its
	// `cd /Users/.../c0wrk` argument resolves in-root and contributes no
	// containment escalation; only the substitution's assessment is under test.
	baseCtx := sdktools.WithWorkspacePath(context.Background(), reportedCommandWorkspace)

	tests := []struct {
		name string
		// command is the shell command fed to the real bash judge.
		command string
		// wantReasonCode is the exact JudgeReasonCode the bash judge must
		// attach; "" means the judge must return an explicit allow (Allow
		// true, no Reason, no ReasonCode) so nothing forces a confirmation.
		wantReasonCode sdktools.JudgeReasonCode
		// wantSeverity is the severity paired with wantReasonCode.
		wantSeverity sdktools.JudgeSeverity
		// wantSymlinkCode is the code the registry symlink gate must attach;
		// "" means the gate must stay silent.
		wantSymlinkCode sdktools.JudgeReasonCode
	}{
		{
			name:            "reported package-test command: judge and symlink gate both stay clean",
			command:         reportedCommand,
			wantReasonCode:  "",
			wantSeverity:    sdktools.JudgeSeverityHard, // zero value; unused for allow rows
			wantSymlinkCode: "",
		},
		{
			name:            "blacklisted inner command stays a canonical hard block",
			command:         `X=$(rm -rf /); echo $X`,
			wantReasonCode:  sdktools.ReasonCodeCommandBlacklist,
			wantSeverity:    sdktools.JudgeSeverityHard,
			wantSymlinkCode: "",
		},
		{
			name:            "out-of-root inner read stays a soft scope escalation",
			command:         `X=$(cat /opt/external/notes.txt); echo $X`,
			wantReasonCode:  sdktools.ReasonCodeOutsideSessionRoots,
			wantSeverity:    sdktools.JudgeSeveritySoft,
			wantSymlinkCode: "",
		},
		{
			name:           "unresolvable inner binding: judge assesses it, symlink walk stays silent",
			command:        `X=$(read D < cfg; echo $D); echo $X`,
			wantReasonCode: "",
			wantSeverity:   sdktools.JudgeSeverityHard, // zero value; unused for allow rows
			// The symlink walk is a pure literal-path extractor: dynamic
			// constructs ($D/$X) are deliberately not assessed there — the
			// deterministic flowsh analysis owns them on this same call.
			wantSymlinkCode: "",
		},
		{
			name:           "bare command substitution: judge assesses it clean, symlink walk stays silent",
			command:        `cat $(echo x)`,
			wantReasonCode: "",
			wantSeverity:   sdktools.JudgeSeverityHard,
			// A bare (argument-position) substitution carries no literal path,
			// so the literal-path walk has nothing to say; flowsh covers the
			// dynamic construct.
			wantSymlinkCode: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": tt.command})
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			// Production posture: the analysis is computed for this exact
			// input and attached before the judge runs (Execute →
			// AttachShellAnalysis → judgeToolCall).
			analysis, aErr := sdktools.AnalyzeShellCommandForJudge(baseCtx, "bash_exec", input)
			if aErr != nil {
				t.Fatalf("AnalyzeShellCommandForJudge(%q): %v", tt.command, aErr)
			}
			ctx := sdktools.WithShellAnalysis(baseCtx, analysis, nil)

			outcome := bashTool.Judge(ctx, input)
			if outcome.ReasonCode != tt.wantReasonCode {
				t.Errorf("bash Judge ReasonCode = %q, want %q (reason: %q)", outcome.ReasonCode, tt.wantReasonCode, outcome.Reason)
			}
			if outcome.Severity != tt.wantSeverity {
				t.Errorf("bash Judge Severity = %v, want %v (reason: %q)", outcome.Severity, tt.wantSeverity, outcome.Reason)
			}
			if tt.wantReasonCode == "" {
				// Explicit allow: no Reason, no ReasonCode — SmartApprove=false
				// has no hard safety backstop to force a confirmation on.
				if !outcome.Allow {
					t.Errorf("bash Judge outcome = %+v, want an explicit allow (no escalation)", outcome)
				}
				if outcome.Reason != "" {
					t.Errorf("bash Judge Reason = %q, want empty for a clean command", outcome.Reason)
				}
			} else if outcome.Reason == "" {
				t.Errorf("bash Judge attached code %q with empty prose; an escalation must carry a reason", outcome.ReasonCode)
			}

			registry := NewToolRegistry()
			symlinkReason, symlinkCode := registry.symlinkHardReason(ctx, "bash_exec", bashTool, input)
			if symlinkCode != tt.wantSymlinkCode {
				t.Errorf("symlinkHardReason() code = %q, want %q (reason: %q)", symlinkCode, tt.wantSymlinkCode, symlinkReason)
			}
			if tt.wantSymlinkCode == "" && symlinkReason != "" {
				t.Errorf("symlinkHardReason() reason = %q, want empty (no symlink escalation)", symlinkReason)
			}
			if tt.wantSymlinkCode != "" && symlinkReason == "" {
				t.Errorf("symlinkHardReason() attached code %q with empty prose; an escalation must carry a reason", symlinkCode)
			}
		})
	}
}
