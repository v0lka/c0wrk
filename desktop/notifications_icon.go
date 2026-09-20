package desktop

import _ "embed"

// notificationIconBytes holds the application icon embedded into the binary
// so notification banners can render it without relying on an installed
// .desktop file / hicolor theme (dev mode via `wails dev`, or a standalone
// binary launched outside a packaged install).
//
// The go:embed directive cannot reference build/appicon.png (it never escapes
// the package directory), so a committed byte-identical copy lives at
// desktop/icon/appicon.png. notifications_icon_test.go guards the copy
// against drift from build/appicon.png so the two never diverge silently.
//
//go:embed icon/appicon.png
var notificationIconBytes []byte

// notificationIconPNG returns the embedded application icon as PNG bytes.
// Callers must treat the result as read-only (the slice aliases the embedded
// asset); empty only if the embed directive itself is broken at build time.
func notificationIconPNG() []byte {
	return notificationIconBytes
}
