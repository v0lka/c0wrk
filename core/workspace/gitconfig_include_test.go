package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveIncludes_FingerprintsIncludeTargets pins the core of the
// include-hidden drift fix: a repo whose config includes another file has that
// file's bytes captured into the fingerprint, so changing the included file
// changes the fingerprint.
func TestResolveIncludes_FingerprintsIncludeTargets(t *testing.T) {
	// The include path is relative to the config file's directory (.git), so
	// "../extra.conf" lands on <root>/extra.conf.
	root := writeTempRepo(t, "", true, "[include]\n\tpath = ../extra.conf\n")
	extra := filepath.Join(root, "extra.conf")
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = /tmp/evil.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if len(info.Includes) != 1 {
		t.Fatalf("includes = %+v, want the one include", info.Includes)
	}

	// Before resolution the include target contributes nothing.
	baseFP := info.Fingerprint()
	info.ResolveIncludes()
	if len(info.includeSources) != 1 {
		t.Fatalf("includeSources = %+v, want 1", info.includeSources)
	}
	src := info.includeSources[0]
	want := evalDir(t, extra)
	if src.kind != "include" || src.path != want {
		t.Errorf("source = kind %q path %q, want include %q", src.kind, src.path, want)
	}
	if !strings.Contains(string(src.data), "evil.sh") {
		t.Errorf("include source must carry the included bytes, got %q", src.data)
	}
	if info.Fingerprint() == baseFP {
		t.Error("fingerprint must change once include sources are resolved")
	}

	// Changing the included file must change the fingerprint (the [1] gap).
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = /tmp/other.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig (changed): %v", err)
	}
	after.ResolveIncludes()
	if after.Fingerprint() == info.Fingerprint() {
		t.Error("fingerprint must change when an included file changes")
	}
}

// TestResolveIncludes_RecursiveAndCycleSafe pins that transitive includes are
// fingerprinted and that an include cycle terminates instead of recursing
// forever.
func TestResolveIncludes_RecursiveAndCycleSafe(t *testing.T) {
	root := writeTempRepo(t, "", true, "[include]\n\tpath = a.conf\n")
	a := filepath.Join(root, ".git", "a.conf")
	b := filepath.Join(root, ".git", "b.conf")
	if err := os.WriteFile(a, []byte("[include]\n\tpath = b.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("[include]\n\tpath = a.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes()
	if len(info.includeSources) != 2 {
		t.Fatalf("includeSources = %+v, want 2 distinct targets (a.conf, b.conf)", info.includeSources)
	}
	paths := map[string]bool{}
	for _, src := range info.includeSources {
		paths[src.path] = true
	}
	if !paths[evalDir(t, a)] || !paths[evalDir(t, b)] {
		t.Errorf("resolved paths = %v, want both a.conf and b.conf", paths)
	}
}

// TestResolveIncludes_MissingAndUnreadableMarkers pins the non-fatal handling:
// a missing include contributes an empty source (so its later appearance still
// drifts) and a non-regular target (a directory here) contributes an
// "(unreadable)" marker, neither of which makes the repo unscannable.
func TestResolveIncludes_MissingAndUnreadableMarkers(t *testing.T) {
	root := writeTempRepo(t, "", true, "[include]\n\tpath = missing.conf\n[include]\n\tpath = subdir\n")
	if err := os.MkdirAll(filepath.Join(root, ".git", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes()
	if len(info.includeSources) != 2 {
		t.Fatalf("includeSources = %+v, want 2 (missing + unreadable)", info.includeSources)
	}
	missing := info.includeSources[0]
	if missing.kind != "include" || len(missing.data) != 0 {
		t.Errorf("missing source = kind %q data %q, want include with empty data", missing.kind, missing.data)
	}
	unreadable := info.includeSources[1]
	if unreadable.kind != "include (unreadable)" {
		t.Errorf("directory source = kind %q, want include (unreadable)", unreadable.kind)
	}
}

// TestExpandHomeTilde pins the tilde expansion shared by include resolution and
// core.attributesFile resolution (current-user forms only; other-user forms are
// left verbatim).
func TestExpandHomeTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if got := expandHomeTilde("~"); got != home {
		t.Errorf("expandHomeTilde(\"~\") = %q, want %q", got, home)
	}
	if got := expandHomeTilde("~/x/y"); got != filepath.Join(home, "x", "y") {
		t.Errorf("expandHomeTilde(\"~/x/y\") = %q, want %q", got, filepath.Join(home, "x", "y"))
	}
	if got := expandHomeTilde("relative/path"); got != "relative/path" {
		t.Errorf("expandHomeTilde(relative) = %q, want unchanged", got)
	}
	if got := expandHomeTilde("~other/x"); got != "~other/x" {
		t.Errorf("expandHomeTilde(other-user) = %q, want unchanged (verbatim)", got)
	}
}
