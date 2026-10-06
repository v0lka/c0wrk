package backend

// Git-config intake warnings: when a project switch or an auxiliary
// work-directory add opens a repository whose .git/config carries dangerous
// keys, the backend scans it with the exec-free parser in core/workspace
// (ScanGitConfig) and emits the global project:git_config_risk event so the
// frontend can warn the user about the workspace before agent work starts.
//
// The scan is pure text parsing — it never spawns git and never follows
// include directives. This path is detection only: actual neutralization
// happens in the git spawn layer (the unconditional sysproc.GitCmd baseline
// plus the per-repo NeutralizingArgv set), not in the UI. A clean, fully
// visible config emits nothing; anything that makes the config suspicious
// (dangerous keys, include directives, malformed or unreadable config) fails
// closed into a warning — the same fail-closed signal as GitConfigInfo.Clean.
// While the blanket attribute-interpretation kill (attr.tree) is part of the
// derived neutralization — the include case, where driver names may be
// hidden from the scan — the warning also discloses that activation and its
// collateral: benign eol/CRLF normalization is off, so text files may be
// reported as falsely modified (review [56]).
//
// Repositories the user explicitly trusted (security.trusted_git_repos,
// maintained by the TrustGitRepo / RemoveTrustedGitRepo / GetTrustedGitRepos
// RPCs below) skip the warning; the trust decision also registers the root in
// the process-wide core/gittrust registry, so the spawn layer runs raw git
// for it (its own hooks, filters and signing apply — see sysproc.GitCmdRaw).
// Untrusted and hardened repositories keep the full neutralization.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/core/workspace"

	"github.com/v0lka/sp4rk/safeio"
)

// Source values carried in a project:git_config_risk payload — what was
// opened when the suspicious config was found.
const (
	GitConfigRiskSourceProject = "project"
	GitConfigRiskSourceWorkdir = "workdir"
)

// gitConfigRiskNotice is the standing safety note carried with every warning.
// It states explicitly that repository-defined hooks never run inside c0wrk
// and that the config-driven programs listed in the findings are blocked or
// neutralized on every git invocation c0wrk makes — including the Git
// panel's remote operations (pull/push/fetch), whose transport keys
// (core.sshCommand, core.askPass, credential helpers) carry their own
// neutralizations. The warning informs without implying the config executed.
const gitConfigRiskNotice = "Repository-defined git hooks do not run inside c0wrk: the config-driven programs listed below are blocked or neutralized on every git invocation c0wrk makes, remote operations (pull, push, fetch) included. Continue only if you trust this repository."

// gitConfigDriftReason is the Reason carried when a previously-trusted
// repository's command-bearing git configuration changed semantically since
// the trust decision (any change outside the inert branch/alias allowlist —
// see workspace.SemanticFingerprint; inert branch/alias bookkeeping never
// fires this event). The trust was evicted and the repository returned to
// the hardened default.
const gitConfigDriftReason = "This repository was previously trusted, but its command-bearing git configuration changed since you trusted it — the diff below shows the security-relevant changes only (routine branch-tracking and alias bookkeeping never triggers this warning). The trust has been revoked and the repository is hardened again; re-trust it only if the change is expected."

// gitConfigTrustUnverifiableReason is the Reason carried when a previously
// trusted repository's trust record could not be verified against the trust
// model — its v1 record could not be migrated to the semantic fingerprint
// (missing fingerprint, or a snapshot that is missing, corrupted, or not a
// snapshot this scanner wrote). The trust cannot be carried over, so it is
// revoked fail-closed; re-trusting re-captures a fresh snapshot.
const gitConfigTrustUnverifiableReason = "This repository was previously trusted, but the git-config snapshot recorded at trust time could no longer be read or interpreted, so the trust cannot be carried over. The trust has been revoked and the repository is hardened again; review the repository's current git configuration and re-trust it only if it is the configuration you originally trusted."

// GitConfigRiskFinding is one detected danger, as delivered to the frontend.
type GitConfigRiskFinding struct {
	// Key is the full dotted config key ("core.fsmonitor",
	// "filter.lfs.process") or a synthetic marker for dangers that are not a
	// single key ("(include directive)", "(config unreadable)").
	Key string `json:"key"`
	// Description is a human-readable explanation of the vector and how
	// c0wrk handles it (produced by the core/workspace scanner).
	Description string `json:"description"`
}

