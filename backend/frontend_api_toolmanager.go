package backend

import (
	"github.com/v0lka/c0wrk/core/toolmanager"
)

// ToolInstallPendingToolDTO names one managed tool awaiting installation —
// the RPC twin of one entry of tool_manager:start's `tools` payload.
type ToolInstallPendingToolDTO struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolInstallStatusDTO is the transport-neutral snapshot of the startup
// tool-install flow (specs/contracts/desktop-frontend.md, GetToolInstallStatus).
// Wails drops events emitted before the webview subscribes, so
// tool_manager:start can be lost; this snapshot lets the frontend recover the
// splash state over RPC instead of guessing. While the install is running
// Active is true and Pending lists every tool still to install in
// NeedsInstall order; once the flow has resolved (every tool_manager:done
// emission, including the fatal and toast-degradation outcomes) the store is
// back to the steady state: Active=false, Pending empty, ToolsTotal=0.
type ToolInstallStatusDTO struct {
	// Active reports whether the install splash is logically up — i.e. a
	// tool_manager:start was recorded and no tool_manager:done has resolved
	// it yet. The NeedsInstall-probe-failure fallback reports Active=true
	// with an empty Pending: the splash is up, nothing is known about which
	// tools are missing.
	Active bool `json:"active"`
	// Pending is the tools awaiting installation (name + pinned version);
	// never nil, so the JSON shape is always an array.
	Pending []ToolInstallPendingToolDTO `json:"pending"`
	// ToolsTotal is the splash denominator the progress events carry —
	// len(Pending) while active, 0 in the steady state.
	ToolsTotal int64 `json:"tools_total"`
}

// SetToolInstallPending records one install-flow transition in the
// transport-neutral store. The desktop host binds backend.ToolPhaseDeps's
// SetPendingTools hook to it, so the store updates sit next to every
// tool_manager:start / tool_manager:done emission in
// reconcileToolsWithSplash. A NON-NIL slice marks the install active with
// that pending list (an empty non-nil slice is the NeedsInstall-probe-
// failure fallback: active, nothing known); nil resolves the store — the
// clear that accompanies every done emission. The slice is copied, so later
// mutations of the caller's slice cannot reach the store.
func (f *FrontendAPI) SetToolInstallPending(tools []toolmanager.ToolSpec) {
	f.toolInstallMu.Lock()
	defer f.toolInstallMu.Unlock()
	if tools == nil {
		f.toolInstallActive = false
		f.toolInstallPending = nil
		f.toolInstallTotal = 0
		return
	}
	pending := make([]ToolInstallPendingToolDTO, len(tools))
	for i, t := range tools {
		pending[i] = ToolInstallPendingToolDTO{Name: t.Name, Version: t.Version}
	}
	f.toolInstallActive = true
	f.toolInstallPending = pending
	f.toolInstallTotal = int64(len(tools))
}

// GetToolInstallStatus returns the current install-flow snapshot: Active
// with the full pending list while the install runs, the steady state
// (Active=false, empty list) after done and in a no-install launch. The
// read-only getter never fails and deliberately does NOT seedAcquire: the
// store is written during Phase 2 — before Init publishes the seed — and
// the RPC exists precisely for the lost-start-event window inside that
// phase, where a seed wait would deadlock the splash recovery it serves.
func (f *FrontendAPI) GetToolInstallStatus() ToolInstallStatusDTO {
	f.toolInstallMu.Lock()
	defer f.toolInstallMu.Unlock()
	pending := make([]ToolInstallPendingToolDTO, len(f.toolInstallPending))
	copy(pending, f.toolInstallPending)
	return ToolInstallStatusDTO{
		Active:     f.toolInstallActive,
		Pending:    pending,
		ToolsTotal: f.toolInstallTotal,
	}
}
