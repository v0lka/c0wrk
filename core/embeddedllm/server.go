package embeddedllm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file supervises the one long-lived process the embedded LLM runs on:
// llama-server, bound strictly to loopback, with its built-in Web UI disabled
// (ADR-066 D12). Everything the command line depends on comes from the install
// record (manifest.json) plus the pure launch policy in resolve.go — the
// supervisor never invents a flag value and never re-derives hardware at load
// time.
//
// Two properties are load-bearing and easy to lose:
//
//   - Readiness is a successful GET /v1/models, NEVER /health. The fork's
//     server opens its listening socket and answers /health long before the
//     weights are in memory, so a /health-based Load would report "loaded"
//     for a model that then refuses or hangs the first request.
//   - A Load is idempotent and single-instance: at most one llama-server per
//     install, and a Load against an already-serving model is a no-op.
//
// Lifetime is owned here, not by a context: the spawned process deliberately
// does NOT use exec.CommandContext, because it must outlive the Load call that
// started it. It stops on Unload/Stop (graceful signal, then kill), on the
// idle timer (idle.go), or on app shutdown.

// LoopbackHost is the only address the embedded server is ever bound to. A
// non-loopback bind would expose an unauthenticated OpenAI-compatible endpoint
// (and, with the Web UI, a second agent) to the local network.
const LoopbackHost = "127.0.0.1"

// Supervision budgets. The weight load dominates: 6.7 GiB read from disk into
// RAM/VRAM takes tens of seconds on an NVMe machine and several minutes on a
// slow one, so the ready budget is generous and the poll interval is coarse —
// probing more often would not make the load faster.
const (
	// DefaultReadyTimeout bounds one Load, from spawn to the first /v1/models
	// answer. Exceeding it is a failure, not a longer wait: a server that has
	// not loaded the weights in this long is wedged.
	DefaultReadyTimeout = 15 * time.Minute
	// DefaultReadyPollInterval is the gap between readiness probes.
	DefaultReadyPollInterval = 500 * time.Millisecond
	// DefaultProbeTimeout bounds a SINGLE readiness probe, so one hung
	// connection cannot consume the whole ready budget.
	DefaultProbeTimeout = 10 * time.Second
	// DefaultStopTimeout is the graceful window between the termination signal
	// and the kill.
	DefaultStopTimeout = 10 * time.Second
	// defaultKillWait bounds the wait after a kill. It should never be needed —
	// SIGKILL is unconditional — but a process stuck in an uninterruptible syscall
	// must not wedge shutdown forever.
	defaultKillWait = 5 * time.Second
)

// Output handling limits. The server's own log is diagnostic, not a data
// channel: lines are capped so one enormous line cannot exhaust memory, and
// only a short tail is retained for error messages.
const (
	maxLogLineBytes  = 64 << 10
	tailLines        = 24
	maxModelsBodyLen = 1 << 20
)

// Fixed launch policy (ADR-066, "Sampling flags"): the fork's demo scripts run
// the model with exactly these values, and they are deliberately not
// configurable — a knob here would silently move the model off the
// configuration its packing was validated with. Reasoning effort is NOT set
// (D7): the server default applies and the user changes it through the
// existing mechanism.
const (
	samplingTemp   = "1.0"
	samplingTopP   = "0.95"
	samplingTopK   = "20"
	flashAttention = "on"
)

// State is the supervision state machine.
//
//	not_installed ─Install→ installed ─Load→ loading ─/v1/models→ loaded
//	                       ▲                                        │
//	                       └────── Unload / idle ── unloading ───────┘
//	any spawn/load/process failure ─────────────────────────────→ error
type State string

const (
	// StateNotInstalled means no manifest: nothing is on disk to run.
	StateNotInstalled State = "not_installed"
	// StateInstalled means the bytes are on disk and the server is NOT
	// running. This is the "unloaded" state of the UI vocabulary.
	StateInstalled State = "installed"
	// StateLoading means a process is up and the weights are being read.
	StateLoading State = "loading"
	// StateLoaded means /v1/models answered: the model can serve requests.
	StateLoaded State = "loaded"
	// StateUnloading means a stop was requested and the process is still
	// draining its graceful window.
	StateUnloading State = "unloading"
	// StateError means the last operation failed, or the process died on its
	// own. A dead server is never reported as loaded.
	StateError State = "error"
)

// Running reports whether the state implies a live llama-server process.
func (st State) Running() bool {
	return st == StateLoading || st == StateLoaded || st == StateUnloading
}

// StateEvent is one state transition, in the shape the backend forwards as the
// global `embedded_llm:state` event. Core never imports backend, so the
// emission is a callback seam (Server.OnState).
type StateEvent struct {
	State State `json:"state"`
	// Port is the loopback port the server is bound (or was bound) to. Zero
	// when no install record was read yet.
	Port int `json:"port"`
	// Message carries the human-readable cause for StateError, and is empty
	// for every other transition.
	Message string `json:"message,omitempty"`
}

// Status is a snapshot of the supervision state, safe to hand to the UI.
type Status struct {
	State State
	Port  int
	// Pid is the OS process id of the supervised server, or 0 when no process
	// is running.
	Pid int
	// Since is when the current state was entered.
	Since time.Time
	// Message is the StateError cause, empty otherwise.
	Message string
}

