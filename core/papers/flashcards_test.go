package papers

import (
	"reflect"
	"strings"
	"testing"
)

const flashcardDeckMD = `# Flashcards — Attention Is All You Need

## Deck header

| Field | Value |
| --- | --- |
| Source paper(s) / identifier | arXiv:1706.03762 |
| Interval schedule (see §4) | 1 → 3 → 7 → 16 → 35 |

## 1. Card format

- **ID** — stable label (e.g. P1-01).

## 2. Cards

| ID | Front (question) | Back (answer) | Anchor | Tag | Stage |
| --- | --- | --- | --- | --- | --- |
| P1-01 | What replaces recurrence? | Self-attention | §3 | architecture | new |
| P1-02 | How many attention heads? | 8 | §3.2 | hyperparams | learning |
| P1-03 | What is scaled by 1/sqrt(d_k)? | The dot-product logits | Eq 1 | math | review |
| P1-04 | | | | | new |

## 3. Review log

| ID | Date | Grade | Next due |
| --- | --- | --- | --- |
| P1-01 | 2024-01-02 | good | 2024-01-05 |
| P1-02 | 2024-01-02 | Again | 2024-01-03 |
| P1-02 | 2024-01-03 | good | 2024-01-06 |
| | | | |

## 4. Spaced-repetition schedule

- **Intervals**: 1 → 3 → 7 → 16 → 35 days.
`

func TestParseFlashcards(t *testing.T) {
	deck := ParseFlashcards(flashcardDeckMD)
	// Card-row identity is id-based (shared with the writer): every row that
	// carries an id is a card, including the not-yet-filled `| P1-04 | | | | | new |`.
	if len(deck.Cards) != 4 {
		t.Fatalf("cards = %d, want 4 (id-carrying rows are cards)", len(deck.Cards))
	}
	want := Card{ID: "P1-01", Front: "What replaces recurrence?", Back: "Self-attention", Anchor: "§3", Tag: "architecture", Stage: StageNew}
	if !reflect.DeepEqual(deck.Cards[0], want) {
		t.Errorf("card[0] = %+v, want %+v", deck.Cards[0], want)
	}
	if deck.Cards[1].Stage != StageLearning {
		t.Errorf("card[1] stage = %q, want learning", deck.Cards[1].Stage)
	}
	if deck.Cards[2].Stage != StageReview {
		t.Errorf("card[2] stage = %q, want review", deck.Cards[2].Stage)
	}
	if last := deck.Cards[3]; last.ID != "P1-04" || last.Front != "" || last.Stage != StageNew {
		t.Errorf("card[3] = %+v, want the not-yet-filled P1-04", last)
	}
}

func TestParseFlashcardsReviews(t *testing.T) {
	deck := ParseFlashcards(flashcardDeckMD)
	if len(deck.Reviews) != 3 {
		t.Fatalf("reviews = %d, want 3 (empty placeholder skipped)", len(deck.Reviews))
	}
	want := ReviewEntry{ID: "P1-01", Date: "2024-01-02", Grade: GradeGood, NextDue: "2024-01-05"}
	if !reflect.DeepEqual(deck.Reviews[0], want) {
		t.Errorf("review[0] = %+v, want %+v", deck.Reviews[0], want)
	}
	if deck.Reviews[1].Grade != GradeAgain {
		t.Errorf("review[1] grade = %q, want again (case-insensitive)", deck.Reviews[1].Grade)
	}
}

func TestParseFlashcardsEmpty(t *testing.T) {
	deck := ParseFlashcards("# Nothing\n\nJust prose.")
	if len(deck.Cards) != 0 || len(deck.Reviews) != 0 {
		t.Errorf("expected an empty deck, got %+v", deck)
	}
}

func TestRenderFlashcardsRoundTrip(t *testing.T) {
	deck := ParseFlashcards(flashcardDeckMD)
	got := ParseFlashcards(RenderFlashcards(deck))
	if !reflect.DeepEqual(got.Cards, deck.Cards) {
		t.Errorf("cards round-trip mismatch:\n got %+v\nwant %+v", got.Cards, deck.Cards)
	}
	if !reflect.DeepEqual(got.Reviews, deck.Reviews) {
		t.Errorf("reviews round-trip mismatch:\n got %+v\nwant %+v", got.Reviews, deck.Reviews)
	}
}