// GitConfigRiskData is the payload of the global project:git_config_risk event.
type GitConfigRiskData struct {
	// Path is the repository WORK-TREE ROOT the warning is attributed to
	// (resolved from the scanned project workspace or work directory via
	// workspace.ResolveWorkTreeRoot, review [52] — the scan walks up to the
	// parent repository, so attribution and trust key on its root).
	Path string `json:"path"`
	// Source is "project" (project switch) or "workdir" (added auxiliary
	// working directory).
	Source string `json:"source"`
	// Notice is the fixed hooks-do-not-run safety note.
	Notice string `json:"notice"`
	// Findings lists every detected key / danger. Never empty when the
	// event fires.
	Findings []GitConfigRiskFinding `json:"findings"`
	// Reason is set when the warning fired for a repository that was
	// previously trusted but whose configuration changed since the trust
	// decision — the trust was evicted and the repository returned to the
	// hardened default. Empty for ordinary first-time intake warnings.
	Reason string `json:"reason,omitempty"`
	// Diff is a human-readable diff between the trusted snapshot and the
	// current configuration ("" for ordinary first-time warnings).
	Diff string `json:"diff,omitempty"`
}

// gitRepoTrusted reports whether path has been marked trusted by the user
// (security.trusted_git_repos, maintained by the TrustGitRepo RPC). Both the
// stored entries and the probe are compared as filepath.Clean-ed strings —
// an exact match, deliberately without prefix/subtree semantics: trust is per
// repository root, so a decision for one root must never silently cover a
// different repository nested inside it (or the parent it lives in). Since
// review [52] both sides are the repository WORK-TREE ROOT: callers pass the
// root resolved by workspace.ResolveWorkTreeRoot, and TrustGitRepo stores
// that same form. With no config loaded nothing is trusted (fail-closed: the
// warning stays on).
func (f *FrontendAPI) gitRepoTrusted(path string) bool {
	_, ok := f.trustedGitRepoEntry(path)
	return ok
}

// trustedGitRepoEntry returns a COPY of the trusted entry for path (the same
// exact filepath.Clean-ed WORK-TREE ROOT match as gitRepoTrusted), or ok=false
// when the path is not trusted. Unlike gitRepoTrusted it exposes the entry —
// and therefore its Fingerprint — so notifyGitConfigRisk can recheck the stored
// snapshot against the current scan instead of trusting blindly. A value copy
// is returned so the caller never holds a pointer into the mutex-guarded slice.
func (f *FrontendAPI) trustedGitRepoEntry(path string) (config.TrustedGitRepo, bool) {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return config.TrustedGitRepo{}, false
	}
	cleaned := filepath.Clean(path)
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(trusted.Path) == cleaned {
			return trusted, true
		}
	}
	return config.TrustedGitRepo{}, false
}

// writeGitConfigSnapshot stores the snapshot bytes under
// ~/.c0wrk/git-config-snapshots/<fingerprint> (content-addressed, so identical
// snapshots share one file). The directory is created lazily. Returns an error
// when the agent dir is unset — production always sets it; tests that trust a
// repo must too.
func (f *FrontendAPI) writeGitConfigSnapshot(fingerprint string, snapshot []byte) error {
	if f.agentDir == "" {
		return errors.New("agent dir not set")
	}
	dir := config.GitConfigSnapshotsDir(f.agentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fingerprint), snapshot, 0o644)
}

// readGitConfigSnapshot returns the previously-stored snapshot bytes for a
// fingerprint, or an error when none is stored (or the agent dir is unset).
func (f *FrontendAPI) readGitConfigSnapshot(fingerprint string) ([]byte, error) {
	if f.agentDir == "" {
		return nil, errors.New("agent dir not set")
	}
	return safeio.ReadFile(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), fingerprint))
}

// syncGitTrustRegistry mirrors security.trusted_git_repos into the
// process-wide core/gittrust registry, which core/workspace consults to
// decide whether a repository may spawn raw git (the trust decision now opts
// a repository back into its own hooks, filters, and signing — see
// sysproc.GitCmdRaw). HardenGitRepos entries are never trusted (config
// validation enforces mutual exclusion), so they are naturally absent from
// the registry and stay hardened. The caller must hold configMu when calling
// from a concurrent context (TrustGitRepo / RemoveTrustedGitRepo hold it;
// NewFrontendAPI calls at construction time, before the API is published).
func (f *FrontendAPI) syncGitTrustRegistry() {
	if f.config == nil {
		gittrust.Replace(nil)
		return
	}
	paths := make([]string, 0, len(f.config.Security.TrustedGitRepos))
	for _, r := range f.config.Security.TrustedGitRepos {
		paths = append(paths, r.Path)
	}
	gittrust.Replace(paths)
}

