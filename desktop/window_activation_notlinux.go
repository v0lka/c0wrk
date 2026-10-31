//go:build !linux

package desktop

// x11ActivateOwnWindow is the non-Linux stub of the EWMH pager-source activation:
// other platforms activate via showWindow (specs/domains/frontend/system-notifications.md).
func x11ActivateOwnWindow() bool { return false }