func TestNormalizeStageAndGrade(t *testing.T) {
	if NormalizeStage("  Review ") != StageReview || NormalizeStage("bogus") != StageNew {
		t.Error("NormalizeStage folding is wrong")
	}
	if NormalizeGrade("Again") != GradeAgain || NormalizeGrade("EASY") != GradeEasy || NormalizeGrade("perfect") != "" {
		t.Error("NormalizeGrade folding is wrong")
	}
	// Markdown emphasis and surrounding punctuation are stripped, so the backend
	// folds the same cell the UI does (mirrors the frontend's norm()).
	if NormalizeStage("**review**") != StageReview || NormalizeStage("`learning`.") != StageLearning {
		t.Error("NormalizeStage must strip Markdown emphasis/punctuation")
	}
	if NormalizeGrade("`good`") != GradeGood || NormalizeGrade("**easy**.") != GradeEasy {
		t.Error("NormalizeGrade must strip Markdown emphasis/punctuation")
	}
	// Stage and grade normalize the same value the same way (issue 47).
	if NormalizeStage(" review ") != NormalizeStage("**review**") {
		t.Error("stage folding is not emphasis-insensitive")
	}
	if NormalizeGrade(" good ") != NormalizeGrade("**good**") {
		t.Error("grade folding is not emphasis-insensitive")
	}
}

// TestParseFlashcardsCardRowIdentity pins the single card-row identity shared by
// the reader and the writer (issue 96): a data row is a card unless the deck has
// an id column and the row's id cell is empty.
func TestParseFlashcardsCardRowIdentity(t *testing.T) {
	deck := ParseFlashcards("| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |\n| | orphan | orphan-b |\n")
	if len(deck.Cards) != 1 || deck.Cards[0].ID != "A1" {
		t.Fatalf("cards = %+v, want only the id-carrying A1 row", deck.Cards)
	}
}

// TestParseFlashcardsNoIDColumn pins the Go/frontend parity for a deck whose
// cards table omits the id column (issue 65): the id column stays absent and the
// card id is empty on both sides — the Go parser must not alias cell 0.
func TestParseFlashcardsNoIDColumn(t *testing.T) {
	deck := ParseFlashcards("| Front | Back |\n| - | - |\n| a question | an answer |\n")
	if len(deck.Cards) != 1 {
		t.Fatalf("cards = %+v, want 1", deck.Cards)
	}
	if deck.Cards[0].ID != "" {
		t.Errorf("card id = %q, want empty (no id column)", deck.Cards[0].ID)
	}
	if deck.Cards[0].Front != "a question" || deck.Cards[0].Back != "an answer" {
		t.Errorf("card = %+v", deck.Cards[0])
	}
}

// TestParseFlashcardsAnchoredColumns pins the word-boundary column resolution
// that mirrors the frontend (issue 105): `Card ID`, `Reference`, and `Tags` are
// not the id/anchor/tag columns, so they must not be claimed.
func TestParseFlashcardsAnchoredColumns(t *testing.T) {
	deck := ParseFlashcards("| Card ID | Front | Back | Reference | Tags |\n| - | - | - | - | - |\n| X9 | q | a | §3 | arch |\n")
	if len(deck.Cards) != 1 {
		t.Fatalf("cards = %+v, want 1", deck.Cards)
	}
	got := deck.Cards[0]
	if got.ID != "" || got.Anchor != "" || got.Tag != "" {
		t.Errorf("card = %+v, want empty id/anchor/tag for a non-canonical header", got)
	}
	if got.Front != "q" || got.Back != "a" {
		t.Errorf("card = %+v, want front/back resolved", got)
	}
}

