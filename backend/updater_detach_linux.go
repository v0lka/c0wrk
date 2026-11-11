//go:build linux

package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"syscall"
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

// launchStagedUpdater starts the staged updater as a Setsid-detached child:
// detached from the app's session and process group, it survives the app's
// exit and teardown. Liveness is proven by a bounded start probe — a child
// that dies the moment it is exec'd must abort the quit instead of leaving
// the user with a closed app and no update.
func launchStagedUpdater(updaterPath string, args []string, stdout, stderr io.Writer, log *slog.Logger) error {
	// context.Background() is intentional: the app context is cancelled by
	// the graceful quit that follows, which would kill a CommandContext tied
	// to it.
	cmd := exec.CommandContext(context.Background(), updaterPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", errUpdaterNeverStarted, err)
	}
	// A healthy updater is guaranteed still running after the probe window —
	// its first action is waiting for THIS process to quit — so an exit
	// inside the window means the update will never happen.
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
