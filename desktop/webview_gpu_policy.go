package desktop

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

// WebviewGpuPolicyEnvVar overrides the Linux webview hardware-acceleration
// policy. Accepted values are case-insensitive and whitespace-tolerant:
//
//	always     hardware acceleration always on
//	on-demand  hardware acceleration as requested by web contents
//	           ("ondemand" is accepted as an alias)
//	never      hardware acceleration always off
//
// Unset or invalid values keep the default "never" policy — the same policy
// Wails applies as its workaround for https://github.com/wailsapp/wails/issues/2977
// when an application passes no Linux options at all. The variable has no
// effect on macOS and Windows (nil Linux options there).
const WebviewGpuPolicyEnvVar = "C0WRK_WEBVIEW_GPU_POLICY"

// webviewGpuPolicy is the effective hardware-acceleration policy for the
// Linux webview, deliberately decoupled from Wails' enum ordering (see
// wailsWebviewGpuPolicy for why the conversion is not the identity).
type webviewGpuPolicy int

const (
	gpuPolicyNever webviewGpuPolicy = iota
	gpuPolicyAlways
	gpuPolicyOnDemand
)

// String returns the canonical WebviewGpuPolicyEnvVar spelling of the policy.
func (p webviewGpuPolicy) String() string {
	switch p {
	case gpuPolicyAlways:
		return "always"
	case gpuPolicyOnDemand:
		return "on-demand"
	default:
		return "never"
	}
}

// parseWebviewGpuPolicy parses a WebviewGpuPolicyEnvVar value.
func parseWebviewGpuPolicy(raw string) (webviewGpuPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "always":
		return gpuPolicyAlways, nil
	case "on-demand", "ondemand":
		return gpuPolicyOnDemand, nil
	case "never":
		return gpuPolicyNever, nil
	default:
		return gpuPolicyNever, fmt.Errorf("unknown webview GPU policy %q: want always, on-demand, or never", raw)
	}
}

// wailsWebviewGpuPolicy converts the policy into the linux.WebviewGpuPolicy
// value to hand to Wails.
//
// Wails v2.15.0 (the version pinned in go.mod) carries a mismatch between
// its Go enum and its C layer: internal/frontend/desktop/linux/window.c
// (SetupWebview) maps the integer to WebKit policies as 0→ALWAYS,
// 1→ON_DEMAND, 2→NEVER, while the Go enum orders OnDemand=0, Always=1,
// Never=2 — so passing the like-named constants silently swaps always and
// on-demand at runtime. The mapping below compensates for that, so the
// policy named on the env var is the one WebKit actually receives:
//
//	desired always    → Go OnDemand(0) → C case 0 → WEBKIT_…POLICY_ALWAYS
//	desired on-demand → Go Always(1)   → C case 1 → WEBKIT_…POLICY_ON_DEMAND
//	desired never     → Go Never(2)    → C case 2 → WEBKIT_…POLICY_NEVER
//
// Re-verify against window.c whenever Wails is upgraded: if upstream ever
// aligns the C switch with the Go enum, the two non-default arms here must
// be swapped back (or dropped) and the tests in webview_gpu_policy_test.go
// updated to match.
func (p webviewGpuPolicy) wailsWebviewGpuPolicy() linux.WebviewGpuPolicy {
	switch p {
	case gpuPolicyAlways:
		return linux.WebviewGpuPolicyOnDemand
	case gpuPolicyOnDemand:
		return linux.WebviewGpuPolicyAlways
	default:
		return linux.WebviewGpuPolicyNever
	}
}

// WebviewGpuPolicyOptions returns the Linux-specific Wails options carrying
// the hardware-acceleration policy requested through WebviewGpuPolicyEnvVar.
// It returns nil on non-Linux platforms — the option only feeds the
// WebKitGTK frontend, so macOS and Windows keep behaving exactly as before.
// With the variable unset it pins WebviewGpuPolicyNever explicitly: the same
// policy Wails applies via its nil-options workaround, made deliberate here
// so a future Wails default change cannot silently alter rendering.
func WebviewGpuPolicyOptions(logger *slog.Logger) *linux.Options {
	return webviewGpuPolicyOptions(runtime.GOOS, os.Getenv(WebviewGpuPolicyEnvVar), logger)
}

func webviewGpuPolicyOptions(goos, raw string, logger *slog.Logger) *linux.Options {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if goos != "linux" {
		if strings.TrimSpace(raw) != "" {
			logger.Debug("webview GPU policy override ignored: Linux-only option",
				"env", WebviewGpuPolicyEnvVar, "value", raw, "goos", goos)
		}
		return nil
	}
	if strings.TrimSpace(raw) == "" {
		logger.Debug("webview GPU policy default",
			"env", WebviewGpuPolicyEnvVar, "policy", gpuPolicyNever.String())
		return &linux.Options{WebviewGpuPolicy: linux.WebviewGpuPolicyNever}
	}
	policy, err := parseWebviewGpuPolicy(raw)
	if err != nil {
		logger.Warn("invalid webview GPU policy override; using default",
			"env", WebviewGpuPolicyEnvVar, "value", raw,
			"policy", gpuPolicyNever.String(), "error", err)
		return &linux.Options{WebviewGpuPolicy: linux.WebviewGpuPolicyNever}
	}
	logger.Info("webview GPU policy override",
		"env", WebviewGpuPolicyEnvVar, "value", policy.String())
	return &linux.Options{WebviewGpuPolicy: policy.wailsWebviewGpuPolicy()}
}
