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

// autoAddPromptWorkDirs scans the user's prompt text for local filesystem
// paths and adds every existing directory as a session-scoped auxiliary work
// directory, so prose-mentioned directories become containment roots. Existing
// directories only; already-recorded and broadly-scoped paths (see
// [isSensitiveWorkDir]) are skipped; best-effort end to end — a failure never
// blocks the message. Emits one workdirs:changed when anything was added.
// See specs/architecture/security-model.md.
func (f *FrontendAPI) autoAddPromptWorkDirs(sessionID, text string) {
	f.seedAcquire()
	if f.store == nil || strings.TrimSpace(text) == "" {
		return
	}

	candidates := extractPromptPathCandidates(text)
	if len(candidates) == 0 {
		return
	}

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
			continue
		}
		if isSensitiveWorkDir(resolved) {
			continue
		}
		if _, ok := existing[resolved]; ok {
			continue
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

// isSensitiveWorkDir reports whether a path is too broad to add silently from
// a prompt mention: the filesystem/volume root, the home root, or a top-level
// system directory. Narrow subdirectories (/opt/build) stay auto-addable.
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

// extractPromptPathCandidates extracts local path-like tokens: absolute
// POSIX/Windows-drive paths via [sdktools.ExtractPaths] (which skips a "/"
// following a path-component character, so "frontend/src" yields nothing)
// plus home-relative "~/..." tokens expanded against the user's home.
// Cleaned but NOT existence-checked; callers filter via [resolveWorkDirPath].
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
