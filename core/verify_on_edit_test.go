package core

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools/builtins"

	"github.com/v0lka/c0wrk/core/tools"
)

// newVerifyTestRegistry builds a session-style ToolRegistry with the real
// builtin tools registered (bash included), mirroring the production setup
// closely enough to exercise ExecuteUnattended + the bash tool end-to-end.
func newVerifyTestRegistry(t *testing.T) *tools.ToolRegistry {
	t.Helper()
	registry := tools.NewToolRegistry()
	// Zero-value BashTimeouts would cap every command at 0s (instant kill);
	// production fills these from config — mirror that with SDK defaults.
	cfg := tools.BuiltinToolsConfig{BashTimeouts: builtins.DefaultBashTimeouts()}
	if err := tools.RegisterBuiltinTools(registry, cfg); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}
	return registry
}

func TestParseVerifyOnEditTimeout(t *testing.T) {
	cases := []struct {
		raw    string
		want   time.Duration
		wantOK bool
	}{
		{"", verifyOnEditDefaultTimeout, true},
		{"   ", verifyOnEditDefaultTimeout, true},
		{"90s", 90 * time.Second, true},
		{"3m", 3 * time.Minute, true},
		{"1h", time.Hour, true},
		{"banana", verifyOnEditDefaultTimeout, false}, // invalid → fallback
		{"-5s", verifyOnEditDefaultTimeout, false},    // non-positive → fallback
		{"0", verifyOnEditDefaultTimeout, false},      // non-positive → fallback
	}
	for _, tc := range cases {
		got, ok := parseVerifyOnEditTimeout(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("parseVerifyOnEditTimeout(%q) = (%v, %v), want (%v, %v)",
				tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParseExitStatus(t *testing.T) {
	if got := parseExitStatus("some output\nexit status 7"); got != 7 {
		t.Errorf("exit status 7 → got %d", got)
	}
	if got := parseExitStatus("plain output"); got != -1 {
		t.Errorf("no exit status → got %d, want -1", got)
	}
	if got := parseExitStatus("blocked by blacklist\n"); got != -1 {
		t.Errorf("trailing newline without status → got %d, want -1", got)
	}
}

func TestBuildEditVerifyRunner_NilWhenUnconfigured(t *testing.T) {
	if got := buildEditVerifyRunner(newVerifyTestRegistry(t), t.TempDir(), "", "", 0, nil); got != nil {
		t.Errorf("empty command → runner must be nil, got %v", got)
	}
	if got := buildEditVerifyRunner(nil, t.TempDir(), "go test ./...", "", 0, nil); got != nil {
		t.Errorf("nil registry → runner must be nil, got %v", got)
	}
}

func TestEditVerifyRunner_ExitZero(t *testing.T) {
	ws := t.TempDir()
	runner := buildEditVerifyRunner(newVerifyTestRegistry(t), ws,
		"echo verify-ok", "30s", 0, nil)
	if runner == nil {
		t.Fatal("runner is nil for configured command")
	}
	res := runner(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected Err: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (output: %q)", res.ExitCode, res.Output)
	}
	if !strings.Contains(res.Output, "verify-ok") {
		t.Errorf("output should contain command stdout, got %q", res.Output)
	}
}

// nonZeroExitShellCommand returns a command that writes "boom" to stderr and
// exits with status 7, in the platform shell's dialect (bash_exec on Unix,
// posh_exec — Windows PowerShell — on Windows). Windows PowerShell 5.1 has no
// bash-style ">&2" stream redirection (it is a parse error there; stream
// merging arrived only in PowerShell 7), so the Windows dialect targets
// stderr via the .NET console API instead.
func nonZeroExitShellCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Error.WriteLine('boom'); exit 7"
	}
	return "echo boom >&2; exit 7"
}

func TestEditVerifyRunner_NonZeroExit(t *testing.T) {
	ws := t.TempDir()
	runner := buildEditVerifyRunner(newVerifyTestRegistry(t), ws,
		nonZeroExitShellCommand(), "30s", 0, nil)
	res := runner(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected Err: %v", res.Err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7 (output: %q)", res.ExitCode, res.Output)
	}
	if res.TimedOut {
		t.Errorf("TimedOut must be false for non-zero exit")
	}
	if !strings.Contains(res.Output, "boom") {
		t.Errorf("output should contain stderr, got %q", res.Output)
	}
}

func TestEditVerifyRunner_Timeout(t *testing.T) {
	ws := t.TempDir()
	runner := buildEditVerifyRunner(newVerifyTestRegistry(t), ws,
		"echo started; sleep 30", "1s", 0, nil)
	res := runner(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected Err: %v", res.Err)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut must be true (output: %q)", res.Output)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode should not be 0 on timeout")
	}
	if !strings.Contains(res.Output, "timeout") {
		t.Errorf("timeout output should mention timeout, got %q", res.Output)
	}
	if res.Timeout != time.Second {
		t.Errorf("result must echo the effective limit, got %v, want 1s", res.Timeout)
	}
}

