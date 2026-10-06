package backend

import (
	"errors"
	"strings"
	"testing"
)

func TestParseGnomeFontName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "typical GNOME value", raw: "'Noto Sans 11'\n", want: "Noto Sans", ok: true},
		{name: "double-quoted serialization", raw: `"Cantarell 10"`, want: "Cantarell", ok: true},
		{name: "single-word family with size", raw: "'Ubuntu 11'", want: "Ubuntu", ok: true},
		{name: "decimal point size", raw: "'Noto Sans 10.5'", want: "Noto Sans", ok: true},
		{name: "style keyword before size", raw: "'DejaVu Sans Bold 10'", want: "DejaVu Sans", ok: true},
		{name: "several style keywords", raw: "'Source Code Pro Medium Italic 11'", want: "Source Code Pro", ok: true},
		{name: "no size at all", raw: "'Noto Sans'", want: "Noto Sans", ok: true},
		{name: "surrounding whitespace", raw: "   'Noto Sans 11'   ", want: "Noto Sans", ok: true},
		{name: "unquoted value", raw: "Inter 12", want: "Inter", ok: true},
		{name: "three-word family", raw: "'IBM Plex Sans 11'", want: "IBM Plex Sans", ok: true},
		{name: "monospace family with style", raw: "'DejaVu Sans Mono Bold 10'", want: "DejaVu Sans Mono", ok: true},
		{name: "empty quoted value", raw: "''", want: "", ok: false},
		{name: "empty input", raw: "", want: "", ok: false},
		{name: "whitespace only", raw: "   ", want: "", ok: false},
		{name: "lone size token", raw: "'11'", want: "", ok: false},
		{name: "lone style keyword survives as family", raw: "'Bold'", want: "Bold", ok: true},
		{name: "size token that is not numeric", raw: "'Noto Sans 11x'", want: "Noto Sans 11x", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGnomeFontName(tt.raw)
			if ok != tt.ok {
				t.Fatalf("parseGnomeFontName(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("parseGnomeFontName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestGetSystemFonts_StubbedSeam(t *testing.T) {
	t.Run("both families parsed", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontsFn: func() (systemFontPair, error) {
			return systemFontPair{
				UI:   "'Noto Sans 11'\n",
				Mono: "'DejaVu Sans Mono Bold 10'",
			}, nil
		}}
		resp := f.GetSystemFonts()
		if resp.UIFamily != "Noto Sans" {
			t.Errorf("GetSystemFonts() UIFamily = %q, want %q", resp.UIFamily, "Noto Sans")
		}
		if resp.MonoFamily != "DejaVu Sans Mono" {
			t.Errorf("GetSystemFonts() MonoFamily = %q, want %q", resp.MonoFamily, "DejaVu Sans Mono")
		}
	})

	t.Run("reader error means zero response, not RPC failure", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontsFn: func() (systemFontPair, error) {
			return systemFontPair{}, errors.New("gsettings not found in PATH: exec: not found")
		}}
		resp := f.GetSystemFonts()
		if resp.UIFamily != "" || resp.MonoFamily != "" {
			t.Errorf("GetSystemFonts() = %+v, want the zero response on a reader error", resp)
		}
	})

	t.Run("unparsable UI value empties only the UI family", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontsFn: func() (systemFontPair, error) {
			return systemFontPair{UI: "''", Mono: "'DejaVu Sans Mono 10'"}, nil
		}}
		resp := f.GetSystemFonts()
		if resp.UIFamily != "" {
			t.Errorf("GetSystemFonts() UIFamily = %q, want empty for an unparsable UI value", resp.UIFamily)
		}
		if resp.MonoFamily != "DejaVu Sans Mono" {
			t.Errorf("GetSystemFonts() MonoFamily = %q, want %q", resp.MonoFamily, "DejaVu Sans Mono")
		}
	})

	t.Run("unparsable mono value empties only the mono family", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontsFn: func() (systemFontPair, error) {
			return systemFontPair{UI: "'Noto Sans 11'\n", Mono: "'11'"}, nil
		}}
		resp := f.GetSystemFonts()
		if resp.UIFamily != "Noto Sans" {
			t.Errorf("GetSystemFonts() UIFamily = %q, want %q", resp.UIFamily, "Noto Sans")
		}
		if resp.MonoFamily != "" {
			t.Errorf("GetSystemFonts() MonoFamily = %q, want empty for an unparsable mono value", resp.MonoFamily)
		}
	})
}

// TestGetSystemFonts_NilSeamUsesRealRead exercises the production read path
// (readSystemFonts): on Linux with a GNOME session it must report real
// families; anywhere else (non-Linux build, no gsettings, non-GNOME desktop)
// the read errors and the RPC reports the zero response. Both outcomes are
// correct, so the test only skips when the environment read itself fails.
func TestGetSystemFonts_NilSeamUsesRealRead(t *testing.T) {
	pair, err := readSystemFonts()
	if err != nil {
		t.Skipf("readSystemFonts() unavailable in this environment: %v", err)
	}
	if strings.TrimSpace(pair.UI) == "" && strings.TrimSpace(pair.Mono) == "" {
		t.Skipf("readSystemFonts() returned empty values: %+v", pair)
	}

	f := &FrontendAPI{}
	resp := f.GetSystemFonts()
	wantUI, uiOK := parseGnomeFontName(pair.UI)
	wantMono, monoOK := parseGnomeFontName(pair.Mono)
	if resp.UIFamily != wantUI {
		t.Errorf("GetSystemFonts() UIFamily = %q, want %q (ok=%v, parsed from %q)", resp.UIFamily, wantUI, uiOK, pair.UI)
	}
	if resp.MonoFamily != wantMono {
		t.Errorf("GetSystemFonts() MonoFamily = %q, want %q (ok=%v, parsed from %q)", resp.MonoFamily, wantMono, monoOK, pair.Mono)
	}
}
