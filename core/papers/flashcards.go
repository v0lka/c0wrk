// Package papers — flashcards model, parser, renderer and scheduler.
//
// The study-paper skill writes a deck as `flashcards.md` (see
// skills/study-paper/assets/flashcards.md): a plain Markdown document with a
// card table (| ID | Front | Back | Anchor | Tag | Stage |) and an append-only
// review log (| ID | Date | Grade | Next due |). This file holds the pure
// logic over that text — parse, render, and the fixed interval scheduler — with
// no I/O. The writer layer (writer.go) owns the atomic, containment-checked
// persistence; the backend exposes a mutation RPC on top of it.
//
// Everything here mirrors the frontend's lib/flashcards.ts and
// lib/spacedRepetition.ts so the review a reader sees and the review the
// backend records agree.

package papers

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// FlashcardFileName is the deck artifact's file name (inside a paper's dir).
const FlashcardFileName = "flashcards.md"

// FlashcardStage is a card's lifecycle stage.
type FlashcardStage string

const (
	// StageNew: never reviewed.
	StageNew FlashcardStage = "new"
	// StageLearning: reviewed but not yet past the first interval.
	StageLearning FlashcardStage = "learning"
	// StageReview: promoted past the first interval.
	StageReview FlashcardStage = "review"
)

// Grade is a self-graded review outcome.
type Grade string

const (
	// GradeAgain: missed — reset to the first interval.
	GradeAgain Grade = "again"
	// GradeHard: repeat at the same interval.
	GradeHard Grade = "hard"
	// GradeGood: promote to the next interval.
	GradeGood Grade = "good"
	// GradeEasy: promote to the next interval.
	GradeEasy Grade = "easy"
)

// Card is one atomic question/answer card.
type Card struct {
	ID     string         `json:"id"`
	Front  string         `json:"front"`
	Back   string         `json:"back"`
	Anchor string         `json:"anchor"`
	Tag    string         `json:"tag"`
	Stage  FlashcardStage `json:"stage"`
}

// ReviewEntry is one row of the review log.
type ReviewEntry struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Grade   Grade  `json:"grade,omitempty"`
	NextDue string `json:"next_due,omitempty"`
}

// Deck is a parsed deck: the ordered cards plus the append-only review log.
type Deck struct {
	Cards   []Card        `json:"cards"`
	Reviews []ReviewEntry `json:"reviews"`
}

// NormalizeStage folds a raw stage token to its canonical value; an unknown or
// blank token maps to StageNew.
func NormalizeStage(raw string) FlashcardStage {
	switch strings.ToLower(cleanLine(raw)) {
	case "learning", "learn":
		return StageLearning
	case "review", "reviewing", "graduated":
		return StageReview
	default:
		return StageNew
	}
}

