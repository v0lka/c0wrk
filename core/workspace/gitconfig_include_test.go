package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestResolveIncludes_DoubleCallInvariant pins the load-bearing invariant the
// spawn layer's drift path builds on (plainCheckoutRawEligible resolves
// includes on a memoized scan, then the hardened path derives its overrides
// from the SAME GitConfigInfo): a repeated call must be append-only and
// idempotent — findings, records, and the Includes directive list are
// untouched, the include-source set stays a single resolution, and the
// semantic fingerprint/snapshot are exactly what the first call produced. A
// future edit that mutates records/findings or re-reads (and duplicates)
// resolved targets would silently desynchronize the hardened path's override
// derivation from a fresh scan's identity.
func TestResolveIncludes_DoubleCallInvariant(t *testing.T) {
	root := writeTempRepo(t, "", true, "[core]\n\tfsmonitor = /tmp/evil.sh\n[include]\n\tpath = ../extra.conf\n")
	extra := filepath.Join(root, "extra.conf")
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = /tmp/inc.sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(4, "../extra.conf"))

	info, err := ScanGitConfig(root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	findingsBefore := info.Findings
	recordsBefore := info.records
	includesBefore := info.Includes

	info.ResolveIncludes(logger)
	firstFingerprint := info.SemanticFingerprint()
	firstSnapshot := info.SemanticSnapshot()
	sourcesAfterFirst := len(info.includeSources)
	if sourcesAfterFirst == 0 {
		t.Fatal("the first ResolveIncludes call must resolve the include target")
	}

	// The double call: nothing may change.
	info.ResolveIncludes(logger)
	if len(info.includeSources) != sourcesAfterFirst {
		t.Errorf("includeSources = %d entries after the second call, want the single resolution's %d (no duplicated targets)",
			len(info.includeSources), sourcesAfterFirst)
	}
	if got := info.SemanticFingerprint(); got != firstFingerprint {
		t.Errorf("SemanticFingerprint changed across the double call: %q != %q", got, firstFingerprint)
	}
	if !bytes.Equal(info.SemanticSnapshot(), firstSnapshot) {
		t.Error("SemanticSnapshot changed across the double call")
	}
	if !reflect.DeepEqual(info.Findings, findingsBefore) {
		t.Errorf("Findings mutated across the double call: %#v != %#v", info.Findings, findingsBefore)
	}
	if !reflect.DeepEqual(info.records, recordsBefore) {
		t.Error("records mutated across the double call")
	}
	if !reflect.DeepEqual(info.Includes, includesBefore) {
		t.Errorf("Includes mutated across the double call: %#v != %#v", info.Includes, includesBefore)
	}

	// The resolved identity still binds the included bytes: a fingerprint
	// including the target proves idempotency did not degenerate into
	// skipping resolution altogether.
	if !strings.Contains(string(firstSnapshot), "/tmp/inc.sh") {
		t.Error("the semantic snapshot must still carry the included file's bytes")
	}
}

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

	logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(2, "../extra.conf"), ignoredIncludeDiagnostic(2, "../extra.conf"))

	info, err := ScanGitConfig(root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if len(info.Includes) != 1 {
		t.Fatalf("includes = %+v, want the one include", info.Includes)
	}

	// Before resolution the include target contributes nothing.
	baseFP := info.Fingerprint()
	info.ResolveIncludes(logger)
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
	after, err := ScanGitConfig(root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig (changed): %v", err)
	}
	after.ResolveIncludes(logger)
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

	logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(2, "a.conf"), ignoredIncludeDiagnostic(2, "b.conf"), ignoredIncludeDiagnostic(2, "a.conf"))

	info, err := ScanGitConfig(root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes(logger)
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

	logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(2, "missing.conf"), ignoredIncludeDiagnostic(4, "subdir"), unreadableIncludeDiagnostic(evalDir(t, filepath.Join(root, ".git", "subdir"))))

	info, err := ScanGitConfig(root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes(logger)
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
