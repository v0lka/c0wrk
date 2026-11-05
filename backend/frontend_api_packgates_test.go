package backend

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if !strings.Contains(warn, "research skill-pack unseeded") {
		t.Errorf("disable log must record the unseed run, got:\n%s", warn)
	}
}

// TestSeedGlobalPacks_DisabledSurfaceUnseedsAtLaunch pins the launch
// reconciliation: when the persisted gate is off but the pack copies are
// still on disk (a failed toggle-time unseed, an interrupted run, or a
// raced toggle), the launch seed UNSEEDS the disabled surface — the disk
// converges to the config on every launch.
func TestSeedGlobalPacks_DisabledSurfaceUnseedsAtLaunch(t *testing.T) {
	f, _, _, _ := seedGlobalPacksTestFrontend(t)
	f.seedGlobalPacks()
	assertResearchSeeded(t, f, true)
	assertSpecsSeeded(t, f, true)

	off := false
	f.config = &config.Config{
		Research: config.SidebarPackConfig{Enabled: &off},
		Specs:    config.SidebarPackConfig{Enabled: &off},
	}

	f.seedGlobalPacks()

	assertResearchSeeded(t, f, false)
	assertSpecsSeeded(t, f, false)
}

// TestSeedGlobalPacks_LaunchUnseedsPreservesUserCopies pins that the launch
// reconcile is as non-destructive as the toggle-time unseed: a locally
// edited, pack-marked copy survives a launch with its surface disabled
// (content and marker intact) while the byte-identical copies disappear.
// The kept copy is named in a visible warning on the instance logger
// (intercepted and asserted at the source — the warning is behavior under
// test, mirroring TestSetResearchEnabled_PreservesModifiedCopyWithWarning).
func TestSeedGlobalPacks_LaunchUnseedsPreservesUserCopies(t *testing.T) {
	f, _, _, _ := seedGlobalPacksTestFrontend(t)
	// Capture the instance logger for the WHOLE test (seed + reconcile) so
	// nothing leaks to slog.Default() and the preservation warning is
	// assertable at its source.
	var logBuf bytes.Buffer
	f.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.seedGlobalPacks()

	skillsDir := config.SkillsDir(f.agentDir)
	editedName := specs.SpecSkillNames()[0]
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

	off := false
	f.config = &config.Config{Specs: config.SidebarPackConfig{Enabled: &off}}
	f.seedGlobalPacks()

	got, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("modified copy vanished at the launch reconcile: %v", err)
	}
	if !bytes.Equal(got, editedBody) {
		t.Errorf("modified copy content was changed by the launch reconcile")
	}
	if _, err := os.Stat(filepath.Join(skillsDir, editedName, seedVersionMarker)); err != nil {
		t.Errorf("modified copy lost its pack marker: %v", err)
	}
	for _, name := range specs.SpecSkillNames() {
		if name == editedName {
			continue
		}
		assertPackDirAbsent(t, skillsDir, name)
	}
	// The untouched research surface is unaffected by the specs reconcile.
	assertResearchSeeded(t, f, true)

	warn := logBuf.String()
	if !strings.Contains(warn, "kept on disk") || !strings.Contains(warn, editedName) {
		t.Errorf("launch reconcile must warn naming the preserved modified copy, got:\n%s", warn)
	}
	if !strings.Contains(warn, "spec skill-pack unseeded") {
		t.Errorf("launch reconcile must record the unseed run, got:\n%s", warn)
	}
}

