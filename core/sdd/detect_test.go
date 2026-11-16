package sdd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
)

// Skill directory fixtures. Detection performs no I/O, so these are labels that
// name the discovery tier each skill would come from.
const (
	projectSkillDir   = "workspace/.agents/skills"
	frameworkSkillDir = "workspace/.claude/skills"
	globalSkillDir    = "agent/.agents/skills"
	userSkillDir      = "home/.agents/skills"
)

func installedSkill(dir, name string, scope sdd.SkillScope) sdd.InstalledSkill {
	return sdd.InstalledSkill{Name: name, Dir: dir + "/" + name, Scope: scope}
}

// projectSkill is a skill in the checkout's own .agents/skills tier.
func projectSkill(name string) sdd.InstalledSkill {
	return installedSkill(projectSkillDir, name, sdd.ScopeProject)
}

// frameworkSkill is a skill in an extra framework-declared directory inside the
// checkout (.claude/skills) — still the project tier.
func frameworkSkill(name string) sdd.InstalledSkill {
	return installedSkill(frameworkSkillDir, name, sdd.ScopeProject)
}

// globalSkill is a skill in c0wrk's own global directory.
func globalSkill(name string) sdd.InstalledSkill {
	return installedSkill(globalSkillDir, name, sdd.ScopeGlobal)
}

// userSkill is a skill in the user's shared agent directory.
func userSkill(name string) sdd.InstalledSkill {
	return installedSkill(userSkillDir, name, sdd.ScopeUser)
}

// builtinCatalog is the merged built-in framework catalog in declaration order.
func builtinCatalog(t *testing.T) []sdd.Definition {
	t.Helper()
	definitions, err := sdd.BuiltinDefinitions()
	if err != nil {
		t.Fatalf("BuiltinDefinitions() error = %v", err)
	}
	return definitions
}

// detect runs detection over the built-in catalog.
func detect(t *testing.T, evidence sdd.Evidence) sdd.DetectResult {
	t.Helper()
	return sdd.DetectInstalled(builtinCatalog(t), evidence)
}

// syntheticFramework builds a definition the way an operator file would
// contribute one, without round-tripping YAML.
func syntheticFramework(id string, priority int, skills sdd.Skills, roots, markers []string) sdd.Definition {
	framework := sdd.Framework{
		Schema:   sdd.SchemaV1,
		ID:       id,
		Label:    id,
		Priority: priority,
		Detect:   sdd.Detect{Markers: markers, Skills: skills},
	}
	for i, root := range roots {
		framework.Roots = append(framework.Roots, sdd.Root{
			ID:       fmt.Sprintf("root-%d", i),
			Path:     root,
			Required: true,
		})
	}
	return sdd.Definition{Framework: framework, Origin: sdd.OriginGlobal, Document: id + ".yaml"}
}

func recognizedIDs(result sdd.DetectResult) []string {
	ids := make([]string, 0, len(result.Frameworks))
	for _, detection := range result.Frameworks {
		ids = append(ids, detection.ID)
	}
	return ids
}

// requireRecognized asserts the exact recognised set and the Active invariant,
// then returns the default selection.
func requireRecognized(t *testing.T, result sdd.DetectResult, want ...string) sdd.Detection {
	t.Helper()
	if !result.Recognized {
		t.Fatalf("Recognized = false, want frameworks %v", want)
	}
	if got := recognizedIDs(result); !slices.Equal(got, want) {
		t.Fatalf("Frameworks = %v, want %v", got, want)
	}
	if result.Active.ID != result.Frameworks[0].ID {
		t.Fatalf("Active = %q, want Frameworks[0] = %q", result.Active.ID, result.Frameworks[0].ID)
	}
	return result.Active
}

func requireNotRecognized(t *testing.T, result sdd.DetectResult) {
	t.Helper()
	if result.Recognized {
		t.Fatalf("Recognized = true with frameworks %v, want none", recognizedIDs(result))
	}
	if len(result.Frameworks) != 0 {
		t.Errorf("Frameworks = %v, want empty", recognizedIDs(result))
	}
	if result.Active.ID != "" {
		t.Errorf("Active = %q, want the zero value", result.Active.ID)
	}
}

