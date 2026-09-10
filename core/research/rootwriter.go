// Package research — root-level writer layer.
//
// This file implements the research-root mutations that operate ABOVE a
// single project's hypothesis graph: activating a project (rewriting the
// root index.md so the project's row becomes the last entry — the
// chronological "last entry = active" rule PickActiveProject applies) and
// deleting a project (removing its index rows and its R-NNN-* directory
// tree). Like the hypothesis mutations in writer.go, both entry points read
// first, compute the new index content purely in memory, and write through
// writeFilesAtomic (temp file + rename, containment-checked against
// researchRoot), so a failure leaves index.md byte-for-byte unchanged.
package research

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"
)

// SetActiveResearch makes rid the active research project of a research root.
// Activation rewrites the root's index.md so rid's row becomes the LAST table
// entry — the chronological rule PickActiveProject uses to select the active
// R-NNN (the latest index entry wins over the highest-numbered directory).
// When index.md is missing, or carries no row for rid, a minimal row linking
// the project's brief is appended instead (index.md is created with the
// canonical skeleton when absent). The write is atomic-ish
// (writeFilesAtomic) and a no-op when rid's row already is the last table
// entry.
//
// rid must resolve to a project under researchRoot — the same ownership
// check ProjectDir applies to hypothesis mutations — so activating a research
// that lives under another root, or an id that matches nothing, is rejected
// before any file is touched.
func SetActiveResearch(researchRoot, rid string) error {
	rid = NormalizeResearchID(rid)
	if rid == "" {
		return errors.New("invalid research project id (want R-NNN)")
	}

	// Ownership check, plus the project directory the minimal row links to.
	projectDir, err := ProjectDir(researchRoot, rid)
	if err != nil {
		return err
	}

	indexPath := filepath.Join(researchRoot, "index.md")
	current, _ := readFile(indexPath)

	next, moved := moveIndexRowToTableEnd(current, rid)
	if !moved {
		title := projectBriefTitle(projectDir)
		if title == "" {
			title = rid
		}
		next = appendMinimalIndexRow(current, rid, title, briefRelLink(researchRoot, projectDir))
	}
	if next == current {
		// rid's row already is the last table entry: nothing to write.
		return nil
	}
	if err := writeFilesAtomic(researchRoot, map[string][]byte{indexPath: []byte(next)}); err != nil {
		return fmt.Errorf("failed to update research index: %w", err)
	}
	return nil
}

