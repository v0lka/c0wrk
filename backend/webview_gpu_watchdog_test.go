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

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/c0wrk/core/instancelock"
	"github.com/v0lka/c0wrk/core/shutdown"
)

// discardLogger returns a logger that drops everything, so watchdog tests do
// not pollute the test output with the expected expiry Error record.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestScanStderrTailHonorsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr.log")
	// Pre-existing content carries the signature too — only bytes after the
	// offset (this run's output) may trigger detection.
	if err := os.WriteFile(path, []byte("old run: Failed to create EGL images for DMABufs\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("new run: clean output\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Offset 0 (no snapshot): the stale signature IS matched — the arm-time
	// snapshot is what protects against it.
	if got := scanStderrTail(path, 0); len(got) != 1 {
		t.Fatalf("offset 0: want 1 signature, got %v", got)
	}
	// Offset = file size at arm time: the stale signature is skipped.
	if got := scanStderrTail(path, info.Size()); len(got) != 0 {
		t.Fatalf("arm-time offset: want no signatures, got %v", got)
	}
}

func TestScanStderrTailMatchesSignatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr.log")
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"webkit egl failure", "some prefix Failed to create EGL images for DMABufs: EINVAL\n", 1},
		{"nvidia gem failure", "NVIDIA: DRM_IOCTL_NVIDIA_GEM_ALLOC_NVKMS_MEMORY failed: Invalid argument\n", 1},
		{"gbm buffer failure (webkit 2.54 wording)", "Failed to create GBM buffer of size 1400x900: Недопустимый аргумент\n", 1},
		{"both", "Failed to create EGL images for DMABufs\nDRM_IOCTL_NVIDIA_GEM_ALLOC_NVKMS_MEMORY failed\n", 2},
		{"clean output", "Gtk-WARNING: whatever\n", 0},
		{"lookalike", "failed to create egl images for dmabufs\n", 0}, // case-sensitive on purpose
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := scanStderrTail(path, 0)
			if len(got) != tc.want {
				t.Fatalf("want %d signatures, got %v", tc.want, got)
			}
		})
	}
}

func TestScanStderrTailMissingFileAndTailCap(t *testing.T) {
	// Missing capture: detection silently disarms for the run.
	if got := scanStderrTail(filepath.Join(t.TempDir(), "absent.log"), 0); got != nil {
		t.Fatalf("missing file: want nil, got %v", got)
	}

	// A signature beyond the tail cap is not scanned: the cap bounds
	// pathological growth, and first-paint failures sit at the tail start.
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr.log")
	payload := strings.Repeat("x", int(gpuWatchdogMaxTailBytes)) + "\nFailed to create EGL images for DMABufs\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := scanStderrTail(path, 0); len(got) != 0 {
		t.Fatalf("signature beyond the cap: want nil, got %v", got)
	}
}

// newTestWatchdog builds a fully seam-injected watchdog recording the order
// of persist → relaunch → quit in events.
func newTestWatchdog(t *testing.T, matched []string, persistErr, relaunchErr error) (*webviewGpuWatchdog, *[]string, *WebviewGpuState) {
	t.Helper()
	var events []string
	// Seed the persisted state the way a real load would (fresh auto), then
	// let the watchdog's mutate land on it — mirroring the production
	// load-modify-save inside UpdateWebviewGpuState.
	persisted := WebviewGpuState{GPUPolicy: WebviewGpuPolicyAuto}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	w := &webviewGpuWatchdog{
		agentDir:   t.TempDir(),
		logger:     slog.New(slog.DiscardHandler),
		stderrPath: filepath.Join(t.TempDir(), "stderr.log"),
		nowFn:      func() time.Time { return now },
		scanTailFn: func(string, int64) []string { return matched },
		updateFn: func(mutate func(*WebviewGpuState)) error {
			events = append(events, "persist")
			mutate(&persisted)
			return persistErr
		},
		relaunchFn: func(*slog.Logger) error {
			events = append(events, "relaunch")
			return relaunchErr
		},
		quitFn: func() { events = append(events, "quit") },
	}
	return w, &events, &persisted
}

