package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestCanonicalHardReasonCodes_FromRealBuiltinJudges is the drift guard for
// the cross-repository contract behind the Smart Approve backstop (ADR-026):
// isCanonicalHardReason keys off sdktools.JudgeReasonCode, so the backstop is
// only as strong as the codes the REAL sp4rk builtin judges attach. This test
// drives the real judges — not prose copies in mocks — so a reworded reason or
// a dropped ReasonCode in sp4rk fails here instead of silently making a fired
// security control clearable by the strict judge.
func TestCanonicalHardReasonCodes_FromRealBuiltinJudges(t *testing.T) {
	ctx := context.Background()

	bashTool, err := builtins.NewBashExecTool([]string{`rm\s+-rf\s+/`})
	if err != nil {
		t.Fatalf("NewBashExecTool() error = %v", err)
	}
	webfetchTool := builtins.NewWebFetchTool(builtins.WebFetchLimits{})
	readFileTool := builtins.NewReadFileTool()

	tests := []struct {
		name          string
		outcome       sdktools.JudgeOutcome
		wantCanonical bool
	}{
		{
			name:          "bash blacklist match",
			outcome:       bashTool.Judge(ctx, json.RawMessage(`{"command":"rm -rf /"}`)),
			wantCanonical: true,
		},
		{
			name:          "bash unresolvable path-like token stays clearable",
			outcome:       bashTool.Judge(ctx, json.RawMessage(`{"command":"cat ~nosuchuser/secret"}`)),
			wantCanonical: false,
		},
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
	}
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
		{sdktools.ReasonCodeSSRFPrivateAddress, true},
		{sdktools.ReasonCodeSSRFDegraded, true},
		{sdktools.ReasonCodeUnassessableURL, true},
		{sdktools.ReasonCodeUnassessablePath, true},
		{sdktools.ReasonCodeSymlinkEscape, true},
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
