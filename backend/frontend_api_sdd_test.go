package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/sp4rk/skills"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// sddTestFrontend wires a FrontendAPI with the given projects persisted to an
// in-memory store and a temp agent dir whose global skills directory is the
// only discovery root (config.Skills.Dirs is left empty, so the harness
// exercises the production fallback). Projects without an explicit
// WorkspacePath get <base>/ws-<ID>.
func sddTestFrontend(t *testing.T, infos ...project.ProjectInfo) (api *FrontendAPI, workspaces map[string]string, agentDir string) {
	t.Helper()
	base := t.TempDir()
	agentDir = filepath.Join(base, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	workspaces = make(map[string]string, len(infos))
	for _, info := range infos {
		if info.WorkspacePath == "" {
			info.WorkspacePath = filepath.Join(base, "ws-"+info.ID)
		}
		workspaces[info.ID] = info.WorkspacePath
		if err := store.SaveProject(context.Background(), info); err != nil {
			t.Fatalf("save project %s: %v", info.ID, err)
		}
	}

	// A defaults-applied config whose skill discovery is confined to the temp
	// agent dir, so the harness never reads the developer's real ~/.c0wrk —
	// detection stays hermetic while config.Save/Load remain valid.
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.Skills.Dirs = []string{config.SkillsDir(agentDir)}
	cfg.LLM.DefaultModel = "anthropic/claude-3-opus"
	cfg.LLM.Anthropic.APIKey = "sk-test"
	cfg.LLM.Anthropic.Models = []string{"claude-3-opus"}

	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		agentDir:       agentDir,
		config:         cfg,
		configPath:     filepath.Join(base, "config.yaml"),
	}
	api.seedPublished.Store(true)
	return api, workspaces, agentDir
}

// restartSDDFrontend returns a fresh FrontendAPI over the SAME project store
// and agent dir — the app-restart shape.
func restartSDDFrontend(api *FrontendAPI, agentDir string) *FrontendAPI {
	restarted := &FrontendAPI{
		projectManager: api.projectManager,
		projStore:      api.projStore,
		agentDir:       agentDir,
		config:         &config.Config{},
		configPath:     api.configPath,
	}
	restarted.seedPublished.Store(true)
	return restarted
}

// seedGlobalSkill installs one skill in the c0wrk global skills directory.
func seedGlobalSkill(t *testing.T, agentDir, name string) {
	t.Helper()
	writeSkill(t, filepath.Join(config.SkillsDir(agentDir), name))
}

// seedProjectSkill installs one skill in a workspace's own .agents/skills.
func seedProjectSkill(t *testing.T, ws, name string) {
	t.Helper()
	writeSkill(t, filepath.Join(ws, ".agents", "skills", name))
}

// writeSkill materializes a minimal skill directory.
func writeSkill(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: "+filepath.Base(dir)+"\ndescription: test\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", dir, err)
	}
}

// writeSpecsTree seeds a structurally complete sdd-framework tree under ws
// plus the workspace Key File it references. It deliberately carries organic
// findings (a duplicate ADR number, INDEX_STALE entries, a dangling
// supersession) so the model is exercised, but tests assert specific records
// rather than a total, because the framework — not this test — owns the check
// vocabulary.
func writeSpecsTree(t *testing.T, ws string) {
	t.Helper()
	files := map[string]string{
		"INDEX.md": "# Spec Index\n\n" +
			"## Navigation by Task\n\n" +
			"| Task | Read these specs |\n" +
			"|---|---|\n" +
			"| Add a tool | [tools](contracts/tools.md) |\n\n" +
			"## Directory Listing\n\n" +
			"- [architecture/layers.md](architecture/layers.md)\n" +
			"- [contracts/tools.md](contracts/tools.md)\n" +
			"- [decisions/001-first.md](decisions/001-first.md)\n",
		"META.md":                            "# Meta\n\nSpec formats.\n",
		"architecture/layers.md":             "# Layers\n\n## Key Files\n\n- `core/tools/registry.go` - the registry\n",
		"contracts/tools.md":                 "# Tool contract\n\nSee [layers](../architecture/layers.md).\n",
		"decisions/001-first.md":             "# ADR-001: First decision\n\n## Status\n\nAccepted\n",
		"decisions/001-dup.md":               "# ADR-001: Duplicate number\n\n## Status\n\nSuperseded by [ADR-002](002-elsewhere.md)\n",
		"domains/orchestration/conductor.md": "# Conductor\n\nNot listed in INDEX.\n",
	}
	for rel, content := range files {
		writeSpecsFile(t, ws, "specs/"+rel, content)
	}
	writeSpecsFile(t, ws, "core/tools/registry.go", "package tools\n")
}

// findDocument returns the document at a workspace-relative path.
func findDocument(docs []SDDDocumentDTO, path string) (SDDDocumentDTO, bool) {
	for _, doc := range docs {
		if doc.Path == path {
			return doc, true
		}
	}
	return SDDDocumentDTO{}, false
}

// detectedIDs returns the detected framework ids sorted.
func detectedIDs(dto *SpecsStatusDTO) []string {
	ids := make([]string, 0, len(dto.Detected))
	for _, fw := range dto.Detected {
		ids = append(ids, fw.ID)
	}
	sort.Strings(ids)
	return ids
}