// GetTrustedGitRepos returns the repository roots the user has marked
// trusted (their untrusted-git-config intake warning is suppressed). The
// slice is a defensive copy — callers cannot mutate the live config through
// the RPC boundary — and is never nil so JSON serializes as [].
func (f *FrontendAPI) GetTrustedGitRepos() []string {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return []string{}
	}
	out := make([]string, 0, len(f.config.Security.TrustedGitRepos))
	for _, trusted := range f.config.Security.TrustedGitRepos {
		out = append(out, trusted.Path)
	}
	return out
}

// TrustGitRepo marks a repository root as trusted: the
// "untrusted git configuration detected" intake warning is no longer emitted
// for it, and the spawn layer runs raw git for it (its own hooks, filters and
// signing apply — see syncGitTrustRegistry). The path must be an existing
// absolute directory. What is stored is the WORK-TREE ROOT resolved from that
// path (review [52]): the intake warning is attributed to the repository root
// the scan discovered (the scan walks up from subdirectory workspaces), so
// trust must key on the same root — trusting any path inside a repository
// trusts that whole repository, and a future open of the root (or any other
// subdirectory of it) stays silent, exactly as ADR-033 documents. When no
// repository is discoverable from the path, the cleaned path itself is stored
// (the fail-closed pairing with the warning's fallback attribution).
//
// The trust decision is bound to two identities of one snapshot: the scanned
// config (common config, config.worktree overlay, and attribute routing
// sources, plus any resolved include target) is hashed into its byte-level
// fingerprint AND into its semantic fingerprint — the hash of the DANGEROUS
// subset only (see workspace.SemanticSnapshot), which is what the trust
// lifecycle keys on. Both snapshot forms are stored content-addressed under
// ~/.c0wrk/git-config-snapshots/ so notifyGitConfigRisk can recheck a later
// scan against them: inert branch/alias churn silently refreshes the stored
// bytes, any semantic change evicts the trust and re-warns with a semantic
// diff. A config that cannot be scanned is refused fail-closed (a trust
// decision cannot be bound to bytes that cannot be read).
// Idempotent: trusting an already-trusted root is a no-op.
func (f *FrontendAPI) TrustGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("repository path must be absolute: %s", path)
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return fmt.Errorf("repository directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repository path is not a directory: %s", cleaned)
	}
	// Normalize to the repository work-tree root — the same form
	// notifyGitConfigRisk attributes warnings to, so what the toast
	// displayed (risk.path) round-trips through the trust check.
	if root := workspace.ResolveWorkTreeRoot(cleaned); root != "" {
		cleaned = root
	}

	// Fail closed on a nil config before doing any I/O: with no config
	// loaded nothing can be trusted.
	f.configMu.RLock()
	cfgNil := f.config == nil
	f.configMu.RUnlock()
	if cfgNil {
		return errors.New("config not initialized")
	}

	// Capture the snapshot BEFORE mutating config: the trust decision is
	// bound to the exact bytes the user reviewed. Both identities are bound
	// at once — the byte-level fingerprint (its snapshot feeds the drift
	// diff and the v1→v2 migration of this very record) and the semantic
	// fingerprint (what the trust lifecycle actually keys on, so inert
	// branch/alias churn never evicts the trust).
	scanned, err := workspace.ScanGitConfig(cleaned, f.log())
	if err != nil {
		return fmt.Errorf("cannot fingerprint repository config (fail closed): %w", err)
	}
	scanned.ResolveIncludes(f.log())
	fingerprint := scanned.Fingerprint()
	if err := f.writeGitConfigSnapshot(fingerprint, scanned.Snapshot()); err != nil {
		return fmt.Errorf("cannot store repository config snapshot (fail closed): %w", err)
	}
	semanticFingerprint := scanned.SemanticFingerprint()
	if err := f.writeGitConfigSnapshot(semanticFingerprint, scanned.SemanticSnapshot()); err != nil {
		return fmt.Errorf("cannot store repository config semantic snapshot (fail closed): %w", err)
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(trusted.Path) == cleaned {
			return nil
		}
	}
	// A root cannot be both trusted and hardened (validation enforces the
	// exclusion); trusting a hardened root drops the hardening.
	f.removeHardenPathLocked(cleaned)
	f.config.Security.TrustedGitRepos = append(f.config.Security.TrustedGitRepos, config.TrustedGitRepo{Path: cleaned, Fingerprint: fingerprint, SemanticFingerprint: semanticFingerprint})
	f.syncGitTrustRegistry()
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist trusted git repositories", "error", err)
	}
	return nil
}

