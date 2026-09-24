package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the embedded local-model startup/shutdown wiring
// (desktop/startup_phases.go initEmbeddedLLM / stopEmbeddedLLM).
//
// The startup invariant under test is the one the phase exists for: restoring
// the state is the WHOLE of the startup work — no download, no hardware probe,
// no network connection and no model load, so startup neither depends on the
// network nor blocks on a multi-gigabyte weight load. The mechanics of stopping
// a live server belong to the backend and core layers (and are covered there
// end-to-end with a faked process); what this layer owns is that Shutdown
// actually asks for the stop, which is what the seam-based test below pins.

// embeddedFixture is an App wired to a real FrontendAPI over a temp agent dir,
// with an event recorder in place of the Wails runtime.
type embeddedFixture struct {
	app      *App
	recorder *emitRecorder
	agentDir string
	layout   embeddedllm.Layout
}

// newEmbeddedFixture builds the fixture. The config is deliberately left
// without ApplyDefaults' skill/agent directory lists so no filesystem watcher
// is started against the real user directories.
func newEmbeddedFixture(t *testing.T) *embeddedFixture {
	t.Helper()
	agentDir := t.TempDir()
	recorder := &emitRecorder{}

	cfg := &config.Config{}
	cfg.LLM.DefaultModel = "claude-3-opus"
	cfg.LLM.Anthropic.APIKey = "sk-test"
	cfg.LLM.Anthropic.Models = []string{"claude-3-opus"}

	app := NewApp()
	app.logger = testLoggerForPhases()
	app.wailsEmit = recorder.emit
	app.FrontendAPI = backend.NewFrontendAPI(backend.FrontendAPIConfig{
		Config:    cfg,
		AgentDir:  agentDir,
		Logger:    app.logger,
		EmitEvent: recorder.emit,
		AppCtx:    context.Background,
	})
	t.Cleanup(func() { app.Lifecycle().Cleanup() })

	layout, err := embeddedllm.NewLayout(config.RuntimesDir(agentDir), config.EmbeddedModelDir(agentDir))
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	return &embeddedFixture{app: app, recorder: recorder, agentDir: agentDir, layout: layout}
}

// writeInstalledTree writes a kilobyte stand-in for a real installation: the
// runtime tree with a llama-server that leaves a marker if it is ever executed,
// the weights and manifest.json bound to the given port.
func (fx *embeddedFixture) writeInstalledTree(t *testing.T, port, contextSize int) embeddedllm.Manifest {
	t.Helper()

	runtimeDir, err := fx.layout.RuntimeDir(embeddedllm.BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	binaryDir := filepath.Join(runtimeDir, "build", "bin")
	if err := os.MkdirAll(binaryDir, 0o750); err != nil {
		t.Fatalf("creating the runtime tree: %v", err)
	}
	// The host binary name, exactly as ServerBinaryPath looks for it.
	name := embeddedllm.ServerBinaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	marker := filepath.Join(fx.agentDir, "server-executed.marker")
	script := "#!/bin/sh\necho executed > " + marker + "\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(binaryDir, name), []byte(script), 0o750); err != nil {
		t.Fatalf("writing the server binary: %v", err)
	}

	if err := os.MkdirAll(fx.layout.ModelRoot, 0o750); err != nil {
		t.Fatalf("creating the model root: %v", err)
	}
	modelFile := filepath.Join(fx.layout.ModelRoot, "Ternary-Bonsai-2-27B-PQ2_0.gguf")
	if err := os.WriteFile(modelFile, []byte("gguf-stub"), 0o640); err != nil {
		t.Fatalf("writing the model file: %v", err)
	}

	manifest := embeddedllm.Manifest{
		Packing:        embeddedllm.PackingPQ2_0,
		Backend:        embeddedllm.BackendMetal,
		RuntimeVersion: embeddedllm.RuntimeTag,
		Checksums:      map[string]string{string(embeddedllm.ComponentModel): "stub"},
		Port:           port,
		ContextSize:    contextSize,
		ModelFile:      modelFile,
		InstalledAt:    "2026-09-24T10:15:00Z",
	}
	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshalling the manifest: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	return manifest
}

