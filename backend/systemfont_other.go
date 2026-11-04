//go:build !linux

package backend

import "errors"

// errSystemFontUnavailable is the non-Linux answer from readSystemFonts:
// desktop-system-font detection is GNOME/gsettings-only in c0wrk; the RPC
// surfaces the zero response and the frontend hides the settings there.
var errSystemFontUnavailable = errors.New("system font detection is only supported on Linux (GNOME/gsettings)")

// readSystemFonts is the non-Linux stub backing GetSystemFonts; see
// systemfont_linux.go for the real implementation.
func readSystemFonts() (systemFontPair, error) {
	return systemFontPair{}, errSystemFontUnavailable
}
