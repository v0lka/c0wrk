package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// Tests of the embedded local-model RPC surface, its two global events and the
// config sink (backend/frontend_api_embedded.go).
//
// Scope: this file tests the BACKEND half — the gates, the background install
// run, the event payloads, the config persistence and the supervisor wiring.
// The install/load/stop mechanics themselves belong to core/embeddedllm and are
// covered there; nothing here downloads a pinned artifact, because a fake
// download can never satisfy a pinned SHA256 (fail-closed by design). The
// install run is therefore driven through the installFn seam, which still
// exercises everything this layer owns: the RPC → background hand-off, the
// per-component progress adapter, the ConfigSink, the router rebuild, the
// supervisor state and the emitted events.

// ── doubles and helpers ────────────────────────────────────────────────────

// recordedEvent is one emitted global event.
type recordedEvent struct {
	name    string
	payload any
}

// embeddedEventRecorder captures every event the FrontendAPI emits. It is
// mutex-guarded because emitConfigUpdated dispatches on its own goroutine and
// the background install run emits from another one.
type embeddedEventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (r *embeddedEventRecorder) emit(name string, data ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var payload any
	if len(data) > 0 {
		payload = data[0]
	}
	r.events = append(r.events, recordedEvent{name: name, payload: payload})
}

func (r *embeddedEventRecorder) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)
	return out
}

// of returns the payloads of every event with the given name.
func (r *embeddedEventRecorder) of(name string) []any {
	var out []any
	for _, ev := range r.snapshot() {
		if ev.name == name {
			out = append(out, ev.payload)
		}
	}
	return out
}

// count returns how many events with the given name were emitted.
func (r *embeddedEventRecorder) count(name string) int {
	return len(r.of(name))
}

// waitForState polls until the recorder has seen n events of the given name, or
// fails the test after the deadline. Event delivery is synchronous, but the
// operations that emit them run on background goroutines.
func (r *embeddedEventRecorder) waitForCount(t *testing.T, name string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.count(name) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d %q event(s), got %d", n, name, r.count(name))
}

// runtimeErrors returns the runtime_error payloads (map[string]string).
func (r *embeddedEventRecorder) runtimeErrors() []map[string]string {
	var out []map[string]string
	for _, payload := range r.of(EventRuntimeError) {
		if m, ok := payload.(map[string]string); ok {
			out = append(out, m)
		}
	}
	return out
}

// newEmbeddedTestAPI builds a FrontendAPI over a temp agent dir with a
// persisted config, an event recorder and a mock builder. The subsystem seams
// are hostile by default: any hardware probe, any process spawn and any HTTP
// request fails the test, so "startup performs no network I/O and no load" is
// asserted by construction rather than by inference. Tests that need one of
// them replace the field explicitly.
func newEmbeddedTestAPI(t *testing.T) (*FrontendAPI, *embeddedEventRecorder, *mockBuilder) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "claude-3-opus"
	cfg.LLM.Anthropic.APIKey = "sk-test-embedded"
	cfg.LLM.Anthropic.Models = []string{"claude-3-opus"}
	if err := config.Save(cfg, cfgPath); err != nil {
		t.Fatalf("saving the test config: %v", err)
	}

	rec := &embeddedEventRecorder{}
	mock := &mockBuilder{}
	f := &FrontendAPI{
		config:          cfg,
		configPath:      cfgPath,
		agentDir:        dir,
		builderOverride: mock,
		emitEvent:       rec.emit,
		appCtx:          context.Background,
		logger:          slog.New(slog.DiscardHandler),
	}
	t.Cleanup(func() {
		_ = f.stopEmbeddedLLM(context.Background())
	})
	return f, rec, mock
}

// forbidEmbeddedSideEffects arms the three seams that would prove an unwanted
// probe, spawn or HTTP request. Called by the startup tests.
func forbidEmbeddedSideEffects(t *testing.T, f *FrontendAPI) {
	t.Helper()
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		t.Error("the hardware probe ran, but this path must not probe")
		return embeddedllm.Hardware{}, errors.New("probe forbidden")
	}
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("a llama-server process was spawned, but this path must not load the model")
		return nil, errors.New("spawn forbidden")
	}
	f.embedded.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("an HTTP request was made, but this path must not touch the network")
			return nil, errors.New("network forbidden")
		}),
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

// embeddedTestManifest is the install record the tree helpers write.
func embeddedTestManifest(port, contextSize int) embeddedllm.Manifest {
	return embeddedllm.Manifest{
		Packing:        embeddedllm.PackingPQ2_0,
		Backend:        embeddedllm.BackendMetal,
		RuntimeVersion: embeddedllm.RuntimeTag,
		Checksums:      map[string]string{string(embeddedllm.ComponentModel): "stub"},
		Port:           port,
		ContextSize:    contextSize,
		InstalledAt:    "2026-09-24T10:15:00Z",
	}
}

// writeEmbeddedInstallTree writes a kilobyte stand-in for a real installation:
// the runtime tree with llama-server nested the way the pinned archives nest
// it, the weights and manifest.json. It returns the layout and the manifest
// (with ModelFile filled in).
func writeEmbeddedInstallTree(t *testing.T, agentDir string, manifest embeddedllm.Manifest) (embeddedllm.Layout, embeddedllm.Manifest) {
	t.Helper()

	layout, err := embeddedllm.NewLayout(config.RuntimesDir(agentDir), config.EmbeddedModelDir(agentDir))
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	runtimeDir, err := layout.RuntimeDir(manifest.Backend)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	// The host binary name, exactly as ServerBinaryPath looks for it (the
	// supervisor's HostOS seam is left empty, so it reads runtime.GOOS).
	binaryName := embeddedllm.ServerBinaryName
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryDir := filepath.Join(runtimeDir, "build", "bin")
	if err := os.MkdirAll(binaryDir, 0o750); err != nil {
		t.Fatalf("creating the runtime tree: %v", err)
	}
	// A marker-writing stand-in: if anything ever executed it, the marker would
	// appear. Nothing in this package executes it (the spawn is faked), which
	// is exactly what the startup tests assert.
	binary := filepath.Join(binaryDir, binaryName)
	script := "#!/bin/sh\necho executed > " + filepath.Join(agentDir, "server-executed.marker") + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o750); err != nil {
		t.Fatalf("writing the server binary: %v", err)
	}

	if err := os.MkdirAll(layout.ModelRoot, 0o750); err != nil {
		t.Fatalf("creating the model root: %v", err)
	}
	modelFile := filepath.Join(layout.ModelRoot, "Ternary-Bonsai-2-27B-PQ2_0.gguf")
	if err := os.WriteFile(modelFile, []byte("gguf-stub"), 0o640); err != nil {
		t.Fatalf("writing the model file: %v", err)
	}
	manifest.ModelFile = modelFile

	// A flat embedding-model neighbour that must survive every operation of
	// this subsystem (it shares <agentDir>/models).
	if err := os.WriteFile(filepath.Join(config.ModelsDir(agentDir), "ggml-model-q4_0.gguf"),
		[]byte("embedding model"), 0o640); err != nil {
		t.Fatalf("writing the flat embedding model: %v", err)
	}

	writeEmbeddedManifest(t, layout, manifest)
	return layout, manifest
}