// assertSDDWireShape pins the stable non-null wire contract: every collection
// and both count maps serialize as [] / {} (never null).
func assertSDDWireShape(t *testing.T, dto *SpecsStatusDTO) {
	t.Helper()
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{
		"present", "tree_present", "root", "workspace", "detected",
		"selector_needed", "roots", "documents", "nodes", "views", "findings",
		"actions", "counts", "warnings",
	} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire missing key %q", key)
		}
	}
	for _, key := range []string{"detected", "roots", "documents", "nodes", "views", "findings", "actions", "warnings"} {
		if string(wire[key]) == "null" {
			t.Errorf("wire key %q is null — the DTO must serialize stable non-nil collections", key)
		}
	}
	var counts map[string]json.RawMessage
	if err := json.Unmarshal(wire["counts"], &counts); err != nil {
		t.Fatalf("unmarshal counts: %v", err)
	}
	for _, key := range []string{"documents", "findings", "by_concept", "findings_by_kind"} {
		if _, ok := counts[key]; !ok {
			t.Errorf("counts wire missing key %q", key)
		}
	}
	for _, key := range []string{"by_concept", "findings_by_kind"} {
		if string(counts[key]) == "null" {
			t.Errorf("counts.%s is null — the histogram must be a non-nil map", key)
		}
	}
}

// ---------------------------------------------------------------------------
// detection + parsing
// ---------------------------------------------------------------------------

