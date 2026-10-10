package backend

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseFontFamilies(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "aliases on one line are kept separately",
			raw:  "DejaVu Sans,DejaVu Sans Book\n",
			want: []string{"DejaVu Sans", "DejaVu Sans Book"},
		},
		{
			name: "exact duplicate collapses to one entry",
			raw:  "Noto Sans\nNoto Sans\n",
			want: []string{"Noto Sans"},
		},
		{
			name: "case-insensitive duplicate keeps the first spelling",
			raw:  "NOTO SANS,Noto Sans\nnoto sans\n",
			want: []string{"NOTO SANS"},
		},
		{
			name: "empty output yields empty non-nil slice",
			raw:  "",
			want: []string{},
		},
		{
			name: "whitespace-only output yields empty slice",
			raw:  "   \n\t\n",
			want: []string{},
		},
		{
			name: "blank lines and empty aliases are skipped",
			raw:  "\nInter,,Ubuntu,\n\nLiberation Sans,\n",
			want: []string{"Inter", "Liberation Sans", "Ubuntu"},
		},
		{
			name: "surrounding whitespace around aliases is trimmed",
			raw:  "Noto Sans , DejaVu Sans\n",
			want: []string{"DejaVu Sans", "Noto Sans"},
		},
		{
			name: "carriage returns from foreign tooling do not leak into names",
			raw:  "Inter\r\nCantarell\r\n",
			want: []string{"Cantarell", "Inter"},
		},
		{
			name: "sorted case-insensitively, not by ASCII case",
			raw:  "Banana\napple\n",
			want: []string{"apple", "Banana"},
		},
		{
			name: "realistic fc-list listing",
			raw: "DejaVu Sans,DejaVu Sans Book\n" +
				"DejaVu Sans Bold,DejaVu Sans\n" +
				"Noto Sans\n" +
				"Noto Sans UI,Noto Sans\n" +
				"Ubuntu,Ubuntu Bold\n",
			want: []string{
				"DejaVu Sans",
				"DejaVu Sans Bold",
				"DejaVu Sans Book",
				"Noto Sans",
				"Noto Sans UI",
				"Ubuntu",
				"Ubuntu Bold",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFontFamilies(tt.raw)
			if got == nil {
				t.Fatal("parseFontFamilies() = nil, want a non-nil slice (wire shape must serialize as [])")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseFontFamilies(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestListFontFamilies_StubbedSeam(t *testing.T) {
	t.Run("families returned", func(t *testing.T) {
		f := &FrontendAPI{listFontFamiliesFn: func(monospace bool) ([]string, error) {
			return []string{"DejaVu Sans", "Noto Sans", "Ubuntu"}, nil
		}}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		resp := f.ListFontFamilies(false)
		if !resp.Available {
			t.Fatal("ListFontFamilies() Available = false, want true")
		}
		want := []string{"DejaVu Sans", "Noto Sans", "Ubuntu"}
		if !reflect.DeepEqual(resp.Families, want) {
			t.Errorf("ListFontFamilies() Families = %q, want %q", resp.Families, want)
		}
	})

	t.Run("reader error means unavailable, not RPC failure", func(t *testing.T) {
		f := &FrontendAPI{listFontFamiliesFn: func(monospace bool) ([]string, error) {
			return nil, errors.New("fc-list not found in PATH: exec: not found")
		}}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		resp := f.ListFontFamilies(true)
		if resp.Available {
			t.Fatal("ListFontFamilies() Available = true, want false on reader error")
		}
		if len(resp.Families) != 0 {
			t.Errorf("ListFontFamilies() Families = %q, want empty on reader error", resp.Families)
		}
	})

	t.Run("nil list with nil error is available but empty", func(t *testing.T) {
		// A fontless system (successful fc-list, zero fonts) is a working
		// enumeration mechanism: Available=true, an empty — never nil, so
		// the wire payload is [] — list.
		f := &FrontendAPI{listFontFamiliesFn: func(monospace bool) ([]string, error) {
			return nil, nil
		}}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		resp := f.ListFontFamilies(false)
		if !resp.Available {
			t.Fatal("ListFontFamilies() Available = false, want true for a successful empty listing")
		}
		if resp.Families == nil {
			t.Fatal("ListFontFamilies() Families = nil, want an empty non-nil slice")
		}
		if len(resp.Families) != 0 {
			t.Errorf("ListFontFamilies() Families = %q, want empty", resp.Families)
		}
	})

	t.Run("monospace flag is passed through to the reader", func(t *testing.T) {
		// The RPC owns no filtering of its own — the flag must reach the
		// seam verbatim (fontlist_linux.go turns it into the `:mono`
		// fontconfig pattern).
		var seen []bool
		f := &FrontendAPI{listFontFamiliesFn: func(monospace bool) ([]string, error) {
			seen = append(seen, monospace)
			return []string{}, nil
		}}
		f.seedPublished.Store(true)
		f.seedPublished.Store(true)
		f.ListFontFamilies(false)
		f.ListFontFamilies(true)
		want := []bool{false, true}
		if !reflect.DeepEqual(seen, want) {
			t.Errorf("reader received monospace flags = %v, want %v", seen, want)
		}
	})
}

// TestListFontFamilies_NilSeamUsesRealRead exercises the production read
// path (listFontFamilies): on Linux with fontconfig installed it must report
// real families; anywhere else (non-Linux build, no fc-list) the read errors
// and the RPC reports unavailable. Both outcomes are correct, so the test
// only skips when the environment read itself fails.
func TestListFontFamilies_NilSeamUsesRealRead(t *testing.T) {
	families, err := listFontFamilies(false)
	if err != nil {
		t.Skipf("listFontFamilies() unavailable in this environment: %v", err)
	}

	f := &FrontendAPI{}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	resp := f.ListFontFamilies(false)
	if !resp.Available {
		t.Fatal("ListFontFamilies() Available = false, want true when the real read succeeded")
	}
	if !reflect.DeepEqual(resp.Families, families) {
		t.Errorf("ListFontFamilies() Families = %d entries, want the %d the reader produced",
			len(resp.Families), len(families))
	}
	// Every listed name must survive post-processing intact: sorted
	// case-insensitively, no duplicates.
	seen := make(map[string]struct{}, len(resp.Families))
	for i, name := range resp.Families {
		if strings.TrimSpace(name) == "" {
			t.Errorf("Families[%d] = %q, want a non-blank family name", i, name)
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			t.Errorf("Families contains a case-insensitive duplicate: %q", name)
		}
		seen[key] = struct{}{}
		if i > 0 && strings.ToLower(resp.Families[i-1]) > key {
			t.Errorf("Families not sorted case-insensitively at [%d]: %q > %q",
				i, resp.Families[i-1], name)
		}
	}

	// The monospace narrowing (the `:mono` fontconfig pattern) must stay a
	// subset of the full listing: every family fontconfig tags as mono is
	// also an installed family. This is exactly the invariant the picker
	// relies on — the mono combobox is a filtered view of the same space.
	mono, err := listFontFamilies(true)
	if err != nil {
		t.Skipf("listFontFamilies(mono) unavailable in this environment: %v", err)
	}
	for _, name := range mono {
		if _, ok := seen[strings.ToLower(name)]; !ok {
			t.Errorf("monospace listing contains %q, which the full listing lacks", name)
		}
	}
}