// Supervisor refusals. All are sentinels so callers can branch with errors.Is
// and surface an actionable message.
var (
	// ErrServerDied reports that the llama-server process exited. When it
	// happens during a Load, the wrapped detail carries the exit status and the
	// last lines of the server's own log, which is where the real cause (a
	// missing CUDA library, an out-of-memory kill, a Gatekeeper block) lives.
	ErrServerDied = errors.New("the embedded LLM server process exited")
	// ErrLoadTimeout reports that the weights did not finish loading inside
	// ReadyTimeout.
	ErrLoadTimeout = errors.New("the embedded LLM server did not become ready")
	// ErrLaunchSpecInvalid reports a launch specification that must not be
	// spawned: no binary, no model, an unusable port, or a context size outside
	// the resolved RAM tiers.
	ErrLaunchSpecInvalid = errors.New("invalid embedded-LLM launch specification")
	// ErrServerBusy reports an operation refused because a server process is
	// live and its state is not the caller's to overwrite.
	ErrServerBusy = errors.New("the embedded LLM server is running")
)

// LaunchSpec is everything one llama-server invocation is derived from. It is
// built from the install record plus the pure launch policy in resolve.go, so
// the flags always match what the installer provisioned for this machine.
type LaunchSpec struct {
	// ServerBinary is the absolute path of llama-server inside the installed
	// runtime tree.
	ServerBinary string
	// ModelFile is the GGUF passed to -m.
	ModelFile string
	// MMProjFile is the vision projector passed to --mmproj. Empty omits the
	// flag (text-only serving), which is only acceptable when the projector is
	// genuinely absent.
	MMProjFile string
	// Host is the bind address. Always LoopbackHost.
	Host string
	// Port is the persisted loopback port.
	Port int
	// Layers is -ngl, from the platform+backend policy in resolve.go.
	Layers int
	// ContextSize is -c: the RAM tier resolved at install time and recorded in
	// the manifest. Never unspecified, never the model's own training context.
	ContextSize int
	// ImageMaxTokens is --image-max-tokens. ImageMaxTokensUncapped omits the
	// flag entirely (CUDA/ROCm run uncapped).
	ImageMaxTokens int
}

// Validate refuses a specification that must never reach exec: spawning it
// would either fail obscurely or, worse, start a server with a memory-unaware
// context.
func (spec LaunchSpec) Validate() error {
	if spec.ServerBinary == "" {
		return fmt.Errorf("%w: no llama-server binary", ErrLaunchSpecInvalid)
	}
	if spec.ModelFile == "" {
		return fmt.Errorf("%w: no model file", ErrLaunchSpecInvalid)
	}
	if spec.Host != LoopbackHost {
		return fmt.Errorf("%w: host %q is not the loopback bind %q",
			ErrLaunchSpecInvalid, spec.Host, LoopbackHost)
	}
	if !usablePort(spec.Port) {
		return fmt.Errorf("%w: port %d is not a usable TCP port", ErrLaunchSpecInvalid, spec.Port)
	}
	if spec.Layers < 0 {
		return fmt.Errorf("%w: -ngl %d is negative", ErrLaunchSpecInvalid, spec.Layers)
	}
	// The context must be an explicit positive tier: an unspecified one lets the
	// server fall back to the model's own training context, which is
	// memory-unaware and OOMs a constrained machine once -ngl offloads the KV
	// cache. Nothing above the resolver's top tier is launchable either — the
	// subsystem never asks for more than Resolve can justify.
	if spec.ContextSize <= 0 || spec.ContextSize > contextTierTop {
		return fmt.Errorf("%w: context size %d is outside the resolved tiers (1..%d)",
			ErrLaunchSpecInvalid, spec.ContextSize, contextTierTop)
	}
	if spec.ImageMaxTokens < 0 {
		return fmt.Errorf("%w: --image-max-tokens %d is negative",
			ErrLaunchSpecInvalid, spec.ImageMaxTokens)
	}
	return nil
}

// Args renders the command line. The flag order is fixed and the values are the
// ones the pinned fork was validated with; the Web UI is always off.
func (spec LaunchSpec) Args() []string {
	args := make([]string, 0, 24)
	args = append(args,
		"-m", spec.ModelFile,
		"--host", spec.Host,
		"--port", strconv.Itoa(spec.Port),
		"-ngl", strconv.Itoa(spec.Layers),
		"-fa", flashAttention,
		"-c", strconv.Itoa(spec.ContextSize),
		"--temp", samplingTemp,
		"--top-p", samplingTopP,
		"--top-k", samplingTopK,
		"--jinja",
		// D12: the built-in Web UI ships its own MCP client and agentic loop.
		// It is disabled unconditionally — this process is an inference
		// endpoint for c0wrk and nothing else.
		"--no-webui",
	)
	if spec.MMProjFile != "" {
		args = append(args, "--mmproj", spec.MMProjFile)
	}
	if spec.ImageMaxTokens != ImageMaxTokensUncapped {
		args = append(args, "--image-max-tokens", strconv.Itoa(spec.ImageMaxTokens))
	}
	return args
}

// ModelsURL is the readiness endpoint: the OpenAI-compatible model list.
func (spec LaunchSpec) ModelsURL() string {
	return baseURL(spec.Port) + "/models"
}

// usablePort reports whether a port can be bound. The bounds are the
// subsystem's own MinLoopbackPort/MaxLoopbackPort, which mirror what
// backend/config's validate() enforces on the persisted value; this is the last
// gate before exec.
func usablePort(port int) bool {
	return port >= MinLoopbackPort && port <= MaxLoopbackPort
}

