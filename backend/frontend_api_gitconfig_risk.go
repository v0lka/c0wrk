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
//
// Repositories the user explicitly trusted (security.trusted_git_repos,
// maintained by the TrustGitRepo / RemoveTrustedGitRepo / GetTrustedGitRepos
// RPCs below) skip the warning; the neutralization itself never turns off.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/core/workspace"
)

// Source values carried in a project:git_config_risk payload — what was
// opened when the suspicious config was found.
const (
	GitConfigRiskSourceProject = "project"
	GitConfigRiskSourceWorkdir = "workdir"
)

// gitConfigRiskNotice is the standing safety note carried with every warning.
// It states explicitly that repository-defined hooks never run inside c0wrk:
// the detected keys are blocked or neutralized on every git invocation c0wrk
// makes, so the warning informs without implying the config executed.
const gitConfigRiskNotice = "Repository-defined git hooks do not run inside c0wrk: hooks and the config-driven programs listed below are blocked or neutralized on every git invocation c0wrk makes. Continue only if you trust this repository."

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
	// Path is the scanned repository or work-directory root.
	Path string `json:"path"`
	// Source is "project" (project switch) or "workdir" (added auxiliary
	// working directory).
	Source string `json:"source"`
	// Notice is the fixed hooks-do-not-run safety note.
	Notice string `json:"notice"`
	// Findings lists every detected key / danger. Never empty when the
	// event fires.
	Findings []GitConfigRiskFinding `json:"findings"`
}

// gitRepoTrusted reports whether path has been marked trusted by the user
// (security.trusted_git_repos, maintained by the TrustGitRepo RPC). Both the
// stored entries and the probe are compared as filepath.Clean-ed strings —
// an exact match, deliberately without prefix/subtree semantics: trust is per
// repository root, so a decision for one root must never silently cover a
// different repository nested inside it (or the parent it lives in). With no
// config loaded nothing is trusted (fail-closed: the warning stays on).
func (f *FrontendAPI) gitRepoTrusted(path string) bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return false
	}
	cleaned := filepath.Clean(path)
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(trusted) == cleaned {
			return true
		}
	}
	return false
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
	out = append(out, f.config.Security.TrustedGitRepos...)
	return out
}

// TrustGitRepo marks a repository root as trusted: the
// "untrusted git configuration detected" intake warning is no longer emitted
// for it. The path must be an existing absolute directory; it is stored in
// filepath.Clean form — the same normalization the trusted check compares
// against, so what the toast displayed (risk.path) is exactly what matches
// on the next scan. Idempotent: trusting an already-trusted root is a no-op.
// Trust suppresses ONLY the warning; the spawn-layer neutralization (ADR-033
// layers 1-2) stays fully in force regardless.
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

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(trusted) == cleaned {
			return nil
		}
	}
	f.config.Security.TrustedGitRepos = append(f.config.Security.TrustedGitRepos, cleaned)
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist trusted git repositories", "error", err)
	}
	return nil
}

// RemoveTrustedGitRepo removes a repository root from the trusted list; its
// intake warning returns on the next open. Idempotent: removing an absent
// entry is a no-op (the settings dialog also uses it to prune stale paths).
func (f *FrontendAPI) RemoveTrustedGitRepo(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	cleaned := filepath.Clean(path)

	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	kept := make([]string, 0, len(f.config.Security.TrustedGitRepos))
	removed := false
	for _, trusted := range f.config.Security.TrustedGitRepos {
		if filepath.Clean(trusted) == cleaned {
			removed = true
			continue
		}
		kept = append(kept, trusted)
	}
	if !removed {
		return nil
	}
	f.config.Security.TrustedGitRepos = kept
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist trusted git repositories", "error", err)
	}
	return nil
}

// notifyGitConfigRisk scans path's git config and emits the global
// project:git_config_risk event when the config is not provably clean
// (dangerous keys, include directives, malformed or unreadable config — the
// fail-closed complement of GitConfigInfo.Clean). A clean repo emits nothing,
// and a directory without .git is clean by definition. A repository the user
// explicitly trusted (security.trusted_git_repos) emits nothing as well — the
// warning is suppressed, never the neutralization. Best-effort and
// synchronous: the scan is a bounded text parse, and emitting after the
// project:switched / workdirs:changed events keeps the UI narrative
// ("opened, then warned") stable. No-op when no emitter is wired (tests).
func (f *FrontendAPI) notifyGitConfigRisk(source, path string) {
	if f.emitEvent == nil {
		return
	}
	if f.gitRepoTrusted(path) {
		f.log().Debug("git-config risk warning suppressed: repository is trusted", "path", path)
		return
	}
	info, err := workspace.ScanGitConfig(path, f.log())
	if err != nil {
		// Fail closed: an unreadable or oversized config cannot be proven
		// safe, so it is itself a warning.
		f.emitEvent(EventGitConfigRisk, GitConfigRiskData{
			Path:   path,
			Source: source,
			Notice: gitConfigRiskNotice,
			Findings: []GitConfigRiskFinding{{
				Key: "(config unreadable)",
				Description: fmt.Sprintf(
					"The git configuration at %s could not be read (%v). Treat this repository as untrusted.", path, err),
			}},
		})
		return
	}
	if info.Clean() {
		return
	}

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

	f.emitEvent(EventGitConfigRisk, GitConfigRiskData{
		Path:     path,
		Source:   source,
		Notice:   gitConfigRiskNotice,
		Findings: findings,
	})
}
