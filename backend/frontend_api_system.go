package backend

import (
	"fmt"
	"math"
)

// GetProcessMemory returns the desktop process's RSS in bytes for the status
// bar's live memory indicator — informational only; failures hide it silently.
// See specs/domains/workspace.md (RSS observability).
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

// SystemFontsResponse is the GetSystemFonts wire shape: an empty family means
// "not detected" — there is no availability flag.
// See specs/domains/frontend/fonts.md.
type SystemFontsResponse struct {
	UIFamily   string `json:"ui_family"`
	MonoFamily string `json:"mono_family"`
}

// GetSystemFonts reports the desktop environment's configured UI and
// monospace families (Linux: GNOME gsettings). Every undetectable outcome is
// the zero response plus a Debug log — a normal answer, never an error — and
// each Pango description is parsed independently: an unparsable value empties
// only its own family. Families only: c0wrk owns its type scale and applies
// weight/slant through CSS. See specs/domains/frontend/fonts.md.
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

// FontFamiliesResponse is the ListFontFamilies wire shape; Available=false
// always pairs with an empty list — there is no partial-failure state.
// See specs/domains/fonts.md.
type FontFamiliesResponse struct {
	Available bool     `json:"available"`
	Families  []string `json:"families"`
}

// ListFontFamilies enumerates installed font families for the frontend pickers
// (Linux: fontconfig's `fc-list`, optionally narrowed to the fontconfig-mono
// families). Available describes the MECHANISM, not the result — a successful
// run on a fontless system is Available=true with an empty list; every reader
// error is {Available: false} plus a Debug log, never a renderer error.
// See specs/domains/fonts.md.
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