// baseURL renders the OpenAI-compatible base URL of a server on the given
// loopback port. It must stay byte-identical to the provider base_url that
// backend/config generates from embedded_llm.port.
func baseURL(port int) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/v1"
}

// LaunchCommand is the fully resolved process invocation handed to SpawnFunc.
// Args and Env are built by production code, so a substituted spawner still
// exercises (and can assert) the exact command line and environment.
type LaunchCommand struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

// Process is the supervisor's handle on one spawned llama-server. It is the
// seam every supervision test drives: the real implementation wraps
// *exec.Cmd, and a test double can start a fake endpoint, die on cue, or
// ignore a graceful signal.
type Process interface {
	// Wait blocks until the process has exited AND its output streams have been
	// drained, then returns the exit error (nil on a clean exit).
	Wait() error
	// Pid is the OS process id, for logs and for the kill fallback message.
	Pid() int
	// Signal asks the process to terminate gracefully. It returns an error when
	// the platform has no graceful signal for a child process (Windows) or the
	// process is already gone — both mean "kill it instead".
	Signal(sig os.Signal) error
	// Kill terminates the process immediately.
	Kill() error
	// Stdout and Stderr are the process's output streams, or nil when the
	// implementation does not capture them. The supervisor drains both into
	// slog; Wait must not return before that drain completes.
	Stdout() io.Reader
	Stderr() io.Reader
}

// SpawnFunc starts the process described by cmd. The context bounds the START,
// not the process lifetime: the returned Process outlives it.
type SpawnFunc func(ctx context.Context, cmd LaunchCommand) (Process, error)

// Server supervises one llama-server process for one installation.
//
// The zero value is not usable; build one with NewServer. As with Installer,
// every external effect is an injectable field, so the whole state machine —
// including a multi-minute weight load, a crash and the idle unload — is
// testable without a runtime, without weights and without waiting.
type Server struct {
	// Layout locates the installed runtime tree and weights. Required.
	Layout Layout
	// Logger receives the subsystem's diagnostics AND the server's own
	// stdout/stderr. nil → slog.Default().
	Logger *slog.Logger
	// AutoUnload is the operator's idle policy (embedded_llm.auto_unload).
	// Change it at runtime with SetAutoUnload, not by writing this field.
	AutoUnload AutoUnload
	// Spawn starts the process. nil → spawnOSServer, the real llama-server.
	Spawn SpawnFunc
	// EnsurePort re-checks that the persisted port is still free immediately
	// before the spawn, and may return a replacement (which it also persists).
	// PRODUCTION ALWAYS WIRES IT: backend.embeddedEnsurePort scans upward with
	// SearchFreePort and writes a moved port back to embedded_llm.port, so the
	// generated provider base_url keeps matching the socket the server actually
	// bound. nil → the persisted port is trusted as-is, which is only correct
	// for a caller that has no config to keep in sync (tests, a core-only
	// embedding); it deliberately performs no probing of its own so a unit test
	// never binds a real port.
	//
	// The re-check is what makes the FIRST readiness probe safe: it runs
	// immediately after the spawn, so a server left behind on the same port by a
	// crashed previous run — or any other local process — would answer it and be
	// mistaken for the new one.
	EnsurePort func(ctx context.Context, port int) (int, error)
	// OnState receives one StateEvent per observable transition. May be nil.
	// It is called from the goroutine that made the transition and must not
	// call back into the Server: it runs outside the state lock, but a reentrant
	// Load/Unload from it would deadlock on the single-instance gate.
	OnState func(StateEvent)
	// HTTPClient performs the readiness probes. nil → http.DefaultClient with a
	// per-probe timeout (ProbeTimeout).
	HTTPClient *http.Client
	// Now stamps state transitions and idle bookkeeping. nil → time.Now.
	Now func() time.Time
	// HostOS overrides the OS the binary name and the library-path policy key
	// off. Empty → runtime.GOOS.
	HostOS string
	// Platform overrides the "<goos>-<goarch>" key the -ngl policy reads.
	// Empty → the host platform. Production leaves it empty; the value that
	// matters (the effective backend) comes from the manifest.
	Platform string
	// ReadyTimeout bounds one Load. <= 0 → DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// ReadyPollInterval is the readiness poll gap. <= 0 → DefaultReadyPollInterval.
	ReadyPollInterval time.Duration
	// ProbeTimeout bounds a single readiness probe. <= 0 → DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// StopTimeout is the graceful window before a kill. <= 0 → DefaultStopTimeout.
	StopTimeout time.Duration

	// killWaitFor shortens the post-kill wait. Unexported: it is a recovery
	// timeout with no operator meaning, and only a test needs it small.
	killWaitFor time.Duration

	mu      sync.Mutex
	state   State
	message string
	port    int
	since   time.Time
	run     *processRun
	// gate is the single-instance lock: a buffered-1 channel used as a token,
	// so a waiter can abandon it through its context instead of blocking for
	// the whole weight load. Lazily created, which keeps the zero value from
	// deadlocking.
	gate chan struct{}
	idle idleTimer
}

