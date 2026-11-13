//go:build darwin

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
// never came up at all (exec/open refused): the staging dir then carries no
// diagnostics worth keeping, so ApplyUpdate removes it.
var errUpdaterNeverStarted = errors.New("staged updater never started")

// launchStagedUpdater starts the staged updater .app bundle through
// LaunchServices (`open -n`): launchd becomes the updater's parent and it
// survives the quitting app — the property a bare fork/exec child lacks on
// macOS, where it keeps the dying GUI app as its responsible process (an
// attribution even a Setsid detach does not shed) and is killed silently
// about a second after the app exits, mid-apply. See
// core/updater's stagingLaunchPath and specs/decisions/023-auto-update.md.
//
// stdout/stderr are unused on this platform: `open` cannot attach
// descriptors, so the updater redirects its own stdio to the staging log
// (main.go, --self-update path). Liveness is proven by polling for the
// updater process itself: `open` returning 0 only means LaunchServices
// accepted the launch.
func launchStagedUpdater(updaterPath string, args []string, _, _ io.Writer, log *slog.Logger) error {
	openArgs := append([]string{"-n", updaterPath, "--args"}, args...)
	// Bounded context: a wedged LaunchServices must not hang the apply path
	// — a healthy `open` returns well inside this window.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "open", openArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: open %s: %w (%s)", errUpdaterNeverStarted, updaterPath, err, string(out))
	}
	// Poll for the updater process: it must appear and stay observable past
	// the async-kill startup window (an updater killed at exec — the macOS
	// amfid behaviour — never appears at all).
	const poll = 200 * time.Millisecond
	const appearWithin = 5 * time.Second
	const stableFor = 1500 * time.Millisecond
	pattern := updaterPath + "/Contents/MacOS/"
	deadline := time.Now().Add(appearWithin)
	var firstSeen time.Time
	for {
		alive, probeErr := updaterProcessAlive(pattern)
		switch {
		case probeErr != nil:
			// Liveness UNKNOWN (probe exec error or its cap fired): keep
			// polling — a failed probe is not a death verdict. The
			// appear/stable deadlines below still bound the wait.
			log.Debug("staged updater liveness probe failed; retrying", "err", probeErr)
		case alive:
			now := time.Now()
			if firstSeen.IsZero() {
				firstSeen = now
			} else if now.Sub(firstSeen) >= stableFor {
				log.Debug("staged updater launch verified", "updater", updaterPath)
				return nil
			}
		case !firstSeen.IsZero():
			return fmt.Errorf("staged updater died %.1fs after launch", time.Since(firstSeen).Seconds())
		}
		if time.Now().After(deadline) {
			if firstSeen.IsZero() {
				return fmt.Errorf("%w: no updater process observed within %s", errUpdaterNeverStarted, appearWithin)
			}
			return fmt.Errorf("staged updater was not observable for %s after launch", stableFor)
		}
		time.Sleep(poll)
	}
}

// updaterProcessAlive reports whether a process whose command line contains
// pattern is running. matched=false with a nil probeErr is pgrep's
// definitive "no processes matched" (exit 1); a non-nil probeErr means the
// probe itself failed (exec error, or the 2s cap SIGKILLed it — exit -1)
// and liveness is UNKNOWN: the caller must keep polling instead of counting
// the failed probe as "not alive", or a transiently wedged pgrep would
// fabricate a "died after launch" verdict on a healthy updater. Bounded:
// the probe's own cadence is 200ms, so a healthy pgrep is nowhere near the
// cap.
func updaterProcessAlive(pattern string) (matched bool, probeErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "pgrep", "-f", pattern).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("pgrep liveness probe failed: %w", err)
}