// RemoveTrustedGitRepo removes a repository root from the trusted list; its
// intake warning returns on the next open. Idempotent: removing an absent
// entry is a no-op (the settings dialog also uses it to prune stale paths).
// Both the given path's work-tree root and the exact cleaned path are
// removed when they differ, so entries stored before the root normalization
// (review [52]) — or a subdirectory path passed by hand — still prune
// cleanly instead of becoming unremovable zombies.
func (f *FrontendAPI) RemoveTrustedGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	cleaned := filepath.Clean(path)
	candidates := []string{cleaned}
	if root := workspace.ResolveWorkTreeRoot(cleaned); root != "" && root != cleaned {
		candidates = append(candidates, root)
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	kept := make([]config.TrustedGitRepo, 0, len(f.config.Security.TrustedGitRepos))
	removed := false
	for _, trusted := range f.config.Security.TrustedGitRepos {
		match := false
		for _, c := range candidates {
			if filepath.Clean(trusted.Path) == c {
				match = true
				break
			}
		}
		if match {
			removed = true
			continue
		}
		kept = append(kept, trusted)
	}
	if !removed {
		return nil
	}
	f.config.Security.TrustedGitRepos = kept
	f.syncGitTrustRegistry()
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist trusted git repositories", "error", err)
	}
	return nil
}

// removeTrustedPathLocked removes the trusted entry whose path equals cleaned
// (exact Clean match). Reports whether an entry was removed. The caller must
// hold configMu.Lock. It does NOT sync the registry or persist — callers do
// that once, after all mutations, so a combined trust/harden transition syncs
// exactly once.
func (f *FrontendAPI) removeTrustedPathLocked(cleaned string) bool {
	kept := make([]config.TrustedGitRepo, 0, len(f.config.Security.TrustedGitRepos))
	removed := false
	for _, t := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(t.Path) == cleaned {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	f.config.Security.TrustedGitRepos = kept
	return removed
}

// removeHardenPathLocked removes cleaned from HardenGitRepos (exact Clean
// match). Reports whether an entry was removed. The caller holds configMu.Lock.
func (f *FrontendAPI) removeHardenPathLocked(cleaned string) bool {
	kept := make([]string, 0, len(f.config.Security.HardenGitRepos))
	removed := false
	for _, h := range f.config.Security.HardenGitRepos {
		if filepath.Clean(h) == cleaned {
			removed = true
			continue
		}
		kept = append(kept, h)
	}
	f.config.Security.HardenGitRepos = kept
	return removed
}

// GetHardenGitRepos returns the repository roots the user has marked hardened
// (always neutralized; never raw-git eligible). The slice is a defensive copy
// and is never nil so JSON serializes as [].
func (f *FrontendAPI) GetHardenGitRepos() []string {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return []string{}
	}
	out := make([]string, len(f.config.Security.HardenGitRepos))
	copy(out, f.config.Security.HardenGitRepos)
	return out
}

// HardenGitRepo marks a repository root as hardened: it can never become
// raw-git eligible and its untrusted-git-config intake warning is never
// suppressed. Hardening is the inverse of trust, so a root that was trusted is
// untrusted (and unregistered from the spawn-layer trust registry) as part of
// the call — a root cannot be both trusted and hardened (validation enforces
// the exclusion). The path must be an existing absolute directory, normalized
// to its work-tree root exactly like TrustGitRepo. Idempotent.
func (f *FrontendAPI) HardenGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("repository path must be absolute: %s", path)
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return fmt.Errorf("repository directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repository path is not a directory: %s", cleaned)
	}
	if root := workspace.ResolveWorkTreeRoot(cleaned); root != "" {
		cleaned = root
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	for _, h := range f.config.Security.HardenGitRepos {
		if filepath.Clean(h) == cleaned {
			return nil
		}
	}
	// A root cannot be both hardened and trusted: hardening drops trust (and
	// the spawn-layer raw-git registration).
	f.removeTrustedPathLocked(cleaned)
	f.config.Security.HardenGitRepos = append(f.config.Security.HardenGitRepos, cleaned)
	f.syncGitTrustRegistry()
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist hardened git repositories", "error", err)
	}
	return nil
}

