package papers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const fullPaperMD = `---
id: P-001
slug: attention-is-all-you-need
title: "Attention Is All You Need"
authors:
  - Ashish Vaswani
  - Noam Shazeer
year: 2017
venue: NeurIPS
identifiers:
  - scheme: doi
    value: 10.5555/3295222.3295349
  - scheme: arxiv
    value: "1706.03762"
mode: deep
reading: selective
verdict: accepted
confidence: high
research_ids:
  - H-001
  - h2
anchors:
  - label: sec3
    ref: "Section 3.2"
    note: scaled dot-product attention
---

# Attention Is All You Need

Body text.
`

const fullNoteMD = `# Note

## Claims

| Claim | Evidence | Location |
|---|---|---|
| Self-attention needs no recurrence | BLEU 28.4 vs 26.4 baseline | Table 2 |
| Training parallelizes over sequence | 3.5 days on 8 GPUs | Section 5 |

## Red Flags

| Flag | Detail | Severity |
|---|---|---|
| Single seed | Only one training run reported | high |

## Uncertainty

| Item | Detail |
|---|---|
| Long-sequence cost | O(n^2) attention untested beyond 512 tokens |
`

const fullAppraisalMD = `# Appraisal

| Field | Value |
|---|---|
| Verdict | accepted |
| Confidence | high |
`

func TestParsePaperFull(t *testing.T) {
	rec := ParsePaper(fullPaperMD, fullNoteMD, fullAppraisalMD)

	if rec.ID != "P-001" {
		t.Errorf("ID = %q, want P-001", rec.ID)
	}
	if rec.Slug != "attention-is-all-you-need" {
		t.Errorf("Slug = %q", rec.Slug)
	}
	if rec.Title != "Attention Is All You Need" {
		t.Errorf("Title = %q", rec.Title)
	}
	if want := []string{"Ashish Vaswani", "Noam Shazeer"}; !reflect.DeepEqual(rec.Authors, want) {
		t.Errorf("Authors = %v, want %v", rec.Authors, want)
	}
	if rec.Year != 2017 {
		t.Errorf("Year = %d, want 2017", rec.Year)
	}
	if rec.Venue != "NeurIPS" {
		t.Errorf("Venue = %q", rec.Venue)
	}
	if rec.Mode != ModeDeep {
		t.Errorf("Mode = %q", rec.Mode)
	}
	if rec.Reading != ReadingSelective {
		t.Errorf("Reading = %q, want selective", rec.Reading)
	}
	if rec.Verdict != VerdictAccepted {
		t.Errorf("Verdict = %q", rec.Verdict)
	}
	if rec.Confidence != ConfidenceHigh {
		t.Errorf("Confidence = %q", rec.Confidence)
	}
	if want := []string{"H-001", "H-002"}; !reflect.DeepEqual(rec.ResearchIDs, want) {
		t.Errorf("ResearchIDs = %v, want %v (h2 must normalize to H-002)", rec.ResearchIDs, want)
	}
	if len(rec.Identifiers) != 2 {
		t.Fatalf("Identifiers = %v, want 2", rec.Identifiers)
	}
	if rec.Identifiers[0].Scheme != "doi" || rec.Identifiers[0].Value != "10.5555/3295222.3295349" {
		t.Errorf("Identifiers[0] = %+v", rec.Identifiers[0])
	}
	if rec.Identifiers[1].Scheme != "arxiv" || rec.Identifiers[1].Value != "1706.03762" {
		t.Errorf("Identifiers[1] = %+v", rec.Identifiers[1])
	}
	if len(rec.Anchors) != 1 || rec.Anchors[0].Label != "sec3" || rec.Anchors[0].Ref != "Section 3.2" {
		t.Errorf("Anchors = %+v", rec.Anchors)
	}

	if len(rec.Claims) != 2 {
		t.Fatalf("Claims = %+v, want 2", rec.Claims)
	}
	if rec.Claims[0].Claim != "Self-attention needs no recurrence" || rec.Claims[0].Evidence != "BLEU 28.4 vs 26.4 baseline" || rec.Claims[0].Location != "Table 2" {
		t.Errorf("Claims[0] = %+v", rec.Claims[0])
	}
	if len(rec.RedFlags) != 1 || rec.RedFlags[0].Flag != "Single seed" || rec.RedFlags[0].Severity != "high" {
		t.Errorf("RedFlags = %+v", rec.RedFlags)
	}
	if len(rec.Uncertainties) != 1 || rec.Uncertainties[0].Item != "Long-sequence cost" {
		t.Errorf("Uncertainties = %+v", rec.Uncertainties)
	}
}

