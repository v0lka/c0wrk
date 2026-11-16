package backend

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// ADR add dates
// ---------------------------------------------------------------------------

// adrDateLine matches a bare `git log --format=%as` date line (YYYY-MM-DD).
// A path line never matches: every decision/ path carries directory
// segments or a slug tail beyond the bare date shape.
var adrDateLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parseADRDatesLog parses `git log --diff-filter=A --name-only
// --format=%as` output into path→add-date. Each commit block starts with
// its bare YYYY-MM-DD date line, followed by one changed-path line per
// added file; blank lines separate blocks. git log is newest-first, so the
// FIRST date seen for a path is its add date (the newest of its add events
// when a path was deleted and re-added). Path lines before any date header
// are skipped — they cannot be dated.
func parseADRDatesLog(output string) map[string]string {
	dates := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(output))
	current := ""
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case adrDateLine.MatchString(line):
			current = line
		case line != "":
			if current == "" {
				continue // path block without a date header — cannot date it
			}
			key := adrDateKey(line)
			if _, seen := dates[key]; !seen {
				dates[key] = current
			}
		}
	}
	return dates
}

// specsADRDates returns the first-commit (add) dates of the decision
// documents, keyed by the FULL workspace-relative path ("specs/decisions/
// NNN-slug.md" for the built-in framework, "decisions/NNN-slug.md" for an
// operator root). pathspecs names the decision directories to query, in their
// workspace-relative spelling; git prints repo-root-relative paths, and
// remapADRDates rekeys them onto the workspace spellings — so a custom
// `decisions/` root and the built-in `specs/decisions/` tree key distinctly
// instead of collapsing into one lookup slot.
//
// ONE hardened git invocation (gitCmdTimeout ceiling via the caller's ctx):
// `git log --diff-filter=A --name-only --format=%as -- <dirs>`.
//
// Fail-soft ALWAYS: a non-git workspace, a missing git binary, an unborn
// HEAD, an unscannable config (GitCmdInRepo fails closed there), and spawn
// or run errors all yield an empty map — the dates are environment
// metadata, never spec content, so their absence degrades the panel
// silently. Nothing is logged above Debug. See
// specs/domains/specs.md (ADR dates) and specs/contracts/desktop-frontend.md
// § Specs.
func specsADRDates(ctx context.Context, log *slog.Logger, workspaceRoot string, pathspecs []string) map[string]string {
	dates := make(map[string]string)
	if workspaceRoot == "" || len(pathspecs) == 0 {
		return dates
	}
	args := make([]string, 0, len(pathspecs)+5)
	args = append(args, "log", "--diff-filter=A", "--name-only", "--format=%as", "--")
	args = append(args, pathspecs...)
	cmd, err := workspace.GitCmdInRepo(ctx, workspaceRoot, args...)
	if err != nil {
		logADRDatesUnavailable(log, workspaceRoot, "preparing git log", err)
		return dates
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		logADRDatesUnavailable(log, workspaceRoot, "git log", err)
		return dates
	}
	return remapADRDates(parseADRDatesLog(stdout.String()), pathspecs)
}

// remapADRDates rekeys the git-log paths (repo-root-relative) onto the
// workspace-relative spellings the DTO carries. git prints paths relative to
// the repository root while the pathspecs were workspace-relative
// (cmd.Dir = workspace), so every printed path is <repo prefix>/<pathspec>/
// <tail>; the longest pathspec wins the match, so a nested decision directory
// cannot be stolen by a broader one. A path matching no pathspec (a merge
// oddity, a rename record) is dropped — fail-soft, like every other stage.
func remapADRDates(raw map[string]string, pathspecs []string) map[string]string {
	ordered := slices.Clone(pathspecs)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	dates := make(map[string]string, len(raw))
	for repoPath, date := range raw {
		for _, spec := range ordered {
			marker := "/" + spec + "/"
			prefixed := "/" + repoPath
			if idx := strings.LastIndex(prefixed, marker); idx >= 0 {
				dates[spec+"/"+prefixed[idx+len(marker):]] = date
				break
			}
		}
	}
	return dates
}

