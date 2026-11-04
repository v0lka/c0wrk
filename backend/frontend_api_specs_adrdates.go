package backend

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"regexp"
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

// specsADRDates returns the first-commit (add) dates of the specs/decisions/
// ADR files, keyed by the specs-root-relative path ("decisions/NNN-slug.md").
// ONE hardened git invocation (gitCmdTimeout ceiling via the caller's ctx):
// `git log --diff-filter=A --name-only --format=%as -- specs/decisions`.
//
// Fail-soft ALWAYS: a non-git workspace, a missing git binary, an unborn
// HEAD, an unscannable config (GitCmdInRepo fails closed there), and spawn
// or run errors all yield an empty map — the dates are environment
// metadata, never spec content, so their absence degrades the panel
// silently. Nothing is logged above Debug. See
// specs/domains/specs.md (ADR dates) and specs/contracts/desktop-frontend.md
// § Specs.
func specsADRDates(ctx context.Context, log *slog.Logger, workspaceRoot string) map[string]string {
	dates := make(map[string]string)
	if workspaceRoot == "" {
		return dates
	}
	cmd, err := workspace.GitCmdInRepo(ctx, workspaceRoot,
		"log", "--diff-filter=A", "--name-only", "--format=%as", "--", "specs/decisions")
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
	return parseADRDatesLog(stdout.String())
}

// adrDateKey normalizes a git-log path line to the specs-root-relative form
// specs.ADRMeta.File uses ("decisions/NNN-slug.md"): git C-style quoting is
// stripped (core.quotePath), and repo-root nesting before the specs/ tree —
// a workspace that is a repository subdirectory or a linked worktree — is
// dropped, so any repo-root-relative printed path keys identically.
func adrDateKey(gitPath string) string {
	path := unquoteGitPathLine(gitPath)
	if i := strings.LastIndex(path, "specs/"); i >= 0 {
		return path[i+len("specs/"):]
	}
	return path
}

// unquoteGitPathLine strips surrounding double quotes and C-style escapes
// from a git output path (git quotes paths when core.quotePath is on);
// unquoted paths pass through. Mirrors the workspace package's helper,
// which is unexported there.
func unquoteGitPathLine(path string) string {
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
	}
	return path
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

// enrichSpecsADRDates stamps each DTO ADR with its add date from git, in
// place, after the pure parse. One git invocation per GetSpecsStatus under
// the shared gitCmdTimeout ceiling, only when there are ADRs to date; every
// failure leaves the ADRs undated and the RPC successful (specsADRDates is
// fail-soft by contract).
func (f *FrontendAPI) enrichSpecsADRDates(ctx context.Context, dto *SpecsStatusDTO, workspaceRoot string) {
	if dto == nil || len(dto.ADRs) == 0 || workspaceRoot == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, gitCmdTimeout)
	defer cancel()
	dates := specsADRDates(ctx, f.log(), workspaceRoot)
	if len(dates) == 0 {
		return
	}
	for i := range dto.ADRs {
		if date, ok := dates[dto.ADRs[i].File]; ok {
			dto.ADRs[i].Date = date
		}
	}
}
