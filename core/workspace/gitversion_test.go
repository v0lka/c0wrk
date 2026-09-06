package workspace

import (
	"testing"
)

// TestParseGitVersion pins the `git --version` output parser: the canonical
// "git version X.Y.Z" spelling, a bare version token, the Apple-suffixed
// variant, and the fail-closed refusals (garbage, empty, missing pair).
func TestParseGitVersion(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    gitVersion
		wantErr bool
	}{
		{"canonical", "git version 2.44.9\n", gitVersion{major: 2, minor: 44}, false},
		{"canonical no newline", "git version 2.45.0", gitVersion{major: 2, minor: 45}, false},
		{"bare token", "2.45.0", gitVersion{major: 2, minor: 45}, false},
		{"apple suffix", "git version 2.50.1 (Apple Git-157)", gitVersion{major: 2, minor: 50}, false},
		{"bare with suffix", "2.50.1 (Apple Git-157)\n", gitVersion{major: 2, minor: 50}, false},
		{"whitespace padded", "  git version 2.45.0  \n", gitVersion{major: 2, minor: 45}, false},
		{"garbage", "garbage", gitVersion{}, true},
		{"empty", "", gitVersion{}, true},
		{"prefix only", "git version", gitVersion{}, true},
		{"prefix only with space", "git version ", gitVersion{}, true},
		{"non numeric", "git version two.fifty", gitVersion{}, true},
		{"major only", "git version 2", gitVersion{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGitVersion(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseGitVersion(%q) = %+v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitVersion(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseGitVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestGitVersionLessThan pins the tuple ordering the attr.tree gate
// compares with: minor-aware within a major, major-aware across.
func TestGitVersionLessThan(t *testing.T) {
	pairs := []struct {
		v, o gitVersion
		want bool
	}{
		{gitVersion{major: 2, minor: 44}, gitVersion{major: 2, minor: 45}, true},
		{gitVersion{major: 2, minor: 45}, gitVersion{major: 2, minor: 45}, false},
		{gitVersion{major: 2, minor: 50}, gitVersion{major: 2, minor: 45}, false},
		{gitVersion{major: 1, minor: 99}, gitVersion{major: 2, minor: 45}, true},
		{gitVersion{major: 3, minor: 0}, gitVersion{major: 2, minor: 45}, false},
	}
	for _, p := range pairs {
		if got := p.v.lessThan(p.o); got != p.want {
			t.Errorf("%+v.lessThan(%+v) = %v, want %v", p.v, p.o, got, p.want)
		}
	}
}