// DeleteResearchProject removes a research project from a research root: every
// index.md entry line referencing it, then its R-NNN-* directory tree in full
// (os.RemoveAll). rid must resolve under researchRoot (the ProjectDir
// ownership check), and the resolved project directory must sit strictly
// INSIDE the resolved research root — a symlinked or otherwise escaping
// project directory, or one equal to the root itself, is rejected before
// anything is touched (the same containment posture as writeFilesAtomic).
//
// The index rewrite is atomic and happens before the directory removal, so an
// index failure leaves the project directory untouched; a directory-removal
// failure leaves a stale index row, which the best-effort parser tolerates
// (an entry without a matching directory never becomes the active project).
// os.RemoveAll does not follow symlinks — a symlinked child inside the tree
// is unlinked, not chased.
func DeleteResearchProject(researchRoot, rid string) error {
	rid = NormalizeResearchID(rid)
	if rid == "" {
		return errors.New("invalid research project id (want R-NNN)")
	}
	projectDir, err := ProjectDir(researchRoot, rid)
	if err != nil {
		return err
	}
	resolvedDir, err := resolveProjectDirWithinRoot(researchRoot, projectDir)
	if err != nil {
		return err
	}

	// 1. Drop every index entry line for rid (atomic; skipped when index.md
	// carries none).
	indexPath := filepath.Join(researchRoot, "index.md")
	if current, ok := readFile(indexPath); ok {
		if next := removeIndexEntryLines(current, rid); next != current {
			if err := writeFilesAtomic(researchRoot, map[string][]byte{indexPath: []byte(next)}); err != nil {
				return fmt.Errorf("failed to update research index: %w", err)
			}
		}
	}

	// 2. Remove the project directory tree — the resolved location that was
	// containment-checked, exactly like writeFilesAtomic writes the resolved
	// target.
	if err := os.RemoveAll(resolvedDir); err != nil {
		return fmt.Errorf("failed to remove research project directory %q: %w", projectDir, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// index.md content helpers (pure)
// ---------------------------------------------------------------------------

// indexTableHeader and indexTableSeparator are the canonical index.md table
// skeleton (the research-init template; see testdata/index.md).
const (
	indexTableHeader    = "| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |"
	indexTableSeparator = "|---|---|---|---|---|---|---|"
)

// indexLineEntryID returns the research ID a single index.md line contributes
// to ParseIndex's result — "" when the line is not an entry line (headings,
// table headers, separators, prose). It reuses ParseIndex on the single line
// so "what counts as an entry" stays defined in exactly one place: a line
// carrying a brief.md Markdown link (ID from the link target, else the link
// text) or, when the line has no link at all, a bare R-NNN token.
func indexLineEntryID(line string) string {
	entries := ParseIndex(line)
	if len(entries) == 0 {
		return ""
	}
	return entries[0].ID
}

// moveIndexRowToTableEnd rewrites index.md content so rid's row becomes the
// last row of its Markdown table — the entry order PickActiveProject reads
// (the last parsed entry is the active project). The row moved is the FIRST
// line referencing rid, the occurrence that fixes its position in ParseIndex's
// de-duplicated order; later duplicate references (e.g. a bullet list
// mirroring the table) stay in place and are ignored by the parser's
// de-duplication. A table row moves to the end of the last table; a
// non-table entry line (a bullet) moves to the end of the file. It returns
// the new content and whether a row was found at all.
func moveIndexRowToTableEnd(content, rid string) (string, bool) {
	lines := strings.Split(content, "\n")
	rowIdx := -1
	for i, line := range lines {
		if indexLineEntryID(line) == rid {
			rowIdx = i
			break
		}
	}
	if rowIdx < 0 {
		return content, false
	}
	row := lines[rowIdx]
	without := make([]string, 0, len(lines)-1)
	without = append(without, lines[:rowIdx]...)
	without = append(without, lines[rowIdx+1:]...)

	// A table row moves to the end of the last table; anything else (a
	// bullet-list entry) moves to the end of the file, which also makes it
	// the last parsed entry.
	if strings.HasPrefix(strings.TrimSpace(row), "|") {
		if end := lastTableEnd(without); end >= 0 {
			out := make([]string, 0, len(without)+1)
			out = append(out, without[:end]...)
			out = append(out, row)
			out = append(out, without[end:]...)
			return strings.Join(out, "\n"), true
		}
	}
	return appendEntryLine(without, row), true
}

// removeIndexEntryLines removes every index entry line referencing rid — both
// its table row and any bullet-list duplicates — so a deleted project leaves
// no trace in ParseIndex's result. Non-entry lines are preserved verbatim;
// content carrying no entry for rid is returned unchanged.
func removeIndexEntryLines(content, rid string) string {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		if indexLineEntryID(line) == rid {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return content
	}
	return strings.Join(kept, "\n")
}

// appendMinimalIndexRow appends a minimal index row for rid to index.md
// content that carries no row for it: a table row matching the existing
// table's column count when a table is present, else a bullet-list line; a
// blank/missing index is created with the canonical skeleton. The row links
// the project's brief so ParseIndex picks it up regardless of the surrounding
// format.
func appendMinimalIndexRow(content, rid, title, briefPath string) string {
	if strings.TrimSpace(content) == "" {
		return minimalIndexContent(rid, title, briefPath)
	}
	lines := strings.Split(content, "\n")
	if end := lastTableEnd(lines); end >= 0 {
		row := minimalTableRow(rid, title, fmt.Sprintf("[brief](%s)", briefPath), tableColumnCount(lines, end))
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:end]...)
		out = append(out, row)
		out = append(out, lines[end:]...)
		return strings.Join(out, "\n")
	}
	return appendEntryLine(lines, fmt.Sprintf("- [%s: %s](%s)", rid, title, briefPath))
}

// minimalIndexContent renders a fresh index.md for a research root that has
// none: the canonical "# Research Index" heading, the table skeleton from the
// research-init template, and rid's row as the single — last, therefore
// active — entry.
func minimalIndexContent(rid, title, briefPath string) string {
	row := minimalTableRow(rid, title, fmt.Sprintf("[brief](%s)", briefPath), 7)
	return "# Research Index\n\n" +
		"Active research projects following the Iterative Engineering Research\n" +
		"Methodology.\n\n" +
		indexTableHeader + "\n" +
		indexTableSeparator + "\n" +
		row + "\n"
}

// minimalTableRow renders a minimal table row for rid: the ID, the title, the
// brief link, and "—" placeholders in between so the row matches the table's
// column count (cols is clamped to at least 2).
func minimalTableRow(rid, title, link string, cols int) string {
	if cols < 2 {
		cols = 2
	}
	cells := make([]string, cols)
	for i := range cells {
		cells[i] = "—"
	}
	cells[0] = rid
	if cols == 2 {
		cells[1] = link
	} else {
		cells[1] = title
		cells[cols-1] = link
	}
	return joinCells(cells)
}

// appendEntryLine appends an entry line after the last non-blank line of an
// already line-split content, preserving the trailing newline (and adding one
// when the content had none).
func appendEntryLine(lines []string, row string) string {
	out := make([]string, len(lines), len(lines)+2)
	copy(out, lines)
	// Drop trailing blank lines; they are re-added after the row.
	trailing := 0
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
		trailing++
	}
	if trailing == 0 {
		trailing = 1 // the file still ends with a newline afterwards
	}
	out = append(out, row)
	for i := 0; i < trailing; i++ {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// lastTableEnd returns the index just past the last line of the last Markdown
// table in lines — a contiguous run of |-prefixed lines that contains a
// separator row — or -1 when no table exists. A |-prefixed run without a
// separator row is not a table and is skipped.
func lastTableEnd(lines []string) int {
	end := -1
	inTable := false
	hasSep := false
	closeBlock := func(i int) {
		if inTable && hasSep {
			end = i
		}
		inTable, hasSep = false, false
	}
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			closeBlock(i)
			continue
		}
		inTable = true
		if isSeparatorRow(splitCells(line)) {
			hasSep = true
		}
	}
	closeBlock(len(lines))
	return end
}

