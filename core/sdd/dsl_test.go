package sdd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
)

// projectMetaDir is the checkout-local c0wrk metadata directory the caller
// passes in as a path component; core/sdd itself knows no layout.
const projectMetaDir = ".c0wrk"

// writeDefsFile writes an operator definitions file, creating parent
// directories the way an operator would.
func writeDefsFile(t *testing.T, path, data string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

// frameworkDoc derives a valid, complete framework document from minimalDoc
// with the given identity — the shape an operator override takes.
func frameworkDoc(id, label string, priority int) string {
	doc := mutate(minimalDoc, "id: demo", "id: "+id)
	doc = mutate(doc, "label: Demo framework", "label: "+label)
	return mutate(doc, "detect:\n", "priority: "+strconv.Itoa(priority)+"\ndetect:\n")
}

func definitionIDs(definitions []sdd.Definition) []string {
	ids := make([]string, 0, len(definitions))
	for _, def := range definitions {
		ids = append(ids, def.ID)
	}
	return ids
}

func loadedIDs(result sdd.LoadResult) []string {
	return definitionIDs(result.Frameworks)
}

func requireNoWarnings(t *testing.T, result sdd.LoadResult) {
	t.Helper()
	if len(result.Warnings) != 0 {
		t.Fatalf("Load() warnings = %q, want none", result.Warnings)
	}
}

func requireBuiltinCatalog(t *testing.T, result sdd.LoadResult) {
	t.Helper()
	if want := []string{"vibespec", "openspec", "spec-kit"}; !slices.Equal(loadedIDs(result), want) {
		t.Fatalf("Load() frameworks = %v, want the built-in catalog %v", loadedIDs(result), want)
	}
	for _, def := range result.Frameworks {
		if def.Origin != sdd.OriginBuiltin {
			t.Errorf("%s: origin = %q, want %q", def.ID, def.Origin, sdd.OriginBuiltin)
		}
		if want := "frameworks/" + def.ID + ".yaml"; def.Document != want {
			t.Errorf("%s: document = %q, want %q", def.ID, def.Document, want)
		}
	}
}

// TestLoad_WithoutOperatorFiles pins AC-3 and AC-6: absent operator files are
// the neutral state — the built-in catalog, no warnings, and no file (or its
// parent directory) materialized on disk.
func TestLoad_WithoutOperatorFiles(t *testing.T) {
	root := t.TempDir()
	globalPath := filepath.Join(root, "agent", "specs.yaml")
	projectPath := filepath.Join(root, "workspace", projectMetaDir, "specs.yaml")

	result := sdd.Load(globalPath, projectPath)

	requireNoWarnings(t, result)
	requireBuiltinCatalog(t, result)

	for _, path := range []string{
		globalPath, filepath.Dir(globalPath),
		projectPath, filepath.Dir(projectPath), filepath.Dir(filepath.Dir(projectPath)),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("Load() created %q, want nothing materialized (stat error = %v)", path, err)
		}
	}
}

// TestLoad_EmptyPathsSkipsScopes covers the CHAT-mode caller that has no
// workspace: an empty path is skipped silently rather than probed.
func TestLoad_EmptyPathsSkipsScopes(t *testing.T) {
	result := sdd.Load("", "")

	requireNoWarnings(t, result)
	requireBuiltinCatalog(t, result)
}

