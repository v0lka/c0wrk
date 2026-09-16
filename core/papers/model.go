// Package papers parses a project's literature ("papers") library — a
// per-paper paper.md front-matter card plus an optional note.md evidence note
// and appraisal.md critical-appraisal sheet — into an in-memory model.
//
// The package mirrors core/research and is split three ways:
//
//   - model.go  — domain types plus pure derived logic (ID/slug
//     normalization, library lookup/indexing). No I/O.
//   - parser.go — content parsing (YAML front matter, Markdown tables) and the
//     thin filesystem orchestrators (ParsePaperDir / ParseLibraryDir). Parsing
//     is best-effort: every artifact is optional, so a partially written or
//     hand-edited paper still yields a usable record.
//   - writer.go — atomic (temp file + rename) persistence of a paper's three
//     artifacts, containment-checked against the library root.
//
// All structural logic is exposed as pure functions over strings or already
// parsed structs, so it can be unit-tested in isolation without touching disk.
//
// Zero values are meaningful: an empty PaperRecord is a valid partial state (a
// paper directory whose paper.md has not been written yet), and an empty
// PaperLibrary is a valid empty library.
package papers

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Artifact file names. These are the single source of truth shared by the
// parser (reads them) and the writer (renders them); the library layout is
// <libraryRoot>/<slug>/{paper.md, note.md, appraisal.md}.
const (
	// PaperFileName is the per-paper front-matter card.
	PaperFileName = "paper.md"

	// NoteFileName is the per-paper evidence note (claims, red flags,
	// uncertainty).
	NoteFileName = "note.md"

	// AppraisalFileName is the per-paper critical-appraisal sheet.
	AppraisalFileName = "appraisal.md"

	// LiteratureFileName is the per-paper literature-neighbourhood graph
	// (predecessors / citing / contradiction candidates) written by the
	// study-paper `literature.py` helper. Unlike the three card artifacts
	// above it is NOT produced by the backend writer and NOT part of
	// PaperArtifacts — it is helper OUTPUT, present only after a lookup runs.
	LiteratureFileName = "literature.json"
)

// PaperArtifacts lists every artifact file name a paper directory may carry,
// in write order. It is the canonical iteration order for writers and for
// tests that assert on a fully-populated directory.
var PaperArtifacts = []string{PaperFileName, NoteFileName, AppraisalFileName}

// Mode is how deeply the paper was engaged with. It is a string enum so it
// round-trips cleanly through JSON and the paper.md front matter. Mode is
// descriptive rather than policy-bearing: an unrecognized value is preserved
// verbatim by NormalizeMode so a hand-authored vocabulary is never lost.
type Mode string

const (
	// ModeSkim: only the abstract/introduction were read.
	ModeSkim Mode = "skim"

	// ModeDeep: the paper was read in full and its claims were verified.
	ModeDeep Mode = "deep"

	// ModeSurvey: the paper was read as part of a broader survey rather than
	// as an individual target.
	ModeSurvey Mode = "survey"

	// ModeReview: the study-paper "review" depth — the paper was read in full
	// and appraised (the skill's full-appraisal pass).
	ModeReview Mode = "review"

	// ModeImplement: the study-paper "implement" depth — the paper was read to
	// re-implement / reproduce its method.
	ModeImplement Mode = "implement"

	// ModeTeach: the study-paper "teach" depth — the paper was studied to
	// learn and explain its ideas.
	ModeTeach Mode = "teach"
)

// Verdict is the appraisal's overall judgement on a paper. String enum,
// matching the methodology's accept/reject vocabulary.
type Verdict string

const (
	// VerdictAccepted: the paper's claims were judged sound enough to build
	// on.
	VerdictAccepted Verdict = "accepted"

	// VerdictRejected: the paper's claims were judged unsound or
	// non-reproducible.
	VerdictRejected Verdict = "rejected"

	// VerdictUncertain: the evidence was insufficient or contradictory.
	VerdictUncertain Verdict = "uncertain"
)

// Confidence is the appraiser's confidence in the recorded Verdict.
type Confidence string

const (
	// ConfidenceLow: the verdict rests on thin or indirect evidence.
	ConfidenceLow Confidence = "low"

	// ConfidenceMedium: the verdict is supported but not exhaustively.
	ConfidenceMedium Confidence = "medium"

	// ConfidenceHigh: the verdict is well-supported by direct evidence.
	ConfidenceHigh Confidence = "high"
)