// tableColumnCount counts the columns of the table whose last line is at
// endIdx-1 (the block's first line is the header row). It returns at least 2.
func tableColumnCount(lines []string, endIdx int) int {
	start := endIdx - 1
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "|") {
		start--
	}
	if cols := len(splitCells(lines[start])); cols >= 2 {
		return cols
	}
	return 2
}

// projectBriefTitle reads a project directory's brief.md and returns its
// parsed title ("" when the brief is missing or carries no title) — used to
// render a human-readable minimal index row.
func projectBriefTitle(projectDir string) string {
	content, _ := readFile(filepath.Join(projectDir, "brief.md"))
	return ParseBrief(content).Title
}

// briefRelLink renders the index.md link target for a project's brief: the
// project directory's path relative to the research root plus brief.md, with
// forward slashes (Markdown link targets are URL paths, not host paths). It
// falls back to the directory's base name when the relative path cannot be
// computed.
func briefRelLink(researchRoot, projectDir string) string {
	rel, err := filepath.Rel(researchRoot, projectDir)
	if err != nil {
		rel = filepath.Base(projectDir)
	}
	return path.Join(filepath.ToSlash(rel), "brief.md")
}

// ---------------------------------------------------------------------------
// Delete containment
// ---------------------------------------------------------------------------

// resolveProjectDirWithinRoot symlink-resolves the research root and a project
// directory and verifies the project directory sits strictly INSIDE the root.
// pathutil.IsWithinPath treats the root itself as "within", so the equality
// case — which would make os.RemoveAll wipe the whole research tree — is
// rejected explicitly. It returns the resolved project directory: the
// on-disk location that was validated and may therefore be removed. It fails
// closed: an unresolvable path or any containment mismatch is an error, so a
// symlinked project directory can never redirect a deletion outside the
// workspace.
func resolveProjectDirWithinRoot(researchRoot, projectDir string) (string, error) {
	if researchRoot == "" {
		return "", errors.New("research delete rejected: empty research root")
	}
	resolvedRoot, err := resolveExistingDir(researchRoot)
	if err != nil {
		return "", fmt.Errorf("research root %q not resolvable: %w", researchRoot, err)
	}
	resolvedDir, err := resolveExistingDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("research project directory %q not resolvable: %w", projectDir, err)
	}
	if resolvedDir == resolvedRoot {
		return "", fmt.Errorf("refusing to delete the research root itself: %q", projectDir)
	}
	contained, err := pathutil.IsWithinPath(resolvedRoot, resolvedDir)
	if err != nil {
		return "", fmt.Errorf("research delete containment check failed for %q: %w", projectDir, err)
	}
	if !contained {
		return "", fmt.Errorf("research project directory %q resolves to %q, outside the research root %q (symlinked directory?)", projectDir, resolvedDir, researchRoot)
	}
	return resolvedDir, nil
}

// resolveExistingDir makes a directory path absolute and symlink-resolves it.
// Both operations fail for a missing path, so callers fail closed.
func resolveExistingDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
