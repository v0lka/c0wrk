package desktop

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/markitdown"
)

// TestAttachmentFilterPattern_ConventionalForm verifies that the file-picker
// pattern uses the cross-platform "*.ext" convention (not bare extensions),
// so the native dialog actually restricts selection to markitdown-supported
// formats instead of silently allowing everything.
func TestAttachmentFilterPattern_ConventionalForm(t *testing.T) {
	pattern := attachmentFilterPattern()
	if pattern == "" {
		t.Fatal("expected a non-empty filter pattern")
	}

	entries := strings.Split(pattern, ";")
	if len(entries) == 0 {
		t.Fatal("expected at least one filter entry")
	}

	// Every entry must use the "*.ext" form.
	for _, e := range entries {
		if !strings.HasPrefix(e, "*.") {
			t.Errorf("filter entry %q does not use the *.ext convention", e)
		}
		if len(e) <= 2 {
			t.Errorf("filter entry %q has no extension after *.", e)
		}
	}

	// Every supported extension must appear in the pattern.
	for _, ext := range markitdown.SupportedExtensions() {
		want := "*." + ext
		if !strings.Contains(pattern, want) {
			t.Errorf("supported extension %q missing from filter pattern %q", ext, pattern)
		}
	}
}

// TestAttachmentFilterPattern_NoDuplicates ensures each extension appears at
// most once in the pattern.
func TestAttachmentFilterPattern_NoDuplicates(t *testing.T) {
	pattern := attachmentFilterPattern()
	entries := strings.Split(pattern, ";")
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e] {
			t.Errorf("duplicate filter entry %q", e)
		}
		seen[e] = true
	}
}