func matchedNames(detection sdd.Detection) []string {
	names := make([]string, 0, len(detection.Skills))
	for _, skill := range detection.Skills {
		names = append(names, skill.Name)
	}
	return names
}

// TestDetectInstalled_EmptyCatalogHidesSurface pins AC-3 and the D8 rule: with
// no installed skill the surface is hidden even though the tree and every marker
// are present — the tree never makes a framework visible.
func TestDetectInstalled_EmptyCatalogHidesSurface(t *testing.T) {
	result := detect(t, sdd.Evidence{
		Markers: []string{"openspec/config.yaml", "specs/META.md", ".specify/templates"},
		Roots:   []string{"openspec", "specs", ".specify"},
	})

	requireNotRecognized(t, result)
}

// TestDetectInstalled_OpenSpecFromProjectSkills pins AC-1: openspec installed in
// the project's .agents/skills tier is recognised from its all_of + any_of set.
func TestDetectInstalled_OpenSpecFromProjectSkills(t *testing.T) {
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		projectSkill("openspec-propose"),
		projectSkill("openspec-apply-change"),
	}})

	active := requireRecognized(t, result, "openspec")
	if active.Scope != sdd.ScopeProject {
		t.Errorf("Scope = %q, want %q", active.Scope, sdd.ScopeProject)
	}
	if want := []string{"openspec-propose", "openspec-apply-change"}; !slices.Equal(matchedNames(active), want) {
		t.Errorf("Skills = %v, want %v", matchedNames(active), want)
	}
	if active.Skills[0].Dir != projectSkillDir+"/openspec-propose" {
		t.Errorf("skill dir = %q, want the project copy", active.Skills[0].Dir)
	}
	if active.TreePresent() {
		t.Error("TreePresent = true, want false with no root or marker evidence")
	}
	if active.Origin != sdd.OriginBuiltin {
		t.Errorf("Origin = %q, want %q", active.Origin, sdd.OriginBuiltin)
	}
}

// TestDetectInstalled_SpecKitFromFrameworkSkills pins AC-2: a framework-declared
// directory outside .agents/skills (.claude/skills) is a first-class skill
// source, so spec-kit installed there is recognised.
func TestDetectInstalled_SpecKitFromFrameworkSkills(t *testing.T) {
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		frameworkSkill("speckit-plan"),
	}})

	active := requireRecognized(t, result, "spec-kit")
	if active.Scope != sdd.ScopeProject {
		t.Errorf("Scope = %q, want the project tier", active.Scope)
	}
	if want := frameworkSkillDir + "/speckit-plan"; active.Skills[0].Dir != want {
		t.Errorf("skill dir = %q, want %q", active.Skills[0].Dir, want)
	}
}

// TestDetectInstalled_EverySkillRecognisesVibeSpec pins AC-5: every skill of
// the declared set is evidence on its own — the declared any_of spans the whole
// skillset, lifecycle (init, explore) and continuous (consult, update, create,
// check) alike. The name must be the exact installed spelling: the detector
// folds no prefixes, so the vibespec-* names the skillset publishes are the
// only ones that count.
func TestDetectInstalled_EverySkillRecognisesVibeSpec(t *testing.T) {
	for _, name := range []string{
		"vibespec-check", "vibespec-init", "vibespec-explore", "vibespec-consult", "vibespec-create", "vibespec-update",
	} {
		t.Run(name, func(t *testing.T) {
			result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{userSkill(name)}})

			active := requireRecognized(t, result, "vibespec")
			if active.Scope != sdd.ScopeUser {
				t.Errorf("Scope = %q, want %q", active.Scope, sdd.ScopeUser)
			}
			if want := []string{name}; !slices.Equal(matchedNames(active), want) {
				t.Errorf("Skills = %v, want the installed spelling %v", matchedNames(active), want)
			}
			if active.Skills[0].Dir != userSkillDir+"/"+name {
				t.Errorf("skill dir = %q, want the user copy", active.Skills[0].Dir)
			}
		})
	}
}

// TestDetectInstalled_MissingAllOfIsNotRecognised pins AC-6: satisfying only the
// any_of half of openspec's set — with every marker and root present — is not a
// recognition, because all_of is not satisfied.
func TestDetectInstalled_MissingAllOfIsNotRecognised(t *testing.T) {
	result := detect(t, sdd.Evidence{
		Skills:  []sdd.InstalledSkill{projectSkill("openspec-apply-change")},
		Markers: []string{"openspec/config.yaml"},
		Roots:   []string{"openspec"},
	})

	requireNotRecognized(t, result)
}