// TestGetSpecsStatus_DetectsInstalledFrameworks pins the framework-aware
// contract: the global vibespec-* skill recognises the VibeSpec framework, the
// project's openspec-* skills recognise OpenSpec, both appear in `detected`,
// the tree-bearing framework is the default active one, and the parsed model
// is the vibespec tree (documents, nodes, roots, actions).
func TestGetSpecsStatus_DetectsInstalledFrameworks(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	seedGlobalSkill(t, agentDir, "vibespec-check")
	seedProjectSkill(t, ws, "openspec-propose")
	seedProjectSkill(t, ws, "openspec-apply-change")
	writeSpecsTree(t, ws)

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present {
		t.Fatal("Present = false, want true (gate on + detections)")
	}
	if got := detectedIDs(status); len(got) != 2 || got[0] != "openspec" || got[1] != "vibespec" {
		t.Fatalf("detected ids = %v, want [openspec vibespec]", got)
	}
	// The vibespec tree exists, so tree-present outranks OpenSpec's stronger skill
	// scope — vibespec is the default active framework.
	if status.ActiveID != "vibespec" {
		t.Errorf("active_id = %q, want %q (tree present wins the default order)", status.ActiveID, "vibespec")
	}
	if status.Active == nil || status.Active.ID != "vibespec" || !status.Active.TreePresent {
		t.Errorf("active = %+v, want the vibespec detection with tree present", status.Active)
	}
	if !status.SelectorNeeded {
		t.Error("selector_needed = false, want true (two frameworks, no persisted choice)")
	}
	if len(status.Roots) != 1 || status.Roots[0].Path != "specs" || !status.Roots[0].Present {
		t.Errorf("roots = %+v, want the present specs root", status.Roots)
	}
	if !status.TreePresent {
		t.Error("tree_present = false, want true")
	}
	if want := filepath.Join(ws, "specs"); status.Root != want {
		t.Errorf("root = %q, want %q", status.Root, want)
	}

	// Documents are concept instances with DSL-driven extraction.
	layers, ok := findDocument(status.Documents, "specs/architecture/layers.md")
	if !ok {
		t.Fatalf("documents lack specs/architecture/layers.md: %v", documentPaths(status.Documents))
	}
	if layers.Concept != "architecture" || layers.Title != "Layers" {
		t.Errorf("layers document = %+v, want concept architecture, title Layers", layers)
	}
	if len(layers.KeyFiles) != 1 || layers.KeyFiles[0] != "core/tools/registry.go" {
		t.Errorf("layers key_files = %v, want [core/tools/registry.go]", layers.KeyFiles)
	}
	index, ok := findDocument(status.Documents, "specs/INDEX.md")
	if !ok || len(index.Tasks) != 1 || index.Tasks[0].Task != "Add a tool" {
		t.Errorf("INDEX document = %+v, want the one navigation row", index)
	}
	decision, ok := findDocument(status.Documents, "specs/decisions/001-dup.md")
	if !ok || decision.Number != "001" || decision.Status != "superseded" || decision.SupersededBy != "002" {
		t.Errorf("decision document = %+v, want number 001, superseded by 002", decision)
	}

	// Nodes and views are the framework's rendered forest sections.
	if len(status.Nodes) == 0 {
		t.Error("nodes = empty, want the rendered tree")
	}
	if len(status.Views) == 0 {
		t.Error("views = empty, want the declared sections")
	}
	// Actions are DSL-driven; the tree-bearing check action is available.
	var checkAction *SDDActionDTO
	for i := range status.Actions {
		if status.Actions[i].Kind == "check" {
			checkAction = &status.Actions[i]
		}
	}
	if checkAction == nil {
		t.Fatalf("actions = %+v, want a check action", status.Actions)
	}
	if !checkAction.Available {
		t.Error("check action available = false, want true on a present root")
	}

	if status.Counts.Documents != len(status.Documents) || status.Counts.Findings != len(status.Findings) {
		t.Errorf("counts = %+v, want documents=%d findings=%d", status.Counts, len(status.Documents), len(status.Findings))
	}
	if len(status.Counts.ByConcept) == 0 {
		t.Error("counts.by_concept = empty, want the concept histogram")
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_NoDetectionHidesSurface pins the visibility invariant: no
// installed skills means no detected framework, which hides the surface even
// when a specs/ tree exists on disk.
func TestGetSpecsStatus_NoDetectionHidesSurface(t *testing.T) {
	api, workspaces, _ := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	writeSpecsTree(t, workspaces["proj-1"])

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.Present {
		t.Error("present = true, want false without a detected framework")
	}
	if len(status.Detected) != 0 || status.Active != nil || status.ActiveID != "" {
		t.Errorf("detected=%v active=%+v activeID=%q, want none", status.Detected, status.Active, status.ActiveID)
	}
	if status.SelectorNeeded {
		t.Error("selector_needed = true without any framework")
	}
	if status.TreePresent {
		t.Error("tree_present = true without an active framework")
	}
	if status.Documents == nil || len(status.Documents) != 0 {
		t.Errorf("documents = %v, want a non-nil empty slice", status.Documents)
	}
	if status.Warnings == nil {
		t.Error("warnings = nil, want a non-nil empty slice")
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_ActionSkillResolvesToInstalled pins the action contract:
// the DSL declares vibespec-* names and the workspace carries that exact copy,
// so the action DTO reports the declared name as available — the name a
// dispatch can actually invoke.
func TestGetSpecsStatus_ActionSkillResolvesToInstalled(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	seedGlobalSkill(t, agentDir, "vibespec-check")
	writeSpecsTree(t, ws)
	// The callable catalog rides the builder's discovery set; the stub hands
	// back the one installed descriptor (the real builder reports the seeded
	// skill the same way), so this test pins the resolution contract, not full
	// discovery.
	api.builderOverride = &mockBuilder{
		skillDirsRes:           []string{config.SkillsDir(agentDir)},
		getSkillDescriptorsRes: []skills.SkillDescriptor{{Name: "vibespec-check", Description: "test"}},
	}

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present || status.ActiveID != "vibespec" {
		t.Fatalf("present=%v active_id=%q, want the vibespec detection", status.Present, status.ActiveID)
	}
	var checkAction *SDDActionDTO
	for i := range status.Actions {
		if status.Actions[i].Kind == "check" {
			checkAction = &status.Actions[i]
		}
	}
	if checkAction == nil {
		t.Fatalf("actions = %+v, want a check action", status.Actions)
	}
	if checkAction.Skill != "vibespec-check" {
		t.Errorf("check action skill = %q, want the installed vibespec-check name", checkAction.Skill)
	}
	if !checkAction.Available {
		t.Error("check action available = false, want true (the declared skill is installed)")
	}
	// The catalog gate is per action: an action whose declared skill is
	// genuinely absent (init wants vibespec-init; only vibespec-check is
	// installed) stays visible but INERT even though it declares no tree
	// requirements — a partial skill install must not offer a gesture that
	// would silently run as a plain prompted task.
	var initAction *SDDActionDTO
	for i := range status.Actions {
		if status.Actions[i].Kind == "init" {
			initAction = &status.Actions[i]
		}
	}
	if initAction == nil {
		t.Fatalf("actions = %+v, want an init action", status.Actions)
	}
	if initAction.Skill != "vibespec-init" {
		t.Errorf("init action skill = %q, want the declared spelling (nothing installed resolves to it)", initAction.Skill)
	}
	if initAction.Available {
		t.Error("init action available = true, want false (its skill is not callable)")
	}
	// The skill fact is reported SEPARATELY from the tree requirements: a
	// partially installed skill set (detection matches all_of plus one any_of)
	// blocks a gesture on a tree that satisfies everything, and init declares no
	// requirements at all — so the skill is its ONLY reason to be inert. Folding
	// the two into `available` alone made every surface label this state "Not
	// available for this tree yet", sending the operator to look for a tree
	// problem that does not exist.
	if initAction.SkillAvailable {
		t.Error("init action skill_available = true, want false (vibespec-init is not installed)")
	}
	if len(initAction.Requires) != 0 {
		t.Errorf("init action requires = %v, want none — the missing skill must be the sole cause", initAction.Requires)
	}
	if !checkAction.SkillAvailable {
		t.Error("check action skill_available = false, want true (the declared skill is installed)")
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_LegacySpecPrefixIsNotEvidence pins the retired alias from
// the detection side: a skill directory left behind by the pre-rename c0wrk
// skillset (`spec-check`) is an ordinary agent skill, but its name is not one
// the VibeSpec declaration lists, so it recognises no framework and the Specs
// surface stays hidden. Only the upstream `vibespec-*` spelling is evidence.
func TestGetSpecsStatus_LegacySpecPrefixIsNotEvidence(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	seedGlobalSkill(t, agentDir, "spec-check")
	writeSpecsTree(t, ws)

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.Present {
		t.Error("present = true, want false (a legacy spec-* skill is not vibespec evidence)")
	}
	if len(status.Detected) != 0 {
		t.Errorf("detected = %v, want none", status.Detected)
	}
	if status.ActiveID != "" {
		t.Errorf("active_id = %q, want empty without a detected framework", status.ActiveID)
	}
}

// TestResolveDeclaredPathRejectsNonRelativeDeclarations pins the probe-side
// twin of the load-time path validation: only clean workspace-relative
// spellings resolve, whatever a future call site feeds in — the `~`-relative
// and absolute forms the DSL validator already rejects at load cannot sneak
// back through the runtime probe. Every rooted dialect is spelled literally
// (POSIX, UNC, drive-rooted, and their backslash forms) so the pin does not
// inherit the running host's notion of "absolute": filepath.IsAbs alone calls
// the POSIX and UNC spellings relative on Windows.
func TestResolveDeclaredPathRejectsNonRelativeDeclarations(t *testing.T) {
	ws := t.TempDir()
	absolute := filepath.Clean(string(filepath.Separator) + filepath.Join("etc", "specs"))
	invalid := []string{
		"", "   ", "~", "~/specs", "a/../specs",
		absolute, "/etc/specs", "//server/share/specs", "C:/specs", `C:\specs`, `\etc\specs`,
	}
	for _, declared := range invalid {
		if resolved, ok := resolveDeclaredPath(ws, declared); ok {
			t.Errorf("resolveDeclaredPath(%q) = %q, want rejection", declared, resolved)
		}
	}
	resolved, ok := resolveDeclaredPath(ws, "specs")
	if !ok || resolved != filepath.Join(ws, "specs") {
		t.Fatalf("resolveDeclaredPath(specs) = %q, %v; want %q, true", resolved, ok, filepath.Join(ws, "specs"))
	}
	// The backslash spelling normalises to the same path on every host: a
	// declaration is slash-separated by contract, and the probe reads either
	// separator identically instead of depending on the running OS.
	nested, ok := resolveDeclaredPath(ws, `specs\sub`)
	if !ok || nested != filepath.Join(ws, "specs", "sub") {
		t.Fatalf("resolveDeclaredPath(specs\\sub) = %q, %v; want %q, true",
			nested, ok, filepath.Join(ws, "specs", "sub"))
	}
}

// TestResolveActionSkill pins the declared→installed skill resolution the
// action DTO runs: the declared name is available when the catalog carries that
// exact name, a differently spelled installation (the retired `spec-check`
// spelling) does not satisfy it, an absent skill stays declared and flags the
// action not-callable, an undeterminable catalog does not gate, and a
// skill-less action is left to the dispatch layer.
func TestResolveActionSkill(t *testing.T) {
	callable := map[string]bool{"vibespec-check": true, "openspec-propose": true}

	if got, ok := resolveActionSkill("vibespec-check", callable); !ok || got != "vibespec-check" {
		t.Errorf("exact = %q, %v; want vibespec-check, true", got, ok)
	}
	if got, ok := resolveActionSkill("spec-check", callable); ok || got != "spec-check" {
		t.Errorf("retired spelling = %q, %v; want the declared spelling, false (names match exactly)", got, ok)
	}
	if got, ok := resolveActionSkill("spec-init", callable); ok || got != "spec-init" {
		t.Errorf("absent = %q, %v; want the declared spelling, false", got, ok)
	}
	if got, ok := resolveActionSkill("vibespec-check", nil); !ok || got != "vibespec-check" {
		t.Errorf("nil catalog = %q, %v; want the declared spelling, true (ungated)", got, ok)
	}
	if got, ok := resolveActionSkill("", callable); !ok || got != "" {
		t.Errorf("skill-less = %q, %v; want empty, true (the dispatch layer decides)", got, ok)
	}
}

// TestGetSpecsStatus_MissingRootsIsEmptyTree pins the detected-but-unbuilt
// state: skills recognise a framework, its roots are absent, so the tree is
// empty while the surface stays visible (the panel's empty state).
func TestGetSpecsStatus_MissingRootsIsEmptyTree(t *testing.T) {
	api, _, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	seedGlobalSkill(t, agentDir, "vibespec-check")

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present {
		t.Fatal("present = false, want true (detected framework, gate on)")
	}
	if status.ActiveID != "vibespec" {
		t.Errorf("active_id = %q, want vibespec", status.ActiveID)
	}
	if status.TreePresent {
		t.Error("tree_present = true, want false without specs/")
	}
	if len(status.Documents) != 0 || len(status.Findings) != 0 {
		t.Errorf("documents=%d findings=%d, want an empty model", len(status.Documents), len(status.Findings))
	}
	if len(status.Roots) != 1 || status.Roots[0].Present {
		t.Errorf("roots = %+v, want the absent specs root reported", status.Roots)
	}
	for _, action := range status.Actions {
		if action.Kind == "check" && action.Available {
			t.Error("check action available = true without a root, want false")
		}
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_NoProjectIsNeutral pins the No-Project contract: the
// pseudo-project yields the neutral present=false state without an error.
func TestGetSpecsStatus_NoProjectIsNeutral(t *testing.T) {
	api, _, _ := sddTestFrontend(t, project.ProjectInfo{
		ID:   project.NoProjectID,
		Name: "No Project",
	})

	status, err := api.GetSpecsStatus(project.NoProjectID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (No Project): %v", err)
	}
	if status.Present || status.TreePresent {
		t.Errorf("present=%v tree_present=%v, want the neutral false state", status.Present, status.TreePresent)
	}
	if status.Root != "" || status.Workspace != "" {
		t.Errorf("root=%q workspace=%q, want empty", status.Root, status.Workspace)
	}
	if len(status.Detected) != 0 || status.Active != nil {
		t.Errorf("detected=%v active=%+v, want none", status.Detected, status.Active)
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_Guards pins the argument and lookup guards.
func TestGetSpecsStatus_Guards(t *testing.T) {
	api, _, _ := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})

	if _, err := api.GetSpecsStatus(""); err == nil {
		t.Error("empty project_id: got no error, want one")
	}
	if _, err := api.GetSpecsStatus("no-such-project"); err == nil {
		t.Error("unknown project: got no error, want one")
	}

	bare := &FrontendAPI{}
	bare.seedPublished.Store(true)
	if _, err := bare.GetSpecsStatus("proj-1"); err == nil {
		t.Error("nil project manager: got no error, want one")
	}
}

// TestGetSpecsStatus_RereadsSkillsEveryCall pins that detection is never
// cached: a skill installed after the first read is recognised by the next
// call — the backend half of the skills:changed re-fetch contract.
func TestGetSpecsStatus_RereadsSkillsEveryCall(t *testing.T) {
	api, _, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})

	before, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (before): %v", err)
	}
	if before.Present {
		t.Fatal("present = true before any skill is installed")
	}

	seedGlobalSkill(t, agentDir, "vibespec-check")

	after, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (after): %v", err)
	}
	if !after.Present || after.ActiveID != "vibespec" {
		t.Errorf("after installing vibespec-check: present=%v active=%q, want a re-detected vibespec", after.Present, after.ActiveID)
	}
}

// ---------------------------------------------------------------------------
// sidebar gate
// ---------------------------------------------------------------------------

// TestSetSpecsEnabled_TogglesVisibilityWithoutTouchingDisk pins the pure
// visibility gate: the toggle persists, flips `present` on the next status
// read, announces config:updated, and leaves the already-installed vibespec-*
// skills untouched on disk.
func TestSetSpecsEnabled_TogglesVisibilityWithoutTouchingDisk(t *testing.T) {
	api, _, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	seedGlobalSkill(t, agentDir, "vibespec-check")
	seedGlobalSkill(t, agentDir, "vibespec-create")
	skillPath := filepath.Join(config.SkillsDir(agentDir), "vibespec-check", "SKILL.md")

	before, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (enabled): %v", err)
	}
	if !before.Present {
		t.Fatal("present = false with the gate on")
	}

	if err := api.SetSpecsEnabled(false); err != nil {
		t.Fatalf("SetSpecsEnabled(false): %v", err)
	}
	off, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (disabled): %v", err)
	}
	if off.Present {
		t.Error("present = true after disabling the gate")
	}
	// Detection is still reported — the gate hides the surface, it does not
	// pretend the frameworks are absent.
	if len(off.Detected) != 1 || off.ActiveID != "vibespec" {
		t.Errorf("detected=%v active=%q, want detection preserved while hidden", off.Detected, off.ActiveID)
	}

	// The already-installed skills are untouched.
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("disabling the gate removed an installed skill: %v", err)
	}

	persisted, err := config.Load(api.configPath)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if persisted.Specs.Enabled == nil || persisted.Specs.PackEnabled() {
		t.Errorf("persisted specs.enabled = %+v, want explicit off", persisted.Specs.Enabled)
	}

	if err := api.SetSpecsEnabled(true); err != nil {
		t.Fatalf("SetSpecsEnabled(true): %v", err)
	}
	on, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (re-enabled): %v", err)
	}
	if !on.Present {
		t.Error("present = false after re-enabling the gate")
	}
}

