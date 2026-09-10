// Package gittrust holds the process-wide set of git repository roots the
// user has explicitly trusted (security.trusted_git_repos).
//
// Backend is the sole writer: it mirrors its loaded config into the registry
// on startup and updates the registry on TrustGitRepo / RemoveTrustedGitRepo.
// core/workspace consults the registry (via IsTrusted) to decide whether a
// repository may spawn raw git — bypassing the sysproc baseline, the per-repo
// NeutralizingArgv scan, and the GIT_EDITOR pin — so a trusted repository
// behaves exactly as it would outside c0wrk, hooks/filters/signing included.
//
// The registry is deliberately process-wide and empty by default: nothing is
// trusted until backend populates it from config, so a fresh process (and any
// test that does not opt in) fails closed into the hardened spawn path.
package gittrust

import (
	"path/filepath"
	"sync"
)

var (
	mu    sync.RWMutex
	roots = make(map[string]struct{})
)

// Trust adds root to the trusted set. Idempotent: trusting an already-trusted
// root is a no-op. The root is canonicalized with filepath.Clean before
// storage; callers are responsible for resolving the path to the repository's
// work-tree root (the same normalization the trust list applies, see
// workspace.ResolveWorkTreeRoot) so both sides of the comparison agree.
func Trust(root string) {
	root = filepath.Clean(root)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return
	}
	mu.Lock()
	roots[root] = struct{}{}
	mu.Unlock()
}

// Untrust removes root from the trusted set. Idempotent: removing an absent
// root is a no-op.
func Untrust(root string) {
	root = filepath.Clean(root)
	mu.Lock()
	delete(roots, root)
	mu.Unlock()
}

// IsTrusted reports whether root is present in the trusted set.
func IsTrusted(root string) bool {
	root = filepath.Clean(root)
	mu.RLock()
	_, ok := roots[root]
	mu.RUnlock()
	return ok
}

// Replace swaps the entire trusted set for the given roots (each
// canonicalized with filepath.Clean). It is the backend's mirror point: after
// loading or mutating security.trusted_git_repos, backend calls Replace with
// the full list so the registry never drifts from config. An empty or nil
// slice clears the set (fail-closed).
func Replace(trusted []string) {
	set := make(map[string]struct{}, len(trusted))
	for _, r := range trusted {
		if c := filepath.Clean(r); c != "" && c != "." && c != string(filepath.Separator) {
			set[c] = struct{}{}
		}
	}
	mu.Lock()
	roots = set
	mu.Unlock()
}

// Snapshot returns a copy of the currently trusted roots (canonicalized,
// unsorted). Used by tests and diagnostics; the returned slice is safe to
// mutate without affecting the registry.
func Snapshot() []string {
	mu.RLock()
	out := make([]string, 0, len(roots))
	for r := range roots {
		out = append(out, r)
	}
	mu.RUnlock()
	return out
}

// Clear removes every trusted root. Intended for tests, which share the
// process-wide registry and must reset it so one test's trust decision never
// leaks into another.
func Clear() {
	mu.Lock()
	roots = make(map[string]struct{})
	mu.Unlock()
}
