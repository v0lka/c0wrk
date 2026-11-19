// Package gittrust holds the process-wide set of git repository roots the user
// has explicitly trusted (security.trusted_git_repos), each bound to the
// semantic git-config fingerprint the trust decision was captured against.
// Backend mirrors its config into the registry; core/workspace consults it to
// decide whether a repository may spawn raw git — re-checking the bound
// fingerprint so a config that drifted after the decision cannot ride the
// trust — and an empty registry fails closed into the hardened spawn path.
// See specs/architecture/security-model.md (layer 5) and
// specs/decisions/034-git-trust-opt-out.md / 070-git-semantic-trust.md.
package gittrust

import (
	"path/filepath"
	"sync"
)

var (
	mu           sync.RWMutex
	roots        = make(map[string]string) // spelling → semantic fingerprint ("" = no recorded identity)
	driftHandler func(root string)
)

// Entry is one mirrored trusted repository: the registry root (the form
// spawn-layer probes match exactly) plus the semantic fingerprint the trust
// decision bound at TrustGitRepo time. An empty SemanticFingerprint marks an
// entry whose decision cannot be verified (a legacy record awaiting its
// next-open migration): the spawn layer refuses raw git for it, fail-closed.
type Entry struct {
	Root                string
	SemanticFingerprint string
}

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

// Trust adds root (both spellings) to the trusted set without a semantic
// fingerprint; callers must pass the repository trust root resolved via
// workspace.ResolveGitTrustRoot. The spawn layer treats a fingerprint-less
// entry as an unverifiable decision and keeps the hardened path — tests that
// exercise raw spawns must register through TrustWithFingerprint (or
// ReplaceEntries) instead.
func Trust(root string) {
	TrustWithFingerprint(root, "")
}

// TrustWithFingerprint registers root (both spellings) bound to the given
// semantic fingerprint — the form the backend's registry mirror produces.
func TrustWithFingerprint(root, semanticFingerprint string) {
	root = filepath.Clean(root)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return
	}
	mu.Lock()
	for _, spelling := range spellings(root) {
		roots[spelling] = semanticFingerprint
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

// SemanticFingerprint returns the semantic git-config fingerprint bound to
// root's trust entry (either spelling). ok=false when root is not registered
// at all; ok=true with an empty fingerprint marks a registered entry without
// a verifiable identity — callers must keep the hardened path for it.
func SemanticFingerprint(root string) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, spelling := range spellings(root) {
		if fp, ok := roots[spelling]; ok {
			return fp, true
		}
	}
	return "", false
}

// Replace swaps the entire trusted set for the given roots — the backend's
// single mirror point, called with the full list after loading or mutating
// security.trusted_git_repos. An empty or nil slice clears the set
// (fail-closed). Entries registered this way carry no semantic fingerprint:
// production callers mirror through ReplaceEntries instead.
func Replace(trusted []string) {
	entries := make([]Entry, 0, len(trusted))
	for _, r := range trusted {
		entries = append(entries, Entry{Root: r})
	}
	ReplaceEntries(entries)
}

// ReplaceEntries swaps the entire trusted set for the given entries — the
// backend's single mirror point for roots AND their semantic fingerprints,
// called with the full list after loading or mutating
// security.trusted_git_repos. An empty or nil slice clears the set
// (fail-closed).
func ReplaceEntries(entries []Entry) {
	set := make(map[string]string, 2*len(entries))
	for _, e := range entries {
		c := filepath.Clean(e.Root)
		if c == "" || c == "." || c == string(filepath.Separator) {
			continue
		}
		for _, spelling := range spellings(c) {
			set[spelling] = e.SemanticFingerprint
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

// SetDriftHandler registers the single callback the spawn layer's drift
// eviction invokes (the backend: remove the config record and re-emit the
// risk warning, so the next registry sync cannot resurrect the evicted
// trust). nil clears. The handler runs OUTSIDE the registry lock and must
// not re-enter EvictDrifted.
func SetDriftHandler(fn func(root string)) {
	mu.Lock()
	driftHandler = fn
	mu.Unlock()
}

// EvictDrifted evicts a repository the spawn layer caught drifting from its
// recorded trust decision (both spellings) and reports the drift to the
// registered handler outside the registry lock — the handler mirrors config
// state and may re-enter Replace. The registry eviction stands even with no
// handler: raw git refuses immediately, fail-closed.
func EvictDrifted(root string) {
	Untrust(root)
	mu.RLock()
	fn := driftHandler
	mu.RUnlock()
	if fn != nil {
		fn(filepath.Clean(root))
	}
}

// Clear removes every trusted root. For tests sharing the process-wide
// registry, so one test's trust decision never leaks into another. Tests
// registering a drift handler must clear it separately via
// SetDriftHandler(nil).
func Clear() {
	mu.Lock()
	roots = make(map[string]string)
	mu.Unlock()
}
