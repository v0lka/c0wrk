package backend

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the effective-context readback: the tier-1
// `llm.models."Bonsai 2 27B".context_window` override must stay HONEST.
//
// `domains/llm-providers.md` gives a tier-1 config override precedence over the
// tier-1.5 lazy probe, so an override frozen at the install's estimate can never
// be corrected by asking the model later — it shadows the answer. The load path
// is the only one that has a resident server to ask, so it is the one that reads
// `/props` and writes the measured value back. `GetConfig` itself stays
// network-free (pinned by TestGetConfig_EmbeddedProviderNetworkFree).
//
// Three properties are load-bearing and each gets its own test below:
//
//   - the measured value reaches config.yaml, on disk, not just in memory;
//   - a readback that fails leaves the previous value and never fails a load
//     that is already serving;
//   - a value that did not move produces no write and no config:updated event,
//     so a steady machine pays nothing on every cold start.

// modelsAndPropsEndpoint is modelsEndpoint plus the /props route the readback
// asks. nCtx is the PER-SLOT context and slots the count it is split across, so
// the effective total the supervisor must record is their product.
func modelsAndPropsEndpoint(t *testing.T, nCtx, slots int) (srv *httptest.Server, port int) {
	t.Helper()
	props := fmt.Sprintf(
		`{"default_generation_settings":{"n_ctx":%d,"model_path":"x"},"total_slots":%d}`, nCtx, slots)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"Bonsai 2 27B","object":"model"}]}`)
		case "/props":
			_, _ = io.WriteString(w, props)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL %q: %v", srv.URL, err)
	}
	port, err = strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parsing the test server port from %q: %v", srv.URL, err)
	}
	return srv, port
}

// embeddedContextWindow reads the tier-1 override the readback is supposed to
// keep honest.
func embeddedContextWindow(f *FrontendAPI) int {
	return f.config.LLM.Models[config.EmbeddedLLMModelName].ContextWindow
}

// TestEmbeddedBuildWiresTheLoadTimeMemorySeams pins the production wiring. All
// three core seams default to nil — "no load-time probe", "the all-Auto tuning"
// and "the manifest is the only record" — so a construction path that forgets a
// line silently loses the behaviour rather than failing loudly.
func TestEmbeddedBuildWiresTheLoadTimeMemorySeams(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	if server.ProbeDevices == nil {
		t.Error("ProbeDevices = nil in production; a load would launch the install's " +
			"shape on a machine whose accelerator may have changed")
	}
	if server.Tuning == nil {
		t.Error("Tuning = nil in production; a load-time re-plan would drop the " +
			"operator's memory plan and launch an all-Auto shape")
	}
	if server.PersistContext == nil {
		t.Error("PersistContext = nil in production; the measured context would " +
			"reach the manifest but never the tier-1 config override")
	}
}

// TestPersistEmbeddedContextWritesTheTierOneOverride is the core requirement:
// the measured value lands in config.yaml, on disk, and the port the install
// allocated is left alone.
func TestPersistEmbeddedContextWritesTheTierOneOverride(t *testing.T) {
	const persistedPort = 4321
	const measured = 98304
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, persistedPort)
	// The install wrote the override from its own estimate.
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the install's estimate reported no change")
	}

	if err := f.persistEmbeddedContext(context.Background(), measured); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}

	if got := embeddedContextWindow(f); got != measured {
		t.Errorf("llm.models override = %d, want the measured %d", got, measured)
	}
	if got := f.embeddedConfig().Port; got != persistedPort {
		t.Errorf("embedded_llm.port = %d, want it left at %d — a context readback "+
			"must not move the port the install allocated", got, persistedPort)
	}
	// The generated provider record still follows the untouched port.
	wantURL := "http://" + config.EmbeddedLLMHost + ":" + strconv.Itoa(persistedPort) + "/v1"
	if got := f.config.LLM.OpenAICompatible[config.EmbeddedLLMProviderName].BaseURL; got != wantURL {
		t.Errorf("provider base_url = %q, want %q", got, wantURL)
	}

	// The write must survive a reload: the override every later router build
	// derives comes from config.yaml, not from this process's memory.
	reloaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reloading the persisted config: %v", err)
	}
	if got := reloaded.LLM.Models[config.EmbeddedLLMModelName].ContextWindow; got != measured {
		t.Errorf("persisted override = %d, want %d", got, measured)
	}
}

// TestPersistEmbeddedContextIsANoOpWhenTheValueAlreadyAgrees keeps a steady
// machine cheap: no rewrite of config.yaml and no config:updated event for a
// value that did not move, on every cold start.
func TestPersistEmbeddedContextIsANoOpWhenTheValueAlreadyAgrees(t *testing.T) {
	const measured = 32768
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(measured) {
		t.Fatal("pinning the estimate reported no change")
	}
	if err := config.Save(f.config, f.configPath); err != nil {
		t.Fatalf("saving the baseline config: %v", err)
	}
	before, err := os.Stat(f.configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := f.persistEmbeddedContext(context.Background(), measured); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}

	after, err := os.Stat(f.configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("config.yaml was rewritten for a context that did not change")
	}
	if got := embeddedContextWindow(f); got != measured {
		t.Errorf("llm.models override = %d, want it left at %d", got, measured)
	}
}

// A readback while the model is NOT installed must not write anything: there is
// no generated provider record to keep in sync, and an override for a model
// nobody can load reads as corrupt state.
func TestPersistEmbeddedContextSkipsTheWriteWhenNotInstalled(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	if err := f.persistEmbeddedContext(context.Background(), 65536); err != nil {
		t.Fatalf("persistEmbeddedContext while not installed: %v", err)
	}
	if got := embeddedContextWindow(f); got != 0 {
		t.Errorf("llm.models override = %d, want no override for an uninstalled model", got)
	}
	if _, ok := f.config.LLM.OpenAICompatible[config.EmbeddedLLMProviderName]; ok {
		t.Error("a readback while not installed generated the provider record")
	}
}

// A non-positive value is refused rather than written: the override treats <= 0
// as "leave the existing one alone", and a non-positive window would fail
// validate() on the next config load — turning an administrative correction
// into an unloadable config.
func TestPersistEmbeddedContextIgnoresANonPositiveValue(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}

	for _, bad := range []int{0, -1} {
		if err := f.persistEmbeddedContext(context.Background(), bad); err != nil {
			t.Fatalf("persistEmbeddedContext(%d): %v", bad, err)
		}
		if got := embeddedContextWindow(f); got != 16384 {
			t.Errorf("persistEmbeddedContext(%d) moved the override to %d, want it left at 16384",
				bad, got)
		}
	}
}

// TestPersistEmbeddedContextIgnoresAnOutOfRangeValue is the CEILING side of the
// same guard. This is the one context figure in the subsystem that arrives from
// an external process over HTTP, and the override it lands in is a tier-1 value
// that shadows the tier-1.5 lazy probe — so a poisoned readback (a squatted
// loopback port answering /props with an absurd n_ctx, or a perSlot*slots product
// that wrapped) would otherwise be persisted durably, in TWO stores, and never
// corrected. An enormous window is worse than a wrong one: every prompt "fits",
// so context compaction silently stops triggering for this model.
//
// The ceiling itself must still be written — it is an absurdity guard, not a
// policy, and refusing a legitimate value would be its own bug.
func TestPersistEmbeddedContextIgnoresAnOutOfRangeValue(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}

	for _, absurd := range []int{
		config.MaxModelContextWindow + 1,
		math.MaxInt,
		math.MinInt64,
	} {
		if err := f.persistEmbeddedContext(context.Background(), absurd); err != nil {
			t.Fatalf("persistEmbeddedContext(%d): %v", absurd, err)
		}
		if got := embeddedContextWindow(f); got != 16384 {
			t.Errorf("persistEmbeddedContext(%d) moved the override to %d, want it left at 16384",
				absurd, got)
		}
		if record, ok := f.embedded.installRecord(); ok && record.ContextSize == absurd {
			t.Errorf("persistEmbeddedContext(%d) poisoned the cached install record", absurd)
		}
	}

	// The boundary value is legitimate and must reach the override.
	if err := f.persistEmbeddedContext(context.Background(), config.MaxModelContextWindow); err != nil {
		t.Fatalf("persistEmbeddedContext at the ceiling: %v", err)
	}
	if got := embeddedContextWindow(f); got != config.MaxModelContextWindow {
		t.Errorf("llm.models override = %d, want the ceiling %d to be written",
			got, config.MaxModelContextWindow)
	}
	// And the config it produced must still load: a write that validate() would
	// reject turns an administrative correction into an unloadable config. Save
	// does not validate, so the round trip through the loader is the check.
	if _, err := config.LoadWithResult(f.configPath); err != nil {
		t.Errorf("the persisted config no longer loads: %v", err)
	}
}

// A persistence failure is reported to core, which logs it and keeps the load
// successful — the model is resident and serving, and the manifest already
// carries the corrected value, so the next load retries. The in-memory config is
// rolled back, so it never claims a value that is not on disk.
func TestPersistEmbeddedContextSurvivesAFailedConfigWrite(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}
	// Point the save at a path that cannot be written.
	f.configPath = f.agentDir + "/does-not-exist/nested/config.yaml"

	err := f.persistEmbeddedContext(context.Background(), 65536)
	if err == nil {
		t.Fatal("persistEmbeddedContext reported success for an unwritable config path")
	}
	if got := embeddedContextWindow(f); got != 16384 {
		t.Errorf("llm.models override = %d after a failed write, want the rollback to 16384", got)
	}
}

// The cached install record is what GetEmbeddedLLMStatus and the
// embedded_llm:state payload answer from, so it must follow the correction —
// otherwise the UI keeps reporting the install's estimate beside a config that
// carries the measured one.
func TestPersistEmbeddedContextUpdatesTheCachedInstallRecord(t *testing.T) {
	const measured = 65536
	f, _, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 16384))
	installEmbeddedLLM(t, f, 4321)

	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	if err := server.SetInstalled(true); err != nil {
		t.Fatalf("SetInstalled: %v", err)
	}
	if got := f.GetEmbeddedLLMStatus().ContextSize; got != 16384 {
		t.Fatalf("status context_size = %d before the readback, want the manifest's 16384", got)
	}

	if err := f.persistEmbeddedContext(context.Background(), measured); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}
	if got := f.GetEmbeddedLLMStatus().ContextSize; got != measured {
		t.Errorf("status context_size = %d, want the measured %d", got, measured)
	}
}

// The end-to-end half: a load whose server reports a different context than the
// install estimated must persist the REPORTED one — this is the criterion that
// keeps the tier-1 override from being permanently stale. The install record
// carries a plan whose context (the launched -c) is at or above the reported
// figure: the readback clamp (core's recordEffectiveContext) never exceeds the
// launched context, and the plan's own context is RAM-tier dependent, so the
// reported figure is held below the 65536 fit floor every plan's context is
// held to — above the estimate, below the clamp.
func TestLoadPersistsTheContextTheServerReported(t *testing.T) {
	const estimated = 16384
	const reported = 60000
	const launched = 98304
	f, _, _ := newEmbeddedTestAPI(t)

	_, livePort := modelsAndPropsEndpoint(t, reported, 1)
	manifest := embeddedTestManifest(livePort, estimated)
	layers := 99
	manifest.Plan = &embeddedllm.MemoryPlan{
		Fit:           false,
		Layers:        &layers,
		ContextSize:   launched,
		KVType:        embeddedllm.KVTypeF16,
		Packing:       embeddedllm.PackingPQ2_0,
		KVOffload:     true,
		MMProjOffload: true,
		Parallel:      1,
		GPUFamily:     embeddedllm.GPUFamilyAppleSilicon,
	}
	writeEmbeddedInstallTree(t, f.agentDir, manifest)
	installEmbeddedLLM(t, f, livePort)
	if !f.config.SyncEmbeddedLLMProvider(estimated) {
		t.Fatal("pinning the install's estimate reported no change")
	}
	stubFreePortProbe(t, f)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return newFakeEmbeddedProcess(4242), nil
	}
	tightenEmbeddedBudgets(t, f)

	if got := embeddedContextWindow(f); got != estimated {
		t.Fatalf("precondition: the override is %d, want the install's estimate %d", got, estimated)
	}

	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("LoadEmbeddedLLM: %v", err)
	}

	if got := embeddedContextWindow(f); got != reported {
		t.Errorf("llm.models override = %d, want the reported %d", got, reported)
	}
	reloaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reloading the persisted config: %v", err)
	}
	if got := reloaded.LLM.Models[config.EmbeddedLLMModelName].ContextWindow; got != reported {
		t.Errorf("persisted override = %d, want the reported %d", got, reported)
	}
	// The manifest core rewrote carries the same figure, so the two records
	// cannot disagree.
	onDisk, err := embeddedllm.ReadManifest(mustEmbeddedManifestPath(t, f))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if onDisk.ContextSize != reported {
		t.Errorf("manifest context_size = %d, want the reported %d", onDisk.ContextSize, reported)
	}
	if got := f.GetEmbeddedLLMStatus().ContextSize; got != reported {
		t.Errorf("status context_size = %d, want the reported %d", got, reported)
	}
}

// A server that does not answer /props must not cost the user their model, and
// must not invent a value: the estimate stays, on disk and in config.
func TestLoadKeepsTheEstimateWhenTheServerReportsNoContext(t *testing.T) {
	const estimated = 16384
	f, _, _ := newEmbeddedTestAPI(t)

	// modelsEndpoint answers only /v1/models; /props is a 404.
	_, livePort := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(livePort, estimated))
	installEmbeddedLLM(t, f, livePort)
	if !f.config.SyncEmbeddedLLMProvider(estimated) {
		t.Fatal("pinning the install's estimate reported no change")
	}
	stubFreePortProbe(t, f)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		return newFakeEmbeddedProcess(4242), nil
	}
	tightenEmbeddedBudgets(t, f)

	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("LoadEmbeddedLLM with a failing readback: %v", err)
	}
	if got := embeddedContextWindow(f); got != estimated {
		t.Errorf("llm.models override = %d, want the estimate left at %d", got, estimated)
	}
	if !f.GetEmbeddedLLMStatus().Loaded {
		t.Error("a failed readback left the model unloaded")
	}
}

// TestEmbeddedTuningReadsTheLiveConfig pins why Server.Tuning is a FUNCTION and
// not a value: the supervisor is built once and cached, while a settings save
// can change embedded_llm.tuning at any point in between. A load must plan with
// the tuning in force when it runs.
func TestEmbeddedTuningReadsTheLiveConfig(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	if server.Tuning == nil {
		t.Fatal("Server.Tuning is not wired")
	}
	if got := server.Tuning(); got.KVType != "" {
		t.Errorf("an unauthored tuning section translated to KVType %q, want the all-Auto zero", got.KVType)
	}

	precision := string(embeddedllm.KVTypeQ4_0)
	f.configMu.Lock()
	f.config.EmbeddedLLM.Tuning.KVCacheType = &precision
	f.configMu.Unlock()

	got := server.Tuning()
	if got.KVType != embeddedllm.KVTypeQ4_0 {
		t.Errorf("Server.Tuning().KVType = %q, want %q — the load path must read the live config",
			got.KVType, embeddedllm.KVTypeQ4_0)
	}
}

// TestEmbeddedTuningIsFailSoft pins the translation's own fallback: a section
// that cannot be translated yields the all-Auto plan rather than failing a load
// over a config surface the load path did not write.
func TestEmbeddedTuningIsFailSoft(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	if _, _, err := f.embeddedBuild(); err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}

	bogus := "q5_0" // excluded by core on a measured long-context slowdown
	f.configMu.Lock()
	f.config.EmbeddedLLM.Tuning.KVCacheType = &bogus
	f.configMu.Unlock()

	if got := f.embeddedTuning(); got.KVType != "" {
		t.Errorf("an untranslatable tuning produced KVType %q, want the all-Auto zero", got.KVType)
	}
}

func mustEmbeddedManifestPath(t *testing.T, f *FrontendAPI) string {
	t.Helper()
	layout, err := embeddedllm.NewLayout(
		config.RuntimesDir(f.agentDir), config.EmbeddedModelDir(f.agentDir))
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	path, err := layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	return path
}

// TestPersistEmbeddedContextSchedulesAnAsyncRouterRefresh pins the read-back
// contract. persistEmbeddedContext runs inside Server.Load — on behalf of the
// request the ensure-loaded transport is waiting on — so it must not rebuild
// the router synchronously; but the previous window is the install-time
// DefaultFitMinContext ESTIMATE, which under-represents the server and makes
// the live router refuse every prompt above (window − reserve) × 95% until an
// unrelated rebuild. The successful persist therefore schedules the refresh,
// and the refreshed rebuild must also PUSH the fresh metadata into the live
// per-session model registries (UpdateModelOverrides) — RebuildRouter alone
// only fixes sessions built afterwards.
func TestPersistEmbeddedContextSchedulesAnAsyncRouterRefresh(t *testing.T) {
	f, _, mock := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}

	// Capture the scheduled work instead of running it, so the synchronous
	// half of the contract is observable: nothing may rebuild DURING persist.
	var scheduled []func()
	f.embeddedRefreshDispatch = func(fn func()) { scheduled = append(scheduled, fn) }

	beforeRebuild := mock.rebuildRouterCalls
	beforePush := mock.updateModelOverridesCalls
	if err := f.persistEmbeddedContext(context.Background(), 65536); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}

	if got := mock.rebuildRouterCalls; got != beforeRebuild {
		t.Fatalf("the router was rebuilt %d time(s) inside the load path, want 0", got-beforeRebuild)
	}
	if len(scheduled) != 1 {
		t.Fatalf("the refresh was scheduled %d time(s), want exactly 1", len(scheduled))
	}

	// Flushing the scheduled work performs the rebuild AND the live-session
	// push, exactly once.
	scheduled[0]()
	if got := mock.rebuildRouterCalls; got != beforeRebuild+1 {
		t.Errorf("RebuildRouter ran %d time(s) after the flush, want 1", got-beforeRebuild)
	}
	if got := mock.updateModelOverridesCalls; got != beforePush+1 {
		t.Errorf("UpdateModelOverrides ran %d time(s) after the flush, want 1", got-beforePush)
	}

	// The single-flight window is closed: a fresh change schedules again.
	// (The value is persisted directly — persisting it pre-synced through
	// SyncEmbeddedLLMProvider would make the persist itself a no-op.)
	if err := f.persistEmbeddedContext(context.Background(), 131072); err != nil {
		t.Fatalf("second persistEmbeddedContext: %v", err)
	}
	if len(scheduled) != 2 {
		t.Errorf("the second change scheduled %d refresh(es), want 1 more", len(scheduled)-1)
	}
}

// TestPersistEmbeddedContextUnchangedContextSchedulesNothing: a value that did
// not move produces no write, no config:updated — and no refresh.
func TestPersistEmbeddedContextUnchangedContextSchedulesNothing(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)

	scheduled := 0
	f.embeddedRefreshDispatch = func(func()) { scheduled++ }

	if err := f.persistEmbeddedContext(context.Background(), 65536); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}
	if err := f.persistEmbeddedContext(context.Background(), 65536); err != nil {
		t.Fatalf("second persistEmbeddedContext: %v", err)
	}
	if scheduled != 1 {
		t.Errorf("the refresh was scheduled %d time(s) across a change and a no-op, want 1", scheduled)
	}
}

// TestPersistEmbeddedContextFailedPersistSchedulesNothing: a save that rolled
// back must not leave a refresh scheduled against a config that no longer
// carries the corrected window.
func TestPersistEmbeddedContextFailedPersistSchedulesNothing(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)
	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}

	scheduled := 0
	f.embeddedRefreshDispatch = func(func()) { scheduled++ }

	// An unpersistable path makes config.Save fail and saveOrRollback restore
	// the previous state.
	f.configPath = filepath.Join(t.TempDir(), "no-such-dir", "config.yaml")

	if err := f.persistEmbeddedContext(context.Background(), 65536); err == nil {
		t.Fatal("persistEmbeddedContext: expected the failed save to surface")
	}
	if scheduled != 0 {
		t.Errorf("the refresh was scheduled %d time(s) after a failed persist, want 0", scheduled)
	}
}

// TestEmbeddedRefreshRerunsForAChangeLandedMidFlight: a change scheduling
// while a refresh is already open must not be lost — the open loop reruns the
// refresh once more before closing (the dirty-flag single-flight).
func TestEmbeddedRefreshRerunsForAChangeLandedMidFlight(t *testing.T) {
	f, _, mock := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)

	// Hold every scheduled refresh instead of running it, so a second
	// schedule lands while the first is guaranteed to still be "open".
	var pending []func()
	f.embeddedRefreshDispatch = func(fn func()) { pending = append(pending, fn) }

	if !f.config.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("pinning the estimate reported no change")
	}
	if err := f.persistEmbeddedContext(context.Background(), 65536); err != nil {
		t.Fatalf("first persistEmbeddedContext: %v", err)
	}
	// A second change lands while the first refresh is still held open.
	if err := f.persistEmbeddedContext(context.Background(), 131072); err != nil {
		t.Fatalf("second persistEmbeddedContext: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("two changes produced %d scheduled refresh(es), want 1 (single-flight)", len(pending))
	}

	// The single open loop observes the dirty flag, refreshes a second time,
	// and only then closes: two rebuilds in total.
	pending[0]()
	if got := mock.rebuildRouterCalls; got != 2 {
		t.Errorf("RebuildRouter ran %d time(s), want 2 (one per landed change)", got)
	}

	// And the window is genuinely closed afterwards: a further change
	// schedules a fresh refresh instead of setting a dead flag.
	if err := f.persistEmbeddedContext(context.Background(), 262144); err != nil {
		t.Fatalf("third persistEmbeddedContext: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("the third change scheduled %d new refresh(es), want 1", len(pending)-1)
	}
}

// TestPersistEmbeddedContextPushesTheDisplayWindow pins the live-session half
// of the readback: when the measured window MOVES, the correction is pushed to
// the session manager's emitters (model-scoped to the embedded model) so an
// idle session — or the tail of a task that started before the correction —
// stops displaying fill percentages and compaction cards against the stale
// basis. A value that did not move (the steady-machine common case) and an
// out-of-range readback push nothing.
func TestPersistEmbeddedContextPushesTheDisplayWindow(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, 4321)

	type displayPush struct {
		model  string
		window int
	}
	var pushes []displayPush
	f.displayWindowPush = func(model string, window int) {
		pushes = append(pushes, displayPush{model, window})
	}

	const measured = 98304
	if err := f.persistEmbeddedContext(context.Background(), measured); err != nil {
		t.Fatalf("persistEmbeddedContext: %v", err)
	}
	if len(pushes) != 1 || pushes[0].model != config.EmbeddedLLMModelName || pushes[0].window != measured {
		t.Fatalf("display window pushes = %+v, want exactly [{%s %d}]",
			pushes, config.EmbeddedLLMModelName, measured)
	}

	// A value that did not move writes nothing and pushes nothing.
	pushes = nil
	if err := f.persistEmbeddedContext(context.Background(), measured); err != nil {
		t.Fatalf("steady persistEmbeddedContext: %v", err)
	}
	if len(pushes) != 0 {
		t.Errorf("a steady readback pushed the display window: %+v", pushes)
	}

	// An out-of-range readback is skipped before the persist — the emitters
	// must not hear about it either.
	pushes = nil
	if err := f.persistEmbeddedContext(context.Background(), config.MaxModelContextWindow+1); err != nil {
		t.Fatalf("out-of-range persistEmbeddedContext: %v", err)
	}
	if len(pushes) != 0 {
		t.Errorf("an out-of-range readback pushed the display window: %+v", pushes)
	}
}