// TestEditVerifyRunner_TimeoutClampedToBashMax proves the clamp is real:
// with a configured timeout above the bash max, the effective limit is the
// bash max — a command configured for "5m" but capped at 1s is killed after
// ~1s (not after the configured 5m, and not after the bash tool's own 120s
// default), and the result echoes the clamped limit so the timeout note
// reports the number that actually applied.
func TestEditVerifyRunner_TimeoutClampedToBashMax(t *testing.T) {
	ws := t.TempDir()
	runner := buildEditVerifyRunner(newVerifyTestRegistry(t), ws,
		"sleep 30", "5m", time.Second, nil)
	if runner == nil {
		t.Fatal("runner is nil for configured command")
	}
	start := time.Now()
	res := runner(context.Background())
	elapsed := time.Since(start)
	if res.Err != nil {
		t.Fatalf("unexpected Err: %v", res.Err)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut must be true under the 1s clamp (output: %q)", res.Output)
	}
	if res.Timeout != time.Second {
		t.Errorf("effective timeout must be the clamped 1s, got %v", res.Timeout)
	}
	if elapsed > 30*time.Second {
		t.Errorf("command should have been killed at ~1s, took %v", elapsed)
	}
}

func TestOrchestrator_VerifyOnEditForMode_Gates(t *testing.T) {
	var runner agent.EditVerifyRunner = func(context.Context) agent.EditVerifyResult {
		return agent.EditVerifyResult{}
	}
	o := &Orchestrator{verifyOnEdit: runner}

	// CODE mode (isNoProject=false): runner passes through.
	if o.verifyOnEditForMode() == nil {
		t.Error("CODE mode must keep the configured runner")
	}
	// CHAT mode (No Project): suppressed.
	o.isNoProject = true
	if o.verifyOnEditForMode() != nil {
		t.Error("No Project (CHAT) mode must suppress verify-on-edit")
	}
}

// TestEditVerifyRunner_BlacklistedCommand proves the security posture holds
// even for config-authored commands: the extra shell blacklist (No Project
// mode hardening) blocks the unattended execution path fail-closed, marking
// the verification as failed (IsError) rather than silently skipping policy.
func TestEditVerifyRunner_BlacklistedCommand(t *testing.T) {
	ws := t.TempDir()
	registry := newVerifyTestRegistry(t)
	if err := registry.SetExtraShellBlacklist([]string{`^forbidden-verify-cmd`}); err != nil {
		t.Fatalf("SetExtraShellBlacklist: %v", err)
	}
	runner := buildEditVerifyRunner(registry, ws, "forbidden-verify-cmd --run", "30s", 0, nil)
	if runner == nil {
		t.Fatal("runner is nil for configured command")
	}
	res := runner(context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected Err: %v", res.Err)
	}
	if res.ExitCode == 0 {
		t.Errorf("blacklisted command must not report success: %+v", res)
	}
	if !strings.Contains(res.Output, "blocked") {
		t.Errorf("output should state the block, got %q", res.Output)
	}
	if res.TimedOut {
		t.Errorf("blacklist block is not a timeout")
	}
}