// TestLoad_OperatorFileOverridesBuiltin pins AC-1: a user definition with a
// built-in id replaces that framework wholesale — label and structure alike.
func TestLoad_OperatorFileOverridesBuiltin(t *testing.T) {
	globalPath := writeDefsFile(t, filepath.Join(t.TempDir(), "specs.yaml"),
		frameworkDoc("vibespec", "My Specification System", 10))

	result := sdd.Load(globalPath, "")

	requireNoWarnings(t, result)
	// The id set and order are unchanged: an override replaces a built-in, it
	// never adds a second entry.
	if want := []string{"vibespec", "openspec", "spec-kit"}; !slices.Equal(loadedIDs(result), want) {
		t.Fatalf("Load() frameworks = %v, want %v", loadedIDs(result), want)
	}

	def, ok := result.ByID("vibespec")
	if !ok {
		t.Fatalf("Load() lost the vibespec framework")
	}
	if def.Label != "My Specification System" {
		t.Errorf("vibespec label = %q, want the operator's %q", def.Label, "My Specification System")
	}
	if def.Origin != sdd.OriginGlobal || def.Document != globalPath {
		t.Errorf("vibespec origin/document = %q/%q, want %q/%q", def.Origin, def.Document, sdd.OriginGlobal, globalPath)
	}
	// minimalDoc carries a single root and two concepts; the built-in vibespec
	// document is far richer, so the shape proves a wholesale replacement
	// rather than a field-level merge.
	if len(def.Roots) != 1 || def.Roots[0].ID != "demo" || len(def.Concepts) != 2 {
		t.Errorf("vibespec roots/concepts = %+v / %d, want the override's single demo root and 2 concepts", def.Roots, len(def.Concepts))
	}
	// The other built-ins are untouched.
	openspec, _ := result.ByID("openspec")
	if openspec.Origin != sdd.OriginBuiltin || openspec.Label == "" {
		t.Errorf("openspec = %+v, want the untouched built-in", openspec)
	}
}

// TestLoad_NewIDIsAdded pins AC-2: an unknown id joins the catalog.
func TestLoad_NewIDIsAdded(t *testing.T) {
	globalPath := writeDefsFile(t, filepath.Join(t.TempDir(), "specs.yaml"),
		frameworkDoc("acme", "Acme Specs", 40))

	result := sdd.Load(globalPath, "")

	requireNoWarnings(t, result)
	if want := []string{"vibespec", "openspec", "spec-kit", "acme"}; !slices.Equal(loadedIDs(result), want) {
		t.Fatalf("Load() frameworks = %v, want %v", loadedIDs(result), want)
	}
	def, ok := result.ByID("acme")
	if !ok {
		t.Fatalf("Load() dropped the operator framework acme")
	}
	if def.Origin != sdd.OriginGlobal || def.Document != globalPath {
		t.Errorf("acme origin/document = %q/%q, want %q/%q", def.Origin, def.Document, sdd.OriginGlobal, globalPath)
	}
}

// TestLoad_PriorityOrdersMergedCatalog documents that the merged catalog is
// ordered by Priority then ID, so an operator can place a framework ahead of
// the built-ins for detection.
func TestLoad_PriorityOrdersMergedCatalog(t *testing.T) {
	globalPath := writeDefsFile(t, filepath.Join(t.TempDir(), "specs.yaml"),
		frameworkDoc("acme", "Acme Specs", 5))

	result := sdd.Load(globalPath, "")

	requireNoWarnings(t, result)
	if want := []string{"acme", "vibespec", "openspec", "spec-kit"}; !slices.Equal(loadedIDs(result), want) {
		t.Fatalf("Load() frameworks = %v, want %v (Priority then ID)", loadedIDs(result), want)
	}
}

// TestLoad_BrokenYAMLWarnsAndKeepsCatalog pins AC-4: a file that is not YAML is
// reported once and otherwise ignored, so the built-ins and other sources still
// load.
func TestLoad_BrokenYAMLWarnsAndKeepsCatalog(t *testing.T) {
	root := t.TempDir()
	globalPath := writeDefsFile(t, filepath.Join(root, "agent", "specs.yaml"), "checks: [\n\t- kind:\n")
	projectPath := writeDefsFile(t, filepath.Join(root, "ws", projectMetaDir, "specs.yaml"),
		frameworkDoc("acme", "Acme Specs", 40))

	result := sdd.Load(globalPath, projectPath)

	if len(result.Warnings) != 1 {
		t.Fatalf("Load() warnings = %q, want exactly one", result.Warnings)
	}
	warning := result.Warnings[0]
	if !strings.Contains(warning, globalPath) || !strings.Contains(warning, "cannot parse document 1") {
		t.Errorf("warning %q must name the broken file and the failing document", warning)
	}
	if want := []string{"vibespec", "openspec", "spec-kit", "acme"}; !slices.Equal(loadedIDs(result), want) {
		t.Errorf("Load() frameworks = %v, want the built-ins plus the project framework %v", loadedIDs(result), want)
	}
}

