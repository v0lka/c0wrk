// Package papers — writer layer.
//
// This file renders a PaperRecord's three artifacts back to Markdown and
// persists them atomically. It follows core/research's rootwriter.go: every
// target is symlink-resolved and containment-checked against the library root
// through pathutil BEFORE a temp file is staged, then all staged files are
// renamed into place — so a failure leaves the prior content byte-for-byte
// unchanged and a symlinked paper directory can never redirect a write outside
// the library.
package papers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/pathutil"
)

// paperFrontOut is the serialized shape of paper.md's front matter. It mirrors
// PaperRecord's identity fields (the note/appraisal-derived lists live in their
// own artifacts) and omits empty fields so a sparse record still yields a clean
// card.
type paperFrontOut struct {
	ID          string       `yaml:"id,omitempty"`
	Slug        string       `yaml:"slug,omitempty"`
	Title       string       `yaml:"title,omitempty"`
	Authors     []string     `yaml:"authors,omitempty"`
	Year        int          `yaml:"year,omitempty"`
	Venue       string       `yaml:"venue,omitempty"`
	Identifiers []Identifier `yaml:"identifiers,omitempty"`
	Mode        Mode         `yaml:"mode,omitempty"`
	Reading     Reading      `yaml:"reading,omitempty"`
	Verdict     Verdict      `yaml:"verdict,omitempty"`
	Confidence  Confidence   `yaml:"confidence,omitempty"`
	ResearchIDs []string     `yaml:"research_ids,omitempty"`
	Anchors     []Anchor     `yaml:"anchors,omitempty"`
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// RenderPaperMD renders rec's paper.md card: a YAML front-matter block carrying
// the identity fields. It is the inverse of ParsePaperMD.
func RenderPaperMD(rec PaperRecord) string {
	out := paperFrontOut{
		ID:          rec.ID,
		Slug:        rec.Slug,
		Title:       rec.Title,
		Authors:     rec.Authors,
		Year:        rec.Year,
		Venue:       rec.Venue,
		Identifiers: rec.Identifiers,
		Mode:        rec.Mode,
		Reading:     rec.Reading,
		Verdict:     rec.Verdict,
		Confidence:  rec.Confidence,
		ResearchIDs: rec.ResearchIDs,
		Anchors:     rec.Anchors,
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		// The shape above cannot fail to marshal; guard anyway so a future
		// field addition cannot panic the writer.
		data = nil
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(data)
	b.WriteString("---\n")
	return b.String()
}

// RenderNoteMD renders rec's note.md: the claim→evidence, red-flags, and
// uncertainty tables. It is the inverse of ParseNote.
func RenderNoteMD(rec PaperRecord) string {
	var b strings.Builder
	b.WriteString("# Note\n\n")

	b.WriteString("## Claims\n\n")
	b.WriteString(joinCells([]string{"Claim", "Evidence", "Location", "Stance"}) + "\n")
	b.WriteString("|---|---|---|---|\n")
	for _, c := range rec.Claims {
		b.WriteString(joinCells([]string{c.Claim, c.Evidence, c.Location, c.Stance}) + "\n")
	}

	b.WriteString("\n## Red Flags\n\n")
	b.WriteString(joinCells([]string{"Flag", "Detail", "Severity"}) + "\n")
	b.WriteString("|---|---|---|\n")
	for _, f := range rec.RedFlags {
		b.WriteString(joinCells([]string{f.Flag, f.Detail, f.Severity}) + "\n")
	}

	b.WriteString("\n## Uncertainty\n\n")
	b.WriteString(joinCells([]string{"Item", "Detail"}) + "\n")
	b.WriteString("|---|---|\n")
	for _, u := range rec.Uncertainties {
		b.WriteString(joinCells([]string{u.Item, u.Detail}) + "\n")
	}

	return b.String()
}

// RenderAppraisalMD renders rec's appraisal.md: the verdict/confidence sheet.
// It is the inverse of ParseAppraisal.
func RenderAppraisalMD(rec PaperRecord) string {
	var b strings.Builder
	b.WriteString("# Appraisal\n\n")
	b.WriteString(joinCells([]string{"Field", "Value"}) + "\n")
	b.WriteString("|---|---|\n")
	b.WriteString(joinCells([]string{"Verdict", string(rec.Verdict)}) + "\n")
	b.WriteString(joinCells([]string{"Confidence", string(rec.Confidence)}) + "\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// WritePaper atomically writes rec's three artifacts into libraryRoot/<slug>/.
// The slug is rec.ResolvedSlug() (the explicit slug, else one derived from the
// title or id). The library root and the paper directory are created when
// absent, and every write target is containment-checked against the library
// root, so a symlinked paper directory cannot redirect the write outside the
// library. A record that yields no valid slug is rejected.
//
// "Atomic" means atomic-ish, exactly like the research writer: every file is
// first staged as a temp file in its resolved target directory and only renamed
// into place after all three are staged, so a staging failure leaves every
// target untouched.
func WritePaper(libraryRoot string, rec PaperRecord) error {
	slug := rec.ResolvedSlug()
	if slug == "" {
		return errors.New("paper write rejected: record has neither a valid slug, title, nor id")
	}
	if _, err := ensurePaperDir(libraryRoot, slug); err != nil {
		return err
	}
	files := map[string][]byte{
		artifactPath(libraryRoot, slug, PaperFileName):     []byte(RenderPaperMD(rec)),
		artifactPath(libraryRoot, slug, NoteFileName):      []byte(RenderNoteMD(rec)),
		artifactPath(libraryRoot, slug, AppraisalFileName): []byte(RenderAppraisalMD(rec)),
	}
	return writeFilesAtomic(libraryRoot, files)
}

// ---------------------------------------------------------------------------
// Flashcards persistence (append-only review)
// ---------------------------------------------------------------------------

// WriteFlashcards atomically writes deck as libraryRoot/<slug>/flashcards.md,
// creating the paper directory when absent. The target is symlink-resolved and
// containment-checked against the library root through writeFilesAtomic, so a
// symlinked paper directory can never redirect the write outside the library.
func WriteFlashcards(libraryRoot, slug string, deck Deck) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("flashcard write rejected: unsafe slug %q", slug)
	}
	if _, err := ensurePaperDir(libraryRoot, slug); err != nil {
		return err
	}
	target := artifactPath(libraryRoot, slug, FlashcardFileName)
	return writeFilesAtomic(libraryRoot, map[string][]byte{target: []byte(RenderFlashcards(deck))})
}

// RecordCardReview appends a self-grade to a paper's flashcards.md and updates
// the card's Stage, then writes the whole document atomically and
// containment-checked against the library root. The deck must already exist:
// the review is an append onto an authored document, never a fresh deck, so a
// missing flashcards.md is an error. An unknown card id (no row), an unknown
// grade, or a deck with no review-log table is rejected before any write —
// leaving the file byte-for-byte unchanged.
func RecordCardReview(libraryRoot, slug, cardID string, grade Grade, date string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("flashcard review rejected: unsafe slug %q", slug)
	}
	if _, err := ensurePaperDir(libraryRoot, slug); err != nil {
		return err
	}
	target := artifactPath(libraryRoot, slug, FlashcardFileName)
	content, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("flashcard review rejected: no deck to review: %w", err)
	}
	updated, err := ApplyReview(string(content), cardID, grade, date)
	if err != nil {
		return err
	}
	return writeFilesAtomic(libraryRoot, map[string][]byte{target: []byte(updated)})
}

