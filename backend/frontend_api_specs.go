package backend

import (
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/specs"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// SpecsStatusDTO is the full Specs-panel view model (ADR-105): the parsed
// specs/ tree of one project — every spec file, the INDEX.md task→spec
// navigation table, ADR metadata, and the deterministic tier-1 integrity
// findings. The wire shape is stable: the collection fields and the counts
// histogram are ALWAYS non-nil (empty collections serialize as [] and {},
// never null), so the frontend renders without null guards. No Project gets
// the neutral present=false state. See specs/decisions/105-specs-sidebar-panel.md.
type SpecsStatusDTO struct {
	// Present reports whether <workspace>/specs exists and is a directory.
	// A missing tree is a neutral state, never an error (ADR-105 D2).
	Present bool `json:"present"`

	// Root is the absolute specs root path (config.ProjectSpecsPath); ""
	// for No Project.
	Root string `json:"root"`

	// Files holds every parsed spec file — the four section trees plus the
	// root files INDEX.md and META.md — sorted by path. Always non-nil.
	Files []specs.SpecFile `json:"files"`

	// IndexTasks is the INDEX.md "Navigation by Task" table in document
	// order. Always non-nil; empty when INDEX.md is missing (present=false
	// or no table).
	IndexTasks []specs.IndexTask `json:"index_tasks"`

	// ADRs holds decisions/NNN-slug.md metadata, sorted by number. Always
	// non-nil.
	ADRs []specs.ADRMeta `json:"adrs"`

	// Findings holds the deterministic tier-1 integrity findings, sorted by
	// kind, file, detail. Always non-nil.
	Findings []specs.Finding `json:"findings"`

	// Counts aggregates the collections for panel badges and filters.
	Counts SpecsCountsDTO `json:"counts"`
}

// SpecsCountsDTO is the aggregate badge model of SpecsStatusDTO: the sizes
// of the collections plus the per-kind findings histogram. The map is
// ALWAYS non-nil (an empty tree serializes {}); it lists only kinds that
// actually occurred, keyed by the stringified specs.FindingKind.
type SpecsCountsDTO struct {
	Files          int            `json:"files"`
	ADRs           int            `json:"adrs"`
	Findings       int            `json:"findings"`
	FindingsByKind map[string]int `json:"findings_by_kind"`
}

// ---------------------------------------------------------------------------
// GetSpecsStatus
// ---------------------------------------------------------------------------

// GetSpecsStatus returns the parsed specs/ tree of a project for the Specs
// sidebar panel (ADR-105): files, the INDEX.md task navigation table, ADR
// metadata, and the always-fresh tier-1 integrity findings — one light
// read-only parse per call. The specs root is always <workspace>/specs
// (config.ProjectSpecsPath); a missing or partial tree parses to the neutral
// present=false state, never an error. No Project returns the same neutral
// state (the workspace tab strip — and with it the Specs panel — is not
// rendered there at all; this is the defensive contract, mirroring
// GetResearchStatus). See specs/decisions/105-specs-sidebar-panel.md.
func (f *FrontendAPI) GetSpecsStatus(projectID string) (*SpecsStatusDTO, error) {
	f.seedAcquire()
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}

	// Project resolution mirrors loadProjectForResearch / GetResearchStatus,
	// except No Project is the neutral present=false state here, not an
	// error — the panel is simply absent in CHAT mode (ADR-105 D1).
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project %q: %w", projectID, err)
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if proj.IsNoProject {
		return specsStatusDTOFromRoot(nil, ""), nil
	}

	specsRoot := config.ProjectSpecsPath(proj.WorkspacePath)
	root, err := specs.ParseSpecsRoot(specsRoot)
	if err != nil {
		// The parser is tolerant by contract (a missing or partial tree is
		// present=false, never an error); fail open to the neutral state if
		// a future revision grows an error path — the parseResearchRootBestEffort
		// pattern.
		f.log().Debug("GetSpecsStatus: specs root not parseable",
			"root", specsRoot, "error", err)
		return specsStatusDTOFromRoot(nil, specsRoot), nil
	}
	return specsStatusDTOFromRoot(root, specsRoot), nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// specsStatusDTOFromRoot maps a parsed specs root to the wire DTO with the
// stable non-null shape: every collection non-nil even when empty, and the
// counts histogram always allocated. A nil root (No Project, or a parse the
// tolerant parser refused) maps to present=false, keeping rootPath.
func specsStatusDTOFromRoot(root *specs.SpecsRoot, rootPath string) *SpecsStatusDTO {
	dto := &SpecsStatusDTO{
		Root:       rootPath,
		Files:      []specs.SpecFile{},
		IndexTasks: []specs.IndexTask{},
		ADRs:       []specs.ADRMeta{},
		Findings:   []specs.Finding{},
		Counts:     SpecsCountsDTO{FindingsByKind: map[string]int{}},
	}
	if root == nil {
		return dto
	}

	dto.Present = root.Present
	if root.Files != nil {
		dto.Files = root.Files
	}
	if root.Index != nil && root.Index.Tasks != nil {
		dto.IndexTasks = root.Index.Tasks
	}
	if root.ADRs != nil {
		dto.ADRs = root.ADRs
	}
	if root.Findings != nil {
		dto.Findings = root.Findings
	}

	dto.Counts.Files = len(dto.Files)
	dto.Counts.ADRs = len(dto.ADRs)
	dto.Counts.Findings = len(dto.Findings)
	for _, finding := range dto.Findings {
		dto.Counts.FindingsByKind[string(finding.Kind)]++
	}
	return dto
}