func TestWatchdogCheckDetectsPersistsRelaunchesQuits(t *testing.T) {
	w, events, persisted := newTestWatchdog(t,
		[]string{"Failed to create EGL images for DMABufs"}, nil, nil)

	if !w.check() {
		t.Fatal("check = false, want true (decision made)")
	}
	if want := []string{"persist", "relaunch", "quit"}; strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", *events, want)
	}
	if persisted.AutoFallback == nil {
		t.Fatal("no fallback persisted")
	}
	if persisted.AutoFallback.Policy != WebviewGpuPolicyNever {
		t.Errorf("fallback policy = %q, want never", persisted.AutoFallback.Policy)
	}
	if persisted.AutoFallback.Reason != GpuFallbackReasonDmabufFailure {
		t.Errorf("fallback reason = %q, want %q", persisted.AutoFallback.Reason, GpuFallbackReasonDmabufFailure)
	}
	if persisted.AutoFallback.DetectedAt != "2026-10-15T12:00:00Z" {
		t.Errorf("detected_at = %q, want the injected now in UTC RFC3339", persisted.AutoFallback.DetectedAt)
	}
}

func TestWatchdogCheckNoMatchKeepsWindowOpen(t *testing.T) {
	w, events, _ := newTestWatchdog(t, nil, nil, nil)
	if w.check() {
		t.Fatal("check = true on a clean tail, want false")
	}
	if len(*events) != 0 {
		t.Fatalf("events = %v, want none", *events)
	}
}

func TestWatchdogCheckPersistFailureNeverRelaunches(t *testing.T) {
	w, events, _ := newTestWatchdog(t,
		[]string{"DRM_IOCTL_NVIDIA_GEM_ALLOC_NVKMS_MEMORY failed"}, errors.New("disk full"), nil)

	// Without the persisted fallback the relaunched instance would resolve
	// the accelerated policy again and grey-window in a loop — relaunching is
	// forbidden on persist failure.
	if !w.check() {
		t.Fatal("check = false, want true (terminal decision)")
	}
	if strings.Join(*events, ",") != "persist" {
		t.Fatalf("events = %v, want [persist] only", *events)
	}
}

func TestWatchdogCheckRelaunchFailureKeepsRunning(t *testing.T) {
	w, events, _ := newTestWatchdog(t,
		[]string{"Failed to create EGL images for DMABufs"}, nil, errors.New("spawn failed"))

	// The fallback IS persisted; only the quit is withheld so the user keeps
	// a live (albeit blank) session and the next start is safe.
	if !w.check() {
		t.Fatal("check = false, want true (terminal decision)")
	}
	if strings.Join(*events, ",") != "persist,relaunch" {
		t.Fatalf("events = %v, want [persist relaunch]", *events)
	}
}

func TestWatchdogCheckSkipsFallbackForExplicitSetting(t *testing.T) {
	w, events, persisted := newTestWatchdog(t,
		[]string{"Failed to create EGL images for DMABufs"}, nil, nil)
	// A Settings write landed inside the detection window: the state the
	// mutate observes under the RMW lock is no longer auto. Writing the
	// fallback anyway would create an inert record the resolver ignores
	// while the UI keeps showing the auto-disabled banner.
	*persisted = WebviewGpuState{GPUPolicy: WebviewGpuPolicyNever}

	if !w.check() {
		t.Fatal("check = false, want true (decision made)")
	}
	// persist → relaunch → quit: the running instance is stuck on the failed
	// accelerated resolution, and the fresh one applies the explicit setting
	// (its own watchdog disarms for a non-auto setting — no relaunch loop).
	if want := []string{"persist", "relaunch", "quit"}; strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", *events, want)
	}
	if persisted.AutoFallback != nil {
		t.Fatalf("an inert fallback record was written despite the explicit setting: %+v", persisted.AutoFallback)
	}
}

// dmabufFallbackFixture lives in webview_gpu_test.go (identical fixture
// shared with the state-machine tests).

