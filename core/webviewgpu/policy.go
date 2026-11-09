// Package webviewgpu holds the Wails-free webview GPU policy artifacts: the
// canonical policy alphabet (env var values, persisted setting, Settings UI,
// RPC wire values) and the effective-policy enum whose Wails-enum mapping
// lives in the desktop host, plus the NVIDIA Wayland explicit-sync
// workaround. See specs/domains/webview-gpu-policy.md.
package webviewgpu

// Canonical webview GPU-policy spellings — the alphabet shared by the env
// var, the persisted setting, the Settings UI, and the RPC wire values.
const (
	CanonicalAlways   = "always"
	CanonicalOnDemand = "on-demand"
	CanonicalNever    = "never"
)

// Policy is the effective hardware-acceleration policy for the Linux webview
// — deliberately decoupled from Wails' enum ordering (the desktop's Wails
// mapping compensates for the pinned Wails v2.16.0 Go-enum/C-layer mismatch).
// See specs/domains/webview-gpu-policy.md.
type Policy int

const (
	PolicyNever Policy = iota
	PolicyAlways
	PolicyOnDemand
)

// String returns the canonical spelling of the policy (the canonical
// alphabet above).
func (p Policy) String() string {
	switch p {
	case PolicyAlways:
		return CanonicalAlways
	case PolicyOnDemand:
		return CanonicalOnDemand
	default:
		return CanonicalNever
	}
}

// webviewGpuPolicyFromCanonical maps a canonical policy spelling (validated
// by backend's resolvers) onto the enum.
func webviewGpuPolicyFromCanonical(canonical string) Policy {
	switch canonical {
	case CanonicalAlways:
		return PolicyAlways
	case CanonicalOnDemand:
		return PolicyOnDemand
	default:
		return PolicyNever
	}
}

// FromCanonical forwards to webviewGpuPolicyFromCanonical for the host
// adapters (the desktop Wails-options builder).
func FromCanonical(canonical string) Policy {
	return webviewGpuPolicyFromCanonical(canonical)
}