// TestDetectInstalled_TreeOutranksScope pins the first clause of D9: a framework
// whose tree is present in the project is the default even when its skill
// evidence comes from a weaker tier.
func TestDetectInstalled_TreeOutranksScope(t *testing.T) {
	result := detect(t, sdd.Evidence{
		Skills: []sdd.InstalledSkill{
			projectSkill("vibespec-check"), // vibespec, strongest tier, no tree
			userSkill("openspec-propose"),  // openspec, weakest tier, tree present
			userSkill("openspec-apply-change"),
		},
		Roots: []string{"openspec"},
	})

	active := requireRecognized(t, result, "openspec", "vibespec")
	if active.Scope != sdd.ScopeUser {
		t.Errorf("Active.Scope = %q, want the user tier (the tree, not the scope, decided)", active.Scope)
	}
	if !active.RootPresent || active.MarkerPresent {
		t.Errorf("Active root/marker present = %v/%v, want true/false", active.RootPresent, active.MarkerPresent)
	}
	if result.Frameworks[1].TreePresent() {
		t.Error("vibespec TreePresent = true, want false")
	}
}

// TestDetectInstalled_ScopeOutranksDeclarationOrder pins the second clause of
// D9: with no tree evidence the strongest skill scope wins, even against a
// framework declared earlier.
func TestDetectInstalled_ScopeOutranksDeclarationOrder(t *testing.T) {
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		userSkill("vibespec-check"), // vibespec, declared first (priority 10)
		globalSkill("speckit-plan"), // spec-kit, stronger tier
	}})

	active := requireRecognized(t, result, "spec-kit", "vibespec")
	if active.Scope != sdd.ScopeGlobal {
		t.Errorf("Active.Scope = %q, want %q", active.Scope, sdd.ScopeGlobal)
	}
}

// TestDetectInstalled_DeclarationOrderBreaksTies pins the third clause of D9:
// equal tree signal and equal scope fall back to the catalog's declaration
// order (Priority, then ID).
func TestDetectInstalled_DeclarationOrderBreaksTies(t *testing.T) {
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		userSkill("vibespec-check"),
		userSkill("openspec-propose"),
		userSkill("openspec-apply-change"),
		userSkill("speckit-plan"),
	}})

	active := requireRecognized(t, result, "vibespec", "openspec", "spec-kit")
	if active.ID != "vibespec" {
		t.Errorf("Active = %q, want vibespec (priority 10)", active.ID)
	}
}

// TestDetectInstalled_UnknownScopeRanksLast pins the tier order: an unattributed
// skill copy never outranks a classified one.
func TestDetectInstalled_UnknownScopeRanksLast(t *testing.T) {
	unknown := installedSkill("elsewhere", "vibespec-check", sdd.ScopeUnknown)
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		unknown,                       // vibespec, declared first, unclassified
		userSkill("openspec-propose"), // openspec, user tier
		userSkill("openspec-apply-change"),
	}})

	active := requireRecognized(t, result, "openspec", "vibespec")
	if result.Frameworks[1].Scope != sdd.ScopeUnknown {
		t.Errorf("vibespec Scope = %q, want %q", result.Frameworks[1].Scope, sdd.ScopeUnknown)
	}
	if active.ID != "openspec" {
		t.Errorf("Active = %q, want openspec", active.ID)
	}
}

// TestDetectInstalled_DuplicateNamesPreferStrongestScope pins the catalog's
// duplicate handling: the same skill in several tiers resolves to the strongest
// one, and a project copy outranks a user copy.
func TestDetectInstalled_DuplicateNamesPreferStrongestScope(t *testing.T) {
	result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{
		userSkill("openspec-propose"),
		projectSkill("openspec-propose"),
		userSkill("openspec-apply-change"),
	}})

	active := requireRecognized(t, result, "openspec")
	if active.Scope != sdd.ScopeProject {
		t.Errorf("Scope = %q, want %q", active.Scope, sdd.ScopeProject)
	}
	for _, skill := range active.Skills {
		if skill.Name != "openspec-propose" {
			continue
		}
		if skill.Scope != sdd.ScopeProject || skill.Dir != projectSkillDir+"/openspec-propose" {
			t.Errorf("openspec-propose evidence = %+v, want the project copy", skill)
		}
	}
}

