//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// platformShellJudgeCases drives the REAL bash_exec judge for the canonical
// hard-reason drift guard: sp4rk's bash.go is //go:build !windows, so the
// constructor cannot be called from the platform-neutral test file (it would
// fail to compile on Windows with "undefined: builtins.NewBashExecTool").
// The bash judge uniquely exposes the unresolvable path-like token stage
// (ReasonCodeUnresolvablePathToken, a clearable hard reason); the Windows
// counterpart in registry_canonical_reasons_windows_test.go drives posh_exec,
// whose judge has no such stage.
func platformShellJudgeCases(t *testing.T) []canonicalJudgeCase {
	t.Helper()

	bashTool, err := builtins.NewBashExecTool([]string{`rm\s+-rf\s+/`})
	if err != nil {
		t.Fatalf("NewBashExecTool() error = %v", err)
	}
	ctx := context.Background()
	return []canonicalJudgeCase{
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
	}
}