// NewServer returns a Server for an installation described by layout. The state
// starts at not_installed; startup moves it to installed after restoring the
// manifest (SetInstalled).
func NewServer(layout Layout, logger *slog.Logger) *Server {
	return &Server{
		Layout:     layout,
		Logger:     logger,
		AutoUnload: DefaultAutoUnload(),
		gate:       make(chan struct{}, 1),
	}
}

// processRun is one supervised process lifetime.
type processRun struct {
	proc Process
	pid  int
	tail *lineTail

	// died is closed exactly once, by the supervise goroutine, AFTER exitErr has
	// been written. A reader must not touch exitErr before it observes the
	// close.
	died    chan struct{}
	exitErr error

	// Both flags are written under Server.mu before the exit is provoked, and
	// tell the supervisor how to report an exit it did not expect to see:
	//   expected  — an Unload asked for it → StateInstalled
	//   abandoned — a failed Load discarded it → the load error owns the state
	expected  bool
	abandoned bool
}

// Load starts the server and blocks until the model can actually answer.
//
// The sequence is fixed:
//
//  1. acquire the single-instance gate (already serving → no-op success);
//  2. read manifest.json — no manifest means not installed, and nothing is
//     spawned;
//  3. re-check the persisted port through the EnsurePort seam;
//  4. derive the LaunchSpec from the manifest plus the pure launch policy, and
//     refuse it if it is not launchable;
//  5. spawn with the runtime's own directory on the dynamic-library path, and
//     start draining its output into slog;
//  6. poll /v1/models until it answers with a non-empty model list, bounded by
//     ReadyTimeout, ctx, and the death of the process;
//  7. only now: state → loaded and the idle budget starts.
//
// Step 7's position is the user-visible requirement behind idle.go: the weight
// load can take minutes, and that time must never be charged against the
// operator's inactivity budget.
//
// ctx bounds the whole operation. Cancelling it aborts the load and stops the
// half-started process, so callers that must survive a long weight load pass a
// context that is not tied to one RPC.
func (s *Server) Load(ctx context.Context) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := s.acquireGate(ctx)
	if err != nil {
		return fmt.Errorf("embeddedllm: waiting for the embedded LLM to become available: %w", err)
	}
	defer release()

	if s.State() == StateLoaded {
		// Idempotent: the model is already serving, so a Load is a no-op that
		// still counts as activity (the caller is about to use it).
		s.MarkActivity()
		return nil
	}

	// A process can still be alive from an attempt whose stop timed out. Single
	// instance means at most one llama-server per install, so it is discarded
	// before another one is spawned. Taking the handle out of the Server first
	// also tells the supervisor that this exit is nobody's news: no state
	// transition is emitted for it.
	if stale := s.takeRun(); stale != nil {
		s.logger().Warn("stopping a leftover embedded LLM process before loading",
			"pid", stale.pid)
		s.discardRun(stale)
	}

	manifest, err := s.readManifest()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			s.transition(StateNotInstalled, "")
		} else {
			s.transition(StateError, err.Error())
		}
		return err
	}

	port := manifest.Port
	if s.EnsurePort != nil {
		port, err = s.EnsurePort(ctx, port)
		if err != nil {
			s.transition(StateError, err.Error())
			return fmt.Errorf("embeddedllm: preparing the loopback port: %w", err)
		}
	}

	spec, err := s.launchSpec(manifest, port)
	if err != nil {
		s.transition(StateError, err.Error())
		return err
	}

	s.mu.Lock()
	s.port = spec.Port
	s.mu.Unlock()
	s.transition(StateLoading, "")

	run, err := s.spawn(ctx, spec)
	if err != nil {
		s.transition(StateError, err.Error())
		return err
	}

	s.mu.Lock()
	s.run = run
	s.mu.Unlock()
	go s.supervise(run)

	if err := s.waitReady(ctx, run, spec); err != nil {
		// The load failed, so the half-started process is discarded. Marking it
		// abandoned first keeps the supervisor from reporting a death THIS call is
		// about to explain as an unexplained crash. A process that died on its own
		// before the flag was set is reported twice — once by the supervisor and
		// once here — which is deliberate: suppressing the supervisor's report
		// instead would need a flag set before the load, and clearing it after
		// readiness opens a window where a death right after the last successful
		// probe is swallowed and a dead server keeps being reported as loaded.
		s.mu.Lock()
		run.abandoned = true
		s.mu.Unlock()
		s.discardRun(run)
		s.transition(StateError, err.Error())
		s.logger().Error("the embedded LLM did not become ready",
			"port", spec.Port, "error", err, "last_output", run.tail.String())
		return err
	}

	s.mu.Lock()
	event := s.transitionLocked(StateLoaded, "")
	// The idle budget starts HERE, after the weights are in memory — see step 7.
	// Stamping it at the spawn instead would charge a multi-minute weight load
	// to the operator's inactivity budget.
	s.recordActivityLocked()
	s.mu.Unlock()
	s.emit(event)

	s.logger().Info("embedded LLM ready",
		"port", spec.Port, "pid", run.pid, "backend", manifest.Backend,
		"context_size", spec.ContextSize, "layers", spec.Layers)
	return nil
}