// TestLoad_InvalidDocumentSkipsOnlyItself pins the partial-application half of
// AC-4: within one file the valid documents apply and only the invalid one is
// skipped, with a warning that names its position.
func TestLoad_InvalidDocumentSkipsOnlyItself(t *testing.T) {
	alpha := frameworkDoc("alpha", "Alpha Specs", 40)
	invalid := mutate(frameworkDoc("beta", "Beta Specs", 50), "roots:\n", "unknown_key: 1\nroots:\n")
	gamma := frameworkDoc("gamma", "Gamma Specs", 60)
	globalPath := writeDefsFile(t, filepath.Join(t.TempDir(), "specs.yaml"),
		strings.Join([]string{alpha, invalid, gamma}, "---\n"))

	result := sdd.Load(globalPath, "")

	if len(result.Warnings) != 1 {
		t.Fatalf("Load() warnings = %q, want exactly one", result.Warnings)
	}
	warning := result.Warnings[0]
	if !strings.Contains(warning, "unknown_key") || !strings.Contains(warning, "(document 2)") {
		t.Errorf("warning %q must name the unknown key and the document position", warning)
	}
	if want := []string{"vibespec", "openspec", "spec-kit", "alpha", "gamma"}; !slices.Equal(loadedIDs(result), want) {
		t.Errorf("Load() frameworks = %v, want the built-ins plus alpha and gamma %v", loadedIDs(result), want)
	}
	if _, ok := result.ByID("beta"); ok {
		t.Errorf("Load() kept the invalid beta definition")
	}
}

// TestLoad_ProjectOverridesGlobal pins AC-5: the project file is read last, so
// a colliding id belongs to it.
func TestLoad_ProjectOverridesGlobal(t *testing.T) {
	root := t.TempDir()
	globalPath := writeDefsFile(t, filepath.Join(root, "agent", "specs.yaml"),
		frameworkDoc("acme", "Global Acme", 40))
	projectPath := writeDefsFile(t, filepath.Join(root, "ws", projectMetaDir, "specs.yaml"),
		frameworkDoc("acme", "Project Acme", 41))

	result := sdd.Load(globalPath, projectPath)

	requireNoWarnings(t, result)
	if want := []string{"vibespec", "openspec", "spec-kit", "acme"}; !slices.Equal(loadedIDs(result), want) {
		t.Fatalf("Load() frameworks = %v, want %v (one acme, not two)", loadedIDs(result), want)
	}
	def, ok := result.ByID("acme")
	if !ok {
		t.Fatalf("Load() dropped acme")
	}
	if def.Label != "Project Acme" || def.Origin != sdd.OriginProject || def.Document != projectPath {
		t.Errorf("acme = %q/%q/%q, want the project definition %q/%q/%q",
			def.Label, def.Origin, def.Document, "Project Acme", sdd.OriginProject, projectPath)
	}
}

// TestLoad_BuiltinCannotBeRemoved documents the non-removal rule: an operator
// file only adds and overrides, so a file listing one framework still yields
// the whole built-in catalog beside it.
func TestLoad_BuiltinCannotBeRemoved(t *testing.T) {
	globalPath := writeDefsFile(t, filepath.Join(t.TempDir(), "specs.yaml"),
		frameworkDoc("acme", "Acme Specs", 40))

	result := sdd.Load(globalPath, "")

	requireNoWarnings(t, result)
	for _, id := range []string{"vibespec", "openspec", "spec-kit"} {
		if _, ok := result.ByID(id); !ok {
			t.Errorf("Load() dropped the built-in %q; operator files must not delete built-ins", id)
		}
	}
}

