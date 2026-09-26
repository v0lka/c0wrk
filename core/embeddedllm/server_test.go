package embeddedllm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive the REAL supervisor — production command-line rendering,
// production readiness polling, production output pumping, production
// termination — with only three things faked: the bytes on disk (a kilobyte
// tree instead of a 7 GiB install), the process (a double that can die on cue)
// and the HTTP endpoint that plays the part of llama-server's model list.
//
// Nothing here starts a real llama-server and nothing here waits minutes: the
// budgets are injected.

// ── doubles ──

// fakeProcess is a Process double. It stays alive until the test says
// otherwise, records every signal, and can be told to ignore the graceful one so
// the kill fallback is exercised.
type fakeProcess struct {
	mu      sync.Mutex
	pid     int
	stdout  io.Reader
	stderr  io.Reader
	signals []os.Signal
	kills   int
	// ignoreSignals models a wedged server: the graceful signal is recorded but
	// has no effect, so only a kill stops it.
	ignoreSignals bool
	// unkillable models the pathological case: even the kill does not take, so a
	// stop has to be reported as a failure instead of a success.
	unkillable bool

	exited   chan struct{}
	exitOnce sync.Once
	exitErr  error
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, exited: make(chan struct{})}
}

func (p *fakeProcess) Wait() error {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

func (p *fakeProcess) Pid() int { return p.pid }

func (p *fakeProcess) Signal(sig os.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	ignored := p.ignoreSignals
	dead := p.hasExitedLocked()
	p.mu.Unlock()
	if dead {
		return os.ErrProcessDone
	}
	if ignored {
		return nil
	}
	// A well-behaved server exits cleanly on the graceful signal.
	p.exit(nil)
	return nil
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.kills++
	dead := p.hasExitedLocked()
	unkillable := p.unkillable
	p.mu.Unlock()
	if dead {
		return os.ErrProcessDone
	}
	if unkillable {
		return nil
	}
	p.exit(errors.New("signal: killed"))
	return nil
}

func (p *fakeProcess) Stdout() io.Reader { return p.stdout }

func (p *fakeProcess) Stderr() io.Reader { return p.stderr }

func (p *fakeProcess) hasExitedLocked() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// exit ends the process exactly once.
func (p *fakeProcess) exit(err error) {
	p.exitOnce.Do(func() {
		p.mu.Lock()
		p.exitErr = err
		p.mu.Unlock()
		close(p.exited)
	})
}

// crash ends the process the way a segfault or an OOM kill does.
func (p *fakeProcess) crash(status int) {
	p.exit(fmt.Errorf("signal: segmentation fault (exit status %d)", status))
}

func (p *fakeProcess) signalLog() []os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]os.Signal(nil), p.signals...)
}

func (p *fakeProcess) killCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kills
}

// setUnkillable flips the pathological flag after the process exists. It goes
// through the process mutex, because a concurrent terminate may be reading it.
func (p *fakeProcess) setUnkillable(unkillable bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unkillable = unkillable
}

func (p *fakeProcess) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.hasExitedLocked()
}

// fakeSpawner records the command lines it was asked to run instead of running
// them.
type fakeSpawner struct {
	mu       sync.Mutex
	commands []LaunchCommand
	procs    []*fakeProcess
	err      error
	// configure lets a test shape the process about to be handed back.
	configure func(proc *fakeProcess, cmd LaunchCommand)
	nextPID   int
}

func (sp *fakeSpawner) spawn(_ context.Context, cmd LaunchCommand) (Process, error) {
	sp.mu.Lock()
	sp.commands = append(sp.commands, cmd)
	err := sp.err
	sp.nextPID += 4242
	pid := sp.nextPID
	configure := sp.configure
	sp.mu.Unlock()
	if err != nil {
		return nil, err
	}
	proc := newFakeProcess(pid)
	if configure != nil {
		configure(proc, cmd)
	}
	sp.mu.Lock()
	sp.procs = append(sp.procs, proc)
	sp.mu.Unlock()
	return proc, nil
}

func (sp *fakeSpawner) spawnCount() int {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return len(sp.commands)
}

func (sp *fakeSpawner) lastCommand() LaunchCommand {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.commands) == 0 {
		return LaunchCommand{}
	}
	return sp.commands[len(sp.commands)-1]
}

func (sp *fakeSpawner) lastProcess() *fakeProcess {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.procs) == 0 {
		return nil
	}
	return sp.procs[len(sp.procs)-1]
}

func (sp *fakeSpawner) allProcesses() []*fakeProcess {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return append([]*fakeProcess(nil), sp.procs...)
}

// modelsEndpoint plays the part of llama-server's HTTP surface. It answers
// /health immediately and unconditionally — which is exactly why readiness must
// not consult it — and gates /v1/models behind a configurable number of
// not-ready answers.
type modelsEndpoint struct {
	server *httptest.Server
	port   int

	mu          sync.Mutex
	healthHits  int
	modelsHits  int
	propsHits   int
	notReady    int
	readyHook   func()
	onProbe     func(hits int)
	hookFired   bool
	modelsBody  string
	propsBody   string
	propsFail   bool
	neverReady  bool
	lastRequest string
}

// serveProps makes /props answer with a per-slot context and a slot count — the
// two figures the post-ready readback multiplies into the effective total. An
// empty body with propsFail false means "no /props route", which is what a
// server that does not implement it looks like.
func (ep *modelsEndpoint) serveProps(nCtx, slots int) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	ep.propsBody = fmt.Sprintf(
		`{"default_generation_settings":{"n_ctx":%d,"model_path":"x"},"total_slots":%d}`, nCtx, slots)
}

// failProps makes /props answer 500, i.e. the readback fails.
func (ep *modelsEndpoint) failProps() {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	ep.propsFail = true
	ep.propsBody = ""
}

