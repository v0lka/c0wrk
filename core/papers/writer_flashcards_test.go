package papers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// seedDeck writes the fixture deck into libraryRoot/<slug>/flashcards.md.
func seedDeck(t *testing.T, libraryRoot, slug string) string {
	t.Helper()
	dir := filepath.Join(libraryRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir paper dir: %v", err)
	}
	path := filepath.Join(dir, FlashcardFileName)
	if err := os.WriteFile(path, []byte(flashcardDeckMD), 0o644); err != nil {
		t.Fatalf("seed deck: %v", err)
	}
	return path
}

func TestRecordCardReviewAppendsAndUpdates(t *testing.T) {
	root := t.TempDir()
	path := seedDeck(t, root, "demo")

	if err := RecordCardReview(root, "demo", "P1-02", GradeGood, "2024-01-10"); err != nil {
		t.Fatalf("RecordCardReview: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read deck: %v", err)
	}
	deck := ParseFlashcards(string(raw))
	// P1-02 was at rung 1 (its log is again→good); a `good` promotes to rung 2
	// (7 days), so the next due is 2024-01-17 and the stage graduates to review.
	last := deck.Reviews[len(deck.Reviews)-1]
	want := ReviewEntry{ID: "P1-02", Date: "2024-01-10", Grade: GradeGood, NextDue: "2024-01-17"}
	if last != want {
		t.Errorf("appended review = %+v, want %+v", last, want)
	}
	if deck.Cards[1].Stage != StageReview {
		t.Errorf("P1-02 stage = %q, want review", deck.Cards[1].Stage)
	}
}

func TestRecordCardReviewInvalidLeavesFileUnchanged(t *testing.T) {
	root := t.TempDir()
	path := seedDeck(t, root, "demo")

	if err := RecordCardReview(root, "demo", "MISSING", GradeGood, "2024-01-10"); err == nil {
		t.Fatal("expected an unknown card id to be rejected")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read deck: %v", err)
	}
	if string(raw) != flashcardDeckMD {
		t.Error("a rejected review must leave the deck byte-for-byte unchanged")
	}
}

func TestRecordCardReviewRejectsMissingDeck(t *testing.T) {
	root := t.TempDir()
	if err := RecordCardReview(root, "no-such-paper", "P1-01", GradeGood, "2024-01-10"); err == nil {
		t.Error("expected a missing deck to be rejected")
	}
}

func TestRecordCardReviewRejectsUnsafeSlug(t *testing.T) {
	root := t.TempDir()
	if err := RecordCardReview(root, "../evil", "P1-01", GradeGood, "2024-01-10"); err == nil {
		t.Error("expected an unsafe slug to be rejected")
	}
}

func TestWriteFlashcardsRoundTrip(t *testing.T) {
	root := t.TempDir()
	deck := ParseFlashcards(flashcardDeckMD)
	if err := WriteFlashcards(root, "demo", deck); err != nil {
		t.Fatalf("WriteFlashcards: %v", err)
	}
	got, err := ParsePaperDir(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("ParsePaperDir: %v", err)
	}
	_ = got // the card artifact is optional; the deck is asserted directly below.
	raw, err := os.ReadFile(filepath.Join(root, "demo", FlashcardFileName))
	if err != nil {
		t.Fatalf("read deck: %v", err)
	}
	round := ParseFlashcards(string(raw))
	if len(round.Cards) != len(deck.Cards) || len(round.Reviews) != len(deck.Reviews) {
		t.Errorf("round-trip lost rows: cards %d/%d reviews %d/%d", len(round.Cards), len(deck.Cards), len(round.Reviews), len(deck.Reviews))
	}
}

// TestRecordCardReviewContainmentSymlinkEscape verifies the end-to-end write
// path: a paper directory that is a symlink pointing outside the library root
// is rejected before anything is written, and the outside target is untouched.
func TestRecordCardReviewContainmentSymlinkEscape(t *testing.T) {
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
	// The real deck lives outside; the in-library name is a symlink to it.
	if err := os.WriteFile(filepath.Join(outside, FlashcardFileName), []byte(flashcardDeckMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(libRoot, "evil")); err != nil {
		t.Fatal(err)
	}

	err := RecordCardReview(libRoot, "evil", "P1-01", GradeGood, "2024-01-10")
	if err == nil {
		t.Fatal("expected a containment rejection for a symlinked paper directory")
	}
	if !strings.Contains(err.Error(), "outside the library root") {
		t.Errorf("expected a containment rejection, got: %v", err)
	}

	raw, readErr := os.ReadFile(filepath.Join(outside, FlashcardFileName))
	if readErr != nil {
		t.Fatalf("read outside deck: %v", readErr)
	}
	if string(raw) != flashcardDeckMD {
		t.Error("the outside deck was modified through the symlink")
	}
	entries, dirErr := os.ReadDir(outside)
	if dirErr != nil {
		t.Fatalf("read outside dir: %v", dirErr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".paper-") {
			t.Errorf("temp file staged outside the library root: %s", e.Name())
		}
	}
}
