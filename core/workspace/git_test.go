package workspace

import (
	"testing"
)

func TestParseReviewDiff(t *testing.T) {
	tests := map[string]struct {
		input string
		want  []ReviewFileDiff
	}{
		"empty diff returns empty slice": {
			input: "",
			want:  []ReviewFileDiff{},
		},
		"whitespace-only diff returns empty slice": {
			input: "  \n  \n",
			want:  []ReviewFileDiff{},
		},
		"single file single hunk": {
			input: "diff --git a/main.go b/main.go\n" +
				"index abc..def 100644\n" +
				"--- a/main.go\n" +
				"+++ b/main.go\n" +
				"@@ -10,3 +10,4 @@ func main() {\n" +
				" \told line\n" +
				" \told line 2\n" +
				"+\tnew line\n" +
				" \told line 3\n",
			want: []ReviewFileDiff{
				{
					Path:    "main.go",
					OldPath: "",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -10,3 +10,4 @@ func main() {\n \told line\n \told line 2\n+\tnew line\n \told line 3",
							OldStart: 10,
							OldCount: 3,
							NewStart: 10,
							NewCount: 4,
						},
					},
				},
			},
		},
		"single file with default count (omitted counts default to 1)": {
			input: "diff --git a/foo.go b/foo.go\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -5 +5,2 @@\n" +
				" context\n" +
				"+added\n",
			want: []ReviewFileDiff{
				{
					Path: "foo.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -5 +5,2 @@\n context\n+added",
							OldStart: 5,
							OldCount: 1,
							NewStart: 5,
							NewCount: 2,
						},
					},
				},
			},
		},
		"multiple files with multiple hunks": {
			input: "diff --git a/a.go b/a.go\n" +
				"--- a/a.go\n" +
				"+++ b/a.go\n" +
				"@@ -1,2 +1,3 @@\n" +
				" ctx\n" +
				" ctx2\n" +
				"+add\n" +
				"@@ -10,2 +11,2 @@\n" +
				" ctx\n" +
				"-old\n" +
				"+new\n" +
				"diff --git a/b.go b/b.go\n" +
				"--- a/b.go\n" +
				"+++ b/b.go\n" +
				"@@ -3,1 +3,1 @@\n" +
				"-del\n" +
				"+ins\n",
			want: []ReviewFileDiff{
				{
					Path: "a.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -1,2 +1,3 @@\n ctx\n ctx2\n+add",
							OldStart: 1,
							OldCount: 2,
							NewStart: 1,
							NewCount: 3,
						},
						{
							Raw:      "@@ -10,2 +11,2 @@\n ctx\n-old\n+new",
							OldStart: 10,
							OldCount: 2,
							NewStart: 11,
							NewCount: 2,
						},
					},
				},
				{
					Path: "b.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -3,1 +3,1 @@\n-del\n+ins",
							OldStart: 3,
							OldCount: 1,
							NewStart: 3,
							NewCount: 1,
						},
					},
				},
			},
		},
		"rename with differing a/ and b/ paths populates OldPath": {
			input: "diff --git a/old_name.go b/new_name.go\n" +
				"similarity index 90%\n" +
				"rename from old_name.go\n" +
				"rename to new_name.go\n" +
				"--- a/old_name.go\n" +
				"+++ b/new_name.go\n" +
				"@@ -1,1 +1,1 @@\n" +
				"-old content\n" +
				"+new content\n",
			want: []ReviewFileDiff{
				{
					Path:    "new_name.go",
					OldPath: "old_name.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -1,1 +1,1 @@\n-old content\n+new content",
							OldStart: 1,
							OldCount: 1,
							NewStart: 1,
							NewCount: 1,
						},
					},
				},
			},
		},
		"pure rename with no hunks has empty Hunks": {
			input: "diff --git a/old.go b/new.go\n" +
				"similarity index 100%\n" +
				"rename from old.go\n" +
				"rename to new.go\n",
			want: []ReviewFileDiff{
				{
					Path:    "new.go",
					OldPath: "old.go",
					Hunks:   nil,
				},
			},
		},
		"git-quoted path with spaces is unquoted": {
			input: "diff --git \"a/my file.go\" \"b/my file.go\"\n" +
				"--- \"a/my file.go\"\n" +
				"+++ \"b/my file.go\"\n" +
				"@@ -1,1 +1,2 @@\n" +
				" ctx\n" +
				"+add\n",
			want: []ReviewFileDiff{
				{
					Path: "my file.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -1,1 +1,2 @@\n ctx\n+add",
							OldStart: 1,
							OldCount: 1,
							NewStart: 1,
							NewCount: 2,
						},
					},
				},
			},
		},
		"no newline at end of file marker is preserved in raw hunk": {
			input: "diff --git a/no_nl.go b/no_nl.go\n" +
				"--- a/no_nl.go\n" +
				"+++ b/no_nl.go\n" +
				"@@ -1,1 +1,2 @@\n" +
				" ctx\n" +
				"+add\n" +
				"\\ No newline at end of file\n",
			want: []ReviewFileDiff{
				{
					Path: "no_nl.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -1,1 +1,2 @@\n ctx\n+add\n\\ No newline at end of file",
							OldStart: 1,
							OldCount: 1,
							NewStart: 1,
							NewCount: 2,
						},
					},
				},
			},
		},
		"context line that looks like a diff header is not treated as a new file": {
			input: "diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n" +
				"+++ b/main.go\n" +
				"@@ -1,3 +1,4 @@\n" +
				" \tcode\n" +
				" \t// diff --git a/fake.go b/fake.go\n" +
				" \tcode2\n" +
				"+\tadd\n",
			want: []ReviewFileDiff{
				{
					Path: "main.go",
					Hunks: []ReviewHunk{
						{
							Raw:      "@@ -1,3 +1,4 @@\n \tcode\n \t// diff --git a/fake.go b/fake.go\n \tcode2\n+\tadd",
							OldStart: 1,
							OldCount: 3,
							NewStart: 1,
							NewCount: 4,
						},
					},
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := ParseReviewDiff(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseReviewDiff returned %d files, want %d", len(got), len(tt.want))
			}
			for i, gf := range got {
				wf := tt.want[i]
				if gf.Path != wf.Path {
					t.Errorf("file[%d].Path = %q, want %q", i, gf.Path, wf.Path)
				}
				if gf.OldPath != wf.OldPath {
					t.Errorf("file[%d].OldPath = %q, want %q", i, gf.OldPath, wf.OldPath)
				}
				if len(gf.Hunks) != len(wf.Hunks) {
					t.Fatalf("file[%d] has %d hunks, want %d", i, len(gf.Hunks), len(wf.Hunks))
				}
				for j, gh := range gf.Hunks {
					wh := wf.Hunks[j]
					if gh.Raw != wh.Raw {
						t.Errorf("file[%d].hunk[%d].Raw = %q, want %q", i, j, gh.Raw, wh.Raw)
					}
					if gh.OldStart != wh.OldStart {
						t.Errorf("file[%d].hunk[%d].OldStart = %d, want %d", i, j, gh.OldStart, wh.OldStart)
					}
					if gh.OldCount != wh.OldCount {
						t.Errorf("file[%d].hunk[%d].OldCount = %d, want %d", i, j, gh.OldCount, wh.OldCount)
					}
					if gh.NewStart != wh.NewStart {
						t.Errorf("file[%d].hunk[%d].NewStart = %d, want %d", i, j, gh.NewStart, wh.NewStart)
					}
					if gh.NewCount != wh.NewCount {
						t.Errorf("file[%d].hunk[%d].NewCount = %d, want %d", i, j, gh.NewCount, wh.NewCount)
					}
				}
			}
		})
	}
}
