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

import (
	"fmt"
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

// notifyGitConfigRisk scans path's git config and emits the global
// project:git_config_risk event when the config is not provably clean
// (dangerous keys, include directives, malformed or unreadable config — the
// fail-closed complement of GitConfigInfo.Clean). A clean repo emits nothing,
// and a directory without .git is clean by definition. Best-effort and
// synchronous: the scan is a bounded text parse, and emitting after the
// project:switched / workdirs:changed events keeps the UI narrative
// ("opened, then warned") stable. No-op when no emitter is wired (tests).
func (f *FrontendAPI) notifyGitConfigRisk(source, path string) {
	if f.emitEvent == nil {
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