func TestParsePaperPartialAndBroken(t *testing.T) {
	t.Run("empty documents", func(t *testing.T) {
		rec := ParsePaper("", "", "")
		if !reflect.DeepEqual(rec, PaperRecord{}) {
			t.Errorf("empty parse = %+v, want zero record", rec)
		}
	})

	t.Run("paper only, no note or appraisal", func(t *testing.T) {
		rec := ParsePaper(fullPaperMD, "", "")
		if rec.Title != "Attention Is All You Need" {
			t.Errorf("Title = %q", rec.Title)
		}
		if rec.Claims != nil || rec.RedFlags != nil || rec.Uncertainties != nil {
			t.Errorf("note lists should be nil: %+v", rec)
		}
	})

	t.Run("no front matter falls back to first heading", func(t *testing.T) {
		rec := ParsePaperMD("# Just A Title\n\nProse.\n")
		if rec.Title != "Just A Title" {
			t.Errorf("Title = %q, want %q", rec.Title, "Just A Title")
		}
	})

	t.Run("truncated front matter (no closing fence)", func(t *testing.T) {
		rec := ParsePaperMD("---\ntitle: No Closing Fence\n")
		if rec.Title != "No Closing Fence" {
			t.Errorf("Title = %q, want %q", rec.Title, "No Closing Fence")
		}
	})

	t.Run("malformed YAML recovers via fallback scanner", func(t *testing.T) {
		md := "---\ntitle: Broken Title\nvenue: Somewhere\nmode: review\nreading: skip\nauthors: [unterminated\n---\n"
		rec := ParsePaperMD(md)
		if rec.Title != "Broken Title" {
			t.Errorf("Title = %q, want %q", rec.Title, "Broken Title")
		}
		if rec.Venue != "Somewhere" {
			t.Errorf("Venue = %q, want %q", rec.Venue, "Somewhere")
		}
		if rec.Mode != ModeReview || rec.Reading != ReadingSkip {
			t.Errorf("Mode/Reading = %q/%q, want review/skip (fallback scanner)", rec.Mode, rec.Reading)
		}
		if len(rec.Authors) != 1 || rec.Authors[0] != "unterminated" {
			t.Errorf("Authors = %v, want [unterminated]", rec.Authors)
		}
	})

	t.Run("scalar shorthands for lists and year", func(t *testing.T) {
		md := "---\ntitle: T\nauthors: Jane Doe, John Roe\nyear: \"2019\"\nresearch_ids: H-003; H-004\n---\n"
		rec := ParsePaperMD(md)
		if want := []string{"Jane Doe", "John Roe"}; !reflect.DeepEqual(rec.Authors, want) {
			t.Errorf("Authors = %v, want %v", rec.Authors, want)
		}
		if rec.Year != 2019 {
			t.Errorf("Year = %d, want 2019", rec.Year)
		}
		if want := []string{"H-003", "H-004"}; !reflect.DeepEqual(rec.ResearchIDs, want) {
			t.Errorf("ResearchIDs = %v, want %v", rec.ResearchIDs, want)
		}
	})
}

func TestParseNoteTolerance(t *testing.T) {
	t.Run("positional columns with unusual headers", func(t *testing.T) {
		md := "# Note\n\n## Assertions\n\n| Statement | Support |\n|---|---|\n| A | B |\n"
		claims, _, _ := ParseNote(md)
		if len(claims) != 1 || claims[0].Claim != "A" || claims[0].Evidence != "B" {
			t.Errorf("claims = %+v, want [{A B}]", claims)
		}
	})

	t.Run("no tables yields nils", func(t *testing.T) {
		claims, flags, unc := ParseNote("# Note\n\nProse only.\n")
		if claims != nil || flags != nil || unc != nil {
			t.Errorf("expected nil lists, got %+v %+v %+v", claims, flags, unc)
		}
	})

	t.Run("escaped pipe and folded newline round-trip in a cell", func(t *testing.T) {
		md := "# Note\n\n## Claims\n\n| Claim | Evidence |\n|---|---|\n| a \\| b | line1<br>line2 |\n"
		claims, _, _ := ParseNote(md)
		if len(claims) != 1 {
			t.Fatalf("claims = %+v", claims)
		}
		if claims[0].Claim != "a | b" {
			t.Errorf("Claim = %q, want %q", claims[0].Claim, "a | b")
		}
		if claims[0].Evidence != "line1\nline2" {
			t.Errorf("Evidence = %q, want a newline", claims[0].Evidence)
		}
	})
}

func TestParseAppraisalTolerance(t *testing.T) {
	t.Run("table form", func(t *testing.T) {
		v, c := ParseAppraisal(fullAppraisalMD)
		if v != VerdictAccepted || c != ConfidenceHigh {
			t.Errorf("got (%q, %q)", v, c)
		}
	})
	t.Run("free-form lines", func(t *testing.T) {
		md := "# Appraisal\n\n- **Verdict:** refuted\nConfidence: low\n"
		v, c := ParseAppraisal(md)
		if v != VerdictRejected || c != ConfidenceLow {
			t.Errorf("got (%q, %q), want (rejected, low)", v, c)
		}
	})
	t.Run("appraisal-template synonyms fold", func(t *testing.T) {
		if v, _ := ParseAppraisal("# Review\n\n- **Verdict:** weak accept\n"); v != VerdictAccepted {
			t.Errorf("weak accept = %q, want accepted", v)
		}
		if v, _ := ParseAppraisal("Verdict: borderline\n"); v != VerdictUncertain {
			t.Errorf("borderline = %q, want uncertain", v)
		}
	})
	t.Run("empty document", func(t *testing.T) {
		v, c := ParseAppraisal("")
		if v != "" || c != "" {
			t.Errorf("got (%q, %q), want empty", v, c)
		}
	})
}

