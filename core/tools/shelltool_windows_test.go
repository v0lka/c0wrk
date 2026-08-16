//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestShellExecToolName_Windows verifies that on Windows the platform-split
// shell-tool registration produces a tool registered under the "posh_exec"
// name. This is the Windows half of the cross-platform contract: the same
// newShellExecTool call registers "bash_exec" on Unix (see
// shelltool_unix_test.go). Guarding the assertion behind a build tag keeps
// the test aligned with the build-tagged constructor it exercises.
func TestShellExecToolName_Windows(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts())
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	if got := tool.Name(); got != "posh_exec" {
		t.Errorf("tool name = %q, want %q", got, "posh_exec")
	}
}

// TestShellExecTool_WindowsAliasSupplement verifies the Windows-only platform
// supplement: the Remove-Item alias patterns that cannot live in the unified
// cross-dialect execute-group blacklist are appended at construction, so even
// an EMPTY configured blacklist still hard-blocks destructive alias deletes
// (`rm -r -f`, `del -Recurse -Force`), while ordinary commands stay clean.
// This is the Windows half of
// backend/config.TestDefaultExecuteGroupBlacklist_CrossDialectSafe: the same
// alias spellings are routine Unix vocabulary there and must stay unblocked
// (see TestShellExecTool_UnixHasNoAliasSupplement).
func TestShellExecTool_WindowsAliasSupplement(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts())
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	judger, ok := tool.(sdktools.ToolJudger)
	if !ok {
		t.Fatal("posh_exec tool does not implement ToolJudger")
	}

	for _, cmd := range []string{
		`rm -r -f C:\Temp\victims`,
		`rm -f -r C:\Temp\victims`,
		`rm -Recurse -Force C:\Temp\victims`,
		`del -r -f C:\Temp\victims`,
	} {
		input, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		outcome := judger.Judge(context.Background(), input)
		if outcome.Allow || outcome.Severity != sdktools.JudgeSeverityHard {
			t.Errorf("supplement must hard-block %q: got allow=%v severity=%v reason=%q",
				cmd, outcome.Allow, outcome.Severity, outcome.Reason)
		}
	}

	input, err := json.Marshal(map[string]string{"command": "Get-ChildItem C:\\Temp"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if outcome := judger.Judge(context.Background(), input); !outcome.Allow && outcome.Reason != "" {
		t.Errorf("benign command escalated: reason=%q", outcome.Reason)
	}
}
