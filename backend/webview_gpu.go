package backend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/safeio"
)

// WebviewGpuPolicyEnvVar overrides the Linux webview GPU policy with the
// highest precedence: an explicit always/on-demand/never (case-insensitive,
// "ondemand" aliased). Unset or invalid falls through to the persisted
// setting; no effect off Linux. See specs/domains/webview-gpu-policy.md.
const WebviewGpuPolicyEnvVar = "C0WRK_WEBVIEW_GPU_POLICY"

// Canonical webview GPU-policy spellings — the alphabet of the env var, the
// persisted setting, the Settings UI, and the RPC wire values. String-level
// only, so the backend never imports the desktop layer.
// See specs/domains/webview-gpu-policy.md.
const (
	WebviewGpuPolicyAlways   = "always"
	WebviewGpuPolicyOnDemand = "on-demand"
	WebviewGpuPolicyNever    = "never"
	// WebviewGpuPolicyAuto is a SETTING value, not an effective policy: it
	// resolves to on-demand unless the watchdog persisted an auto-fallback.
	WebviewGpuPolicyAuto = "auto"
)

// Where the effective policy of a launch came from — reported by the Settings
// RPCs and the startup logs.
const (
	WebviewGpuSourceEnv          = "env"
	WebviewGpuSourceSetting      = "setting"
	WebviewGpuSourceAuto         = "auto"
	WebviewGpuSourceAutoFallback = "auto-fallback"
	// WebviewGpuSourceAutoUnobserved: fresh auto would have resolved
	// accelerated, but this run's stderr is not observable, so the DMABUF
	// watchdog could never detect a grey window — demoted to never for the
	// launch. Nothing detected, nothing persisted; the next launch with a
	// usable capture resolves on-demand again.
	WebviewGpuSourceAutoUnobserved = "auto-unobserved"
)

// GpuFallbackReasonDmabufFailure is the watchdog's fallback reason when the
// captured run stderr carries the DMABUF grey-window signatures (wails#2977).
const GpuFallbackReasonDmabufFailure = "dmabuf_render_failure"

// GpuFallbackReasonWaylandProtocolError is the next-start post-mortem's
// fallback reason when the previous fresh-auto run died on a fatal Wayland
// protocol error before DomReady (the in-run watchdog never got to scan).
const GpuFallbackReasonWaylandProtocolError = "wayland_protocol_error"

// NormalizeWebviewGpuPolicy parses an env-override value; the env alphabet
// carries no "auto" — the variable is an explicit override.
func NormalizeWebviewGpuPolicy(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WebviewGpuPolicyAlways:
		return WebviewGpuPolicyAlways, nil
	case WebviewGpuPolicyOnDemand, "ondemand":
		return WebviewGpuPolicyOnDemand, nil
	case WebviewGpuPolicyNever:
		return WebviewGpuPolicyNever, nil
	default:
		return WebviewGpuPolicyNever, fmt.Errorf("unknown webview GPU policy %q: want always, on-demand, or never", raw)
	}
}

// NormalizeWebviewGpuSetting parses a persisted webview_state.json gpu_policy
// value — the Settings alphabet plus "auto", case-insensitive, "ondemand"
// aliased.
func NormalizeWebviewGpuSetting(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WebviewGpuPolicyAuto:
		return WebviewGpuPolicyAuto, nil
	case WebviewGpuPolicyAlways:
		return WebviewGpuPolicyAlways, nil
	case WebviewGpuPolicyOnDemand, "ondemand":
		return WebviewGpuPolicyOnDemand, nil
	case WebviewGpuPolicyNever:
		return WebviewGpuPolicyNever, nil
	default:
		return WebviewGpuPolicyAuto, fmt.Errorf("unknown webview GPU policy setting %q: want auto, always, on-demand, or never", raw)
	}
}

// ResolveWebviewGpuPolicy maps the persisted setting (plus the watchdog's
// auto-mode fallback) onto the effective policy and its deciding layer. The
// fallback applies only while the setting is "auto" — an explicit choice is
// never second-guessed, and the Settings RPC clears the record on any
// concrete value.
func ResolveWebviewGpuPolicy(setting string, fallbackActive bool) (policy, source string) {
	switch setting {
	case WebviewGpuPolicyAlways:
		return WebviewGpuPolicyAlways, WebviewGpuSourceSetting
	case WebviewGpuPolicyOnDemand:
		return WebviewGpuPolicyOnDemand, WebviewGpuSourceSetting
	case WebviewGpuPolicyNever:
		return WebviewGpuPolicyNever, WebviewGpuSourceSetting
	default:
		if fallbackActive {
			return WebviewGpuPolicyNever, WebviewGpuSourceAutoFallback
		}
		// Auto resolves to on-demand — the WebKit default, which composites
		// trivial CSS animations on the GPU instead of pegging a CPU core
		// (issue #34).
		return WebviewGpuPolicyOnDemand, WebviewGpuSourceAuto
	}
}

// WebviewGpuAutoFallback records why auto mode is pinned to software
// rendering (the watchdog's DMABUF failure). Sticky until the user acts:
// the Settings RPC clears it on any choice; re-choosing auto is the retry.
type WebviewGpuAutoFallback struct {
	Policy     string `json:"policy"`      // always "never" today; kept explicit for future fallback targets
	Reason     string `json:"reason"`      // a GpuFallbackReason* constant
	DetectedAt string `json:"detected_at"` // RFC3339 UTC
}

