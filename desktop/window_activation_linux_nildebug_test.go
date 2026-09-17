//go:build linux

package desktop

import "testing"

// TestX11ActivationNilDebugNoPanic is the regression test for the startup
// crash (exit code 2): x11ActivateOwnWindow used to call the bare
// x11ActivateDebug(...) at every diagnostics point while the variable is
// nil in production, so the first DomReady → showWindow pass panicked with
// a nil pointer dereference before any window work happened. Every
// diagnostics call now goes through the nil-guarded x11Debug helper.
//
// Safe to run on any machine without the live-test opt-in: the test process
// owns no X windows, so the walk terminates at "no window with
// _NET_WM_PID == pid" without activating anything; headless environments
// terminate earlier at XOpenDisplay. Both terminations pass through
// nil-guarded diagnostics calls.
func TestX11ActivationNilDebugNoPanic(t *testing.T) {
	orig := x11ActivateDebug
	x11ActivateDebug = nil
	defer func() { x11ActivateDebug = orig }()

	// Must not panic; the boolean result is environment-dependent
	// (false on headless or when no window matches the test process).
	x11ActivateOwnWindow()
}
