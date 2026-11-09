package desktop

import (
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/core/webviewgpu"
)

// wailsWebviewGpuPolicy converts the policy into the linux.WebviewGpuPolicy
// value handed to Wails, compensating for the pinned Wails v2.16.0's
// Go-enum/C-layer mismatch: window.c (SetupWebview) maps the integer as
// 0→ALWAYS, 1→ON_DEMAND, 2→NEVER while the Go enum orders OnDemand=0,
// Always=1, Never=2 — like-named constants would silently swap always and
// on-demand at runtime. This is the single place aware of the mismatch;
// re-verify against window.c on every Wails upgrade and update
// webview_gpu_policy_test.go (numeric + enum-order canary pins).
// See specs/domains/webview-gpu-policy.md.
func wailsWebviewGpuPolicy(p webviewgpu.Policy) linux.WebviewGpuPolicy {
	switch p {
	case webviewgpu.PolicyAlways:
		return linux.WebviewGpuPolicyOnDemand
	case webviewgpu.PolicyOnDemand:
		return linux.WebviewGpuPolicyAlways
	default:
		return linux.WebviewGpuPolicyNever
	}
}

// WebviewGpuPolicyOptions returns the Linux-specific Wails options carrying
// the hardware-acceleration policy for this launch, or nil off Linux (the
// option only feeds the WebKitGTK frontend, so macOS and Windows keep
// behaving exactly as before). stderrObserved reports whether this launch has
// a usable stderr capture: without one, fresh auto is demoted to never
// (DeriveWebviewGpuRun) because the failure watchdog could never detect the
// resulting grey window. See specs/domains/webview-gpu-policy.md.
func WebviewGpuPolicyOptions(agentDir string, stderrObserved bool, logger *slog.Logger) *linux.Options {
	return webviewGpuPolicyOptions(runtime.GOOS, agentDir, stderrObserved, logger)
}

func webviewGpuPolicyOptions(goos, agentDir string, stderrObserved bool, logger *slog.Logger) *linux.Options {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if goos != "linux" {
		if raw := os.Getenv(backend.WebviewGpuPolicyEnvVar); strings.TrimSpace(raw) != "" {
			logger.Debug("webview GPU policy override ignored: Linux-only option",
				"env", backend.WebviewGpuPolicyEnvVar, "value", raw, "goos", goos)
		}
		return nil
	}
	// Single launch-time read of the state file: the consumed setting rides
	// along, and a second read could observe a different state than the first.
	run := backend.DeriveWebviewGpuRun(goos, agentDir, stderrObserved, logger)
	resolved := webviewgpu.FromCanonical(run.Policy)
	switch run.Source {
	case backend.WebviewGpuSourceEnv:
		logger.Info("webview GPU policy override",
			"env", backend.WebviewGpuPolicyEnvVar, "value", run.Policy)
	case backend.WebviewGpuSourceAutoUnobserved:
		// Explicit Warn for the demotion: a silent accelerated default with no
		// failure detection would surface as a broken grey window first; the
		// user keeps an explicit override (env or setting) to force
		// acceleration anyway.
		logger.Warn("webview GPU policy pinned to never: fresh auto without a usable stderr capture "+
			"(the DMABUF watchdog cannot observe this run; set C0WRK_WEBVIEW_GPU_POLICY or the Settings choice to override)",
			"policy", run.Policy, "setting", run.Setting)
	default:
		logger.Debug("webview GPU policy resolved",
			"policy", run.Policy, "source", run.Source, "setting", run.Setting)
	}
	return &linux.Options{WebviewGpuPolicy: wailsWebviewGpuPolicy(resolved)}
}
