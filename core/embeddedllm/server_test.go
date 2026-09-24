package embeddedllm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	notReady    int
	readyHook   func()
	onProbe     func(hits int)
	hookFired   bool
	modelsBody  string
	neverReady  bool
	lastRequest string
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

// TestLaunchSpecArgs pins the exact command line, flag order included: it is the
// configuration the pinned fork's own demo scripts run, and every value except
// the paths and the port is a fixed constant.
func TestLaunchSpecArgs(t *testing.T) {
	t.Parallel()

	spec := LaunchSpec{
		ServerBinary:   "/tmp/runtimes/llama-x/build/bin/llama-server",
		ModelFile:      "/tmp/models/bonsai.gguf",
		MMProjFile:     "/tmp/models/mmproj.gguf",
		Host:           LoopbackHost,
		Port:           4321,
		Layers:         99,
		ContextSize:    32768,
		ImageMaxTokens: 1024,
	}

	want := []string{
		"-m", "/tmp/models/bonsai.gguf",
		"--host", "127.0.0.1",
		"--port", "4321",
		"-ngl", "99",
		"-fa", "on",
		"-c", "32768",
		"--temp", "1.0",
		"--top-p", "0.95",
		"--top-k", "20",
		"--jinja",
		"--no-webui",
		"--mmproj", "/tmp/models/mmproj.gguf",
		"--image-max-tokens", "1024",
	}
	got := spec.Args()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args =\n  %v\nwant\n  %v", got, want)
	}
}

// TestLaunchSpecOmitsOptionalFlags covers the two flags that must disappear
// rather than be passed a sentinel: an absent projector, and an uncapped image
// budget (CUDA/ROCm).
func TestLaunchSpecOmitsOptionalFlags(t *testing.T) {
	t.Parallel()

	spec := LaunchSpec{
		ServerBinary:   "/bin/llama-server",
		ModelFile:      "/models/bonsai.gguf",
		Host:           LoopbackHost,
		Port:           4321,
		Layers:         99,
		ContextSize:    65536,
		ImageMaxTokens: ImageMaxTokensUncapped,
	}
	args := spec.Args()
	requireNoFlag(t, args, "--mmproj")
	requireNoFlag(t, args, "--image-max-tokens")
	requireFlag(t, args, "--no-webui")
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
				Layers:         layersFor(platform, backend),
				ContextSize:    contextSizeFor(32),
				ImageMaxTokens: imageMaxTokensFor(backend),
			}
			if err := spec.Validate(); err != nil {
				t.Errorf("%s/%s: Validate: %v", platform, backend, err)
				continue
			}
			args := spec.Args()
			requireArgs(t, args, "--host", LoopbackHost)
			requireFlag(t, args, "--no-webui")
			requireNoFlag(t, args, "--agent")
			requireNoFlag(t, args, "-ag")
			requireNoFlag(t, args, "--webui")
			requireNoFlag(t, args, "--ui")
			requireNoFlag(t, args, "--tools")
			requireNoFlag(t, args, "--cors-origins")
		}
	}
}

// TestLaunchSpecRejectsUnlaunchableContexts is the "-c is never unspecified and
// never the model's own training context" rule, enforced at the last gate before
// exec.
func TestLaunchSpecRejectsUnlaunchableContexts(t *testing.T) {
	t.Parallel()

	fullTrainingContext := 1 << 18
	for _, ctxSize := range []int{0, -1, fullTrainingContext, contextTierTop + 1} {
		spec := LaunchSpec{
			ServerBinary: "/bin/" + ServerBinaryName,
			ModelFile:    "/models/bonsai.gguf",
			Host:         LoopbackHost,
			Port:         4321,
			ContextSize:  ctxSize,
		}
		err := spec.Validate()
		if !errors.Is(err, ErrLaunchSpecInvalid) {
			t.Errorf("context %d: Validate = %v, want ErrLaunchSpecInvalid", ctxSize, err)
		}
	}

	// Every tier the resolver can emit IS launchable.
	for _, ram := range []float64{16, 20, 24, 32, 48, 72, 128} {
		spec := LaunchSpec{
			ServerBinary: "/bin/" + ServerBinaryName,
			ModelFile:    "/models/bonsai.gguf",
			Host:         LoopbackHost,
			Port:         4321,
			ContextSize:  contextSizeFor(ram),
		}
		if err := spec.Validate(); err != nil {
			t.Errorf("context %d (from %.0f GiB): Validate: %v", spec.ContextSize, ram, err)
		}
	}
}

// TestLaunchSpecRejectsOtherUnusableValues rounds out the validation gate.
func TestLaunchSpecRejectsOtherUnusableValues(t *testing.T) {
	t.Parallel()

	base := LaunchSpec{
		ServerBinary:   "/bin/" + ServerBinaryName,
		ModelFile:      "/models/bonsai.gguf",
		Host:           LoopbackHost,
		Port:           4321,
		ContextSize:    16384,
		ImageMaxTokens: 1024,
	}
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
		"negative layers":   func(spec *LaunchSpec) { spec.Layers = -1 },
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
			resolution, err := Resolve(tc.platform, tc.backend, tc.ramGiB)
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
			spec, err := fx.srv.launchSpec(fx.manifest, fx.manifest.Port)
			if err != nil {
				t.Fatalf("launchSpec: %v", err)
			}
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
	spec, err := fx.srv.launchSpec(fx.manifest, fx.manifest.Port)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
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
	requireFlag(t, cmd.Args, "--no-webui")
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