// TestSeedGlobalPacks_GateMirrorOverridesStaleSnapshot pins the stale-gate
// half of the serialization contract: the launch seed re-reads the
// researchSeedMu-guarded gate mirror INSIDE its critical section, so a
// toggle that has applied (mirror written before its disk pass) always wins
// over the pre-lock config snapshot — the exact sequencing that would
// otherwise let a launch seed resurrect just-unseeded packs.
func TestSeedGlobalPacks_GateMirrorOverridesStaleSnapshot(t *testing.T) {
	f, _, _, _ := seedGlobalPacksTestFrontend(t)

	// Freeze a stale pre-lock snapshot: the config still says research=on
	// (a disable toggle has applied since) and specs=off (an enable toggle
	// has applied since); the mirror carries both toggles' values.
	off, on := false, true
	f.config = &config.Config{Specs: config.SidebarPackConfig{Enabled: &off}}
	f.packGateResearch = &off
	f.packGateSpecs = &on

	f.seedGlobalPacks()

	assertResearchSeeded(t, f, false)
	assertSpecsSeeded(t, f, true)
}

// TestSetResearchEnabled_WritesGateMirror pins that each toggle apply
// writes its researchSeedMu-guarded gate mirror (the value the launch
// seed's in-lock re-read honors) and leaves the untouched surface's mirror
// nil.
func TestSetResearchEnabled_WritesGateMirror(t *testing.T) {
	f, _, _ := newTestAPI(t)

	if err := f.SetResearchEnabled(false); err != nil {
		t.Fatalf("SetResearchEnabled(false): %v", err)
	}
	if f.packGateResearch == nil || *f.packGateResearch {
		t.Fatalf("packGateResearch = %v, want a mirrored false", f.packGateResearch)
	}
	if f.packGateSpecs != nil {
		t.Errorf("the untouched specs surface's mirror must stay nil, got %v", *f.packGateSpecs)
	}

	if err := f.SetSpecsEnabled(false); err != nil {
		t.Fatalf("SetSpecsEnabled(false): %v", err)
	}
	if f.packGateSpecs == nil || *f.packGateSpecs {
		t.Fatalf("packGateSpecs = %v, want a mirrored false", f.packGateSpecs)
	}
}

// TestSeedGlobalPacks_ConcurrentToggleConverges pins the serialization
// contract under a real overlap: a launch seed racing both surface toggles
// converges — once all three complete, the disk matches the persisted gates
// exactly, whatever the interleaving (the applies serialize on
// researchSeedMu and enforce the persisted value; the seed honors the gate
// mirror the applies write under the same lock).
func TestSeedGlobalPacks_ConcurrentToggleConverges(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.seedGlobalPacks()
	assertResearchSeeded(t, f, true)
	assertSpecsSeeded(t, f, true)

	var (
		wg          sync.WaitGroup
		researchErr error
		specsErr    error
	)
	wg.Add(3)
	go func() { defer wg.Done(); f.seedGlobalPacks() }()
	go func() { defer wg.Done(); researchErr = f.SetResearchEnabled(false) }()
	go func() { defer wg.Done(); specsErr = f.SetSpecsEnabled(false) }()
	wg.Wait()

	if researchErr != nil || specsErr != nil {
		t.Fatalf("toggle errors under the race: research=%v specs=%v", researchErr, specsErr)
	}
	assertResearchSeeded(t, f, false)
	assertSpecsSeeded(t, f, false)
}

// TestSeedGlobalPacks_ConcurrentEnableConverges pins the convergence
// contract in the enable direction: a launch seed racing an enable of a
// disabled surface ends with the packs seeded, never resurrected-away (the
// enable's apply is serialized after whatever the seed did and enforces the
// persisted on value).
func TestSeedGlobalPacks_ConcurrentEnableConverges(t *testing.T) {
	f, _, _ := newTestAPI(t)
	off := false
	f.config.Research.Enabled = &off
	f.seedGlobalPacks()
	assertResearchSeeded(t, f, false)

	var (
		wg     sync.WaitGroup
		toggle error
	)
	wg.Add(2)
	go func() { defer wg.Done(); f.seedGlobalPacks() }()
	go func() { defer wg.Done(); toggle = f.SetResearchEnabled(true) }()
	wg.Wait()

	if toggle != nil {
		t.Fatalf("SetResearchEnabled(true) under the race: %v", toggle)
	}
	assertResearchSeeded(t, f, true)
	assertSpecsSeeded(t, f, true)
}
