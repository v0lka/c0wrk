package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
)

const flashcardsDeckMD = `# Flashcards — Deck Paper

## 2. Cards

| ID | Front (question) | Back (answer) | Anchor | Tag | Stage |
| --- | --- | --- | --- | --- | --- |
| P1-01 | Q1 | A1 | §3 | t | new |
| P1-02 | Q2 | A2 | §4 | t | learning |

## 3. Review log

| ID | Date | Grade | Next due |
| --- | --- | --- | --- |
| P1-02 | 2024-01-02 | again | 2024-01-03 |
| P1-02 | 2024-01-03 | good | 2024-01-06 |
`

// seedDeckPaper writes a paper card and a flashcards.md deck into
// libraryRoot/<slug>, so the library parses the paper and the deck exists.
func seedDeckPaper(t *testing.T, libraryRoot, slug string) string {
	t.Helper()
	dir := filepath.Join(libraryRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir paper dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, papers.PaperFileName), []byte("---\nid: P-001\ntitle: Deck Paper\n---\n"), 0o644); err != nil {
		t.Fatalf("seed paper.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, papers.FlashcardFileName), []byte(flashcardsDeckMD), 0o644); err != nil {
		t.Fatalf("seed flashcards.md: %v", err)
	}
	return dir
}

func readDeck(t *testing.T, dir string) papers.Deck {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, papers.FlashcardFileName))
	if err != nil {
		t.Fatalf("read deck: %v", err)
	}
	return papers.ParseFlashcards(string(raw))
}

// TestRecordFlashcardReview_AppendsAndUpdates pins the happy path: a grade is
// appended to the deck's review log and the card's Stage advances.
func TestRecordFlashcardReview_AppendsAndUpdates(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedDeckPaper(t, libraryRoot, "deck")

	before := readDeck(t, dir)
	if err := api.RecordFlashcardReview(projectID, "P-001", "P1-02", "good"); err != nil {
		t.Fatalf("RecordFlashcardReview: %v", err)
	}

	after := readDeck(t, dir)
	if len(after.Reviews) != len(before.Reviews)+1 {
		t.Fatalf("reviews = %d, want %d", len(after.Reviews), len(before.Reviews)+1)
	}
	last := after.Reviews[len(after.Reviews)-1]
	if last.ID != "P1-02" || last.Grade != papers.GradeGood {
		t.Errorf("appended review = %+v, want P1-02/good", last)
	}
	if last.NextDue == "" || last.Date == "" {
		t.Errorf("appended review missing date/next due: %+v", last)
	}
	// P1-02 was at rung 1; `good` graduates it to `review`.
	if after.Cards[1].Stage != papers.StageReview {
		t.Errorf("P1-02 stage = %q, want review", after.Cards[1].Stage)
	}
}

// TestRecordFlashcardReview_RejectionsLeaveDeckUnchanged pins the fail-closed
// surface: an unknown paper/card, a bad grade, or a missing identifier is
// rejected, and a rejected review never touches the deck.
func TestRecordFlashcardReview_RejectionsLeaveDeckUnchanged(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedDeckPaper(t, libraryRoot, "deck")

	cases := []struct {
		name    string
		paperID string
		cardID  string
		grade   string
	}{
		{"unknown paper", "nope", "P1-01", "good"},
		{"unknown card", "P-001", "MISSING", "good"},
		{"bad grade", "P-001", "P1-01", "perfect"},
		{"empty card", "P-001", "", "good"},
		{"empty paper", "", "P1-01", "good"},
	}
	for _, tc := range cases {
		if err := api.RecordFlashcardReview(projectID, tc.paperID, tc.cardID, tc.grade); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, papers.FlashcardFileName))
	if err != nil {
		t.Fatalf("read deck: %v", err)
	}
	if string(raw) != flashcardsDeckMD {
		t.Error("a rejected review must leave the deck byte-for-byte unchanged")
	}
}

// TestRecordFlashcardReview_RequiresProject rejects a missing/unknown project.
func TestRecordFlashcardReview_RequiresProject(t *testing.T) {
	api, _, _, _ := papersTestFrontend(t, "", project.ResearchPins{})
	if err := api.RecordFlashcardReview("", "P-001", "P1-01", "good"); err == nil {
		t.Error("an empty project id should be rejected")
	}
	if err := api.RecordFlashcardReview("does-not-exist", "P-001", "P1-01", "good"); err == nil {
		t.Error("an unknown project id should be rejected")
	}
}