func newModelsEndpoint(t *testing.T) *modelsEndpoint {
	t.Helper()
	ep := &modelsEndpoint{modelsBody: `{"object":"list","data":[{"id":"Bonsai 2 27B","object":"model"}]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		ep.mu.Lock()
		ep.healthHits++
		ep.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		ep.mu.Lock()
		ep.modelsHits++
		ep.lastRequest = "/v1/models"
		stillNotReady := ep.neverReady || ep.modelsHits <= ep.notReady
		hook := ep.readyHook
		probe := ep.onProbe
		hits := ep.modelsHits
		fireHook := hook != nil && !ep.hookFired && !stillNotReady
		if fireHook {
			ep.hookFired = true
		}
		body := ep.modelsBody
		ep.mu.Unlock()

		// Hooks run outside the endpoint lock: a test uses them to advance a
		// fake clock, which has its own.
		if probe != nil {
			probe(hits)
		}
		if fireHook {
			hook()
		}
		w.Header().Set("Content-Type", "application/json")
		if stillNotReady {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"loading model"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/props", func(w http.ResponseWriter, _ *http.Request) {
		ep.mu.Lock()
		ep.propsHits++
		ep.lastRequest = "/props"
		fail := ep.propsFail
		body := ep.propsBody
		ep.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if fail || body == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"no properties"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ep.mu.Lock()
		ep.lastRequest = r.URL.Path
		ep.mu.Unlock()
		http.NotFound(w, r)
	})

	ep.server = httptest.NewServer(mux)
	t.Cleanup(ep.server.Close)
	addr, ok := ep.server.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the test endpoint is not a TCP listener: %T", ep.server.Listener.Addr())
	}
	ep.port = addr.Port
	return ep
}

func (ep *modelsEndpoint) hits(path string) int {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if path == "/health" {
		return ep.healthHits
	}
	return ep.modelsHits
}

// fakeClock is a manually advanced clock, so a "the load took ten minutes"
// scenario costs no wall time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// eventRecorder captures the state transitions the supervisor emits.
type eventRecorder struct {
	mu     sync.Mutex
	events []StateEvent
}

func (r *eventRecorder) onState(event StateEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

func (r *eventRecorder) states() []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]State, 0, len(r.events))
	for _, event := range r.events {
		out = append(out, event.State)
	}
	return out
}

func (r *eventRecorder) last() (StateEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return StateEvent{}, false
	}
	return r.events[len(r.events)-1], true
}

func (r *eventRecorder) count(state State) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, event := range r.events {
		if event.State == state {
			n++
		}
	}
	return n
}

// logCapture is a thread-safe slog sink.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ── fixture ──

// fixtureOptions shapes a supervision fixture. Zero values are the boring,
// healthy case: a CPU install on linux-amd64 with the auto-unload timer off.
type fixtureOptions struct {
	backend     Backend
	packing     Packing
	contextSize int
	platform    string
	hostOS      string
	autoUnload  AutoUnload
	// notReady is how many /v1/models probes answer 503 before the model list
	// appears, i.e. a slow weight load.
	notReady int
	// neverReady keeps the model list from ever appearing.
	neverReady bool
	// corruptContext writes an unusable context tier into the manifest instead
	// of defaulting it.
	corruptContext bool
	// readyHook runs once, on the first ready answer — the seam a test uses to
	// advance a fake clock by the "duration" of the load.
	readyHook func()
	// onProbe runs on EVERY /v1/models probe with the probe count, so a test can
	// make a fake clock advance as the "weight load" progresses.
	onProbe    func(hits int)
	spawnErr   error
	ensurePort func(ctx context.Context, port int) (int, error)
	// probeDevices wires Server.ProbeDevices. nil leaves it UNWIRED, which is
	// the pre-existing "no hardware probe runs at load time" behaviour and what
	// every test that predates the load-time refinement expects.
	probeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// tuning wires Server.Tuning, the operator override vocabulary a load-time
	// re-plan reads.
	tuning func() Tuning
	// propsNCtx / propsSlots are what the fake server's /props reports once it
	// is ready. propsNCtx defaults to the manifest's context tier, so the
	// readback agrees with the record and a test that is not about the readback
	// sees no manifest write. propsFail makes /props answer 500 instead.
	propsNCtx  int
	propsSlots int
	propsFail  bool
	// plan / topology are recorded into the fixture manifest, which is how a
	// test stages a memory-aware install.
	plan       *MemoryPlan
	topology   *MemoryTopology
	now        func() time.Time
	stdout     string
	stderr     string
	readyDelay time.Duration
	// readyTimeout bounds the whole load. <= 0 → 30 s.
	readyTimeout time.Duration
	stopTimeout  time.Duration
	logger       *slog.Logger
	noMMProj     bool
}

type fixture struct {
	t        *testing.T
	layout   Layout
	manifest Manifest
	srv      *Server
	spawner  *fakeSpawner
	events   *eventRecorder
	endpoint *modelsEndpoint
	logs     *logCapture

	persistMu        sync.Mutex
	persistedContext []int
}

// persistedContexts returns every context value the supervisor handed to
// Server.PersistContext, in order.
func (fx *fixture) persistedContexts() []int {
	fx.persistMu.Lock()
	defer fx.persistMu.Unlock()
	out := make([]int, len(fx.persistedContext))
	copy(out, fx.persistedContext)
	return out
}

// recordContext is the Server.PersistContext seam.
func (fx *fixture) recordContext(_ context.Context, contextSize int) error {
	fx.persistMu.Lock()
	defer fx.persistMu.Unlock()
	fx.persistedContext = append(fx.persistedContext, contextSize)
	return nil
}

// diskManifest re-reads manifest.json, so a test asserts what a NEXT load would
// see rather than what the fixture started with.
func (fx *fixture) diskManifest(t *testing.T) Manifest {
	t.Helper()
	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	m, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	return m
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()

	if opts.backend == "" {
		opts.backend = BackendCPU
	}
	if opts.packing == "" {
		opts.packing = PackingPQ2_0
	}
	if opts.contextSize == 0 && !opts.corruptContext {
		opts.contextSize = 16384
	}
	if opts.platform == "" {
		opts.platform = PlatformLinuxAMD64
	}
	if opts.readyDelay == 0 {
		opts.readyDelay = 2 * time.Millisecond
	}
	if opts.readyTimeout == 0 {
		opts.readyTimeout = 30 * time.Second
	}
	if opts.stopTimeout == 0 {
		opts.stopTimeout = 250 * time.Millisecond
	}

	endpoint := newModelsEndpoint(t)
	endpoint.notReady = opts.notReady
	endpoint.neverReady = opts.neverReady
	endpoint.readyHook = opts.readyHook
	endpoint.onProbe = opts.onProbe
	// By default the fake server reports exactly the context the manifest
	// records, so the post-ready readback agrees with the record and a test
	// that is not about the readback observes no manifest write.
	if opts.propsFail {
		endpoint.failProps()
	} else {
		nCtx := opts.propsNCtx
		if nCtx == 0 {
			nCtx = opts.contextSize
		}
		slots := opts.propsSlots
		if slots == 0 {
			slots = 1
		}
		endpoint.serveProps(nCtx, slots)
	}

	layout, _ := testLayout(t)
	manifest := installedTree(t, layout, treeOptions{
		backend:     opts.backend,
		packing:     opts.packing,
		port:        endpoint.port,
		contextSize: opts.contextSize,
		goos:        opts.hostOS,
		noMMProj:    opts.noMMProj,
	})

	// By default the fixture captures the subsystem log, so a test can assert
	// that the child's own output reached slog.
	logs := &logCapture{}
	logger := logs.logger()
	if opts.logger != nil {
		logger = opts.logger
	}

	spawner := &fakeSpawner{}
	if opts.spawnErr != nil {
		spawner.err = opts.spawnErr
	}
	if opts.stdout != "" || opts.stderr != "" {
		stdout, stderr := opts.stdout, opts.stderr
		spawner.configure = func(proc *fakeProcess, _ LaunchCommand) {
			if stdout != "" {
				proc.stdout = strings.NewReader(stdout)
			}
			if stderr != "" {
				proc.stderr = strings.NewReader(stderr)
			}
		}
	}

	events := &eventRecorder{}
	srv := NewServer(layout, logger)
	srv.Spawn = spawner.spawn
	srv.OnState = events.onState
	srv.Platform = opts.platform
	srv.HostOS = opts.hostOS
	srv.AutoUnload = opts.autoUnload
	srv.EnsurePort = opts.ensurePort
	srv.ProbeDevices = opts.probeDevices
	srv.Tuning = opts.tuning
	srv.Now = opts.now
	srv.ReadyPollInterval = opts.readyDelay
	srv.ReadyTimeout = opts.readyTimeout
	srv.ProbeTimeout = 2 * time.Second
	srv.StopTimeout = opts.stopTimeout

	// The fixture describes an installation startup has already restored, so the
	// state machine starts at installed. The transition that emits is fixture
	// setup, not behaviour under test.
	if err := srv.SetInstalled(true); err != nil {
		t.Fatalf("SetInstalled: %v", err)
	}
	events.reset()

	// A staged plan and topology go into the manifest the supervisor reads, so
	// the fixture describes a memory-aware install rather than a legacy one.
	if opts.plan != nil || opts.topology != nil {
		manifest.Plan = opts.plan
		manifest.Topology = opts.topology
		path, err := layout.ManifestPath()
		if err != nil {
			t.Fatalf("ManifestPath: %v", err)
		}
		if err := writeManifest(path, manifest); err != nil {
			t.Fatalf("writeManifest: %v", err)
		}
	}

	fx := &fixture{
		t:        t,
		layout:   layout,
		manifest: manifest,
		srv:      srv,
		spawner:  spawner,
		events:   events,
		endpoint: endpoint,
		logs:     logs,
	}
	srv.PersistContext = fx.recordContext
	t.Cleanup(func() {
		// Never leave a supervise goroutine blocked on a process that will not
		// exit, and never leave an idle timer armed after the test ends.
		srv.SetAutoUnload(AutoUnload{Enabled: false})
		for _, proc := range spawner.allProcesses() {
			proc.exit(errors.New("test cleanup"))
		}
	})
	return fx
}

type treeOptions struct {
	backend     Backend
	packing     Packing
	port        int
	contextSize int
	goos        string
	noMMProj    bool
}

// installedTree writes a kilobyte stand-in for a real install: the runtime tree
// with llama-server nested the way the pinned archives nest it, the weights, the
// projector and manifest.json.
func installedTree(t *testing.T, layout Layout, opts treeOptions) Manifest {
	t.Helper()

	runtimeDir := filepath.Join(layout.RuntimesRoot, RuntimeDirName(opts.backend))
	// An empty goos means the host, exactly as ServerBinaryPath reads it, so the
	// tree always contains the name the supervisor will look for.
	goos := opts.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	binaryName := ServerBinaryName
	if goos == "windows" {
		binaryName += ".exe"
	}
	binaryDir := filepath.Join(runtimeDir, "build", "bin")
	if err := os.MkdirAll(binaryDir, 0o750); err != nil {
		t.Fatalf("creating the runtime tree: %v", err)
	}
	binary := filepath.Join(binaryDir, binaryName)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho fake\n"), 0o750); err != nil {
		t.Fatalf("writing the server binary: %v", err)
	}
	// A library next to the binary, so the tree looks like a real CUDA/ROCm
	// release to anything that walks it.
	if err := os.WriteFile(filepath.Join(binaryDir, "libggml.so"), []byte("stub"), 0o640); err != nil {
		t.Fatalf("writing the runtime library: %v", err)
	}

	if err := os.MkdirAll(layout.ModelRoot, 0o750); err != nil {
		t.Fatalf("creating the model root: %v", err)
	}
	modelFile := filepath.Join(layout.ModelRoot, modelNameFor(opts.packing))
	if err := os.WriteFile(modelFile, []byte("gguf-stub"), 0o640); err != nil {
		t.Fatalf("writing the model file: %v", err)
	}
	if !opts.noMMProj {
		projector := filepath.Join(layout.ModelRoot, MMProjAsset().ArchiveName)
		if err := os.WriteFile(projector, []byte("mmproj-stub"), 0o640); err != nil {
			t.Fatalf("writing the projector: %v", err)
		}
	}

	manifest := Manifest{
		Packing:        opts.packing,
		Backend:        opts.backend,
		RuntimeVersion: RuntimeTag,
		Checksums:      map[string]string{string(ComponentModel): "stub"},
		Port:           opts.port,
		ContextSize:    opts.contextSize,
		ModelFile:      modelFile,
		InstalledAt:    testInstallInstant.Format(time.RFC3339),
	}
	path, err := layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if err := writeManifest(path, manifest); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return manifest
}

// ── assertion helpers ──

func waitForState(t *testing.T, srv *Server, want State) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return srv.State() == want },
		fmt.Sprintf("state %s", want))
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func requireArgs(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, arg := range args {
		if arg != flag {
			continue
		}
		if i+1 >= len(args) {
			t.Fatalf("%s is the last argument, so it carries no value: %v", flag, args)
		}
		if args[i+1] != want {
			t.Fatalf("%s = %q, want %q (args: %v)", flag, args[i+1], want, args)
		}
		return
	}
	t.Fatalf("%s is missing from %v", flag, args)
}

func requireFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			return
		}
	}
	t.Fatalf("%s is missing from %v", flag, args)
}

func requireNoFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, arg := range args {
		if arg == flag {
			t.Fatalf("%s must not be present: %v", flag, args)
		}
	}
}

func argValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// ── acceptance: readiness ──

// TestLoadBecomesReadyOnModelsEndpointNotHealth is the acceptance test for the
// readiness rule: /health answers from the very first moment (the socket is open
// long before the weights are in memory), so a Load that trusted it would report
// a model that cannot serve. Readiness must come from /v1/models alone.
func TestLoadBecomesReadyOnModelsEndpointNotHealth(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{notReady: 3})

	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
	if hits := fx.endpoint.hits("/health"); hits != 0 {
		t.Errorf("/health was probed %d times; readiness must never consult it", hits)
	}
	if hits := fx.endpoint.hits("/v1/models"); hits < 4 {
		t.Errorf("/v1/models was probed %d times, want at least the 3 failures plus the success", hits)
	}
	if got := fx.srv.Port(); got != fx.endpoint.port {
		t.Errorf("Port() = %d, want the persisted %d", got, fx.endpoint.port)
	}
	if got := fx.srv.BaseURL(); got != fmt.Sprintf("http://127.0.0.1:%d/v1", fx.endpoint.port) {
		t.Errorf("BaseURL() = %q", got)
	}
	states := fx.events.states()
	if len(states) == 0 || states[0] != StateLoading || states[len(states)-1] != StateLoaded {
		t.Errorf("emitted states = %v, want loading…loaded", states)
	}
}

// TestLoadRequiresANonEmptyModelList pins the second half of the readiness rule:
// a 200 is not enough, because a server that is still initialising can answer
// the route with an empty list.
func TestLoadRequiresANonEmptyModelList(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	fx.endpoint.mu.Lock()
	fx.endpoint.modelsBody = `{"object":"list","data":[]}`
	fx.endpoint.neverReady = false
	fx.endpoint.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := fx.srv.Load(ctx)
	if err == nil {
		t.Fatal("Load succeeded against an empty model list")
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
	if hits := fx.endpoint.hits("/v1/models"); hits < 2 {
		t.Errorf("an empty model list stopped the polling after %d probe(s); it must keep polling", hits)
	}
}

// TestLoadFailsFastWhenTheProcessDiesWhileLoading covers the case the ready
// budget exists to bound: a server that segfaults halfway through the weight load
// must be reported in milliseconds, not after the timeout.
func TestLoadFailsFastWhenTheProcessDiesWhileLoading(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{neverReady: true})
	fx.spawner.configure = func(proc *fakeProcess, _ LaunchCommand) {
		proc.stderr = strings.NewReader("ggml_cuda_init: found 0 devices\n")
		go func() {
			time.Sleep(20 * time.Millisecond)
			proc.crash(139)
		}()
	}

	start := time.Now()
	err := fx.srv.Load(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrServerDied) {
		t.Fatalf("Load error = %v, want it to wrap ErrServerDied", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("a dead server took %s to report; it must not wait out the ready budget", elapsed)
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
	event, ok := fx.events.last()
	if !ok || event.State != StateError {
		t.Fatalf("last event = %+v, want an error transition", event)
	}
	if !strings.Contains(event.Message, "ggml_cuda_init") {
		t.Errorf("the error carries no server output:\n%s", event.Message)
	}
}

// ── the fit contract's failure signature (R1 regression guard) ──

// The two lines below are the fit pass's two verdicts in the fork's own
// spelling. The success line is verbatim from a fit trace captured 2026-09-26
// from the pin this package ships (`prism-b10735-842b188`, darwin-arm64,
// projected 24450 MiB against 109950 MiB free); the failure line is the same
// sentence in the abort spelling `fitFailureMarker` exists to detect. They
// differ by one word, which is why the scanner matches the failure sentence
// and not a looser fragment.
const (
	capturedFitSuccessLine = "0.00.287.233 I common_fit_params: successfully fit params to free device memory"
	fitAbortLine           = "0.00.290.001 E common_fit_params: failed to fit params to free device memory"
)

// TestScanFitFailureMatchesTheFailureSpellingOnly is the pure half of the R1
// guard: the scanner distinguishes the abort from the success line that ends
// every healthy fit pass, prefix noise cannot hide the marker, and a tail
// without the complaint yields no warning.
func TestScanFitFailureMatchesTheFailureSpellingOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"empty tail", "", false},
		{"the captured success verdict", capturedFitSuccessLine, false},
		{"an unrelated complaint", "ggml_cuda_init: found 0 devices", false},
		{"the abort verdict", fitAbortLine, true},
		{"the abort buried mid-tail",
			"load: loading model\n" + fitAbortLine + "\nload: failed", true},
	}
	for _, tc := range cases {
		if got := scanFitFailure(tc.tail); (got != "") != tc.want {
			t.Errorf("%s: scanFitFailure = %q, want a warning: %v", tc.name, got, tc.want)
		}
	}
	if got := scanFitFailure(fitAbortLine); !strings.Contains(got, fitFailureMarker) {
		t.Errorf("the warning does not quote the marker it found:\n%s", got)
	}
}

// TestLoadTimeoutSurfacesTheFitContractWarning stages the pin-bump scenario the
// guard exists for: a launch whose fit pass aborted (the process is killed
// after the ready budget because it never serves), with the fork's complaint
// in the output tail. The complaint must surface in Status instead of dying
// with the discarded run.
func TestLoadTimeoutSurfacesTheFitContractWarning(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		neverReady:   true,
		readyTimeout: 200 * time.Millisecond,
		stderr:       fitAbortLine + "\n",
	})
	err := fx.srv.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded against a server that never became ready")
	}

	snapshot := fx.srv.Status()
	if snapshot.FitWarning == "" {
		t.Fatal("the fit-failure complaint in the output tail was not surfaced as a warning")
	}
	if !strings.Contains(snapshot.FitWarning, fitFailureMarker) {
		t.Errorf("the warning does not carry the fit-failure marker:\n%s", snapshot.FitWarning)
	}
}

// TestLoadDeathWithoutTheFitComplaintStaysQuiet pins the guard's negative: an
// ordinary crash (a segfault, a missing CUDA library) is NOT mislabelled as a
// fit-contract breach.
func TestLoadDeathWithoutTheFitComplaintStaysQuiet(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{neverReady: true})
	fx.spawner.configure = func(proc *fakeProcess, _ LaunchCommand) {
		proc.stderr = strings.NewReader("ggml_cuda_init: found 0 devices\n")
		go func() {
			time.Sleep(20 * time.Millisecond)
			proc.crash(139)
		}()
	}
	if err := fx.srv.Load(context.Background()); err == nil {
		t.Fatal("Load succeeded against a crashing server")
	}
	if got := fx.srv.Status().FitWarning; got != "" {
		t.Errorf("a crash without the fit complaint raised a fit warning:\n%s", got)
	}
}

// TestLoadReadyClearsTheFitWarning pins the clearing half of the contract: the
// finding describes the LAST FAILED launch, so a launch that becomes ready —
// proving the fit contract held on this pin — retires it.
func TestLoadReadyClearsTheFitWarning(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		neverReady:   true,
		readyTimeout: 200 * time.Millisecond,
		stderr:       fitAbortLine + "\n",
	})
	if err := fx.srv.Load(context.Background()); err == nil {
		t.Fatal("the first Load succeeded against a never-ready server")
	}
	if got := fx.srv.Status().FitWarning; got == "" {
		t.Fatal("the failed load did not record the fit warning")
	}

	// The endpoint now answers: the second launch is a healthy one.
	fx.endpoint.mu.Lock()
	fx.endpoint.neverReady = false
	fx.endpoint.mu.Unlock()
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("the retry Load: %v", err)
	}
	if got := fx.srv.Status().FitWarning; got != "" {
		t.Errorf("a ready launch did not clear the fit warning:\n%s", got)
	}
}

// TestLoadRefusesWhenNothingIsInstalled checks the state machine's entry gate:
// no manifest, no process.
func TestLoadRefusesWhenNothingIsInstalled(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	path, err := fx.layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing the manifest: %v", err)
	}

	err = fx.srv.Load(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Load error = %v, want ErrNotInstalled", err)
	}
	if got := fx.srv.State(); got != StateNotInstalled {
		t.Errorf("state = %s, want %s", got, StateNotInstalled)
	}
	if n := fx.spawner.spawnCount(); n != 0 {
		t.Errorf("%d process(es) were spawned for an install that does not exist", n)
	}
}

// TestLoadRefusesAMissingModelFile covers the install record that outlived its
// bytes: the manifest is there, the weights are not.
func TestLoadRefusesAMissingModelFile(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	if err := os.Remove(fx.manifest.ModelFile); err != nil {
		t.Fatalf("removing the weights: %v", err)
	}

	err := fx.srv.Load(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Load error = %v, want it to wrap ErrNotInstalled", err)
	}
	if n := fx.spawner.spawnCount(); n != 0 {
		t.Errorf("%d process(es) were spawned without weights", n)
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
}

// TestLoadReportsASpawnFailure checks that a binary the OS refuses to start is an
// error state, not a hang.
func TestLoadReportsASpawnFailure(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{spawnErr: errors.New("permission denied")})

	err := fx.srv.Load(context.Background())
	if err == nil {
		t.Fatal("Load succeeded although the spawn failed")
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
	event, _ := fx.events.last()
	if !strings.Contains(event.Message, "permission denied") {
		t.Errorf("the event message lost the cause: %q", event.Message)
	}
}

// TestLoadHonoursCancellation verifies the ready wait is ctx-bound and that a
// cancelled load does not leave a process behind.
func TestLoadHonoursCancellation(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{neverReady: true})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	err := fx.srv.Load(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load error = %v, want context.Canceled", err)
	}
	proc := fx.spawner.lastProcess()
	if proc == nil {
		t.Fatal("no process was spawned")
	}
	waitFor(t, 2*time.Second, func() bool { return !proc.alive() }, "the cancelled load's process to stop")
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
}

// TestUnexpectedCleanExitIsStillAnError covers the exit that carries no error: a
// server that returns 0 on its own was still not asked to stop, so it is a fault
// and must not be reported as an unload.
func TestUnexpectedCleanExitIsStillAnError(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()
	proc.exit(nil)

	waitForState(t, fx.srv, StateError)
	event, _ := fx.events.last()
	if !strings.Contains(event.Message, "exited with status 0") {
		t.Errorf("a clean unexpected exit is not explained: %q", event.Message)
	}
	if got := fx.srv.State(); got == StateInstalled {
		t.Error("an unexpected exit was reported as an unload")
	}
}

// TestLoadTimesOutWhenTheModelNeverAppears covers the ready budget: a server that
// stays up but never finishes loading is a failure with a diagnostic, not an
// eternal wait.
func TestLoadTimesOutWhenTheModelNeverAppears(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		readyTimeout: 120 * time.Millisecond,
		readyDelay:   10 * time.Millisecond,
	})
	fx.endpoint.mu.Lock()
	fx.endpoint.modelsBody = `{"object":"list","data":[]}`
	fx.endpoint.mu.Unlock()

	start := time.Now()
	err := fx.srv.Load(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrLoadTimeout) {
		t.Fatalf("Load error = %v, want ErrLoadTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the ready budget was not honoured: Load took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "the model list stayed empty") {
		t.Errorf("the timeout carries no diagnostic: %v", err)
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
	if proc := fx.spawner.lastProcess(); proc == nil || proc.alive() {
		t.Error("a load that timed out left its process running")
	}
}

// ── acceptance: single instance ──

// TestLoadIsIdempotentAndSingleInstance is the acceptance test for the lock: a
// Load against a serving model is a no-op, and concurrent Loads still produce
// exactly one process.
func TestLoadIsIdempotentAndSingleInstance(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	ctx := context.Background()

	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	first := fx.spawner.lastProcess()

	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("%d processes were spawned for two Loads, want 1", got)
	}
	if fx.spawner.lastProcess() != first {
		t.Error("the second Load replaced the running process")
	}

	// Concurrent loads, all of which must coalesce onto the running one.
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fx.srv.Load(ctx)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Load %d: %v", i, err)
		}
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("%d processes were spawned by %d concurrent Loads, want 1", got, workers+2)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
}

// TestLoadAfterACrashStartsAFreshProcess checks that error is not terminal: the
// user's retry must be able to bring the model back.
func TestLoadAfterACrashStartsAFreshProcess(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := fx.spawner.lastProcess()
	first.crash(139)
	waitForState(t, fx.srv, StateError)

	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("the retry Load: %v", err)
	}
	if got := fx.spawner.spawnCount(); got != 2 {
		t.Errorf("%d processes were spawned, want 2 (the crashed one is not reusable)", got)
	}
	if fx.spawner.lastProcess() == first {
		t.Error("the retry reused the crashed process")
	}
	waitForState(t, fx.srv, StateLoaded)
}

// ── acceptance: process death ──

// TestProcessDeathTransitionsToErrorAndEmits is the acceptance test for crash
// detection: a dead server is never reported as loaded.
func TestProcessDeathTransitionsToErrorAndEmits(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		stderr: "srv  load_model: loaded in 4211 ms\n",
		// An armed idle timer must be disarmed by the death, not left running
		// against a process that is gone.
		autoUnload: AutoUnload{Enabled: true, Idle: time.Hour},
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(fx.logs.String(), "loaded in 4211 ms")
	}, "the server's own log line to reach slog")

	proc := fx.spawner.lastProcess()
	before := fx.events.count(StateError)
	proc.crash(139)

	waitForState(t, fx.srv, StateError)
	if got := fx.srv.State(); got == StateLoaded {
		t.Fatal("a dead process is still reported as loaded")
	}
	if fx.events.count(StateError) <= before {
		t.Errorf("no error event was emitted: %v", fx.events.states())
	}
	event, _ := fx.events.last()
	if event.State != StateError {
		t.Errorf("last event state = %s, want %s", event.State, StateError)
	}
	if event.Port != fx.endpoint.port {
		t.Errorf("event port = %d, want %d", event.Port, fx.endpoint.port)
	}
	if !strings.Contains(event.Message, "loaded in 4211 ms") {
		t.Errorf("the crash event carries no server output:\n%s", event.Message)
	}
	if got := fx.srv.Status().Pid; got != 0 {
		t.Errorf("Status().Pid = %d after the process died, want 0", got)
	}
	if left, armed := fx.srv.IdleRemaining(); armed || left != 0 {
		t.Errorf("the idle timer is still armed for a dead server: %v armed=%v", left, armed)
	}
}

// TestServerOutputReachesSlog pins the logging contract: both streams of the
// child are drained into slog, at debug level, tagged with the pid.
func TestServerOutputReachesSlog(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		stdout: "listening on http://127.0.0.1:8080 - starting main loop\n",
		stderr: "main: server is listening on port 8080\n",
	})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		logs := fx.logs.String()
		return strings.Contains(logs, "starting main loop") &&
			strings.Contains(logs, "server is listening")
	}, "both output streams to reach slog")

	logs := fx.logs.String()
	if !strings.Contains(logs, "level=DEBUG") {
		t.Errorf("the server output was not logged at debug level:\n%s", logs)
	}
	if !strings.Contains(logs, "stream=stdout") || !strings.Contains(logs, "stream=stderr") {
		t.Errorf("the log lines do not say which stream they came from:\n%s", logs)
	}
	if !strings.Contains(logs, "pid=4242") {
		t.Errorf("the log lines carry no pid:\n%s", logs)
	}
}

// TestLoadDiscardsALeftoverProcessBeforeSpawning covers the recovery path from a
// stop that did not take: the state says the model is not serving, but a process
// is still resident. Single instance must win over starting a second one.
func TestLoadDiscardsALeftoverProcessBeforeSpawning(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	leftover := fx.spawner.lastProcess()

	// The aftermath of a failed stop: an error state with a live process the
	// Server still owns.
	fx.srv.mu.Lock()
	fx.srv.state = StateError
	fx.srv.message = "llama-server did not exit within 15s of being killed"
	fx.srv.mu.Unlock()
	fx.events.reset()

	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("the recovery Load: %v", err)
	}
	if leftover.alive() {
		t.Error("the leftover process was not stopped before the new one started")
	}
	if got := fx.spawner.spawnCount(); got != 2 {
		t.Errorf("%d processes were spawned, want 2", got)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
	if proc := fx.spawner.lastProcess(); proc == nil || !proc.alive() {
		t.Error("the freshly spawned process is not running")
	}
	// Detaching the leftover means its exit is not reported as a crash.
	if n := fx.events.count(StateError); n != 0 {
		t.Errorf("discarding the leftover emitted %d error event(s): %v", n, fx.events.states())
	}
}

// TestUnloadReportsAStopThatDidNotTake checks the honest failure: when the
// process survives both the signal and the kill, the state says so and the
// handle is kept, so a retry can try again instead of a second server appearing.
func TestUnloadReportsAStopThatDidNotTake(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{stopTimeout: 20 * time.Millisecond})
	fx.srv.killWaitFor = 30 * time.Millisecond
	fx.spawner.configure = func(proc *fakeProcess, _ LaunchCommand) {
		proc.ignoreSignals = true
		proc.unkillable = true
	}
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	live := fx.spawner.lastProcess()

	err := fx.srv.Unload(context.Background())
	if err == nil {
		t.Fatal("Unload reported success for a process that is still running")
	}
	if !live.alive() {
		t.Error("the double was supposed to survive both the signal and the kill")
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
	if !strings.Contains(fx.srv.Status().Message, "did not exit") {
		t.Errorf("the status message is not actionable: %q", fx.srv.Status().Message)
	}

	// The handle is kept, so the next attempt targets THIS process instead of
	// stacking a second one on top of it.
	live.setUnkillable(false)
	if err := fx.srv.Unload(context.Background()); err != nil {
		t.Errorf("the retry once the process became killable: %v", err)
	}
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s after a successful retry", got, StateInstalled)
	}
	if live.alive() {
		t.Error("the retry left the process running")
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("%d processes were spawned, want 1", got)
	}
}

// ── acceptance: the command line ──

// TestLaunchSpecArgsGoldenShapes pins the exact command line, flag order
// included, for every shape the launch spec can take. These are golden on
// purpose: the flag order is part of the contract (it is what a support bundle's
// process listing shows, and what a diff against the pinned fork's own demo
// scripts reads), and a reordering is a reviewable change rather than a silent
// one.
//
// The six shapes are the ones that matter:
//
//	fit-auto                     the exclusivity rule's first branch — NO -ngl
//	fit-off-explicit-ngl         the second branch, and today's install path
//	fit-with-target              both fit knobs rendered
//	q4-0-kv-without-offload      the escalated cache in system RAM
//	cpu-with-prompt-cache-off    -ngl 0, the projector reserve in host RAM
//	multi-device-explicit-split  -ngl all beside -sm and -dev
func TestLaunchSpecArgsGoldenShapes(t *testing.T) {
	t.Parallel()

	const (
		binary = "/tmp/runtimes/llama-x/build/bin/llama-server"
		model  = "/tmp/models/bonsai.gguf"
		mmproj = "/tmp/models/mmproj.gguf"
		port   = 4321
	)

	cases := []struct {
		name string
		spec LaunchSpec
		want []string
	}{
		{
			// The runtime sizes the offload AND the context, so `-ngl` is
			// absent rather than set to a value fit would have chosen, and the
			// context is an explicit zero the fork reads as "loaded from model"
			// and fit then adjusts. `-fitc` carries the floor it is held to.
			name: "fit-auto",
			spec: LaunchSpec{
				ServerBinary:   binary,
				ModelFile:      model,
				Host:           LoopbackHost,
				Port:           port,
				Fit:            true,
				Layers:         LayerAuto(),
				ContextSize:    0,
				FitMinContext:  DefaultFitMinContext,
				KVType:         KVTypeF16,
				KVOffload:      boolPtr(true),
				MMProjOffload:  boolPtr(true),
				Parallel:       DefaultParallel,
				ImageMaxTokens: ImageMaxTokensUncapped,
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "on",
				"-fitc", "65536",
				"-fa", "on",
				"-c", "0",
				"-ctk", "f16", "-ctv", "f16",
				"-np", "1",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
			},
		},
		{
			// The shape Server.launchSpec produces today: the install record
			// pins the offload, so fit is off and `-fit off` is rendered rather
			// than inherited from the fork's own 'on' default.
			name: "fit-off-explicit-ngl",
			spec: LaunchSpec{
				ServerBinary:   binary,
				ModelFile:      model,
				MMProjFile:     mmproj,
				Host:           LoopbackHost,
				Port:           port,
				Fit:            false,
				Layers:         LayerCount(nglAllGPU),
				ContextSize:    32768,
				Parallel:       DefaultParallel,
				ImageMaxTokens: imageMaxTokensCapped,
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "off",
				"-ngl", "99",
				"-fa", "on",
				"-c", "32768",
				"-np", "1",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
				"--mmproj", mmproj,
				"--image-max-tokens", "1024",
			},
		},
		{
			name: "fit-with-target",
			spec: LaunchSpec{
				ServerBinary:  binary,
				ModelFile:     model,
				MMProjFile:    mmproj,
				Host:          LoopbackHost,
				Port:          port,
				Fit:           true,
				FitTargetMiB:  2048,
				FitMinContext: DefaultFitMinContext,
				Layers:        LayerAuto(),
				ContextSize:   0,
				KVType:        KVTypeQ8_0,
				KVOffload:     boolPtr(true),
				MMProjOffload: boolPtr(true),
				Parallel:      DefaultParallel,
				CacheRAMMiB:   intPtr(4096),
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "on",
				"-fitt", "2048",
				"-fitc", "65536",
				"-fa", "on",
				"-c", "0",
				"-ctk", "q8_0", "-ctv", "q8_0",
				"-np", "1",
				"--cache-ram", "4096",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
				"--mmproj", mmproj,
			},
		},
		{
			// The planner escalated the cache to q4_0 to fit the budget, and
			// moved it off the accelerator into system RAM.
			name: "q4-0-kv-without-offload",
			spec: LaunchSpec{
				ServerBinary:   binary,
				ModelFile:      model,
				MMProjFile:     mmproj,
				Host:           LoopbackHost,
				Port:           port,
				Fit:            false,
				Layers:         LayerCount(nglAllGPU),
				ContextSize:    65536,
				KVType:         KVTypeQ4_0,
				KVOffload:      boolPtr(false),
				MMProjOffload:  boolPtr(true),
				Parallel:       DefaultParallel,
				ImageMaxTokens: imageMaxTokensCapped,
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "off",
				"-ngl", "99",
				"-fa", "on",
				"-c", "65536",
				"-ctk", "q4_0", "-ctv", "q4_0",
				"-nkvo",
				"-np", "1",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
				"--mmproj", mmproj,
				"--image-max-tokens", "1024",
			},
		},
		{
			// A CPU-only launch: nothing on the accelerator, the projector's
			// reserve in host RAM, the prompt cache disabled, and one slot.
			name: "cpu-with-prompt-cache-off",
			spec: LaunchSpec{
				ServerBinary:   binary,
				ModelFile:      model,
				MMProjFile:     mmproj,
				Host:           LoopbackHost,
				Port:           port,
				Fit:            false,
				Layers:         LayerCPU(),
				ContextSize:    16384,
				KVType:         KVTypeF16,
				KVOffload:      boolPtr(true),
				MMProjOffload:  boolPtr(false),
				Parallel:       1,
				CacheRAMMiB:    intPtr(0),
				ImageMaxTokens: imageMaxTokensCapped,
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "off",
				"-ngl", "0",
				"-fa", "on",
				"-c", "16384",
				"-ctk", "f16", "-ctv", "f16",
				"-np", "1",
				"--cache-ram", "0",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
				"--mmproj", mmproj,
				"--no-mmproj-offload",
				"--image-max-tokens", "1024",
			},
		},
		{
			// A multi-device launch: naming the devices and the split mode is
			// pinning the offload, so fit is off and `-ngl all` is explicit.
			name: "multi-device-explicit-split",
			spec: LaunchSpec{
				ServerBinary:   binary,
				ModelFile:      model,
				MMProjFile:     mmproj,
				Host:           LoopbackHost,
				Port:           port,
				Fit:            false,
				Layers:         LayerAll(),
				ContextSize:    32768,
				KVType:         KVTypeF16,
				KVOffload:      boolPtr(true),
				MMProjOffload:  boolPtr(true),
				Parallel:       DefaultParallel,
				CacheRAMMiB:    intPtr(cacheRAMNoLimit),
				Devices:        []string{"CUDA0", "CUDA1"},
				SplitMode:      SplitModeRow,
				ImageMaxTokens: ImageMaxTokensUncapped,
			},
			want: []string{
				"-m", model,
				"--host", "127.0.0.1",
				"--port", "4321",
				"-fit", "off",
				"-ngl", "all",
				"-sm", "row",
				"-dev", "CUDA0,CUDA1",
				"-fa", "on",
				"-c", "32768",
				"-ctk", "f16", "-ctv", "f16",
				"-np", "1",
				"--cache-ram", "-1",
				"--temp", "1.0",
				"--top-p", "0.95",
				"--top-k", "20",
				"--jinja",
				"--no-ui",
				"--mmproj", mmproj,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.spec.Validate(); err != nil {
				t.Fatalf("the golden spec must be launchable: %v", err)
			}
			got := tc.spec.Args()
			if !slices.Equal(got, tc.want) {
				t.Errorf("args =\n  %v\nwant\n  %v", got, tc.want)
			}
			// No element outside the declared flag vocabulary, in any shape.
			requireClosedFlagVocabulary(t, got)

			// The invariants that hold for EVERY shape, restated here so a
			// golden cannot quietly drop one.
			requireArgs(t, got, "--host", LoopbackHost)
			requireFlag(t, got, "--no-ui")
			requireNoFlag(t, got, "--no-webui")
			requireNoFlag(t, got, "--agent")
			requireNoFlag(t, got, "--tools")
			requireNoFlag(t, got, "--cors-origins")
			// `-fit` is never inherited from the binary's default.
			if value, ok := argValue(got, "-fit"); !ok || (value != "on" && value != "off") {
				t.Errorf("-fit = %q (present: %v), want an explicit on or off", value, ok)
			}
			// `-np` is never left to the runtime's auto default: it is rendered
			// for every shape and always carries the spec's own slot count, so
			// the runtime's `-1` (auto) sentinel can never appear.
			np, ok := argValue(got, "-np")
			if !ok {
				t.Errorf("-np is missing, so the runtime's auto slot count would apply: %v", got)
			} else if want := strconv.Itoa(tc.spec.Parallel); np != want {
				t.Errorf("-np = %q, want the spec's %q", np, want)
			} else if tc.spec.Parallel >= 1 && np == "-1" {
				t.Error("-np rendered the runtime's auto sentinel for a positive slot count")
			}
			// The `-ngl` element exists exactly when the mode says it does.
			_, wantLayers := tc.spec.Layers.arg()
			_, gotLayers := argValue(got, "-ngl")
			if wantLayers != gotLayers {
				t.Errorf("-ngl present = %v, want %v (mode %s)", gotLayers, wantLayers, tc.spec.Layers)
			}
		})
	}
}

// TestLaunchSpecAutoLayerModeOmitsTheFlag is the assertion the fit-exclusivity
// rule actually rests on: in auto mode there is no `-ngl` ELEMENT in argv at
// all — not `-ngl auto`, not `-ngl 0`, not `-ngl 99`. `--fit` adjusts only
// unset arguments and the fork's fit.cpp throws on an already-set one, so a
// value here would turn a fit-sized launch into an abort.
func TestLaunchSpecAutoLayerModeOmitsTheFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		mode      LayerMode
		wantValue string // "" means the flag must be absent
	}{
		{"auto omits the flag", LayerAuto(), ""},
		{"the zero value is auto", LayerMode{}, ""},
		{"all", LayerAll(), "all"},
		{"cpu", LayerCPU(), "0"},
		{"an exact count", LayerCount(37), "37"},
		{"zero as an exact count", LayerCount(0), "0"},
		{"the resolver's GPU value", LayerCount(nglAllGPU), "99"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec := goldenBase()
			spec.Layers = tc.mode
			// Auto is only launchable under fit, so the shape is completed
			// before rendering; the assertion is about the element, not the
			// validation.
			if !tc.mode.EmitsFlag() {
				spec.Fit = true
				spec.ContextSize = 0
				spec.FitMinContext = DefaultFitMinContext
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			args := spec.Args()

			if tc.wantValue == "" {
				requireNoFlag(t, args, "-ngl")
				// And no stray value that a parser could read as one: the
				// spellings `-ngl` would have taken must not appear as loose
				// elements either.
				for _, stray := range []string{"auto", "all"} {
					if slices.Contains(args, stray) {
						t.Errorf("argv carries a stray %q element: %v", stray, args)
					}
				}
			} else {
				requireArgs(t, args, "-ngl", tc.wantValue)
			}
			if got := tc.mode.EmitsFlag(); got != (tc.wantValue != "") {
				t.Errorf("EmitsFlag() = %v, want %v", got, tc.wantValue != "")
			}
		})
	}
}

// TestLaunchSpecFitIsNeverInheritedFromTheBinary pins R1: `-fit` is an
// undocumented fork contract, so the launch never relies on its default. Every
// rendered command line states the switch, including the zero value's.
func TestLaunchSpecFitIsNeverInheritedFromTheBinary(t *testing.T) {
	t.Parallel()

	specs := map[string]LaunchSpec{
		"the zero value":   {},
		"fit off":          goldenBase(),
		"fit on":           goldenFitSpec(),
		"the install path": goldenBase(),
		"every field populated": func() LaunchSpec {
			spec := goldenBase()
			spec.KVType = KVTypeQ4_0
			spec.KVOffload = boolPtr(false)
			spec.MMProjOffload = boolPtr(false)
			spec.CacheRAMMiB = intPtr(2048)
			spec.SplitMode = SplitModeLayer
			spec.Devices = []string{"MTL0"}
			return spec
		}(),
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			args := spec.Args()
			value, ok := argValue(args, "-fit")
			if !ok {
				t.Fatalf("-fit is missing, so the binary's own default would decide: %v", args)
			}
			want := "off"
			if spec.Fit {
				want = "on"
			}
			if value != want {
				t.Errorf("-fit = %q, want %q", value, want)
			}
			if got := spec.fitArg(); got != value {
				t.Errorf("fitArg() = %q but argv carries %q", got, value)
			}
		})
	}
}

// TestLaunchSpecArgsRenderTypedValuesOnly is the structural half of the
// "LaunchSpec.Args is the only place the command line exists" guarantee, and the
// ASI05 note in SECURITY.md: it reads server.go's own syntax tree and requires
// that every value reaching argv comes from a typed field, a fixed literal, or
// an integer rendering of one.
//
// Three things are asserted, and each one fails on a different mistake:
//
//   - Args contains no string concatenation at all, so no argv element can be
//     assembled out of two values;
//   - the only functions Args calls are the renderers listed below, so nothing
//     outside this vocabulary (a formatter, a config read, a lookup) can supply
//     an element;
//   - the set of LaunchSpec fields Args reads is EXACTLY the allow-list, and
//     every field of the struct is either on that list or is ServerBinary, which
//     is the exec target and not an argv element. A new field therefore cannot
//     start reaching the command line without a deliberate edit here.
func TestLaunchSpecArgsRenderTypedValuesOnly(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading server.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", source, 0)
	if err != nil {
		t.Fatalf("parsing server.go: %v", err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Args" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := fn.Recv.List[0].Type
		if star, isPtr := recv.(*ast.StarExpr); isPtr {
			recv = star.X
		}
		if ident, isIdent := recv.(*ast.Ident); isIdent && ident.Name == "LaunchSpec" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("server.go declares no (LaunchSpec).Args; the guard is guarding nothing")
	}

	// The rendering vocabulary Args is allowed to call.
	allowedCallees := map[string]string{
		"append":          "the argv builder",
		"strconv.Itoa":    "an integer field becomes digits and a sign, nothing else",
		"strings.Join":    "the validated device list becomes ONE element",
		"string":          "a closed enum's own spelling",
		"len":             "a presence test",
		"make":            "the argv slice's capacity hint",
		"spec.Layers.arg": "the LayerMode switch — the only place a mode becomes a value",
		"spec.fitArg":     "a two-branch choice between the literals on and off",
	}
	// Every field Args may read, and why reading it is safe.
	allowedFields := map[string]string{
		"ModelFile":      "a path from the install record",
		"MMProjFile":     "a path from the install record",
		"Host":           "pinned to LoopbackHost by Validate",
		"Port":           "an integer, bounds-checked by Validate",
		"ContextSize":    "an integer, bounds-checked by Validate",
		"ImageMaxTokens": "an integer, bounds-checked by Validate",
		"Fit":            "a bool, rendered from two literals",
		"FitTargetMiB":   "an integer, non-negative by Validate",
		"FitMinContext":  "an integer, positive under fit by Validate",
		"KVType":         "a closed enum, allow-listed by Validate",
		"KVOffload":      "a bool that selects a fixed switch",
		"MMProjOffload":  "a bool that selects a fixed switch",
		"Parallel":       "an integer, at least 1 by Validate",
		"CacheRAMMiB":    "an integer, at least the runtime's no-limit spelling",
		"Devices":        "validated token by token by validateDeviceName",
		"SplitMode":      "a closed enum, allow-listed by Validate",
		"Layers":         "a LayerMode, whose four values are the only ones constructible",
	}

	// Call-function expressions, so a method call is not also counted as a field
	// read of its own name.
	callFuns := map[ast.Expr]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		callFuns[call.Fun] = true
		return true
	})

	callees := map[string]int{}
	fields := map[string]int{}
	concatenations := 0
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BinaryExpr:
			if typed.Op == token.ADD {
				concatenations++
			}
		case *ast.CallExpr:
			callees[calleeName(typed.Fun)]++
		case *ast.SelectorExpr:
			if callFuns[typed] {
				return true
			}
			if ident, ok := typed.X.(*ast.Ident); ok && ident.Name == "spec" {
				fields[typed.Sel.Name]++
			}
		}
		return true
	})

	if concatenations != 0 {
		t.Errorf("Args contains %d string concatenation(s); an argv element must be one typed value, never two values spliced together",
			concatenations)
	}
	for name := range callees {
		if _, ok := allowedCallees[name]; !ok {
			t.Errorf("Args calls %s, which is outside the rendering vocabulary %v", name, sortedKeys(allowedCallees))
		}
	}
	// Non-vacuity: a guard that passes because Args stopped rendering anything
	// is worse than no guard.
	for _, required := range []string{"strconv.Itoa", "strings.Join", "string", "spec.Layers.arg", "spec.fitArg"} {
		if callees[required] == 0 {
			t.Errorf("Args no longer calls %s; either the rendering moved or this guard went stale", required)
		}
	}
	for name := range fields {
		if _, ok := allowedFields[name]; !ok {
			t.Errorf("Args reads spec.%s, which is not on the typed-field allow-list", name)
		}
	}
	for name := range allowedFields {
		if fields[name] == 0 {
			t.Errorf("spec.%s is allow-listed but Args never reads it; drop it from the list or restore the flag", name)
		}
	}

	// The ratchet: every field of the struct is accounted for.
	structType := reflect.TypeOf(LaunchSpec{})
	for i := range structType.NumField() {
		name := structType.Field(i).Name
		if _, listed := allowedFields[name]; listed {
			continue
		}
		if name == "ServerBinary" {
			// The exec target, not an argv element — and asserting Args does
			// NOT read it is the point.
			if fields[name] != 0 {
				t.Error("Args reads ServerBinary; the binary is the exec target and must never also be an argument")
			}
			continue
		}
		t.Errorf("LaunchSpec.%s is neither rendered by Args nor listed as the exec target; decide which, and say why it is safe", name)
	}
}

// TestLaunchSpecStringFieldsCannotSplitArgv is the behavioural half of the same
// guarantee. Args goes to exec directly and never through a shell, so the only
// thing a hostile string could do is become a second argv element — a flag the
// planner never chose. It cannot: each string field lands in exactly one element,
// and the set of dash-prefixed elements is identical to the benign baseline's.
// The four fields Validate allow-lists must additionally reject the payload.
func TestLaunchSpecStringFieldsCannotSplitArgv(t *testing.T) {
	t.Parallel()

	payloads := []string{
		"/tmp/m.gguf --cors-origins *",
		"--agent",
		"--ui",
		"--tools",
		"$(rm -rf /)",
		"a\n--no-ui-please",
		"--cache-ram 0 --port 1",
	}

	benign := goldenBase()
	benign.KVType = KVTypeF16
	benign.SplitMode = SplitModeLayer
	benign.Devices = []string{"CUDA0"}
	baseline := benign.Args()
	wantFlags := flagsOf(baseline)
	requireClosedFlagVocabulary(t, baseline)

	// The two path fields are the only strings Args copies into argv verbatim.
	// Each one is a flag's VALUE, so the assertion is positional: the payload
	// must replace exactly one element, in the same slot, preceded by the same
	// flag — one element in, one element out, nothing split and nothing added.
	pathFields := []struct {
		field  string
		flag   string
		benign string
		set    func(*LaunchSpec, string)
	}{
		{"ModelFile", "-m", benign.ModelFile, func(s *LaunchSpec, v string) { s.ModelFile = v }},
		{"MMProjFile", "--mmproj", benign.MMProjFile, func(s *LaunchSpec, v string) { s.MMProjFile = v }},
	}
	for _, tc := range pathFields {
		for _, payload := range payloads {
			spec := benign
			tc.set(&spec, payload)
			got := spec.Args()

			if len(got) != len(baseline) {
				t.Errorf("%s = %q produced %d argv elements, want %d: %v",
					tc.field, payload, len(got), len(baseline), got)
				continue
			}
			changed := -1
			for i := range got {
				if got[i] == baseline[i] {
					continue
				}
				if changed >= 0 {
					t.Errorf("%s = %q changed argv at both %d and %d: %v", tc.field, payload, changed, i, got)
					break
				}
				changed = i
			}
			if changed < 0 {
				t.Errorf("%s = %q did not reach argv at all: %v", tc.field, payload, got)
				continue
			}
			if got[changed] != payload {
				t.Errorf("%s: argv[%d] = %q, want the payload verbatim as ONE element", tc.field, changed, got[changed])
			}
			if baseline[changed] != tc.benign {
				t.Errorf("%s: the payload displaced argv[%d] = %q, not the benign %q",
					tc.field, changed, baseline[changed], tc.benign)
			}
			if got[changed-1] != tc.flag {
				t.Errorf("%s: the payload landed after %q rather than in %s's value slot: %v",
					tc.field, got[changed-1], tc.flag, got)
			}
			// And the command line still parses as the same flags: a payload in
			// a value slot is data, so the flag sequence is untouched and every
			// element still sits inside the declared vocabulary.
			if diff := flagDiff(wantFlags, flagsOf(got)); diff != "" {
				t.Errorf("%s = %q changed the flag positions: %s", tc.field, payload, diff)
			}
			requireClosedFlagVocabulary(t, got)
		}
	}

	// The allow-listed string fields refuse every payload outright, so none of
	// them can reach argv at all.
	validatedFields := map[string]func(*LaunchSpec, string){
		"Host":      func(s *LaunchSpec, v string) { s.Host = v },
		"KVType":    func(s *LaunchSpec, v string) { s.KVType = KVType(v) },
		"SplitMode": func(s *LaunchSpec, v string) { s.SplitMode = SplitMode(v) },
		"Devices":   func(s *LaunchSpec, v string) { s.Devices = []string{v} },
	}
	for name, set := range validatedFields {
		for _, payload := range payloads {
			spec := benign
			set(&spec, payload)
			if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
				t.Errorf("%s = %q: Validate = %v, want ErrLaunchSpecInvalid", name, payload, err)
			}
		}
	}

	// A device list is joined into ONE element, so a name carrying a comma
	// would smuggle a second device past the planner's decision. That is the
	// reason validateDeviceName exists, and it is refused rather than escaped.
	for _, name := range []string{"CUDA0,CUDA1", "CUDA0 --cors-origins *", "-CUDA0", "", "CUDA 0"} {
		spec := benign
		spec.Devices = []string{name}
		if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("-dev %q: Validate = %v, want ErrLaunchSpecInvalid", name, err)
		}
	}
	// A well-formed multi-device list is one element and validates.
	spec := benign
	spec.Devices = []string{"CUDA0", "CUDA1"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("a well-formed device list was refused: %v", err)
	}
	if value, ok := argValue(spec.Args(), "-dev"); !ok || value != "CUDA0,CUDA1" {
		t.Errorf("-dev = %q (present %v), want one joined element %q", value, ok, "CUDA0,CUDA1")
	}
}

// TestLaunchSpecValidateEnforcesFitExclusivity is plan.go's rule enforced at the
// last gate before exec, in both directions. The fork does not degrade the
// ambiguous shape — fit.cpp throws — so a spec that carries both is refused here
// instead of aborting at spawn time.
func TestLaunchSpecValidateEnforcesFitExclusivity(t *testing.T) {
	t.Parallel()

	reject := map[string]func(*LaunchSpec){
		"fit on with -ngl all":         func(s *LaunchSpec) { s.Layers = LayerAll() },
		"fit on with -ngl 0":           func(s *LaunchSpec) { s.Layers = LayerCPU() },
		"fit on with an exact -ngl":    func(s *LaunchSpec) { s.Layers = LayerCount(32) },
		"fit on with -dev":             func(s *LaunchSpec) { s.Devices = []string{"CUDA0"} },
		"fit on with -sm":              func(s *LaunchSpec) { s.SplitMode = SplitModeNone },
		"fit on with -sm and -dev":     func(s *LaunchSpec) { s.Devices = []string{"CUDA0"}; s.SplitMode = SplitModeRow },
		"fit on with no -fitc floor":   func(s *LaunchSpec) { s.FitMinContext = 0 },
		"fit on with a negative floor": func(s *LaunchSpec) { s.FitMinContext = -1 },
	}
	fitOn := goldenFitSpec()
	for name, mutate := range reject {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := fitOn
			mutate(&spec)
			if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
				t.Errorf("Validate = %v, want ErrLaunchSpecInvalid", err)
			}
		})
	}

	accept := map[string]func(*LaunchSpec){
		"the all-auto fit shape":   func(*LaunchSpec) {},
		"fit on with an exact -c":  func(s *LaunchSpec) { s.ContextSize = 32768 },
		"fit on with a fit target": func(s *LaunchSpec) { s.FitTargetMiB = 2048 },
		"fit on with q4_0":         func(s *LaunchSpec) { s.KVType = KVTypeQ4_0 },
		"fit on with -nkvo":        func(s *LaunchSpec) { s.KVOffload = boolPtr(false) },
		"fit on with 2 slots":      func(s *LaunchSpec) { s.Parallel = 2 },
		"fit off with -ngl all":    func(s *LaunchSpec) { s.Fit = false; s.Layers = LayerAll(); s.ContextSize = 32768 },
		"fit off with -dev and -sm": func(s *LaunchSpec) {
			s.Fit = false
			s.Layers = LayerAll()
			s.ContextSize = 32768
			s.Devices = []string{"CUDA0"}
			s.SplitMode = SplitModeRow
		},
		"fit off with a floor record": func(s *LaunchSpec) {
			// plan.go sets FitMinContext unconditionally, so a fit-off plan
			// carries a floor that Args omits. It is a record, not a dropped
			// decision, and refusing it would make the bridge lossy.
			s.Fit = false
			s.Layers = LayerCount(nglAllGPU)
			s.ContextSize = 32768
			s.FitMinContext = DefaultFitMinContext
		},
	}
	for name, mutate := range accept {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := fitOn
			mutate(&spec)
			if err := spec.Validate(); err != nil {
				t.Errorf("Validate = %v, want a launchable spec", err)
			}
		})
	}

	// The converse direction on its own: with fit off, an omitted -ngl leaves
	// the offload to the runtime's own `auto` default, which is decided by the
	// model file and not by any memory measurement.
	t.Run("fit off with no -ngl", func(t *testing.T) {
		t.Parallel()
		spec := goldenBase()
		spec.Layers = LayerAuto()
		if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("Validate = %v, want ErrLaunchSpecInvalid", err)
		}
	})
}

// TestLaunchSpecContextCeilingIsTheModelledMaximum pins the widened ceiling. The
// RAM-tiered ladder in resolve.go still stops at contextTierTop; what changed is
// that the last gate before exec no longer refuses the top of the modelled range,
// because a memory-aware plan can legitimately be held to a floor there. The
// negative half of the old rule is unchanged, and a zero context is accepted
// only when fit is on to give it a value.
func TestLaunchSpecContextCeilingIsTheModelledMaximum(t *testing.T) {
	t.Parallel()

	profile := mustProfile(t)
	if maxTrainingContext != profile.MaxContext {
		t.Fatalf("maxTrainingContext = %d but the pinned profile reports MaxContext = %d; Validate would enforce the wrong ceiling",
			maxTrainingContext, profile.MaxContext)
	}
	if maxTrainingContext <= contextTierTop {
		t.Fatalf("the ceiling %d is not above the resolver's top tier %d, so this test proves nothing",
			maxTrainingContext, contextTierTop)
	}

	fitOff := goldenBase()
	for _, ctx := range []int{8192, 16384, 32768, 65536, contextTierTop, contextTierTop * 2, maxTrainingContext} {
		spec := fitOff
		spec.ContextSize = ctx
		if err := spec.Validate(); err != nil {
			t.Errorf("context %d: Validate = %v, want it accepted", ctx, err)
		}
	}
	for _, ctx := range []int{maxTrainingContext + 1, maxTrainingContext * 2, 1 << 20} {
		spec := fitOff
		spec.ContextSize = ctx
		if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("context %d: Validate = %v, want ErrLaunchSpecInvalid", ctx, err)
		}
	}
	// Negative is refused under BOTH fit settings.
	for _, fit := range []bool{false, true} {
		for _, ctx := range []int{-1, -65536} {
			spec := fitOff
			spec.Fit = fit
			spec.ContextSize = ctx
			if fit {
				spec.Layers = LayerAuto()
				spec.FitMinContext = DefaultFitMinContext
			}
			if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
				t.Errorf("fit=%v context %d: Validate = %v, want ErrLaunchSpecInvalid", fit, ctx, err)
			}
		}
	}
	// Zero means "fit sizes it": refused with fit off, accepted with fit on and
	// rendered as an explicit zero element.
	spec := fitOff
	spec.ContextSize = 0
	if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
		t.Errorf("a zero context with -fit off: Validate = %v, want ErrLaunchSpecInvalid", err)
	}
	fitSized := goldenFitSpec()
	if err := fitSized.Validate(); err != nil {
		t.Fatalf("the fit-sized spec is invalid: %v", err)
	}
	if value, ok := argValue(fitSized.Args(), "-c"); !ok || value != "0" {
		t.Errorf("-c = %q (present %v), want an explicit zero so fit sizes it", value, ok)
	}

	// Every tier the resolver can emit stays launchable.
	for _, ram := range []float64{16, 20, 24, 32, 48, 72, 128} {
		tier := fitOff
		tier.ContextSize = contextSizeFor(ram)
		if err := tier.Validate(); err != nil {
			t.Errorf("context %d (from %.0f GiB): Validate: %v", tier.ContextSize, ram, err)
		}
	}
}

// TestLaunchSpecRejectsUnmodelledKVTypes is the allow-list half of the gate:
// `-ctk`/`-ctv` render only a member of memory.go's closed three-precision set,
// and an empty value omits both flags so the runtime's own f16 default applies.
func TestLaunchSpecRejectsUnmodelledKVTypes(t *testing.T) {
	t.Parallel()

	spec := goldenBase()
	spec.KVType = ""
	if err := spec.Validate(); err != nil {
		t.Fatalf("an empty KVType must omit the flags, not fail: %v", err)
	}
	requireNoFlag(t, spec.Args(), "-ctk")
	requireNoFlag(t, spec.Args(), "-ctv")

	for _, kv := range KVTypes() {
		precise := spec
		precise.KVType = kv
		if err := precise.Validate(); err != nil {
			t.Errorf("KVType %q: Validate = %v, want it accepted", kv, err)
			continue
		}
		args := precise.Args()
		requireArgs(t, args, "-ctk", string(kv))
		requireArgs(t, args, "-ctv", string(kv))
		// One precision for both halves: memory.go documents that a mixed pair
		// silently drops to CPU until the target context fits.
		k, _ := argValue(args, "-ctk")
		v, _ := argValue(args, "-ctv")
		if k != v {
			t.Errorf("-ctk %q and -ctv %q differ; a mixed pair silently drops to CPU", k, v)
		}
	}

	for _, unmodelled := range []string{
		"q5_0", "q4_1", "iq4_nl", "q5_1", "f32", "bf16", "auto", "F16 ",
		"--cors-origins", "-ctk f16 -ctv f16", "",
	} {
		if unmodelled == "" {
			continue // the empty value is the omit case, asserted above
		}
		bad := spec
		bad.KVType = KVType(unmodelled)
		if err := bad.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("KVType %q: Validate = %v, want ErrLaunchSpecInvalid", unmodelled, err)
		}
	}
}

// TestLaunchSpecRejectsUnusableBudgets covers the remaining numeric gates: one
// slot at minimum, the runtime's own no-limit spelling as the cache floor, and a
// non-negative fit margin.
func TestLaunchSpecRejectsUnusableBudgets(t *testing.T) {
	t.Parallel()

	base := goldenBase()
	if err := base.Validate(); err != nil {
		t.Fatalf("the baseline spec is invalid: %v", err)
	}

	cases := map[string]func(*LaunchSpec){
		"-np 0":              func(s *LaunchSpec) { s.Parallel = 0 },
		"-np negative":       func(s *LaunchSpec) { s.Parallel = -1 },
		"--cache-ram -2":     func(s *LaunchSpec) { s.CacheRAMMiB = intPtr(cacheRAMNoLimit - 1) },
		"-fitt negative":     func(s *LaunchSpec) { s.FitTargetMiB = -1 },
		"-sm unknown":        func(s *LaunchSpec) { s.SplitMode = SplitMode("diagonal") },
		"-sm injected":       func(s *LaunchSpec) { s.SplitMode = SplitMode("layer --agent") },
		"-ngl negative":      func(s *LaunchSpec) { s.Layers = LayerCount(-1) },
		"--image-max-tokens": func(s *LaunchSpec) { s.ImageMaxTokens = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := base
			mutate(&spec)
			if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
				t.Errorf("Validate = %v, want ErrLaunchSpecInvalid", err)
			}
		})
	}

	accept := map[string]func(*LaunchSpec){
		"-np 1":                func(s *LaunchSpec) { s.Parallel = 1 },
		"-np 4":                func(s *LaunchSpec) { s.Parallel = 4 },
		"--cache-ram 0":        func(s *LaunchSpec) { s.CacheRAMMiB = intPtr(0) },
		"--cache-ram no limit": func(s *LaunchSpec) { s.CacheRAMMiB = intPtr(cacheRAMNoLimit) },
		"--cache-ram omitted":  func(s *LaunchSpec) { s.CacheRAMMiB = nil },
		"-sm none":             func(s *LaunchSpec) { s.SplitMode = SplitModeNone },
		"-sm tensor":           func(s *LaunchSpec) { s.SplitMode = SplitModeTensor },
	}
	for name, mutate := range accept {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := base
			mutate(&spec)
			if err := spec.Validate(); err != nil {
				t.Errorf("Validate = %v, want it accepted", err)
			}
		})
	}

	// A disabled prompt cache is passed through verbatim, not dropped: 0 is a
	// choice, and nil is the omission.
	disabled := base
	disabled.CacheRAMMiB = intPtr(0)
	requireArgs(t, disabled.Args(), "--cache-ram", "0")
	omitted := base
	omitted.CacheRAMMiB = nil
	requireNoFlag(t, omitted.Args(), "--cache-ram")
}

// TestApplyMemoryPlanRendersEveryFlagBearingField is the bridge's totality
// ratchet. It reflects over MemoryPlan and requires every field to be either a
// documented non-flag (the arithmetic and the Notes trail) or one whose value
// changes the rendered argv — so a new planner knob cannot be added and then
// silently dropped on the way to the command line.
func TestApplyMemoryPlanRendersEveryFlagBearingField(t *testing.T) {
	t.Parallel()

	// MemoryPlan fields that name no runtime flag: the packing is a download
	// decision, GPUFamily is echoed for a support bundle, the two budgets and
	// the two projected footprints are the arithmetic the gate ran, and Notes is
	// the operator-facing trail.
	flagless := map[string]bool{
		"Packing":           true,
		"GPUFamily":         true,
		"DeviceBudgetMiB":   true,
		"HostBudgetMiB":     true,
		"ExpectedDeviceMiB": true,
		"ExpectedHostMiB":   true,
		"Notes":             true,
	}
	// One non-default value per field, chosen so it is visible in argv under at
	// least one of the two baselines below.
	variations := map[string]func(*MemoryPlan){
		"Fit":           func(p *MemoryPlan) { p.Fit = !p.Fit },
		"FitTargetMiB":  func(p *MemoryPlan) { p.FitTargetMiB = 2048 },
		"FitMinContext": func(p *MemoryPlan) { p.FitMinContext = contextTierTop },
		"Layers":        func(p *MemoryPlan) { p.Layers = intPtr(nglCPUOnly) },
		"ContextSize":   func(p *MemoryPlan) { p.ContextSize = 8192 },
		"KVType":        func(p *MemoryPlan) { p.KVType = KVTypeQ4_0 },
		"KVOffload":     func(p *MemoryPlan) { p.KVOffload = !p.KVOffload },
		"MMProjOffload": func(p *MemoryPlan) { p.MMProjOffload = !p.MMProjOffload },
		"Parallel":      func(p *MemoryPlan) { p.Parallel = 2 },
		"CacheRAMMiB":   func(p *MemoryPlan) { p.CacheRAMMiB = intPtr(4096) },
		"Devices":       func(p *MemoryPlan) { p.Devices = []string{"CUDA0"} },
		"SplitMode":     func(p *MemoryPlan) { p.SplitMode = SplitModeRow },
	}

	baselines := map[string]MemoryPlan{
		"fit off": func() MemoryPlan {
			layers := nglAllGPU
			return MemoryPlan{
				Fit:           false,
				FitMinContext: DefaultFitMinContext,
				Layers:        &layers,
				ContextSize:   32768,
				KVType:        KVTypeF16,
				KVOffload:     true,
				MMProjOffload: true,
				Parallel:      DefaultParallel,
			}
		}(),
		"fit on": func() MemoryPlan {
			return MemoryPlan{
				Fit:           true,
				FitMinContext: DefaultFitMinContext,
				Layers:        nil,
				ContextSize:   0,
				KVType:        KVTypeF16,
				KVOffload:     true,
				MMProjOffload: true,
				Parallel:      DefaultParallel,
			}
		}(),
	}

	structType := reflect.TypeOf(MemoryPlan{})
	checked := 0
	for i := range structType.NumField() {
		name := structType.Field(i).Name
		if flagless[name] {
			continue
		}
		vary, ok := variations[name]
		if !ok {
			t.Errorf("MemoryPlan.%s is neither a documented non-flag nor covered by a variation; ApplyMemoryPlan may be dropping it", name)
			continue
		}
		checked++

		visible := false
		for baselineName, baseline := range baselines {
			spec := goldenBase().ApplyMemoryPlan(baseline)
			// The applied baseline must itself stay launchable, so the bridge
			// cannot produce a spec Validate refuses.
			if err := spec.Validate(); err != nil {
				t.Errorf("%s baseline: Validate = %v", baselineName, err)
			}
			before := spec.Args()

			changed := baseline
			vary(&changed)
			after := goldenBase().ApplyMemoryPlan(changed).Args()

			// Unchanged is not an error on its own: the two fit knobs render
			// only under `-fit on`, so varying them against the fit-off
			// baseline legitimately changes nothing. The requirement is that
			// every flag-bearing field is visible under at least one baseline.
			if !slices.Equal(before, after) {
				visible = true
			}
		}
		if !visible {
			t.Errorf("MemoryPlan.%s changes nothing in argv under either baseline", name)
		}
	}
	if checked == 0 {
		t.Fatal("no MemoryPlan field was checked; the ratchet is vacuous")
	}

	// A plan straight out of the planner maps onto a launchable spec, and the
	// fit side of the exclusivity rule survives the crossing.
	profile := mustProfile(t)
	topology := probedTopology(t, PlatformDarwinARM64, 128,
		DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: 110100, FreeMiB: 110100})
	for _, tuning := range []Tuning{
		{},
		{Offload: Offload{Mode: OffloadAll}},
		{Offload: Offload{Mode: OffloadCPU}},
		{Offload: Offload{Mode: OffloadLayers, Layers: 16}},
		{KVType: KVTypeQ4_0},
		{KVOffload: boolPtr(false)},
		{MMProjOffload: boolPtr(false)},
		{CacheRAMMiB: intPtr(1024)},
		{FitEnabled: boolPtr(false)},
		{FitTargetMiB: intPtr(2048)},
		{Context: ContextTuning{Mode: ContextExact, Tokens: 16384}},
	} {
		plan, err := Plan(topology, profile, tuning, BackendMetal, GPUFamilyAppleSilicon)
		if err != nil {
			if errors.Is(err, ErrMemoryPlanInfeasible) || errors.Is(err, ErrTuningInvalid) {
				continue
			}
			t.Fatalf("Plan(%+v): %v", tuning, err)
		}
		spec := goldenBase().ApplyMemoryPlan(plan)
		if err := spec.Validate(); err != nil {
			t.Fatalf("Plan(%+v) produced an unlaunchable spec: %v", tuning, err)
		}
		args := spec.Args()
		// The exclusivity rule, restated on the argv the bridge produced.
		_, hasLayers := argValue(args, "-ngl")
		if hasLayers == (plan.Layers == nil) {
			t.Errorf("Plan(%+v): -ngl present = %v but plan.Layers = %v", tuning, hasLayers, plan.Layers)
		}
		if hasLayers && plan.Fit {
			t.Errorf("Plan(%+v): argv carries both -fit on and -ngl, the shape fit.cpp aborts on", tuning)
		}
		if value, _ := argValue(args, "-fit"); value != plan.FitArg() {
			t.Errorf("Plan(%+v): -fit = %q, want the plan's %q", tuning, value, plan.FitArg())
		}
		if plan.Parallel >= 1 {
			requireArgs(t, args, "-np", strconv.Itoa(plan.Parallel))
		}
	}
}

// calleeName renders a call's function expression as a dotted name.
func calleeName(expr ast.Expr) string {
	switch fn := expr.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return calleeName(fn.X) + "." + fn.Sel.Name
	default:
		return "<dynamic>"
	}
}

// sortedKeys lists a map's keys in order, for a stable failure message.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// argvValueFlags is the closed vocabulary of flags Args may render that consume
// the NEXT element as their value. argvSwitches is the closed vocabulary of the
// ones that take no value. Together they are every dash-prefixed element a
// c0wrk-spawned llama-server command line may contain, and
// requireClosedFlagVocabulary fails on anything else — which is what makes "no
// string field can inject a flag" a checkable claim rather than an argument.
//
// Both maps must stay in sync with Args. A flag added there and not here fails
// the golden shapes rather than slipping through.
var (
	argvValueFlags = map[string]bool{
		"-m": true, "--host": true, "--port": true,
		"-fit": true, "-fitt": true, "-fitc": true,
		"-ngl": true, "-sm": true, "-dev": true,
		"-fa": true, "-c": true, "-ctk": true, "-ctv": true,
		"-np": true, "--cache-ram": true,
		"--temp": true, "--top-p": true, "--top-k": true,
		"--mmproj": true, "--image-max-tokens": true,
	}
	argvSwitches = map[string]bool{
		"--jinja": true, "--no-ui": true,
		"-nkvo": true, "--no-mmproj-offload": true,
	}
)

// flagsOf returns the elements of argv that sit in a FLAG POSITION: every flag
// literal, but never an element consumed as the value of the flag before it.
// That distinction is the whole question the injection test asks — a payload
// like "--agent" sitting in -m's value slot is data, and this is what proves it
// stays data.
func flagsOf(args []string) []string {
	flags := make([]string, 0, len(args))
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if argvValueFlags[arg] {
			flags = append(flags, arg)
			skipNext = true
			continue
		}
		if argvSwitches[arg] {
			flags = append(flags, arg)
		}
	}
	return flags
}

// requireClosedFlagVocabulary walks argv the way the runtime's parser does and
// fails on any element that is not a known flag or a known flag's value. A flag
// nobody declared cannot appear, whatever a string field was set to.
func requireClosedFlagVocabulary(t *testing.T, args []string) {
	t.Helper()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case argvValueFlags[arg]:
			if i+1 >= len(args) {
				t.Fatalf("%s is the last element, so it carries no value: %v", arg, args)
			}
			i++ // the value is data by definition
		case argvSwitches[arg]:
		default:
			if strings.HasPrefix(arg, "-") {
				t.Fatalf("argv element %d is %q, a dash-prefixed element outside the declared flag vocabulary: %v",
					i, arg, args)
			}
			t.Fatalf("argv element %d is %q, which is in no flag's value position: %v", i, arg, args)
		}
	}
}

// flagDiff reports the difference between two flag sequences, or "" when they
// are identical.
func flagDiff(want, got []string) string {
	if slices.Equal(want, got) {
		return ""
	}
	return fmt.Sprintf("want %v, got %v", want, got)
}

// goldenBase is a valid, fully-populated fit-OFF spec: the shape
// Server.launchSpec produces for a GPU-backed install, plus the vision
// projector and an explicit KV precision. Every test above mutates it, so a
// failure names the field that broke rather than the whole command line.
func goldenBase() LaunchSpec {
	return LaunchSpec{
		ServerBinary:   "/tmp/runtimes/llama-x/build/bin/llama-server",
		ModelFile:      "/tmp/models/bonsai.gguf",
		MMProjFile:     "/tmp/models/mmproj.gguf",
		Host:           LoopbackHost,
		Port:           4321,
		Fit:            false,
		Layers:         LayerCount(nglAllGPU),
		ContextSize:    32768,
		KVType:         KVTypeF16,
		KVOffload:      boolPtr(true),
		MMProjOffload:  boolPtr(true),
		Parallel:       DefaultParallel,
		ImageMaxTokens: imageMaxTokensCapped,
	}
}

// goldenFitSpec is the same launch handed to the runtime's own sizing pass: fit
// on, no `-ngl` element, a zero `-c` for fit to adjust, and the floor it is held
// to. It is the exclusivity rule's first branch, and the shape
// ApplyMemoryPlan produces for an all-auto Tuning.
func goldenFitSpec() LaunchSpec {
	spec := goldenBase()
	spec.Fit = true
	spec.Layers = LayerAuto()
	spec.ContextSize = 0
	spec.FitMinContext = DefaultFitMinContext
	spec.Devices = nil
	spec.SplitMode = SplitModeAuto
	return spec
}

// TestLaunchSpecOmitsOptionalFlags covers the flags that must disappear rather
// than be passed a sentinel: an absent projector, an uncapped image budget
// (CUDA/ROCm), an unset KV precision, and the three pointer knobs whose nil
// means "the runtime's own default".
func TestLaunchSpecOmitsOptionalFlags(t *testing.T) {
	t.Parallel()

	spec := LaunchSpec{
		ServerBinary:   "/bin/llama-server",
		ModelFile:      "/models/bonsai.gguf",
		Host:           LoopbackHost,
		Port:           4321,
		Fit:            false,
		Layers:         LayerCount(nglAllGPU),
		ContextSize:    65536,
		Parallel:       DefaultParallel,
		ImageMaxTokens: ImageMaxTokensUncapped,
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	args := spec.Args()
	requireNoFlag(t, args, "--mmproj")
	requireNoFlag(t, args, "--image-max-tokens")
	requireNoFlag(t, args, "-ctk")
	requireNoFlag(t, args, "-ctv")
	requireNoFlag(t, args, "-nkvo")
	requireNoFlag(t, args, "--no-mmproj-offload")
	requireNoFlag(t, args, "--cache-ram")
	requireNoFlag(t, args, "-fitt")
	requireNoFlag(t, args, "-fitc")
	requireNoFlag(t, args, "-sm")
	requireNoFlag(t, args, "-dev")
	requireFlag(t, args, "--no-ui")
	requireArgs(t, args, "-c", "65536")
}

// TestLaunchSpecAlwaysLoopbackAndNeverAgentFacing is the security pin (ADR-066
// D12): every rendered command line binds loopback, disables the Web UI, and
// never enables the server's own agent surface.
func TestLaunchSpecAlwaysLoopbackAndNeverAgentFacing(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{
		BackendCPU, BackendMetal, BackendVulkan, BackendROCm,
		BackendCUDA124, BackendCUDA128, BackendCUDA133,
	} {
		for _, platform := range []string{
			PlatformDarwinAMD64, PlatformDarwinARM64,
			PlatformLinuxAMD64, PlatformLinuxARM64, PlatformWindowsAMD64,
		} {
			spec := LaunchSpec{
				ServerBinary:   "/bin/" + ServerBinaryName,
				ModelFile:      "/models/bonsai.gguf",
				Host:           LoopbackHost,
				Port:           4321,
				Fit:            false,
				Layers:         LayerCount(layersFor(platform, backend)),
				ContextSize:    contextSizeFor(32),
				Parallel:       DefaultParallel,
				ImageMaxTokens: imageMaxTokensFor(backend),
			}
			if err := spec.Validate(); err != nil {
				t.Errorf("%s/%s: Validate: %v", platform, backend, err)
				continue
			}
			// The same machine handed to the runtime's own sizing pass. Both
			// shapes must carry the same security posture.
			fitSized := spec.ApplyMemoryPlan(MemoryPlan{
				Fit:           true,
				Layers:        nil,
				ContextSize:   0,
				FitMinContext: DefaultFitMinContext,
				KVType:        KVTypeF16,
				KVOffload:     true,
				MMProjOffload: true,
				Parallel:      DefaultParallel,
			})
			if err := fitSized.Validate(); err != nil {
				t.Errorf("%s/%s fit-sized: Validate: %v", platform, backend, err)
				continue
			}
			for _, args := range [][]string{spec.Args(), fitSized.Args()} {
				requireArgs(t, args, "--host", LoopbackHost)
				requireFlag(t, args, "--no-ui")
				// The legacy alias must be gone, not merely accompanied: the
				// pinned fork accepts both spellings, so a stale --no-webui would
				// keep working and hide the migration.
				requireNoFlag(t, args, "--no-webui")
				requireNoFlag(t, args, "--agent")
				requireNoFlag(t, args, "-ag")
				requireNoFlag(t, args, "--webui")
				requireNoFlag(t, args, "--ui")
				requireNoFlag(t, args, "--tools")
				requireNoFlag(t, args, "--cors-origins")
				requireNoFlag(t, args, "--mcp")
				// The bind is the loopback constant and nothing else.
				if value, _ := argValue(args, "--host"); value != LoopbackHost {
					t.Errorf("--host = %q, want %q", value, LoopbackHost)
				}
			}
		}
	}
}

// TestLaunchSpecRejectsOtherUnusableValues rounds out the validation gate: the
// identity and socket half, which the memory-plan tests above do not cover.
func TestLaunchSpecRejectsOtherUnusableValues(t *testing.T) {
	t.Parallel()

	base := goldenBase()
	if err := base.Validate(); err != nil {
		t.Fatalf("the baseline spec is invalid: %v", err)
	}

	cases := map[string]func(spec *LaunchSpec){
		"no binary":         func(spec *LaunchSpec) { spec.ServerBinary = "" },
		"no model":          func(spec *LaunchSpec) { spec.ModelFile = "" },
		"non-loopback":      func(spec *LaunchSpec) { spec.Host = "0.0.0.0" },
		"empty host":        func(spec *LaunchSpec) { spec.Host = "" },
		"port zero":         func(spec *LaunchSpec) { spec.Port = 0 },
		"privileged port":   func(spec *LaunchSpec) { spec.Port = 80 },
		"port out of range": func(spec *LaunchSpec) { spec.Port = 70000 },
		"negative layers":   func(spec *LaunchSpec) { spec.Layers = LayerCount(-1) },
		"negative images":   func(spec *LaunchSpec) { spec.ImageMaxTokens = -1 },
	}
	for name, mutate := range cases {
		spec := base
		mutate(&spec)
		if err := spec.Validate(); !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("%s: Validate = %v, want ErrLaunchSpecInvalid", name, err)
		}
	}
}

// TestLaunchSpecMatchesTheResolution is the cross-check against step_3's pure
// policy: for each backend the flags the supervisor renders are exactly what
// Resolve decided for that machine, and the context comes from the install
// record rather than from a fresh probe.
func TestLaunchSpecMatchesTheResolution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		platform    string
		backend     Backend
		packing     Packing
		hostOS      string
		ramGiB      float64
		wantLayers  string
		wantImageTx string // "" means the flag must be absent
	}{
		{PlatformDarwinARM64, BackendMetal, PackingPQ2_0, "darwin", 32, "99", "1024"},
		{PlatformDarwinAMD64, BackendCPU, PackingPQ2_0, "darwin", 32, "0", "1024"},
		{PlatformLinuxAMD64, BackendCUDA128, PackingPQ2_0, "linux", 72, "99", ""},
		{PlatformLinuxAMD64, BackendROCm, PackingPQ2_0, "linux", 24, "99", ""},
		{PlatformLinuxAMD64, BackendVulkan, PackingPTQ1_0, "linux", 24, "99", "1024"},
		{PlatformLinuxARM64, BackendCPU, PackingPQ2_0, "linux", 20, "0", "1024"},
		{PlatformWindowsAMD64, BackendCUDA124, PackingPQ2_0, "windows", 32, "99", ""},
	}

	for _, tc := range cases {
		name := tc.platform + "/" + string(tc.backend)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The resolution step_3 would have produced for this machine. The RAM
			// here only feeds the context tier, which the manifest already froze.
			resolution, err := ResolveMachine(tc.platform, tc.backend, tc.ramGiB)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			fx := newFixture(t, fixtureOptions{
				backend:     tc.backend,
				packing:     tc.packing,
				platform:    tc.platform,
				hostOS:      tc.hostOS,
				contextSize: resolution.ContextSize,
			})
			launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
			if err != nil {
				t.Fatalf("launchSpec: %v", err)
			}
			// The fixture writes a plan-less manifest, so this is the legacy
			// derivation: the pure policy in resolve.go, not a recorded plan.
			if launch.Plan != nil {
				t.Errorf("a plan-less manifest produced a plan: %+v", *launch.Plan)
			}
			spec := launch.Spec
			args := spec.Args()

			requireArgs(t, args, "-ngl", tc.wantLayers)
			requireArgs(t, args, "-c", strconv.Itoa(resolution.ContextSize))
			requireArgs(t, args, "--host", LoopbackHost)
			requireArgs(t, args, "-m", fx.manifest.ModelFile)
			if tc.wantImageTx == "" {
				requireNoFlag(t, args, "--image-max-tokens")
			} else {
				requireArgs(t, args, "--image-max-tokens", tc.wantImageTx)
			}
			if got, _ := argValue(args, "--mmproj"); got == "" {
				t.Error("the projector is installed, so --mmproj must be passed")
			}

			// The whole command line the spawner would have received.
			if err := fx.srv.Load(context.Background()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			cmd := fx.spawner.lastCommand()
			if strings.Join(cmd.Args, " ") != strings.Join(args, " ") {
				t.Errorf("spawned args =\n  %v\nwant\n  %v", cmd.Args, args)
			}
			if cmd.Binary != spec.ServerBinary {
				t.Errorf("spawned binary = %q, want %q", cmd.Binary, spec.ServerBinary)
			}
			if !strings.HasSuffix(cmd.Binary, binaryNameFor(tc.hostOS)) {
				t.Errorf("spawned binary %q does not carry the %s suffix", cmd.Binary, tc.hostOS)
			}
		})
	}
}

func binaryNameFor(goos string) string {
	if goos == "windows" {
		return ServerBinaryName + ".exe"
	}
	return ServerBinaryName
}

// TestLaunchSpecToleratesAMissingProjector checks the degradation: no vision is
// better than no model.
func TestLaunchSpecToleratesAMissingProjector(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{noMMProj: true})
	launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	spec := launch.Spec
	if spec.MMProjFile != "" {
		t.Errorf("MMProjFile = %q for an install without a projector", spec.MMProjFile)
	}
	requireNoFlag(t, spec.Args(), "--mmproj")
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Errorf("Load without a projector: %v", err)
	}
}

// TestLaunchSpecRefusesACorruptContext checks the manifest whose context tier is
// unusable: the supervisor refuses rather than letting the server pick its own.
func TestLaunchSpecRefusesACorruptContext(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{corruptContext: true})
	err := fx.srv.Load(context.Background())
	if !errors.Is(err, ErrLaunchSpecInvalid) {
		t.Fatalf("Load error = %v, want ErrLaunchSpecInvalid", err)
	}
	if n := fx.spawner.spawnCount(); n != 0 {
		t.Errorf("%d process(es) were spawned with an unusable context", n)
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
}

// ── acceptance: environment and port ──

// TestLaunchEnvPutsTheRuntimeOnTheLibraryPath checks the environment contract:
// the runtime's own directory is where the loader looks first, on every platform,
// and the inherited value is preserved behind it.
func TestLaunchEnvPutsTheRuntimeOnTheLibraryPath(t *testing.T) {
	t.Parallel()

	const binaryDir = "/home/u/.c0wrk/runtimes/llama-x/build/bin"
	cases := []struct {
		goos string
		key  string
		sep  string
	}{
		{"linux", "LD_LIBRARY_PATH", ":"},
		{"darwin", "DYLD_LIBRARY_PATH", ":"},
		{"windows", "PATH", ";"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()

			withExisting := launchEnv(binaryDir, tc.goos,
				[]string{"HOME=/home/u", tc.key + "=/opt/other", "LANG=C"})
			want := tc.key + "=" + binaryDir + tc.sep + "/opt/other"
			if !contains(want, withExisting) {
				t.Errorf("env = %v, want it to contain %q", withExisting, want)
			}
			if !contains("HOME=/home/u", withExisting) {
				t.Errorf("the parent environment was dropped: %v", withExisting)
			}
			if countKey(withExisting, tc.key) != 1 {
				t.Errorf("%s appears more than once: %v", tc.key, withExisting)
			}

			without := launchEnv(binaryDir, tc.goos, []string{"HOME=/home/u"})
			if !contains(tc.key+"="+binaryDir, without) {
				t.Errorf("env = %v, want it to contain %q", without, tc.key+"="+binaryDir)
			}

			// Case-insensitive match, because Windows env names are.
			mixed := launchEnv(binaryDir, tc.goos,
				[]string{strings.ToUpper(tc.key) + "=/opt/other"})
			if countKey(mixed, tc.key) != 1 {
				t.Errorf("a differently-cased %s survived: %v", tc.key, mixed)
			}
		})
	}

	if got := launchEnv("", "linux", []string{"A=B"}); len(got) != 1 || got[0] != "A=B" {
		t.Errorf("an empty binary dir must leave the environment alone: %v", got)
	}
}

func contains(want string, got []string) bool {
	for _, entry := range got {
		if entry == want {
			return true
		}
	}
	return false
}

func countKey(env []string, key string) int {
	n := 0
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, key) {
			n++
		}
	}
	return n
}

// TestSpawnedEnvironmentAndWorkDir checks what the spawner actually receives.
func TestSpawnedEnvironmentAndWorkDir(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{hostOS: "linux", platform: PlatformLinuxAMD64})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cmd := fx.spawner.lastCommand()
	binaryDir := filepath.Dir(cmd.Binary)
	if cmd.Dir != binaryDir {
		t.Errorf("working dir = %q, want the runtime's binary dir %q", cmd.Dir, binaryDir)
	}
	if !contains("LD_LIBRARY_PATH="+binaryDir, cmd.Env) {
		t.Errorf("the spawned environment does not point the loader at %q: %v", binaryDir, cmd.Env)
	}
	if len(cmd.Env) < 2 {
		t.Errorf("the parent environment was not inherited: %v", cmd.Env)
	}

	// The command line that actually reaches exec, not just the one Args()
	// renders in isolation.
	requireArgs(t, cmd.Args, "--host", LoopbackHost)
	requireArgs(t, cmd.Args, "--port", strconv.Itoa(fx.manifest.Port))
	requireArgs(t, cmd.Args, "-m", fx.manifest.ModelFile)
	requireArgs(t, cmd.Args, "-fa", "on")
	requireFlag(t, cmd.Args, "--no-ui")
	requireNoFlag(t, cmd.Args, "--no-webui")
	requireFlag(t, cmd.Args, "--jinja")
	requireNoFlag(t, cmd.Args, "--agent")
	if cmd.Args[0] != "-m" {
		t.Errorf("args = %v, want the model file first", cmd.Args)
	}
}

// TestEnsurePortIsConsultedBeforeTheSpawn covers the port seam: whatever it
// returns is what gets bound and probed, so a collision fix-up reaches the
// command line.
func TestEnsurePortIsConsultedBeforeTheSpawn(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	replacement := fx.endpoint.port
	seen := make(chan int, 1)
	fx.srv.EnsurePort = func(_ context.Context, port int) (int, error) {
		seen <- port
		return replacement, nil
	}

	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := <-seen; got != fx.manifest.Port {
		t.Errorf("EnsurePort saw port %d, want the persisted %d", got, fx.manifest.Port)
	}
	requireArgs(t, fx.spawner.lastCommand().Args, "--port", strconv.Itoa(replacement))
	if got := fx.srv.Port(); got != replacement {
		t.Errorf("Port() = %d, want %d", got, replacement)
	}
}

// TestEnsurePortFailureIsFatal checks that a port that cannot be prepared stops
// the load before a process exists.
func TestEnsurePortFailureIsFatal(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	fx.srv.EnsurePort = func(_ context.Context, _ int) (int, error) {
		return 0, errors.New("no free loopback port")
	}

	err := fx.srv.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no free loopback port") {
		t.Fatalf("Load error = %v, want the port failure", err)
	}
	if n := fx.spawner.spawnCount(); n != 0 {
		t.Errorf("%d process(es) were spawned without a usable port", n)
	}
	if got := fx.srv.State(); got != StateError {
		t.Errorf("state = %s, want %s", got, StateError)
	}
}

// ── acceptance: stop ──

// TestUnloadStopsTheProcessGracefully covers the normal path: one signal, a clean
// exit, and the state back to installed — the bytes stay, the RAM is returned.
func TestUnloadStopsTheProcessGracefully(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	ctx := context.Background()
	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	if err := fx.srv.Unload(ctx); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	if proc.alive() {
		t.Error("the process is still running after Unload")
	}
	if got := proc.killCount(); got != 0 {
		t.Errorf("the process was killed %d time(s); a clean exit needs no kill", got)
	}
	signals := proc.signalLog()
	if len(signals) != 1 || signals[0] != gracefulSignal() {
		t.Errorf("signals = %v, want exactly one %v", signals, gracefulSignal())
	}
	// installed IS the unloaded state: on disk, not resident.
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s", got, StateInstalled)
	}
	if got := fx.srv.Status().Pid; got != 0 {
		t.Errorf("Status().Pid = %d after the unload, want 0", got)
	}
	if !containsState(fx.events.states(), StateUnloading) {
		t.Errorf("no unloading transition was emitted: %v", fx.events.states())
	}
	if err := fx.srv.Load(ctx); err != nil {
		t.Errorf("a reload after an unload: %v", err)
	}
}

// TestUnloadKillsAProcessThatIgnoresTheSignal covers the fallback half of D5.
func TestUnloadKillsAProcessThatIgnoresTheSignal(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{stopTimeout: 40 * time.Millisecond})
	fx.spawner.configure = func(proc *fakeProcess, _ LaunchCommand) {
		proc.ignoreSignals = true
	}
	ctx := context.Background()
	if err := fx.srv.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	proc := fx.spawner.lastProcess()

	start := time.Now()
	if err := fx.srv.Unload(ctx); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("Unload returned in %s; the graceful window should have been honoured first", elapsed)
	}
	if got := proc.killCount(); got != 1 {
		t.Errorf("kills = %d, want 1", got)
	}
	if proc.alive() {
		t.Error("the process survived the kill")
	}
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s", got, StateInstalled)
	}
}

// TestUnloadWithoutAProcessIsANoOp checks idempotency on the other side: Remove
// and shutdown both call it unconditionally.
func TestUnloadWithoutAProcessIsANoOp(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	if err := fx.srv.Unload(context.Background()); err != nil {
		t.Fatalf("Unload with nothing running: %v", err)
	}
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s", got, StateInstalled)
	}
	if n := fx.spawner.spawnCount(); n != 0 {
		t.Errorf("Unload spawned %d process(es)", n)
	}
}

// TestUnloadDoesNotClaimAnInstallThatIsNotThere keeps the state machine honest:
// unloading cannot turn "not installed" into "installed".
func TestUnloadDoesNotClaimAnInstallThatIsNotThere(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	if err := fx.srv.SetInstalled(false); err != nil {
		t.Fatalf("SetInstalled: %v", err)
	}
	if err := fx.srv.Unload(context.Background()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if got := fx.srv.State(); got != StateNotInstalled {
		t.Errorf("state = %s, want %s", got, StateNotInstalled)
	}
}

// TestStopIsTheInstallerSeam pins the signature match that lets
// Installer.Stop = Server.Stop be wired without an adapter.
func TestStopIsTheInstallerSeam(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	// Assigning through the Installer's own seam field is the type-level proof:
	// this compiles only if the signatures match exactly, so Remove and shutdown
	// need no adapter.
	installer := &Installer{Layout: fx.layout}
	installer.Stop = fx.srv.Stop
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := installer.Stop(context.Background()); err != nil {
		t.Fatalf("Stop through the Installer seam: %v", err)
	}
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s", got, StateInstalled)
	}
	if proc := fx.spawner.lastProcess(); proc.alive() {
		t.Error("the process is still running after Stop")
	}
}

// TestSetInstalledRefusesWhileRunning keeps the state of a live process owned by
// the supervisor.
func TestSetInstalledRefusesWhileRunning(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{})
	if err := fx.srv.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := fx.srv.SetInstalled(false); !errors.Is(err, ErrServerBusy) {
		t.Errorf("SetInstalled(false) on a running server = %v, want ErrServerBusy", err)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s", got, StateLoaded)
	}
	if err := fx.srv.Unload(context.Background()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if err := fx.srv.SetInstalled(false); err != nil {
		t.Errorf("SetInstalled(false) on a stopped server: %v", err)
	}
	if got := fx.srv.State(); got != StateNotInstalled {
		t.Errorf("state = %s, want %s", got, StateNotInstalled)
	}
	if err := fx.srv.SetInstalled(true); err != nil {
		t.Errorf("SetInstalled(true): %v", err)
	}
	if got := fx.srv.State(); got != StateInstalled {
		t.Errorf("state = %s, want %s", got, StateInstalled)
	}
}

// ── small units ──

func TestLineTailKeepsTheLastLines(t *testing.T) {
	t.Parallel()

	tail := newLineTail(3)
	for i := range 5 {
		tail.add(fmt.Sprintf("line %d", i))
	}
	want := "line 2\nline 3\nline 4"
	if got := tail.String(); got != want {
		t.Errorf("tail = %q, want %q", got, want)
	}

	var nilTail *lineTail
	nilTail.add("ignored")
	if got := nilTail.String(); got != "" {
		t.Errorf("a nil tail = %q, want empty", got)
	}
	if got := newLineTail(0).limit; got != tailLines {
		t.Errorf("a zero limit = %d, want the default %d", got, tailLines)
	}
}

func TestProbeModelsReadiness(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status int
		body   string
		ready  bool
	}{
		"a served model":        {http.StatusOK, `{"object":"list","data":[{"id":"Bonsai 2 27B"}]}`, true},
		"an empty list":         {http.StatusOK, `{"object":"list","data":[]}`, false},
		"still loading":         {http.StatusServiceUnavailable, `{"error":"loading"}`, false},
		"200 but not a listing": {http.StatusOK, `<html>web ui</html>`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ready, err := probeModels(ctx, server.Client(), server.URL+"/v1/models")
			if ready != tc.ready {
				t.Errorf("ready = %v, want %v (err = %v)", ready, tc.ready, err)
			}
			if tc.ready && err != nil {
				t.Errorf("a ready probe returned an error: %v", err)
			}
		})
	}
}

func TestStateRunning(t *testing.T) {
	t.Parallel()

	for state, want := range map[State]bool{
		StateNotInstalled: false,
		StateInstalled:    false,
		StateLoading:      true,
		StateLoaded:       true,
		StateUnloading:    true,
		StateError:        false,
	} {
		if got := state.Running(); got != want {
			t.Errorf("%s.Running() = %v, want %v", state, got, want)
		}
	}
}

func TestServerDefaultsAreBounded(t *testing.T) {
	t.Parallel()

	layout, _ := testLayout(t)
	srv := NewServer(layout, nil)
	if got := srv.readyTimeout(); got != DefaultReadyTimeout {
		t.Errorf("readyTimeout = %s, want %s", got, DefaultReadyTimeout)
	}
	if got := srv.pollInterval(); got != DefaultReadyPollInterval {
		t.Errorf("pollInterval = %s, want %s", got, DefaultReadyPollInterval)
	}
	if got := srv.probeTimeout(); got != DefaultProbeTimeout {
		t.Errorf("probeTimeout = %s, want %s", got, DefaultProbeTimeout)
	}
	if got := srv.stopTimeout(); got != DefaultStopTimeout {
		t.Errorf("stopTimeout = %s, want %s", got, DefaultStopTimeout)
	}
	if got := srv.State(); got != StateNotInstalled {
		t.Errorf("a fresh server is %s, want %s", got, StateNotInstalled)
	}
	if got := srv.AutoUnloadPolicy(); got.Enabled != DefaultAutoUnloadEnabled ||
		got.Idle != DefaultAutoUnloadMinutes*time.Minute {
		t.Errorf("the default idle policy = %+v, want enabled/%d min", got, DefaultAutoUnloadMinutes)
	}
	if srv.httpClient() == nil {
		t.Error("no readiness client")
	}
	// The zero value must not deadlock on the lazily created gate.
	var zero Server
	if got := zero.State(); got != StateNotInstalled {
		t.Errorf("the zero value reports %s, want %s", got, StateNotInstalled)
	}
	if err := zero.Load(context.Background()); !errors.Is(err, ErrLayoutInvalid) {
		t.Errorf("the zero value Load = %v, want ErrLayoutInvalid", err)
	}
}

func TestNilServerIsTolerated(t *testing.T) {
	t.Parallel()

	var srv *Server
	if err := srv.Load(context.Background()); err == nil {
		t.Error("a nil Server accepted a Load")
	}
	if err := srv.Unload(context.Background()); err == nil {
		t.Error("a nil Server accepted an Unload")
	}
	if err := srv.SetInstalled(true); err == nil {
		t.Error("a nil Server accepted SetInstalled")
	}
	if got := srv.State(); got != StateNotInstalled {
		t.Errorf("a nil Server reports %s", got)
	}
	if got := srv.Port(); got != 0 {
		t.Errorf("a nil Server reports port %d", got)
	}
	if _, armed := srv.IdleRemaining(); armed {
		t.Error("a nil Server has an armed idle timer")
	}
	srv.MarkActivity()
	srv.SetAutoUnload(DefaultAutoUnload())
}

// ── the real process glue ──

// TestSpawnOSServerStartsARealProcess is the only test that runs the production
// spawn path: real exec, real pipes, a real graceful signal. The child is this
// test binary re-executed in helper mode (the pattern os/exec's own tests use), so
// it is portable and needs no llama-server on the machine.
func TestSpawnOSServerStartsARealProcess(t *testing.T) {
	t.Parallel()

	logs := &logCapture{}
	cmd := LaunchCommand{
		Binary: os.Args[0],
		Args:   []string{"-test.run=TestHelperServerProcess", "-test.v=false"},
		Env:    append(os.Environ(), helperProcessEnv+"=1"),
		Dir:    t.TempDir(),
	}

	proc, err := spawnOSServer(context.Background(), cmd, logs.logger())
	if err != nil {
		t.Fatalf("spawnOSServer: %v", err)
	}
	if proc.Pid() <= 0 {
		t.Errorf("Pid() = %d, want a real process id", proc.Pid())
	}

	if proc.Stdout() == nil || proc.Stderr() == nil {
		t.Fatal("the child's output streams were not captured")
	}

	// Reading the startup lines first is also the barrier: it proves the child is
	// up and has installed its signal handler before we ask it to stop.
	if got := readFirstLine(t, proc.Stdout(), "stdout"); got != helperStdoutLine {
		t.Errorf("stdout = %q, want %q", got, helperStdoutLine)
	}
	if got := readFirstLine(t, proc.Stderr(), "stderr"); got != helperStderrLine {
		t.Errorf("stderr = %q, want %q", got, helperStderrLine)
	}

	// A graceful stop, exactly as Unload performs it: signal first, and only a
	// platform without one (Windows) falls back to the kill. A clean exit is then
	// expected; after a kill, any exit status is.
	graceful := true
	if sigErr := proc.Signal(gracefulSignal()); sigErr != nil {
		graceful = false
		t.Logf("no graceful child signal on this platform (%v); killing instead", sigErr)
		if killErr := proc.Kill(); killErr != nil {
			t.Fatalf("Kill: %v", killErr)
		}
	}
	if err := proc.Wait(); err != nil && graceful {
		t.Errorf("Wait after a graceful stop = %v, want a clean exit", err)
	}
}

// readFirstLine reads one line from a child's stream, bounded so a helper that
// never writes cannot hang the suite.
func readFirstLine(t *testing.T, r io.Reader, stream string) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			lines <- scanner.Text()
			return
		}
		lines <- ""
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(30 * time.Second):
		t.Fatalf("the helper process wrote nothing on %s", stream)
		return ""
	}
}

// TestDefaultSpawnerRejectsAMissingBinary covers the production spawner seam
// itself: with no Spawn override, an unusable binary is an error rather than a
// hang.
func TestDefaultSpawnerRejectsAMissingBinary(t *testing.T) {
	t.Parallel()

	layout, _ := testLayout(t)
	srv := NewServer(layout, nil)
	if srv.Spawn != nil {
		t.Fatal("test setup: a fresh Server must default to the real spawner")
	}
	spawn := srv.spawner()

	proc, err := spawn(context.Background(), LaunchCommand{
		Binary: filepath.Join(t.TempDir(), "no-such-llama-server"),
		Args:   []string{"--version"},
	})
	if err == nil {
		if proc != nil {
			_ = proc.Kill()
		}
		t.Fatal("spawning a missing binary succeeded")
	}
	if proc != nil {
		t.Errorf("a failed spawn returned a process: %v", proc)
	}
}

// TestHelperServerProcess is not a test. It is the child process
// TestSpawnOSServerStartsARealProcess re-executes: it writes one line to each
// stream and then waits to be terminated, which is the shape of a server.
func TestHelperServerProcess(t *testing.T) {
	if os.Getenv(helperProcessEnv) != "1" {
		t.Skip("helper mode only; re-executed by TestSpawnOSServerStartsARealProcess")
	}
	_, _ = fmt.Fprintln(os.Stdout, helperStdoutLine)
	_, _ = fmt.Fprintln(os.Stderr, helperStderrLine)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, gracefulSignal(), os.Interrupt)
	select {
	case <-signals:
		os.Exit(0)
	case <-time.After(60 * time.Second):
		// The parent never stopped us: fail loudly rather than linger.
		os.Exit(1)
	}
}

const (
	helperProcessEnv = "EMBEDDEDLLM_HELPER_SERVER"
	helperStdoutLine = "helper: listening on 127.0.0.1"
	helperStderrLine = "helper: model loaded"
)

func containsState(states []State, want State) bool {
	for _, state := range states {
		if state == want {
			return true
		}
	}
	return false
}

// ── the recorded plan, the load-time refinement and the context readback ──

// stagedPlan is a memory-aware install record: an explicit offload with a
// planner-computed context, i.e. `-fit off` beside a pinned `-ngl`. It is
// deliberately a shape the legacy pure-policy derivation would NOT produce (a
// KV precision, a cache ceiling and a non-default layer count), so a test that
// sees it in argv knows the recorded plan was used.
func stagedPlan() MemoryPlan {
	layers := 12
	cacheRAM := 0
	return MemoryPlan{
		Fit:               false,
		FitMinContext:     DefaultFitMinContext,
		Layers:            &layers,
		ContextSize:       32768,
		KVType:            KVTypeQ8_0,
		Packing:           PackingPQ2_0,
		KVOffload:         false,
		MMProjOffload:     false,
		Parallel:          DefaultParallel,
		CacheRAMMiB:       &cacheRAM,
		GPUFamily:         GPUFamilyAppleSilicon,
		DeviceBudgetMiB:   24576,
		HostBudgetMiB:     8192,
		ExpectedDeviceMiB: 20000,
		ExpectedHostMiB:   4000,
		Notes:             []string{"staged"},
	}
}

// stagedTopology is the measurement stagedPlan was supposedly made from.
func stagedTopology() MemoryTopology {
	return MemoryTopology{
		Devices: []DeviceMemory{
			{Name: "MTL0", Description: "Apple M3 Pro", TotalMiB: 24576, FreeMiB: 24576},
		},
		HostRAMGiB:        36,
		Unified:           true,
		DeviceBudgetBytes: 24576 * bytesPerMiB,
		HostBudgetBytes:   8192 * bytesPerMiB,
		ProbedAt:          testInstallInstant.Format(time.RFC3339),
	}
}

// memoryAwareFixture stages a manifest carrying a plan and the topology that
// plan was made from, on top of an otherwise ordinary fixture.
func memoryAwareFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	plan := stagedPlan()
	topology := stagedTopology()
	opts.plan = &plan
	opts.topology = &topology
	// The recorded context must agree with the recorded plan, exactly as an
	// install would have written it, so a readback that agrees produces no
	// write and a test about the plan is not also a test about the context.
	opts.contextSize = plan.ContextSize
	return newFixture(t, opts)
}

// TestLoadLaunchesTheRecordedPlan is the core of the wiring: a memory-aware
// manifest drives the command line, so a load reproduces the shape the install
// decided instead of re-deriving a coarser one from the pure policy.
func TestLoadLaunchesTheRecordedPlan(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{})

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	args := fx.spawner.lastCommand().Args

	requireArgs(t, args, "-fit", "off")
	requireArgs(t, args, "-ngl", "12")
	requireArgs(t, args, "-c", "32768")
	requireArgs(t, args, "-ctk", string(KVTypeQ8_0))
	requireArgs(t, args, "-ctv", string(KVTypeQ8_0))
	requireArgs(t, args, "-np", strconv.Itoa(DefaultParallel))
	// `-fitc` is rendered ONLY under fit: a fit-off plan still carries the
	// planner's floor in FitMinContext as a record of the gate it ran, but
	// Args omits the flag because with fit off nothing reads it.
	requireNoFlag(t, args, "-fitc")
	// The staged plan turns BOTH offloads off, so both negations are rendered —
	// values the legacy derivation leaves at the runtime's defaults.
	requireFlag(t, args, "-nkvo")
	requireFlag(t, args, "--no-mmproj-offload")
	requireArgs(t, args, "--cache-ram", "0")

	// The identity half still comes from the install record.
	requireArgs(t, args, "-m", fx.manifest.ModelFile)
	requireArgs(t, args, "--host", LoopbackHost)
	requireArgs(t, args, "--port", strconv.Itoa(fx.manifest.Port))
}

// TestLoadDoesNotProbeWhenTheHookIsNotWired pins the pre-existing contract for a
// caller that does not opt in: with Server.ProbeDevices nil, no hardware probe
// runs at load time at all and the recorded plan is launched verbatim.
func TestLoadDoesNotProbeWhenTheHookIsNotWired(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{}) // probeDevices left nil

	launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	if launch.Refreshed {
		t.Error("an unwired probe reported a refinement")
	}
	if !reflect.DeepEqual(*launch.Plan, stagedPlan()) {
		t.Errorf("the applied plan is not the recorded one: %+v", *launch.Plan)
	}
	if !reflect.DeepEqual(*launch.Topology, stagedTopology()) {
		t.Errorf("the applied topology is not the recorded snapshot: %+v", *launch.Topology)
	}
}

// TestLoadFallsBackToTheRecordedPlanWhenTheDeviceProbeDoesNotAnswer is the
// acceptance criterion the whole fail-soft design exists for: a WEDGED or
// ABSENT driver query at load time must not cost the user their model. The load
// falls back to the manifest snapshot and still succeeds.
func TestLoadFallsBackToTheRecordedPlanWhenTheDeviceProbeDoesNotAnswer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		probe func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	}{
		{
			name: "the probe answers nothing",
			probe: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
				return MemoryTopology{}, false
			},
		},
		{
			name: "the probe wedges and is cut off by the short bound",
			probe: func(ctx context.Context, _ string, _ *slog.Logger) (MemoryTopology, bool) {
				// A driver query that never returns. LoadProbeTimeout is what
				// makes this a Debug line instead of a hung load.
				<-ctx.Done()
				return MemoryTopology{}, false
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fx := memoryAwareFixture(t, fixtureOptions{probeDevices: tc.probe})
			fx.srv.LoadProbeTimeout = 100 * time.Millisecond

			start := time.Now()
			if err := fx.srv.Load(t.Context()); err != nil {
				t.Fatalf("Load with a failing device probe: %v", err)
			}
			if tc.name == "the probe wedges and is cut off by the short bound" &&
				time.Since(start) > 30*time.Second {
				t.Errorf("a wedged probe was not bounded: Load took %s", time.Since(start))
			}

			// The load launched the RECORDED shape, not a degraded guess.
			args := fx.spawner.lastCommand().Args
			requireArgs(t, args, "-ngl", "12")
			requireArgs(t, args, "-c", "32768")
			requireArgs(t, args, "-ctk", string(KVTypeQ8_0))

			waitForState(t, fx.srv, StateLoaded)
		})
	}
}

// TestLoadFallsBackWhenTheFreshTopologyCannotBePlanned covers the third way the
// refinement can fail: the probe ANSWERS, but the planner refuses the machine it
// now describes. The recorded plan still launches — a load-time measurement is a
// refinement of a decision the install already made and the user already
// accepted, not a new gate with a new refusal.
func TestLoadFallsBackWhenTheFreshTopologyCannotBePlanned(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			// A machine that cannot hold the model: 1 GiB of everything. The
			// planner refuses it, and the refusal must not become a load
			// failure.
			return MemoryTopology{
				Devices: []DeviceMemory{
					{Name: "MTL0", Description: "Apple M3 Pro", TotalMiB: 512, FreeMiB: 512},
				},
				HostRAMGiB:        1,
				Unified:           true,
				DeviceBudgetBytes: 512 * bytesPerMiB,
				HostBudgetBytes:   256 * bytesPerMiB,
				ProbedAt:          testInstallInstant.Format(time.RFC3339),
			}, true
		},
	})

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load with an unplannable fresh topology: %v", err)
	}
	args := fx.spawner.lastCommand().Args
	requireArgs(t, args, "-ngl", "12")
	requireArgs(t, args, "-c", "32768")
	waitForState(t, fx.srv, StateLoaded)

	// The snapshot was NOT overwritten by a measurement nothing could be
	// planned from.
	onDisk := fx.diskManifest(t)
	if onDisk.Topology == nil || !reflect.DeepEqual(*onDisk.Topology, stagedTopology()) {
		t.Errorf("the recorded topology was replaced: %+v", onDisk.Topology)
	}
}

// TestLoadReplansFromAFreshTopology is the freshness the probe buys: a machine
// whose accelerator changed since the install is priced at launch, and the
// measurement that produced the new shape is the one recorded.
func TestLoadReplansFromAFreshTopology(t *testing.T) {
	t.Parallel()

	fresh := MemoryTopology{
		Devices: []DeviceMemory{
			{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: 24576},
		},
		HostRAMGiB:        128,
		Unified:           false,
		DeviceBudgetBytes: 24576 * bytesPerMiB,
		HostBudgetBytes:   112 * 1024 * bytesPerMiB,
		ProbedAt:          testInstallInstant.Add(time.Hour).Format(time.RFC3339),
	}
	fx := memoryAwareFixture(t, fixtureOptions{
		backend: BackendCUDA124,
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return fresh, true
		},
	})

	launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	if !launch.Refreshed {
		t.Fatal("a fresh topology produced no refinement")
	}
	if launch.Topology == nil || !reflect.DeepEqual(*launch.Topology, fresh) {
		t.Errorf("the applied topology is not the fresh measurement: %+v", launch.Topology)
	}
	if reflect.DeepEqual(*launch.Plan, stagedPlan()) {
		t.Error("the applied plan is still the recorded one, though the machine changed")
	}
	// The plan is priced for the card that is actually there.
	if launch.Plan.GPUFamily != GPUFamilyNVIDIAAda {
		t.Errorf("plan gpu family = %q, want %q", launch.Plan.GPUFamily, GPUFamilyNVIDIAAda)
	}
	if err := launch.Spec.Validate(); err != nil {
		t.Errorf("the re-planned spec is invalid: %v", err)
	}
}

// TestLoadReplanPinsTheInstalledPacking is the one input a load-time re-plan
// must not be allowed to re-decide. `Plan` derives a packing from the backend
// and GPU family whenever the tuning leaves it unset, and the operator's tuning
// may name another one entirely — but the weights on disk are the manifest's
// packing, and a plan priced for a different quantization would compute a
// footprint the installed GGUF does not have.
func TestLoadReplanPinsTheInstalledPacking(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{
		backend: BackendCUDA124,
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return MemoryTopology{
				Devices: []DeviceMemory{
					{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: 24576},
				},
				HostRAMGiB:        128,
				DeviceBudgetBytes: 24576 * bytesPerMiB,
				HostBudgetBytes:   112 * 1024 * bytesPerMiB,
				ProbedAt:          testInstallInstant.Format(time.RFC3339),
			}, true
		},
		// The operator asked for a different quantization than the one installed.
		tuning: func() Tuning { return Tuning{Packing: PackingPTQ1_0} },
	})
	if fx.manifest.Packing != PackingPQ2_0 {
		t.Fatalf("precondition: the staged install carries %q", fx.manifest.Packing)
	}

	launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	if launch.Plan == nil {
		t.Fatal("no plan was applied")
	}
	if launch.Plan.Packing != PackingPQ2_0 {
		t.Errorf("the re-plan priced %q, but %q is what is on disk",
			launch.Plan.Packing, PackingPQ2_0)
	}
	// The model path is still the installed one, which is the observable half of
	// the same invariant.
	requireArgs(t, launch.Spec.Args(), "-m", fx.manifest.ModelFile)
}

// TestLoadRecordsTheContextTheServerReported is the honesty criterion: after a
// successful load, the persisted context equals the value the server itself
// reported through /props — not the estimate the install froze. This matters
// because llm-providers.md gives the tier-1 config override precedence over the
// tier-1.5 lazy probe, so a stale override can never be corrected by anything
// else.
func TestLoadRecordsTheContextTheServerReported(t *testing.T) {
	t.Parallel()

	const reported = 24576
	fx := newFixture(t, fixtureOptions{
		contextSize: 16384, // the install's estimate
		propsNCtx:   reported,
		propsSlots:  1,
	})

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := fx.persistedContexts(); len(got) != 1 || got[0] != reported {
		t.Errorf("PersistContext calls = %v, want exactly [%d]", got, reported)
	}
	if got := fx.diskManifest(t).ContextSize; got != reported {
		t.Errorf("the manifest context_size = %d, want the reported %d", got, reported)
	}
	if fx.endpoint.hits("/props") == 0 {
		t.Error("the load never asked the server what context it came up with")
	}
	waitForState(t, fx.srv, StateLoaded)
}

// TestLoadMultipliesTheReportedContextByTheSlotCount pins the arithmetic:
// `default_generation_settings.n_ctx` is PER SLOT, so the effective total the
// model can hold is that times `total_slots`. Reading the per-slot figure alone
// would under-report a multi-slot server by a factor of -np.
func TestLoadMultipliesTheReportedContextByTheSlotCount(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		contextSize: 16384,
		propsNCtx:   8192,
		propsSlots:  4,
	})

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := fx.persistedContexts(); len(got) != 1 || got[0] != 8192*4 {
		t.Errorf("PersistContext calls = %v, want exactly [%d]", got, 8192*4)
	}
	if got := fx.diskManifest(t).ContextSize; got != 8192*4 {
		t.Errorf("the manifest context_size = %d, want %d", got, 8192*4)
	}
}

// TestLoadKeepsTheRecordedContextWhenTheReadbackFails is the fail-soft half: a
// readback that does not answer leaves the previous value in place and does NOT
// fail a load that is already serving.
func TestLoadKeepsTheRecordedContextWhenTheReadbackFails(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		contextSize: 16384,
		propsFail:   true,
	})

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load with a failing /props readback: %v", err)
	}
	if got := fx.persistedContexts(); len(got) != 0 {
		t.Errorf("a failed readback persisted %v, want nothing", got)
	}
	if got := fx.diskManifest(t).ContextSize; got != 16384 {
		t.Errorf("a failed readback changed the manifest to %d, want the previous 16384", got)
	}
	waitForState(t, fx.srv, StateLoaded)
}

// TestLoadDoesNotRewriteTheRecordWhenNothingChanged keeps a steady machine
// cheap: a readback that agrees with the record and a probe nobody wired produce
// no manifest write and no config write on every cold start.
func TestLoadDoesNotRewriteTheRecordWhenNothingChanged(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{})

	before := fx.diskManifest(t)
	beforeInfo, err := os.Stat(mustManifestPath(t, fx.layout))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := fx.persistedContexts(); len(got) != 0 {
		t.Errorf("an unchanged context was persisted: %v", got)
	}
	after := fx.diskManifest(t)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the manifest changed though nothing was learned:\n got %+v\nwant %+v", after, before)
	}
	afterInfo, err := os.Stat(mustManifestPath(t, fx.layout))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Error("the manifest was rewritten though nothing changed")
	}
}

func mustManifestPath(t *testing.T, layout Layout) string {
	t.Helper()
	path, err := layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	return path
}

// TestReadPropsContextIsFailSoft covers every way the readback question can go
// wrong. None of them may produce a number: a value invented from a broken
// response would be persisted into a tier-1 config override that shadows every
// later attempt to correct it.
func TestReadPropsContextIsFailSoft(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"a 500", http.StatusInternalServerError, `{"error":"boom"}`},
		{"a 404", http.StatusNotFound, `not found`},
		{"a body that is not JSON", http.StatusOK, `<html></html>`},
		{"a missing default_generation_settings", http.StatusOK, `{"total_slots":1}`},
		{"a zero n_ctx", http.StatusOK, `{"default_generation_settings":{"n_ctx":0},"total_slots":1}`},
		{"a negative n_ctx", http.StatusOK, `{"default_generation_settings":{"n_ctx":-1},"total_slots":1}`},
		{"an empty object", http.StatusOK, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			got, ok := readPropsContext(t.Context(), srv.Client(), srv.URL+"/props", 2*time.Second)
			if ok {
				t.Errorf("readPropsContext answered %d for %s, want ok=false", got, tc.name)
			}
			if got != 0 {
				t.Errorf("readPropsContext returned %d alongside ok=false", got)
			}
		})
	}

	t.Run("an absent total_slots means one slot", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096}}`))
		}))
		t.Cleanup(srv.Close)

		got, ok := readPropsContext(t.Context(), srv.Client(), srv.URL+"/props", 2*time.Second)
		if !ok || got != 4096 {
			t.Errorf("readPropsContext = (%d, %v), want (4096, true)", got, ok)
		}
	})

	t.Run("an unreachable server is not an error anybody sees", func(t *testing.T) {
		t.Parallel()
		got, ok := readPropsContext(t.Context(), http.DefaultClient,
			"http://127.0.0.1:1/props", 200*time.Millisecond)
		if ok || got != 0 {
			t.Errorf("readPropsContext = (%d, %v) against a closed port, want (0, false)", got, ok)
		}
	})

	t.Run("a cancelled context is bounded", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got, ok := readPropsContext(ctx, http.DefaultClient, "http://127.0.0.1:1/props", time.Second)
		if ok || got != 0 {
			t.Errorf("readPropsContext = (%d, %v) on a cancelled context, want (0, false)", got, ok)
		}
	})
}

