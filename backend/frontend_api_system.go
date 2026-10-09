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

// SystemFontsResponse is the wire shape of GetSystemFonts: the detected
// desktop-environment font families. An empty string means "this font was
// not found" — there is no separate availability flag, so an undetectable
// desktop is simply the zero response.
type SystemFontsResponse struct {
	UIFamily   string `json:"ui_family"`
	MonoFamily string `json:"mono_family"`
}

// GetSystemFonts reports the desktop environment's configured UI and
// monospace font families, when they can be detected. On Linux this reads
// the GNOME interface fonts (org.gnome.desktop.interface font-name and
// monospace-font-name) via gsettings; other platforms and non-GNOME desktops
// (no gsettings / no schema) return the zero response, which the frontend
// renders as "the settings do not exist here".
//
// Why the families only: the WebKitGTK webview does not follow gtk-font-name
// (the UI font is decided by the app's own CSS), so the app has to carry the
// system choice itself — but the point sizes and styles stay behind, because
// c0wrk owns its type scale (14px base + the UI Scale setting) and applies
// weight/slant through CSS. A GNOME-side change is picked up on the next
// launch; there is no live propagation into a running WebKitGTK instance.
//
// The read goes through the readSystemFonts seam (systemfont_linux.go /
// systemfont_other.go); tests stub it via the readSystemFontsFn field on
// FrontendAPI, mirroring readProcessRSSFn above. Unavailability (a reader
// error) zeroes the whole response and logs at Debug only — a desktop
// without gsettings is a normal outcome, not a failure to surface. When the
// reads succeed, each description is parsed independently: an unparsable
// value empties only its own family.
func (f *FrontendAPI) GetSystemFonts() SystemFontsResponse {
	f.seedAcquire()
	readFonts := f.readSystemFontsFn
	if readFonts == nil {
		readFonts = readSystemFonts
	}
	pair, err := readFonts()
	if err != nil {
		if f.logger != nil {
			f.logger.Debug("system fonts unavailable", "error", err)
		}
		return SystemFontsResponse{}
	}
	var resp SystemFontsResponse
	if family, ok := parseGnomeFontName(pair.UI); ok {
		resp.UIFamily = family
	}
	if family, ok := parseGnomeFontName(pair.Mono); ok {
		resp.MonoFamily = family
	}
	return resp
}

// FontFamiliesResponse is the wire shape of ListFontFamilies: whether the
// installed-font enumeration is usable here, and the family names when it
// is. Available=false always pairs with an empty list — there is no
// partial-failure state.
type FontFamiliesResponse struct {
	Available bool     `json:"available"`
	Families  []string `json:"families"`
}

// ListFontFamilies reports the installed font family names for the frontend
// font picker. On Linux this enumerates fontconfig's families via `fc-list
// --format '%{family}\n'` (fontlist_linux.go): comma-separated aliases are
// split, trimmed, deduplicated case-insensitively keeping the first
// spelling, and sorted case-insensitively (parseFontFamilies,
// fontlist.go). The monospace flag narrows the enumeration to the families
// fontconfig tags as monospace (`fc-list :mono …`) — the monospace picker
// offers only real mono families; the interface picker passes false for the
// full list. Other platforms — and any Linux system where fc-list is
// missing or fails — return Available=false with an empty list, which the
// frontend renders as "font enumeration does not exist here".
//
// Availability is about the MECHANISM, not the result: a successful fc-list
// run on a fontless system is Available=true with an empty list. Why
// families only: the picker chooses a family; weights, styles and faces are
// resolved by the CSS font matching the family name.
//
// The read goes through the listFontFamilies seam (fontlist_linux.go /
// fontlist_other.go); tests stub it via the listFontFamiliesFn field on
// FrontendAPI, mirroring readSystemFontsFn above. A reader error is logged
// at Debug only — a desktop without fontconfig is a normal outcome, never
// an error surfaced to the renderer.
func (f *FrontendAPI) ListFontFamilies(monospace bool) FontFamiliesResponse {
	f.seedAcquire()
	listFamilies := f.listFontFamiliesFn
	if listFamilies == nil {
		listFamilies = listFontFamilies
	}
	families, err := listFamilies(monospace)
	if err != nil {
		if f.logger != nil {
			f.logger.Debug("font family list unavailable", "error", err)
		}
		return FontFamiliesResponse{}
	}
	if families == nil {
		// A nil seam/reader result serializes as null on the wire; the
		// response always carries a real (possibly empty) list.
		families = []string{}
	}
	return FontFamiliesResponse{Available: true, Families: families}
}
