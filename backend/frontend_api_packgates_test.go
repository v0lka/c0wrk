package backend

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/specs"
)

// assertPackDirAbsent/-present are the pack-entry assertions shared by the
// sidebar-gate tests: a seeded entry is a directory under the global
// skills/agents dirs.
func assertPackDirAbsent(t *testing.T, baseDir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(baseDir, name)); !os.IsNotExist(err) {
		t.Errorf("pack entry %s still present under %s (stat err=%v)", name, baseDir, err)
	}
}

func assertPackDirPresent(t *testing.T, baseDir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(baseDir, name)); err != nil {
		t.Errorf("pack entry %s missing under %s: %v", name, baseDir, err)
	}
}

// assertResearchSeeded / assertSpecsSeeded assert the full per-surface pack
// sets (skills + agent profiles) in their respective global directories.
func assertResearchSeeded(t *testing.T, f *FrontendAPI, want bool) {
	t.Helper()
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)
	for _, name := range research.ResearchSkillNames() {
		if want {
			assertPackDirPresent(t, skillsDir, name)
		} else {
			assertPackDirAbsent(t, skillsDir, name)
		}
	}
	for _, name := range papers.SkillNames() {
		if want {
			assertPackDirPresent(t, skillsDir, name)
		} else {
			assertPackDirAbsent(t, skillsDir, name)
		}
	}
	for _, name := range research.ResearchAgentNames() {
		if want {
			assertPackDirPresent(t, agentsDir, name)
		} else {
			assertPackDirAbsent(t, agentsDir, name)
		}
	}
}

func assertSpecsSeeded(t *testing.T, f *FrontendAPI, want bool) {
	t.Helper()
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)
	for _, name := range specs.SpecSkillNames() {
		if want {
			assertPackDirPresent(t, skillsDir, name)
		} else {
			assertPackDirAbsent(t, skillsDir, name)
		}
	}
	for _, name := range specs.SpecAgentNames() {
		if want {
			assertPackDirPresent(t, agentsDir, name)
		} else {
			assertPackDirAbsent(t, agentsDir, name)
		}
	}
}

// TestSeedGlobalPacks_ResearchGateOff pins the launch-side gate: with config
// research.enabled off, the research/paper packs are NOT seeded while the
// specs packs still are — a disabled surface's packs must not resurrect on
// restart.
func TestSeedGlobalPacks_ResearchGateOff(t *testing.T) {
	f, _, _, _ := seedGlobalPacksTestFrontend(t)
	off := false
	f.config = &config.Config{Research: config.SidebarPackConfig{Enabled: &off}}

	f.seedGlobalPacks()

	assertResearchSeeded(t, f, false)
	assertSpecsSeeded(t, f, true)
}

// TestSeedGlobalPacks_SpecsGateOff pins the launch-side gate for the specs
// surface: specs.enabled off leaves the spec packs unseeded while the
// research/paper packs seed normally.
func TestSeedGlobalPacks_SpecsGateOff(t *testing.T) {
	f, _, _, _ := seedGlobalPacksTestFrontend(t)
	off := false
	f.config = &config.Config{Specs: config.SidebarPackConfig{Enabled: &off}}

	f.seedGlobalPacks()

	assertSpecsSeeded(t, f, false)
	assertResearchSeeded(t, f, true)
}

// TestSetResearchEnabled_DisableUnseedsAndPersists pins the disable half of
// the RPC contract: the flip reaches config.yaml as an explicit value and
// GetConfig in the same call, the research/paper packs disappear from the
// global dirs while the specs packs stay, and config:updated is announced.
func TestSetResearchEnabled_DisableUnseedsAndPersists(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)
	updated := make(chan string, 4)
	f.emitEvent = func(name string, _ ...any) { updated <- name }

	f.seedGlobalPacks()
	assertResearchSeeded(t, f, true)
	assertSpecsSeeded(t, f, true)

	if err := f.SetResearchEnabled(false); err != nil {
		t.Fatalf("SetResearchEnabled(false): %v", err)
	}

	assertResearchSeeded(t, f, false)
	assertSpecsSeeded(t, f, true)

	if f.config.Research.PackEnabled() {
		t.Errorf("in-memory config still reports research enabled after disabling")
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if loaded.Research.Enabled == nil {
		t.Errorf("persisted research.enabled must be an explicit value, not the nil default")
	}
	if loaded.Research.PackEnabled() {
		t.Errorf("persisted research.enabled = on, want explicit off")
	}
	if got := f.GetConfig(); !got.Loaded || got.Research.Enabled {
		t.Errorf("GetConfig().research.enabled = %v (loaded=%v), want false", got.Research.Enabled, got.Loaded)
	}
	if got := f.GetConfig(); !got.Specs.Enabled {
		t.Errorf("GetConfig().specs.enabled = false, want true (untouched surface)")
	}
	select {
	case name := <-updated:
		if name != EventConfigUpdated {
			t.Errorf("announced event = %q, want %q", name, EventConfigUpdated)
		}
	case <-time.After(2 * time.Second):
		t.Error("no config:updated announced after the toggle")
	}
}

