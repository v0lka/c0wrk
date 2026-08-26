//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// platformShellJudgeCases drives the REAL posh_exec judge for the canonical
// hard-reason drift guard: sp4rk's posh.go is //go:build windows, mirroring
// the bash.go split, so the platform-neutral test file never references a
// platform-only constructor. The posh judge has no unresolvable path-like
// token stage (that classification is exercised through the classification
// table in registry_canonical_reasons_test.go on every OS); here the
// blacklist match proves the fired security control carries the canonical
// ReasonCodeCommandBlacklist the Smart Approve backstop keys off.
func platformShellJudgeCases(t *testing.T) []canonicalJudgeCase {
	t.Helper()

	poshTool, err := builtins.NewPoshExecTool([]string{`rm\s+-rf\s+/`})
	if err != nil {
		t.Fatalf("NewPoshExecTool() error = %v", err)
	}
	ctx := context.Background()
	return []canonicalJudgeCase{
		{
			name:          "posh blacklist match",
			outcome:       poshTool.Judge(ctx, json.RawMessage(`{"command":"rm -rf /"}`)),
			wantCanonical: true,
		},
	}
}
