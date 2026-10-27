package backend

// Git-config intake warnings: opening a repository whose .git/config carries
// dangerous keys emits project:git_config_risk before agent work starts.
// Detection only — exec-free text parsing; neutralization happens in the git
// spawn layer, never the UI (specs/architecture/security-model.md,
// specs/decisions/033-git-subprocess-hardening.md). Anything not provably
// clean fails closed into a warning; trusted repositories run raw git
// (specs/decisions/034-git-trust-opt-out.md, 070-git-semantic-trust.md).

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

// gitConfigRiskNotice is the standing safety note carried with every warning:
// hooks never run and the listed config-driven programs are neutralized on
// every git invocation, remote operations included. It informs without
// implying the config executed.
const gitConfigRiskNotice = "Repository-defined git hooks do not run inside c0wrk: the config-driven programs listed below are blocked or neutralized on every git invocation c0wrk makes, remote operations (pull, push, fetch) included. Continue only if you trust this repository."

// gitConfigDriftReason is the Reason for semantic drift of a trusted
// repository's command-bearing config: trust evicted, repository hardened
// again (inert branch/alias churn never fires this — ADR-070).
const gitConfigDriftReason = "This repository was previously trusted, but its command-bearing git configuration changed since you trusted it — the diff below shows the security-relevant changes only (routine branch-tracking and alias bookkeeping never triggers this warning). The trust has been revoked and the repository is hardened again; re-trust it only if the change is expected."

// gitConfigTrustUnverifiableReason is the Reason when a v1 trust record
// cannot be migrated to the semantic fingerprint (missing/corrupted
// snapshot): revoked fail-closed; re-trusting re-captures a fresh snapshot.
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
	// Reason is set only for evicted-trust warnings; empty for ordinary
	// first-time intake warnings.
	Reason string `json:"reason,omitempty"`
	// Diff is a human-readable diff between the trusted snapshot and the
	// current configuration ("" for ordinary first-time warnings).
	Diff string `json:"diff,omitempty"`
}

// gitRepoTrusted reports whether path's repository was marked trusted by the
// user (security.trusted_git_repos). Matching runs across every trust-key
// form of both sides, deliberately WITHOUT prefix/subtree semantics: trust
// is per repository root and must never silently cover a nested repository
// or its parent. No config loaded → nothing is trusted (fail-closed).
func (f *FrontendAPI) gitRepoTrusted(path string) bool {
	_, ok := f.trustedGitRepoEntryFor(path)
	return ok
}

// trustedGitRepoEntryFor returns a COPY of the trusted entry matching any
// trust-key form of path (workspace.TrustProbePaths), or ok=false. It
// exposes the entry — its Fingerprint included — so notifyGitConfigRisk can
// recheck the stored snapshot instead of trusting blindly; a value copy so
// the caller never holds a pointer into the mutex-guarded slice.
func (f *FrontendAPI) trustedGitRepoEntryFor(path string) (config.TrustedGitRepo, bool) {
	candidates := workspace.TrustProbePaths(path)

	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return config.TrustedGitRepo{}, false
	}
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if trustEntryMatchesPath(trusted.Path, candidates) {
			return trusted, true
		}
	}
	return config.TrustedGitRepo{}, false
}

// trustEntryMatchesPath reports whether a stored entry's path denotes the
// same repository as one of the probe candidates: SYMMETRIC across every
// trust-key form of BOTH sides (workspace.TrustProbePaths), so any legacy
// spelling matches a root query and vice versa — what keeps the trust⊥harden
// exclusion, idempotency loops, and removals sound. It may walk symlinks;
// keep it off hot loops' inner iterations.
func trustEntryMatchesPath(entryPath string, candidates []string) bool {
	for _, form := range workspace.TrustProbePaths(entryPath) {
		for _, c := range candidates {
			if form == c {
				return true
			}
		}
	}
	return false
}