// TestApplyReviewGradeLessLogRejected pins issue 97: a review log whose header
// carries no grade column cannot record the grade, so the review is rejected
// rather than appending a row that silently loses it (or overwrites `Next due`).
func TestApplyReviewGradeLessLogRejected(t *testing.T) {
	content := "| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |\n\n| ID | Date | Next due |\n| - | - | - |\n"
	if _, err := ApplyReview(content, "A1", GradeGood, "2024-03-01"); err == nil {
		t.Fatal("a grade-less review log must be rejected")
	}
}

// TestApplyReviewPlaceholderRowIsConsistent pins issue 96: a review of an
// id-only (not-yet-filled) card row succeeds AND every reader sees the card, so
// the writer and reader agree on what a card row is.
func TestApplyReviewPlaceholderRowIsConsistent(t *testing.T) {
	out, err := ApplyReview(flashcardDeckMD, "P1-04", GradeGood, "2024-01-10")
	if err != nil {
		t.Fatalf("ApplyReview of a placeholder card: %v", err)
	}
	deck := ParseFlashcards(out)
	found := false
	for _, c := range deck.Cards {
		if c.ID == "P1-04" {
			found = true
		}
	}
	if !found {
		t.Errorf("reader does not see reviewed placeholder card P1-04: %+v", deck.Cards)
	}
}

func TestSchedulerMirror(t *testing.T) {
	if StageForIndex(-1) != StageNew || StageForIndex(0) != StageLearning || StageForIndex(1) != StageReview {
		t.Error("StageForIndex derivation is wrong")
	}
	if IntervalDays(-1) != 1 || IntervalDays(0) != 1 || IntervalDays(1) != 3 || IntervalDays(99) != 35 {
		t.Error("IntervalDays clamping is wrong")
	}
	if AdvanceIndex(3, GradeAgain) != 0 {
		t.Error("again must reset to the first rung")
	}
	if AdvanceIndex(2, GradeHard) != 2 || AdvanceIndex(-1, GradeHard) != 0 {
		t.Error("hard must hold the rung")
	}
	if AdvanceIndex(0, GradeGood) != 1 || AdvanceIndex(4, GradeGood) != 4 || AdvanceIndex(-1, GradeEasy) != 0 {
		t.Error("good/easy promotion is wrong")
	}

	// new → good (rung 0) → again (rung 0) → good (rung 1) → good (rung 2)
	state := StateFromReviews([]ReviewEntry{
		{Grade: GradeGood}, {Grade: GradeAgain}, {Grade: GradeGood}, {Grade: GradeGood},
	})
	if state != (ReviewState{IntervalIndex: 2, Stage: StageReview}) {
		t.Errorf("StateFromReviews = %+v, want rung 2 review", state)
	}

	next, due := ScheduleAfter(StateForIndex(0), GradeGood, "2024-01-02")
	if next != (ReviewState{IntervalIndex: 1, Stage: StageReview}) || due != "2024-01-05" {
		t.Errorf("ScheduleAfter = %+v/%s, want rung 1 review / 2024-01-05", next, due)
	}
	if _, due := ScheduleAfter(StateForIndex(-1), GradeGood, "2024-01-01"); due != "2024-01-02" {
		t.Errorf("first review due = %s, want 2024-01-02", due)
	}
}

func TestApplyReviewAppendsAndUpdatesStage(t *testing.T) {
	out, err := ApplyReview(flashcardDeckMD, "P1-01", GradeGood, "2024-01-10")
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	deck := ParseFlashcards(out)
	if len(deck.Reviews) != 4 {
		t.Fatalf("reviews = %d, want 4", len(deck.Reviews))
	}
	last := deck.Reviews[len(deck.Reviews)-1]
	want := ReviewEntry{ID: "P1-01", Date: "2024-01-10", Grade: GradeGood, NextDue: "2024-01-13"}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("appended review = %+v, want %+v", last, want)
	}
	if deck.Cards[0].ID != "P1-01" || deck.Cards[0].Stage != StageReview {
		t.Errorf("card stage not updated: %+v", deck.Cards[0])
	}
	// The skill's prose survives an in-place edit.
	if !strings.Contains(out, "## 4. Spaced-repetition schedule") {
		t.Error("prose sections were dropped by ApplyReview")
	}
}