// paperDirPath is the single construction site for a paper directory path.
// Every artifact path derives from it, and each resulting target is separately
// containment-checked before it is written.
func paperDirPath(libraryRoot, slug string) string {
	return filepath.Join(libraryRoot, slug)
}

// artifactPath is the single construction site for a paper artifact's path.
func artifactPath(libraryRoot, slug, name string) string {
	return filepath.Join(paperDirPath(libraryRoot, slug), name)
}

// ensurePaperDir creates the library root (when absent) and the paper's own
// directory, and verifies through pathutil that the paper directory stays
// inside the resolved library root. It returns the resolved paper directory.
// It fails closed: an empty root, an unsafe slug, an unresolvable root, or a
// containment mismatch is an error.
func ensurePaperDir(libraryRoot, slug string) (string, error) {
	if libraryRoot == "" {
		return "", errors.New("paper write rejected: empty library root")
	}
	if !ValidSlug(slug) {
		return "", fmt.Errorf("paper write rejected: unsafe slug %q", slug)
	}
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		return "", fmt.Errorf("failed to create paper library root %q: %w", libraryRoot, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(libraryRoot)
	if err != nil {
		return "", fmt.Errorf("paper library root %q not resolvable: %w", libraryRoot, err)
	}
	dir := filepath.Join(resolvedRoot, slug)
	contained, err := pathutil.IsWithinPath(resolvedRoot, dir)
	if err != nil {
		return "", fmt.Errorf("paper directory containment check failed for %q: %w", dir, err)
	}
	if !contained {
		return "", fmt.Errorf("paper directory %q resolves outside the library root %q", dir, libraryRoot)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create paper directory %q: %w", dir, err)
	}
	return dir, nil
}

// writeFilesAtomic writes a group of files "atomic-ish": every file is first
// staged as a temp file in its own directory (0600 → chmod 0644), and only
// after all files are fully staged are they renamed into place. Staging before
// the first rename means a staging failure (disk full, permissions) leaves
// every target untouched.
//
// root is the containment root (paper artifacts stay strictly inside the
// library). Every target is symlink-resolved and checked against the resolved
// root BEFORE any staging, and the resolved target is what is written, so a
// symlinked intermediate directory cannot redirect the write outside the
// library.
func writeFilesAtomic(root string, files map[string][]byte) error {
	type stagedFile struct {
		target string
		tmp    string
	}
	staged := make([]stagedFile, 0, len(files))
	defer func() {
		for _, s := range staged {
			_ = os.Remove(s.tmp)
		}
	}()

	// Deterministic staging order (sorted keys) so write order is stable
	// regardless of map iteration.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Resolve and containment-check every target before touching the disk:
	// a rejection here leaves all files untouched.
	resolved := make(map[string]string, len(paths))
	for _, p := range paths {
		rp, err := resolveTargetWithinRoot(root, p)
		if err != nil {
			return err
		}
		resolved[p] = rp
	}

	for _, p := range paths {
		rp := resolved[p]
		tmp, err := os.CreateTemp(filepath.Dir(rp), ".paper-*.tmp")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(files[p]); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return err
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		if err := os.Chmod(tmpName, 0o644); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		staged = append(staged, stagedFile{target: rp, tmp: tmpName})
	}

	for _, s := range staged {
		if err := os.Rename(s.tmp, s.target); err != nil {
			return err
		}
	}
	return nil
}

// resolveTargetWithinRoot symlink-resolves the containment root and the
// target's directory (the file itself may not exist yet) and re-checks
// pathutil.IsWithinPath immediately before the caller stages and renames. It
// returns the resolved target path — the on-disk location the caller may safely
// write. It fails closed: an unresolvable root/directory or any containment
// mismatch is an error.
func resolveTargetWithinRoot(root, target string) (string, error) {
	if root == "" {
		return "", errors.New("paper write rejected: empty containment root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("paper write root %q not resolvable: %w", root, err)
	}
	dir, base := filepath.Dir(target), filepath.Base(target)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("paper write target directory %q not resolvable: %w", dir, err)
	}
	resolvedTarget := filepath.Join(resolvedDir, base)

	contained, err := pathutil.IsWithinPath(resolvedRoot, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("paper write containment check failed for %q: %w", target, err)
	}
	if !contained {
		return "", fmt.Errorf("paper write target %q resolves to %q, outside the library root %q (symlinked intermediate directory?)", target, resolvedTarget, root)
	}
	return resolvedTarget, nil
}
