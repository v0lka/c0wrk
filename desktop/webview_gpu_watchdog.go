package desktop

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/sp4rk/safeio"
)

// Set by the watchdog's relaunch on the child process; mainImpl consumes it
// before the single-instance gate so the spawn-then-quit ordering cannot race
// the fresh instance into the Wails single-instance relay (which would exit
// it). See specs/domains/webview-gpu-policy.md.
const GpuRelaunchWaitEnvVar = "C0WRK_GPU_RELAUNCH_WAIT_LOCK"

// Slack past the predecessor's shutdown hard deadline: the forced exit and the
// kernel dropping the advisory lock are bounded and fast; this covers only the
// scheduling tail. See specs/domains/webview-gpu-policy.md.
const gpuRelaunchHandoverMargin = 10 * time.Second

// Watchdog timing (specs/domains/webview-gpu-policy.md): the DMABUF failure
// prints at first paint, before DomReady, so the immediate first scan almost
// always decides; the grey window is a renderer-init failure, hence a bounded
// startup window (no hostile self-relaunch while the user works).
const (
	gpuWatchdogScanInterval     = 2 * time.Second        // rescan period after the immediate first scan
	gpuWatchdogScanWindow       = 30 * time.Second       // detection is startup-only
	gpuWatchdogMaxTailBytes     = int64(1 << 20)         // signatures sit at the start of this run's output
	gpuRelaunchLockPollInterval = 100 * time.Millisecond // relaunched child's lock-wait poll period
)

// gpuDmabufFailureSignatures are the stderr markers of the NVIDIA GBM/DMABUF
// grey-window failure class (wails#2977, issue #34), substring-matched against
// this run's captured stderr tail. Extend as new failure wording is reported:
// a miss leaves the grey window up, a false match only forces software
// rendering — both recoverable. See specs/domains/webview-gpu-policy.md.
var gpuDmabufFailureSignatures = []string{
	"Failed to create EGL images for DMABufs",
	"DRM_IOCTL_NVIDIA_GEM_ALLOC_NVKMS_MEMORY failed",
	"Failed to create GBM buffer",
}

// GpuRelaunchHandoverBudget sizes the relaunched child's pre-startup wait: the
// shutdown hard deadline (the predecessor's worst legitimate teardown) plus
// gpuRelaunchHandoverMargin — a child that gives up waiting early relays its
// launch to the still-quitting instance and exits, losing the replacement
// window. Unset (≤ 0) resolves to the built-in default.
// See specs/domains/webview-gpu-policy.md.
func GpuRelaunchHandoverBudget(shutdownDeadline time.Duration) time.Duration {
	if shutdownDeadline <= 0 {
		shutdownDeadline = defaultShutdownHardDeadline
	}
	return shutdownDeadline + gpuRelaunchHandoverMargin
}

// WaitForGpuRelaunchHandover is the relaunched child's pre-startup handover
// (issue #34): it waits up to budget for the dying instance's advisory
// single-instance lock to release — proof the predecessor has exited. A budget
// expiry is a TERMINAL error: entering Wails startup against a held lock would
// make this process a one-shot relay (its launch data forwarded to the
// quitting instance) and leave no window at all — failing loudly keeps that
// visible and bounded. See specs/domains/webview-gpu-policy.md.
func WaitForGpuRelaunchHandover(lockPath string, budget time.Duration, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.Info("GPU-fallback relaunch: waiting for the previous instance to exit before starting up",
		"lock", lockPath, "budget", budget.String())
	if WaitForInstanceLockRelease(lockPath, budget) {
		return nil
	}
	return fmt.Errorf("GPU-fallback relaunch handover: the previous instance still holds the single-instance lock after %s", budget)
}

// webviewGpuWatchdog detects a DMABUF renderer failure at startup under a
// fresh-auto accelerated resolution, persists the auto-fallback to never, and
// relaunches so the fresh instance starts safe. Detection covers fresh auto
// only — an explicit choice (setting or env) is never second-guessed, a
// fallen-back instance has nothing to detect — re-checked under the RMW lock
// at write time. See specs/domains/webview-gpu-policy.md.
type webviewGpuWatchdog struct {
	agentDir    string
	logger      *slog.Logger
	stderrPath  string
	startOffset int64
	started     atomic.Bool

	// Seams for tests; nil means the production implementation.
	scanTailFn func(path string, offset int64) []string
	updateFn   func(mutate func(*backend.WebviewGpuState)) error
	relaunchFn func(logger *slog.Logger) error
	nowFn      func() time.Time
	quitFn     func()
}