// Unload stops the server, which deterministically returns its RAM and VRAM to
// the system (ADR-066 D5). It is a no-op when nothing is running.
//
// Termination is graceful first: the process is asked to exit, and killed only
// after StopTimeout. The resulting state is installed — the bytes stay on disk,
// the model is simply not resident.
func (s *Server) Unload(ctx context.Context) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := s.acquireGate(ctx)
	if err != nil {
		return fmt.Errorf("embeddedllm: waiting for the embedded LLM to become available: %w", err)
	}
	defer release()

	s.mu.Lock()
	run := s.run
	if run == nil {
		// Nothing is running. A stale error is cleared, because "no process" is
		// the truth this call was asked to establish; a not-installed state is
		// left alone, since Unload does not change what is on disk.
		var event *StateEvent
		if s.stateLocked() != StateNotInstalled {
			event = s.transitionLocked(StateInstalled, "")
		}
		s.mu.Unlock()
		s.emit(event)
		return nil
	}
	run.expected = true
	event := s.transitionLocked(StateUnloading, "")
	s.mu.Unlock()
	s.emit(event)

	err = s.terminate(ctx, run)

	// The supervisor normally records the final transition; do it here too so
	// Unload's caller can rely on the state when it returns. transitionLocked
	// de-duplicates, so whichever runs first wins and no double event escapes.
	//
	// A stop that did not take is reported as an error and KEEPS the run handle:
	// claiming "installed" while a process is still resident would misreport the
	// RAM, and dropping the handle would let the next Load start a second one.
	s.mu.Lock()
	var final *StateEvent
	if err != nil {
		final = s.transitionLocked(StateError, err.Error())
	} else {
		if s.run == run {
			s.run = nil
		}
		final = s.transitionLocked(StateInstalled, "")
	}
	s.mu.Unlock()
	s.emit(final)

	if err != nil {
		return err
	}
	s.logger().Info("embedded LLM unloaded", "port", s.Port())
	return nil
}

// Stop is Unload under the name the shutdown path and Installer.Stop use: the
// signatures match, so wiring is `installer.Stop = server.Stop`.
func (s *Server) Stop(ctx context.Context) error {
	return s.Unload(ctx)
}

// SetInstalled records whether the weights and runtime are present, without
// touching any process. Startup calls it with true after restoring the
// manifest; Remove calls it with false.
//
// It is refused while a server is live: the state of a running process belongs
// to the supervisor, and silently rewriting it would let a caller report a
// running model as absent (or the reverse).
func (s *Server) SetInstalled(installed bool) error {
	if s == nil {
		return errors.New("embeddedllm: nil Server")
	}
	s.mu.Lock()
	if s.run != nil {
		state := s.stateLocked()
		s.mu.Unlock()
		return fmt.Errorf("%w (state %s)", ErrServerBusy, state)
	}
	next := StateNotInstalled
	if installed {
		next = StateInstalled
	}
	event := s.transitionLocked(next, "")
	s.mu.Unlock()
	s.emit(event)
	return nil
}