// TestSetResearchEnabled_EnableReseedsPacks pins the enable half: after a
// disable, enabling seeds the research/paper packs back and persists the
// explicit on value.
func TestSetResearchEnabled_EnableReseedsPacks(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	f.seedGlobalPacks()
	if err := f.SetResearchEnabled(false); err != nil {
		t.Fatalf("SetResearchEnabled(false): %v", err)
	}
	assertResearchSeeded(t, f, false)

	if err := f.SetResearchEnabled(true); err != nil {
		t.Fatalf("SetResearchEnabled(true): %v", err)
	}

	assertResearchSeeded(t, f, true)
	assertSpecsSeeded(t, f, true)
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if loaded.Research.Enabled == nil || !loaded.Research.PackEnabled() {
		t.Errorf("persisted research.enabled = %+v, want explicit on", loaded.Research.Enabled)
	}
}

// TestSetResearchEnabled_NoOpDoesNotWriteConfig pins the no-op contract:
// enabling an already-on (nil default) gate must not create the config file.
func TestSetResearchEnabled_NoOpDoesNotWriteConfig(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	if err := f.SetResearchEnabled(true); err != nil {
		t.Fatalf("SetResearchEnabled(true) on nil default: %v", err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("no-op enable must not write the config file (stat err = %v)", err)
	}
}

// TestSetResearchEnabled_RollbackOnFailedSave pins the persistence contract:
// a failed disk write returns an error, leaves the in-memory gate at its
// previous value, and skips the pack side-effect (the seeded dirs stay).
func TestSetResearchEnabled_RollbackOnFailedSave(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.configPath = filepath.Join(t.TempDir(), "missing", "config.yaml")

	f.seedGlobalPacks()
	assertResearchSeeded(t, f, true)

	if err := f.SetResearchEnabled(false); err == nil {
		t.Fatal("expected an error when the config persist fails")
	}
	if !f.config.Research.PackEnabled() {
		t.Errorf("in-memory gate must roll back to on after a failed save")
	}
	assertResearchSeeded(t, f, true)
}

// TestSetSpecsEnabled_DisableUnseedsAndPersists pins the specs RPC's
// disable half: the spec skill/agent packs disappear while the research/
// paper packs stay, and the explicit off value persists.
func TestSetSpecsEnabled_DisableUnseedsAndPersists(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	f.seedGlobalPacks()
	assertSpecsSeeded(t, f, true)

	if err := f.SetSpecsEnabled(false); err != nil {
		t.Fatalf("SetSpecsEnabled(false): %v", err)
	}

	assertSpecsSeeded(t, f, false)
	assertResearchSeeded(t, f, true)

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if loaded.Specs.Enabled == nil || loaded.Specs.PackEnabled() {
		t.Errorf("persisted specs.enabled = %+v, want explicit off", loaded.Specs.Enabled)
	}
}

// TestSetResearchEnabled_PreservesModifiedCopyWithWarning pins the
// preserve_warn contract end-to-end: a pack-marked, locally edited skill
// survives the disable with its content and marker intact, while the
// byte-identical copies are removed; the kept copy is named in a visible
// warning on the instance logger (intercepted and asserted at the source —
// the warning is behavior under test).
func TestSetResearchEnabled_PreservesModifiedCopyWithWarning(t *testing.T) {
	f, _, _ := newTestAPI(t)

	f.seedGlobalPacks()

	skillsDir := config.SkillsDir(f.agentDir)
	editedName := research.ResearchSkillNames()[0]
	editedPath := filepath.Join(skillsDir, editedName, "SKILL.md")
	orig, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("read seeded %s: %v", editedName, err)
	}
	const localEdit = "\n<!-- local edit -->\n"
	editedBody := make([]byte, 0, len(orig)+len(localEdit))
	editedBody = append(editedBody, orig...)
	editedBody = append(editedBody, localEdit...)
	if err := os.WriteFile(editedPath, editedBody, 0o644); err != nil {
		t.Fatalf("edit seeded %s: %v", editedName, err)
	}

	var logBuf bytes.Buffer
	f.logger = slog.New(slog.NewTextHandler(&logBuf, nil))

	if err := f.SetResearchEnabled(false); err != nil {
		t.Fatalf("SetResearchEnabled(false): %v", err)
	}

	got, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("modified copy vanished: %v", err)
	}
	if !bytes.Equal(got, editedBody) {
		t.Errorf("modified copy content was changed by the unseed")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, editedName, seedVersionMarker)); err != nil {
		t.Errorf("modified copy lost its pack marker: %v", err)
	}
	for _, name := range research.ResearchSkillNames() {
		if name == editedName {
			continue
		}
		assertPackDirAbsent(t, skillsDir, name)
	}

	warn := logBuf.String()
	if !strings.Contains(warn, "kept on disk") || !strings.Contains(warn, editedName) {
		t.Errorf("disable log must warn naming the preserved modified copy, got:\n%s", warn)
	}
	if !strings.Contains(warn, "research surface disabled") {
		t.Errorf("disable log must record the unseed run, got:\n%s", warn)
	}
}
