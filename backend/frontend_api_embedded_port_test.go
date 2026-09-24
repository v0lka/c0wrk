package backend

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the pre-spawn loopback port check.
//
// The persisted port (embedded_llm.port, mirrored into manifest.json) is a
// PREFERENCE, not a reservation: loopback is shared with every other local
// process, and nothing holds the port between an install and the next load. The
// requirement is that a taken port does not fail the load — it walks upward until
// a bindable one is found.
//
// Two properties are load-bearing and each gets its own test below:
//
//   - the move reaches the SPAWN (the command line binds the free port) and the
//     readiness probe (so a foreign listener on the old port can never be
//     mistaken for the model);
//   - the move is PERSISTED, because the generated provider base_url is derived
//     from embedded_llm.port — an unpersisted move would leave every later
//     router build pointing at a socket this process does not own.

// takenBelow returns a prober that reports every port below free as taken, so a
// test stages a collision deterministically without occupying real ports.
func takenBelow(free int) embeddedllm.PortProber {
	return func(_ context.Context, port int) bool { return port >= free }
}

// TestEmbeddedBuildWiresThePreSpawnPortCheck pins the production wiring itself.
// core's EnsurePort seam defaults to nil ("trust the persisted port"), so a
// construction path that forgets this line silently loses the collision
// protection — and the failure it reopens is a readiness probe answered by a
// foreign listener, i.e. a false "loaded".
func TestEmbeddedBuildWiresThePreSpawnPortCheck(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	if server.EnsurePort == nil {
		t.Fatal("EnsurePort = nil in production; the persisted port would be bound " +
			"without a re-check, and a taken port would be reported as a loaded model")
	}
}

func TestEmbeddedEnsurePortKeepsAFreePersistedPort(t *testing.T) {
	const persisted = 4321
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, persisted)
	stubFreePortProbe(t, f)

	got, err := f.embeddedEnsurePort(context.Background(), persisted)
	if err != nil {
		t.Fatalf("embeddedEnsurePort: %v", err)
	}
	if got != persisted {
		t.Errorf("port = %d, want the persisted %d (a free preference must not churn)", got, persisted)
	}
	if cfg := f.embeddedConfig(); cfg.Port != persisted {
		t.Errorf("embedded_llm.port = %d, want it left at %d", cfg.Port, persisted)
	}
}

// The core requirement: a taken port increments until a free one is found, and
// the move is written back to config together with the generated provider
// base_url — on disk, not just in memory.
func TestEmbeddedEnsurePortMovesOffATakenPortAndPersistsIt(t *testing.T) {
	const persisted = 4321
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, persisted)
	// Both the persisted port and the next one are taken.
	f.embedded.portProbeFn = takenBelow(persisted + 2)

	got, err := f.embeddedEnsurePort(context.Background(), persisted)
	if err != nil {
		t.Fatalf("embeddedEnsurePort: %v", err)
	}
	if got != persisted+2 {
		t.Errorf("port = %d, want %d (the scan must walk upward past every collision)", got, persisted+2)
	}

	cfg := f.embeddedConfig()
	if cfg.Port != persisted+2 {
		t.Errorf("embedded_llm.port = %d, want the moved %d", cfg.Port, persisted+2)
	}
	wantURL := "http://" + config.EmbeddedLLMHost + ":" + strconv.Itoa(persisted+2) + "/v1"
	provider := f.config.LLM.OpenAICompatible[config.EmbeddedLLMProviderName]
	if provider.BaseURL != wantURL {
		t.Errorf("provider base_url = %q, want %q — the generated record must follow the move",
			provider.BaseURL, wantURL)
	}

	// The write must survive a reload: the base_url every later router build
	// derives comes from config.yaml, not from this process's memory.
	reloaded, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reloading the persisted config: %v", err)
	}
	if reloaded.EmbeddedLLM.Port != persisted+2 {
		t.Errorf("persisted embedded_llm.port = %d, want %d", reloaded.EmbeddedLLM.Port, persisted+2)
	}
	if got := reloaded.LLM.OpenAICompatible[config.EmbeddedLLMProviderName].BaseURL; got != wantURL {
		t.Errorf("persisted provider base_url = %q, want %q", got, wantURL)
	}
}