// TestLoadDefinitions_MissingFileIsNeutral pins the per-file contract: a
// missing file is no definitions and no warning, and it stays absent.
func TestLoadDefinitions_MissingFileIsNeutral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "specs.yaml")

	definitions, warnings := sdd.LoadDefinitions(sdd.OriginGlobal, path)

	if len(definitions) != 0 || len(warnings) != 0 {
		t.Fatalf("LoadDefinitions() = %v / %q, want no definitions and no warnings", definitions, warnings)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("LoadDefinitions() materialized %q", path)
	}
}

// TestLoadDefinitions_UnreadablePathWarns covers the fail-soft read failure: a
// path that exists but is not a readable file is one warning, never an error.
func TestLoadDefinitions_UnreadablePathWarns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "specs.yaml")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}

	definitions, warnings := sdd.LoadDefinitions(sdd.OriginGlobal, path)

	if len(definitions) != 0 {
		t.Fatalf("LoadDefinitions() = %v, want no definitions from a directory", definitions)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], path) {
		t.Fatalf("LoadDefinitions() warnings = %q, want one naming %q", warnings, path)
	}
}

// TestParseDefinitions_BlankDocumentsIgnored keeps empty files and trailing
// `---` separators out of the warning channel.
func TestParseDefinitions_BlankDocumentsIgnored(t *testing.T) {
	data := "\n---\n\n---\n" + frameworkDoc("alpha", "Alpha Specs", 40) + "---\n"

	frameworks, warnings := sdd.ParseDefinitions("specs.yaml", []byte(data))

	if len(warnings) != 0 {
		t.Fatalf("ParseDefinitions() warnings = %q, want none", warnings)
	}
	if len(frameworks) != 1 || frameworks[0].ID != "alpha" {
		t.Fatalf("ParseDefinitions() = %+v, want the single alpha definition", frameworks)
	}
}

// TestParseDefinitions_ContentWithoutHeaderIsReported pins the blankness rule:
// a document carrying keys but neither `schema` nor `id` is NOT blank — it is
// the likeliest operator mistake (a fragment copied out of the guide, missing
// its two header lines) — so it reaches the validator and is named, while a
// genuinely empty document stays silent.
func TestParseDefinitions_ContentWithoutHeaderIsReported(t *testing.T) {
	headerless := "---\nroots:\n  - id: docs\n    path: docs\n"

	t.Run("headerless content is named with its faults", func(t *testing.T) {
		data := frameworkDoc("alpha", "Alpha Specs", 40) + headerless

		frameworks, warnings := sdd.ParseDefinitions("specs.yaml", []byte(data))

		if len(frameworks) != 1 || frameworks[0].ID != "alpha" {
			t.Fatalf("ParseDefinitions() = %+v, want the valid alpha kept", frameworks)
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %q, want exactly one naming the headerless document", warnings)
		}
		if !strings.Contains(warnings[0], "document 2") {
			t.Errorf("warning = %q, want it to name document 2", warnings[0])
		}
		for _, want := range []string{"schema", "id"} {
			if !strings.Contains(warnings[0], want) {
				t.Errorf("warning = %q, want the %q fault named (not a generic skip)", warnings[0], want)
			}
		}
	})

	t.Run("truly blank documents stay silent", func(t *testing.T) {
		data := "\n---\n\n---\nnull\n---\n" + frameworkDoc("alpha", "Alpha Specs", 40) + "---\n"

		frameworks, warnings := sdd.ParseDefinitions("specs.yaml", []byte(data))

		if len(warnings) != 0 {
			t.Fatalf("warnings = %q, want none for genuinely empty documents", warnings)
		}
		if len(frameworks) != 1 || frameworks[0].ID != "alpha" {
			t.Fatalf("ParseDefinitions() = %+v, want the single alpha definition", frameworks)
		}
	})
}