// TestWatchdogStartScansImmediately pins the immediate first scan: the
// scanner goroutine must check the tail as it opens, not wait out the first
// gpuWatchdogScanInterval tick. The failure signature prints at the webview's
// first paint — before DomReady — so every tick of delay is grey window on
// the fallback relaunch path. The 1s bound is deliberately below the 2s tick:
// a scan that only fired from the ticker could not arrive inside it.
func TestWatchdogStartScansImmediately(t *testing.T) {
	scanned := make(chan struct{}, 1)
	w := &webviewGpuWatchdog{
		agentDir:   t.TempDir(),
		logger:     slog.New(slog.DiscardHandler),
		stderrPath: filepath.Join(t.TempDir(), "stderr.log"),
		scanTailFn: func(string, int64) []string {
			select {
			case scanned <- struct{}{}:
			default:
			}
			return nil
		},
	}
	w.start()

	select {
	case <-scanned:
	case <-time.After(1 * time.Second):
		t.Fatal("the scanner's first scan did not fire immediately on start")
	}
}

// TestWatchdogStartFirstScanDecidesWithoutTick pins the full decision flow on
// the immediate first scan: a signature already in the tail (the common case
// — the failure printed before DomReady) must persist, relaunch and quit
// without waiting out a single ticker tick.
func TestWatchdogStartFirstScanDecidesWithoutTick(t *testing.T) {
	var events []string
	record := func(e string) { events = append(events, e) }
	done := make(chan struct{})
	persisted := WebviewGpuState{GPUPolicy: WebviewGpuPolicyAuto}
	w := &webviewGpuWatchdog{
		agentDir:   t.TempDir(),
		logger:     slog.New(slog.DiscardHandler),
		stderrPath: filepath.Join(t.TempDir(), "stderr.log"),
		nowFn:      func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
		scanTailFn: func(string, int64) []string {
			return []string{"Failed to create EGL images for DMABufs"}
		},
		updateFn: func(mutate func(*WebviewGpuState)) error {
			record("persist")
			mutate(&persisted)
			return nil
		},
		relaunchFn: func(*slog.Logger) error {
			record("relaunch")
			return nil
		},
		quitFn: func() {
			record("quit")
			close(done)
		},
	}
	w.start()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("the immediate first scan did not decide; events", events)
	}
	if want := []string{"persist", "relaunch", "quit"}; strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", events, want)
	}
	if persisted.AutoFallback == nil {
		t.Fatal("no fallback persisted by the immediate first scan")
	}
}

func TestNewWebviewGpuWatchdogArming(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	agentDir := t.TempDir()
	logDir := t.TempDir()
	stderrPath := crashlog.StderrLogPath(logDir)
	if err := os.WriteFile(stderrPath, []byte("startup chatter\n"), 0o600); err != nil {
		t.Fatalf("write stderr fixture: %v", err)
	}

	writeState := func(t *testing.T, st WebviewGpuState) {
		t.Helper()
		if err := SaveWebviewGpuState(agentDir, st); err != nil {
			t.Fatalf("save state: %v", err)
		}
	}

	t.Run("fresh auto arms with the stderr offset", func(t *testing.T) {
		w := newWebviewGpuWatchdog("linux", "", agentDir, logDir, true, logger)
		if w == nil {
			t.Fatal("expected armed watchdog for fresh auto")
		}
		if w.stderrPath != stderrPath {
			t.Errorf("stderrPath = %q, want %q", w.stderrPath, stderrPath)
		}
		if w.startOffset != int64(len("startup chatter\n")) {
			t.Errorf("startOffset = %d, want the pre-existing file size", w.startOffset)
		}
	})

	// Review finding #155.2: without a usable stderr capture there is nothing
	// to scan — detection could never fire, so the watchdog disarms (the
	// launch policy is demoted to never by the same fact).
	t.Run("unobserved stderr disarms", func(t *testing.T) {
		if w := newWebviewGpuWatchdog("linux", "", agentDir, logDir, false, logger); w != nil {
			t.Fatal("expected disarmed watchdog without a usable stderr capture")
		}
	})

	t.Run("active fallback disarms", func(t *testing.T) {
		writeState(t, WebviewGpuState{AutoFallback: dmabufFallbackFixture()})
		if w := newWebviewGpuWatchdog("linux", "", agentDir, logDir, true, logger); w != nil {
			t.Fatal("expected disarmed watchdog with an active fallback")
		}
	})

	t.Run("explicit setting disarms", func(t *testing.T) {
		writeState(t, WebviewGpuState{GPUPolicy: WebviewGpuPolicyNever})
		if w := newWebviewGpuWatchdog("linux", "", agentDir, logDir, true, logger); w != nil {
			t.Fatal("expected disarmed watchdog for an explicit setting")
		}
	})

	t.Run("explicit env override disarms", func(t *testing.T) {
		writeState(t, WebviewGpuState{})
		if w := newWebviewGpuWatchdog("linux", "on-demand", agentDir, logDir, true, logger); w != nil {
			t.Fatal("expected disarmed watchdog for an explicit env override")
		}
	})

	t.Run("invalid env stays armed", func(t *testing.T) {
		if w := newWebviewGpuWatchdog("linux", "  turbo ", agentDir, logDir, true, logger); w == nil {
			t.Fatal("expected armed watchdog for an invalid env value (fall-through to auto)")
		}
	})

	t.Run("non-linux never arms", func(t *testing.T) {
		writeState(t, WebviewGpuState{})
		if w := newWebviewGpuWatchdog("darwin", "", agentDir, logDir, true, logger); w != nil {
			t.Fatal("expected nil watchdog on darwin")
		}
	})
}

