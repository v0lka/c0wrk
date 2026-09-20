//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// TestShellExecToolName_Unix verifies that on Unix the platform-split
// shell-tool registration produces a tool registered under the "bash_exec"
// name. This is the Unix half of the cross-platform contract: the same
// newShellExecTool call registers "posh_exec" on Windows (see
// shelltool_windows_test.go). Guarding the assertion behind a build tag keeps
// the test aligned with the build-tagged constructor it exercises.
func TestShellExecToolName_Unix(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts(), nil, nil)
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	if got := tool.Name(); got != "bash_exec" {
		t.Errorf("tool name = %q, want %q", got, "bash_exec")
	}
}

// TestShellExecTool_UnixHasNoAliasSupplement verifies that newShellExecTool
// compiles in no hidden patterns: the blocklist is user-authored and empty by
// default (no predefined list ships any more, and the Windows alias
// supplement is gone), so the routine Unix idiom `rm -r -f <dir>` (and its
// GNU long-option spelling, and alias tokens inside compounds) stays a
// policy-gated call rather than a hard blocklist confirmation.
func TestShellExecTool_UnixHasNoAliasSupplement(t *testing.T) {
	tool, err := newShellExecTool(nil, builtins.DefaultBashTimeouts(), nil, nil)
	if err != nil {
		t.Fatalf("newShellExecTool: unexpected error: %v", err)
	}
	judger, ok := tool.(sdktools.ToolJudger)
	if !ok {
		t.Fatal("bash_exec tool does not implement ToolJudger")
	}

	for _, cmd := range []string{
		"rm -r -f ./build",
		"rm --recursive --force dist",
		"rmdir foo && rm -r -f build",
		"grep -ri secret . && rm -r -f dist",
	} {
		input, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		outcome := judger.Judge(context.Background(), input)
		// JudgeSeverityHard is the zero value (meaningless on Allow=true), so
		// a hard blocklist match is precisely: not allowed, with a reason,
		// classified hard.
		if !outcome.Allow && outcome.Reason != "" && outcome.Severity == sdktools.JudgeSeverityHard {
			t.Errorf("Unix constructor must not hard-block %q: reason=%q", cmd, outcome.Reason)
		}
	}
}

// TestUpdateShellTool_ReplacesBlocklist verifies that UpdateShellTool
// re-registers the shell tool with a new compiled-in blocklist: after the
// call, the registered tool reflects the replacement list, and an invalid
// pattern leaves the previous tool intact.
func TestUpdateShellTool_ReplacesBlocklist(t *testing.T) {
	registry := NewToolRegistry()
	if err := RegisterBuiltinTools(registry, BuiltinToolsConfig{
		BashTimeouts: builtins.BashTimeouts{MaxTimeout: 30 * time.Second},
	}); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}

	// An empty replacement (nil blocklist) removes every pattern.
	if err := UpdateShellTool(registry, nil, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}, nil, nil); err != nil {
		t.Fatalf("UpdateShellTool: unexpected error: %v", err)
	}
	// The tool must still be registered under the platform shell name.
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec not registered after UpdateShellTool")
	}

	// A non-empty replacement registers and compiles.
	if err := UpdateShellTool(registry, []string{`^echo\s+danger`}, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}, nil, nil); err != nil {
		t.Fatalf("UpdateShellTool with a valid pattern: unexpected error: %v", err)
	}
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec not registered after a blocklist update")
	}

	// An invalid pattern must fail and leave the previously registered
	// tool in place.
	if err := UpdateShellTool(registry, []string{"("}, builtins.BashTimeouts{MaxTimeout: 30 * time.Second}, nil, nil); err == nil {
		t.Fatal("expected an error for a pattern that does not compile")
	}
	if _, ok := registry.Get("bash_exec"); !ok {
		t.Fatal("bash_exec must remain registered after a failed update")
	}
}
