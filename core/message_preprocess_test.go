package core

import "testing"

// TestPreprocessMessageText verifies the user-message preprocessor. It focuses
// on the @file → fileref:// conversion, including GitHub-style line/line-range
// anchors (#L20, #L20-L36, #L5-10), legacy bare-number anchors (#42, #5-10),
// plain paths, as well as /skill stripping and multi-space collapsing.
func TestPreprocessMessageText(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		activeSkills []string
		want         string
	}{
		// GitHub-style L-anchored refs (the core regression this test guards).
		{
			name: "L-range anchor with nested path",
			text: "@desktop/x.go#L20-L36",
			want: "fileref://desktop/x.go#L20-L36",
		},
		{
			name: "L-prefixed start, bare-number end (#L5-10)",
			text: "@x.go#L5-10",
			want: "fileref://x.go#L5-10",
		},
		{
			name: "L-prefixed start and end (#L5-L10)",
			text: "@x.go#L5-L10",
			want: "fileref://x.go#L5-L10",
		},
		{
			name: "single L-anchored line (#L42)",
			text: "@x.go#L42",
			want: "fileref://x.go#L42",
		},

		// Legacy bare-number anchors must keep working unchanged.
		{
			name: "single bare-number line (#42)",
			text: "@x.go#42",
			want: "fileref://x.go#42",
		},
		{
			name: "bare-number range (#5-10)",
			text: "@x.go#5-10",
			want: "fileref://x.go#5-10",
		},

		// Plain path with no anchor.
		{
			name: "plain path no anchor",
			text: "@x.go",
			want: "fileref://x.go",
		},

		// Ref embedded in surrounding prose preserves the prose and surrounding spaces.
		{
			name: "anchored ref inside prose",
			text: "see @x.go#L20-L36 here",
			want: "see fileref://x.go#L20-L36 here",
		},

		// /skill stripping still behaves: the skill ref and its surrounding spaces
		// collapse to a single space.
		{
			name:         "skill strip collapses surrounding spaces",
			text:         "hello /explore world",
			activeSkills: []string{"explore"},
			want:         "hello world",
		},
		{
			name:         "skill strip at start leaves no leading space",
			text:         "/explore do stuff",
			activeSkills: []string{"explore"},
			want:         "do stuff",
		},

		// Multiple spaces collapse into one after all transforms.
		{
			name: "multi-space collapse",
			text: "hello   world",
			want: "hello world",
		},
		{
			name: "leading and trailing whitespace trimmed",
			text: "   @x.go#L5-L10   ",
			want: "fileref://x.go#L5-L10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreprocessMessageText(tt.text, tt.activeSkills)
			if got != tt.want {
				t.Errorf("PreprocessMessageText(%q, %v) =\n  got:  %q\n  want: %q",
					tt.text, tt.activeSkills, got, tt.want)
			}
		})
	}
}