// RemoveHardenGitRepo removes a repository root from the hardened list.
// Idempotent: removing an absent entry is a no-op. Like RemoveTrustedGitRepo,
// it prunes both the given path's work-tree root and the exact cleaned path so
// stale or subdirectory spellings still remove cleanly.
func (f *FrontendAPI) RemoveHardenGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	cleaned := filepath.Clean(path)
	candidates := []string{cleaned}
	if root := workspace.ResolveWorkTreeRoot(cleaned); root != "" && root != cleaned {
		candidates = append(candidates, root)
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	kept := make([]string, 0, len(f.config.Security.HardenGitRepos))
	removed := false
	for _, h := range f.config.Security.HardenGitRepos {
		match := false
		for _, c := range candidates {
			if filepath.Clean(h) == c {
				match = true
				break
			}
		}
		if match {
			removed = true
			continue
		}
		kept = append(kept, h)
	}
	if !removed {
		return nil
	}
	f.config.Security.HardenGitRepos = kept
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist hardened git repositories", "error", err)
	}
	return nil
}

// notifyGitConfigRisk scans path's git config and emits the global
// project:git_config_risk event when the config is not provably clean
// (dangerous keys, include directives, malformed or unreadable config — the
// fail-closed complement of GitConfigInfo.Clean). A clean repo emits nothing,
// and a directory without .git is clean by definition. A repository the user
// explicitly trusted (security.trusted_git_repos) emits nothing ONLY while its
// configuration still matches the trusted snapshot: on drift the trust is
// evicted and the repo re-warns with a diff (see recheckTrustedGitRepo).
// Best-effort and synchronous: the scan is a bounded text parse, and emitting
// after the project:switched / workdirs:changed events keeps the UI narrative
// ("opened, then warned") stable. No-op when no emitter is wired (tests).
//
// The warning and the trust check are attributed to the repository's
// WORK-TREE ROOT, not the given path (review [52]): the scan itself walks up
// to the parent repository, so a workspace opened at a subdirectory must be
// warned under — and trusted as — the repository root it actually belongs
// to, exactly as ADR-033 and the security model describe. TrustGitRepo
// normalizes through the same workspace.ResolveWorkTreeRoot, so both sides
// always agree. The scan still runs on the given path: relative
// core.attributesFile values anchor where git actually runs (the workspace
// directory), not the repository root.
func (f *FrontendAPI) notifyGitConfigRisk(source, path string) {
	if f.emitEvent == nil {
		return
	}
	displayPath := path
	if root := workspace.ResolveWorkTreeRoot(path); root != "" {
		displayPath = root
	}
	if trusted, ok := f.trustedGitRepoEntry(displayPath); ok {
		f.recheckTrustedGitRepo(source, displayPath, trusted)
		return
	}
	info, err := workspace.ScanGitConfig(path, f.log())
	if err != nil {
		// Fail closed: an unreadable or oversized config cannot be proven
		// safe, so it is itself a warning.
		f.emitEvent(EventGitConfigRisk, GitConfigRiskData{
			Path:   displayPath,
			Source: source,
			Notice: gitConfigRiskNotice,
			Findings: []GitConfigRiskFinding{{
				Key: "(config unreadable)",
				Description: fmt.Sprintf(
					"The git configuration of the repository at %s could not be read (%v). Treat this repository as untrusted.", displayPath, err),
			}},
		})
		return
	}
	if info.Clean() {
		return
	}

	findings := buildGitConfigFindings(info)

	f.emitEvent(EventGitConfigRisk, GitConfigRiskData{
		Path:     displayPath,
		Source:   source,
		Notice:   gitConfigRiskNotice,
		Findings: findings,
	})
}