func TestAppraisalSheetWinsOverFrontMatter(t *testing.T) {
	md := "---\ntitle: T\nverdict: rejected\nconfidence: low\n---\n"
	appraisal := "# Appraisal\n\n| Field | Value |\n|---|---|\n| Verdict | accepted |\n| Confidence | high |\n"
	rec := ParsePaper(md, "", appraisal)
	if rec.Verdict != VerdictAccepted || rec.Confidence != ConfidenceHigh {
		t.Errorf("got (%q, %q), want the appraisal sheet to win", rec.Verdict, rec.Confidence)
	}
}

// TestReadingAndVerdictAreDistinctAxes pins the acceptance criteria: the card's
// `mode`/`reading`/`verdict` are three distinct signals. `reading` is the
// reading DECISION (only ever from the card), while `verdict` is the SOUNDNESS
// verdict (appraisal wins), and the appraisal-template synonyms fold.
func TestReadingAndVerdictAreDistinctAxes(t *testing.T) {
	md := "---\nid: P-009\ntitle: T\nmode: review\nreading: read selectively\nverdict: weak accept\nconfidence: high\n---\n"
	rec := ParsePaperMD(md)
	if rec.Mode != ModeReview {
		t.Errorf("Mode = %q, want review", rec.Mode)
	}
	if rec.Reading != ReadingSelective {
		t.Errorf("Reading = %q, want selective", rec.Reading)
	}
	if rec.Verdict != VerdictAccepted {
		t.Errorf("Verdict = %q, want accepted (weak accept folds)", rec.Verdict)
	}

	// The appraisal sheet still wins for the soundness verdict and never
	// supplies a reading decision (the reading axis has no appraisal source).
	appraisal := "# Appraisal\n\n| Field | Value |\n|---|---|\n| Verdict | rejected |\n| Confidence | low |\n"
	combined := ParsePaper(md, "", appraisal)
	if combined.Verdict != VerdictRejected {
		t.Errorf("Verdict = %q, want rejected (appraisal wins)", combined.Verdict)
	}
	if combined.Reading != ReadingSelective {
		t.Errorf("Reading = %q, want selective (card is the only reading source)", combined.Reading)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	rec := ParsePaper(fullPaperMD, fullNoteMD, fullAppraisalMD)

	re := ParsePaper(RenderPaperMD(rec), RenderNoteMD(rec), RenderAppraisalMD(rec))
	if !reflect.DeepEqual(rec, re) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", re, rec)
	}
}

func TestParsePaperDirAndLibrary(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "papers")

	writeFixtureDir(t, filepath.Join(lib, "attention"), fullPaperMD, fullNoteMD, fullAppraisalMD)
	// A partial paper: only paper.md.
	if err := os.MkdirAll(filepath.Join(lib, "partial"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "partial", PaperFileName), []byte("---\nid: P-002\ntitle: Partial\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray non-paper directory must be skipped.
	if err := os.MkdirAll(filepath.Join(lib, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("ParsePaperDir reads a full directory", func(t *testing.T) {
		rec, err := ParsePaperDir(filepath.Join(lib, "attention"))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Title != "Attention Is All You Need" || len(rec.Claims) != 2 {
			t.Errorf("rec = %+v", rec)
		}
		if rec.Dir != filepath.Join(lib, "attention") {
			t.Errorf("Dir = %q", rec.Dir)
		}
	})

	t.Run("ParsePaperDir tolerates a partial directory", func(t *testing.T) {
		rec, err := ParsePaperDir(filepath.Join(lib, "partial"))
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != "P-002" || rec.Slug != "partial" {
			t.Errorf("rec = %+v, want id P-002 slug partial", rec)
		}
	})

	t.Run("ParsePaperDir errors on a missing directory", func(t *testing.T) {
		if _, err := ParsePaperDir(filepath.Join(lib, "nope")); err == nil {
			t.Error("expected an error for a missing directory")
		}
	})

	t.Run("ParseLibraryDir collects papers and skips strays", func(t *testing.T) {
		got, err := ParseLibraryDir(lib)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Papers) != 2 {
			t.Fatalf("Papers = %d, want 2 (%+v)", len(got.Papers), got.Papers)
		}
		if got.Papers[0].ID != "P-001" || got.Papers[1].ID != "P-002" {
			t.Errorf("order = [%q, %q], want [P-001, P-002]", got.Papers[0].ID, got.Papers[1].ID)
		}
		if got.Root != lib {
			t.Errorf("Root = %q", got.Root)
		}
	})

	t.Run("ParseLibraryDir errors on a missing root", func(t *testing.T) {
		if _, err := ParseLibraryDir(filepath.Join(root, "missing")); err == nil {
			t.Error("expected an error for a missing library root")
		}
	})
}

// writeFixtureDir writes the three artifact files of a paper directory.
func writeFixtureDir(t *testing.T, dir, paper, note, appraisal string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		PaperFileName:     paper,
		NoteFileName:      note,
		AppraisalFileName: appraisal,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