// ---------------------------------------------------------------------------
// active selection
// ---------------------------------------------------------------------------

// TestSetActiveSDD_PersistsAcrossRestartAndIsPerProject pins the selection
// contract: a non-default choice survives a restart, is reported by the next
// GetSpecsStatus, silences the selector, and never affects another project.
func TestSetActiveSDD_PersistsAcrossRestartAndIsPerProject(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t,
		project.ProjectInfo{ID: "proj-1", Name: "P1"},
		project.ProjectInfo{ID: "proj-2", Name: "P2"},
	)
	seedGlobalSkill(t, agentDir, "vibespec-check")
	ws1 := workspaces["proj-1"]
	seedProjectSkill(t, ws1, "openspec-propose")
	seedProjectSkill(t, ws1, "openspec-apply-change")
	writeSpecsTree(t, ws1)

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.ActiveID != "vibespec" {
		t.Fatalf("default active_id = %q, want vibespec", status.ActiveID)
	}
	if !status.SelectorNeeded {
		t.Fatal("selector_needed = false, want true before a choice")
	}

	if err := api.SetActiveSDD("proj-1", "openspec"); err != nil {
		t.Fatalf("SetActiveSDD: %v", err)
	}

	// Restart: a fresh FrontendAPI over the same store.
	restarted := restartSDDFrontend(api, agentDir)
	after, err := restarted.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus after restart: %v", err)
	}
	if after.ActiveID != "openspec" {
		t.Errorf("active_id after restart = %q, want the persisted openspec", after.ActiveID)
	}
	if after.SelectorNeeded {
		t.Error("selector_needed = true after an explicit choice")
	}

	// The other project is untouched (it detects only the global vibespec skills).
	other, err := restarted.GetSpecsStatus("proj-2")
	if err != nil {
		t.Fatalf("GetSpecsStatus (other project): %v", err)
	}
	if other.ActiveID != "vibespec" {
		t.Errorf("other project active_id = %q, want vibespec (no cross-project leak)", other.ActiveID)
	}
}

