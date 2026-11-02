// SPDX-License-Identifier: Apache-2.0
//
// webaccess_extra.go — the App-level methods the browser surface mirrors,
// behind the MINIMAL ExtraDispatch adapter. Never a passthrough to the App:
// every method widens the browser attack surface and is pinned by the
// surface contract test. See specs/domains/web-access.md.

package desktop

// webExtraSurface is the opted-in slice of App-level capabilities the
// mirrored UI needs (fallback after the FrontendAPI surface).
type webExtraSurface struct {
	app *App
}

// GetPendingActions mirrors App.GetPendingActions for the web transport —
// the same session-switch HITL reconciliation sweep; read-only.
func (w *webExtraSurface) GetPendingActions(sessionID string) (*PendingActionsResponse, error) {
	return w.app.GetPendingActions(sessionID)
}
