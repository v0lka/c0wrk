package desktop

import _ "embed"

// notificationIconBytes embeds the application icon for notification
// banners; go:embed cannot reference build/appicon.png, so a byte-identical
// drift-guarded copy lives at desktop/icon/appicon.png
// (specs/domains/frontend/system-notifications.md).
//
//go:embed icon/appicon.png
var notificationIconBytes []byte

// notificationIconPNG returns the embedded icon; callers must treat the
// slice as read-only (it aliases the embedded asset).
func notificationIconPNG() []byte {
	return notificationIconBytes
}