// TestDetectInstalled_MarkersOnlyFrameworkIsNeverVisible pins D8 for an
// operator-defined framework that declares no skill set: markers and roots can
// never make it visible.
func TestDetectInstalled_MarkersOnlyFrameworkIsNeverVisible(t *testing.T) {
	markersOnly := syntheticFramework("markers-only", 1, sdd.Skills{}, []string{"mo"}, []string{"mo/index.md"})

	result := sdd.DetectInstalled([]sdd.Definition{markersOnly}, sdd.Evidence{
		Markers: []string{"mo/index.md"},
		Roots:   []string{"mo"},
	})

	requireNotRecognized(t, result)
}

// TestDetectInstalled_OperatorFramework pins catalog-agnostic detection: an
// operator-contributed framework is recognised by its own skill set and keeps
// its provenance.
func TestDetectInstalled_OperatorFramework(t *testing.T) {
	framework := syntheticFramework("acme", 5, sdd.Skills{
		AllOf: []string{"acme-init"},
		AnyOf: []string{"acme-build", "acme-review"},
	}, []string{"acme"}, []string{"acme/config.yaml"})

	result := sdd.DetectInstalled([]sdd.Definition{framework}, sdd.Evidence{
		Skills:  []sdd.InstalledSkill{projectSkill("acme-init"), projectSkill("acme-review")},
		Markers: []string{"acme/config.yaml"},
	})

	active := requireRecognized(t, result, "acme")
	if active.Origin != sdd.OriginGlobal {
		t.Errorf("Origin = %q, want %q", active.Origin, sdd.OriginGlobal)
	}
	if active.Document != "acme.yaml" {
		t.Errorf("Document = %q, want acme.yaml", active.Document)
	}
	if !active.MarkerPresent || active.RootPresent || !active.TreePresent() {
		t.Errorf("evidence = root %v / marker %v / tree %v, want false/true/true",
			active.RootPresent, active.MarkerPresent, active.TreePresent())
	}
}

// TestDetectInstalled_AllOfBeatsAnyOf pins the match conjunct: every all_of name
// must be installed even when any_of is fully satisfied.
func TestDetectInstalled_AllOfBeatsAnyOf(t *testing.T) {
	framework := syntheticFramework("acme", 5, sdd.Skills{
		AllOf: []string{"acme-init", "acme-sync"},
		AnyOf: []string{"acme-build"},
	}, nil, nil)

	result := sdd.DetectInstalled([]sdd.Definition{framework}, sdd.Evidence{
		Skills: []sdd.InstalledSkill{projectSkill("acme-init"), projectSkill("acme-build")},
	})

	requireNotRecognized(t, result)
}

// TestDetectInstalled_TreeSignal pins the tree evidence: roots and markers are
// both tree signals, declaration forms are normalised, and malformed probe paths
// never match.
func TestDetectInstalled_TreeSignal(t *testing.T) {
	framework := syntheticFramework("acme", 5, sdd.Skills{AnyOf: []string{"acme-init"}},
		[]string{"acme"}, []string{"acme/config.yaml"})

	cases := []struct {
		name       string
		roots      []string
		markers    []string
		wantRoot   bool
		wantMarker bool
	}{
		{name: "root", roots: []string{"acme"}, wantRoot: true},
		{name: "root with dot prefix", roots: []string{"./acme"}, wantRoot: true},
		{name: "root with trailing slash", roots: []string{"acme/"}, wantRoot: true},
		{name: "marker", markers: []string{"acme/config.yaml"}, wantMarker: true},
		{name: "both", roots: []string{"acme"}, markers: []string{"acme/config.yaml"}, wantRoot: true, wantMarker: true},
		{name: "unrelated", roots: []string{"other"}, markers: []string{"other/index.md"}},
		{name: "parent escape", roots: []string{"../acme"}, markers: []string{"../acme/config.yaml"}},
		{name: "absolute", roots: []string{"/acme"}, markers: []string{"/acme/config.yaml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := sdd.DetectInstalled([]sdd.Definition{framework}, sdd.Evidence{
				Skills:  []sdd.InstalledSkill{projectSkill("acme-init")},
				Roots:   tc.roots,
				Markers: tc.markers,
			})
			active := requireRecognized(t, result, "acme")
			if active.RootPresent != tc.wantRoot {
				t.Errorf("RootPresent = %v, want %v", active.RootPresent, tc.wantRoot)
			}
			if active.MarkerPresent != tc.wantMarker {
				t.Errorf("MarkerPresent = %v, want %v", active.MarkerPresent, tc.wantMarker)
			}
			if want := tc.wantRoot || tc.wantMarker; active.TreePresent() != want {
				t.Errorf("TreePresent = %v, want %v", active.TreePresent(), want)
			}
		})
	}
}