// Reading is the reading DECISION recorded on a paper's card — how much of the
// paper to read. It is a distinct axis from Verdict: the reading decision
// ("read in full / read selectively / skip") answers "should I read this?",
// while the Verdict answers "are its claims sound?". The card's front matter
// carries the reading decision (`reading:`) and the soundness verdict
// (`verdict:`, kept in sync with the appraisal sheet). String enum, so it
// round-trips cleanly through JSON and the paper.md front matter.
type Reading string

const (
	// ReadingFull: read the paper in full.
	ReadingFull Reading = "full"

	// ReadingSelective: read only the parts worth reading.
	ReadingSelective Reading = "selective"

	// ReadingSkip: skip the paper.
	ReadingSkip Reading = "skip"
)

// NormalizeMode canonicalizes a mode token (lower-cased, trimmed). Known
// values map to their constants; anything else is returned lower-cased so it
// is preserved rather than dropped. The known set spans both vocabularies the
// skill writes — the engagement depths (skim/deep/survey) and the skill's own
// modes (review/implement/teach) — so a card written with either token survives
// the round trip instead of losing its mode badge.
func NormalizeMode(raw string) Mode {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch Mode(s) {
	case ModeSkim, ModeDeep, ModeSurvey, ModeReview, ModeImplement, ModeTeach:
		return Mode(s)
	default:
		return Mode(s)
	}
}

// NormalizeReading canonicalizes a reading-decision token (lower-cased,
// trimmed), folding the phrase forms the skill writes in prose ("read in full",
// "read selectively") onto the canonical constants, and folding the "no reading
// decision" spellings ("n/a", "none", "-", "") to the empty value. An
// unrecognized value is returned lower-cased so callers can still render it.
func NormalizeReading(raw string) Reading {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.Trim(s, "*_` .")
	switch s {
	case "full", "read in full", "read fully", "in full":
		return ReadingFull
	case "selective", "read selectively", "partially", "partial":
		return ReadingSelective
	case "skip", "skipped":
		return ReadingSkip
	case "", "n/a", "na", "none", "-":
		return ""
	default:
		return Reading(s)
	}
}

// NormalizeVerdict canonicalizes a verdict token drawn from any source (front
// matter, appraisal.md table, or a free-form line). It lower-cases and trims
// the input and folds common synonyms onto the canonical constants; an
// unrecognized value is returned lower-cased so callers can still render it.
func NormalizeVerdict(raw string) Verdict {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.Trim(s, "*_` .")
	switch s {
	case "accepted", "accept", "confirmed", "confirm", "reproducible", "sound",
		"weak accept", "weak-accept":
		return VerdictAccepted
	case "rejected", "reject", "refuted", "refute", "unsound", "non-reproducible", "flawed",
		"weak reject", "weak-reject":
		return VerdictRejected
	case "uncertain", "unclear", "unknown", "inconclusive", "unverified", "borderline":
		return VerdictUncertain
	default:
		return Verdict(s)
	}
}

// NormalizeConfidence canonicalizes a confidence token, folding common
// synonyms ("med", "moderate") onto the canonical constants. An unrecognized
// value is returned lower-cased.
func NormalizeConfidence(raw string) Confidence {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.Trim(s, "*_` .")
	switch s {
	case "low", "weak":
		return ConfidenceLow
	case "medium", "med", "moderate", "mid":
		return ConfidenceMedium
	case "high", "strong":
		return ConfidenceHigh
	default:
		return Confidence(s)
	}
}

// paperIDRe matches a library-local paper identifier, e.g. "P-001".
var paperIDRe = regexp.MustCompile(`(?i)P-?(\d+)`)

// researchIDRe matches a research-hypothesis reference (H-NNN) as recorded in a
// paper's research_ids list. It is intentionally a local copy of the research
// package's spelling rule so core/papers does not depend on core/research.
var researchIDRe = regexp.MustCompile(`(?i)H-?(\d+)`)

// NormalizePaperID canonicalizes a paper identifier to its zero-padded,
// hyphenated, upper-case form ("P-001"). It accepts "P001", "P-001", "P1" and
// "P-1" spellings, case-insensitive, and returns "" for input that contains no
// identifier. Padding to three digits (growing beyond for numbers ≥ 1000)
// keeps two spellings of the same number from coexisting as distinct records.
func NormalizePaperID(raw string) string {
	m := paperIDRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return "P-" + m[1]
	}
	return fmt.Sprintf("P-%03d", n)
}

// NormalizeResearchID canonicalizes a research-hypothesis reference to its
// zero-padded, hyphenated, upper-case form ("H-001"), accepting the same
// spelling variants as NormalizePaperID. It returns "" for input carrying no
// H-NNN reference.
func NormalizeResearchID(raw string) string {
	m := researchIDRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return "H-" + m[1]
	}
	return fmt.Sprintf("H-%03d", n)
}