// writeGitConfigSnapshot stores snapshot bytes content-addressed under
// ~/.c0wrk/git-config-snapshots/<fingerprint> (directory created lazily;
// errors when the agent dir is unset — tests that trust a repo must set it).
func (f *FrontendAPI) writeGitConfigSnapshot(fingerprint string, snapshot []byte) error {
	f.seedAcquire()
	if f.agentDir == "" {
		return errors.New("agent dir not set")
	}
	dir := config.GitConfigSnapshotsDir(f.agentDir)
	// REAL directories only (review [108]): os.MkdirAll accepts a
	// symlink-to-directory, so a swapped-in link would redirect snapshot
	// writes and reads outside ~/.c0wrk; a pre-existing operator symlink
	// resolves as intent.
	if err := safeio.MkdirAllReal(dir, 0o755); err != nil {
		return fmt.Errorf("creating git-config snapshot directory: %w", err)
	}
	// safeio.WriteFile refuses non-regular targets (a planted FIFO would
	// block forever); since the write is destructive (O_TRUNC) and Windows
	// resolves a final symlink, that symlink is refused explicitly on every
	// platform instead of relying on the no-follow open.
	snapshotPath := filepath.Join(dir, fingerprint)
	if info, lerr := os.Lstat(snapshotPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write git-config snapshot: %s is a symlink", snapshotPath)
	}
	return safeio.WriteFile(snapshotPath, snapshot, 0o644)
}

// readGitConfigSnapshot returns the previously-stored snapshot bytes for a
// fingerprint, or an error when none is stored (or the agent dir is unset).
func (f *FrontendAPI) readGitConfigSnapshot(fingerprint string) ([]byte, error) {
	f.seedAcquire()
	if f.agentDir == "" {
		return nil, errors.New("agent dir not set")
	}
	return safeio.ReadFile(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), fingerprint))
}

// syncGitTrustRegistry mirrors security.trusted_git_repos into the
// process-wide core/gittrust registry the spawn layer consults before raw
// git (ADR-034). Entries register under their REPOSITORY TRUST ROOT because
// the registry matches strings exactly — a legacy worktree-keyed entry must
// register the main root; an unresolvable pointer chain mirrors verbatim
// (the form degraded probes still reach). Harden entries never mirror.
// Caller holds configMu in concurrent contexts.
func (f *FrontendAPI) syncGitTrustRegistry() {
	if f.config == nil {
		gittrust.Replace(nil)
		return
	}
	paths := make([]string, 0, len(f.config.Security.TrustedGitRepos))
	for _, r := range f.config.Security.TrustedGitRepos {
		registered := r.Path
		if root := workspace.ResolveGitTrustRoot(r.Path); root != "" {
			registered = root
		}
		paths = append(paths, registered)
	}
	gittrust.Replace(paths)
}

// GetTrustedGitRepos returns the trusted repository roots as a defensive
// copy, never nil (JSON []).
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