// NormalizeGrade folds a raw grade token to its canonical value; an unknown or
// blank token maps to "" (an ungraded review row).
func NormalizeGrade(raw string) Grade {
	switch strings.ToLower(cleanLine(raw)) {
	case "again":
		return GradeAgain
	case "hard":
		return GradeHard
	case "good":
		return GradeGood
	case "easy":
		return GradeEasy
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Column detection (shared with the frontend parser)
// ---------------------------------------------------------------------------

func headerMatches(header []string, names ...string) bool {
	return columnIndex(header, names...) >= 0
}

// isCardsHeader reports whether a table header is the deck's card table.
func isCardsHeader(header []string) bool {
	return headerMatches(header, "front", "question", "prompt") &&
		headerMatches(header, "back", "answer")
}

// isReviewHeader reports whether a table header is the review log.
func isReviewHeader(header []string) bool {
	return headerMatches(header, "grade", "rating", "result") || headerMatches(header, "next", "due")
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// ParseFlashcards parses a flashcards.md document into its deck. Best-effort
// and total: a document with no recognizable table yields an empty deck, and a
// row carrying neither a prompt nor an answer (the template's placeholder) is
// skipped.
func ParseFlashcards(content string) Deck {
	var deck Deck
	for _, t := range parseTables(content) {
		if len(deck.Cards) == 0 && isCardsHeader(t.Header) {
			deck.Cards = parseCards(t)
			continue
		}
		if len(deck.Reviews) == 0 && isReviewHeader(t.Header) {
			deck.Reviews = parseReviews(t)
		}
	}
	return deck
}

func parseCards(t mdTable) []Card {
	idIdx := colOr(t.Header, 0, "id")
	frontIdx := colOr(t.Header, 0, "front", "question", "prompt")
	backIdx := colOr(t.Header, 1, "back", "answer")
	anchorIdx := columnIndexExcept(t.Header, []int{idIdx, frontIdx, backIdx}, "anchor", "source", "location", "ref", "where", "page")
	// Resolve Stage before Tag: "stage" contains the substring "tag", so a
	// tag-first pick would misclaim a Tag-less deck's Stage column.
	stageIdx := columnIndexExcept(t.Header, []int{idIdx, frontIdx, backIdx, anchorIdx}, "stage", "state", "status")
	tagIdx := columnIndexExcept(t.Header, []int{idIdx, frontIdx, backIdx, anchorIdx, stageIdx}, "tag", "topic", "concept", "label")
	var out []Card
	for _, row := range t.Rows {
		front := cellAt(row, frontIdx)
		back := cellAt(row, backIdx)
		if front == "" && back == "" {
			continue
		}
		out = append(out, Card{
			ID:     cellAt(row, idIdx),
			Front:  front,
			Back:   back,
			Anchor: cellAt(row, anchorIdx),
			Tag:    cellAt(row, tagIdx),
			Stage:  NormalizeStage(cellAt(row, stageIdx)),
		})
	}
	return out
}

func parseReviews(t mdTable) []ReviewEntry {
	idIdx := colOr(t.Header, 0, "id")
	dateIdx := columnIndexExcept(t.Header, []int{idIdx}, "date", "when")
	gradeIdx := columnIndexExcept(t.Header, []int{idIdx, dateIdx}, "grade", "rating", "result")
	dueIdx := columnIndexExcept(t.Header, []int{idIdx, dateIdx, gradeIdx}, "next", "due")
	var out []ReviewEntry
	for _, row := range t.Rows {
		id := cellAt(row, idIdx)
		date := cellAt(row, dateIdx)
		grade := cellAt(row, gradeIdx)
		due := cellAt(row, dueIdx)
		if id == "" && date == "" && grade == "" && due == "" {
			continue
		}
		out = append(out, ReviewEntry{ID: id, Date: date, Grade: NormalizeGrade(grade), NextDue: due})
	}
	return out
}

// columnIndexExcept is columnIndex over the header cells whose index is not in
// exclude (used so a later pick never re-selects an earlier column).
func columnIndexExcept(header []string, exclude []int, names ...string) int {
	for i, h := range header {
		if containsInt(exclude, i) {
			continue
		}
		hn := strings.ToLower(strings.Trim(h, "*_` "))
		for _, n := range names {
			if strings.Contains(hn, n) {
				return i
			}
		}
	}
	return -1
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// RenderFlashcards renders a deck to a canonical flashcards.md document. It is
// the inverse of ParseFlashcards for the two data tables (the skill's prose
// sections are not reproduced — the writer's mutation PATH instead edits the
// existing document in place; see ApplyReview).
func RenderFlashcards(deck Deck) string {
	var b strings.Builder
	b.WriteString("# Flashcards\n\n")
	b.WriteString("## Cards\n\n")
	b.WriteString(joinCells([]string{"ID", "Front", "Back", "Anchor", "Tag", "Stage"}) + "\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, c := range deck.Cards {
		b.WriteString(joinCells([]string{c.ID, c.Front, c.Back, c.Anchor, c.Tag, string(c.Stage)}) + "\n")
	}
	b.WriteString("\n## Review log\n\n")
	b.WriteString(joinCells([]string{"ID", "Date", "Grade", "Next due"}) + "\n")
	b.WriteString("|---|---|---|---|\n")
	for _, r := range deck.Reviews {
		b.WriteString(joinCells([]string{r.ID, r.Date, string(r.Grade), r.NextDue}) + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Scheduling (mirrors frontend lib/spacedRepetition.ts)
// ---------------------------------------------------------------------------

// IntervalScheduleDays is the skill's default interval ladder, in days.
var IntervalScheduleDays = []int{1, 3, 7, 16, 35}

// NewIntervalIndex is a never-reviewed card (not yet scheduled).
const NewIntervalIndex = -1

// ReviewState is a card's scheduling state: its progress rung plus the derived
// stage.
type ReviewState struct {
	IntervalIndex int            `json:"interval_index"`
	Stage         FlashcardStage `json:"stage"`
}

func clampIndex(index int) int {
	if index < NewIntervalIndex {
		return NewIntervalIndex
	}
	if index > len(IntervalScheduleDays)-1 {
		return len(IntervalScheduleDays) - 1
	}
	return index
}

// StageForIndex derives the stage implied by an interval index.
func StageForIndex(index int) FlashcardStage {
	i := clampIndex(index)
	switch {
	case i < 0:
		return StageNew
	case i == 0:
		return StageLearning
	default:
		return StageReview
	}
}

// StateForIndex builds a review state from an interval index.
func StateForIndex(index int) ReviewState {
	i := clampIndex(index)
	return ReviewState{IntervalIndex: i, Stage: StageForIndex(i)}
}

// IntervalDays returns the interval length (days) for an index, clamped
// (a never-reviewed card maps to the first rung).
func IntervalDays(index int) int {
	i := clampIndex(index)
	if i < 0 {
		i = 0
	}
	return IntervalScheduleDays[i]
}

// AdvanceIndex applies a grade to the current rung: `again` resets to the first
// rung, `hard` holds, `good`/`easy` advance one (capped). A never-reviewed card
// lands on the first rung for every grade.
func AdvanceIndex(currentIndex int, grade Grade) int {
	current := clampIndex(currentIndex)
	switch grade {
	case GradeAgain:
		return 0
	case GradeHard:
		if current < 0 {
			return 0
		}
		return current
	case GradeGood, GradeEasy:
		if current < 0 {
			return 0
		}
		if current+1 > len(IntervalScheduleDays)-1 {
			return len(IntervalScheduleDays) - 1
		}
		return current + 1
	default:
		return current
	}
}

// StateFromReviews replays a card's review log to recover its current state.
func StateFromReviews(entries []ReviewEntry) ReviewState {
	index := NewIntervalIndex
	for _, e := range entries {
		if e.Grade == "" {
			continue
		}
		index = AdvanceIndex(index, e.Grade)
	}
	return StateForIndex(index)
}

// ScheduleAfter grades a card at state on date, returning the resulting state
// and the computed next due date (YYYY-MM-DD).
func ScheduleAfter(state ReviewState, grade Grade, date string) (next ReviewState, nextDue string) {
	index := AdvanceIndex(state.IntervalIndex, grade)
	return StateForIndex(index), addDays(date, IntervalDays(index))
}

// addDays adds days to a YYYY-MM-DD date; an unparseable date is returned
// unchanged.
func addDays(date string, days int) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

// ---------------------------------------------------------------------------
// Surgical in-place update (pure)
// ---------------------------------------------------------------------------

// tableBlock is a contiguous run of Markdown table lines, half-open [start, end).
type tableBlock struct{ start, end int }

func tableBlocks(lines []string) []tableBlock {
	var blocks []tableBlock
	for i := 0; i < len(lines); {
		if !isTableRow(cleanLine(lines[i])) {
			i++
			continue
		}
		start := i
		for i < len(lines) && isTableRow(cleanLine(lines[i])) {
			i++
		}
		if _, ok := buildTable(lines[start:i]); ok {
			blocks = append(blocks, tableBlock{start: start, end: i})
		}
	}
	return blocks
}

// ApplyReview edits a flashcards.md document in place: it appends a review-log
// row for cardID graded on date and updates that card's Stage cell, leaving all
// other content (including the skill's prose) byte-for-byte intact. It fails
// closed when the deck has no card row for cardID or no review-log table.
func ApplyReview(content, cardID string, grade Grade, date string) (string, error) {
	if strings.TrimSpace(cardID) == "" {
		return "", errors.New("flashcard review rejected: empty card id")
	}
	if NormalizeGrade(string(grade)) == "" {
		return "", fmt.Errorf("flashcard review rejected: unknown grade %q", grade)
	}
	// Canonicalize the grade so the appended row is always well-formed.
	grade = NormalizeGrade(string(grade))

	lines := strings.Split(content, "\n")
	var cardsBlock, reviewBlock *tableBlock
	for _, b := range tableBlocks(lines) {
		header := splitCells(cleanLine(lines[b.start]))
		if cardsBlock == nil && isCardsHeader(header) {
			bb := b
			cardsBlock = &bb
			continue
		}
		if reviewBlock == nil && isReviewHeader(header) {
			bb := b
			reviewBlock = &bb
		}
	}
	if cardsBlock == nil {
		return "", errors.New("flashcard review rejected: no card table found")
	}
	if reviewBlock == nil {
		return "", errors.New("flashcard review rejected: no review-log table found")
	}

	// Locate the card row and its stage column.
	header := splitCells(cleanLine(lines[cardsBlock.start]))
	idIdx := colOr(header, 0, "id")
	stageIdx := columnIndex(header, "stage", "state", "status")
	cardRow := -1
	for i := cardsBlock.start + 2; i < cardsBlock.end; i++ {
		if cellAt(splitCells(cleanLine(lines[i])), idIdx) == cardID {
			cardRow = i
			break
		}
	}
	if cardRow < 0 {
		return "", fmt.Errorf("flashcard review rejected: card %q not found in the deck", cardID)
	}

	// Replay the card's log to schedule the new review.
	entries := reviewsForCardID(ParseFlashcards(content).Reviews, cardID)
	next, nextDue := ScheduleAfter(StateFromReviews(entries), grade, date)

	// Build the updated card row (only when the table carries a stage column).
	newCardLine := lines[cardRow]
	if stageIdx >= 0 {
		cells := splitCells(cleanLine(lines[cardRow]))
		for len(cells) <= stageIdx {
			cells = append(cells, "")
		}
		cells[stageIdx] = string(next.Stage)
		newCardLine = joinCells(cells)
	}

	// Build the review row against the log's own header width.
	rHeader := splitCells(cleanLine(lines[reviewBlock.start]))
	row := make([]string, len(rHeader))
	set := func(names []string, fallback int, value string) {
		idx := colOr(rHeader, fallback, names...)
		if idx >= 0 && idx < len(row) {
			row[idx] = value
		}
	}
	set([]string{"id"}, 0, cardID)
	set([]string{"date", "when"}, 1, date)
	set([]string{"grade", "rating", "result"}, 2, string(grade))
	set([]string{"next", "due"}, 3, nextDue)
	newReviewLine := joinCells(row)

	out := make([]string, 0, len(lines)+1)
	for i, line := range lines {
		if i == reviewBlock.end {
			out = append(out, newReviewLine)
		}
		if i == cardRow {
			out = append(out, newCardLine)
			continue
		}
		out = append(out, line)
	}
	if reviewBlock.end >= len(lines) {
		out = append(out, newReviewLine)
	}
	return strings.Join(out, "\n"), nil
}

func reviewsForCardID(all []ReviewEntry, cardID string) []ReviewEntry {
	var out []ReviewEntry
	for _, e := range all {
		if e.ID == cardID {
			out = append(out, e)
		}
	}
	return out
}