// stateEvents returns the embedded_llm:state payloads the recorder saw.
func (fx *embeddedFixture) stateEvents(t *testing.T) []backend.EmbeddedLLMStateData {
	t.Helper()
	var out []backend.EmbeddedLLMStateData
	for _, ev := range flattenEmittedBatches(t, fx.recorder.snapshot()) {
		if ev.Name != backend.EventEmbeddedLLMState {
			continue
		}
		if len(ev.Data) == 0 {
			t.Fatalf("the %s event carries no payload", ev.Name)
		}
		payload, ok := ev.Data[0].(backend.EmbeddedLLMStateData)
		if !ok {
			t.Fatalf("the %s payload has type %T, want backend.EmbeddedLLMStateData", ev.Name, ev.Data[0])
		}
		out = append(out, payload)
	}
	return out
}

// listenOnLoopback opens a listener that records any connection. It stands in
// for the model's own port: a startup that probed the server would be caught
// here, which is what makes "no network" an observation rather than an
// inference.
func listenOnLoopback(t *testing.T) (port int, contacted func() bool) {
	t.Helper()
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	seen := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		mu.Lock()
		seen = true
		mu.Unlock()
		_ = conn.Close()
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the listener address %v is not a TCP address", ln.Addr())
	}
	return addr.Port, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// TestInitEmbeddedLLM_RestoresTheManifestWithoutNetworkOrLoad is the startup
// contract: the phase reads manifest.json, reports the install as present but
// NOT resident, and touches nothing else.
func TestInitEmbeddedLLM_RestoresTheManifestWithoutNetworkOrLoad(t *testing.T) {
	fx := newEmbeddedFixture(t)
	port, contacted := listenOnLoopback(t)
	want := fx.writeInstalledTree(t, port, 32768)

	fx.app.initEmbeddedLLM(fx.app.logger)

	states := fx.stateEvents(t)
	if len(states) != 1 {
		t.Fatalf("embedded_llm:state emitted %d time(s), want exactly 1 startup snapshot: %+v",
			len(states), states)
	}
	got := states[0]
	if !got.Installed {
		t.Error("installed = false, want the manifest to be restored")
	}
	if got.Loading || got.Loaded {
		t.Errorf("state = %+v, want the model NOT resident after startup", got)
	}
	if got.Port != port || got.ContextSize != 32768 {
		t.Errorf("state port/context = %d/%d, want %d/32768", got.Port, got.ContextSize, port)
	}
	if got.Packing != string(want.Packing) || got.Backend != string(want.Backend) {
		t.Errorf("state packing/backend = %q/%q, want %q/%q",
			got.Packing, got.Backend, want.Packing, want.Backend)
	}
	if got.AutoUnloadMinutes != config.EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("auto_unload_minutes = %d, want the default", got.AutoUnloadMinutes)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty", got.Error)
	}

	// The status RPC agrees: installed, not resident, no process.
	status := fx.app.GetEmbeddedLLMStatus()
	if !status.Available || !status.Installed {
		t.Fatalf("status = %+v, want an available installation", status)
	}
	if status.Loaded || status.Loading || status.Installing || status.Pid != 0 {
		t.Errorf("status = %+v, want nothing running", status)
	}
	if status.State != "installed" {
		t.Errorf("status.State = %q, want %q", status.State, "installed")
	}
	if status.ModelID != config.EmbeddedLLMProviderName+"/"+config.EmbeddedLLMModelName {
		t.Errorf("status.ModelID = %q", status.ModelID)
	}

	// No network: the port the manifest records was never contacted, so no
	// readiness probe and no download happened.
	time.Sleep(50 * time.Millisecond)
	if contacted() {
		t.Error("startup contacted the loopback port from the manifest — it must not probe or load")
	}
	// No spawn: the runtime binary was never executed.
	if _, err := os.Stat(filepath.Join(fx.agentDir, "server-executed.marker")); !os.IsNotExist(err) {
		t.Errorf("the runtime binary was executed during startup (stat err = %v)", err)
	}
	// No download: nothing was fetched or staged.
	downloads, err := fx.layout.DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if _, err := os.Stat(downloads); !os.IsNotExist(err) {
		t.Errorf("the downloads directory %q exists (stat err = %v)", downloads, err)
	}
	err = filepath.WalkDir(fx.agentDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), embeddedllm.PartialSuffix) {
			t.Errorf("a partial download exists: %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the agent dir: %v", err)
	}

	// Stopping a model that was never loaded is a no-op, not an error.
	if err := fx.app.Lifecycle().StopEmbeddedLLM(context.Background()); err != nil {
		t.Errorf("StopEmbeddedLLM after a load-free startup: %v", err)
	}
}

