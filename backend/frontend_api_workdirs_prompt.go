package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// promptAutoWorkDirDescription is the description stamped on work-directory
// records auto-discovered from the user's prompt text. It distinguishes them
// from manually added directories in the UI list while remaining editable.
const promptAutoWorkDirDescription = "auto-detected from prompt"

// homeRelativeRe matches home-relative path tokens ("~", "~/path", "~user/path")
// in prompt text. The leading "~" is not an absolute path, so it is invisible
// to [sdktools.ExtractPaths] (which matches only "/abs" and drive-letter forms);
// this separate pass expands it against the current user's home directory.
var homeRelativeRe = regexp.MustCompile(`~[A-Za-z0-9_.\-]*(?:/[A-Za-z0-9/_.\-~]+)?`)

// autoAddPromptWorkDirs scans the user's prompt text for local filesystem paths
// and adds every existing directory as a session-scoped auxiliary working
// directory. This makes directories the user mentions in prose (e.g. "the build
// output is in /opt/build") automatically available as containment roots and
// appears in the system prompt's "Additional Work Directories" section.
//
// Only paths that exist on disk and are directories are added; non-existent
// paths and regular files are skipped. Paths already recorded for the session
// (deduplicated by canonical path) are not re-added. Broadly-scoped paths — the
// filesystem/volume root, the home-directory root, and top-level system
// directories — are skipped (see [isSensitiveWorkDir]) so a mere mention of
// "~", "/", "/etc", etc. does not silently make a whole subtree a containment
// root. The whole operation is best-effort: any per-path error is logged and
// skipped, and a failure never blocks the user's message. Emits a single
// workdirs:changed event when at least one directory was added.
func (f *FrontendAPI) autoAddPromptWorkDirs(sessionID, text string) {
	if f.store == nil || strings.TrimSpace(text) == "" {
		return
	}

	candidates := extractPromptPathCandidates(text)
	if len(candidates) == 0 {
		return
	}

	// Deduplicate against directories already recorded for the session so a
	// repeated mention (or a re-send) does not hit the unique-constraint error
	// or create redundant rows.
	existing := make(map[string]struct{})
	{
		ctx, cancel := context.WithTimeout(f.ctx(), 5*time.Second)
		recs, err := f.store.ListSessionWorkDirs(ctx, sessionID)
		cancel()
		if err != nil {
			f.log().Warn("auto-add: failed to list existing session work directories", "session", sessionID, "error", err)
		} else {
			for _, rec := range recs {
				existing[rec.Path] = struct{}{}
			}
		}
	}

	added := 0
	for _, c := range candidates {
		resolved, err := resolveWorkDirPath(c)
		if err != nil {
			continue // non-existent or not a directory — skip per the policy.
		}
		if isSensitiveWorkDir(resolved) {
			continue // broad system/home/root path — do not silently widen scope.
		}
		if _, ok := existing[resolved]; ok {
			continue // already recorded for this session.
		}
		ctx, cancel := context.WithTimeout(f.ctx(), 5*time.Second)
		err = f.store.SaveSessionWorkDir(ctx, sessionID, project.WorkDirectoryRecord{
			Path:        resolved,
			Description: promptAutoWorkDirDescription,
		})
		cancel()
		if err != nil {
			if errors.Is(err, project.ErrWorkDirAlreadyExists) {
				existing[resolved] = struct{}{}
				continue
			}
			f.log().Warn("auto-add: failed to save session work directory", "session", sessionID, "path", resolved, "error", err)
			continue
		}
		existing[resolved] = struct{}{}
		added++
	}

	if added > 0 {
		f.emitWorkDirsChanged()
	}
}

// isSensitiveWorkDir reports whether an auto-discovered work-directory path
// grants such broad access that it must never be added silently from a prompt
// mention: the filesystem/volume root, the user's home-directory root, or a
// top-level system directory. Narrowly-scoped subdirectories (e.g. /opt/build)
// are not in the set and remain auto-addable — the concern is only a mention
// of a path that would make an entire broad subtree a containment root.
func isSensitiveWorkDir(resolved string) bool {
	resolved = filepath.Clean(resolved)
	// Filesystem/volume root ("/", "C:\").
	if filepath.Dir(resolved) == resolved {
		return true
	}
	// Home-directory root (a bare "~" mention).
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if resolved == filepath.Clean(home) {
			return true
		}
	}
	// Top-level system directories (POSIX). Their immediate subdirectories are
	// intentionally NOT listed, so a specific path like /opt/build still passes.
	switch resolved {
	case "/etc", "/usr", "/var", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "/tmp",
		"/System", "/Library", "/private", "/opt", "/home", "/Users", "/Applications", "/Volumes":
		return true
	}
	return false
}

// extractPromptPathCandidates extracts local path-like tokens from prompt text:
// absolute POSIX/Windows-drive paths via [sdktools.ExtractPaths] (which skips a
// "/" that follows a path-component character, so embedded relative separators
// like the "/src" in "frontend/src" are not mistaken for absolute paths), plus
// home-relative "~/..." tokens expanded against the current user's home
// directory. Results are cleaned but NOT yet checked for existence; callers
// filter via [resolveWorkDirPath].
func extractPromptPathCandidates(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	for _, p := range sdktools.ExtractPaths(text) {
		add(p)
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, m := range homeRelativeRe.FindAllString(text, -1) {
			// Only expand the current user's home ("~/..."); "~user" forms
			// cannot be resolved portably and are skipped.
			if m == "~" || strings.HasPrefix(m, "~/") {
				add(filepath.Join(home, strings.TrimPrefix(m, "~")))
			}
		}
	}
	return out
}
