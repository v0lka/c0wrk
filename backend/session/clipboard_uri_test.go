package session

import (
	"testing"
)

// TestParseFileURIList is a platform-agnostic table-driven test for the
// text/uri-list and x-special/gnome-copied-files parser shared by the Linux
// clipboardFiles reader. Parsing is a pure string transform, so the test does
// not need the linux build tag.
func TestParseFileURIList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "empty input yields nil",
			raw:  "",
			want: nil,
		},
		{
			name: "whitespace-only input yields nil",
			raw:  "   \n\t  \n",
			want: nil,
		},
		{
			name: "single text/uri-list entry",
			raw:  "file:///home/user/doc.md",
			want: []string{"/home/user/doc.md"},
		},
		{
			name: "multiple text/uri-list entries preserve order",
			raw:  "file:///a.txt\nfile:///b.txt\nfile:///c.txt",
			want: []string{"/a.txt", "/b.txt", "/c.txt"},
		},
		{
			name: "gnome copied-files format with copy header",
			raw:  "copy\nfile:///home/user/note.md\nfile:///home/user/img.png",
			want: []string{"/home/user/note.md", "/home/user/img.png"},
		},
		{
			name: "gnome copied-files format with cut header",
			raw:  "cut\nfile:///tmp/x.go",
			want: []string{"/tmp/x.go"},
		},
		{
			name: "url-encoded spaces are decoded",
			raw:  "file:///home/user/my%20documents/report.md",
			want: []string{"/home/user/my documents/report.md"},
		},
		{
			name: "url-encoded unicode is decoded",
			raw:  "file:///home/user/caf%C3%A9.txt",
			want: []string{"/home/user/café.txt"},
		},
		{
			name: "non-file lines are skipped",
			raw:  "file:///a.txt\nhttp://example.com/b\nnot-a-uri\ncfile:///c.txt",
			want: []string{"/a.txt"},
		},
		{
			name: "blank lines between entries are ignored",
			raw:  "file:///a.txt\n\n\nfile:///b.txt",
			want: []string{"/a.txt", "/b.txt"},
		},
		{
			name: "trailing/leading whitespace per line is trimmed",
			raw:  "  file:///a.txt  \n\tfile:///b.txt\t",
			want: []string{"/a.txt", "/b.txt"},
		},
		{
			name: "empty decoded path is skipped",
			raw:  "file://",
			want: nil,
		},
		{
			name: "mixed valid and malformed uris keep the valid ones",
			raw:  "file:///good.txt\nfile://%%bad\nfile:///also-good.txt",
			want: []string{"/good.txt", "/also-good.txt"},
		},
		{
			name: "carriage returns are handled (CRLF line endings)",
			raw:  "file:///a.txt\r\nfile:///b.txt\r\n",
			want: []string{"/a.txt", "/b.txt"},
		},
		{
			name: "percent-encoded percent is decoded exactly once",
			raw:  "file:///home/user/100%25.txt",
			want: []string{"/home/user/100%.txt"},
		},
		{
			name: "double-encoded space keeps the literal percent sequence",
			raw:  "file:///home/user/foo%2520bar.txt",
			want: []string{"/home/user/foo%20bar.txt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseFileURIList(tc.raw)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseFileURIList(%q) = %v (len %d), want %v (len %d)",
					tc.raw, got, len(got), tc.want, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("parseFileURIList(%q)[%d] = %q, want %q",
						tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}
