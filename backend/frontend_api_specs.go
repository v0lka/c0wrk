package backend

import (
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/project"
)

// Workspace resolution for the SDD (spec-driven development) surface.
//
// The SDD subsystem is framework-aware: what the Specs panel shows depends on
// which SDD framework the workspace has installed (skill detection), and the
// tree is parsed through that framework's declarative definition (core/sdd).
// Two roots are involved and they deliberately differ:
//
//	active-session workspace   the TREE — where the agent writes specs
//	project checkout           the operator DSL file (<checkout>/.c0wrk/specs.yaml)
//
// The tree follows the active session (ADR-116, ADR-099's protocol), because an
// agent writes specifications inside its session tree; the operator's c0wrk
// metadata file travels with the repository, so it is always read from the
// checkout. See specs/domains/specs.md.

// sddWorkspace resolves the workspace the SDD tree is read from: the active
// session's workspace when one resolves and belongs to the project, the
// project checkout otherwise. "" for nil, No Project, or a workspace-less
// project.
func (f *FrontendAPI) sddWorkspace(p *project.ProjectInfo) string {
	if p == nil || p.IsNoProject || p.WorkspacePath == "" {
		return ""
	}
	if ws, ok := f.activeSessionWorkspace(p.ID); ok {
		return ws
	}
	return p.WorkspacePath
}

// sddCheckout returns the project checkout path — the root the checkout-local
// operator DSL file is read from. It deliberately does NOT follow the session:
// the file is repository data, not agent output. "" for nil, No Project, or a
// workspace-less project.
func sddCheckout(p *project.ProjectInfo) string {
	if p == nil || p.IsNoProject {
		return ""
	}
	return p.WorkspacePath
}

// sddRootPath returns the absolute path of one declared framework root inside
// workspace. The declared path is workspace-relative and slash separated.
func sddRootPath(workspace, declared string) string {
	if workspace == "" || declared == "" {
		return ""
	}
	return filepath.Join(workspace, filepath.FromSlash(declared))
}
