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

// globSymlinkNarrowedDiagnostic is the WARN the registry's symlink gate emits
// when it narrows scanning to the recognized path fields (glob's [path]) and
// leaves the other string fields ([pattern type]) unscanned. Captured at the
// source so the run stays diagnostic-clean, matching registry_test.go.
var globSymlinkNarrowedDiagnostic = expectedToolDiagnostic{
	message: "symlink detection narrowed by path-field allowlist; non-path string fields not scanned",
	attrs: map[string]string{
		"tool":                    "glob",
		"scanned_path_fields":     "[path]",
		"unscanned_string_fields": "[pattern type]",
	},
}

// TestRegisterBuiltinTools_GlobLimitsReachGlobTool proves the configured
// toolLimits glob budgets are threaded into the registered glob tool: a
// MaxResults of 1 aborts a walk that matches three files with the
// results-limited message, while a generous budget returns every match. This
// guards the RegisterBuiltinTools -> NewGlobToolWithLimits wiring (the former
// NewGlobTool() call ignored the configured limits entirely).
func TestRegisterBuiltinTools_GlobLimitsReachGlobTool(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	input, err := json.Marshal(map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	// The glob path sits at the workspace root, so the containment/symlink gate
	// sees an in-roots read and the local_read allow policy executes it.
	ctx := sdktools.WithWorkspacePath(context.Background(), dir)

	// newRegistry registers the built-ins with the given glob limits and
	// installs the diagnostic capture (exactly one symlink-narrowed WARN is
	// emitted per executed glob walk).
	newRegistry := func(t *testing.T, limits builtins.GlobLimits) *ToolRegistry {
		t.Helper()
		r := NewToolRegistry()
		setDefaultGroupPolicies(r)
		captureToolDiagnostics(t, r, globSymlinkNarrowedDiagnostic)
		if err := RegisterBuiltinTools(r, BuiltinToolsConfig{GlobLimits: limits}); err != nil {
			t.Fatalf("RegisterBuiltinTools: %v", err)
		}
		return r
	}

	t.Run("configured max results is enforced", func(t *testing.T) {
		r := newRegistry(t, builtins.GlobLimits{MaxResults: 1})
		r.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
			t.Fatal("confirmFunc must not be called: glob is always-allow and in-roots")
			return sdktools.ConfirmDeny, nil
		})
		res, err := r.Execute(ctx, "glob", input)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		// A bound that fires is a non-error warning, not a failure: the matches
		// already collected are returned with a truncation suffix rather than
		// discarded.
		if res.IsError {
			t.Fatalf("a bound abort must not be an error result, got content=%q", res.Content)
		}
		if !strings.Contains(res.Content, "results limited to 1") {
			t.Fatalf("expected the MaxResults=1 limit to fire, got content=%q", res.Content)
		}
	})

	t.Run("generous max results returns all matches", func(t *testing.T) {
		res, err := newRegistry(t, builtins.GlobLimits{MaxResults: 100}).Execute(ctx, "glob", input)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if res.IsError {
			t.Fatalf("unexpected error result: %q", res.Content)
		}
		if got := len(strings.Split(strings.TrimSpace(res.Content), "\n")); got != 3 {
			t.Fatalf("expected 3 matches, got %d (%q)", got, res.Content)
		}
	})
}