// NewWebviewGpuWatchdog arms the watchdog for this launch, or returns nil when
// detection does not apply (see gpuFailureDetectionApplies). Must be called
// after crashlog.Install (the offset snapshot needs the redirected fds) and
// before wails.Run (the webview must not render before the offset is taken).
func NewWebviewGpuWatchdog(agentDir, logDir string, stderrObserved bool, logger *slog.Logger) *webviewGpuWatchdog {
	return newWebviewGpuWatchdog(runtime.GOOS, os.Getenv(backend.WebviewGpuPolicyEnvVar), agentDir, logDir, stderrObserved, logger)
}

func newWebviewGpuWatchdog(goos, envRaw, agentDir, logDir string, stderrObserved bool, logger *slog.Logger) *webviewGpuWatchdog {
	if goos != "linux" {
		return nil
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if !gpuFailureDetectionApplies(envRaw, agentDir, logDir, stderrObserved, logger) {
		return nil
	}
	w := &webviewGpuWatchdog{
		agentDir:   agentDir,
		logger:     logger,
		stderrPath: crashlog.StderrLogPath(logDir),
	}
	// Everything appended after this point is this run's output (stderr.log
	// is append-only; rotation happens only at install time).
	if info, err := os.Stat(w.stderrPath); err == nil {
		w.startOffset = info.Size()
	}
	logger.Debug("webview GPU watchdog armed",
		"stderr", w.stderrPath, "start_offset", w.startOffset)
	return w
}

// gpuFailureDetectionApplies reports whether GPU-failure detection (the in-run
// watchdog and the next-start post-mortem alike) may act on this Linux launch:
// usable stderr capture, fresh auto (no explicit choice, no active fallback),
// no valid env override. See specs/domains/webview-gpu-policy.md.
func gpuFailureDetectionApplies(envRaw, agentDir, logDir string, stderrObserved bool, logger *slog.Logger) bool {
	if !stderrObserved {
		// Nothing to scan — policy resolution has already pinned fresh auto
		// to never for the same reason.
		logger.Debug("webview GPU watchdog disarmed: no usable stderr capture",
			"stderr", crashlog.StderrLogPath(logDir))
		return false
	}
	state := backend.LoadWebviewGpuState(agentDir, logger)
	if state.Setting() != backend.WebviewGpuPolicyAuto || state.FallbackActive() {
		logger.Debug("webview GPU watchdog disarmed: detection covers fresh auto only",
			"setting", state.Setting(), "fallback", state.FallbackActive())
		return false
	}
	// An invalid env value is not an explicit choice: fall through, stay armed.
	if strings.TrimSpace(envRaw) != "" {
		if _, err := backend.NormalizeWebviewGpuPolicy(envRaw); err == nil {
			logger.Debug("webview GPU watchdog disarmed: explicit env override",
				"env", backend.WebviewGpuPolicyEnvVar, "value", strings.TrimSpace(envRaw))
			return false
		}
	}
	return true
}

// start runs the bounded startup scan. Idempotent: DomReady fires again on
// every frontend reload, and a second scanner would only double-scan.
func (w *webviewGpuWatchdog) start() {
	if w == nil || !w.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		// First scan fires before the first tick: the DMABUF failure prints
		// at first paint, before DomReady, so the tail already carries the
		// signature — waiting for the ticker would add a full scan interval
		// of grey window to every fallback relaunch.
		if w.check() {
			return
		}
		ticker := time.NewTicker(gpuWatchdogScanInterval)
		defer ticker.Stop()
		deadline := time.After(gpuWatchdogScanWindow)
		for {
			select {
			case <-ticker.C:
				if w.check() {
					return
				}
			case <-deadline:
				w.logger.Debug("webview GPU watchdog disarmed: no DMABUF failure within the startup window",
					"window", gpuWatchdogScanWindow.String())
				return
			}
		}
	}()
}

