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
		activeAgents []string
		workspace    string
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

		// #agent-name stripping mirrors /skill: only known agent names are
		// removed, preserving surrounding whitespace boundaries.
		{
			name:         "agent strip collapses surrounding spaces",
			text:         "hello #code-reviewer world",
			activeAgents: []string{"code-reviewer"},
			want:         "hello world",
		},
		{
			name:         "agent strip at start leaves no leading space",
			text:         "#code-reviewer do stuff",
			activeAgents: []string{"code-reviewer"},
			want:         "do stuff",
		},
		{
			name:         "multiple agent mentions stripped",
			text:         "#code-reviewer and #test-writer please",
			activeAgents: []string{"code-reviewer", "test-writer"},
			want:         "and please",
		},
		// A GitHub-style @file#L20 line anchor is NOT matched as an agent ref:
		// the "#" is glued to the file path (no preceding whitespace) and
		// "L20" is not a known agent name regardless.
		{
			name:         "@file#L20 line anchor not stripped as agent",
			text:         "see @x.go#L20 here",
			activeAgents: []string{"L20"},
			want:         "see fileref://x.go#L20 here",
		},
		// Collision guard: #review, /review, and @review are three distinct
		// references handled by independent preprocessors.
		{
			name:         "collision review: only known agent stripped",
			text:         "#review /review @review",
			activeAgents: []string{"review"},
			activeSkills: []string{"review"},
			want:         "fileref://review",
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

		// Relative @file paths are resolved against the workspace so the LLM
		// prompt carries unambiguous absolute paths. The line anchor is split
		// off during resolution and re-attached verbatim.
		{
			name:      "relative path resolved against workspace",
			text:      "see @main.go here",
			workspace: "/ws",
			want:      "see fileref:///ws/main.go here",
		},
		{
			name:      "nested relative path with L-range anchor",
			text:      "@desktop/x.go#L20-L36",
			workspace: "/ws",
			want:      "fileref:///ws/desktop/x.go#L20-L36",
		},
		{
			name:      "relative path with dot segment cleaned",
			text:      "@./src/a.go#L5",
			workspace: "/ws",
			want:      "fileref:///ws/src/a.go#L5",
		},
		{
			name:      "absolute path left unchanged",
			text:      "@/abs/path/x.go",
			workspace: "/ws",
			want:      "fileref:///abs/path/x.go",
		},
		{
			name:      "home-relative path left unchanged",
			text:      "@~/notes/x.go",
			workspace: "/ws",
			want:      "fileref://~/notes/x.go",
		},
		{
			name:      "no workspace leaves relative path untouched",
			text:      "@x.go#L10",
			workspace: "",
			want:      "fileref://x.go#L10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreprocessMessageText(tt.text, tt.activeSkills, tt.activeAgents, tt.workspace)
			if got != tt.want {
				t.Errorf("PreprocessMessageText(%q, %v, %v, %q) =\n  got:  %q\n  want: %q",
					tt.text, tt.activeSkills, tt.activeAgents, tt.workspace, got, tt.want)
			}
		})
	}
}
