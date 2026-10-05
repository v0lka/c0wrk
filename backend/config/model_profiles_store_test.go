package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// storeTestConfigA/B are two distinct valid knob sets used for round-trip
// and collision tests. A carries sentinel values of every value shape
// (floats, ints, strings, a string list) so byte-in-value fidelity is
// actually exercised.
func storeTestConfigA() ModelProfileConfig {
	return ModelProfileConfig{
		EssentialTools: EssentialToolsConfig{
			Enabled:             true,
			AlwaysPresent:       []string{"read_file", "edit_file"},
			CompactDescriptions: true,
		},
		SystemPrompt: SystemPromptConfig{Lite: true},
		Sampling: ModelProfilesSamplingConfig{
			Enabled:           true,
			Temperature:       0.7,
			TopP:              0.95,
			TopK:              64,
			RepetitionPenalty: 1.1,
			PresencePenalty:   1.5,
			ReasoningEffort:   "medium",
		},
		LoopHardening: LoopHardeningConfig{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      3,
			FruitlessAbortThreshold:      5,
			SameToolRepeatNudgeThreshold: 4,
		},
		Context: ModelProfilesContextConfig{
			Enabled:             true,
			Compaction:          ModelProfilesCompactionConfig{KeepLast: 6, BlockSize: 5, TriggerPercent: 80},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  16384,
		},
	}
}

func storeTestConfigB() ModelProfileConfig {
	return ModelProfileConfig{
		Sampling: ModelProfilesSamplingConfig{Enabled: true, ReasoningEffort: "low"},
	}
}

// writeRawModelProfilesFile marshals raw (unvalidated) profiles into the
// versioned on-disk document, letting tests plant entries that would never
// pass the constructor.
func writeRawModelProfilesFile(t *testing.T, path string, version int, profiles []ModelProfile) {
	t.Helper()
	data, err := yaml.Marshal(&modelProfilesFile{Version: version, Profiles: profiles})
	if err != nil {
		t.Fatalf("marshal raw file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write raw file: %v", err)
	}
}

func TestModelProfilesPath(t *testing.T) {
	if got, want := ModelProfilesPath(filepath.Join("home", ".c0wrk")), filepath.Join("home", ".c0wrk", "model-profiles.yaml"); got != want {
		t.Fatalf("ModelProfilesPath() = %q, want %q", got, want)
	}
}

func TestCustomModelProfilesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")

	// No file before the first save: lazy creation.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("profile file must not exist before the first save, stat err = %v", err)
	}

	p1, err := CreateCustomModelProfile("My Tuned Model", storeTestConfigA(), nil)
	if err != nil {
		t.Fatalf("create p1: %v", err)
	}
	p2, err := CreateCustomModelProfile("Aggressive Compaction", storeTestConfigB(), []ModelProfile{p1})
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}
	if p1.Kind != ModelProfileKindCustom || p2.Kind != ModelProfileKindCustom {
		t.Fatalf("created profiles must be kind=custom, got %q/%q", p1.Kind, p2.Kind)
	}

	if err := SaveCustomModelProfiles(path, []ModelProfile{p1, p2}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, warnings := LoadCustomModelProfiles(path)
	if len(warnings) != 0 {
		t.Fatalf("load after clean save produced warnings: %v", warnings)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d profiles, want 2", len(loaded))
	}
	if !reflect.DeepEqual(loaded[0], p1) || !reflect.DeepEqual(loaded[1], p2) {
		t.Fatalf("round-trip changed profiles:\n got[0]=%+v\nwant[0]=%+v\n got[1]=%+v\nwant[1]=%+v", loaded[0], p1, loaded[1], p2)
	}

	// The file carries the format version.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var doc modelProfilesFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if doc.Version != modelProfilesFormatVersion {
		t.Fatalf("saved version = %d, want %d", doc.Version, modelProfilesFormatVersion)
	}

	// Save the loaded set back and reload: ids stay stable and values stay
	// identical between saves.
	if err := SaveCustomModelProfiles(path, loaded); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	reloaded, warnings := LoadCustomModelProfiles(path)
	if len(warnings) != 0 {
		t.Fatalf("load after re-save produced warnings: %v", warnings)
	}
	if !reflect.DeepEqual(reloaded, loaded) {
		t.Fatalf("ids/values not stable between saves:\n got=%+v\nwant=%+v", reloaded, loaded)
	}
	for i := range reloaded {
		if reloaded[i].ID != loaded[i].ID {
			t.Fatalf("profile %d id drifted: %q -> %q", i, loaded[i].ID, reloaded[i].ID)
		}
	}
}

func TestLoadCustomModelProfilesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 0 {
		t.Fatalf("missing file must load an empty list, got %d profiles", len(profiles))
	}
	if len(warnings) != 0 {
		t.Fatalf("missing file must not warn, got %v", warnings)
	}
}

func TestLoadCustomModelProfilesFullyBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nprofiles:\n  - id: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write broken file: %v", err)
	}
	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 0 {
		t.Fatalf("unparseable file must load an empty list, got %d profiles", len(profiles))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not valid YAML") {
		t.Fatalf("unparseable file must warn exactly once about YAML, got %v", warnings)
	}
}

func TestLoadCustomModelProfilesUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	writeRawModelProfilesFile(t, path, 2, []ModelProfile{{ID: "future", Name: "Future", Kind: ModelProfileKindCustom}})
	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 0 {
		t.Fatalf("future version must load an empty list, got %d profiles", len(profiles))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unsupported format version") {
		t.Fatalf("future version must warn about the version, got %v", warnings)
	}
}

func TestLoadCustomModelProfilesPartiallyBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	good, err := NewModelProfile("good-one", "Good One", ModelProfileKindCustom, storeTestConfigA())
	if err != nil {
		t.Fatalf("build good profile: %v", err)
	}
	badValue := good
	badValue.ID = "bad-value"
	badValue.Name = "Bad Value"
	badValue.Config.Sampling.TopP = 5 // out of (0, 1]
	badName := good
	badName.ID = "bad-name"
	badName.Name = "" // empty name
	writeRawModelProfilesFile(t, path, modelProfilesFormatVersion, []ModelProfile{good, badValue, badName})

	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 1 || profiles[0].ID != "good-one" {
		t.Fatalf("partially broken file must keep only the valid entry, got %+v", profiles)
	}
	if len(warnings) != 2 {
		t.Fatalf("want one warning per discarded entry, got %v", warnings)
	}
}

func TestLoadCustomModelProfilesCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	good, err := NewModelProfile("first", "First Custom", ModelProfileKindCustom, storeTestConfigB())
	if err != nil {
		t.Fatalf("build first: %v", err)
	}
	predefinedName := good
	predefinedName.ID = "not-generic"
	predefinedName.Name = "Generic (model-agnostic)" // collides with predefined name
	predefinedID := good
	predefinedID.ID = ModelProfilesGenericProfileID // collides with predefined id
	predefinedID.Name = "Another Name"
	dupName := good
	dupName.ID = "second"
	dupName.Name = "First Custom" // collides with the earlier custom
	dupID := good
	dupID.ID = "first" // collides with the earlier custom id
	dupID.Name = "Second Custom"
	writeRawModelProfilesFile(t, path, modelProfilesFormatVersion,
		[]ModelProfile{good, predefinedName, predefinedID, dupName, dupID})

	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 1 || profiles[0].ID != "first" {
		t.Fatalf("colliding entries must be dropped, got %+v", profiles)
	}
	if len(warnings) != 4 {
		t.Fatalf("want one warning per colliding entry, got %v", warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "skipped") {
			t.Fatalf("warnings must name the skipped entries, got %q", w)
		}
	}
}

func TestLoadCustomModelProfilesNameAndKindRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	base, err := NewModelProfile("base", "Base", ModelProfileKindCustom, storeTestConfigB())
	if err != nil {
		t.Fatalf("build base: %v", err)
	}
	tooLong := base
	tooLong.ID = "too-long"
	tooLong.Name = strings.Repeat("x", modelProfileNameMaxLength+1)
	wrongKind := base
	wrongKind.ID = "wrong-kind"
	wrongKind.Name = "Wrong Kind"
	wrongKind.Kind = ModelProfileKindPredefined
	writeRawModelProfilesFile(t, path, modelProfilesFormatVersion, []ModelProfile{base, tooLong, wrongKind})

	profiles, warnings := LoadCustomModelProfiles(path)
	if len(profiles) != 1 || profiles[0].ID != "base" {
		t.Fatalf("only the conforming entry may load, got %+v", profiles)
	}
	if len(warnings) != 2 {
		t.Fatalf("want warnings for the long name and the wrong kind, got %v", warnings)
	}
}