// TestSetActiveSDD_AcceptsSessionWorkspaceDetection pins offer/accept
// coherence: GetSpecsStatus detects in the session workspace (the tree it
// renders), so a framework whose evidence lives only in the active session's
// managed worktree — the state an agent leaves after running the framework's
// init inside its session tree — is OFFERED by the selector and must be
// accepted by SetActiveSDD, not refused as undetected. An id detected nowhere
// is still rejected.
func TestSetActiveSDD_AcceptsSessionWorkspaceDetection(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	seedGlobalSkill(t, h.agentDir, "vibespec-check")

	managed := h.api.mustManagedSession(t, "", true, "")
	ws := managed.WorkspaceBinding.WorkspacePath
	seedProjectSkill(t, ws, "openspec-propose")
	seedProjectSkill(t, ws, "openspec-apply-change")

	status, err := h.api.GetSpecsStatus(h.project.ID)
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	offered := false
	for _, fw := range status.Detected {
		if fw.ID == "openspec" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("detected = %+v, want openspec offered from the session workspace", status.Detected)
	}

	if err := h.api.SetActiveSDD(h.project.ID, "openspec"); err != nil {
		t.Fatalf("SetActiveSDD (session-workspace detection): %v", err)
	}
	if err := h.api.SetActiveSDD(h.project.ID, "nope"); err == nil {
		t.Error("SetActiveSDD with an id detected nowhere: got no error, want one")
	}
}

