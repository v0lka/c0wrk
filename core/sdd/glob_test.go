package sdd

import "testing"

// TestMatchPattern pins the glob vocabulary a concept's `match.glob` uses: `*`
// and `?` match inside one segment, `**` matches zero or more segments, and a
// pattern never crosses into an absolute or parent path.
func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"META.md", "META.md", true},
		{"META.md", "INDEX.md", false},
		{"META.md", "domains/META.md", false},
		{"architecture/*.md", "architecture/layers.md", true},
		{"architecture/*.md", "architecture/backend/layers.md", false},
		{"architecture/*.md", "architecture", false},
		{"*", "001-login", true},
		{"*", "a/b", false},
		{"*/spec.md", "001-login/spec.md", true},
		{"*/spec.md", "spec.md", false},
		{"*/spec.md", "a/b/spec.md", false},
		{"domains/**/*.md", "domains/app.md", true},
		{"domains/**/*.md", "domains/a/app.md", true},
		{"domains/**/*.md", "domains/a/b/app.md", true},
		{"domains/**/*.md", "domainsx/app.md", false},
		{"domains/**/*.md", "domains/app.txt", false},
		{"**", "any/deep/path.md", true},
		{"**", "", true},
		{"changes/archive", "changes/archive", true},
		{"changes/archive", "changes/archive/2025-01-old", false},
		{"changes/archive/*", "changes/archive/2025-01-old", true},
		{"decisions/_*.md", "decisions/_template.md", true},
		{"decisions/_*.md", "decisions/template.md", false},
		{"a?c.md", "abc.md", true},
		{"a?c.md", "ac.md", false},
		{"changes/*/specs/*/spec.md", "changes/add-mfa/specs/auth/spec.md", true},
		{"changes/*/specs/*/spec.md", "changes/add-mfa/specs/auth/design.md", false},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// TestClassify pins the overlap rule: directories match directory concepts and
// files match file concepts, and the most specific glob wins an overlap.
func TestClassify(t *testing.T) {
	cases := []struct {
		framework string
		root      string
		rel       string
		dir       bool
		want      string // concept kind; "" for no match
	}{
		{"vibespec", "specs", "INDEX.md", false, "index"},
		{"vibespec", "specs", "META.md", false, "meta"},
		{"vibespec", "specs", "architecture/layers.md", false, "architecture"},
		{"vibespec", "specs", "domains/app.md", false, "domain"},
		{"vibespec", "specs", "domains/backend/app.md", false, "domain"},
		{"vibespec", "specs", "contracts/api.md", false, "contract"},
		{"vibespec", "specs", "decisions/001-one.md", false, "decision"},
		// The more specific `decisions/_*.md` wins over `decisions/*.md`.
		{"vibespec", "specs", "decisions/_template.md", false, "template"},
		{"vibespec", "specs", "unmatched.txt", false, ""},

		{"openspec", "openspec", "specs/auth", true, "capability"},
		{"openspec", "openspec", "specs/auth/spec.md", false, "spec"},
		{"openspec", "openspec", "changes/add-mfa", true, "change"},
		{"openspec", "openspec", "changes/add-mfa/proposal.md", false, "proposal"},
		{"openspec", "openspec", "changes/add-mfa/specs/auth/spec.md", false, "delta"},
		// `changes/archive` is both a `changes/*` directory and the `archive`
		// concept; the deeper fixed prefix wins.
		{"openspec", "openspec", "changes/archive", true, "archive"},
		{"openspec", "openspec", "changes/archive/2025-01-old", true, "archived-change"},
		{"openspec", "openspec", "schemas/workflow/schema.yaml", false, "schema"},
		// A directory path never matches a file concept and vice versa.
		{"openspec", "openspec", "specs/auth/spec.md", true, ""},

		{"spec-kit", "specs", "001-login", true, "feature"},
		{"spec-kit", "specs", "001-login/spec.md", false, "spec"},
		{"spec-kit", "specs", "001-login/contracts", true, "contract"},
		{"spec-kit", "specify", "memory/constitution.md", false, "constitution"},
		{"spec-kit", "specify", "templates/spec-template.md", false, "template"},
	}
	for _, tc := range cases {
		fw, err := Builtin(tc.framework)
		if err != nil {
			t.Fatalf("Builtin(%q) error = %v", tc.framework, err)
		}
		concept := classify(&fw, tc.root, tc.rel, tc.dir)
		got := ""
		if concept != nil {
			got = concept.Kind
		}
		if got != tc.want {
			t.Errorf("%s: classify(%q, dir=%v) = %q, want %q", tc.framework, tc.rel, tc.dir, got, tc.want)
		}
	}
}

// TestPatternSpecificity pins the score the overlap rule compares.
func TestPatternSpecificity(t *testing.T) {
	cases := []struct {
		pattern   string
		wantDepth int
		wantLen   int
	}{
		{"META.md", 1, len("META.md")},
		{"architecture/*.md", 1, len("architecture/*.md")},
		{"domains/**/*.md", 1, len("domains/**/*.md")},
		{"changes/archive/*", 2, len("changes/archive/*")},
		{"*", 0, 1},
		{"*/spec.md", 0, len("*/spec.md")},
	}
	for _, tc := range cases {
		depth, length := patternSpecificity(tc.pattern)
		if depth != tc.wantDepth || length != tc.wantLen {
			t.Errorf("patternSpecificity(%q) = (%d, %d), want (%d, %d)",
				tc.pattern, depth, length, tc.wantDepth, tc.wantLen)
		}
	}
}
