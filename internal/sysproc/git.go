package sysproc

import (
	"context"
	"os/exec"
)

// GitCmd creates a git [exec.Cmd] with the console window hidden on Windows
// (CREATE_NO_WINDOW). c0wrk-desktop is a GUI-subsystem app; without this flag
// every git invocation flashes a console window on screen.
//
// Use this helper for every git invocation so the HideConsole call cannot be
// forgotten. For non-git child processes apply [HideConsole] directly.
func GitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	HideConsole(cmd)
	return cmd
}
