package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
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
// paths and silently adds the ones that do NOT widen the session's containment
// as session-scoped auxiliary work directories: a candidate is added only when
// it lies inside the session's workspace or inside an already-approved session
// root (explicitly added by the user, or previously recorded). Everything else
// — an unrelated home directory, a hidden state/credential store (~/.ssh,
// ~/.c0wrk), an arbitrary absolute path — is left to the user's explicit
// action in the UI: prompt text is untrusted input (a repository can plant a
// command template whose body mentions credential directories — ADR-096
// templates are expanded and sent without a preview), so a mention must never
// become a read root on its own. Existing directories only; already-recorded
// and broadly-scoped paths (see [isSensitiveWorkDir]) are skipped; best-effort
// end to end — a failure never blocks the message. Emits one workdirs:changed
// when anything was added. See specs/architecture/security-model.md.
func (f *FrontendAPI) autoAddPromptWorkDirs(sessionID, workspaceRoot, text string) {
	f.seedAcquire()
	if f.store == nil || strings.TrimSpace(text) == "" {
		return
	}

	candidates := extractPromptPathCandidates(text)
	if len(candidates) == 0 {
		return
	}

	// approved holds the containment roots recorded BEFORE this message: the
	// eligibility check runs against this fixed set, so an auto-added path
	// can never authorize a further candidate inside it within the same pass
	// (no chained widening from one prompt).
	approved := make(map[string]struct{})
	existing := make(map[string]struct{})
	{
		ctx, cancel := context.WithTimeout(f.ctx(), 5*time.Second)
		recs, err := f.store.ListSessionWorkDirs(ctx, sessionID)
		cancel()
		if err != nil {
			f.log().Warn("auto-add: failed to list existing session work directories", "session", sessionID, "error", err)
		} else {
			for _, rec := range recs {
				approved[rec.Path] = struct{}{}
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
		if !promptAutoAddAllowed(resolved, workspaceRoot, approved) {
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

// promptAutoAddAllowed reports whether a resolved candidate may be added
// silently from prompt text: only when it lies inside the session's workspace
// or inside an already-approved session root (equal counts as within — the
// candidate itself is already a containment root). Containment goes through
// the centralized path primitive; a containment error resolves to "not
// allowed" (fail-closed).
func promptAutoAddAllowed(candidate, workspaceRoot string, approved map[string]struct{}) bool {
	if withinRoot(workspaceRoot, candidate) {
		return true
	}
	for root := range approved {
		if withinRoot(root, candidate) {
			return true
		}
	}
	return false
}

// withinRoot reports whether child lies within parent; an empty parent never
// contains anything.
func withinRoot(parent, child string) bool {
	if parent == "" {
		return false
	}
	ok, err := config.IsWithinPath(parent, child)
	return err == nil && ok
}

// isSensitiveWorkDir reports whether a path is too broad or too sensitive to
// add silently from a prompt mention: the filesystem/volume root, the home
// root, a hidden directory directly under home (state/credential stores by
// convention — ~/.ssh, ~/.c0wrk, ~/.aws, ~/.gnupg, ~/.kube, ~/.config), or a
// top-level system directory. Deeper paths are governed by
// [promptAutoAddAllowed]: nothing outside the workspace and already-approved
// roots is eligible at all.
func isSensitiveWorkDir(resolved string) bool {
	resolved = filepath.Clean(resolved)
	// Filesystem/volume root ("/", "C:\").
	if filepath.Dir(resolved) == resolved {
		return true
	}
	// The home root and hidden directories directly under it: the hidden ones
	// are state/credential stores by convention, and a prompt mention (or a
	// planted template mentioning them) must never silently turn one into a
	// session read root.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = filepath.Clean(home)
		if resolved == home {
			return true
		}
		if filepath.Dir(resolved) == home && strings.HasPrefix(filepath.Base(resolved), ".") {
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
