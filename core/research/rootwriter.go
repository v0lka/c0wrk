// Package research — root-level writer layer: the research-root mutations
// ABOVE a single project's hypothesis graph — activating a project (index.md
// row moved to last, the "last entry = active" rule) and deleting a project
// (index rows + R-NNN-* tree removed). Like writer.go, both read first,
// compute in memory, and write through writeFilesAtomic, so a failure leaves
// index.md byte-for-byte unchanged.
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

// SetActiveResearch makes rid the active research project by moving its
// index.md row to the last table entry — the chronological rule
// PickActiveProject applies. A missing file/row gets the minimal
// brief-linking row (canonical skeleton when the file is absent); rid must
// resolve under researchRoot (ProjectDir ownership check) before any file is
// touched. Full contract: specs/domains/research.md.
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
	// Fail closed on an unreadable index: "" would rewrite index.md as a
	// one-row skeleton, dropping every other project's row.
	current, err := readFileStrict(indexPath)
	if err != nil {
		return fmt.Errorf("failed to read research index: %w", err)
	}

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

// DeleteResearchProject removes every index.md entry line referencing rid,
// then rid's R-NNN-* directory tree in full. The resolved project directory
// must sit strictly inside the resolved research root — equality with the
// root, or any escape, is rejected before anything is touched (full contract:
// specs/domains/research.md). The atomic index rewrite precedes the removal,
// so an index failure leaves the tree untouched, and a removal failure leaves
// a stale row the best-effort parser tolerates; os.RemoveAll unlinks
// symlinked children rather than chasing them.
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

	// 1. Drop every index entry line for rid (atomic; skipped when none).
	// readFileStrict, not the tolerant readFile: a present-but-unreadable
	// index must abort the deletion rather than leave a dangling index row
	// pointing at a removed project.
	indexPath := filepath.Join(researchRoot, "index.md")
	current, readErr := readFileStrict(indexPath)
	if readErr != nil {
		return fmt.Errorf("failed to read research index: %w", readErr)
	}
	if next := removeIndexEntryLines(current, rid); next != current {
		if err := writeFilesAtomic(researchRoot, map[string][]byte{indexPath: []byte(next)}); err != nil {
			return fmt.Errorf("failed to update research index: %w", err)
		}
	}

	// 2. Remove the project tree at the resolved, containment-checked location.
	if err := os.RemoveAll(resolvedDir); err != nil {
		return fmt.Errorf("failed to remove research project directory %q: %w", projectDir, err)
	}
	return nil
}

// -- index.md content helpers (pure) --

// indexTableHeader and indexTableSeparator are the canonical index.md table
// skeleton (the research-init template; see testdata/index.md).
const (
	indexTableHeader    = "| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |"
	indexTableSeparator = "|---|---|---|---|---|---|---|"
)

// indexLineEntryID returns the research ID a single index.md line contributes
// to ParseIndex ("": not an entry line). It reuses ParseIndex so "what counts
// as an entry" stays defined in exactly one place — a brief.md link (ID from
// target, else text) or a bare R-NNN token.
func indexLineEntryID(line string) string {
	entries := ParseIndex(line)
	if len(entries) == 0 {
		return ""
	}
	return entries[0].ID
}

// moveIndexRowToTableEnd rewrites index.md so rid's row becomes the last row
// of its table — the entry order PickActiveProject reads. The FIRST line
// referencing rid moves (it fixes the parser's de-duplicated position);
// later duplicates stay. A table row moves to the end of the last table, a
// bullet entry to the end of the file. Returns the new content and whether
// a row was found.
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

	// A table row moves to the end of the last table; a bullet entry moves to
	// the end of the file (also making it the last parsed entry).
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

// removeIndexEntryLines removes every index entry line referencing rid (table
// row and bullet duplicates); non-entry lines are preserved verbatim, and
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

// appendMinimalIndexRow appends a minimal brief-linking row for rid: a table
// row matching the existing table's column count when one exists, else a
// bullet line; a blank index is created with the canonical skeleton.
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

// minimalIndexContent renders a fresh index.md: canonical heading, the
// research-init table skeleton, rid's row as the single (active) entry.
func minimalIndexContent(rid, title, briefPath string) string {
	row := minimalTableRow(rid, title, fmt.Sprintf("[brief](%s)", briefPath), 7)
	return "# Research Index\n\n" +
		"Active research projects following the Iterative Engineering Research\n" +
		"Methodology.\n\n" +
		indexTableHeader + "\n" +
		indexTableSeparator + "\n" +
		row + "\n"
}

// minimalTableRow renders a minimal row for rid (ID, title, brief link, "—"
// placeholders; cols clamped to at least 2).
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

// appendEntryLine appends an entry line after the last non-blank line,
// preserving (or adding) the trailing newline.
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

// lastTableEnd returns the index just past the last Markdown table (a run of
// |-prefixed lines containing a separator row), or -1 when none exists.
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

// tableColumnCount counts the columns of the table ending at endIdx-1;
// at least 2.
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

// projectBriefTitle returns the parsed brief.md title ("" when absent) for a
// human-readable minimal index row.
func projectBriefTitle(projectDir string) string {
	content, _ := readFile(filepath.Join(projectDir, "brief.md"))
	return ParseBrief(content).Title
}

// briefRelLink renders the index.md link target for a project's brief:
// root-relative, forward slashes (Markdown targets are URL paths); falls
// back to the base name when the relative path cannot be computed.
func briefRelLink(researchRoot, projectDir string) string {
	rel, err := filepath.Rel(researchRoot, projectDir)
	if err != nil {
		rel = filepath.Base(projectDir)
	}
	return path.Join(filepath.ToSlash(rel), "brief.md")
}

// -- Delete containment --

// resolveProjectDirWithinRoot symlink-resolves root and project directory and
// verifies the directory sits strictly INSIDE the root: IsWithinPath treats
// the root itself as "within", so the equality case — which would make
// os.RemoveAll wipe the whole research tree — is rejected explicitly. Returns
// the validated on-disk location; fails closed on any mismatch.
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
