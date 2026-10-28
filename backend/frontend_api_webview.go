package backend

import (
	"runtime"
)

// WebviewGpuPolicyInfo is the Get/SetWebviewGpuPolicy wire shape (issue #34); Linux-only
// in effect. See specs/contracts/desktop-frontend.md.
type WebviewGpuPolicyInfo struct {
	// Setting is the persisted user choice; "auto" when nothing was chosen yet.
	Setting string `json:"setting"`
	// Running is the canonical policy this launch resolved to; RunningSource names
	// the layer that decided it. See specs/domains/webview-gpu-policy.md.
	Running       string `json:"running"`
	RunningSource string `json:"running_source"`
	// Fallback is non-nil while the watchdog has pinned auto to software rendering.
	Fallback *WebviewGpuAutoFallback `json:"fallback,omitempty"`
	// RestartRequired reports whether a restart would move the running policy to
	// what the current setting resolves to; always false under an env override.
	RestartRequired bool `json:"restart_required"`
	LinuxOnly       bool `json:"linux_only"`
}

// GetWebviewGpuPolicy reports the webview GPU-policy state.
func (f *FrontendAPI) GetWebviewGpuPolicy() WebviewGpuPolicyInfo {
	return f.webviewGpuPolicyInfo()
}

// SetWebviewGpuPolicy persists the user's choice (auto | always | on-demand | never);
// ANY explicit choice clears the watchdog fallback — re-choosing auto is the "retry
// accelerated rendering" action; applies on the next app start. See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) SetWebviewGpuPolicy(setting string) (WebviewGpuPolicyInfo, error) {
	normalized, err := NormalizeWebviewGpuSetting(setting)
	if err != nil {
		return WebviewGpuPolicyInfo{}, err
	}
	// Serialized against the watchdog's fallback persist via the webview_state
	// lock: both writers rewrite the whole file, so an unlocked last-writer-wins
	// could drop either side's change. Any future writer of webview_state.json
	// must take the same lock.
	// See specs/domains/webview-gpu-policy.md.
	err = UpdateWebviewGpuState(f.agentDir, f.logger, func(state *WebviewGpuState) {
		state.GPUPolicy = normalized
		state.AutoFallback = nil
	})
	if err != nil {
		return WebviewGpuPolicyInfo{}, err
	}
	f.log().Info("webview GPU policy setting changed", "setting", normalized)
	return f.webviewGpuPolicyInfo(), nil
}

// webviewGpuPolicyInfo assembles the wire shape: the live state file (the
// pending truth) against the launch-time running snapshot.
func (f *FrontendAPI) webviewGpuPolicyInfo() WebviewGpuPolicyInfo {
	f.configMu.RLock()
	running, runningSource := f.webviewGpuRunning, f.webviewGpuRunningSource
	f.configMu.RUnlock()

	state := LoadWebviewGpuState(f.agentDir, f.logger)
	pending, _ := ResolveWebviewGpuPolicy(state.Setting(), state.FallbackActive())
	return WebviewGpuPolicyInfo{
		Setting:         state.Setting(),
		Running:         running,
		RunningSource:   runningSource,
		Fallback:        state.AutoFallback,
		RestartRequired: runningSource != WebviewGpuSourceEnv && pending != running,
		LinuxOnly:       runtime.GOOS == "linux",
	}
}