// TestDetectInstalled_NamesMatchExactly pins the matching contract from the
// negative side: a declared name is satisfied only by that exact skill, so the
// retired pre-rename spec-* spellings are distinct skills that recognise
// nothing, and a look-alike name the skillset does not ship is not evidence
// either.
func TestDetectInstalled_NamesMatchExactly(t *testing.T) {
	for _, name := range []string{
		"spec-check",         // pre-rename spelling: no prefix folding
		"spec-init",          // pre-rename spelling
		"spec-update",        // pre-rename spelling
		"vibespec-checker",   // extra suffix
		"vibespec-check-era", // extra suffix after the same stem
		"my-spec-check",      // unrelated prefix
		"vibespec-archive",   // not a shipped vibespec skill
	} {
		t.Run(name, func(t *testing.T) {
			result := detect(t, sdd.Evidence{Skills: []sdd.InstalledSkill{userSkill(name)}})
			requireNotRecognized(t, result)
		})
	}
}

// TestDetectInstalled_EmptyInputs pins the neutral outcomes: no catalog and no
// evidence recognise nothing rather than panicking.
func TestDetectInstalled_EmptyInputs(t *testing.T) {
	requireNotRecognized(t, sdd.DetectInstalled(nil, sdd.Evidence{}))
	requireNotRecognized(t, sdd.DetectInstalled(builtinCatalog(t), sdd.Evidence{}))
}

// TestDetectInstalled_DotRootIsPresent pins the one declaration the loader, the
// reader and detection must agree on: a lone `path: "."` root (the whole
// workspace as one subtree). The loader accepts it as a clean relative path and
// the reader parses it as a present subtree, so detection must report the root
// present too — otherwise `root_present`/`tree_present` are false for a tree the
// panel is simultaneously rendering, and the first default-selection key (D9)
// silently prefers a framework whose tree does not exist.
func TestDetectInstalled_DotRootIsPresent(t *testing.T) {
	framework := sdd.Framework{
		Schema: sdd.SchemaV1,
		ID:     "adot",
		Label:  "adot",
		Detect: sdd.Detect{Skills: sdd.Skills{AllOf: []string{"demo-init"}}},
		Roots:  []sdd.Root{{ID: "all", Path: "."}},
		Concepts: []sdd.Concept{{
			Kind:  "doc",
			Match: sdd.Match{Root: "all", Glob: "*.md"},
		}},
	}

	// The reader agrees with the loader: the declared root is already there.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "overview.md"), []byte("# Overview\n"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	tree, err := sdd.ParseTree(dir, framework)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	if !tree.Present || len(tree.Roots) != 1 || !tree.Roots[0].Present {
		t.Fatalf("parse: Present = %v, roots = %+v, want the `.` root present", tree.Present, tree.Roots)
	}

	// presentDeclaredPaths hands detection the DECLARED spelling, so "." is what
	// the evidence carries for this root.
	result := sdd.DetectInstalled(
		[]sdd.Definition{{Framework: framework, Origin: sdd.OriginGlobal, Document: "adot.yaml"}},
		sdd.Evidence{
			Skills: []sdd.InstalledSkill{projectSkill("demo-init")},
			Roots:  []string{"."},
		},
	)
	active := requireRecognized(t, result, "adot")
	if !active.RootPresent {
		t.Error("RootPresent = false for a `.` root the reader parses as present")
	}
	if !active.TreePresent() {
		t.Error("TreePresent() = false for a `.` root the reader parses as present")
	}
}
