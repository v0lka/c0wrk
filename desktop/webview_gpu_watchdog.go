package desktop

import "github.com/v0lka/c0wrk/backend"

// webviewGpuWatchdog is the desktop-side host adapter for the backend-owned
// GPU-failure watchdog (desktop-layering audit P-13): the detection policy,
// stderr scan state machine and post-mortem live in backend
// (backend/webview_gpu_watchdog.go); ownership and start stay with the GUI
// host — desktop arms it in main, wires the quit at DomReady
// (gpuFallbackQuit) and fires Start; a daemon never starts a watchdog for a
// webview it does not have (audit §9.1).
//
// The wrapper carries the SAME name as the backend type on purpose: it is
// the parameter type of the Wails-bound App.SetWebviewGpuWatchdog, and the
// generated frontend/wailsjs model (desktop.webviewGpuWatchdog, an empty
// class — every field is unexported) must stay byte-identical
// (audit §6.1 rule 1). A type alias is not an option: the binding generator
// resolves aliases and would re-namespace the model to backend. Keep this a
// thin delegating shell — all behavior lives in backend.
type webviewGpuWatchdog struct {
	w *backend.WebviewGpuWatchdog

	// quitFn is the GUI-host quit wired by wireGpuWatchdogQuit; Start
	// forwards it into the backend watchdog before the scan opens. Held on
	// the host adapter (instead of writing straight through) so the wiring
	// exists — and is testable — even around a nil inner, which is the
	// universal off-Linux / detection-not-applied shape.
	quitFn func()
}

// NewWebviewGpuWatchdogHost wraps the backend-armed watchdog for the App
// binding surface (main.go — internal startup plumbing rides the Wails
// binding surface by the SetWebviewGpuWatchdog precedent, so process-level
// wiring can hand over a value only it holds).
func NewWebviewGpuWatchdogHost(w *backend.WebviewGpuWatchdog) *webviewGpuWatchdog {
	return &webviewGpuWatchdog{w: w}
}

// SetQuitFn records the post-relaunch quit callback — GUI-host territory
// (the quit must arm the close-guard bypass and route through the live Wails
// context), wired at DomReady where that context exists. Start hands it to
// the backend watchdog.
func (h *webviewGpuWatchdog) SetQuitFn(fn func()) {
	if h == nil {
		return
	}
	h.quitFn = fn
}

// QuitFn returns the recorded post-relaunch quit (nil when not wired).
func (h *webviewGpuWatchdog) QuitFn() func() {
	if h == nil {
		return nil
	}
	return h.quitFn
}

// Start begins the bounded startup scan, handing the wired quit to the
// backend watchdog first (a quit recorded after the scan's decision would
// never fire — the relaunch flow quits through it). Idempotent end to end:
// the backend start is guarded by its started flag and the quit forwarding
// by the watchdog's nil check. No-op without a backend watchdog (off Linux,
// detection not applied) — matching the backend's nil-receiver tolerance.
func (h *webviewGpuWatchdog) Start() {
	if h == nil || h.w == nil {
		return
	}
	if h.quitFn != nil && h.w.QuitFn() == nil {
		h.w.SetQuitFn(h.quitFn)
	}
	h.w.Start()
}