func TestApplyReviewWithoutStageColumn(t *testing.T) {
	content := "| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |\n\n| ID | Date | Grade | Next due |\n| - | - | - | - |\n"
	out, err := ApplyReview(content, "A1", GradeEasy, "2024-03-01")
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	deck := ParseFlashcards(out)
	if len(deck.Reviews) != 1 || deck.Reviews[0].Grade != GradeEasy {
		t.Errorf("review not appended: %+v", deck.Reviews)
	}
	if deck.Cards[0].Stage != StageNew {
		t.Errorf("card without a Stage column must keep its default stage, got %q", deck.Cards[0].Stage)
	}
}

func TestApplyReviewRejections(t *testing.T) {
	if _, err := ApplyReview(flashcardDeckMD, "NOPE", GradeGood, "2024-01-10"); err == nil {
		t.Error("unknown card id was not rejected")
	}
	if _, err := ApplyReview(flashcardDeckMD, "P1-01", Grade("bogus"), "2024-01-10"); err == nil {
		t.Error("unknown grade was not rejected")
	}
	if _, err := ApplyReview(flashcardDeckMD, "", GradeGood, "2024-01-10"); err == nil {
		t.Error("empty card id was not rejected")
	}
	// A deck with no review-log table cannot record a grade.
	if _, err := ApplyReview("| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |", "A1", GradeGood, "2024-01-10"); err == nil {
		t.Error("a deck with no review log was not rejected")
	}
}

// TestApplyReviewPartialReviewLogHeader pins the collision-safe positional
// fallback (review finding 5): a hand-edited review log whose header omits a
// canonical column must never write a field into another field's column. The
// grade lands in the Grade column; a field with no column at all (no Date
// column) is omitted rather than written into the wrong cell — so a replay of
// the log never reads a date as a grade or loses the recorded grade.
func TestApplyReviewPartialReviewLogHeader(t *testing.T) {
	cases := []struct {
		name      string
		logHeader string
		wantRow   string
	}{
		{
			name:      "id and grade only",
			logHeader: "| ID | Grade |\n| - | - |",
			// No Date column: the date is omitted, not written under Grade.
			wantRow: "| A1 | good |",
		},
		{
			name:      "id, grade, and next due",
			logHeader: "| ID | Grade | Next due |\n| - | - | - |",
			// No Date column: grade and next-due land in their named columns.
			wantRow: "| A1 | good | 2024-03-02 |",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |\n\n" + tc.logHeader + "\n"
			out, err := ApplyReview(content, "A1", GradeGood, "2024-03-01")
			if err != nil {
				t.Fatalf("ApplyReview: %v", err)
			}
			if !strings.Contains(out, tc.wantRow) {
				t.Errorf("appended review row:\n got %q\nwant it to contain %q\nfull document:\n%s", out, tc.wantRow, out)
			}
			// The appended row must replay as the graded review (never a
			// grade-less row or a date folded into the grade).
			deck := ParseFlashcards(out)
			if len(deck.Reviews) != 1 || deck.Reviews[0].Grade != GradeGood {
				t.Errorf("reviews after append = %+v, want exactly one entry graded good", deck.Reviews)
			}
		})
	}
}

// TestApplyReviewPartialLogOmitsIDColumn pins the same fallback collision for
// a log that omits the ID column (review finding 5): the card id must not be
// written into the first physical column when that column names another
// field — every field that resolves by name lands in its own column.
func TestApplyReviewPartialLogOmitsIDColumn(t *testing.T) {
	content := "| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |\n\n| Date | Grade | Next due |\n| - | - | - |\n"
	out, err := ApplyReview(content, "A1", GradeGood, "2024-03-01")
	if err != nil {
		t.Fatalf("ApplyReview: %v", err)
	}
	// No id column: the id is omitted; date, grade, and next-due keep their
	// own named columns (a date masquerading as a grade would break replay).
	want := "| 2024-03-01 | good | 2024-03-02 |"
	if !strings.Contains(out, want) {
		t.Errorf("appended review row:\n got %q\nwant it to contain %q\nfull document:\n%s", out, want, out)
	}
	deck := ParseFlashcards(out)
	if len(deck.Reviews) != 1 || deck.Reviews[0].Grade != GradeGood || deck.Reviews[0].Date != "2024-03-01" {
		t.Errorf("reviews after append = %+v, want one entry dated 2024-03-01 graded good", deck.Reviews)
	}
}

