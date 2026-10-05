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

func TestGetSystemUIFont_StubbedSeam(t *testing.T) {
	t.Run("available with parsed family", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontFn: func() (string, error) {
			return "'Noto Sans 11'\n", nil
		}}
		resp := f.GetSystemUIFont()
		if !resp.Available {
			t.Fatal("GetSystemUIFont() Available = false, want true")
		}
		if resp.FontFamily != "Noto Sans" {
			t.Errorf("GetSystemUIFont() FontFamily = %q, want %q", resp.FontFamily, "Noto Sans")
		}
	})

	t.Run("reader error means unavailable, not RPC failure", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontFn: func() (string, error) {
			return "", errors.New("gsettings not found in PATH: exec: not found")
		}}
		resp := f.GetSystemUIFont()
		if resp.Available {
			t.Fatal("GetSystemUIFont() Available = true, want false on reader error")
		}
		if resp.FontFamily != "" {
			t.Errorf("GetSystemUIFont() FontFamily = %q, want empty", resp.FontFamily)
		}
	})

	t.Run("unparsable value means unavailable", func(t *testing.T) {
		f := &FrontendAPI{readSystemFontFn: func() (string, error) {
			return "''", nil
		}}
		resp := f.GetSystemUIFont()
		if resp.Available {
			t.Fatal("GetSystemUIFont() Available = true, want false for an empty font value")
		}
	})
}

// TestGetSystemUIFont_NilSeamUsesRealRead exercises the production read path
// (readSystemFontName): on Linux with a GNOME session it must report a real
// family; anywhere else (non-Linux build, no gsettings, non-GNOME desktop)
// the read errors and the RPC reports unavailable. Both outcomes are correct,
// so the test only skips when the environment read itself fails.
func TestGetSystemUIFont_NilSeamUsesRealRead(t *testing.T) {
	raw, err := readSystemFontName()
	if err != nil {
		t.Skipf("readSystemFontName() unavailable in this environment: %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		t.Skipf("readSystemFontName() returned an empty value: %q", raw)
	}

	f := &FrontendAPI{}
	resp := f.GetSystemUIFont()
	wantFamily, wantOK := parseGnomeFontName(raw)
	if resp.Available != wantOK {
		t.Fatalf("GetSystemUIFont() Available = %v, want %v (parsed from %q)", resp.Available, wantOK, raw)
	}
	if resp.FontFamily != wantFamily {
		t.Errorf("GetSystemUIFont() FontFamily = %q, want %q", resp.FontFamily, wantFamily)
	}
}
