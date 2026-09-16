package papers

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestWritePaperRoundTrip(t *testing.T) {
	rec := ParsePaper(fullPaperMD, fullNoteMD, fullAppraisalMD)
	root := t.TempDir()

	if err := WritePaper(root, rec); err != nil {
		t.Fatalf("WritePaper: %v", err)
	}

	dir := filepath.Join(root, rec.ResolvedSlug())
	for _, name := range PaperArtifacts {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("artifact %s missing: %v", name, err)
		}
	}

	got, err := ParsePaperDir(dir)
	if err != nil {
		t.Fatalf("ParsePaperDir: %v", err)
	}
	want := rec
	want.Dir = dir
	if !reflect.DeepEqual(got, &want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestWritePaperDerivesSlugFromTitle(t *testing.T) {
	rec := PaperRecord{ID: "P-005", Title: "Some Great Paper", Verdict: VerdictAccepted}
	root := t.TempDir()

	if err := WritePaper(root, rec); err != nil {
		t.Fatalf("WritePaper: %v", err)
	}
	paperPath := filepath.Join(root, "some-great-paper", PaperFileName)
	data, err := os.ReadFile(paperPath)
	if err != nil {
		t.Fatalf("expected slug-derived directory: %v", err)
	}
	if !strings.Contains(string(data), "id: P-005") {
		t.Errorf("paper.md does not carry the id:\n%s", data)
	}
}

func TestWritePaperRejectsInvalidRecords(t *testing.T) {
	root := t.TempDir()
	cases := map[string]PaperRecord{
		"traversal slug":  {Slug: "../evil"},
		"hidden slug":     {Slug: ".hidden"},
		"separator slug":  {Slug: "a/b"},
		"backslash slug":  {Slug: `a\b`},
		"nothing to slug": {},
	}
	for name, rec := range cases {
		if err := WritePaper(root, rec); err == nil {
			t.Errorf("%s: WritePaper = nil error, want rejection", name)
		}
	}
}

func TestWritePaperRejectsEmptyLibraryRoot(t *testing.T) {
	if err := WritePaper("", PaperRecord{Title: "X"}); err == nil {
		t.Error("WritePaper with an empty library root = nil error, want rejection")
	}
}

func TestEnsurePaperDirContainsAndCreates(t *testing.T) {
	root := t.TempDir()
	dir, err := ensurePaperDir(root, "my-paper")
	if err != nil {
		t.Fatalf("ensurePaperDir: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("paper directory not created: %v", err)
	}
	if _, err := ensurePaperDir(root, "../escape"); err == nil {
		t.Error("ensurePaperDir accepted an unsafe slug")
	}
}

// TestResolveTargetWithinRoot exercises the writer's containment helper
// directly: a target under a symlinked intermediate directory that escapes the
// root must be rejected (acceptance criterion: containment via pathutil).
func TestResolveTargetWithinRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("inside resolves", func(t *testing.T) {
		sub := filepath.Join(root, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := resolveTargetWithinRoot(root, filepath.Join(sub, "paper.md"))
		if err != nil {
			t.Fatalf("unexpected rejection: %v", err)
		}
		// The returned path is symlink-resolved, so compare against the
		// resolved directory (macOS resolves /var to /private/var).
		resolvedSub, err := filepath.EvalSymlinks(sub)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, resolvedSub) {
			t.Errorf("resolved target = %q, want under %q", got, resolvedSub)
		}
	})

	t.Run("symlink escape rejected", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveTargetWithinRoot(root, filepath.Join(link, "paper.md")); err == nil {
			t.Error("symlink escape was not rejected")
		}
	})

	t.Run("empty root rejected", func(t *testing.T) {
		if _, err := resolveTargetWithinRoot("", filepath.Join(root, "paper.md")); err == nil {
			t.Error("empty root was not rejected")
		}
	})
}

// TestWritePaperContainmentSymlinkEscape verifies the end-to-end write path: a
// paper directory that is a symlink pointing outside the library root is
// rejected before anything is written.
func TestWritePaperContainmentSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	base := t.TempDir()
	libRoot := filepath.Join(base, "papers")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(libRoot, "evil")); err != nil {
		t.Fatal(err)
	}

	if err := WritePaper(libRoot, PaperRecord{Slug: "evil", Title: "Evil"}); err == nil {
		t.Fatal("expected containment rejection for a symlinked paper directory")
	}
	if _, err := os.Stat(filepath.Join(outside, PaperFileName)); err == nil {
		t.Error("artifact escaped the library root")
	}
}

// TestRenderPaperMDCarriesReadingAndVerdict guards the writer side of the two
// axes: the reading decision and the soundness verdict are both rendered.
func TestRenderPaperMDCarriesReadingAndVerdict(t *testing.T) {
	md := RenderPaperMD(PaperRecord{
		Title:   "T",
		Mode:    ModeReview,
		Reading: ReadingSelective,
		Verdict: VerdictAccepted,
	})
	for _, want := range []string{"mode: review", "reading: selective", "verdict: accepted"} {
		if !strings.Contains(md, want) {
			t.Errorf("RenderPaperMD missing %q:\n%s", want, md)
		}
	}
}