// TestParseDefinitions_DuplicateIDWarnsLastWins treats a repeated id inside one
// file as an authoring mistake: the later definition wins and says so.
func TestParseDefinitions_DuplicateIDWarnsLastWins(t *testing.T) {
	data := frameworkDoc("alpha", "First Alpha", 40) + "---\n" + frameworkDoc("alpha", "Second Alpha", 40)

	frameworks, warnings := sdd.ParseDefinitions("specs.yaml", []byte(data))

	if len(warnings) != 1 || !strings.Contains(warnings[0], `duplicate framework id "alpha"`) {
		t.Fatalf("ParseDefinitions() warnings = %q, want one duplicate-id warning", warnings)
	}
	if len(frameworks) != 1 || frameworks[0].Label != "Second Alpha" {
		t.Fatalf("ParseDefinitions() = %+v, want the later alpha definition", frameworks)
	}
}

// TestParseDefinitions_TruncatedStreamKeepsEarlierDocuments documents the
// stream-level failure: the documents decoded before the fault survive.
func TestParseDefinitions_TruncatedStreamKeepsEarlierDocuments(t *testing.T) {
	data := frameworkDoc("alpha", "Alpha Specs", 40) + "---\nchecks: [\n\t- kind:\n"

	frameworks, warnings := sdd.ParseDefinitions("specs.yaml", []byte(data))

	if len(frameworks) != 1 || frameworks[0].ID != "alpha" {
		t.Fatalf("ParseDefinitions() = %+v, want the alpha definition decoded before the fault", frameworks)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cannot parse document 2") {
		t.Fatalf("ParseDefinitions() warnings = %q, want one naming document 2", warnings)
	}
}

// TestMerge_ReplacesByIDUnit covers Merge directly: the overlay replaces in
// full, unseen ids are appended, and input order does not leak into the result
// order (Priority then ID governs).
func TestMerge_ReplacesByIDUnit(t *testing.T) {
	builtins, err := sdd.BuiltinDefinitions()
	if err != nil {
		t.Fatalf("BuiltinDefinitions() error = %v", err)
	}
	replacement, err := sdd.Parse("override.yaml", []byte(frameworkDoc("openspec", "Overridden", 20)))
	if err != nil {
		t.Fatalf("Parse(override) error = %v", err)
	}
	overlay := []sdd.Definition{
		{Framework: replacement, Origin: sdd.OriginGlobal, Document: "override.yaml"},
	}

	merged := sdd.Merge(builtins, overlay)

	if want := []string{"vibespec", "openspec", "spec-kit"}; !slices.Equal(definitionIDs(merged), want) {
		t.Fatalf("Merge() ids = %v, want %v", definitionIDs(merged), want)
	}
	// Precedence follows argument order: a set merged later wins, which is how
	// Load layers project over global over built-in.
	reversedMerge := sdd.LoadResult{Frameworks: sdd.Merge(overlay, builtins)}
	def, ok := reversedMerge.ByID("openspec")
	if !ok || def.Label != "OpenSpec" || def.Origin != sdd.OriginBuiltin {
		t.Errorf("Merge(overlay, builtins) openspec = %+v, want the later built-in definition to win", def)
	}
}

// TestLoadResult_ByID pins the lookup helper used by the surfaces.
func TestLoadResult_ByID(t *testing.T) {
	result := sdd.Load("", "")

	if _, ok := result.ByID("vibespec"); !ok {
		t.Errorf("ByID(vibespec) not found")
	}
	if def, ok := result.ByID("nope"); ok || def.ID != "" {
		t.Errorf("ByID(nope) = %+v, %v; want the zero definition and false", def, ok)
	}
}

// TestOriginValues pins the wire values a surface renders and an RPC payload
// carries.
func TestOriginValues(t *testing.T) {
	for origin, want := range map[sdd.Origin]string{
		sdd.OriginBuiltin: "builtin",
		sdd.OriginGlobal:  "global",
		sdd.OriginProject: "project",
	} {
		if string(origin) != want {
			t.Errorf("origin %q = %q, want %q", want, origin, want)
		}
	}
}
