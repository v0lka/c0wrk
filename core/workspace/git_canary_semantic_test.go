package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Semantic-fingerprint canary tests ───────────────────────────────────────
//
// SemanticFingerprint/SemanticSnapshot narrow trust-drift detection to the
// DANGEROUS subset of a repository's git config. The allowlist direction is
// pinned here end-to-end: writing and deleting a [branch] section (what git
// itself does on --set-upstream) and adding an [alias] entry must NOT move
// the semantic fingerprint — while the byte-level fingerprint DOES move,
// proving the semantic layer, not an absence of change, absorbs the churn.
// The fail-closed direction is pinned the same way: a command key
// (core.fsmonitor), a filter driver command (filter.x.process), an unknown
// key under an unknown section, and a change to .git/info/attributes must
// each move it. The classifier's unit behavior (case-insensitivity,
// subsections, multi-value, BOM) is pinned in gitconfig_test.go.
//
// ScanGitConfig is exec-free (it never spawns git), so the fixtures below
// are manually constructed repository directories — no git binary, no
// shell, and no POSIX gate is needed.

// semanticCanaryRepo builds a minimal repository the ScanGitConfig pipeline
// accepts: a .git/config (written by the tests) plus a benign
// .git/info/attributes routing source, so the attributes canary proves
// CHANGE detection rather than mere appearance of the source.
func semanticCanaryRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatalf("creating .git/info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.txt text\n"), 0o644); err != nil {
		t.Fatalf("writing .git/info/attributes: %v", err)
	}
	return root
}

// semanticWriteRepoConfig (over)writes the .git/config of a canary repo.
func semanticWriteRepoConfig(t *testing.T, root, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(config), 0o644); err != nil {
		t.Fatalf("writing .git/config: %v", err)
	}
}

// semanticScanFingerprint scans the canary repo through the real pipeline
// and returns both fingerprints: the byte-level one and the semantic one.
func semanticScanFingerprint(t *testing.T, root string) (raw, semantic string) {
	t.Helper()
	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	return info.Fingerprint(), info.SemanticFingerprint()
}

// TestCanarySemanticFingerprintIgnoresBranchAndAlias arms the allowlist
// direction: [branch] section write AND delete, plus an [alias] entry, must
// leave the semantic fingerprint identical to the baseline, while the
// byte-level fingerprint moves on every one of those edits.
func TestCanarySemanticFingerprintIgnoresBranchAndAlias(t *testing.T) {
	const base = "[core]\n\trepositoryformatversion = 0\n"
	root := semanticCanaryRepo(t)
	semanticWriteRepoConfig(t, root, base)
	rawBase, semBase := semanticScanFingerprint(t, root)

	// Writing a [branch] section (git's own --set-upstream churn).
	semanticWriteRepoConfig(t, root, base+"[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n")
	raw1, sem1 := semanticScanFingerprint(t, root)
	if sem1 != semBase {
		t.Errorf("adding a [branch] section changed the semantic fingerprint (%s -> %s)", semBase, sem1)
	}
	if raw1 == rawBase {
		t.Error("adding [branch] must still change the byte-level fingerprint — the semantic layer is what absorbs the churn")
	}

	// Deleting it again: back to the identical state on both layers.
	semanticWriteRepoConfig(t, root, base)
	raw2, sem2 := semanticScanFingerprint(t, root)
	if sem2 != semBase {
		t.Errorf("deleting a [branch] section changed the semantic fingerprint (%s -> %s)", semBase, sem2)
	}
	if raw2 != rawBase {
		t.Error("restoring the config bytes must restore the byte-level fingerprint (scan must be deterministic)")
	}

	// An [alias] entry joins the config: same tolerance.
	semanticWriteRepoConfig(t, root, base+"[alias]\n\tco = checkout\n")
	_, sem3 := semanticScanFingerprint(t, root)
	if sem3 != semBase {
		t.Errorf("adding an [alias] entry changed the semantic fingerprint (%s -> %s)", semBase, sem3)
	}
}

// TestCanarySemanticFingerprintDetectsSemanticDrift arms the fail-closed
// direction: every addition outside the allowlist — a command key, a filter
// driver command, an unknown key under an unknown section — and any change
// to .git/info/attributes must change the semantic fingerprint.
func TestCanarySemanticFingerprintDetectsSemanticDrift(t *testing.T) {
	const base = "[core]\n\trepositoryformatversion = 0\n"
	for _, tc := range []struct {
		name     string
		addition string
	}{
		{"core.fsmonitor", "[core]\n\tfsmonitor = /tmp/evil\n"},
		{"filter.process", "[filter \"x\"]\n\tprocess = /tmp/evil\n"},
		{"unknown key", "[misc]\n\tsomekey = somevalue\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := semanticCanaryRepo(t)
			semanticWriteRepoConfig(t, root, base)
			_, before := semanticScanFingerprint(t, root)
			semanticWriteRepoConfig(t, root, base+tc.addition)
			_, after := semanticScanFingerprint(t, root)
			if before == after {
				t.Errorf("adding %s must change the semantic fingerprint", tc.name)
			}
		})
	}

	t.Run("info/attributes change", func(t *testing.T) {
		root := semanticCanaryRepo(t)
		semanticWriteRepoConfig(t, root, base)
		_, before := semanticScanFingerprint(t, root)
		if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.ps1 filter=evil\n"), 0o644); err != nil {
			t.Fatalf("rewriting .git/info/attributes: %v", err)
		}
		_, after := semanticScanFingerprint(t, root)
		if before == after {
			t.Error("changing .git/info/attributes must change the semantic fingerprint")
		}
		// Attribute routing sources serialize verbatim into the semantic
		// snapshot, so the diff names the exact routing change.
		info, err := ScanGitConfig(root)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if !strings.Contains(string(info.SemanticSnapshot()), "filter=evil") {
			t.Errorf("semantic snapshot must carry the info/attributes source verbatim, got %q", info.SemanticSnapshot())
		}
	})
}
