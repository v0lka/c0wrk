//go:build windows

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

// detachStubSleepEnv turns the test binary into a "healthy updater" payload
// when it is launched as a staged-updater child: the launcher passes the
// parent environment through unchanged, so t.Setenv reaches the stub.
const detachStubSleepEnv = "C0WRK_UPDATER_DETACH_TEST_STUB_SLEEP"

// TestUpdaterDetachStubSleeper asserts nothing: it IS the payload the
// launchStagedUpdater tests launch as the staged updater (an os.Executable
// copy selected via -test.run). Sleeping past the probe window emulates a
// healthy updater, whose first action is waiting for the quitting app.
func TestUpdaterDetachStubSleeper(t *testing.T) {
	if os.Getenv(detachStubSleepEnv) == "" {
		t.Skip("stub payload: runs only as a launched staged-updater child")
	}
	time.Sleep(2 * time.Second)
}

// copyTestBinaryAsStagedUpdater stages the running test binary as the
// "updater": a real PE the OS can exec (a crafted file would be refused at
// CreateProcess and never reach the probe).
func copyTestBinaryAsStagedUpdater(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), "c0wrk-updater.exe")
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return staged
}

// TestLaunchStagedUpdater_VerifiesLiveChild covers the verified path: the
// sleeping stub is still running when the (shortened) probe window closes,
// so the launch is verified. The stub self-terminates via its bounded sleep.
func TestLaunchStagedUpdater_VerifiesLiveChild(t *testing.T) {
	old := updaterStartProbeDelay
	updaterStartProbeDelay = 50 * time.Millisecond
	t.Cleanup(func() { updaterStartProbeDelay = old })
	staged := copyTestBinaryAsStagedUpdater(t)
	t.Setenv(detachStubSleepEnv, "1")
	err := launchStagedUpdater(staged, []string{"-test.run=TestUpdaterDetachStubSleeper"}, io.Discard, io.Discard, quietUpdaterLogger())
	if err != nil {
		t.Fatalf("launchStagedUpdater verified-live child: %v", err)
	}
}

// TestLaunchStagedUpdater_AbortsOnImmediateExit covers the dead-updater path:
// with the stub env unset the sleeper test skips and the child binary exits 0
// right after its (no-op) TestMain — inside the probe window, which must be
// wide enough to see the exit — so the launch aborts with the immediate-exit
// diagnosis and NOT errUpdaterNeverStarted (an exit after a successful Start
// keeps the staging diagnostics).
func TestLaunchStagedUpdater_AbortsOnImmediateExit(t *testing.T) {
	old := updaterStartProbeDelay
	updaterStartProbeDelay = 5 * time.Second
	t.Cleanup(func() { updaterStartProbeDelay = old })
	staged := copyTestBinaryAsStagedUpdater(t)
	err := launchStagedUpdater(staged, []string{"-test.run=TestUpdaterDetachStubSleeper"}, io.Discard, io.Discard, quietUpdaterLogger())
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
// refusal path: a staged path that is not a PE image fails cmd.Start
// (ERROR_BAD_EXE_FORMAT) and maps to errUpdaterNeverStarted — ApplyUpdate
// then removes the diagnostics-free staging dir.
func TestLaunchStagedUpdater_StartRefusalMapsToNeverStarted(t *testing.T) {
	staged := filepath.Join(t.TempDir(), "c0wrk-updater.exe")
	if err := os.WriteFile(staged, []byte("definitely not a PE image\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := launchStagedUpdater(staged, nil, io.Discard, io.Discard, quietUpdaterLogger())
	if !errors.Is(err, errUpdaterNeverStarted) {
		t.Fatalf("err = %v; want errUpdaterNeverStarted (an invalid PE is refused at CreateProcess)", err)
	}
}