func TestSaveCustomModelProfilesRejectsInvalidSets(t *testing.T) {
	good, err := NewModelProfile("one", "One", ModelProfileKindCustom, storeTestConfigB())
	if err != nil {
		t.Fatalf("build good: %v", err)
	}

	t.Run("duplicate names within the set", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		dup := good
		dup.ID = "one-b"
		err := SaveCustomModelProfiles(path, []ModelProfile{good, dup})
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("duplicate names must fail the save, got err = %v", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("a rejected save must not create the file, stat err = %v", statErr)
		}
	})

	t.Run("predefined name collision", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		clash := good
		clash.ID = "clash"
		clash.Name = "Generic (model-agnostic)"
		if err := SaveCustomModelProfiles(path, []ModelProfile{clash}); err == nil {
			t.Fatal("predefined name collision must fail the save")
		}
	})

	t.Run("invalid knob value", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		bad := good
		bad.Config.Sampling.TopP = 5
		if err := SaveCustomModelProfiles(path, []ModelProfile{bad}); err == nil {
			t.Fatal("invalid knob values must fail the save")
		}
	})

	t.Run("predefined kind is not storable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		pre, ok := FindPredefinedModelProfile(ModelProfilesGenericProfileID)
		if !ok {
			t.Fatal("generic profile must exist")
		}
		if err := SaveCustomModelProfiles(path, []ModelProfile{pre}); err == nil {
			t.Fatal("saving a predefined-kind entry must fail")
		}
	})
}