// paperIDNumber extracts the numeric part of a canonical paper ID ("P-010" →
// 10). ok is false when the ID carries no P-NNN number.
func paperIDNumber(id string) (int, bool) {
	m := paperIDRe.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// comparePaperIDs orders paper IDs numerically (P-2 before P-10) rather than
// lexicographically, with a lexicographic tie-break so the order stays
// deterministic for equal numbers and for IDs without a numeric part.
func comparePaperIDs(a, b string) int {
	na, aok := paperIDNumber(a)
	nb, bok := paperIDNumber(b)
	switch {
	case aok && bok && na != nb:
		return na - nb
	case aok && !bok:
		return -1 // numbered sorts before non-numbered
	case !aok && bok:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// Slugify converts an arbitrary title (or ID) into a filesystem-safe slug:
// lower-case, runs of non-alphanumeric characters collapsed to a single
// hyphen, and leading/trailing hyphens trimmed. Unicode letters and digits are
// preserved (so a non-Latin title still yields a non-empty slug) rather than
// being dropped. The result is NOT guaranteed to satisfy ValidSlug for
// pathological inputs — callers that need a validated component must check.
func Slugify(s string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			continue
		}
		pendingDash = true
	}
	return b.String()
}

// ValidSlug reports whether s is a safe single path component usable as a
// paper's directory name. It rejects empty strings, the traversal names "."
// and "..", anything containing a path separator, hidden (dot-prefixed) names,
// and control characters, and requires at least one letter or digit. It is a
// security check (a slug is joined onto the library root), so it fails closed;
// the writer additionally re-checks containment through pathutil.
func ValidSlug(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 200 {
		return false
	}
	if strings.HasPrefix(s, ".") {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	hasAlnum := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasAlnum = true
		}
	}
	return hasAlnum
}

// Identifier is a single external identifier for a paper (DOI, arXiv id,
// ISBN, URL, ...). Scheme is a lower-cased tag (e.g. "doi"); Value is the raw
// identifier text, preserved verbatim.
type Identifier struct {
	Scheme string `json:"scheme" yaml:"scheme"`
	Value  string `json:"value" yaml:"value"`
}

// Anchor is a notable location inside the paper (a section, figure, or
// quotation) that later evidence can point back to. Label is a short human tag
// (e.g. "sec3", "fig2"), Ref is the location or quoted text, and Note is
// optional context.
type Anchor struct {
	Label string `json:"label,omitempty" yaml:"label,omitempty"`
	Ref   string `json:"ref,omitempty" yaml:"ref,omitempty"`
	Note  string `json:"note,omitempty" yaml:"note,omitempty"`
}

// Claim is one extracted claim paired with the evidence supporting or
// contradicting it, parsed from note.md's claim→evidence table.
type Claim struct {
	// Claim is the assertion extracted from the paper.
	Claim string `json:"claim"`

	// Evidence is the supporting (or contradicting) evidence text.
	Evidence string `json:"evidence,omitempty"`

	// Location records where in the paper the claim/evidence sits.
	Location string `json:"location,omitempty"`

	// Stance records whether the evidence supports or contradicts the claim
	// (free-form: "supports", "contradicts", ...); empty when unstated.
	Stance string `json:"stance,omitempty"`
}

// RedFlag is one recorded concern about a paper, parsed from note.md's
// red-flags table.
type RedFlag struct {
	// Flag is the short name of the concern.
	Flag string `json:"flag"`

	// Detail explains the concern; empty when unstated.
	Detail string `json:"detail,omitempty"`

	// Severity is a free-form severity tag (e.g. "high"); empty when unstated.
	Severity string `json:"severity,omitempty"`
}

// Uncertainty is one open question or limitation, parsed from note.md's
// uncertainty table.
type Uncertainty struct {
	// Item is the open question or limitation.
	Item string `json:"item"`

	// Detail is optional elaboration.
	Detail string `json:"detail,omitempty"`
}

