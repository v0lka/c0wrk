package desktop

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/crashlog"
	"github.com/v0lka/sp4rk/safeio"
)

// Together the markers identify GDK's fatal Wayland EPROTO dispatch failure
// (the strerror half is localized, the format half is not; other errnos are
// not a GPU-policy signal). See specs/domains/webview-gpu-policy.md.
const (
	gpuWaylandProtocolErrorMarker = "dispatching to Wayland display"
	gpuWaylandEPROTO              = "Error 71 ("
)

// ApplyWebviewGpuCrashFallback is the next-start post-mortem for the GPU
// failure class the in-run watchdog cannot see: when the previous run died
// on a fatal Wayland protocol error, the auto-fallback to never persists
// before policy resolution (specs/domains/webview-gpu-policy.md).
func ApplyWebviewGpuCrashFallback(agentDir, logDir string, stderrObserved bool, logger *slog.Logger) {
	applyWebviewGpuCrashFallback(runtime.GOOS, os.Getenv(backend.WebviewGpuPolicyEnvVar),
		agentDir, logDir, stderrObserved, logger, time.Now)
}

func applyWebviewGpuCrashFallback(goos, envRaw, agentDir, logDir string, stderrObserved bool, logger *slog.Logger, now func() time.Time) {
	if goos != "linux" {
		return
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if !gpuFailureDetectionApplies(envRaw, agentDir, logDir, stderrObserved, logger) {
		return
	}
	stderrPath := crashlog.StderrLogPath(logDir)
	if !previousRunDiedOnWaylandProtocolError(stderrPath) {
		return
	}
	logger.Warn("webview GPU fallback: previous run died on a fatal Wayland protocol error; pinning auto to software rendering",
		"stderr", stderrPath)
	detectedAt := now().UTC().Format(time.RFC3339)
	if err := backend.UpdateWebviewGpuState(agentDir, logger, func(state *backend.WebviewGpuState) {
		if state.Setting() != backend.WebviewGpuPolicyAuto || state.FallbackActive() {
			return
		}
		state.AutoFallback = &backend.WebviewGpuAutoFallback{
			Policy:     backend.WebviewGpuPolicyNever,
			Reason:     backend.GpuFallbackReasonWaylandProtocolError,
			DetectedAt: detectedAt,
		}
	}); err != nil {
		logger.Error("webview GPU fallback: persisting fallback state failed; this launch stays on the accelerated policy",
			"error", err)
	}
}

// previousRunDiedOnWaylandProtocolError splits the stderr.log tail into
// runs by the crashlog banners and inspects the run before the current one
// (specs/domains/webview-gpu-policy.md).
func previousRunDiedOnWaylandProtocolError(path string) bool {
	data := readStderrTail(path, gpuWatchdogMaxTailBytes)
	runBanner := []byte(crashlog.RunBannerPrefix)
	current := bytes.LastIndex(data, runBanner)
	if current < 0 {
		return false
	}
	start := max(bytes.LastIndex(data[:current], runBanner), 0)
	prev := data[start:current]
	if bytes.Contains(prev, []byte(crashlog.ExitBannerPrefix)) {
		return false
	}
	for line := range bytes.SplitSeq(prev, []byte("\n")) {
		if bytes.Contains(line, []byte(gpuWaylandProtocolErrorMarker)) && bytes.Contains(line, []byte(gpuWaylandEPROTO)) {
			return true
		}
	}
	return false
}

// readStderrTail returns at most maxBytes from the end of path; nil on any
// read error.
func readStderrTail(path string, maxBytes int64) []byte {
	file, err := safeio.OpenFileNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if offset := info.Size() - maxBytes; offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil
	}
	return data
}