// recheckTrustedGitRepo decides whether a repository the user trusted is still
// safe to leave silent. The trust lifecycle is bound to the SEMANTIC
// fingerprint of the config snapshot captured at trust time (the hash of its
// DANGEROUS subset — see workspace.SemanticSnapshot), so a fresh scan is
// classified three ways:
//
//   - semantic fingerprint unchanged, byte-level fingerprint unchanged: the
//     configuration is exactly what the user trusted; nothing is emitted.
//   - semantic fingerprint unchanged, byte-level fingerprint differs: the
//     delta is INERT by construction (branch/alias bookkeeping — the only
//     records outside the semantic identity). The trust stands and the stored
//     raw snapshot is silently refreshed to the new bytes, so the byte-level
//     binding follows the config without an event.
//   - semantic fingerprint differs (or the config can no longer be scanned —
//     fail closed): the trust is evicted and the repository returns to the
//     hardened default with a warning carrying the drift reason and a diff of
//     the SEMANTIC change (DiffGitConfigSnapshots over the stored and current
//     semantic snapshots), so inert churn never buries the real difference.
//
// Records written before the semantic layer (a fingerprint but no semantic
// fingerprint, or the legacy bare-path form with neither) are migrated on
// this path: the semantic identity is recovered from the raw snapshot the
// record stored — describing the bytes the user actually trusted — and
// persisted. A record whose identity cannot be recovered fails closed: the
// trust is evicted and the repository warns (see
// migrateTrustedGitRepoRecord).
//
// The scan runs on displayPath (the work-tree root), NOT the caller's path:
// trust is stored — and fingerprinted — under the root TrustGitRepo resolved,
// so the recheck must scan the same path form to stay consistent (a relative
// core.attributesFile anchors where git runs; scanning a subdirectory would
// resolve it differently and false-trigger drift).
func (f *FrontendAPI) recheckTrustedGitRepo(source, displayPath string, trusted config.TrustedGitRepo) {
	// v1→v2 migration: a record predating the semantic layer carries no
	// semantic fingerprint. Recover it from the stored raw snapshot before
	// anything else — an unrecoverable record fails closed below.
	if trusted.SemanticFingerprint == "" {
		migrated := f.migrateTrustedGitRepoRecord(trusted)
		if migrated == nil {
			f.log().Debug("trusted git repository record cannot be migrated to the semantic fingerprint; evicting (fail closed)", "path", displayPath)
			f.evictTrustedGitRepo(displayPath)
			f.emitEvent(EventGitConfigRisk, GitConfigRiskData{
				Path:   displayPath,
				Source: source,
				Notice: gitConfigRiskNotice,
				Reason: gitConfigTrustUnverifiableReason,
				Findings: []GitConfigRiskFinding{{
					Key: "(trust unverifiable)",
					Description: "The trust record of this repository predates the semantic trust model and its recorded snapshot could not be recovered (missing, corrupted, or not written by this scanner). " +
						"Treat this repository as untrusted.",
				}},
			})
			return
		}
		trusted = *migrated
	}

	scanned, err := workspace.ScanGitConfig(displayPath, f.log())
	if err == nil {
		scanned.ResolveIncludes(f.log())
	}

	if err == nil && scanned.SemanticFingerprint() == trusted.SemanticFingerprint {
		if scanned.Fingerprint() == trusted.Fingerprint {
			f.log().Debug("git-config risk warning suppressed: trusted repository unchanged", "path", displayPath)
			return
		}
		// The bytes moved but the meaning did not: the delta is inert by
		// construction (branch/alias churn). Keep the trust and follow the
		// bytes silently — no event.
		f.refreshTrustedGitRepoRawSnapshot(displayPath, trusted, scanned)
		return
	}

	// Semantic drift (or an unreadable config — fail closed): the trust is
	// stale. Evict it so the spawn layer stops running raw git and the repo
	// hardens again.
	f.evictTrustedGitRepo(displayPath)

	data := GitConfigRiskData{
		Path:   displayPath,
		Source: source,
		Notice: gitConfigRiskNotice,
		Reason: gitConfigDriftReason,
	}

	var currentSemantic []byte
	if err == nil {
		currentSemantic = scanned.SemanticSnapshot()
	}
	if previous, readErr := f.readGitConfigSnapshot(trusted.SemanticFingerprint); readErr == nil {
		data.Diff = workspace.DiffGitConfigSnapshots(previous, currentSemantic)
	}

	switch {
	case err != nil:
		data.Findings = []GitConfigRiskFinding{{
			Key: "(config unreadable)",
			Description: fmt.Sprintf(
				"The repository's git configuration changed since it was trusted and can no longer be read (%v). Treat this repository as untrusted.", err),
		}}
	case scanned.Clean():
		// The config is no longer dangerous; the drift itself is still
		// material because the trust was bound to the old snapshot.
		data.Findings = []GitConfigRiskFinding{{
			Key:         "(config changed)",
			Description: "The repository's git configuration changed since it was trusted.",
		}}
	default:
		data.Findings = buildGitConfigFindings(scanned)
	}

	f.emitEvent(EventGitConfigRisk, data)
}

