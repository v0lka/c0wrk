//go:build windows

package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"
)

// errUpdaterNeverStarted marks a launch failure where the updater process
// never came up at all (exec refused): the staging dir then carries no
// diagnostics worth keeping, so ApplyUpdate removes it.
var errUpdaterNeverStarted = errors.New("staged updater never started")

// updaterStartProbeDelay bounds the post-Start liveness probe: a healthy
// staged updater is guaranteed still to be running this long, so an exit
// inside the window proves the child was killed before doing anything
// (macOS amfid kills asynchronously). See specs/decisions/023-auto-update.md.
const updaterStartProbeDelay = 1 * time.Second

// launchStagedUpdater starts the staged updater as a plain detached child.
// Windows has no Unix session/process-group signal sweep to detach FROM (the
// reason the macOS path launches through LaunchServices instead), and the
// two-process dance works as shipped — the running updater .exe cannot
// self-delete, hence the startup reap in CleanupStaleUpdaters — so the
// creation context is deliberately left default.
func launchStagedUpdater(updaterPath string, args []string, stdout, stderr io.Writer, log *slog.Logger) error {
	// context.Background() is intentional: the app context is cancelled by
	// the graceful quit that follows, which would kill a CommandContext tied
	// to it.
	cmd := exec.CommandContext(context.Background(), updaterPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", errUpdaterNeverStarted, err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case waitErr := <-waitCh:
		return fmt.Errorf("staged updater exited immediately without applying the update: %w", waitErr)
	case <-time.After(updaterStartProbeDelay):
		log.Debug("staged updater launch verified", "updater", updaterPath)
		return nil
	}
}
