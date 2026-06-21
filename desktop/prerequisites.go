package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// requiredBinaries lists external CLI tools c0wrk hard-depends on at runtime.
// Only git is a native hard dependency — all other tools (rg, rtk, uv,
// markitdown) are managed by the tool-manager (see specs/decisions/010-tool-manager.md).
var requiredBinaries = []struct {
	name    string
	purpose string
}{
	{"git", "workspace git status, diffs, branch detection, and .gitignore filtering"},
}

// verifyExternalDependencies ensures every required external binary is on
// PATH. If anything is missing, it shows a blocking fatal modal with an
// Exit button, waits for the user to dismiss it, asks Wails to quit, and
// returns false so the caller can abort Startup. Returns true when every
// dependency is present.
func verifyExternalDependencies(ctx context.Context) bool {
	type missingBinary struct {
		name, purpose string
	}
	var missing []missingBinary
	for _, b := range requiredBinaries {
		if _, err := exec.LookPath(b.name); err != nil {
			missing = append(missing, missingBinary{b.name, b.purpose})
		}
	}
	if len(missing) == 0 {
		return true
	}

	var sb strings.Builder
	sb.WriteString("c0wrk requires the following external tools, but they were not found in PATH:\n\n")
	for _, m := range missing {
		fmt.Fprintf(&sb, "  \u2022 %s \u2014 %s\n", m.name, m.purpose)
	}
	sb.WriteString("\nInstall the missing tools and restart c0wrk.")

	_, _ = wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
		Type:          wailsRuntime.ErrorDialog,
		Title:         "Missing Required Dependencies",
		Message:       sb.String(),
		Buttons:       []string{"Exit"},
		DefaultButton: "Exit",
	})
	wailsRuntime.Quit(ctx)
	return false
}
