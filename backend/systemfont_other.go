//go:build !linux

package backend

import "errors"

// errSystemFontUnavailable is the non-Linux answer from readSystemFontName:
// there is no gsettings-equivalent probe for the desktop UI font that c0wrk
// implements today (macOS and Windows resolve their "system font" through
// platform-native stacks the webview already follows). The RPC surfaces this
// as Available=false and the frontend hides the setting entirely.
var errSystemFontUnavailable = errors.New("system UI font detection is only supported on Linux (GNOME/gsettings)")

// readSystemFontName is the non-Linux stub backing GetSystemUIFont; see
// systemfont_linux.go for the real implementation.
func readSystemFontName() (string, error) {
	return "", errSystemFontUnavailable
}
