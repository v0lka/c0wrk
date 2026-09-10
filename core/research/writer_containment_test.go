package research

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// symlinkEscapeFixture builds a research root whose only project has its
// hypotheses/ directory replaced by a symlink to a directory OUTSIDE the
// root — the cloned-repo attack shape from review finding [3]: parsing reads
// through the symlink, so without a containment gate the mutation would stage
// (os.CreateTemp) and rename card/graph writes into the outside directory.
// The card and graph are seeded in the outside target first (moved out of the
// project), exactly like a repo whose .research tree carries the symlink with
// the real content living elsewhere.
func symlinkEscapeFixture(t *testing.T) (root, projectDir, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "research-root")
	projectDir = filepath.Join(root, "R-001-test")
	hypDir := filepath.Join(projectDir, "hypotheses")
	outside = filepath.Join(base, "escape-target")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hyp dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("MkdirAll outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hypDir, "H-001.md"), []byte("# H-001: Old title\n"), 0o644); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hypDir, "graph.md"), []byte("# Hypothesis Graph\n"), 0o644); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	// Swap the hypotheses dir for a symlink to the outside directory.
	for _, name := range []string{"H-001.md", "graph.md"} {
		if err := os.Rename(filepath.Join(hypDir, name), filepath.Join(outside, name)); err != nil {
			t.Fatalf("move %s out: %v", name, err)
		}
	}
	if err := os.Remove(hypDir); err != nil {
		t.Fatalf("remove real hyp dir: %v", err)
	}
	if err := os.Symlink(outside, hypDir); err != nil {
		t.Fatalf("symlink hyp dir: %v", err)
	}
	return root, projectDir, outside
}

// assertNothingEscaped verifies the outside directory is byte-for-byte
// unchanged and carries no staged temp files.
func assertNothingEscaped(t *testing.T, outside string) {
	t.Helper()
	cardRaw, err := os.ReadFile(filepath.Join(outside, "H-001.md"))
	if err != nil {
		t.Fatalf("read outside card: %v", err)
	}
	if string(cardRaw) != "# H-001: Old title\n" {
		t.Errorf("outside card was modified: %q", string(cardRaw))
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir outside: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hyp-") {
			t.Errorf("temp file staged outside the research root: %s", e.Name())
		}
		if e.Name() == "H-002.md" {
			t.Errorf("new hypothesis card written outside the research root")
		}
	}
}

// TestUpdateHypothesis_SymlinkedHypothesesDirBlocked verifies the [3]
// containment gate: a symlinked intermediate directory must block the card +
// graph mutation before anything is staged or renamed, even though the reads
// (parse) happily follow the symlink.
func TestUpdateHypothesis_SymlinkedHypothesesDirBlocked(t *testing.T) {
	root, projectDir, outside := symlinkEscapeFixture(t)

	status := "in-progress"
	err := UpdateHypothesis(root, projectDir, "H-001", HypothesisUpdate{Status: &status})
	if err == nil {
		t.Fatal("expected mutation through a symlinked intermediate directory to be rejected")
	}
	if !strings.Contains(err.Error(), "outside the research root") {
		t.Errorf("expected a containment rejection, got: %v", err)
	}
	assertNothingEscaped(t, outside)
}

// TestCreateHypothesis_SymlinkedHypothesesDirBlocked is the create-side
// twin: a new card (and the graph rewrite) must not land outside the root
// through a symlinked hypotheses/ directory.
func TestCreateHypothesis_SymlinkedHypothesesDirBlocked(t *testing.T) {
	root, projectDir, outside := symlinkEscapeFixture(t)

	if _, err := CreateHypothesis(root, projectDir, NewHypothesis{Title: "Escape attempt"}); err == nil {
		t.Fatal("expected create through a symlinked intermediate directory to be rejected")
	}
	assertNothingEscaped(t, outside)
}

// TestWriteFilesAtomic_LegitimateWriteStillSucceeds is the positive control:
// the containment gate must not false-positive on a plain nested layout
// (real directories only). It also covers roots whose absolute path itself
// traverses a symlink (macOS temp dirs resolve /var → /private/var), since
// the root is EvalSymlinks-resolved before the check.
func TestWriteFilesAtomic_LegitimateWriteStillSucceeds(t *testing.T) {
	root, projectDir := setupProjectDir(t)

	if err := UpdateHypothesis(root, projectDir, "H-001", HypothesisUpdate{}); err != nil {
		t.Fatalf("legitimate update rejected: %v", err)
	}
	if _, err := CreateHypothesis(root, projectDir, NewHypothesis{Title: "Legit"}); err != nil {
		t.Fatalf("legitimate create rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "hypotheses", "H-002.md")); err != nil {
		t.Errorf("new card not written in-project: %v", err)
	}
}