// PaperRecord is a single paper parsed from a paper directory. It is the atomic
// unit of the library. The front-matter fields are optional: a partially
// written or hand-edited card yields a record with whatever was present and
// zero values for the rest.
type PaperRecord struct {
	// ID is the library-local canonical identifier, e.g. "P-001". Empty when
	// the card carries none.
	ID string `json:"id,omitempty"`

	// Slug is the paper's directory name. When empty, ResolvedSlug derives one
	// from the title or ID.
	Slug string `json:"slug,omitempty"`

	// Title is the paper's human-readable title.
	Title string `json:"title,omitempty"`

	// Authors is the ordered author list.
	Authors []string `json:"authors,omitempty"`

	// Year is the publication year (0 when unknown).
	Year int `json:"year,omitempty"`

	// Venue is the publication venue (journal/conference/preprint server).
	Venue string `json:"venue,omitempty"`

	// Identifiers lists external identifiers (DOI, arXiv, ...).
	Identifiers []Identifier `json:"identifiers,omitempty"`

	// Mode records how deeply the paper was engaged with.
	Mode Mode `json:"mode,omitempty"`

	// Reading is the reading decision (read in full / selectively / skip),
	// distinct from Verdict (the soundness judgement).
	Reading Reading `json:"reading,omitempty"`

	// Verdict is the appraisal's overall judgement.
	Verdict Verdict `json:"verdict,omitempty"`

	// Confidence is the confidence in Verdict.
	Confidence Confidence `json:"confidence,omitempty"`

	// ResearchIDs lists the research-hypothesis IDs (H-NNN) this paper informs.
	ResearchIDs []string `json:"research_ids,omitempty"`

	// Anchors lists notable locations inside the paper.
	Anchors []Anchor `json:"anchors,omitempty"`

	// Claims, RedFlags, and Uncertainties are parsed from note.md.
	Claims        []Claim       `json:"claims,omitempty"`
	RedFlags      []RedFlag     `json:"red_flags,omitempty"`
	Uncertainties []Uncertainty `json:"uncertainties,omitempty"`

	// Dir is the on-disk directory the record was parsed from. It is empty for
	// a record built purely in memory (e.g. rendered for writing).
	Dir string `json:"-"`
}

// ResolvedSlug returns the slug to use for the record's directory: the
// explicit Slug when it is valid, else a slug derived from the title, else one
// derived from the ID. It returns "" when none of the three yields a valid
// component — the writer rejects such a record rather than guessing.
func (r PaperRecord) ResolvedSlug() string {
	if s := strings.TrimSpace(r.Slug); ValidSlug(s) {
		return s
	}
	if s := Slugify(r.Title); ValidSlug(s) {
		return s
	}
	if s := Slugify(r.ID); ValidSlug(s) {
		return s
	}
	return ""
}

// IsAppraised reports whether the record carries an appraisal verdict.
func (r PaperRecord) IsAppraised() bool {
	return r.Verdict != ""
}

// PaperLibrary is a parsed paper library: the library root directory and every
// paper record under it, sorted deterministically. An empty library (no papers
// yet) is a valid partial state.
type PaperLibrary struct {
	// Root is the library root directory the papers were parsed from.
	Root string `json:"root,omitempty"`

	// Papers is the set of paper records, ordered by ID then slug.
	Papers []*PaperRecord `json:"papers"`
}

// Get returns the paper matching key — by exact ID, by exact slug, by a
// normalized P-NNN id, or by a slugified key. It returns nil when nothing
// matches. The lookup order makes an exact ID win over a slug that happens to
// equal another paper's slug.
func (l *PaperLibrary) Get(key string) *PaperRecord {
	if l == nil || key == "" {
		return nil
	}
	for _, p := range l.Papers {
		if p.ID == key {
			return p
		}
	}
	for _, p := range l.Papers {
		if p.Slug == key {
			return p
		}
	}
	if id := NormalizePaperID(key); id != "" {
		for _, p := range l.Papers {
			if p.ID == id {
				return p
			}
		}
	}
	if slug := Slugify(key); slug != "" {
		for _, p := range l.Papers {
			if p.Slug == slug {
				return p
			}
		}
	}
	return nil
}

// ByResearchID returns every paper linked to the given research-hypothesis
// reference (H-NNN), in library order. The reference is normalized, so "h1"
// and "H-001" select the same papers. The result is nil when nothing matches.
func (l *PaperLibrary) ByResearchID(researchID string) []*PaperRecord {
	want := NormalizeResearchID(researchID)
	if l == nil || want == "" {
		return nil
	}
	var out []*PaperRecord
	for _, p := range l.Papers {
		for _, rid := range p.ResearchIDs {
			if rid == want {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// sortPapers orders papers by ID then slug, deterministically, so a library
// parse is stable regardless of directory-read order.
func sortPapers(papers []*PaperRecord) {
	sort.SliceStable(papers, func(i, j int) bool {
		if c := comparePaperIDs(papers[i].ID, papers[j].ID); c != 0 {
			return c < 0
		}
		return papers[i].Slug < papers[j].Slug
	})
}