// TestParseFlashcardsLooseColumnHeaders pins the frontend parity for
// substring column matching (review finding 36): the frontend twin's
// unanchored /front|question|prompt/ and /back|answer/ accept `Questions` and
// `Answers` headers, so the Go parser must too — the two parsers read the
// same flashcards.md.
func TestParseFlashcardsLooseColumnHeaders(t *testing.T) {
	deck := ParseFlashcards("| Questions | Answers |\n| - | - |\n| What replaces recurrence? | Self-attention |\n")
	if len(deck.Cards) != 1 {
		t.Fatalf("cards = %+v, want 1 (Questions/Answers is a cards header)", deck.Cards)
	}
	if deck.Cards[0].Front != "What replaces recurrence?" || deck.Cards[0].Back != "Self-attention" {
		t.Errorf("card = %+v, want front/back resolved from the Questions/Answers columns", deck.Cards[0])
	}
}

// TestIsCardsReviewHeaderLooseMatch drives the deck/review predicates with
// the header variants the frontend twin accepts by substring (review finding
// 36) plus the anchored ones it rejects, so the two matchers cannot drift.
func TestIsCardsReviewHeaderLooseMatch(t *testing.T) {
	cases := []struct {
		header []string
		cards  bool
		review bool
	}{
		{[]string{"ID", "Front", "Back"}, true, false},
		{[]string{"Questions", "Answers"}, true, false},
		{[]string{"Prompt", "Answer", "Tag"}, true, false},
		{[]string{"ID", "Date", "Grade", "Next due"}, false, true},
		{[]string{"Grades", "Dates"}, false, true},
		{[]string{"Rating", "When"}, false, true},
		{[]string{"Results", "Due"}, false, true},
		{[]string{"Field", "Value"}, false, false},
		// Anchored tokens still reject embedded forms, mirroring the
		// frontend's `\b…\b` alternatives.
		{[]string{"Tags", "Reference"}, false, false},
		{[]string{"Card ID", "Front", "Back"}, true, false}, // id stays start-anchored; front/back loose
	}
	for _, tc := range cases {
		if got := isCardsHeader(tc.header); got != tc.cards {
			t.Errorf("isCardsHeader(%v) = %v, want %v", tc.header, got, tc.cards)
		}
		if got := isReviewHeader(tc.header); got != tc.review {
			t.Errorf("isReviewHeader(%v) = %v, want %v", tc.header, got, tc.review)
		}
	}
}

// TestApplyReviewLooseHeadersEndToEnd drives the review writer against the
// deck the UI renders with loose headers (review finding 36): a card table
// headed `| ID | Questions | Answers |` and a review log headed
// `| Grades | Dates |` must be recognized, and the appended row must land in
// the named columns.
func TestApplyReviewLooseHeadersEndToEnd(t *testing.T) {
	content := "| ID | Questions | Answers |\n| - | - | - |\n| Q1 | q | a |\n\n| Grades | Dates |\n| - | - |\n"
	out, err := ApplyReview(content, "Q1", GradeGood, "2024-03-01")
	if err != nil {
		t.Fatalf("ApplyReview rejected a deck the UI accepts: %v", err)
	}
	// The log has no id/next-due column; grade and date resolve by name.
	want := "| good | 2024-03-01 |"
	if !strings.Contains(out, want) {
		t.Errorf("appended review row:\n got %q\nwant it to contain %q\nfull document:\n%s", out, want, out)
	}
	deck := ParseFlashcards(out)
	if len(deck.Reviews) != 1 || deck.Reviews[0].Grade != GradeGood || deck.Reviews[0].Date != "2024-03-01" {
		t.Errorf("reviews after append = %+v, want one entry dated 2024-03-01 graded good", deck.Reviews)
	}
}
