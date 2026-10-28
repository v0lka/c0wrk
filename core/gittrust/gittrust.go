// Package gittrust holds the process-wide set of git repository roots the user
// has explicitly trusted (security.trusted_git_repos). Backend mirrors its
// config into the registry; core/workspace consults it to decide whether a
// repository may spawn raw git; an empty registry fails closed into the
// hardened spawn path. See specs/architecture/security-model.md (layer 5).
package gittrust

import (
	"path/filepath"
	"sync"
)

var (
	mu    sync.RWMutex
	roots = make(map[string]struct{})
)

// spellings returns the cleaned path plus its symlink-resolved spelling (when
// different): user-facing storage keeps the lexical form, git's worktree
// pointers carry canonical paths (macOS /var → /private/var) — one root, two forms.
func spellings(root string) []string {
	cleaned := filepath.Clean(root)
	out := []string{cleaned}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != cleaned {
		out = append(out, resolved)
	}
	return out
}

// Trust adds root (both spellings) to the trusted set; callers must pass the
// repository trust root resolved via workspace.ResolveGitTrustRoot.
func Trust(root string) {
	root = filepath.Clean(root)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return
	}
	mu.Lock()
	for _, spelling := range spellings(root) {
		roots[spelling] = struct{}{}
	}
	mu.Unlock()
}

// Untrust removes root from the trusted set (both spellings).
func Untrust(root string) {
	mu.Lock()
	for _, spelling := range spellings(root) {
		delete(roots, spelling)
	}
	mu.Unlock()
}

// IsTrusted reports whether root is trusted under either spelling.
func IsTrusted(root string) bool {
	mu.RLock()
	defer mu.RUnlock()
	for _, spelling := range spellings(root) {
		if _, ok := roots[spelling]; ok {
			return true
		}
	}
	return false
}

// Replace swaps the entire trusted set for the given roots — the backend's
// single mirror point, called with the full list after loading or mutating
// security.trusted_git_repos. An empty or nil slice clears the set (fail-closed).
func Replace(trusted []string) {
	set := make(map[string]struct{}, 2*len(trusted))
	for _, r := range trusted {
		c := filepath.Clean(r)
		if c == "" || c == "." || c == string(filepath.Separator) {
			continue
		}
		for _, spelling := range spellings(c) {
			set[spelling] = struct{}{}
		}
	}
	mu.Lock()
	roots = set
	mu.Unlock()
}

// Snapshot returns a copy of the trusted roots (canonicalized, unsorted);
// the slice is safe to mutate.
func Snapshot() []string {
	mu.RLock()
	out := make([]string, 0, len(roots))
	for r := range roots {
		out = append(out, r)
	}
	mu.RUnlock()
	return out
}

// Clear removes every trusted root. For tests sharing the process-wide
// registry, so one test's trust decision never leaks into another.
func Clear() {
	mu.Lock()
	roots = make(map[string]struct{})
	mu.Unlock()
}