func TestCreateCustomModelProfile(t *testing.T) {
	t.Run("slug from name", func(t *testing.T) {
		p, err := CreateCustomModelProfile("  My Cool Model  ", storeTestConfigB(), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if p.ID != "my-cool-model" {
			t.Fatalf("id = %q, want %q", p.ID, "my-cool-model")
		}
		if p.Name != "My Cool Model" {
			t.Fatalf("name = %q, want trimmed %q", p.Name, "My Cool Model")
		}
	})

	t.Run("id deduplicated against predefined and existing ids", func(t *testing.T) {
		// "Generic" slugs to the predefined id "generic" but does not
		// collide with any predefined *name* — the id must dedupe to -2.
		p, err := CreateCustomModelProfile("Generic", storeTestConfigB(), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if p.ID != "generic-2" {
			t.Fatalf("id = %q, want %q", p.ID, "generic-2")
		}
	})

	t.Run("name collision with existing custom rejected", func(t *testing.T) {
		p1, err := CreateCustomModelProfile("Twin", storeTestConfigB(), nil)
		if err != nil {
			t.Fatalf("create p1: %v", err)
		}
		if _, err := CreateCustomModelProfile("Twin", storeTestConfigA(), []ModelProfile{p1}); err == nil {
			t.Fatal("duplicate custom name must be rejected")
		}
	})

	t.Run("name collision with predefined rejected", func(t *testing.T) {
		if _, err := CreateCustomModelProfile("Generic (model-agnostic)", storeTestConfigB(), nil); err == nil {
			t.Fatal("predefined name must be rejected")
		}
	})

	t.Run("empty and oversized names rejected", func(t *testing.T) {
		if _, err := CreateCustomModelProfile("   ", storeTestConfigB(), nil); err == nil {
			t.Fatal("empty name must be rejected")
		}
		if _, err := CreateCustomModelProfile(strings.Repeat("n", modelProfileNameMaxLength+1), storeTestConfigB(), nil); err == nil {
			t.Fatal("oversized name must be rejected")
		}
	})

	t.Run("invalid config rejected", func(t *testing.T) {
		cfg := storeTestConfigB()
		cfg.Sampling.TopP = 5
		if _, err := CreateCustomModelProfile("Valid Name", cfg, nil); err == nil {
			t.Fatal("invalid knob values must be rejected")
		}
	})

	t.Run("non-latin name falls back to a valid slug", func(t *testing.T) {
		p, err := CreateCustomModelProfile("Феникс", storeTestConfigB(), nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if !modelProfileIDPattern.MatchString(p.ID) || p.ID == "" {
			t.Fatalf("id %q must be a non-empty valid slug", p.ID)
		}
		if p.ID != "profile" {
			t.Fatalf("id fallback = %q, want %q", p.ID, "profile")
		}
	})
}

func TestDeleteCustomModelProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-profiles.yaml")
	p1, err := CreateCustomModelProfile("First", storeTestConfigA(), nil)
	if err != nil {
		t.Fatalf("create p1: %v", err)
	}
	p2, err := CreateCustomModelProfile("Second", storeTestConfigB(), []ModelProfile{p1})
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}
	if err := SaveCustomModelProfiles(path, []ModelProfile{p1, p2}); err != nil {
		t.Fatalf("save: %v", err)
	}

	remaining, err := DeleteCustomModelProfile(path, p1.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != p2.ID {
		t.Fatalf("delete must return the remainder, got %+v", remaining)
	}
	loaded, warnings := LoadCustomModelProfiles(path)
	if len(warnings) != 0 || len(loaded) != 1 || loaded[0].ID != p2.ID {
		t.Fatalf("post-delete load = %+v, warnings = %v", loaded, warnings)
	}

	// Unknown and predefined ids are rejected; the file keeps its content.
	if _, err := DeleteCustomModelProfile(path, p1.ID); err == nil {
		t.Fatal("deleting an absent id must fail")
	}
	if _, err := DeleteCustomModelProfile(path, ModelProfilesGenericProfileID); err == nil {
		t.Fatal("deleting a predefined id must fail")
	}
	loaded, _ = LoadCustomModelProfiles(path)
	if len(loaded) != 1 || loaded[0].ID != p2.ID {
		t.Fatalf("failed deletes must leave the file untouched, got %+v", loaded)
	}

	// Deleting the last profile persists an empty set.
	if _, err := DeleteCustomModelProfile(path, p2.ID); err != nil {
		t.Fatalf("delete last: %v", err)
	}
	loaded, warnings = LoadCustomModelProfiles(path)
	if len(loaded) != 0 || len(warnings) != 0 {
		t.Fatalf("empty store after last delete: profiles=%v warnings=%v", loaded, warnings)
	}
}

// TestSaveCustomModelProfilesRefusesUnreadableExistingStore pins the pre-write
// guard: a full-set rewrite must never silently destroy content the fail-soft
// load cannot represent (foreign format version, unparseable YAML, or an entry
// the current validator rejects). Each refusal must leave the file byte-for-byte
// untouched.
func TestSaveCustomModelProfilesRefusesUnreadableExistingStore(t *testing.T) {
	valid, err := CreateCustomModelProfile("Keep Me", storeTestConfigB(), nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	t.Run("foreign format version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		writeRawModelProfilesFile(t, path, 2, []ModelProfile{{ID: "future", Name: "Future", Kind: ModelProfileKindCustom}})
		before, _ := os.ReadFile(path)
		err := SaveCustomModelProfiles(path, []ModelProfile{valid})
		if err == nil || !strings.Contains(err.Error(), "unsupported format version") {
			t.Fatalf("save over a foreign-version file must fail closed, got %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("a refused save must leave the foreign-version file untouched")
		}
	})

	t.Run("unparseable YAML", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		if err := os.WriteFile(path, []byte("version: 1\nprofiles:\n  - id: [unclosed\n"), 0o644); err != nil {
			t.Fatalf("write broken file: %v", err)
		}
		before, _ := os.ReadFile(path)
		err := SaveCustomModelProfiles(path, []ModelProfile{valid})
		if err == nil || !strings.Contains(err.Error(), "not valid YAML") {
			t.Fatalf("save over unparseable YAML must fail closed, got %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("a refused save must leave the unparseable file untouched")
		}
	})

	t.Run("invalid existing entry is not silently pruned", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		good, err := NewModelProfile("good-one", "Good One", ModelProfileKindCustom, storeTestConfigA())
		if err != nil {
			t.Fatalf("build good: %v", err)
		}
		bad := good
		bad.ID = "bad-value"
		bad.Name = "Bad Value"
		bad.Config.Sampling.TopP = 5 // out of (0, 1]
		writeRawModelProfilesFile(t, path, modelProfilesFormatVersion, []ModelProfile{good, bad})
		before, _ := os.ReadFile(path)
		err = SaveCustomModelProfiles(path, []ModelProfile{valid})
		if err == nil || !strings.Contains(err.Error(), "entry 2 is invalid") {
			t.Fatalf("save over a file with an invalid entry must fail closed, got %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("a refused save must leave the partially-invalid file untouched")
		}
	})

	t.Run("empty file is treated as pristine", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "model-profiles.yaml")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("write empty file: %v", err)
		}
		if err := SaveCustomModelProfiles(path, []ModelProfile{valid}); err != nil {
			t.Fatalf("an empty file must not block saving, got %v", err)
		}
		loaded, _ := LoadCustomModelProfiles(path)
		if len(loaded) != 1 || loaded[0].ID != valid.ID {
			t.Fatalf("saved set not persisted: %+v", loaded)
		}
	})
}

// TestCustomModelProfileNameLimitCountsRunes pins the rune-based (not byte-based)
// name-length bound: a name of exactly modelProfileNameMaxLength multibyte runes
// is accepted, one rune more is rejected.
func TestCustomModelProfileNameLimitCountsRunes(t *testing.T) {
	name := strings.Repeat("ф", modelProfileNameMaxLength) // 64 runes, 128 bytes
	if _, err := CreateCustomModelProfile(name, storeTestConfigB(), nil); err != nil {
		t.Fatalf("a %d-rune name must be accepted, got %v", modelProfileNameMaxLength, err)
	}
	if _, err := CreateCustomModelProfile(name+"ф", storeTestConfigB(), nil); err == nil {
		t.Fatal("a name one rune over the limit must be rejected")
	}
}

