package sdd_test

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
)

func TestBuiltins_ParseEveryDocument(t *testing.T) {
	frameworks, err := sdd.Builtins()
	if err != nil {
		t.Fatalf("Builtins() error = %v", err)
	}
	if len(frameworks) != 3 {
		t.Fatalf("Builtins() returned %d frameworks, want 3", len(frameworks))
	}
	wantOrder := []string{"sdd", "openspec", "spec-kit"}
	gotOrder := make([]string, 0, len(frameworks))
	for _, fw := range frameworks {
		gotOrder = append(gotOrder, fw.ID)
		if fw.Schema != sdd.SchemaV1 {
			t.Errorf("%s: schema = %q, want %q", fw.ID, fw.Schema, sdd.SchemaV1)
		}
		if fw.Label == "" || fw.Description == "" || fw.DocsURL == "" {
			t.Errorf("%s: label/description/docs_url must all be set, got %q / %q / %q", fw.ID, fw.Label, fw.Description, fw.DocsURL)
		}
		if len(fw.Roots) == 0 || len(fw.Concepts) == 0 {
			t.Errorf("%s: roots and concepts must not be empty", fw.ID)
		}
	}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Errorf("Builtins() order = %v, want %v (Priority then ID)", gotOrder, wantOrder)
	}

	names, err := sdd.BuiltinNames()
	if err != nil {
		t.Fatalf("BuiltinNames() error = %v", err)
	}
	if want := []string{"openspec.yaml", "sdd.yaml", "spec-kit.yaml"}; !slices.Equal(names, want) {
		t.Errorf("BuiltinNames() = %v, want %v", names, want)
	}
}

func TestBuiltin_ByID(t *testing.T) {
	for _, id := range []string{"sdd", "openspec", "spec-kit"} {
		fw, err := sdd.Builtin(id)
		if err != nil {
			t.Fatalf("Builtin(%q) error = %v", id, err)
		}
		if fw.ID != id {
			t.Errorf("Builtin(%q).ID = %q", id, fw.ID)
		}
	}
	if _, err := sdd.Builtin("nope"); err == nil || !strings.Contains(err.Error(), `unknown built-in framework "nope"`) {
		t.Errorf("Builtin(nope) error = %v, want an unknown-framework error naming the known ids", err)
	}
}

// TestBuiltins_SDDExpressesCurrentLayout pins AC-3: the sdd document describes
// this repository's tree (META/INDEX + the four sections) and declares exactly
// the nine built-in integrity checks through the documented primitives.
func TestBuiltins_SDDExpressesCurrentLayout(t *testing.T) {
	fw, err := sdd.Builtin("sdd")
	if err != nil {
		t.Fatalf("Builtin(sdd) error = %v", err)
	}

	if len(fw.Roots) != 1 || fw.Roots[0].Path != "specs" || !fw.Roots[0].Required {
		t.Fatalf("sdd roots = %+v, want one required root at specs", fw.Roots)
	}

	wantConcepts := map[string]string{
		"meta":         "META.md",
		"index":        "INDEX.md",
		"architecture": "architecture/**/*.md",
		"domain":       "domains/**/*.md",
		"contract":     "contracts/**/*.md",
		"decision":     "decisions/**/*.md",
	}
	got := make(map[string]string, len(fw.Concepts))
	for _, c := range fw.Concepts {
		got[c.Kind] = c.Match.Glob
		if len(c.Match.Exclude) == 0 && c.Kind != "meta" && c.Kind != "index" && c.Kind != "template" {
			t.Errorf("sdd concept %q declares no template exclude — templates would enter the views and checks", c.Kind)
		}
	}
	for kind, glob := range wantConcepts {
		if got[kind] != glob {
			t.Errorf("sdd concept %q glob = %q, want %q", kind, got[kind], glob)
		}
	}
	wantFindings := make([]string, 0, 9)
	wantFindings = append(wantFindings,
		"ROOT_FILE_MISSING", "PATH_MISSING", "ADR_NAME_MALFORMED", "CROSS_REF_BROKEN",
		"INDEX_STALE", "ADR_NUMBER_MISMATCH", "DUPLICATE_ADR_NUMBER",
		"SUPERSEDED_TARGET_MISSING",
	)
	if len(fw.Checks) != len(wantFindings) {
		t.Fatalf("sdd declares %d checks, want %d", len(fw.Checks), len(wantFindings))
	}
	gotFindings := make([]string, 0, len(fw.Checks))
	for _, c := range fw.Checks {
		if !slices.Contains(sdd.CheckKinds, c.Kind) {
			t.Errorf("sdd check %q uses undeclared kind %q", c.Finding, c.Kind)
		}
		if c.Message == "" {
			t.Errorf("sdd check %q carries no operator message", c.Finding)
		}
		gotFindings = append(gotFindings, c.Finding)
		if c.CycleFinding != "" {
			gotFindings = append(gotFindings, c.CycleFinding)
		}
	}
	// Resolution and loop detection are one declared references check with
	// two finding tokens (finding + cycle_finding), not two sibling checks.
	superseded := slices.IndexFunc(fw.Checks, func(c sdd.Check) bool { return c.Kind == sdd.CheckReferences })
	if superseded < 0 {
		t.Fatal("sdd declares no references check")
	}
	if fw.Checks[superseded].Finding != "SUPERSEDED_TARGET_MISSING" ||
		fw.Checks[superseded].CycleFinding != "SUPERSEDED_CYCLE" || !fw.Checks[superseded].Acyclic {
		t.Errorf("sdd references check = %+v, want finding SUPERSEDED_TARGET_MISSING + cycle_finding SUPERSEDED_CYCLE + acyclic", fw.Checks[superseded])
	}
	wantFindings = append(wantFindings, "SUPERSEDED_CYCLE")
	sort.Strings(gotFindings)
	sortedWant := slices.Clone(wantFindings)
	sort.Strings(sortedWant)
	if !slices.Equal(gotFindings, sortedWant) {
		t.Errorf("sdd findings = %v, want %v", gotFindings, sortedWant)
	}

	wantSkills := []string{"spec-init", "spec-check", "spec-consult", "spec-explore", "spec-create", "spec-update"}
	gotSkills := make([]string, 0, len(fw.Actions))
	for _, a := range fw.Actions {
		gotSkills = append(gotSkills, a.Skill)
	}
	for _, skill := range wantSkills {
		if !slices.Contains(gotSkills, skill) {
			t.Errorf("sdd actions do not dispatch %q (got %v)", skill, gotSkills)
		}
	}
}