// TestSetActiveSDD_ClearsToDefaultAndValidates pins the clearing and
// validation halves of the write contract.
func TestSetActiveSDD_ClearsToDefaultAndValidates(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t,
		project.ProjectInfo{ID: "proj-1", Name: "P1"},
		project.ProjectInfo{ID: project.NoProjectID, Name: "No Project"},
	)
	seedGlobalSkill(t, agentDir, "vibespec-check")
	ws1 := workspaces["proj-1"]
	seedProjectSkill(t, ws1, "openspec-propose")
	seedProjectSkill(t, ws1, "openspec-apply-change")

	if err := api.SetActiveSDD("proj-1", "nope"); err == nil {
		t.Error("SetActiveSDD with an undetected id: got no error, want one")
	}
	if err := api.SetActiveSDD("", "vibespec"); err == nil {
		t.Error("SetActiveSDD with an empty project id: got no error, want one")
	}
	if err := api.SetActiveSDD(project.NoProjectID, "vibespec"); err == nil {
		t.Error("SetActiveSDD for No Project: got no error, want one")
	}

	if err := api.SetActiveSDD("proj-1", "openspec"); err != nil {
		t.Fatalf("SetActiveSDD(openspec): %v", err)
	}
	if err := api.SetActiveSDD("proj-1", ""); err != nil {
		t.Fatalf("SetActiveSDD(clear): %v", err)
	}
	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	// After clearing, detection's default applies again (openspec has the
	// stronger project skill scope and vibespec has no tree, so openspec wins).
	if status.ActiveID != "openspec" {
		t.Errorf("active_id after clearing = %q, want the detection default openspec", status.ActiveID)
	}
}

// TestGetSpecsStatus_OpenSpecDirectoryConcepts pins the DSL-driven action
// availability for a DIRECTORY concept: an OpenSpec change folder satisfies
// `concept:change`, so the change-scoped actions become available and the
// change renders as a directory node.
func TestGetSpecsStatus_OpenSpecDirectoryConcepts(t *testing.T) {
	api, workspaces, _ := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	seedProjectSkill(t, ws, "openspec-propose")
	seedProjectSkill(t, ws, "openspec-apply-change")
	writeSpecsFile(t, ws, "openspec/changes/add-login/proposal.md", "# Add login\n")

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present || status.ActiveID != "openspec" {
		t.Fatalf("present=%v active=%q, want the detected OpenSpec framework", status.Present, status.ActiveID)
	}
	if !status.TreePresent {
		t.Fatal("tree_present = false, want true for openspec/changes")
	}

	var changeNode *SDDNodeDTO
	for i := range status.Nodes {
		if status.Nodes[i].Kind == "directory" && status.Nodes[i].Concept == "change" {
			changeNode = &status.Nodes[i]
		}
	}
	if changeNode == nil {
		t.Fatalf("nodes = %+v, want a change directory node", status.Nodes)
	}
	if changeNode.Path != "openspec/changes/add-login" {
		t.Errorf("change node path = %q, want openspec/changes/add-login", changeNode.Path)
	}

	var applyAction *SDDActionDTO
	for i := range status.Actions {
		if status.Actions[i].Kind == "apply" {
			applyAction = &status.Actions[i]
		}
	}
	if applyAction == nil {
		t.Fatalf("actions = %+v, want an apply action", status.Actions)
	}
	if !applyAction.Available {
		t.Error("apply available = false, want true (a change folder exists)")
	}
	assertSDDWireShape(t, status)
}