// State reports the current supervision state.
func (s *Server) State() State {
	if s == nil {
		return StateNotInstalled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// Status reports a snapshot for the UI.
func (s *Server) Status() Status {
	if s == nil {
		return Status{State: StateNotInstalled}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := Status{
		State:   s.stateLocked(),
		Port:    s.port,
		Since:   s.since,
		Message: s.message,
	}
	if s.run != nil {
		snapshot.Pid = s.run.pid
	}
	return snapshot
}

// Port is the loopback port of the current (or most recent) server, or 0 when
// no install record has been read.
func (s *Server) Port() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// BaseURL is the OpenAI-compatible base URL of the supervised server. It is
// derived from the persisted port, never stored independently, and matches the
// provider base_url backend/config generates.
func (s *Server) BaseURL() string {
	return baseURL(s.Port())
}

// ── gate ──

// acquireGate takes the single-instance token, honouring ctx while waiting. It
// returns the release function; a nil error always comes with a non-nil
// release.
func (s *Server) acquireGate(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.gate == nil {
		s.gate = make(chan struct{}, 1)
	}
	gate := s.gate
	s.mu.Unlock()

	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── launch derivation ──

// launchSpec turns the install record into a launch specification. The context
// size is the tier resolved at install time (the manifest), while -ngl and
// --image-max-tokens are re-derived from the same pure policy Resolve uses,
// keyed on the EFFECTIVE backend the manifest recorded. No hardware probe runs
// at load time: a load must not fail because a driver query wedged.
func (s *Server) launchSpec(manifest Manifest, port int) (LaunchSpec, error) {
	runtimeDir, err := s.Layout.RuntimeDir(manifest.Backend)
	if err != nil {
		return LaunchSpec{}, err
	}
	binary, err := ServerBinaryPath(runtimeDir, s.hostOS())
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("embeddedllm: %w", err)
	}

	modelFile := manifest.ModelFile
	if modelFile == "" {
		modelFile, err = s.Layout.ModelFile(manifest.Packing)
		if err != nil {
			return LaunchSpec{}, err
		}
	}
	if !pathExists(modelFile) {
		return LaunchSpec{}, fmt.Errorf("%w: the recorded model file %q is missing — reinstall the model",
			ErrNotInstalled, modelFile)
	}

	// The projector is optional at launch: a missing one means no vision, which
	// beats refusing to serve text at all.
	mmproj := ""
	if asset := MMProjAsset(); asset.ArchiveName != "" {
		candidate, destErr := s.Layout.Destination(asset)
		switch {
		case destErr != nil:
			s.logger().Warn("the vision projector path could not be resolved",
				"error", destErr)
		case !pathExists(candidate):
			s.logger().Warn("the vision projector is missing; starting without image input",
				"path", candidate)
		default:
			mmproj = candidate
		}
	}

	platform := s.Platform
	if platform == "" {
		platform = runtime.GOOS + "-" + runtime.GOARCH
	}
	spec := LaunchSpec{
		ServerBinary:   binary,
		ModelFile:      modelFile,
		MMProjFile:     mmproj,
		Host:           LoopbackHost,
		Port:           port,
		Layers:         layersFor(platform, manifest.Backend),
		ContextSize:    manifest.ContextSize,
		ImageMaxTokens: imageMaxTokensFor(manifest.Backend),
	}
	if err := spec.Validate(); err != nil {
		return LaunchSpec{}, fmt.Errorf("embeddedllm: %w", err)
	}
	return spec, nil
}

// launchEnv builds the child environment: the parent's, with the runtime's own
// binary directory prepended to the platform's dynamic-library search path.
//
// The pinned archives ship their GPU/CUDA libraries next to the executable, and
// nothing is installed into a system location (ADR-066 D3: never under the
// agent's PATH). Without this, a Linux CUDA or ROCm build fails at dlopen time
// with a library-not-found error that looks like a broken install.
func launchEnv(binaryDir, goos string, parent []string) []string {
	env := make([]string, 0, len(parent)+1)
	if binaryDir == "" {
		return append(env, parent...)
	}
	key := libraryPathVar(goos)
	if key == "" {
		return append(env, parent...)
	}
	replaced := false
	for _, entry := range parent {
		name, value, found := strings.Cut(entry, "=")
		if !found || !strings.EqualFold(name, key) {
			env = append(env, entry)
			continue
		}
		replaced = true
		if value == "" {
			env = append(env, key+"="+binaryDir)
			continue
		}
		env = append(env, key+"="+binaryDir+pathListSeparator(goos)+value)
	}
	if !replaced {
		env = append(env, key+"="+binaryDir)
	}
	return env
}

// pathListSeparator is the list separator of the target platform's environment,
// derived from goos rather than from the host: the policy is a pure function of
// the platform it is built for, which is what keeps it table-testable from any
// CI machine.
func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

// libraryPathVar names the environment variable the platform's loader consults.
// Windows has no separate one, so the binary directory goes on PATH.
func libraryPathVar(goos string) string {
	switch goos {
	case "windows":
		return "PATH"
	case "darwin":
		return "DYLD_LIBRARY_PATH"
	default:
		return "LD_LIBRARY_PATH"
	}
}

// ── spawn ──

// spawn builds the LaunchCommand and hands it to the spawner.
func (s *Server) spawn(ctx context.Context, spec LaunchSpec) (*processRun, error) {
	binaryDir := filepath.Dir(spec.ServerBinary)
	cmd := LaunchCommand{
		Binary: spec.ServerBinary,
		Args:   spec.Args(),
		Env:    launchEnv(binaryDir, s.hostOS(), os.Environ()),
		Dir:    binaryDir,
	}
	s.logger().Debug("spawning the embedded LLM server",
		"binary", cmd.Binary, "args", strings.Join(cmd.Args, " "))

	proc, err := s.spawner()(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("embeddedllm: starting %s: %w", ServerBinaryName, err)
	}
	return &processRun{
		proc: proc,
		pid:  proc.Pid(),
		tail: newLineTail(tailLines),
		died: make(chan struct{}),
	}, nil
}

func (s *Server) spawner() SpawnFunc {
	if s.Spawn != nil {
		return s.Spawn
	}
	return func(ctx context.Context, cmd LaunchCommand) (Process, error) {
		return spawnOSServer(ctx, cmd, s.logger())
	}
}

// spawnOSServer starts the real llama-server.
//
// The context is detached with context.WithoutCancel: the one that started a load
// is usually an RPC or a UI action, and the server must outlive both. Its lifetime
// is owned by Unload/Stop, the idle timer and app shutdown — never by the caller
// that happened to ask for the load.
func spawnOSServer(ctx context.Context, cmd LaunchCommand, logger *slog.Logger) (Process, error) {
	// The binary and every argument come from the pinned install record and the
	// pure launch policy — never from a model, a user string or the network.
	proc := exec.CommandContext(context.WithoutCancel(ctx), cmd.Binary, cmd.Args...)
	proc.Dir = cmd.Dir
	proc.Env = cmd.Env

	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capturing %s stdout: %w", ServerBinaryName, err)
	}
	stderr, err := proc.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capturing %s stderr: %w", ServerBinaryName, err)
	}
	if err := proc.Start(); err != nil {
		return nil, err
	}
	logger.Debug("embedded LLM server started", "pid", proc.Process.Pid, "binary", cmd.Binary)
	return &osProcess{cmd: proc, stdout: stdout, stderr: stderr}, nil
}

// osProcess adapts *exec.Cmd to Process. Stdout/Stderr hand out the pipes once;
// the supervisor drains them and only then calls Wait, which is the ordering
// os/exec requires for piped output.
type osProcess struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader
}

func (p *osProcess) Wait() error { return p.cmd.Wait() }

func (p *osProcess) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osProcess) Signal(sig os.Signal) error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(sig)
}

func (p *osProcess) Kill() error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}

func (p *osProcess) Stdout() io.Reader { return p.stdout }

func (p *osProcess) Stderr() io.Reader { return p.stderr }