// TestBuiltins_ConceptCoverage pins AC-4: each third-party framework models the
// whole concept set its documentation describes.
func TestBuiltins_ConceptCoverage(t *testing.T) {
	cases := map[string][]string{
		"openspec": {
			"config", "capability", "spec", "change", "proposal", "design",
			"tasks", "delta", "archive", "archived-change",
		},
		"spec-kit": {
			"constitution", "template", "feature-state", "feature", "spec",
			"plan", "research", "data-model", "quickstart", "tasks", "contract",
			"checklist",
		},
	}
	for id, wantKinds := range cases {
		fw, err := sdd.Builtin(id)
		if err != nil {
			t.Fatalf("Builtin(%q) error = %v", id, err)
		}
		kinds := make([]string, 0, len(fw.Concepts))
		for _, c := range fw.Concepts {
			kinds = append(kinds, c.Kind)
		}
		for _, want := range wantKinds {
			if !slices.Contains(kinds, want) {
				t.Errorf("%s: missing concept %q (got %v)", id, want, kinds)
			}
		}
		if len(fw.Actions) == 0 {
			t.Errorf("%s: declares no actions", id)
		}
		if len(fw.Views.Sections) == 0 {
			t.Errorf("%s: declares no view sections", id)
		}
		for _, kind := range wantKinds {
			covered := false
			for _, section := range fw.Views.Sections {
				if slices.Contains(section.Concepts, kind) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("%s: concept %q is not reachable from any view section", id, kind)
			}
		}
	}
}

// TestBuiltins_ConceptKindsAreUnique guards the precedence rule the DSL relies
// on: a kind names one glob, so overlapping globs resolve by specificity alone.
func TestBuiltins_ConceptKindsAreUnique(t *testing.T) {
	frameworks, err := sdd.Builtins()
	if err != nil {
		t.Fatalf("Builtins() error = %v", err)
	}
	for _, fw := range frameworks {
		seen := map[string]bool{}
		for _, c := range fw.Concepts {
			if seen[c.Kind] {
				t.Errorf("%s: duplicate concept kind %q", fw.ID, c.Kind)
			}
			seen[c.Kind] = true
		}
	}
}

// TestBuiltins_NodeActionsTargetNodes pins the node-action wiring: every
// concept-scoped action names the selected node through a {path} placeholder
// (the frontend substitutes it — a prompt without one loses the chosen
// change/feature), and Spec Kit's Plan/Tasks attach to the FEATURE directory
// (creation steps gated by the artifact's absence, not to the output file
// that would not exist yet).
func TestBuiltins_NodeActionsTargetNodes(t *testing.T) {
	for _, id := range []string{"sdd", "openspec", "spec-kit"} {
		fw, err := sdd.Builtin(id)
		if err != nil {
			t.Fatalf("Builtin(%q) error = %v", id, err)
		}
		for _, action := range fw.Actions {
			if action.Concept == "" {
				continue // panel-level actions have no node to name
			}
			if !strings.Contains(action.Prompt, "{path}") {
				t.Errorf("%s action %q targets concept %q but its prompt names no {path}: %q",
					id, action.Kind, action.Concept, action.Prompt)
			}
		}
	}

	specKit, err := sdd.Builtin("spec-kit")
	if err != nil {
		t.Fatalf("Builtin(spec-kit) error = %v", err)
	}
	actions := make(map[string]sdd.Action, len(specKit.Actions))
	for _, action := range specKit.Actions {
		actions[action.Kind] = action
	}
	plan, ok := actions["plan"]
	if !ok {
		t.Fatal("spec-kit declares no plan action")
	}
	if plan.Concept != "feature" || !slices.Contains(plan.Requires, "absent:plan") {
		t.Errorf("spec-kit plan action = %+v, want it attached to the feature until plan.md exists", plan)
	}
	tasks, ok := actions["tasks"]
	if !ok {
		t.Fatal("spec-kit declares no tasks action")
	}
	if tasks.Concept != "feature" ||
		!slices.Contains(tasks.Requires, "companion:plan") || !slices.Contains(tasks.Requires, "absent:tasks") {
		t.Errorf("spec-kit tasks action = %+v, want it attached to the feature once plan.md exists and until tasks.md does", tasks)
	}
}