// TestRecordEffectiveContextSurvivesAFailingConfigWrite pins the last fail-soft
// layer: the model is resident and serving, so an administrative write that
// fails is a warning and not a load failure. The manifest — which core owns and
// already wrote — stays correct, and the next load retries the config side.
func TestRecordEffectiveContextSurvivesAFailingConfigWrite(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{contextSize: 16384, propsNCtx: 65536})
	fx.srv.PersistContext = func(context.Context, int) error {
		return errors.New("the config write failed")
	}

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load with a failing config write: %v", err)
	}
	waitForState(t, fx.srv, StateLoaded)
	if got := fx.diskManifest(t).ContextSize; got != 65536 {
		t.Errorf("the manifest context_size = %d, want the reported 65536", got)
	}
}

// TestLaunchSpecFallsBackToThePurePolicyForAPlanLessManifest keeps the legacy
// path honest: a manifest written before plans were recorded still launches,
// from the pure policy in resolve.go, and gains no plan it never had.
func TestLaunchSpecFallsBackToThePurePolicyForAPlanLessManifest(t *testing.T) {
	t.Parallel()

	fx := newFixture(t, fixtureOptions{
		contextSize: 16384,
		// A probe is wired and answers, but there is no recorded plan to refine,
		// so the load must not invent one.
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return stagedTopology(), true
		},
	})

	launch, err := fx.srv.launchSpec(t.Context(), fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	if launch.Plan != nil {
		t.Errorf("a plan-less manifest produced a plan: %+v", *launch.Plan)
	}
	if launch.Topology != nil {
		t.Errorf("a plan-less manifest recorded a topology: %+v", *launch.Topology)
	}
	if launch.Refreshed {
		t.Error("a plan-less manifest reported a refinement")
	}
	args := launch.Spec.Args()
	requireArgs(t, args, "-fit", "off")
	requireArgs(t, args, "-c", "16384")
	requireArgs(t, args, "-np", strconv.Itoa(DefaultParallel))
}
