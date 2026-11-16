package config

import "path/filepath"

// Paths for the operator-authored SDD framework definitions consumed by
// core/sdd. Both files are OPTIONAL, operator-owned data: the application only
// ever reads them, a missing file is the neutral "no operator frameworks"
// state, and neither is created lazily — not at startup, not on a panel open,
// not on a failed parse. Merging rules and the fail-soft contract live in
// core/sdd/dsl.go.

// ProjectMetaDirName is the checkout-local c0wrk metadata directory
// (<workspace>/.c0wrk). It deliberately mirrors the agent-directory name
// (DefaultAgentDir) while naming a different thing: per-checkout operator data
// that travels with the repository, not the application's own state.
const ProjectMetaDirName = ".c0wrk"

// SDDPath returns the operator's global SDD framework definitions file
// (<agentDir>/specs.yaml, i.e. ~/.c0wrk/specs.yaml): a YAML stream of framework
// documents that add to, or override wholesale, the built-in catalog.
func SDDPath(agentDir string) string {
	return filepath.Join(agentDir, "specs.yaml")
}

// ProjectSDDPath returns the checkout-local SDD framework definitions file
// (<workspacePath>/.c0wrk/specs.yaml). It is read after SDDPath, so with a
// colliding framework id the project file wins over the global one. An empty
// workspace yields "" — never a RELATIVE path: joining onto "" would resolve
// against the process CWD and read a stranger's specs.yaml as project data.
func ProjectSDDPath(workspacePath string) string {
	if workspacePath == "" {
		return ""
	}
	return filepath.Join(workspacePath, ProjectMetaDirName, "specs.yaml")
}