// gracefulSignal is what Unload sends before resorting to a kill. Windows has
// no graceful child-process signal, so Signal fails there and the caller falls
// back to Kill — which is exactly what this fallback ordering is for.
func gracefulSignal() os.Signal { return syscall.SIGTERM }

// ── output ──

// pumpOutput drains both streams into slog and into the run's tail. It returns
// once both readers have hit EOF, which is the precondition for Wait.
func (s *Server) pumpOutput(run *processRun, wg *sync.WaitGroup) {
	s.pump(run, wg, run.proc.Stdout(), "stdout")
	s.pump(run, wg, run.proc.Stderr(), "stderr")
}

func (s *Server) pump(run *processRun, wg *sync.WaitGroup, r io.Reader, stream string) {
	if r == nil {
		return
	}
	logger := s.logger()
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 8*1024), maxLogLineBytes)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if line == "" {
				continue
			}
			run.tail.add(line)
			logger.Debug("embedded LLM server output",
				"stream", stream, "pid", run.pid, "line", line)
		}
		if err := scanner.Err(); err != nil {
			logger.Debug("the embedded LLM output stream ended early",
				"stream", stream, "pid", run.pid, "error", err)
		}
	}()
}

// lineTail keeps the last n lines of server output. A crash or a failed load is
// only actionable if the server's own complaint comes with it: "the process
// exited" is not a diagnosis, "ggml_cuda_init: found 0 devices" is.
type lineTail struct {
	mu    sync.Mutex
	limit int
	lines []string
}

func newLineTail(limit int) *lineTail {
	if limit <= 0 {
		limit = tailLines
	}
	return &lineTail{limit: limit}
}

func (t *lineTail) add(line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if over := len(t.lines) - t.limit; over > 0 {
		t.lines = t.lines[over:]
	}
}

func (t *lineTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// ── supervision ──

// supervise owns one process lifetime: drain its output, wait for the exit,
// then report it. It is the only goroutine that closes run.died.
func (s *Server) supervise(run *processRun) {
	var wg sync.WaitGroup
	s.pumpOutput(run, &wg)
	wg.Wait()
	exitErr := run.proc.Wait()

	s.mu.Lock()
	run.exitErr = exitErr
	current := s.run == run
	expected := run.expected
	abandoned := run.abandoned
	if current {
		s.run = nil
	}
	close(run.died)
	var event *StateEvent
	if current && !abandoned {
		if expected {
			event = s.transitionLocked(StateInstalled, "")
		} else {
			event = s.transitionLocked(StateError, s.crashMessage(run, exitErr))
		}
	}
	pid := run.pid
	s.mu.Unlock()

	if current && !abandoned && !expected {
		s.logger().Error("the embedded LLM server died",
			"pid", pid, "error", exitErr, "last_output", run.tail.String())
	}
	s.emit(event)
}

// crashMessage renders the user-facing cause of an unexpected exit.
func (s *Server) crashMessage(run *processRun, exitErr error) string {
	message := fmt.Sprintf("%s (pid %d): %s", ServerBinaryName, run.pid, exitReason(exitErr))
	if tail := strings.TrimSpace(run.tail.String()); tail != "" {
		message += "\nlast server output:\n" + tail
	}
	return message
}

// takeRun detaches and returns the live process handle, if any. Once detached,
// the supervisor treats its exit as unremarkable and reports no transition — the
// caller owns the state from here.
func (s *Server) takeRun() *processRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.run
	s.run = nil
	return run
}

// discardRun stops a run the supervisor no longer wants (a failed load) and
// waits, bounded, for the exit to be observed.
func (s *Server) discardRun(run *processRun) {
	if killErr := run.proc.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		s.logger().Debug("discarding the embedded LLM process failed",
			"pid", run.pid, "error", killErr)
	}
	timer := time.NewTimer(s.killWait())
	defer timer.Stop()
	select {
	case <-run.died:
	case <-timer.C:
		s.logger().Warn("the discarded embedded LLM process did not exit", "pid", run.pid)
	}
}

// terminate asks the process to stop, gracefully first, and kills it after the
// graceful window. It returns once the exit has been observed.
func (s *Server) terminate(ctx context.Context, run *processRun) error {
	if err := run.proc.Signal(gracefulSignal()); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		// Not a failure: Windows has no graceful child signal, and a process
		// that is already gone needs no signal. Both fall through to the kill.
		s.logger().Debug("the graceful stop signal was not delivered",
			"pid", run.pid, "error", err)
	}

	grace := time.NewTimer(s.stopTimeout())
	defer grace.Stop()
	select {
	case <-run.died:
		return nil
	case <-grace.C:
	case <-ctx.Done():
		s.logger().Warn("the graceful stop was interrupted; killing the server",
			"pid", run.pid, "error", ctx.Err())
	}

	if err := run.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.logger().Warn("killing the embedded LLM server failed", "pid", run.pid, "error", err)
	}
	hardWait := time.NewTimer(s.killWait())
	defer hardWait.Stop()
	select {
	case <-run.died:
		return nil
	case <-hardWait.C:
		return fmt.Errorf("embeddedllm: %s (pid %d) did not exit within %s of being killed",
			ServerBinaryName, run.pid, s.stopTimeout()+s.killWait())
	}
}

// ── readiness ──