// writeEmbeddedManifest writes manifest.json the way the installer does.
func writeEmbeddedManifest(t *testing.T, layout embeddedllm.Layout, manifest embeddedllm.Manifest) {
	t.Helper()
	path, err := layout.ManifestPath()
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
}

// supervisorOf returns the constructed supervisor, building the subsystem if
// needed. Test-only accessor.
func supervisorOf(t *testing.T, f *FrontendAPI) *embeddedllm.Server {
	t.Helper()
	server, _, err := f.embeddedBuild()
	if err != nil {
		t.Fatalf("embeddedBuild: %v", err)
	}
	return server
}

// tightenEmbeddedBudgets shrinks the supervision budgets so a load/stop test
// never waits on the production-scale timeouts.
func tightenEmbeddedBudgets(t *testing.T, f *FrontendAPI) *embeddedllm.Server {
	t.Helper()
	server := supervisorOf(t, f)
	server.ReadyTimeout = 10 * time.Second
	server.ReadyPollInterval = 5 * time.Millisecond
	server.ProbeTimeout = 2 * time.Second
	server.StopTimeout = 500 * time.Millisecond
	return server
}

// stubFreePortProbe tells the pre-spawn port scan that every loopback port is
// bindable, so a load keeps the port its manifest records.
//
// A test that FAKES the spawn has nothing binding that port except its own
// /v1/models stand-in, so the production prober would report it taken and walk
// the load onto a port no listener answers. The scan is right and the fixture is
// what differs from production, where the spawned server owns the socket. Tests
// that exercise a collision install their own prober instead (see
// frontend_api_embedded_port_test.go).
func stubFreePortProbe(t *testing.T, f *FrontendAPI) {
	t.Helper()
	f.embedded.portProbeFn = func(context.Context, int) bool { return true }
}

// fakeEmbeddedProcess is an embeddedllm.Process double: it stays alive until
// signalled or killed and records what it received.
type fakeEmbeddedProcess struct {
	mu      sync.Mutex
	pid     int
	signals []os.Signal
	kills   int

	exited   chan struct{}
	exitOnce sync.Once
	exitErr  error
}

func newFakeEmbeddedProcess(pid int) *fakeEmbeddedProcess {
	return &fakeEmbeddedProcess{pid: pid, exited: make(chan struct{})}
}

func (p *fakeEmbeddedProcess) Wait() error {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

func (p *fakeEmbeddedProcess) Pid() int { return p.pid }

func (p *fakeEmbeddedProcess) Signal(sig os.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	p.mu.Unlock()
	p.exit(nil)
	return nil
}

func (p *fakeEmbeddedProcess) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.exit(errors.New("signal: killed"))
	return nil
}

func (p *fakeEmbeddedProcess) Stdout() io.Reader { return strings.NewReader("") }
func (p *fakeEmbeddedProcess) Stderr() io.Reader { return strings.NewReader("") }

func (p *fakeEmbeddedProcess) exit(err error) {
	p.exitOnce.Do(func() {
		p.mu.Lock()
		p.exitErr = err
		p.mu.Unlock()
		close(p.exited)
	})
}

func (p *fakeEmbeddedProcess) signalCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.signals) + p.kills
}

