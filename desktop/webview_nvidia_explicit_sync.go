package desktop

import (
	"log/slog"
	"os"
	"runtime"
)

// nvidiaDisableExplicitSyncEnvVar is read by NVIDIA's EGL Wayland platform
// (libnvidia-egl-wayland / -wayland2) when it sets up a window surface.
const nvidiaDisableExplicitSyncEnvVar = "__NV_DISABLE_EXPLICIT_SYNC"

// ApplyNvidiaExplicitSyncWorkaround disables NVIDIA's Wayland explicit sync
// for this process and its WebKit children unless the operator set the
// variable already (the WebKitGTK/mutter shm-attach kill it removes: see
// specs/domains/webview-gpu-policy.md).
func ApplyNvidiaExplicitSyncWorkaround(logger *slog.Logger) {
	applyNvidiaExplicitSyncWorkaround(runtime.GOOS, os.LookupEnv, os.Setenv, logger)
}

func applyNvidiaExplicitSyncWorkaround(goos string, lookup func(string) (string, bool), setenv func(string, string) error, logger *slog.Logger) {
	if goos != "linux" {
		return
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if v, ok := lookup(nvidiaDisableExplicitSyncEnvVar); ok {
		logger.Debug("NVIDIA explicit-sync workaround skipped: variable set by the environment",
			"env", nvidiaDisableExplicitSyncEnvVar, "value", v)
		return
	}
	if err := setenv(nvidiaDisableExplicitSyncEnvVar, "1"); err != nil {
		logger.Warn("NVIDIA explicit-sync workaround not applied",
			"env", nvidiaDisableExplicitSyncEnvVar, "error", err)
		return
	}
	logger.Debug("NVIDIA explicit-sync workaround applied", "env", nvidiaDisableExplicitSyncEnvVar)
}
