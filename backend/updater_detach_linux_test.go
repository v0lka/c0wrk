//go:build linux

package backend

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietUpdaterLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// shortenProbeDelay collapses the post-Start liveness window so the
// verified-live test does not spend a real second per run. Restored via
// t.Cleanup; tests in this file do not run in parallel.
func shortenProbeDelay(t *testing.T) {
	t.Helper()
	old := updaterStartProbeDelay
	updaterStartProbeDelay = 20 * time.Millisecond
	t.Cleanup(func() { updaterStartProbeDelay = old })
}

// TestLaunchStagedUpdater_VerifiesLiveChild covers the verified path: a stub
// that survives the (shortened) probe window — a healthy updater's first
// action is waiting for this process to quit — returns nil. The stub
// self-terminates via its bounded sleep.
func TestLaunchStagedUpdater_VerifiesLiveChild(t *testing.T) {
	shortenProbeDelay(t)
	script := filepath.Join(t.TempDir(), "c0wrk-updater")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := launchStagedUpdater(script, nil, io.Discard, io.Discard, quietUpdaterLogger()); err != nil {
		t.Fatalf("launchStagedUpdater verified-live child: %v", err)
	}
}

// TestLaunchStagedUpdater_AbortsOnImmediateExit covers the dead-updater path:
// a child that exits the moment it is exec'd aborts the quit with the
// immediate-exit diagnosis, and must NOT map to errUpdaterNeverStarted — an
// exit AFTER a successful Start means the staging dir carries diagnostics
// worth keeping (ApplyUpdate removes it only for never-started launches).
func TestLaunchStagedUpdater_AbortsOnImmediateExit(t *testing.T) {
	script := filepath.Join(t.TempDir(), "c0wrk-updater")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := launchStagedUpdater(script, nil, io.Discard, io.Discard, quietUpdaterLogger())
	if err == nil {
		t.Fatal("an immediately-exiting updater must fail the launch")
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Fatalf("err = %v; want the immediate-exit diagnosis", err)
	}
	if errors.Is(err, errUpdaterNeverStarted) {
		t.Fatalf("an exit after a successful Start keeps the staging diagnostics — must not be errUpdaterNeverStarted: %v", err)
	}
}

// TestLaunchStagedUpdater_StartRefusalMapsToNeverStarted covers the exec
// refusal path: a staged path that cannot exec (no permission bit) fails
// cmd.Start and maps to errUpdaterNeverStarted — ApplyUpdate then removes
// the diagnostics-free staging dir.
func TestLaunchStagedUpdater_StartRefusalMapsToNeverStarted(t *testing.T) {
	script := filepath.Join(t.TempDir(), "c0wrk-updater")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := launchStagedUpdater(script, nil, io.Discard, io.Discard, quietUpdaterLogger())
	if !errors.Is(err, errUpdaterNeverStarted) {
		t.Fatalf("err = %v; want errUpdaterNeverStarted", err)
	}
}
