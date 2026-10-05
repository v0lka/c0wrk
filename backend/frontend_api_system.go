package backend

import (
	"fmt"
	"math"
)

// GetProcessMemory returns the resident set size (RSS) of the c0wrk desktop
// process, in bytes. The frontend status bar polls it every few seconds to
// render a live memory indicator; the value is informational only (no policy
// or security decisions hang off it).
//
// The read goes through the readProcessRSS seam (processmem.go); tests stub
// it via the readProcessRSSFn field on FrontendAPI.
func (f *FrontendAPI) GetProcessMemory() (int64, error) {
	readRSS := f.readProcessRSSFn
	if readRSS == nil {
		readRSS = readProcessRSS
	}
	rss, err := readRSS()
	if err != nil {
		return 0, fmt.Errorf("failed to read process memory: %w", err)
	}
	if rss > math.MaxInt64 {
		return 0, fmt.Errorf("process memory %d bytes overflows int64", rss)
	}
	return int64(rss), nil
}

// SystemUIFontResponse is the wire shape of GetSystemUIFont: whether a
// desktop-environment UI font was detected, and its family name if so.
type SystemUIFontResponse struct {
	Available  bool   `json:"available"`
	FontFamily string `json:"font_family"`
}

// GetSystemUIFont reports the desktop environment's configured UI font
// family, when one can be detected. On Linux this reads the GNOME interface
// font (org.gnome.desktop.interface font-name) via gsettings; other platforms
// and non-GNOME desktops (no gsettings / no schema) report Available=false,
// which the frontend renders as "the setting does not exist here".
//
// Why the family only: the WebKitGTK webview does not follow gtk-font-name
// (the UI font is decided by the app's own CSS), so the app has to carry the
// system choice itself — but the point size and style stay behind, because
// c0wrk owns its type scale (14px base + the UI Scale setting) and applies
// weight/slant through CSS. A GNOME-side change is picked up on the next
// launch; there is no live propagation into a running WebKitGTK instance.
//
// The read goes through the readSystemFontName seam (systemfont_linux.go /
// systemfont_other.go); tests stub it via the readSystemFontFn field on
// FrontendAPI, mirroring readProcessRSSFn above.
func (f *FrontendAPI) GetSystemUIFont() SystemUIFontResponse {
	readFont := f.readSystemFontFn
	if readFont == nil {
		readFont = readSystemFontName
	}
	raw, err := readFont()
	if err != nil {
		// Unavailability is a normal outcome (non-Linux build, KDE, missing
		// gsettings), not a failure to surface: the frontend hides the
		// setting entirely, so there is nothing to report to the user.
		// Debug only — a desktop without gsettings must not generate noise.
		if f.logger != nil {
			f.logger.Debug("system UI font unavailable", "error", err)
		}
		return SystemUIFontResponse{}
	}
	family, ok := parseGnomeFontName(raw)
	if !ok {
		return SystemUIFontResponse{}
	}
	return SystemUIFontResponse{Available: true, FontFamily: family}
}