// TestWaitForGpuRelaunchHandover pins the relaunched child's handover contract
// (review finding #155.1): while the predecessor retains the single-instance
// lock past the budget — a legitimate teardown outlasting the old fixed 15 s
// timeout — the handover FAILS with an error instead of failing open, because
// a child that proceeds into Wails startup then relays its launch to the
// quitting instance and exits: a one-shot relay, no replacement window. main
// treats that error as terminal (no Wails startup). Once the predecessor has
// actually exited, the same handover succeeds.
func TestWaitForGpuRelaunchHandover(t *testing.T) {
	path := config.SingleInstanceLockPath(t.TempDir())

	// Predecessor still running: the short budget expires with the lock held.
	lock, held, err := instancelock.AcquireSingleInstanceLock(path)
	if err != nil || !held {
		t.Fatalf("acquire (predecessor): held=%v err=%v", held, err)
	}
	handoverErr := WaitForGpuRelaunchHandover(path, 300*time.Millisecond, discardLogger())
	if handoverErr == nil {
		t.Fatal("handover succeeded while the predecessor holds the lock; the replacement would become a one-shot relay")
	}
	if !strings.Contains(handoverErr.Error(), "300ms") {
		t.Errorf("handover error %q should name the exhausted budget", handoverErr.Error())
	}

	// Predecessor exits mid-handover-window: the child proceeds.
	if err := lock.Close(); err != nil {
		t.Fatalf("close (predecessor exit): %v", err)
	}
	if err := WaitForGpuRelaunchHandover(path, time.Second, discardLogger()); err != nil {
		t.Fatalf("handover after the predecessor exited: %v", err)
	}
}

// TestGpuRelaunchHandoverBudgetSizedFromShutdownDeadline pins the budget's
// deadline mapping: a positive config value passes through, an unset (0) or
// nonsense value resolves to the built-in cumulative default. The
// GPU-relaunch handover waits exactly this long (plus margin) for the
// predecessor — sized against anything shorter, a legitimate slow teardown
// (the ~46 s embedded-LLM stop) would outlast the wait and lose the
// replacement window (review finding #155.1). The deadline mapping itself is
// pinned in core/shutdown; here only the handover-budget wiring over the
// shutdown deadline is verified.
func TestGpuRelaunchHandoverBudgetSizedFromShutdownDeadline(t *testing.T) {
	if got := GpuRelaunchHandoverBudget(0); got != shutdown.DefaultHardDeadline()+gpuRelaunchHandoverMargin {
		t.Fatalf("budget from the default deadline = %v, want %v", got, shutdown.DefaultHardDeadline()+gpuRelaunchHandoverMargin)
	}
	if got := GpuRelaunchHandoverBudget(2 * time.Minute); got != 2*time.Minute+gpuRelaunchHandoverMargin {
		t.Fatalf("budget from a configured deadline = %v, want %v", got, 2*time.Minute+gpuRelaunchHandoverMargin)
	}
	// The default budget must exceed the handover timeout that preceded this
	// fix — the regression this test exists for.
	if GpuRelaunchHandoverBudget(0) <= 15*time.Second {
		t.Fatalf("default budget %v does not exceed the legacy 15s handover timeout", GpuRelaunchHandoverBudget(0))
	}
}