// TrustGitRepo marks a repository root as trusted: its intake warning is
// suppressed and the spawn layer runs raw git for it (ADR-034,
// specs/decisions/070-git-semantic-trust.md). The REPOSITORY TRUST ROOT is
// stored — trusting any path inside a repository trusts it whole, worktrees
// included. The decision binds the byte-level fingerprint AND the semantic
// fingerprint (the DANGEROUS subset, workspace.SemanticSnapshot), both
// stored content-addressed for later rechecks: inert churn refreshes the
// bytes, semantic change evicts. Unscannable → refused fail-closed.
// Idempotent across every trust-key form.
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
	// Normalize to the repository trust root — the form warnings are
	// attributed to, so risk.path round-trips through the trust check (a
	// linked worktree normalizes to the MAIN root its commit surface is
	// shared with).
	if root := workspace.ResolveGitTrustRoot(cleaned); root != "" {
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

	// Capture the snapshot BEFORE mutating config: the decision is bound to
	// the exact bytes the user reviewed — byte-level fingerprint (drift
	// diff, v1→v2 migration) and semantic fingerprint (the lifecycle key).
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
	// Idempotent across every trust-key form: a duplicate record would
	// re-fire the intake warning on the next open.
	candidates := workspace.TrustProbePaths(cleaned)
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if trustEntryMatchesPath(trusted.Path, candidates) {
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

// RemoveTrustedGitRepo removes a repository root from the trusted list (its
// warning returns on the next open). Idempotent; prunes every trust-key form
// of the path so no spelling becomes an unremovable zombie.
func (f *FrontendAPI) RemoveTrustedGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	candidates := workspace.TrustProbePaths(path)

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	kept := make([]config.TrustedGitRepo, 0, len(f.config.Security.TrustedGitRepos))
	removed := false
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if trustEntryMatchesPath(trusted.Path, candidates) {
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

// removeTrustedPathLocked removes every trusted entry denoting the same
// repository as cleaned (all trust-key forms), so a legacy-spelled entry
// cannot survive a hardening as a raw-git-eligible zombie. Caller holds
// configMu.Lock; no registry sync or persist — callers do that once after
// all mutations.
func (f *FrontendAPI) removeTrustedPathLocked(cleaned string) bool {
	candidates := workspace.TrustProbePaths(cleaned)
	kept := make([]config.TrustedGitRepo, 0, len(f.config.Security.TrustedGitRepos))
	removed := false
	for _, t := range f.config.Security.TrustedGitRepos {
		if trustEntryMatchesPath(t.Path, candidates) {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	f.config.Security.TrustedGitRepos = kept
	return removed
}

// removeHardenPathLocked is removeTrustedPathLocked's mirror for hardened
// entries, so a legacy worktree-keyed hardening cannot survive a trust
// decision for the repository root. Caller holds configMu.Lock.
func (f *FrontendAPI) removeHardenPathLocked(cleaned string) bool {
	candidates := workspace.TrustProbePaths(cleaned)
	kept := make([]string, 0, len(f.config.Security.HardenGitRepos))
	removed := false
	for _, h := range f.config.Security.HardenGitRepos {
		if trustEntryMatchesPath(h, candidates) {
			removed = true
			continue
		}
		kept = append(kept, h)
	}
	f.config.Security.HardenGitRepos = kept
	return removed
}

// GetHardenGitRepos returns the hardened repository roots (always
// neutralized, never raw-git eligible) as a defensive copy, never nil.
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

// HardenGitRepo marks a repository root as hardened: never raw-git eligible,
// its warning never suppressed. The inverse of trust — drops and unregisters
// any trusted entry for the same repository (mutual exclusion), normalized
// like TrustGitRepo. Idempotent across every trust-key form.
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
	if root := workspace.ResolveGitTrustRoot(cleaned); root != "" {
		cleaned = root
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	// Idempotent across every trust-key form (matching as in TrustGitRepo).
	candidates := workspace.TrustProbePaths(cleaned)
	for _, h := range f.config.Security.HardenGitRepos {
		if trustEntryMatchesPath(h, candidates) {
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
// Idempotent; prunes every trust-key form like RemoveTrustedGitRepo.
func (f *FrontendAPI) RemoveHardenGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	candidates := workspace.TrustProbePaths(path)

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	kept := make([]string, 0, len(f.config.Security.HardenGitRepos))
	removed := false
	for _, h := range f.config.Security.HardenGitRepos {
		if trustEntryMatchesPath(h, candidates) {
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

// notifyGitConfigRisk scans path's git config and emits project:git_config_risk
// when the config is not provably clean (fail-closed complement of
// GitConfigInfo.Clean; no .git → clean). A trusted repository stays silent
// only while it matches its snapshot — on drift the trust is evicted and the
// repo re-warns with a diff (recheckTrustedGitRepo). Synchronous, emitted
// after project:switched / workdirs:changed ("opened, then warned"); no-op
// without an emitter (tests). Attribution and trust key on the repository
// trust root (ADR-033); the entry lookup and the scan still run on the given
// path — relative core.attributesFile anchors where git runs.
func (f *FrontendAPI) notifyGitConfigRisk(source, path string) {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	displayPath := path
	if root := workspace.ResolveGitTrustRoot(path); root != "" {
		displayPath = root
	}
	if trusted, ok := f.trustedGitRepoEntryFor(path); ok {
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

// recheckTrustedGitRepo decides whether a trusted repository stays silent.
// The lifecycle keys on the SEMANTIC fingerprint (ADR-070): both fingerprints
// unchanged → silence; bytes differ, semantics not → inert by construction,
// refresh the raw snapshot silently; semantics differ (or unscannable —
// fail closed) → evict and re-warn with the SEMANTIC diff. Pre-semantic
// records are migrated from their raw snapshot first
// (migrateTrustedGitRepoRecord); unrecoverable identity fails closed. The
// scan runs on displayPath — trust is fingerprinted under the resolved root.
func (f *FrontendAPI) recheckTrustedGitRepo(source, displayPath string, trusted config.TrustedGitRepo) {
	f.seedAcquire()
	// v1→v2 migration first: an unrecoverable record fails closed below.
	if trusted.SemanticFingerprint == "" {
		migrated := f.migrateTrustedGitRepoRecord(trusted)
		if migrated == nil {
			f.log().Debug("trusted git repository record cannot be migrated to the semantic fingerprint; evicting (fail closed)", "path", displayPath)
			f.evictTrustedGitRepo(trusted)
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
		// Bytes moved, meaning did not: inert by construction. Follow
		// silently, no event.
		f.refreshTrustedGitRepoRawSnapshot(displayPath, trusted, scanned)
		return
	}

	// Semantic drift (or unreadable config — fail closed): evict so the
	// spawn layer stops running raw git. Keys on the ENTRY's stored path
	// form, so the record actually leaves the list.
	f.evictTrustedGitRepo(trusted)

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

// migrateTrustedGitRepoRecord recovers a v1 record's semantic fingerprint
// from its stored RAW snapshot — deliberately without re-scanning, so the
// identity describes the bytes the user actually reviewed, whatever the
// repository carries now. On success the record is updated and persisted and
// the semantic snapshot stored; nil means unmigratable (no fingerprint,
// missing/corrupted snapshot, uninterpretable content) and the caller must
// fail closed: evict rather than carry an unverifiable trust.
func (f *FrontendAPI) migrateTrustedGitRepoRecord(trusted config.TrustedGitRepo) *config.TrustedGitRepo {
	if trusted.Fingerprint == "" {
		// Legacy bare-path record: nothing to recover the identity from.
		f.log().Debug("trusted git repository record has no snapshot (legacy); cannot migrate", "path", trusted.Path)
		return nil
	}
	raw, err := f.readGitConfigSnapshot(trusted.Fingerprint)
	if err != nil {
		f.log().Debug("trusted git repository record migration failed: raw snapshot unreadable", "path", trusted.Path, "error", err)
		return nil
	}
	// Content-addressed store: a file that no longer hashes to its name is
	// corrupted — carrying trust over would bind it to unreviewed bytes.
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
	if updated {
		// Persisted UNDER configMu (review [103]): config.Save's yaml.Marshal
		// ranges the live config maps — marshalling outside the lock races a
		// concurrent Settings save into a fatal concurrent map access.
		if err := f.persistConfig(); err != nil {
			f.log().Warn("failed to persist migrated trusted git repositories", "error", err)
		}
	}
	f.configMu.Unlock()
	if !updated {
		// The record changed or vanished concurrently — no migration to report.
		return nil
	}
	f.log().Debug("trusted git repository record migrated to the semantic fingerprint", "path", trusted.Path)
	out := trusted
	out.SemanticFingerprint = semanticFingerprint
	return &out
}

// refreshTrustedGitRepoRawSnapshot rebinds a trusted entry to freshly scanned
// bytes after inert drift: only the byte-level fingerprint and its snapshot
// follow the config; the semantic snapshot rewrite is a no-op unless a
// previous write was lost (it heals the store). Nothing is emitted.
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
		// Match the ENTRY's own stored path, never the display form — a
		// refresh must never touch a different record.
		if filepath.Clean(entry.Path) == filepath.Clean(trusted.Path) && entry.Fingerprint == trusted.Fingerprint {
			entry.Fingerprint = fingerprint
			updated = true
		}
	}
	if updated {
		// Persisted UNDER configMu — review [103], see migrateTrustedGitRepoRecord.
		if err := f.persistConfig(); err != nil {
			f.log().Warn("failed to persist refreshed trusted git repositories", "error", err)
		}
	}
	f.configMu.Unlock()
	if !updated {
		return // the entry was evicted concurrently; nothing to refresh
	}
	f.log().Debug("git-config drift for trusted repository is inert; raw snapshot refreshed", "path", displayPath)
}

// evictTrustedGitRepo removes the trusted entry (by its stored path form, so
// legacy-spelled records still leave the list) and mirrors the removal into
// the gittrust registry so the repository hardens again. Best-effort — the
// caller emits the warning either way.
func (f *FrontendAPI) evictTrustedGitRepo(trusted config.TrustedGitRepo) {
	if err := f.RemoveTrustedGitRepo(trusted.Path); err != nil {
		f.log().Warn("failed to evict drifted trusted git repository", "path", trusted.Path, "error", err)
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

	// Review [56]c: disclose the blanket attribute-interpretation kill while
	// it is active (only the include case still engages attr.tree), so a
	// user staring at falsely-modified CRLF files knows why.
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
