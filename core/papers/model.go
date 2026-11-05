// Package papers parses the literature ("papers") library — paper.md cards
// plus optional note.md/appraisal.md sheets — into an in-memory model:
// model.go types + pure logic, parser.go best-effort parsing, writer.go
// atomic containment-checked writes (specs/domains/papers.md). Zero values
// are valid partial states.
package papers

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Artifact file names — single source of truth shared by parser and
// writer; layout <libraryRoot>/<slug>/{paper.md, note.md, appraisal.md}.
const (
	// PaperFileName is the per-paper front-matter card.
	PaperFileName = "paper.md"

	// NoteFileName is the per-paper evidence note (claims, red flags,
	// uncertainty).
	NoteFileName = "note.md"

	// AppraisalFileName is the per-paper critical-appraisal sheet.
	AppraisalFileName = "appraisal.md"

	// LiteratureFileName is the literature-neighbourhood graph written by the
	// study-paper `literature.py` helper — helper OUTPUT, not a backend-writer
	// artifact and not part of PaperArtifacts.
	LiteratureFileName = "literature.json"
)

// PaperArtifacts lists the writer-owned artifacts in write order — the
// canonical iteration order for writers and tests.
var PaperArtifacts = []string{PaperFileName, NoteFileName, AppraisalFileName}

// Mode is how deeply the paper was engaged with; unrecognized values are
// preserved verbatim (specs/domains/papers.md).
type Mode string

const (
	// ModeSkim: only the abstract/introduction were read.
	ModeSkim Mode = "skim"

	// ModeDeep: the paper was read in full and its claims were verified.
	ModeDeep Mode = "deep"

	// ModeSurvey: read as part of a broader survey, not as an individual target.
	ModeSurvey Mode = "survey"

	// ModeReview: study-paper "review" depth — read in full and appraised.
	ModeReview Mode = "review"

	// ModeImplement: study-paper "implement" depth — read to re-implement it.
	ModeImplement Mode = "implement"

	// ModeTeach: study-paper "teach" depth — studied to explain its ideas.
	ModeTeach Mode = "teach"
)

// Verdict is the appraisal's overall judgement on a paper. String enum,
// matching the methodology's accept/reject vocabulary.
type Verdict string

const (
	// VerdictAccepted: claims judged sound enough to build on.
	VerdictAccepted Verdict = "accepted"

	// VerdictRejected: claims judged unsound or non-reproducible.
	VerdictRejected Verdict = "rejected"

	// VerdictUncertain: evidence insufficient or contradictory.
	VerdictUncertain Verdict = "uncertain"
)

// Confidence is the appraiser's confidence in the recorded Verdict.
type Confidence string

const (
	// ConfidenceLow: thin or indirect evidence.
	ConfidenceLow Confidence = "low"

	// ConfidenceMedium: supported but not exhaustively.
	ConfidenceMedium Confidence = "medium"

	// ConfidenceHigh: well-supported by direct evidence.
	ConfidenceHigh Confidence = "high"
)

// Reading is the reading DECISION — a distinct axis from Verdict (the
// soundness judgement); the two MUST NOT be conflated (specs/domains/papers.md).
type Reading string

const (
	// ReadingFull: read the paper in full.
	ReadingFull Reading = "full"

	// ReadingSelective: read only the parts worth reading.
	ReadingSelective Reading = "selective"

	// ReadingSkip: skip the paper.
	ReadingSkip Reading = "skip"
)

// normalizeValueToken lower-cases and strips Markdown emphasis/punctuation
// — the single value-normalizer for every fold in the package (incl.
// stage/grade in flashcards.go), so header and value folding cannot drift.
func normalizeValueToken(raw string) string {
	return strings.ToLower(strings.Trim(cleanLine(raw), "*_` ."))
}

// NormalizeMode performs NO validation — every value passes through
// verbatim, preserving a hand-authored vocabulary (specs/domains/papers.md).
func NormalizeMode(raw string) Mode {
	return Mode(normalizeValueToken(raw))
}

// NormalizeReading folds the skill's prose forms ("read in full", "read
// selectively") onto the canonical constants and the "no decision" spellings
// ("n/a", "none", "-", "") to ""; unrecognized values come back lower-cased.
func NormalizeReading(raw string) Reading {
	s := normalizeValueToken(raw)
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

// NormalizeVerdict folds appraisal-template synonyms onto the canonical
// constants; unrecognized values come back lower-cased.
func NormalizeVerdict(raw string) Verdict {
	s := normalizeValueToken(raw)
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

// NormalizeConfidence folds synonyms ("med", "moderate") onto the canonical
// constants; unrecognized values come back lower-cased.
func NormalizeConfidence(raw string) Confidence {
	s := normalizeValueToken(raw)
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

// paperIDRe matches a whole paper identifier ("P-001"), anchored at both
// ends so "group-2" cannot mint a spurious canonical id.
var paperIDRe = regexp.MustCompile(`(?i)^P-?(\d+)$`)

// researchIDRe matches a whole H-NNN reference — a deliberate local copy of
// the research package's spelling rule (no core/research dependency).
var researchIDRe = regexp.MustCompile(`(?i)^H-?(\d+)$`)

// NormalizePaperID canonicalizes a paper id to "P-NNN" (P001 / P-1 / p1
// accepted); zero padding keeps one number from coexisting as two records
// (specs/domains/papers.md).
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

// NormalizeResearchID canonicalizes an H-NNN reference the way
// NormalizePaperID does for papers; "" when no reference is present.
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

// comparePaperIDs orders IDs numerically (P-2 before P-10), with a
// lexicographic tie-break for determinism.
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

// Slugify folds a title/id into a filesystem-safe slug (Unicode letters
// preserved, so a non-Latin title stays non-empty); the result is NOT
// guaranteed to satisfy ValidSlug (specs/domains/papers.md).
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

// ValidSlug is the fail-closed security check for a slug used as a paper's
// directory name (a slug is joined onto the library root); the writer
// re-checks containment through pathutil (specs/domains/papers.md).
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

// Identifier is one external identifier for a paper (DOI, arXiv id, ISBN,
// URL, …); Scheme is a lower-cased tag, Value the verbatim identifier text.
type Identifier struct {
	Scheme string `json:"scheme" yaml:"scheme"`
	Value  string `json:"value" yaml:"value"`
}

// Anchor is a notable paper location later evidence points back to: Label
// a short human tag, Ref the location or quote, Note optional context.
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

// PaperRecord is one paper — the library's atomic unit; the optional front-
// matter fields carry zero values for whatever the card lacked.
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

// ResolvedSlug resolves the directory name: the valid Slug, else a
// title-derived slug, else an ID-derived one; "" when none is valid — the
// writer rejects such a record rather than guessing.
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

// PaperLibrary is a parsed library: root plus deterministically ordered
// records; an empty library is a valid partial state.
type PaperLibrary struct {
	// Root is the library root directory the papers were parsed from.
	Root string `json:"root,omitempty"`

	// Papers is the set of paper records, ordered by ID then slug.
	Papers []*PaperRecord `json:"papers"`
}

// Get resolves a key by exact ID → exact slug → normalized P-NNN → slugified
// key, so an exact ID wins over a colliding slug (specs/domains/papers.md);
// nil when nothing matches.
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

// ByResearchID returns the papers linked to an H-NNN reference (normalized,
// so "h1" and "H-001" agree), in library order. Spec-owned API (specs/
// domains/papers.md) — keep despite no current backend caller.
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