// check scans this run's stderr tail once. It returns true when the watchdog
// has decided (failure detected and the fallback flow ran, or a terminal
// error made further scans pointless) and false when the scan found nothing
// and the window stays open.
func (w *webviewGpuWatchdog) check() bool {
	matched := w.scanTail()
	if len(matched) == 0 {
		return false
	}
	w.logger.Warn("webview GPU fallback: DMABUF renderer failure detected; pinning auto to software rendering",
		"signatures", matched, "stderr", w.stderrPath)

	// Persist BEFORE relaunching: the record is what makes the fresh instance
	// resolve auto to never, so a failed persist must not relaunch (the fresh
	// instance would grey-window in a loop). The load-modify-save runs under
	// backend's webview_state RMW lock.
	detectedAt := w.now().UTC().Format(time.RFC3339)
	skipFallback := false
	if err := w.updateState(func(state *backend.WebviewGpuState) {
		// Re-check fresh-auto under the RMW lock: a Settings choice landing
		// inside the detection window suppresses the fallback record (writing
		// it anyway would create an inert record ResolveWebviewGpuPolicy
		// ignores), but the relaunch still runs — the fresh instance applies
		// the explicit setting (its own watchdog disarms; no loop).
		if state.Setting() != backend.WebviewGpuPolicyAuto {
			skipFallback = true
			return
		}
		state.AutoFallback = &backend.WebviewGpuAutoFallback{
			Policy:     backend.WebviewGpuPolicyNever,
			Reason:     backend.GpuFallbackReasonDmabufFailure,
			DetectedAt: detectedAt,
		}
	}); err != nil {
		w.logger.Error("webview GPU fallback: persisting fallback state failed; NOT relaunching "+
			"(the relaunched instance would resolve the accelerated policy again)",
			"error", err)
		return true
	}
	if err := w.relaunch(); err != nil {
		// Keep this instance alive: the fallback (or explicit setting) applies
		// on the next start either way.
		if skipFallback {
			w.logger.Error("webview GPU fallback: relaunch failed; restart manually to apply the explicit setting",
				"error", err)
		} else {
			w.logger.Error("webview GPU fallback: relaunch failed; the fallback applies on the next start",
				"error", err)
		}
		return true
	}
	if skipFallback {
		w.logger.Info("webview GPU fallback: the setting left auto during detection; relaunching on the user's explicit choice",
			"reason", backend.GpuFallbackReasonDmabufFailure)
	} else {
		w.logger.Info("webview GPU fallback: relaunching with software rendering",
			"reason", backend.GpuFallbackReasonDmabufFailure)
	}
	w.quit()
	return true
}

func (w *webviewGpuWatchdog) scanTail() []string {
	if w.scanTailFn != nil {
		return w.scanTailFn(w.stderrPath, w.startOffset)
	}
	return scanStderrTail(w.stderrPath, w.startOffset)
}

// updateState persists through backend's serialized webview-state update, so
// the load-modify-save is atomic with respect to the Settings RPC
// (backend.UpdateWebviewGpuState).
func (w *webviewGpuWatchdog) updateState(mutate func(*backend.WebviewGpuState)) error {
	if w.updateFn != nil {
		return w.updateFn(mutate)
	}
	return backend.UpdateWebviewGpuState(w.agentDir, w.logger, mutate)
}

func (w *webviewGpuWatchdog) relaunch() error {
	if w.relaunchFn != nil {
		return w.relaunchFn(w.logger)
	}
	return relaunchDetachedSelf(w.logger)
}

func (w *webviewGpuWatchdog) now() time.Time {
	if w.nowFn != nil {
		return w.nowFn()
	}
	return time.Now()
}

func (w *webviewGpuWatchdog) quit() {
	if w.quitFn != nil {
		w.quitFn()
	}
}

// scanStderrTail reads at most gpuWatchdogMaxTailBytes of stderrPath
// starting at startOffset (this run's first byte) and returns the failure
// signatures it contains. Read errors, a missing capture file, and a
// not-yet-grown tail all return nil — a missing capture disarms detection
// for the run rather than failing it.
func scanStderrTail(path string, startOffset int64) []string {
	file, err := safeio.OpenFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() <= startOffset {
		return nil
	}
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, gpuWatchdogMaxTailBytes))
	if err != nil {
		return nil
	}
	var matched []string
	for _, sig := range gpuDmabufFailureSignatures {
		if bytes.Contains(data, []byte(sig)) {
			matched = append(matched, sig)
		}
	}
	return matched
}