// migrateTrustedGitRepoRecord recovers the semantic fingerprint of a v1 trust
// record — one written before the semantic layer, carrying only the raw
// byte-level fingerprint (or, in the legacy bare-path form, neither
// fingerprint). The identity is recovered from the raw snapshot the record
// stored, deliberately WITHOUT re-scanning the repository: the recovered
// identity must describe the bytes the user actually reviewed at trust time,
// whatever the repository carries now. On success the record in the config is
// updated and persisted, and the semantic snapshot is stored content-
// addressed next to the raw one. A nil return means the record cannot be
// migrated — no fingerprint, a missing or corrupted snapshot (one that no
// longer hashes to its content-addressed name), or a snapshot this scanner
// cannot interpret — and the caller must fail closed: evict the trust rather
// than carry an unverifiable one over.
func (f *FrontendAPI) migrateTrustedGitRepoRecord(trusted config.TrustedGitRepo) *config.TrustedGitRepo {
	if trusted.Fingerprint == "" {
		// Legacy bare-path record: no snapshot was ever captured, so there
		// is nothing to recover the trusted identity from.
		f.log().Debug("trusted git repository record has no snapshot (legacy); cannot migrate", "path", trusted.Path)
		return nil
	}
	raw, err := f.readGitConfigSnapshot(trusted.Fingerprint)
	if err != nil {
		f.log().Debug("trusted git repository record migration failed: raw snapshot unreadable", "path", trusted.Path, "error", err)
		return nil
	}
	// The snapshot store is content-addressed: a file that no longer hashes
	// to its name is corrupted, and carrying trust over from it would bind
	// the trust to bytes the user never reviewed.
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != trusted.Fingerprint {
		f.log().Debug("trusted git repository record migration failed: raw snapshot does not match its fingerprint (corrupted)", "path", trusted.Path)
		return nil
	}
	semanticFingerprint, semantic, err := workspace.SemanticFingerprintFromSnapshot(raw)
	if err != nil {
		f.log().Debug("trusted git repository record migration failed: snapshot cannot be interpreted", "path", trusted.Path, "error", err)
		return nil
	}
	if err := f.writeGitConfigSnapshot(semanticFingerprint, semantic); err != nil {
		// The identity itself is recovered; only its stored copy is missing,
		// which degrades a future drift diff, never the detection.
		f.log().Warn("failed to store migrated git-config semantic snapshot", "path", trusted.Path, "error", err)
	}

	f.configMu.Lock()
	updated := false
	for i := range f.config.Security.TrustedGitRepos {
		entry := &f.config.Security.TrustedGitRepos[i]
		if filepath.Clean(entry.Path) == filepath.Clean(trusted.Path) &&
			entry.Fingerprint == trusted.Fingerprint && entry.SemanticFingerprint == "" {
			entry.SemanticFingerprint = semanticFingerprint
			updated = true
		}
	}
	f.configMu.Unlock()
	if !updated {
		// The record changed or vanished concurrently; do not report a
		// migration for a trust that is no longer the one being rechecked.
		return nil
	}
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist migrated trusted git repositories", "error", err)
	}
	f.log().Debug("trusted git repository record migrated to the semantic fingerprint", "path", trusted.Path)
	out := trusted
	out.SemanticFingerprint = semanticFingerprint
	return &out
}