// waitReady polls /v1/models until the model can serve. It gives up on the
// ready budget, on ctx, and — immediately — on the death of the process, so a
// server that segfaults during the weight load is reported in milliseconds
// instead of after a fifteen-minute timeout.
func (s *Server) waitReady(ctx context.Context, run *processRun, spec LaunchSpec) error {
	modelsURL := spec.ModelsURL()
	client := s.httpClient()
	probeTimeout := s.probeTimeout()

	var lastErr error
	probe := func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		ready, err := probeModels(probeCtx, client, modelsURL)
		if err != nil {
			lastErr = err
			return false
		}
		return ready
	}

	if probe() {
		return nil
	}
	deadline := time.NewTimer(s.readyTimeout())
	defer deadline.Stop()
	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("embeddedllm: waiting for %s to load the model: %w",
				ServerBinaryName, ctx.Err())
		case <-run.died:
			return fmt.Errorf("%w before it became ready (%s)\nlast server output:\n%s",
				ErrServerDied, exitReason(run.exitErr), strings.TrimSpace(run.tail.String()))
		case <-deadline.C:
			return fmt.Errorf("%w within %s (last probe: %s)",
				ErrLoadTimeout, s.readyTimeout(), probeDetail(lastErr))
		case <-ticker.C:
			if probe() {
				return nil
			}
		}
	}
}

// modelsPayload is the slice of the OpenAI model-list response readiness looks
// at. Only the presence of a served model matters.
type modelsPayload struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// probeModels performs one readiness probe.
//
// The answer must be more than "the socket is open": a 200 with an empty model
// list is a server that is still initialising, so it is treated as not ready
// and polling continues. A transport error is the normal state during the
// weight load (nothing is listening yet), so it is reported as an error the
// caller keeps as diagnostic detail rather than as a failure.
func probeModels(ctx context.Context, client *http.Client, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("building the readiness probe: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBodyLen))
	if err != nil {
		return false, fmt.Errorf("reading the readiness probe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	var payload modelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		// A 200 that is not the model list is not readiness either.
		return false, fmt.Errorf("GET %s answered 200 but not a model list: %w", url, err)
	}
	if len(payload.Data) == 0 {
		return false, nil
	}
	return true, nil
}

// exitReason renders an exit error for a message. A clean exit is still a
// failure when it was not asked for, so nil gets its own wording instead of
// disappearing into the message.
func exitReason(err error) string {
	if err == nil {
		return "exited with status 0"
	}
	return err.Error()
}

// probeDetail renders the last readiness-probe outcome for a timeout message, so
// "it did not become ready" says whether nothing was listening or the model list
// stayed empty.
func probeDetail(err error) string {
	if err == nil {
		return "the model list stayed empty"
	}
	return err.Error()
}

// ── state plumbing ──

// stateLocked normalises the zero value. s.mu must be held.
func (s *Server) stateLocked() State {
	if s.state == "" {
		return StateNotInstalled
	}
	return s.state
}

// transitionLocked records a state change and returns the event to emit, or nil
// when nothing observable changed. De-duplication matters: a failed load and the
// supervisor both observe the same death, and the UI needs one event, not two.
// s.mu must be held, and the returned event must be emitted after releasing it.
func (s *Server) transitionLocked(next State, message string) *StateEvent {
	if s.stateLocked() == next && s.message == message {
		return nil
	}
	s.state = next
	s.message = message
	s.since = s.now()
	if next != StateLoaded {
		// The idle timer only ever runs against a loaded model.
		s.idle.disarm()
	}
	return &StateEvent{State: next, Port: s.port, Message: message}
}

// transition records a state change and emits its event.
func (s *Server) transition(next State, message string) {
	s.mu.Lock()
	event := s.transitionLocked(next, message)
	s.mu.Unlock()
	s.emit(event)
}

// emit delivers one transition to the callback seam, outside the state lock: an
// OnState implementation is allowed to read the server's state.
func (s *Server) emit(event *StateEvent) {
	if event == nil || s.OnState == nil {
		return
	}
	s.OnState(*event)
}

func (s *Server) readManifest() (Manifest, error) {
	path, err := s.Layout.ManifestPath()
	if err != nil {
		return Manifest{}, fmt.Errorf("embeddedllm: %w", err)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (s *Server) logger() *slog.Logger {
	if s != nil && s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) hostOS() string {
	if s.HostOS != "" {
		return s.HostOS
	}
	return runtime.GOOS
}

func (s *Server) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	// No overall Client.Timeout: each probe carries its own ProbeTimeout
	// context, and the client is shared across a load that may last minutes.
	return &http.Client{}
}

func (s *Server) readyTimeout() time.Duration {
	if s.ReadyTimeout > 0 {
		return s.ReadyTimeout
	}
	return DefaultReadyTimeout
}

func (s *Server) pollInterval() time.Duration {
	if s.ReadyPollInterval > 0 {
		return s.ReadyPollInterval
	}
	return DefaultReadyPollInterval
}

func (s *Server) probeTimeout() time.Duration {
	if s.ProbeTimeout > 0 {
		return s.ProbeTimeout
	}
	return DefaultProbeTimeout
}

func (s *Server) stopTimeout() time.Duration {
	if s.StopTimeout > 0 {
		return s.StopTimeout
	}
	return DefaultStopTimeout
}

func (s *Server) killWait() time.Duration {
	if s.killWaitFor > 0 {
		return s.killWaitFor
	}
	return defaultKillWait
}