// modelsEndpoint serves the readiness endpoint the supervisor polls: a 200 with
// a non-empty OpenAI model list.
func modelsEndpoint(t *testing.T) (srv *httptest.Server, port int) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"Bonsai 2 27B","object":"model"}]}`)
	}))
	t.Cleanup(srv.Close)
	// The URL is http://127.0.0.1:<port>; the supervisor derives the same
	// address from the persisted port, so the manifest records this one.
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL %q: %v", srv.URL, err)
	}
	if parsed.Hostname() != embeddedllm.LoopbackHost {
		t.Fatalf("the test server is bound to %q, want the loopback", parsed.Hostname())
	}
	port, err = strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parsing the test server port from %q: %v", srv.URL, err)
	}
	return srv, port
}

// statePayload asserts the type of an embedded_llm:state payload.
func statePayload(t *testing.T, payload any) EmbeddedLLMStateData {
	t.Helper()
	data, ok := payload.(EmbeddedLLMStateData)
	if !ok {
		t.Fatalf("state payload type %T, want EmbeddedLLMStateData", payload)
	}
	return data
}

// waitForStatus polls a status predicate.
func waitForStatus(t *testing.T, f *FrontendAPI, what string, want func(EmbeddedLLMStatus) bool) EmbeddedLLMStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last EmbeddedLLMStatus
	for time.Now().Before(deadline) {
		last = f.GetEmbeddedLLMStatus()
		if want(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last status %+v", what, last)
	return last
}

// ── signature convention ───────────────────────────────────────────────────

// TestEmbeddedLLMRPCSignaturesFollowTheBoundaryConvention pins the RPC shapes
// the desktop-frontend contract requires: the read-only getter returns no
// error, every mutating method returns exactly one error, and the auto-unload
// setter takes the two documented arguments. A drift here silently breaks the
// generated Wails bindings and every frontend caller.
func TestEmbeddedLLMRPCSignaturesFollowTheBoundaryConvention(t *testing.T) {
	api := reflect.TypeOf(&FrontendAPI{})
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	t.Run("getter returns no error", func(t *testing.T) {
		m, ok := api.MethodByName("GetEmbeddedLLMStatus")
		if !ok {
			t.Fatal("GetEmbeddedLLMStatus is not exported on *FrontendAPI")
		}
		// Method values on a type carry the receiver as the first input.
		if m.Type.NumIn() != 1 {
			t.Errorf("GetEmbeddedLLMStatus takes %d argument(s), want 0", m.Type.NumIn()-1)
		}
		if m.Type.NumOut() != 1 {
			t.Fatalf("GetEmbeddedLLMStatus returns %d value(s), want exactly 1 (no error)", m.Type.NumOut())
		}
		if got := m.Type.Out(0); got != reflect.TypeOf(EmbeddedLLMStatus{}) {
			t.Errorf("GetEmbeddedLLMStatus returns %s, want EmbeddedLLMStatus", got)
		}
	})

	for _, name := range []string{
		"InstallEmbeddedLLM", "RemoveEmbeddedLLM", "LoadEmbeddedLLM", "UnloadEmbeddedLLM",
	} {
		t.Run(name+" returns only an error", func(t *testing.T) {
			m, ok := api.MethodByName(name)
			if !ok {
				t.Fatalf("%s is not exported on *FrontendAPI", name)
			}
			if m.Type.NumIn() != 1 {
				t.Errorf("%s takes %d argument(s), want 0", name, m.Type.NumIn()-1)
			}
			if m.Type.NumOut() != 1 || m.Type.Out(0) != errorType {
				t.Errorf("%s returns %d value(s) (%s), want exactly error",
					name, m.Type.NumOut(), m.Type)
			}
		})
	}

	t.Run("auto-unload setter", func(t *testing.T) {
		m, ok := api.MethodByName("SetEmbeddedLLMAutoUnload")
		if !ok {
			t.Fatal("SetEmbeddedLLMAutoUnload is not exported on *FrontendAPI")
		}
		wantIn := []reflect.Type{reflect.TypeOf(&FrontendAPI{}), reflect.TypeOf(false), reflect.TypeOf(0)}
		if m.Type.NumIn() != len(wantIn) {
			t.Fatalf("SetEmbeddedLLMAutoUnload takes %d argument(s), want (enabled bool, minutes int)",
				m.Type.NumIn()-1)
		}
		for i, want := range wantIn {
			if got := m.Type.In(i); got != want {
				t.Errorf("SetEmbeddedLLMAutoUnload argument %d is %s, want %s", i, got, want)
			}
		}
		if m.Type.NumOut() != 1 || m.Type.Out(0) != errorType {
			t.Errorf("SetEmbeddedLLMAutoUnload returns %s, want exactly error", m.Type)
		}
	})

	t.Run("event names are global bare names", func(t *testing.T) {
		for _, name := range []string{EventEmbeddedLLMInstallProgress, EventEmbeddedLLMState} {
			if strings.Contains(name, "session:") || strings.HasPrefix(name, "session") {
				t.Errorf("event %q must be a global bare name, not session-scoped", name)
			}
		}
		if EventEmbeddedLLMInstallProgress != "embedded_llm:install_progress" {
			t.Errorf("install progress event = %q", EventEmbeddedLLMInstallProgress)
		}
		if EventEmbeddedLLMState != "embedded_llm:state" {
			t.Errorf("state event = %q", EventEmbeddedLLMState)
		}
	})
}

// ── startup restore ────────────────────────────────────────────────────────

// TestInitEmbeddedLLMRestoresTheManifestWithoutNetworkOrLoad is the startup
// invariant: the phase restores the state from manifest.json and emits ONE
// snapshot. It performs no hardware probe (no external command), no HTTP
// request, no download and no spawn — the model stays unloaded until something
// explicitly asks for it.
func TestInitEmbeddedLLMRestoresTheManifestWithoutNetworkOrLoad(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	forbidEmbeddedSideEffects(t, f)
	layout, want := writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))

	f.Lifecycle().InitEmbeddedLLM()

	// Exactly one snapshot event, carrying the restored install record.
	payloads := rec.of(EventEmbeddedLLMState)
	if len(payloads) != 1 {
		t.Fatalf("embedded_llm:state emitted %d time(s), want exactly 1 (the restore must not double-emit)",
			len(payloads))
	}
	state := statePayload(t, payloads[0])
	if !state.Installed || state.Loading || state.Loaded {
		t.Errorf("state = %+v, want installed and NOT resident", state)
	}
	if state.Packing != string(want.Packing) || state.Backend != string(want.Backend) {
		t.Errorf("state packing/backend = %q/%q, want %q/%q",
			state.Packing, state.Backend, want.Packing, want.Backend)
	}
	if state.Port != want.Port || state.ContextSize != want.ContextSize {
		t.Errorf("state port/context = %d/%d, want %d/%d",
			state.Port, state.ContextSize, want.Port, want.ContextSize)
	}
	if state.AutoUnloadMinutes != config.EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("auto_unload_minutes = %d, want the default %d",
			state.AutoUnloadMinutes, config.EmbeddedLLMDefaultAutoUnloadMinutes)
	}
	if state.Error != "" {
		t.Errorf("state error = %q, want empty", state.Error)
	}

	// The status getter agrees, and reports the derived provider identity.
	status := f.GetEmbeddedLLMStatus()
	if !status.Available || !status.Installed || status.Loading || status.Loaded || status.Installing {
		t.Errorf("status = %+v, want available+installed and nothing running", status)
	}
	if status.State != string(embeddedllm.StateInstalled) {
		t.Errorf("status.State = %q, want %q", status.State, embeddedllm.StateInstalled)
	}
	if status.Pid != 0 {
		t.Errorf("status.Pid = %d, want 0 (no process)", status.Pid)
	}
	if status.ModelID != embeddedCompositeID() {
		t.Errorf("status.ModelID = %q, want %q", status.ModelID, embeddedCompositeID())
	}
	if wantURL := "http://127.0.0.1:4321/v1"; status.BaseURL != wantURL {
		t.Errorf("status.BaseURL = %q, want %q", status.BaseURL, wantURL)
	}

	// Nothing was fetched and nothing was executed.
	downloads, err := layout.DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if _, err := os.Stat(downloads); !os.IsNotExist(err) {
		t.Errorf("the downloads directory %q exists (stat err = %v): startup must not download", downloads, err)
	}
	if _, err := os.Stat(filepath.Join(f.agentDir, "server-executed.marker")); !os.IsNotExist(err) {
		t.Errorf("the runtime binary was executed during startup (stat err = %v)", err)
	}
	assertNoPartialDownloads(t, f.agentDir)
}

// TestInitEmbeddedLLMWithoutAnInstallReportsNotInstalled covers the fresh
// machine: no manifest, so the snapshot says "not installed" and the derived
// provider identity stays empty.
func TestInitEmbeddedLLMWithoutAnInstallReportsNotInstalled(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	forbidEmbeddedSideEffects(t, f)

	f.Lifecycle().InitEmbeddedLLM()

	payloads := rec.of(EventEmbeddedLLMState)
	if len(payloads) != 1 {
		t.Fatalf("embedded_llm:state emitted %d time(s), want exactly 1", len(payloads))
	}
	state := statePayload(t, payloads[0])
	if state.Installed || state.Loading || state.Loaded {
		t.Errorf("state = %+v, want nothing installed", state)
	}
	if state.Port != 0 || state.ContextSize != 0 {
		t.Errorf("state port/context = %d/%d, want 0/0", state.Port, state.ContextSize)
	}

	status := f.GetEmbeddedLLMStatus()
	if !status.Available || status.Installed {
		t.Errorf("status = %+v, want available and not installed", status)
	}
	if status.ModelID != "" || status.BaseURL != "" {
		t.Errorf("status identity = %q/%q, want empty when nothing is installed",
			status.ModelID, status.BaseURL)
	}
	assertNoPartialDownloads(t, f.agentDir)
}

// TestGetEmbeddedLLMStatusWithoutAnAgentDirIsNotAPanic covers the pre-startup
// zero-value FrontendAPI (desktop.NewApp constructs one so early RPC calls
// return errors instead of panicking): the getter degrades to "unavailable".
func TestGetEmbeddedLLMStatusWithoutAnAgentDirIsNotAPanic(t *testing.T) {
	f := &FrontendAPI{logger: slog.New(slog.DiscardHandler)}

	status := f.GetEmbeddedLLMStatus()
	if status.Available {
		t.Error("Available = true without an agent directory")
	}
	if status.State != string(embeddedllm.StateNotInstalled) {
		t.Errorf("State = %q, want %q", status.State, embeddedllm.StateNotInstalled)
	}
	if status.AutoUnloadMinutes != config.EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("AutoUnloadMinutes = %d, want the documented default", status.AutoUnloadMinutes)
	}
	// The lifecycle hooks are safe on the zero value too.
	f.Lifecycle().InitEmbeddedLLM()
	if err := f.Lifecycle().StopEmbeddedLLM(context.Background()); err != nil {
		t.Errorf("StopEmbeddedLLM on an unbuilt subsystem: %v", err)
	}
}

// assertNoPartialDownloads fails when any transfer marker exists below the
// agent dir: proof that nothing started a download.
func assertNoPartialDownloads(t *testing.T, agentDir string) {
	t.Helper()
	err := filepath.WalkDir(agentDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), embeddedllm.PartialSuffix) {
			t.Errorf("a partial download exists: %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the agent dir: %v", err)
	}
}

// ── install gates ──────────────────────────────────────────────────────────

// TestInstallEmbeddedLLMRefusesBelowTheRAMGateWithoutDownloading is D6 as an
// RPC contract: a machine under 16 GiB gets an actionable refusal from the call
// itself — not from a toast after a download — and not one byte is fetched.
func TestInstallEmbeddedLLMRefusesBelowTheRAMGateWithoutDownloading(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedllm.Hardware{
			Platform: "darwin-arm64",
			Arch:     "arm64",
			RAMGiB:   8,
			Backend:  embeddedllm.BackendMetal,
		}, nil
	}
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("a process was spawned by a refused install")
		return nil, errors.New("spawn forbidden")
	}
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		t.Error("the install run started on a machine below the RAM gate")
		return nil, errors.New("install forbidden")
	}
	// Belt and braces: even if a run did start, its downloader must never reach
	// a socket.
	supervisorOf(t, f)
	f.embedded.installer.Downloader = &embeddedllm.Downloader{
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("the downloader performed an HTTP request after the RAM refusal")
			return nil, errors.New("network forbidden")
		})},
	}

	err := f.InstallEmbeddedLLM()
	if err == nil {
		t.Fatal("InstallEmbeddedLLM succeeded on an 8 GiB machine")
	}
	if !errors.Is(err, embeddedllm.ErrInsufficientRAM) {
		t.Errorf("error = %v, want it to wrap ErrInsufficientRAM", err)
	}
	for _, want := range []string{"16 GiB", "8.0 GiB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q is not actionable: it does not mention %q", err, want)
		}
	}

	// The refusal is reported by the rejected promise alone: no toast on top.
	if got := rec.runtimeErrors(); len(got) != 0 {
		t.Errorf("a synchronous refusal raised %d runtime_error toast(s): %+v", len(got), got)
	}
	// The install slot is released, the cause is visible in the status.
	status := f.GetEmbeddedLLMStatus()
	if status.Installing {
		t.Error("Installing = true after a refusal; the slot was not released")
	}
	if !strings.Contains(status.Error, "16 GiB") {
		t.Errorf("status.Error = %q, want the refusal cause", status.Error)
	}
	if status.Installed {
		t.Error("Installed = true after a refused install")
	}
	// Nothing was created: no runtime tree, no downloads, no weights.
	if _, err := os.Stat(config.RuntimesDir(f.agentDir)); !os.IsNotExist(err) {
		t.Errorf("the runtimes root exists after a refused install (stat err = %v)", err)
	}
	assertNoPartialDownloads(t, f.agentDir)
	if f.config.EmbeddedLLM.Installed {
		t.Error("config claims an install that was refused")
	}
}

// TestInstallEmbeddedLLMRefusesAnUnreadableHardwareProbe covers the other
// synchronous gate: RAM is a hard input, so a probe that cannot read it refuses
// the install instead of assuming the machine is big enough.
func TestInstallEmbeddedLLMRefusesAnUnreadableHardwareProbe(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedllm.Hardware{}, embeddedllm.ErrRAMUnknown
	}
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		t.Error("the install run started without a hardware probe result")
		return nil, errors.New("install forbidden")
	}

	err := f.InstallEmbeddedLLM()
	if !errors.Is(err, embeddedllm.ErrRAMUnknown) {
		t.Fatalf("error = %v, want it to wrap ErrRAMUnknown", err)
	}
	if f.embeddedInstalling() {
		t.Error("the install slot stayed claimed after a refused install")
	}
}

// ── background install run ─────────────────────────────────────────────────

// TestInstallEmbeddedLLMRunsInBackgroundAndEmitsProgressPerComponent is the
// whole backend chain of an install: the RPC returns while the run is still in
// flight, every component reports its own progress event with byte counts, the
// sink persists the state and regenerates the provider, the router is rebuilt,
// the supervisor reports installed — and the model is NOT loaded by the install.
func TestInstallEmbeddedLLMRunsInBackgroundAndEmitsProgressPerComponent(t *testing.T) {
	f, rec, mock := newEmbeddedTestAPI(t)
	hw := embeddedllm.Hardware{
		Platform: "darwin-arm64",
		Arch:     "arm64",
		RAMGiB:   64,
		Backend:  embeddedllm.BackendMetal,
	}
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return hw, nil
	}
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("the install spawned the server; installing must not load the model")
		return nil, errors.New("spawn forbidden")
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var gotOpts embeddedllm.InstallOptions
	var gotProbe embeddedllm.Hardware
	f.embedded.installFn = func(ctx context.Context, in *embeddedllm.Installer, opts embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		gotOpts = opts
		// The run pins the probe result the RPC already paid for: calling the
		// installer's Probe must return it without probing again.
		probed, probeErr := in.Probe(ctx, nil)
		if probeErr != nil {
			t.Errorf("the pinned probe returned an error: %v", probeErr)
		}
		gotProbe = probed
		close(started)
		<-release

		if err := in.Sink.ApplyInstalled(ctx, embeddedllm.InstallState{
			Packing:           embeddedllm.PackingPQ2_0,
			Backend:           embeddedllm.BackendMetal,
			Port:              4321,
			ModelFile:         filepath.Join(config.EmbeddedModelDir(f.agentDir), "Ternary-Bonsai-2-27B-PQ2_0.gguf"),
			RuntimeVersion:    embeddedllm.RuntimeTag,
			InstalledAt:       "2026-09-24T10:15:00Z",
			ContextSize:       32768,
			AutoUnloadEnabled: embeddedllm.DefaultAutoUnloadEnabled,
			AutoUnloadMinutes: embeddedllm.DefaultAutoUnloadMinutes,
		}); err != nil {
			return nil, err
		}
		// Per-component progress, the way the real installer emits it: each
		// artifact walks its own stages with its own byte counts.
		for _, p := range []embeddedllm.Progress{
			{Component: embeddedllm.ComponentRuntime, Stage: embeddedllm.StageDownloading, BytesDone: 512, BytesTotal: 4096},
			{Component: embeddedllm.ComponentRuntime, Stage: embeddedllm.StageVerifying},
			{Component: embeddedllm.ComponentRuntime, Stage: embeddedllm.StageExtracting},
			{Component: embeddedllm.ComponentRuntime, Stage: embeddedllm.StageSigning},
			{Component: embeddedllm.ComponentRuntime, Stage: embeddedllm.StageDone, BytesDone: 4096, BytesTotal: 4096},
			{Component: embeddedllm.ComponentModel, Stage: embeddedllm.StageDownloading, BytesDone: 1 << 30, BytesTotal: 7206168928},
			{Component: embeddedllm.ComponentModel, Stage: embeddedllm.StageDone, BytesDone: 7206168928, BytesTotal: 7206168928},
			{Component: embeddedllm.ComponentMMProj, Stage: embeddedllm.StageDownloading, BytesDone: 128, BytesTotal: 1024},
			{Component: embeddedllm.ComponentMMProj, Stage: embeddedllm.StageDone, BytesDone: 1024, BytesTotal: 1024},
		} {
			opts.Progress(p)
		}
		return &embeddedllm.InstallReport{
			Manifest: embeddedTestManifest(4321, 32768),
			Hardware: hw,
		}, nil
	}

	// The RPC must not block on the run.
	returned := make(chan error, 1)
	go func() { returned <- f.InstallEmbeddedLLM() }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("InstallEmbeddedLLM: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InstallEmbeddedLLM blocked instead of starting a background run")
	}
	<-started
	if !f.GetEmbeddedLLMStatus().Installing {
		t.Error("Installing = false while the background run is in flight")
	}
	close(release)

	status := waitForStatus(t, f, "the install to complete", func(s EmbeddedLLMStatus) bool {
		return s.Installed && !s.Installing
	})

	// The probe result the RPC gathered is what the run used — one probe per
	// install, no re-probe in the background.
	if gotOpts.Platform != hw.Platform || gotOpts.Backend != hw.Backend {
		t.Errorf("install options = %+v, want the probed platform/backend", gotOpts)
	}
	if gotOpts.Progress == nil {
		t.Error("the install run got no progress callback")
	}
	if gotProbe != hw {
		t.Errorf("the run re-probed: %+v, want the pinned %+v", gotProbe, hw)
	}

	// One event per component update, each with its own byte counts.
	progress := rec.of(EventEmbeddedLLMInstallProgress)
	if len(progress) != 9 {
		t.Fatalf("install_progress emitted %d time(s), want 9 (one per component update)", len(progress))
	}
	perComponent := map[string][]EmbeddedLLMProgressData{}
	for i, payload := range progress {
		data, ok := payload.(EmbeddedLLMProgressData)
		if !ok {
			t.Fatalf("progress payload %d has type %T, want EmbeddedLLMProgressData", i, payload)
		}
		perComponent[data.Component] = append(perComponent[data.Component], data)
	}
	for _, component := range []string{"runtime", "model", "mmproj"} {
		events, ok := perComponent[component]
		if !ok || len(events) == 0 {
			t.Fatalf("no install_progress event for the %q component: %+v", component, perComponent)
		}
		first := events[0]
		if first.Stage != embeddedllm.StageDownloading {
			t.Errorf("the %q component's first stage = %q, want %q",
				component, first.Stage, embeddedllm.StageDownloading)
		}
		if first.BytesTotal <= 0 {
			t.Errorf("the %q component reported bytes_total = %d", component, first.BytesTotal)
		}
		last := events[len(events)-1]
		if last.Stage != embeddedllm.StageDone {
			t.Errorf("the %q component's last stage = %q, want %q", component, last.Stage, embeddedllm.StageDone)
		}
		if last.BytesDone != last.BytesTotal {
			t.Errorf("the %q component finished at %d/%d bytes",
				component, last.BytesDone, last.BytesTotal)
		}
	}
	if got := perComponent["model"][0].BytesTotal; got != 7206168928 {
		t.Errorf("the model component's bytes_total = %d, want the pinned artifact size", got)
	}

	// The sink persisted the install and regenerated the backend-owned provider.
	if !f.config.EmbeddedLLM.Installed || f.config.EmbeddedLLM.Port != 4321 {
		t.Errorf("config = %+v, want installed on port 4321", f.config.EmbeddedLLM)
	}
	entry, ok := f.config.LLM.OpenAICompatible[config.EmbeddedLLMProviderName]
	if !ok {
		t.Fatalf("the %q provider record was not generated", config.EmbeddedLLMProviderName)
	}
	if entry.BaseURL != "http://127.0.0.1:4321/v1" {
		t.Errorf("provider base_url = %q", entry.BaseURL)
	}
	if override, ok := f.config.LLM.Models[config.EmbeddedLLMModelName]; !ok || override.ContextWindow != 32768 {
		t.Errorf("the context-window override = %+v, want context_window 32768", f.config.LLM.Models)
	}
	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("reading the persisted config: %v", err)
	}
	for _, want := range []string{"installed: true", "port: 4321", "http://127.0.0.1:4321/v1"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the persisted config is missing %q:\n%s", want, data)
		}
	}
	if mock.rebuildRouterCalls == 0 {
		t.Error("the LLM router was not rebuilt after the install")
	}
	// config:updated is dispatched on its own goroutine (persistConfig must
	// never emit under configMu), so wait for it instead of sampling.
	rec.waitForCount(t, EventConfigUpdated, 1)

	// The supervisor reports installed — and NOT loaded: an install never
	// starts the server.
	if status.State != string(embeddedllm.StateInstalled) || status.Loaded || status.Loading {
		t.Errorf("status = %+v, want installed and not resident", status)
	}
	if status.Pid != 0 {
		t.Errorf("status.Pid = %d after an install, want 0", status.Pid)
	}
	if status.ContextSize != 32768 || status.Packing != "PQ2_0" || status.Backend != "metal" {
		t.Errorf("status install record = %+v", status)
	}
	if status.ModelID != embeddedCompositeID() {
		t.Errorf("status.ModelID = %q, want %q", status.ModelID, embeddedCompositeID())
	}
	if status.Error != "" {
		t.Errorf("status.Error = %q after a successful install, want empty", status.Error)
	}

	// The completion snapshot arrived as a state event.
	rec.waitForCount(t, EventEmbeddedLLMState, 1)
	last := rec.of(EventEmbeddedLLMState)
	final := statePayload(t, last[len(last)-1])
	if !final.Installed || final.Loaded || final.Port != 4321 || final.ContextSize != 32768 {
		t.Errorf("final state event = %+v, want installed on 4321 with a 32768 context", final)
	}
	if got := rec.runtimeErrors(); len(got) != 0 {
		t.Errorf("a successful install raised %d runtime_error toast(s): %+v", len(got), got)
	}
}

// TestInstallEmbeddedLLMRefusesASecondConcurrentRun pins the single-run gate:
// two rapid clicks must not race for the same staging tree.
func TestInstallEmbeddedLLMRefusesASecondConcurrentRun(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedllm.Hardware{Platform: "darwin-arm64", Arch: "arm64", RAMGiB: 32,
			Backend: embeddedllm.BackendMetal}, nil
	}
	release := make(chan struct{})
	runs := make(chan struct{}, 4)
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		runs <- struct{}{}
		<-release
		return nil, errors.New("test run finished")
	}

	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("the first InstallEmbeddedLLM: %v", err)
	}
	<-runs

	err := f.InstallEmbeddedLLM()
	if err == nil {
		t.Fatal("a second concurrent install was accepted")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("the refusal %q does not say an install is already running", err)
	}
	close(release)
	waitForStatus(t, f, "the first run to finish", func(s EmbeddedLLMStatus) bool { return !s.Installing })
	if len(runs) != 0 {
		t.Errorf("a second install run started (%d extra run(s))", len(runs))
	}
}

// TestInstallEmbeddedLLMBackgroundFailureRaisesAToast covers the case with no
// RPC left to carry the error: the failure must reach the user as a
// runtime_error toast AND as the state event's error field, and it must release
// the install slot so a retry is possible.
func TestInstallEmbeddedLLMBackgroundFailureRaisesAToast(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedllm.Hardware{Platform: "linux-amd64", Arch: "amd64", RAMGiB: 32,
			Backend: embeddedllm.BackendCPU}, nil
	}
	failure := errors.New("sha256 mismatch: the pinned runtime archive changed upstream")
	done := make(chan struct{})
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		defer close(done)
		return nil, failure
	}

	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("InstallEmbeddedLLM: %v", err)
	}
	<-done

	rec.waitForCount(t, EventRuntimeError, 1)
	toasts := rec.runtimeErrors()
	if len(toasts) != 1 {
		t.Fatalf("runtime_error emitted %d time(s), want 1", len(toasts))
	}
	if toasts[0]["error_code"] != embeddedErrCodeInstall {
		t.Errorf("error_code = %q, want %q", toasts[0]["error_code"], embeddedErrCodeInstall)
	}
	if toasts[0]["id"] == "" {
		t.Error("the toast carries no id")
	}
	if !strings.Contains(toasts[0]["message"], failure.Error()) {
		t.Errorf("the toast message %q does not carry the cause", toasts[0]["message"])
	}

	status := waitForStatus(t, f, "the failure to be recorded", func(s EmbeddedLLMStatus) bool {
		return !s.Installing && s.Error != ""
	})
	if status.Installed {
		t.Error("Installed = true after a failed install")
	}
	if !strings.Contains(status.Error, "sha256 mismatch") {
		t.Errorf("status.Error = %q, want the failure cause", status.Error)
	}
	// The state event carries the same cause.
	states := rec.of(EventEmbeddedLLMState)
	if len(states) == 0 {
		t.Fatal("no state event after a failed install")
	}
	last := statePayload(t, states[len(states)-1])
	if !strings.Contains(last.Error, "sha256 mismatch") || last.Installed {
		t.Errorf("final state event = %+v, want the failure cause and not installed", last)
	}
	if f.config.EmbeddedLLM.Installed {
		t.Error("config claims an install that failed")
	}

	// The slot is released, so a retry is possible.
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		return nil, errors.New("second attempt")
	}
	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Errorf("a retry after a failed install was refused: %v", err)
	}
}

// ── load / unload / stop ───────────────────────────────────────────────────

// TestLoadEmbeddedLLMStartsTheServerAndStopStopsIt drives a full resident
// cycle through the RPC surface with a faked process and a real loopback
// /v1/models responder: Load reaches loaded, the shutdown stop terminates the
// process and returns the state to installed (unloaded), and a second stop is a
// no-op. This is the backend half of "Shutdown stops a running server"; the
// desktop half (Shutdown calls it) is desktop/startup_embedded_test.go.
func TestLoadEmbeddedLLMStartsTheServerAndStopStopsIt(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	_, port := modelsEndpoint(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(port, 32768))
	// The spawn is faked, so nothing but this test's own /v1/models responder
	// holds the manifest port; the scan has to be told the port is bindable or
	// it would move the load off the endpoint that answers readiness.
	stubFreePortProbe(t, f)

	proc := newFakeEmbeddedProcess(4242)
	spawns := make(chan embeddedllm.LaunchCommand, 1)
	f.embedded.spawnFn = func(_ context.Context, cmd embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		spawns <- cmd
		return proc, nil
	}
	server := tightenEmbeddedBudgets(t, f)
	f.Lifecycle().InitEmbeddedLLM()

	if err := f.LoadEmbeddedLLM(); err != nil {
		t.Fatalf("LoadEmbeddedLLM: %v", err)
	}

	// The command line is the pinned one: loopback only, no Web UI, the
	// persisted port and a non-zero context.
	select {
	case cmd := <-spawns:
		joined := strings.Join(cmd.Args, " ")
		for _, want := range []string{"--host 127.0.0.1", "--no-webui", fmt.Sprintf("--port %d", port), "-c 32768"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the launch arguments %q are missing %q", joined, want)
			}
		}
	default:
		t.Fatal("the supervisor did not spawn the server")
	}

	status := f.GetEmbeddedLLMStatus()
	if !status.Loaded || status.State != string(embeddedllm.StateLoaded) {
		t.Fatalf("status = %+v, want loaded", status)
	}
	if status.Pid != 4242 {
		t.Errorf("status.Pid = %d, want the spawned process id", status.Pid)
	}
	if status.Loading || status.Installing {
		t.Errorf("status = %+v, want a settled loaded state", status)
	}
	// The idle budget is armed now that the model is resident, and the load
	// time was not charged to it (core's invariant, observable here as a
	// nearly full budget).
	if status.AutoUnloadMinutes != config.EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("AutoUnloadMinutes = %d", status.AutoUnloadMinutes)
	}
	if remaining, armed := server.IdleRemaining(); !armed || remaining <= 0 {
		t.Errorf("the idle timer is not armed after a load (remaining %v, armed %v)", remaining, armed)
	}
	if status.IdleRemainingSeconds <= 0 {
		t.Errorf("IdleRemainingSeconds = %d, want a positive budget", status.IdleRemainingSeconds)
	}

	// Both transitions were reported.
	rec.waitForCount(t, EventEmbeddedLLMState, 3) // restore + loading + loaded
	var sawLoading, sawLoaded bool
	for _, payload := range rec.of(EventEmbeddedLLMState) {
		state := statePayload(t, payload)
		sawLoading = sawLoading || state.Loading
		sawLoaded = sawLoaded || state.Loaded
	}
	if !sawLoading || !sawLoaded {
		t.Errorf("the state events never reported loading=%v loaded=%v", sawLoading, sawLoaded)
	}

	// Shutdown stops the process.
	if err := f.Lifecycle().StopEmbeddedLLM(context.Background()); err != nil {
		t.Fatalf("StopEmbeddedLLM: %v", err)
	}
	if proc.signalCount() == 0 {
		t.Error("the server process was never signalled or killed")
	}
	status = f.GetEmbeddedLLMStatus()
	if status.Loaded || status.Loading {
		t.Errorf("status = %+v, want the model unloaded after the stop", status)
	}
	if status.State != string(embeddedllm.StateInstalled) {
		t.Errorf("status.State = %q, want %q (the bytes stay on disk)",
			status.State, embeddedllm.StateInstalled)
	}
	if status.Pid != 0 {
		t.Errorf("status.Pid = %d after the stop, want 0", status.Pid)
	}
	if !status.Installed {
		t.Error("Installed = false after the stop; unloading is not uninstalling")
	}

	// Idempotent: stopping again is a no-op, not an error.
	if err := f.Lifecycle().StopEmbeddedLLM(context.Background()); err != nil {
		t.Errorf("a second StopEmbeddedLLM: %v", err)
	}
	if err := f.stopEmbeddedLLM(context.Background()); err != nil {
		t.Errorf("a third stop: %v", err)
	}
}

// TestUnloadEmbeddedLLMWithoutARunningServerIsNotAnError pins the idempotence
// the UI relies on: the Unload button is never an error on an unloaded model.
func TestUnloadEmbeddedLLMWithoutARunningServerIsNotAnError(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("Unload spawned the server")
		return nil, errors.New("spawn forbidden")
	}
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()

	if err := f.UnloadEmbeddedLLM(); err != nil {
		t.Fatalf("UnloadEmbeddedLLM on an unloaded model: %v", err)
	}
	status := f.GetEmbeddedLLMStatus()
	if !status.Installed || status.Loaded {
		t.Errorf("status = %+v, want installed and not resident", status)
	}
}

// TestLoadEmbeddedLLMRefusesWhenNotInstalled covers the actionable refusal: the
// caller is told to install first, and nothing is spawned.
func TestLoadEmbeddedLLMRefusesWhenNotInstalled(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.spawnFn = func(context.Context, embeddedllm.LaunchCommand) (embeddedllm.Process, error) {
		t.Error("Load spawned a server with no installation")
		return nil, errors.New("spawn forbidden")
	}
	f.Lifecycle().InitEmbeddedLLM()

	err := f.LoadEmbeddedLLM()
	if err == nil {
		t.Fatal("LoadEmbeddedLLM succeeded with nothing installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("the refusal %q does not say the model is not installed", err)
	}
}

// TestLoadEmbeddedLLMRefusesWhileAnInstallRuns prevents a load from racing a
// half-written runtime tree.
func TestLoadEmbeddedLLMRefusesWhileAnInstallRuns(t *testing.T) {
	f, _, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedllm.Hardware{Platform: "darwin-arm64", Arch: "arm64", RAMGiB: 32,
			Backend: embeddedllm.BackendMetal}, nil
	}
	release := make(chan struct{})
	started := make(chan struct{})
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer, embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		close(started)
		<-release
		return nil, errors.New("test run finished")
	}
	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("InstallEmbeddedLLM: %v", err)
	}
	<-started

	err := f.LoadEmbeddedLLM()
	if err == nil || !strings.Contains(err.Error(), "install is running") {
		t.Errorf("LoadEmbeddedLLM during an install = %v, want a refusal", err)
	}
	if err := f.RemoveEmbeddedLLM(); err == nil || !strings.Contains(err.Error(), "install is running") {
		t.Errorf("RemoveEmbeddedLLM during an install = %v, want a refusal", err)
	}
	close(release)
	waitForStatus(t, f, "the install run to finish", func(s EmbeddedLLMStatus) bool { return !s.Installing })
}

// ── removal ────────────────────────────────────────────────────────────────

// TestRemoveEmbeddedLLMClearsTreesConfigAndSupervisorState drives the REAL
// core removal (no network involved) across the RPC: both trees come off the
// disk, the flat embedding-model neighbour survives, the provider record is
// erased, the default model is migrated off the composite, and the supervisor
// stops reporting an installation.
func TestRemoveEmbeddedLLMClearsTreesConfigAndSupervisorState(t *testing.T) {
	f, rec, mock := newEmbeddedTestAPI(t)
	layout, manifest := writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	f.Lifecycle().InitEmbeddedLLM()

	// The config side of an installation, with the embedded model as the
	// default so the migration path is exercised.
	installEmbeddedLLM(t, f, 4321)
	f.config.LLM.DefaultModel = embeddedCompositeID()
	if err := config.Save(f.config, f.configPath); err != nil {
		t.Fatalf("saving the installed config: %v", err)
	}

	if err := f.RemoveEmbeddedLLM(); err != nil {
		t.Fatalf("RemoveEmbeddedLLM: %v", err)
	}

	// Both trees are gone; the flat embedding model is not.
	if _, err := os.Stat(layout.ModelRoot); !os.IsNotExist(err) {
		t.Errorf("the weights root still exists (stat err = %v)", err)
	}
	runtimeDir, err := layout.RuntimeDir(manifest.Backend)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Errorf("the runtime tree still exists (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(config.ModelsDir(f.agentDir), "ggml-model-q4_0.gguf")); err != nil {
		t.Errorf("the flat embedding model was deleted: %v", err)
	}

	// The config no longer claims an install, and the provider record is gone.
	if f.config.EmbeddedLLM.Installed || f.config.EmbeddedLLM.Port != 0 {
		t.Errorf("config = %+v, want the cleared state", f.config.EmbeddedLLM)
	}
	if entry, ok := f.config.LLM.OpenAICompatible[config.EmbeddedLLMProviderName]; ok {
		t.Errorf("the provider record survived: %+v", entry)
	}
	if f.config.LLM.DefaultModel == embeddedCompositeID() {
		t.Error("llm.default_model still points at the removed model")
	}
	if f.config.LLM.DefaultModel == "" {
		t.Error("llm.default_model was cleared instead of migrated to another enabled model")
	}
	if _, _, err := f.config.LLM.ResolveModelID(f.config.LLM.DefaultModel); err != nil {
		t.Errorf("the migrated llm.default_model %q does not resolve: %v",
			f.config.LLM.DefaultModel, err)
	}
	if mock.rebuildRouterCalls == 0 {
		t.Error("the router was not rebuilt after the removal")
	}
	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("reading the persisted config: %v", err)
	}
	if strings.Contains(string(data), "http://127.0.0.1:4321/v1") {
		t.Errorf("the persisted config still carries the embedded provider:\n%s", data)
	}

	// The supervisor and the status agree.
	status := f.GetEmbeddedLLMStatus()
	if status.Installed || status.State != string(embeddedllm.StateNotInstalled) {
		t.Errorf("status = %+v, want not installed", status)
	}
	if status.ModelID != "" || status.BaseURL != "" {
		t.Errorf("status identity = %q/%q, want empty after a removal", status.ModelID, status.BaseURL)
	}
	rec.waitForCount(t, EventEmbeddedLLMState, 2) // restore + removal
	states := rec.of(EventEmbeddedLLMState)
	last := statePayload(t, states[len(states)-1])
	if last.Installed {
		t.Errorf("the final state event = %+v, want not installed", last)
	}
	if got := rec.runtimeErrors(); len(got) != 0 {
		t.Errorf("a successful removal raised %d toast(s): %+v", len(got), got)
	}
}

// ── auto-unload setting ────────────────────────────────────────────────────

// TestSetEmbeddedLLMAutoUnloadPersistsAndApplies covers the setter: the config
// file carries the explicit choice, the supervisor's live policy changes with
// it, the state event reports the new budget, and an unusable value is refused
// without touching the config.
func TestSetEmbeddedLLMAutoUnloadPersistsAndApplies(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	writeEmbeddedInstallTree(t, f.agentDir, embeddedTestManifest(4321, 32768))
	server := tightenEmbeddedBudgets(t, f)
	f.Lifecycle().InitEmbeddedLLM()
	before := rec.count(EventEmbeddedLLMState)

	if err := f.SetEmbeddedLLMAutoUnload(false, 15); err != nil {
		t.Fatalf("SetEmbeddedLLMAutoUnload: %v", err)
	}

	if f.config.EmbeddedLLM.AutoUnload.Enabled == nil || *f.config.EmbeddedLLM.AutoUnload.Enabled {
		t.Errorf("auto_unload.enabled = %v, want an explicit false", f.config.EmbeddedLLM.AutoUnload.Enabled)
	}
	if f.config.EmbeddedLLM.AutoUnload.Minutes == nil || *f.config.EmbeddedLLM.AutoUnload.Minutes != 15 {
		t.Errorf("auto_unload.minutes = %v, want 15", f.config.EmbeddedLLM.AutoUnload.Minutes)
	}
	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatalf("reading the persisted config: %v", err)
	}
	if !strings.Contains(string(data), "minutes: 15") || !strings.Contains(string(data), "enabled: false") {
		t.Errorf("the persisted config does not carry the choice:\n%s", data)
	}
	policy := server.AutoUnloadPolicy()
	if policy.Enabled || policy.Idle != 15*time.Minute {
		t.Errorf("the supervisor policy = %+v, want disabled with a 15m budget", policy)
	}
	status := f.GetEmbeddedLLMStatus()
	if status.AutoUnloadEnabled || status.AutoUnloadMinutes != 15 {
		t.Errorf("status auto-unload = %v/%d, want false/15",
			status.AutoUnloadEnabled, status.AutoUnloadMinutes)
	}
	rec.waitForCount(t, EventEmbeddedLLMState, before+1)
	states := rec.of(EventEmbeddedLLMState)
	last := statePayload(t, states[len(states)-1])
	if last.AutoUnloadMinutes != 15 {
		t.Errorf("the state event reports auto_unload_minutes = %d, want 15", last.AutoUnloadMinutes)
	}
	rec.waitForCount(t, EventConfigUpdated, 1)

	// Re-enabling works too, and the value survives into the supervisor.
	if err := f.SetEmbeddedLLMAutoUnload(true, 30); err != nil {
		t.Fatalf("re-enabling: %v", err)
	}
	if policy := server.AutoUnloadPolicy(); !policy.Enabled || policy.Idle != 30*time.Minute {
		t.Errorf("the supervisor policy = %+v, want enabled with a 30m budget", policy)
	}

	// An unusable budget is refused and changes nothing.
	if err := f.SetEmbeddedLLMAutoUnload(true, 0); err == nil {
		t.Fatal("a zero-minute budget was accepted")
	}
	if got := f.config.EmbeddedLLM.AutoUnload.Minutes; got == nil || *got != 30 {
		t.Errorf("auto_unload.minutes = %v after a rejected write, want the previous 30", got)
	}
}

// ── event payload shape ────────────────────────────────────────────────────

// TestEmbeddedLLMEventPayloadsSerializeToTheCatalogShape pins the wire shape of
// both events: the JSON keys the frontend type guards and the event catalog
// depend on. A renamed Go field would otherwise silently break the UI.
func TestEmbeddedLLMEventPayloadsSerializeToTheCatalogShape(t *testing.T) {
	state := EmbeddedLLMStateData{
		Installed: true, Loading: false, Loaded: true,
		Packing: "PQ2_0", Backend: "metal", Port: 4321,
		ContextSize: 32768, AutoUnloadMinutes: 60, Error: "",
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling the state payload: %v", err)
	}
	var stateKeys map[string]any
	if err := json.Unmarshal(stateJSON, &stateKeys); err != nil {
		t.Fatalf("unmarshalling the state payload: %v", err)
	}
	for _, key := range []string{
		"installed", "loading", "loaded", "packing", "backend",
		"port", "context_size", "auto_unload_minutes", "error",
	} {
		if _, ok := stateKeys[key]; !ok {
			t.Errorf("the state payload is missing the %q key: %s", key, stateJSON)
		}
	}
	if len(stateKeys) != 9 {
		t.Errorf("the state payload carries %d keys (%s), want exactly the 9 documented ones",
			len(stateKeys), stateJSON)
	}

	progress := EmbeddedLLMProgressData{
		Component: "model", Stage: "downloading", BytesDone: 1, BytesTotal: 2,
	}
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		t.Fatalf("marshalling the progress payload: %v", err)
	}
	if want := `{"component":"model","stage":"downloading","bytes_done":1,"bytes_total":2}`; string(progressJSON) != want {
		t.Errorf("progress payload = %s, want %s", progressJSON, want)
	}
}