// documentPaths returns the document paths for failure output.
func documentPaths(docs []SDDDocumentDTO) []string {
	paths := make([]string, 0, len(docs))
	for _, doc := range docs {
		paths = append(paths, doc.Path)
	}
	return paths
}

// writeRawSkillMD materializes a skill directory whose SKILL.md carries the raw
// content given — the invalid-document shapes the detection evidence must
// reject (a directory with a SKILL.md file is not automatically an installed
// skill). Distinct from writeSkillMD, which always writes a valid document.
func writeRawSkillMD(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// TestGetSpecsStatus_WorkspacelessProjectIsNeutral pins the empty-workspace
// guard: a project whose WorkspacePath is "" (an ordinary project, not the
// No Project pseudo-project) never resolves the operator DSL file or the tree
// against the process CWD — detection cannot fire and the neutral DTO comes
// back, the same exit shape research and papers take.
func TestGetSpecsStatus_WorkspacelessProjectIsNeutral(t *testing.T) {
	base := t.TempDir()
	agentDir := filepath.Join(base, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID: "proj-1", Name: "P", WorkspacePath: "",
	}); err != nil {
		t.Fatalf("save workspace-less project: %v", err)
	}
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.Skills.Dirs = []string{config.SkillsDir(agentDir)}
	// A valid vibespec skill in the global dir would otherwise be detection
	// evidence — exactly the state the guard must keep inert for a
	// workspace-less project.
	seedGlobalSkill(t, agentDir, "vibespec-check")

	api := &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		agentDir:       agentDir,
		config:         cfg,
		configPath:     filepath.Join(base, "config.yaml"),
	}
	api.seedPublished.Store(true)

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.Present || status.TreePresent {
		t.Errorf("present=%v tree_present=%v, want the neutral shape", status.Present, status.TreePresent)
	}
	if status.Workspace != "" || status.Active != nil || status.ActiveID != "" {
		t.Errorf("workspace=%q active=%+v active_id=%q, want empty", status.Workspace, status.Active, status.ActiveID)
	}
	if len(status.Detected) != 0 {
		t.Errorf("detected = %v, want none (a CWD- or global-dir-only detection must not render)", status.Detected)
	}
	if status.Warnings == nil {
		t.Error("warnings = nil, want a non-nil empty slice")
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_StalePersistedSelectionWarns pins the visible-fallback
// contract: the selection persists per project while detection runs per
// workspace, so a framework whose skills disappear (a worktree-only install
// gone with the tree, an uninstall) silently falls back to the detection
// default — and the DTO must NAME the stale selection instead of quietly
// showing a different framework.
func TestGetSpecsStatus_StalePersistedSelectionWarns(t *testing.T) {
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	ws := workspaces["proj-1"]
	seedGlobalSkill(t, agentDir, "vibespec-check")
	seedProjectSkill(t, ws, "openspec-propose")
	seedProjectSkill(t, ws, "openspec-apply-change")

	if err := api.SetActiveSDD("proj-1", "openspec"); err != nil {
		t.Fatalf("SetActiveSDD: %v", err)
	}

	// The framework's skills vanish from this workspace after the choice.
	if err := os.RemoveAll(filepath.Join(ws, ".agents")); err != nil {
		t.Fatalf("remove project skills: %v", err)
	}

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.ActiveID != "vibespec" {
		t.Errorf("active_id = %q, want the detection default vibespec after the fallback", status.ActiveID)
	}
	warned := false
	for _, w := range status.Warnings {
		if strings.Contains(w, "openspec") && strings.Contains(w, "vibespec") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %v, want one naming the stale %q and the shown %q", status.Warnings, "openspec", "vibespec")
	}
	assertSDDWireShape(t, status)
}

// TestGetSpecsStatus_InvalidSkillMDIsNotEvidence pins the evidence contract:
// detection counts a skill only when the SDK parser accepts its SKILL.md (the
// same ParseSkill the callable SkillManager runs) — a directory carrying an
// empty, description-less, malformed or name-mismatched document is NOT an
// installed skill, so it cannot recognize a framework whose skills could
// never activate. A valid lower-tier copy still provides evidence under an
// invalid higher-tier one.
func TestGetSpecsStatus_InvalidSkillMDIsNotEvidence(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty document", content: ""},
		{name: "missing description", content: "---\nname: vibespec-check\n---\nBody."},
		{name: "malformed frontmatter", content: "---\nname: vibespec-check\ndescription: [unclosed\n---\nBody."},
		{name: "name mismatch", content: "---\nname: other-name\ndescription: test\n---\nBody."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, _, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
			writeRawSkillMD(t, filepath.Join(config.SkillsDir(agentDir), "vibespec-check"), tt.content)

			status, err := api.GetSpecsStatus("proj-1")
			if err != nil {
				t.Fatalf("GetSpecsStatus: %v", err)
			}
			if status.Present {
				t.Errorf("present = true, want false (invalid SKILL.md is not detection evidence)")
			}
			if len(status.Detected) != 0 {
				t.Errorf("detected = %v, want none", status.Detected)
			}
		})
	}

	// The tier invariant: an invalid project-local copy never masks the valid
	// global one — evidence comes from whatever copy actually parses.
	api, workspaces, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	writeRawSkillMD(t, filepath.Join(workspaces["proj-1"], ".agents", "skills", "vibespec-check"),
		"---\nname: vibespec-check\n---\nBody.")
	seedGlobalSkill(t, agentDir, "vibespec-check")

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if !status.Present || status.ActiveID != "vibespec" {
		t.Errorf("present=%v active_id=%q, want the valid global copy to provide evidence", status.Present, status.ActiveID)
	}
}