// TestPredefinedBonsaiIDCannotCollideWithLegacyCustom pins the upgrade
// compatibility the dotted embedded preset id exists for. Against the
// pre-bonsai catalog a custom profile named "Bonsai 2 27B" was creatable and
// derives exactly the id "bonsai-2-27b" (generateCustomModelProfileID collapses
// every non-[a-z0-9] run to '-'), so a dash-only predefined id would have
// claimed it on upgrade: the load would drop the operator's stored profile and
// ensureStoreWritable would then refuse every later custom write. The dotted
// "bonsai.2-27b" spelling cannot be produced by the custom generator, so the
// legacy entry keeps loading, stays resolvable as the active profile with its
// own tuning, and ordinary custom CRUD survives the catalog growth.
func TestPredefinedBonsaiIDCannotCollideWithLegacyCustom(t *testing.T) {
	dir := t.TempDir()
	path := ModelProfilesPath(dir)

	// The pre-upgrade state: a custom profile under the id a dash-only
	// embedded preset would later claim.
	legacy, err := CreateCustomModelProfile("Bonsai 2 27B", storeTestConfigB(), nil)
	if err != nil {
		t.Fatalf("create legacy custom profile: %v", err)
	}
	if legacy.ID != "bonsai-2-27b" {
		t.Fatalf("legacy id = %q, want the slug the custom generator derives for the name (bonsai-2-27b)", legacy.ID)
	}
	if err := SaveCustomModelProfiles(path, []ModelProfile{legacy}); err != nil {
		t.Fatalf("save legacy store: %v", err)
	}

	// The post-upgrade load through the production catalog path: the enlarged
	// predefined catalog must not reject the formerly valid entry.
	catalog, warnings := LoadModelProfilesCatalog(dir)
	if len(warnings) != 0 {
		t.Fatalf("catalog load produced warnings: %v", warnings)
	}
	pre, ok := FindModelProfile(catalog, "bonsai.2-27b")
	if !ok || pre.Kind != ModelProfileKindPredefined {
		t.Fatalf("predefined bonsai.2-27b missing from the catalog: %+v (ok=%v)", pre, ok)
	}
	got, ok := FindModelProfile(catalog, legacy.ID)
	if !ok || got.Kind != ModelProfileKindCustom {
		t.Fatalf("legacy custom profile %q missing from the catalog (ok=%v)", legacy.ID, ok)
	}
	if !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy custom profile changed across the catalog load:\n got=%+v\nwant=%+v", got, legacy)
	}

	// Identity resolution: the persisted active-profile reference keeps
	// resolving to the OPERATOR'S custom tuning, not to the predefined preset
	// that now sits beside it in the catalog.
	resolved, resolveWarnings := ResolveModelProfilesConfig(
		ModelProfilesPersistConfig{Enabled: true, ActiveProfile: legacy.ID}, catalog)
	if len(resolveWarnings) != 0 {
		t.Fatalf("resolution produced warnings: %v", resolveWarnings)
	}
	if !reflect.DeepEqual(resolved.EssentialTools, legacy.Config.EssentialTools) ||
		!reflect.DeepEqual(resolved.SystemPrompt, legacy.Config.SystemPrompt) ||
		!reflect.DeepEqual(resolved.Sampling, legacy.Config.Sampling) ||
		!reflect.DeepEqual(resolved.LoopHardening, legacy.Config.LoopHardening) ||
		!reflect.DeepEqual(resolved.Context, legacy.Config.Context) {
		t.Fatalf("resolved values drifted from the legacy custom tuning:\n got=%+v\nwant=%+v", resolved, legacy.Config)
	}
	if reflect.DeepEqual(resolved.Sampling, pre.Config.Sampling) {
		t.Fatalf("resolved sampling equals the predefined preset's (%+v): the identity was substituted", resolved.Sampling)
	}

	// Store writability: the fail-closed writer still accepts the store, so
	// creating and saving another custom profile keeps working after upgrade.
	next, err := CreateCustomModelProfile("Another Tuning", storeTestConfigA(), []ModelProfile{legacy})
	if err != nil {
		t.Fatalf("create another custom profile after upgrade: %v", err)
	}
	if err := SaveCustomModelProfiles(path, []ModelProfile{legacy, next}); err != nil {
		t.Fatalf("save after upgrade: %v", err)
	}
}
