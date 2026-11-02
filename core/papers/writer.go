// Package papers — writer layer: renders a PaperRecord's three artifacts back
// to Markdown and persists them atomically; every write target is
// symlink-resolved and containment-checked against the library root before any
// temp file is staged.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
package papers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

// paperFrontOut is the serialized shape of paper.md's front matter: the
// PaperRecord identity fields only (note/appraisal-derived lists live in their
// own artifacts), with empty fields omitted so a sparse record yields a clean card.
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

// --- Rendering --------------------------------------------------------------

// RenderPaperMD renders rec's paper.md card; the inverse of ParsePaperMD. A
// YAML marshal failure is returned rather than silently emitting a card whose
// entire front matter is empty.
func RenderPaperMD(rec PaperRecord) (string, error) {
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
		return "", fmt.Errorf("rendering paper.md front matter: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(data)
	b.WriteString("---\n")
	return b.String(), nil
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

// --- Persistence ------------------------------------------------------------

// WritePaper atomically writes rec's three artifacts into
// libraryRoot/<slug>/, creating the directories when absent; a record with no
// valid slug is rejected.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
func WritePaper(libraryRoot string, rec PaperRecord) error {
	slug := rec.ResolvedSlug()
	if slug == "" {
		return errors.New("paper write rejected: record has neither a valid slug, title, nor id")
	}
	// Render before touching the filesystem, so a render failure leaves the
	// directory tree untouched.
	paperMD, err := RenderPaperMD(rec)
	if err != nil {
		return err
	}
	if _, err := ensurePaperDir(libraryRoot, slug); err != nil {
		return err
	}
	files := map[string][]byte{
		artifactPath(libraryRoot, slug, PaperFileName):     []byte(paperMD),
		artifactPath(libraryRoot, slug, NoteFileName):      []byte(RenderNoteMD(rec)),
		artifactPath(libraryRoot, slug, AppraisalFileName): []byte(RenderAppraisalMD(rec)),
	}
	return writeFilesAtomic(libraryRoot, files)
}

// --- Flashcards persistence (append-only review) ----------------------------

// WriteFlashcards atomically writes deck as libraryRoot/<slug>/flashcards.md
// (creating the paper directory when absent): the public deck-writer API, the
// inverse of ParseFlashcards paired with RenderFlashcards — intentionally
// exported even though in-app appends go through RecordCardReview.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
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
// the card's Stage, writing the whole document atomically. The review is an
// append onto an authored deck (a missing flashcards.md is an error).
// Fail-closed validation order: see specs/domains/papers.md
// (RecordFlashcardReview invariant).
func RecordCardReview(libraryRoot, slug, cardID string, grade Grade, date string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("flashcard review rejected: unsafe slug %q", slug)
	}
	target := artifactPath(libraryRoot, slug, FlashcardFileName)
	// Read (validating the deck's existence) before creating anything; safeio
	// refuses a non-regular deck path instead of blocking the review RPC forever.
	content, err := safeio.ReadFile(target)
	if err != nil {
		return fmt.Errorf("flashcard review rejected: no deck to review: %w", err)
	}
	updated, err := ApplyReview(string(content), cardID, grade, date)
	if err != nil {
		return err
	}
	// Only now, with a valid updated document in hand, create/verify the paper
	// directory and write atomically.
	if _, err := ensurePaperDir(libraryRoot, slug); err != nil {
		return err
	}
	return writeFilesAtomic(libraryRoot, map[string][]byte{target: []byte(updated)})
}

// paperDirPath is the single construction site for a paper directory path;
// every artifact path derives from it, and each target is separately
// containment-checked before it is written.
func paperDirPath(libraryRoot, slug string) string {
	return filepath.Join(libraryRoot, slug)
}

// artifactPath is the single construction site for a paper artifact's path.
func artifactPath(libraryRoot, slug, name string) string {
	return filepath.Join(paperDirPath(libraryRoot, slug), name)
}

// ensurePaperDir creates the library root (when absent) and the paper's
// directory, and returns the containment-verified resolved directory.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
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

// writeFilesAtomic writes a group of files atomically: every target is
// symlink-resolved and containment-checked against root before staging, all
// targets are staged as temp files (0600 → chmod 0644) before the first
// rename, and a failure in either phase rolls the whole set back to the
// byte-for-byte original state (existing targets are parked at same-directory
// backups, so entries whose own rename never ran still get their originals
// restored). Same contract as core/research/writer.go.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
// stagedFile tracks one file of a writeFilesAtomic set through its phases:
// the resolved target, the staged temp holding the new contents, and — once
// the original has been moved aside — the backup it is parked at ("" while
// the target is untouched, or for a brand-new file that has no original).
type stagedFile struct {
	target string
	tmp    string
	backup string
}

func writeFilesAtomic(root string, files map[string][]byte) error {
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

	// Phase 1 — move every existing original aside to a same-directory backup,
	// so the commit below never destroys a pre-image it cannot restore; a
	// failure here rolls the already-moved entries back.
	for i := range staged {
		backup, err := backupTargetPath(staged[i].target)
		if err == nil {
			err = os.Rename(staged[i].target, backup)
		}
		if errors.Is(err, fs.ErrNotExist) {
			// Brand-new file: nothing to move aside, nothing to roll back.
			continue
		}
		if err != nil {
			return fmt.Errorf("moving %q aside for an atomic write: %w", staged[i].target,
				errors.Join(err, rollbackStaged(staged[:i])))
		}
		staged[i].backup = backup
	}

	// Phase 2 — commit: rename every staged temp onto its now-free target. A
	// failure rolls the WHOLE set back, not just the committed prefix: entries
	// after the failing index lost their originals to phase 1 and need their
	// backups restored even though their own rename never ran.
	for i := range staged {
		if err := os.Rename(staged[i].tmp, staged[i].target); err != nil {
			return fmt.Errorf("committing %q: %w", staged[i].target, errors.Join(err, rollbackStaged(staged)))
		}
	}

	// Success — drop the backups (best-effort: the mutation is committed, a
	// stuck backup must not fail it; a leftover keeps the original bytes).
	for i := range staged {
		if staged[i].backup != "" {
			_ = os.Remove(staged[i].backup)
		}
	}
	return nil
}

// rollbackStaged restores every entry of a partially committed writeFilesAtomic
// set to its pre-write state (a committed entry gets its original renamed back
// over the new contents, which move to the temp name for the deferred cleanup;
// a never-committed entry just gets its original back; a brand-new file loses
// its partial creation). It returns the joined errors of every failed restore —
// non-nil means some file may be left missing or replaced, which the caller
// MUST surface alongside the original failure.
func rollbackStaged(staged []stagedFile) error {
	var restoreErrs []error
	for i := range staged {
		s := &staged[i]
		switch s.backup {
		case "":
			// Brand-new file: if the temp got as far as the target, remove it.
			if err := os.Remove(s.target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restoreErrs = append(restoreErrs, fmt.Errorf("removing rolled-back new file %q: %w", s.target, err))
			}
		default:
			// Original parked at the backup: whatever now sits at the target
			// makes way for it, and the displaced bytes move to the temp name
			// (deferred cleanup) rather than being removed, so they stay
			// inspectable on a later failure.
			if err := os.Rename(s.target, s.tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restoreErrs = append(restoreErrs, fmt.Errorf("displacing rolled-back contents of %q: %w", s.target, err))
				continue
			}
			if err := os.Rename(s.backup, s.target); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restoring original %q from backup: %w", s.target, err))
			}
		}
	}
	return errors.Join(restoreErrs...)
}

// backupTargetPath returns a fresh, currently-unused same-directory path to
// park a target's original at while its replacement is committed
// (.<base>.bak-<random>): dot-prefixed so the parked original is invisible to
// the paper listing/globbing paths, and .bak-suffixed so it never matches *.md.
func backupTargetPath(target string) (string, error) {
	dir, base := filepath.Dir(target), filepath.Base(target)
	var randBytes [4]byte
	for range 3 {
		if _, err := rand.Read(randBytes[:]); err != nil {
			return "", fmt.Errorf("generating backup name for %q: %w", target, err)
		}
		candidate := filepath.Join(dir, fmt.Sprintf(".%s.bak-%s", base, hex.EncodeToString(randBytes[:])))
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("finding a free backup name for %q", target)
}

// resolveTargetWithinRoot symlink-resolves the containment root and the
// target's directory (the file itself may not exist yet) and re-checks
// containment immediately before the caller stages and renames, returning the
// resolved target path.
// See specs/domains/papers.md (atomic, containment-checked paper writes).
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