// A port move while the model is NOT installed must not write anything: there is
// no generated provider record to keep in sync, and a port in a section that
// reports installed:false reads as corrupt state.
func TestPersistEmbeddedPortSkipsTheWriteWhenNotInstalled(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.portProbeFn = takenBelow(4400)

	got, err := f.embeddedEnsurePort(context.Background(), 4321)
	if err != nil {
		t.Fatalf("embeddedEnsurePort: %v", err)
	}
	if got != 4400 {
		t.Errorf("port = %d, want 4400 — the load still gets a free port", got)
	}
	if cfg := f.embeddedConfig(); cfg.Port != 0 {
		t.Errorf("embedded_llm.port = %d, want it untouched at 0 while not installed", cfg.Port)
	}
}

// A persistence failure must not fail the load. The server binds the free port
// either way and the ensure-loaded transport redirects requests to the live port,
// so a config.yaml that cannot be written is an administrative problem — turning
// it into "the model is unusable" would be worse than the stale value.
func TestEmbeddedEnsurePortSurvivesAFailedConfigWrite(t *testing.T) {
	const persisted = 4321
	f, _, _ := newEmbeddedTestAPI(t)
	installEmbeddedLLM(t, f, persisted)
	f.embedded.portProbeFn = takenBelow(persisted + 1)
	// Point the save at a path that cannot be written.
	f.configPath = f.agentDir + "/does-not-exist/nested/config.yaml"

	got, err := f.embeddedEnsurePort(context.Background(), persisted)
	if err != nil {
		t.Fatalf("embeddedEnsurePort = %v, want the free port despite the failed write", err)
	}
	if got != persisted+1 {
		t.Errorf("port = %d, want %d", got, persisted+1)
	}
	// The in-memory value is rolled back by the save-or-rollback path, so the
	// config never claims a port that is not on disk.
	if cfg := f.embeddedConfig(); cfg.Port != persisted {
		t.Errorf("embedded_llm.port = %d, want the rollback to the persisted %d", cfg.Port, persisted)
	}
}

// The end-to-end half: a load whose persisted port is taken must spawn on the
// free one, become ready against THAT port, and report it to the UI.
func TestLoadMovesOffATakenPortAndServesOnTheNewOne(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)

	// A real /v1/models listener stands in for the server's own endpoint. The
	// persisted port is the one below it, so the scan has exactly one collision
	// to walk past and lands on the port that actually answers.
	_, livePort := modelsEndpoint(t)
	taken := livePort - 1

	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(taken, 32768))
	installEmbeddedLLM(t, f, taken)

	f.embedded.portProbeFn = takenBelow(livePort)
	var spawned []string
	f.embedded.spawnFn = func(_ context.Context, cmd embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		spawned = cmd.Args
		return newFakeEmbeddedProcess(4242), nil
	}
	tightenEmbeddedBudgets(t, f)

	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("LoadEmbeddedLLM with the persisted port taken: %v", err)
	}

	if len(spawned) == 0 {
		t.Fatal("no llama-server was spawned")
	}
	wantFlag := strconv.Itoa(livePort)
	if !hasFlagValue(spawned, "--port", wantFlag) {
		t.Errorf("spawn args = %v, want --port %s (the moved port)", spawned, wantFlag)
	}
	if hasFlagValue(spawned, "--port", strconv.Itoa(taken)) {
		t.Errorf("spawn args = %v still bind the taken port %d", spawned, taken)
	}

	status := f.GetEmbeddedLLMStatus()
	if !status.Loaded {
		t.Errorf("status = %+v, want loaded — readiness must be probed on the moved port", status)
	}
	if status.Port != livePort {
		t.Errorf("status.Port = %d, want the moved %d", status.Port, livePort)
	}
	if !strings.Contains(status.BaseURL, ":"+wantFlag+"/v1") {
		t.Errorf("status.BaseURL = %q, want the moved port %s", status.BaseURL, wantFlag)
	}
	if cfg := f.embeddedConfig(); cfg.Port != livePort {
		t.Errorf("embedded_llm.port = %d, want the persisted move to %d", cfg.Port, livePort)
	}
}

// hasFlagValue reports whether args carry `flag value` as a consecutive pair.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