// TestInitEmbeddedLLM_WithoutAnInstallReportsNotInstalled covers the fresh
// machine: no manifest, so the startup snapshot says "not installed" and no
// tree is created.
func TestInitEmbeddedLLM_WithoutAnInstallReportsNotInstalled(t *testing.T) {
	fx := newEmbeddedFixture(t)

	fx.app.initEmbeddedLLM(fx.app.logger)

	states := fx.stateEvents(t)
	if len(states) != 1 {
		t.Fatalf("embedded_llm:state emitted %d time(s), want exactly 1", len(states))
	}
	if states[0].Installed || states[0].Loaded || states[0].Loading {
		t.Errorf("state = %+v, want nothing installed", states[0])
	}
	if status := fx.app.GetEmbeddedLLMStatus(); status.Installed || !status.Available {
		t.Errorf("status = %+v, want available and not installed", status)
	}
	if _, err := os.Stat(config.RuntimesDir(fx.agentDir)); !os.IsNotExist(err) {
		t.Errorf("startup created the runtimes root (stat err = %v)", err)
	}
}

// TestInitEmbeddedLLM_IsIdempotent pins that a second call (a restart of the
// phase, or an early RPC that built the subsystem first) neither double-emits
// nor changes the restored state.
func TestInitEmbeddedLLM_IsIdempotent(t *testing.T) {
	fx := newEmbeddedFixture(t)
	port, _ := listenOnLoopback(t)
	fx.writeInstalledTree(t, port, 32768)

	// An early status RPC builds and restores the subsystem before the phase
	// runs — the startup path must still work and still emit its snapshot.
	if status := fx.app.GetEmbeddedLLMStatus(); !status.Installed {
		t.Fatalf("an early status read did not restore the install: %+v", status)
	}
	fx.app.initEmbeddedLLM(fx.app.logger)
	fx.app.initEmbeddedLLM(fx.app.logger)

	states := fx.stateEvents(t)
	if len(states) != 2 {
		t.Fatalf("embedded_llm:state emitted %d time(s), want one per initEmbeddedLLM call", len(states))
	}
	for i, state := range states {
		if !state.Installed || state.Loaded {
			t.Errorf("snapshot %d = %+v, want installed and not resident", i, state)
		}
	}
}

// TestStopEmbeddedLLM_IsSafeWithoutASupervisor covers the two degenerate shapes
// Shutdown can meet: no FrontendAPI at all (an early startup exit) and one whose
// embedded subsystem was never constructed.
func TestStopEmbeddedLLM_IsSafeWithoutASupervisor(t *testing.T) {
	bare := &App{logger: testLoggerForPhases()}
	bare.FrontendAPI = nil
	bare.stopEmbeddedLLM(context.Background()) // must not panic

	empty := &App{logger: testLoggerForPhases(), FrontendAPI: &backend.FrontendAPI{}}
	empty.stopEmbeddedLLM(context.Background())

	// A nil context is tolerated too: Shutdown may be handed a torn-down one.
	empty.stopEmbeddedLLM(nil) //nolint:staticcheck // deliberately exercising the nil-context guard
}

// TestShutdown_StopsTheEmbeddedLLMServer pins the teardown wiring: Shutdown
// asks the backend to stop the supervised server, hands it its own context, and
// a failure to stop is logged rather than fatal (quitting must not be blocked
// by a model that refuses to die). The stop itself — terminating a live
// llama-server and returning to the unloaded state — is covered end-to-end by
// backend's TestLoadEmbeddedLLMStartsTheServerAndStopStopsIt.
func TestShutdown_StopsTheEmbeddedLLMServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"clean stop", nil},
		{"a stop that failed still shuts down", errors.New("the server did not exit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newEmbeddedFixture(t)
			port, _ := listenOnLoopback(t)
			fx.writeInstalledTree(t, port, 32768)
			fx.app.initEmbeddedLLM(fx.app.logger)

			var calls int
			var gotCtx context.Context
			fx.app.embeddedLLMStopFn = func(ctx context.Context) error {
				calls++
				gotCtx = ctx
				return tc.err
			}
			// The real notification cleanup calls into the Wails runtime, which
			// fatals without a live one.
			fx.app.notificationsCleanupFn = func(context.Context) {}

			shutdownCtx := context.WithValue(context.Background(), embeddedShutdownKey{}, "shutdown")
			fx.app.Shutdown(shutdownCtx)

			if calls != 1 {
				t.Fatalf("the embedded LLM stop was called %d time(s), want exactly 1", calls)
			}
			if gotCtx != shutdownCtx {
				t.Error("Shutdown did not hand its own context to the embedded LLM stop")
			}
		})
	}
}

// embeddedShutdownKey is the context key the shutdown test uses to recognise
// its own context.
type embeddedShutdownKey struct{}