// adrDateKey normalizes a git-log path line to the enrichment's lookup key:
// git C-style quoting is stripped (core.quotePath). No folding happens HERE —
// git prints repo-root-relative paths (no --relative flag is passed), so
// remapADRDates has already rekeyed them onto the workspace-relative
// spellings the documents carry. Folding further to a specs-relative form
// would collide a custom decisions/ root with the built-in specs/decisions/
// tree (two documents, one lookup key).
func adrDateKey(gitPath string) string {
	return unquoteGitPathLine(gitPath)
}

// unquoteGitPathLine strips surrounding double quotes and C-style escapes
// from a git output path (git quotes paths when core.quotePath is on);
// unquoted paths pass through. Mirrors the workspace package's helper,
// which is unexported there.
func unquoteGitPathLine(line string) string {
	if len(line) >= 2 && line[0] == '"' && line[len(line)-1] == '"' {
		if unquoted, err := strconv.Unquote(line); err == nil {
			return unquoted
		}
	}
	return line
}

// logADRDatesUnavailable records WHY the ADR dates are absent at Debug —
// expected on every non-git workspace, so it must never spam Warn/Error.
func logADRDatesUnavailable(log *slog.Logger, workspaceRoot, stage string, err error) {
	if log == nil {
		return
	}
	log.Debug("specs panel: ADR add dates unavailable",
		"workspace", workspaceRoot, "stage", stage, "error", err)
}

// ---------------------------------------------------------------------------
// DTO enrichment
// ---------------------------------------------------------------------------

// enrichSpecsDocumentDates stamps decision documents with their git add date,
// in place, after the pure parse. The SDD parser is date-free by design — git
// history is environment metadata, never spec content — so the backend adds it
// here. One git invocation per GetSpecsStatus under the shared gitCmdTimeout
// ceiling, only when the tree carries decision documents; every failure leaves
// the dates absent and the RPC successful (specsADRDates is fail-soft).
func (f *FrontendAPI) enrichSpecsDocumentDates(ctx context.Context, docs []SDDDocumentDTO, workspaceRoot string) {
	if len(docs) == 0 || workspaceRoot == "" || !hasDecisionDocuments(docs) {
		return
	}
	// The query covers each decision document's own directory, in the
	// workspace-relative spelling the DTO carries: the built-in
	// specs/decisions/ tree and an operator's custom decisions/ root are two
	// distinct pathspecs of the same bounded call, never one shared prefix.
	pathspecs := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	for _, doc := range docs {
		if !isDecisionDocument(doc) {
			continue
		}
		dir := path.Dir(doc.Path)
		if !seen[dir] {
			seen[dir] = true
			pathspecs = append(pathspecs, dir)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, gitCmdTimeout)
	defer cancel()
	dates := specsADRDates(ctx, f.log(), workspaceRoot, pathspecs)
	if len(dates) == 0 {
		return
	}
	for i := range docs {
		if !isDecisionDocument(docs[i]) {
			continue
		}
		if date, ok := dates[docs[i].Path]; ok {
			docs[i].Date = date
		}
	}
}

// isDecisionDocument reports whether the framework itself classifies the
// document as a decision: the DSL concept id is the semantic signal, not the
// path shape. An operator framework may root its decisions anywhere (the
// built-in tree happens to use specs/decisions/), and a non-decision concept
// must not inherit dates from a decisions/-shaped directory.
func isDecisionDocument(doc SDDDocumentDTO) bool {
	return doc.Concept == "decision"
}

func hasDecisionDocuments(docs []SDDDocumentDTO) bool {
	for _, doc := range docs {
		if isDecisionDocument(doc) {
			return true
		}
	}
	return false
}
