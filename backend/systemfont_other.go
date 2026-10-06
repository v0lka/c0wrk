//go:build !linux

package backend

import "errors"

// errSystemFontUnavailable is the non-Linux answer from readSystemFonts:
// there is no gsettings-equivalent probe for the desktop fonts that c0wrk
// implements today (macOS and Windows resolve their "system fonts" through
// platform-native stacks the webview already follows). The RPC surfaces this
// as the zero response and the frontend hides the settings entirely.
var errSystemFontUnavailable = errors.New("system font detection is only supported on Linux (GNOME/gsettings)")

// readSystemFonts is the non-Linux stub backing GetSystemFonts; see
// systemfont_linux.go for the real implementation.
func readSystemFonts() (systemFontPair, error) {
	return systemFontPair{}, errSystemFontUnavailable
}