// refreshTrustedGitRepoRawSnapshot rebinds a trusted entry to the freshly
// scanned bytes after inert drift: the semantic identity is unchanged, so the
// delta cannot affect how c0wrk's git operations behave, and only the
// byte-level fingerprint and its snapshot follow the config. The semantic
// snapshot is rewritten too — content-addressed, so the write is a no-op
// unless a previous write was lost, in which case it heals the store.
// Nothing is emitted: the user trusted a meaning, and the meaning did not
// change.
func (f *FrontendAPI) refreshTrustedGitRepoRawSnapshot(displayPath string, trusted config.TrustedGitRepo, scanned *workspace.GitConfigInfo) {
	fingerprint := scanned.Fingerprint()
	if err := f.writeGitConfigSnapshot(fingerprint, scanned.Snapshot()); err != nil {
		f.log().Warn("failed to refresh git-config snapshot for trusted repository", "path", displayPath, "error", err)
	}
	if err := f.writeGitConfigSnapshot(trusted.SemanticFingerprint, scanned.SemanticSnapshot()); err != nil {
		f.log().Warn("failed to refresh git-config semantic snapshot for trusted repository", "path", displayPath, "error", err)
	}
	f.configMu.Lock()
	updated := false
	for i := range f.config.Security.TrustedGitRepos {
		entry := &f.config.Security.TrustedGitRepos[i]
		if filepath.Clean(entry.Path) == displayPath && entry.Fingerprint == trusted.Fingerprint {
			entry.Fingerprint = fingerprint
			updated = true
		}
	}
	f.configMu.Unlock()
	if !updated {
		return // the entry was evicted concurrently; nothing to refresh
	}
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist refreshed trusted git repositories", "error", err)
	}
	f.log().Debug("git-config drift for trusted repository is inert; raw snapshot refreshed", "path", displayPath)
}

// evictTrustedGitRepo removes the trusted entry for displayPath, mirroring
// the removal into the process-wide git trust registry so the repository
// hardens again. Best-effort: failures are logged, not returned — the caller
// always emits the warning event either way.
func (f *FrontendAPI) evictTrustedGitRepo(displayPath string) {
	if err := f.RemoveTrustedGitRepo(displayPath); err != nil {
		f.log().Warn("failed to evict drifted trusted git repository", "path", displayPath, "error", err)
	}
}

// buildGitConfigFindings renders a scan result into the findings slice the
// warning payload carries: every dangerous key, include directive, malformed
// construct, and (when engaged) the attribute-interpretation disclosure.
func buildGitConfigFindings(info *workspace.GitConfigInfo) []GitConfigRiskFinding {
	findings := make([]GitConfigRiskFinding, 0, len(info.Findings)+len(info.Includes)+len(info.Errors)+1)
	for i := range info.Findings {
		findings = append(findings, GitConfigRiskFinding{
			Key:         info.Findings[i].FullKey,
			Description: info.Findings[i].Description,
		})
	}
	for _, inc := range info.Includes {
		kind := "include"
		if inc.Conditional {
			kind = "includeIf"
		}
		findings = append(findings, GitConfigRiskFinding{
			Key: "(include directive)",
			Description: fmt.Sprintf(
				"Config line %d references an external file via %s (%q). Included files are deliberately not read, so the visible config may be incomplete.",
				inc.Line, kind, inc.Path),
		})
	}
	if len(info.Errors) > 0 {
		parts := make([]string, 0, len(info.Errors))
		for _, e := range info.Errors {
			parts = append(parts, fmt.Sprintf("line %d: %s", e.Line, e.Message))
		}
		findings = append(findings, GitConfigRiskFinding{
			Key: "(config malformed)",
			Description: "The config contains constructs git itself refuses to parse (" +
				strings.Join(parts, "; ") + ").",
		})
	}

	// Review [56]c: surface the blanket attribute-interpretation kill while
	// it is active. In narrow mode (visible drivers pinned by name) benign
	// attributes keep working and nothing is added; only the include case —
	// where driver names may be hidden from the scan — still engages
	// attr.tree, and its collateral is disclosed here so a user staring at
	// falsely-modified CRLF files knows why.
	if info.AttributesInterpretationDisabled() {
		findings = append(findings, GitConfigRiskFinding{
			Key: "(attributes disabled)",
			Description: "The configuration is not fully visible (include directives), so c0wrk " +
				"additionally disables ALL git attribute interpretation for its operations as a " +
				"precaution. Benign attributes stop working too: eol/CRLF text normalization is off, " +
				"so text files may be reported as modified (with whole-file diff statistics) even " +
				"though their content is unchanged. This affects only how c0wrk reads the " +
				"repository; no repository-defined program executes.",
		})
	}

	return findings
}