// TestBaseSkillDirs_ExplicitEmptyStaysEmpty pins the two-consumer agreement
// on an explicitly empty skills.dirs: detection resolves the same empty set
// activation does — a skill living only in the c0wrk global dir is neither
// visible nor callable — while the UNCONFIGURED state (nil, as in a bare test
// FrontendAPI) still falls back to the global dir on both sides.
func TestBaseSkillDirs_ExplicitEmptyStaysEmpty(t *testing.T) {
	api, _, agentDir := sddTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	seedGlobalSkill(t, agentDir, "vibespec-check")

	api.config.Skills.Dirs = []string{}
	if got := api.baseSkillDirs(); len(got) != 0 {
		t.Fatalf("baseSkillDirs with skills.dirs: [] = %v, want empty", got)
	}
	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}
	if status.Present || len(status.Detected) != 0 {
		t.Errorf("present=%v detected=%v, want none — the global dir is out of BOTH discovery sets",
			status.Present, status.Detected)
	}

	api.config.Skills.Dirs = nil
	got := api.baseSkillDirs()
	if len(got) != 1 || got[0] != config.SkillsDir(agentDir) {
		t.Fatalf("baseSkillDirs unconfigured = %v, want [%q]", got, config.SkillsDir(agentDir))
	}
	status, err = api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus after fallback: %v", err)
	}
	if !status.Present || status.ActiveID != "vibespec" {
		t.Errorf("present=%v active_id=%q, want the global-dir fallback to detect vibespec", status.Present, status.ActiveID)
	}
}

// Review 111: companion:/absent: are refined PER NODE, not only tree-wide, so a
// holder that already has its artifact loses the gesture while a holder that
// lacks it keeps it. Both attachment shapes are covered: a sibling document in
// the same directory (Spec Kit's */plan.md beside */spec.md) and a child of a
// directory instance (an OpenSpec change's proposal.md).
func TestActionBlockedNodes_RefinesPerNodeGates(t *testing.T) {
	tree := &sdd.Tree{Nodes: []sdd.Node{
		{Path: "specs/001-a/spec.md", Concept: "feature"},
		{Path: "specs/001-a/plan.md", Concept: "plan"},
		{Path: "specs/002-b/spec.md", Concept: "feature"},
	}}
	plan := sdd.Action{Kind: "plan", Concept: "feature", Requires: []string{"root", "concept:feature", "absent:plan"}}
	if got := strings.Join(actionBlockedNodes(plan, tree), ","); got != "specs/001-a/spec.md" {
		t.Errorf("plan blocked at %q, want only the feature that already has plan.md", got)
	}

	tasks := sdd.Action{Kind: "tasks", Concept: "feature", Requires: []string{"companion:plan", "absent:tasks"}}
	if got := strings.Join(actionBlockedNodes(tasks, tree), ","); got != "specs/002-b/spec.md" {
		t.Errorf("tasks blocked at %q, want only the feature still lacking plan.md", got)
	}

	if got := actionBlockedNodes(sdd.Action{Kind: "x", Concept: "feature", Requires: []string{"root"}}, tree); got != nil {
		t.Errorf("gate-free action blocked at %v, want none", got)
	}
	if got := actionBlockedNodes(plan, nil); got != nil {
		t.Errorf("nil tree blocked at %v, want none", got)
	}
	if got := actionBlockedNodes(plan, &sdd.Tree{Nodes: []sdd.Node{{Path: "specs/001-a/spec.md", Concept: "feature"}}}); got != nil {
		t.Errorf("unplanned-only tree blocked at %v, want none", got)
	}

	dirTree := &sdd.Tree{Nodes: []sdd.Node{
		{Path: "openspec/changes/add-mfa", Concept: "change"},
		{Path: "openspec/changes/add-mfa/proposal.md", Concept: "proposal"},
		{Path: "openspec/changes/add-mfa/design.md", Concept: "design"},
		{Path: "openspec/changes/other", Concept: "change"},
		{Path: "openspec/changes/other/proposal.md", Concept: "proposal"},
	}}
	design := sdd.Action{Kind: "design", Concept: "change", Requires: []string{"companion:proposal", "absent:design"}}
	if got := strings.Join(actionBlockedNodes(design, dirTree), ","); got != "openspec/changes/add-mfa" {
		t.Errorf("design blocked at %q, want only the change folder that has design.md", got)
	}
}
