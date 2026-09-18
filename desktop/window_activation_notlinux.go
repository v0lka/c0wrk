//go:build !linux

package desktop

// x11ActivateOwnWindow is the non-Linux stub of the EWMH pager-source
// window activation (window_activation_linux.go). Activation on the other
// platforms is fully covered by the Wails runtime primitives in
// showWindow's platform branches, so the stub simply reports failure and
// lets those branches run.
func x11ActivateOwnWindow() bool { return false }