// WebviewGpuState is the on-disk shape of webview_state.json; GPUPolicy is
// the user setting ("" = nothing chosen yet → auto).
type WebviewGpuState struct {
	GPUPolicy    string                  `json:"gpu_policy,omitempty"`
	AutoFallback *WebviewGpuAutoFallback `json:"auto_fallback,omitempty"`
}

// Setting returns the canonical setting; an unparsable persisted value (a
// hand edit) degrades to auto — LoadWebviewGpuState has already logged it.
func (s WebviewGpuState) Setting() string {
	setting, err := NormalizeWebviewGpuSetting(s.GPUPolicy)
	if err != nil {
		return WebviewGpuPolicyAuto
	}
	return setting
}

// FallbackActive reports whether the watchdog has persisted an auto-fallback.
func (s WebviewGpuState) FallbackActive() bool {
	return s.AutoFallback != nil
}

// LoadWebviewGpuState reads webview_state.json defensively: missing,
// malformed, or unknown-valued all resolve to the zero state (setting auto,
// no fallback) so a corrupt state file can never prevent startup. main.go
// calls this before wails.Run.
func LoadWebviewGpuState(agentDir string, logger *slog.Logger) WebviewGpuState {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var st WebviewGpuState
	data, err := safeio.ReadFile(config.WebviewStatePath(agentDir))
	if err != nil {
		// Missing on first run is expected — zero state silently.
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		logger.Debug("webview state file unreadable; using defaults",
			"path", config.WebviewStatePath(agentDir), "error", err)
		return WebviewGpuState{}
	}
	if st.GPUPolicy != "" {
		if _, err := NormalizeWebviewGpuSetting(st.GPUPolicy); err != nil {
			logger.Debug("unknown webview GPU policy setting; treating as auto",
				"path", config.WebviewStatePath(agentDir), "value", st.GPUPolicy, "error", err)
		}
	}
	return st
}

// SaveWebviewGpuState atomically writes webview_state.json via
// safeio.WriteFileAtomic — the rename replaces the directory entry itself,
// so a planted symlink is replaced, never written through.
func SaveWebviewGpuState(agentDir string, st WebviewGpuState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(config.WebviewStatePath(agentDir), data, 0o600)
}

// webviewGpuStateMu serializes read-modify-write cycles on webview_state.json:
// the Settings RPC and the desktop watchdog's fallback persist both RMW the
// whole file through UpdateWebviewGpuState, so neither can clobber the other's
// change. Plain Load/Save callers stay lock-free — the atomic replace means
// readers always observe one complete state.
var webviewGpuStateMu sync.Mutex

// UpdateWebviewGpuState applies mutate to the freshly loaded state and saves
// the result atomically, under the RMW lock.
func UpdateWebviewGpuState(agentDir string, logger *slog.Logger, mutate func(*WebviewGpuState)) error {
	webviewGpuStateMu.Lock()
	defer webviewGpuStateMu.Unlock()
	state := LoadWebviewGpuState(agentDir, logger)
	mutate(&state)
	return SaveWebviewGpuState(agentDir, state)
}

// WebviewGpuRun is a fresh launch's webview GPU resolution, as returned by
// DeriveWebviewGpuRun.
type WebviewGpuRun struct {
	// Policy is the canonical effective policy; Source the layer that
	// decided it (env | setting | auto | auto-fallback | auto-unobserved).
	Policy string
	Source string
	// Setting is the canonical persisted setting the resolution consumed;
	// returned here so consumers never re-read webview_state.json to log it.
	Setting string
}

// DeriveWebviewGpuRun resolves the effective policy and its deciding layer
// for a fresh launch: a valid env override wins, otherwise the persisted
// setting with its auto resolution. The single formula both consumers share
// (desktop's wails.Run options and Init's running snapshot) and the launch's
// single read of webview_state.json — the consumed setting rides along.
// goos gates the demotion (tests pass the platform explicitly); stderrObserved
// reports a usable stderr capture — without one, Linux must not apply the
// accelerated fresh-auto default, since the DMABUF watchdog detects the
// grey window ONLY from that capture. Explicit choices are never demoted.
// See specs/domains/webview-gpu-policy.md.
func DeriveWebviewGpuRun(goos, agentDir string, stderrObserved bool, logger *slog.Logger) WebviewGpuRun {
	state := LoadWebviewGpuState(agentDir, logger)
	setting := state.Setting()
	if raw := os.Getenv(WebviewGpuPolicyEnvVar); strings.TrimSpace(raw) != "" {
		if p, err := NormalizeWebviewGpuPolicy(raw); err == nil {
			return WebviewGpuRun{Policy: p, Source: WebviewGpuSourceEnv, Setting: setting}
		}
	}
	policy, source := ResolveWebviewGpuPolicy(setting, state.FallbackActive())
	if !stderrObserved && goos == "linux" && policy == WebviewGpuPolicyOnDemand && source == WebviewGpuSourceAuto {
		return WebviewGpuRun{Policy: WebviewGpuPolicyNever, Source: WebviewGpuSourceAutoUnobserved, Setting: setting}
	}
	return WebviewGpuRun{Policy: policy, Source: source, Setting: setting}
}
