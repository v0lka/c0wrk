package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// The RPC surface of the embedded local model (see
// specs/domains/embedded-llm.md and specs/contracts/desktop-frontend.md).
//
// Ownership: this file owns the FrontendAPI-side wiring of
// core/embeddedllm — the supervisor, the installer, the config sink and the
// two global events. It owns NO policy: the command line, the resolution, the
// state machine and the idle budget all live in core. Two slices of the same
// surface live beside it: the wire shapes and their mappers in
// frontend_api_embedded_dto.go, and the operator tuning / device-probe RPCs in
// frontend_api_embedded_tuning.go.
//
// The conventions the surface follows:
//
//   - GetEmbeddedLLMStatus is a read-only getter and therefore returns no
//     error (desktop-frontend.md "RPC Surface"): an unavailable subsystem
//     reports the not-installed state instead of failing.
//   - every mutating method returns an error, and an error it returns is
//     actionable — it names the refused operation and the reason.
//   - the multi-gigabyte install runs in the BACKGROUND: InstallEmbeddedLLM
//     performs only the synchronous gates (single-run, hardware probe, the
//     combined memory refusal) and then returns, so the RPC never blocks on a
//     download. Progress arrives through embedded_llm:install_progress.
//   - download failures are split by resumability, and only the fatal half is
//     ever user-visible: a resumable transfer failure (a dropped connection,
//     an unreachable server, a retryable HTTP refusal) is retried SILENTLY
//     inside core/embeddedllm's Downloader — no toast, no error field, the
//     progress bar simply continues from the kept partial. A FATAL failure
//     (core's ErrAttemptsExhausted: no network after
//     embeddedllm.DefaultMaxFailedAttempts consecutive zero-progress attempts,
//     a checksum mismatch, the disk guard, …) is recorded as the
//     Settings-facing install_error (embeddedInstallFailureMessage translates
//     it) and raises the runtime_error toast with the same friendly text —
//     never the raw transport diagnostics. The status-bar indicator shows
//     NONE of them: its error surface is the supervisor's own error state.
//   - the background install is CANCELLABLE: CancelEmbeddedLLMInstall delivers
//     the operator's stop through the cancel published at gate-claim time, and
//     a REQUESTED cancellation (the flag AND the cancellation cause agreeing)
//     is the quiet outcome — no toast, no recorded error, the click is the
//     report — while core keeps the partial download as the resume point. A
//     genuine failure and a shutdown-cancellation stay reported failures.
//   - startup performs no network I/O and never loads the model
//     (initEmbeddedLLM); it only restores the state from manifest.json.

// Budgets of the synchronous parts of this surface. The install itself is
// unbounded (it is a multi-gigabyte resumable download on a background
// goroutine); these bound only what an RPC waits for.
const (
	// embeddedProbeTimeout bounds the hardware probe the install RPC runs
	// before it commits to a background download. Every external probe command
	// is already bounded inside core (2s each), so this is the outer belt: a
	// machine with every probe helper installed and wedged must still return
	// an actionable error instead of hanging the settings dialog.
	embeddedProbeTimeout = 30 * time.Second
	// embeddedStopTimeout bounds every supervisor stop this surface waits for:
	// the shutdown stop, UnloadEmbeddedLLM, the stop RemoveEmbeddedLLM runs
	// first, and the step-0 stop a background install performs before its first
	// numbered step. The install one travels to core as Installer.StopTimeout
	// rather than as a wrapped context, because the install runs on the
	// application context and its stop is the only call in that sequence nobody
	// else bounds. The supervisor's own graceful window is DefaultStopTimeout (10s)
	// followed by a kill and a bounded post-kill wait, so this only covers a
	// wedged terminate path — and, more importantly, the wait for the
	// single-instance gate an in-flight load holds for up to DefaultReadyTimeout
	// (minutes). Quitting must not hang on a model that refuses to die, a
	// blocking RPC must not hang its UI spinner for the length of a cold weight
	// load, and an install must not sit in step 0 for that same length while
	// holding the operation gate every other embedded call refuses behind —
	// step 0 precedes the plan, so it precedes the first install_progress event,
	// and the operator would be left watching a bar at 0%.
	//
	// This budget bounds the GATE WAIT, not the whole stop: when it expires with a
	// child still live, core's force path takes over, arms its own budget
	// (stopTimeout + killWait + 1s) on a detached context, terminates the child and
	// returns SUCCESS. So a stop-shaped call's real ceiling is this plus that force
	// budget, and the TRANSLATED "a load is still in progress" error — the one
	// embeddedBoundedStopErr produces, and it fires only on
	// errors.Is(err, context.DeadlineExceeded) — comes back only when no child had
	// spawned yet, the one case where there is nothing to kill and the gate timeout
	// is the whole truth. All four bounded paths can produce it, each on its own
	// channel: the shutdown stop logs it, UnloadEmbeddedLLM and RemoveEmbeddedLLM
	// return it to their caller, and an install carries it into its
	// background-failure toast.
	//
	// That scoping is deliberate: it is a claim about THIS translation, not about
	// every error a stop-shaped call can return. Two other paths produce actionable
	// errors with a child very much involved or none at all — a stop that did NOT
	// take is reported by core's terminate on BOTH the gated and the force path
	// ("did not exit within … of being killed", precisely the uninterruptible-
	// syscall case defaultKillWait exists for), and RemoveEmbeddedLLM's post-stop
	// work returns errors.Join over the three tree deletions plus Sink.ApplyRemoved,
	// so a read-only or busy tree yields an actionable error and no child was ever
	// part of it.
	embeddedStopTimeout = 30 * time.Second
)

// runtime_error codes of this subsystem (the payload's error_code field).
const (
	embeddedErrCodeInstall = "embedded_llm_install_failed"
	embeddedErrCodeRemove  = "embedded_llm_remove_failed"
)

// embeddedOpKind names the embedded-LLM operation that currently holds the
// busy gate. Its spelling is also the operator-facing noun the refusals use, so
// a refusal names the operation that is actually in flight instead of always
// blaming an install.
type embeddedOpKind string

const (
	// embeddedOpIdle is the zero value: no embedded operation is in flight and
	// the gate is free.
	embeddedOpIdle embeddedOpKind = ""
	// embeddedOpInstall is the background download/verify/provision run.
	embeddedOpInstall embeddedOpKind = "install"
	// embeddedOpRemove is the blocking stop + tree removal + config save.
	embeddedOpRemove embeddedOpKind = "removal"
)

// ---------------------------------------------------------------------------
// Subsystem state
// ---------------------------------------------------------------------------

// embeddedLLMState is the FrontendAPI-owned bookkeeping of the embedded local
// model: the storage layout, the supervisor, the installer, the cached install
// record and the operation-busy gate. It lives on FrontendAPI as the value
// field `embedded`, so the zero value is usable and no construction call is
// needed before the first RPC.
//
// TWO mutexes, deliberately separate:
//
//   - mu guards construction and the operation-gate bookkeeping. It is NEVER
//     held across a call into the supervisor: core calls OnState synchronously
//     from the transitioning goroutine and that handler reads the cached install
//     record, so holding mu there would deadlock on the first transition.
//   - infoMu guards the cached manifest snapshot, the restore-in-progress flag
//     and the last background failure. It is likewise never held across a
//     supervisor call.
//
// Neither mutex is ever taken while configMu is held, which keeps the lock
// order one-directional: (st.mu | st.infoMu) → configMu.
type embeddedLLMState struct {
	mu        sync.Mutex
	built     bool
	layout    embeddedllm.Layout
	server    *embeddedllm.Server
	installer *embeddedllm.Installer
	// loader is a LOCK-FREE snapshot of server, published the moment the
	// supervisor is constructed. It exists so the router build can reach the
	// supervisor without taking mu: ToBuilderConfig runs while configMu is held
	// at every call site, and mu must never be taken under configMu (the lock
	// order is one-directional, (st.mu | st.infoMu) → configMu). Readers get the
	// current supervisor or nil. Only embeddedBuild writes it, and it writes the
	// winner's instance, so a concurrent build publishes one supervisor.
	loader atomic.Pointer[embeddedllm.Server]
	// busyOp names the embedded operation currently holding the busy gate, or
	// embeddedOpIdle when the gate is free. It is the single-run gate for the
	// WHOLE subsystem, not just for installs: the installer is a plain struct
	// with no internal synchronization, and Install and Remove write and delete
	// the same staging/runtime/model trees. An install that starts mid-removal
	// therefore races the deletion for a half-staged tree (and can complete
	// after ApplyRemoved, silently re-adding what the user just removed), so
	// both directions claim the one gate and every gated entry point refuses
	// while it is held.
	busyOp embeddedOpKind
	// installCancel is the cancel of the cancellable context the CURRENT
	// background install runs under, published the moment the gate is claimed —
	// before the preflight, before the goroutine exists — so a
	// CancelEmbeddedLLMInstall click is honored at every point of the run,
	// including one racing the first progress event. nil when no install is in
	// flight. Published by embeddedBeginInstallRun; withdrawn (and CALLED, so
	// the context is released rather than living until the app context dies) by
	// embeddedEndInstallRun, which is the run's bookkeeping defer and the
	// preflight-refusal path both go through. Guarded by mu like the gate
	// bookkeeping beside it.
	installCancel context.CancelFunc
	// installCancelRequested records that the OPERATOR asked to cancel the
	// current install, so the run's failure branch can tell a REQUESTED
	// cancellation (this flag AND errors.Is(err, context.Canceled)) from a
	// genuine fault. The flag alone must not silence anything: a run that
	// fails for an unrelated reason after the click stays a reported failure,
	// and the cause alone must not either — a shutdown of the application
	// context cancels the parent without anybody asking.
	installCancelRequested bool

	infoMu sync.Mutex
	// manifest is the cached manifest.json snapshot (the durable install
	// record). hasManifest reports whether it describes a real installation;
	// without it the cached value is the zero Manifest.
	manifest    embeddedllm.Manifest
	hasManifest bool
	// muted suppresses the state event of a supervisor transition the caller
	// is about to provoke, because the caller emits ONE explicit snapshot
	// right afterwards (the startup restore, an install completion, a
	// removal). Without it those operations would emit the same state twice.
	// It is only ever set and cleared by the goroutine performing the
	// operation — core calls OnState synchronously — so the window is
	// microseconds wide and never spans another caller's transition.
	muted bool
	// lastError carries the operator-friendly cause of the last failed INSTALL
	// run (fatal download failures included; a resumable one never fails the
	// run — core retries it silently). It surfaces as the status/event
	// install_error field, a Settings-only surface. Removal failures do NOT
	// land here: they are carried by their own RPC's rejected promise (and its
	// toast), and painting them into a field named after installs would lie
	// about which action failed. Cleared when the next install run starts.
	lastError string
	// launchedTuning is the fingerprint of the memory-plan overrides the
	// CURRENTLY RESIDENT process was launched with, recorded on the
	// supervisor's `loading` transition and cleared on every non-resident one.
	// hasLaunchedTuning says whether a fingerprint was recorded at all.
	//
	// It exists for one question the RPC surface has to answer honestly: "the
	// operator just saved a tuning change — is the model running with it?" A
	// tuning knob is a LAUNCH flag (`-c`, `-ngl`, `-ctk`, `-fit`, …), so the
	// answer is only knowable by comparing the live config against what the
	// running process was actually started with, and nothing else records that.
	// It is deliberately runtime state and NOT part of manifest.json: tuning is
	// an operator setting the config sink carries verbatim, and a second
	// persisted copy would be a second source of truth going stale on the first
	// edit (the same reason `Manifest.Plan` records the plan and not the
	// tuning). It is therefore empty after a restart, which is correct — after
	// a restart nothing is resident either.
	launchedTuning    string
	hasLaunchedTuning bool

	// Test seams. All are nil in production, where the core defaults run
	// (ProbeHardware, spawnOSServer, the supervisor's own readiness client and
	// the real Installer.Install). They exist so this package's tests can drive
	// a full install/load/stop cycle without a network, without the pinned
	// multi-gigabyte artifacts (whose SHA256 pins no fake download can
	// satisfy) and without a real llama-server. Mirrors fetchPaperOriginalFn
	// and gitStatusFn.
	probeFn    func(ctx context.Context, logger *slog.Logger) (embeddedllm.Hardware, error)
	spawnFn    embeddedllm.SpawnFunc
	httpClient *http.Client
	installFn  func(ctx context.Context, in *embeddedllm.Installer, opts embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error)
	// portProbeFn substitutes the loopback port prober used by the pre-spawn
	// port scan, so a test can stage a collision without occupying a real port.
	portProbeFn embeddedllm.PortProber
	// deviceProbeFn substitutes the accelerator-memory probe behind
	// ProbeEmbeddedLLMDevices, so a test can answer (or refuse, or wedge)
	// without a provisioned runtime to spawn. nil in production, where
	// embeddedllm.ProbeDevices runs — the SAME function the supervisor's
	// load-time hook is wired to, so an explicit re-probe and a load-time
	// re-plan can never disagree about the machine.
	deviceProbeFn func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool)
	// stopBudget overrides embeddedStopTimeout on the stop-shaped paths (the
	// shutdown stop, UnloadEmbeddedLLM, RemoveEmbeddedLLM's stop and the install's
	// step-0 stop). Zero in production, where the documented const runs; a test
	// tightens it so the deadline path — a gate an in-flight load holds for
	// minutes — is observable without waiting the real 30s.
	stopBudget time.Duration
}

// embeddedStopBudget resolves the budget every stop-shaped path hands core: the
// test override when one is set, the documented embeddedStopTimeout otherwise.
// Three of those paths wrap a context with it; the fourth — a background install's
// step-0 stop — passes it to core as Installer.StopTimeout, because the install's
// own context must stay deadline-free for the download.
//
// The bound is the whole point of the contract. Core's Stop acquires the
// supervisor's single-instance gate first, and an in-flight cold load holds that
// gate for up to DefaultReadyTimeout (15 min); the application context carries no
// deadline, so an unbounded stop would hang a blocking RPC — and a quit — for the
// length of the load. Bounding it is what makes core's force path reachable: a
// budget that expires terminates the child instead of merely reporting that the
// gate was busy.
func (f *FrontendAPI) embeddedStopBudget() time.Duration {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stopBudget > 0 {
		return st.stopBudget
	}
	return embeddedStopTimeout
}

// installRecord returns the cached manifest snapshot.
func (s *embeddedLLMState) installRecord() (embeddedllm.Manifest, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.manifest, s.hasManifest
}

// setInstallRecord replaces the cached manifest snapshot.
func (s *embeddedLLMState) setInstallRecord(m embeddedllm.Manifest, ok bool) {
	s.infoMu.Lock()
	s.manifest, s.hasManifest = m, ok
	s.infoMu.Unlock()
}

// muteStateEvent arms/clears the suppression of the next transition's event.
func (s *embeddedLLMState) muteStateEvent(v bool) {
	s.infoMu.Lock()
	s.muted = v
	s.infoMu.Unlock()
}

// stateEventMuted reports whether transition events are suppressed.
func (s *embeddedLLMState) stateEventMuted() bool {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.muted
}

// setError records the operator-friendly cause of a failed install run (see
// lastError for why removals never land here).
func (s *embeddedLLMState) setError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.infoMu.Lock()
	s.lastError = msg
	s.infoMu.Unlock()
}

// lastFailure returns the recorded install failure ("" when none).
func (s *embeddedLLMState) lastFailure() string {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.lastError
}

// setLaunchedTuning records the fingerprint of the overrides the process now
// starting was launched with. An empty fingerprint with ok=false clears the
// record, which is what every non-resident transition does.
func (s *embeddedLLMState) setLaunchedTuning(fingerprint string, ok bool) {
	s.infoMu.Lock()
	s.launchedTuning, s.hasLaunchedTuning = fingerprint, ok
	s.infoMu.Unlock()
}

// launchedTuningFingerprint returns the recorded launch fingerprint. ok is
// false when nothing was recorded — no resident process, or one this app
// instance did not start — and a caller MUST treat that as "unknown" rather
// than as "unchanged".
func (s *embeddedLLMState) launchedTuningFingerprint() (string, bool) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.launchedTuning, s.hasLaunchedTuning
}

// deviceProber returns the accelerator-memory prober for an explicit re-probe:
// the test seam when one is installed, the core function otherwise. Read at
// call time, so a test may install it after construction.
func (s *embeddedLLMState) deviceProber() func(ctx context.Context, binaryPath string, logger *slog.Logger) (embeddedllm.MemoryTopology, bool) {
	if s.deviceProbeFn != nil {
		return s.deviceProbeFn
	}
	return embeddedllm.ProbeDevices
}

// ---------------------------------------------------------------------------
// Construction, restore and teardown
// ---------------------------------------------------------------------------

// embeddedBuild returns the supervisor and the installer, constructing them on
// first use and restoring the on-disk state. Construction and restore are pure
// local work: no network, no hardware probe, no process spawn — which is what
// lets startup call it on the critical path and lets an early RPC call it
// safely.
func (f *FrontendAPI) embeddedBuild() (*embeddedllm.Server, *embeddedllm.Installer, error) {
	st := &f.embedded

	st.mu.Lock()
	if st.built {
		server, installer := st.server, st.installer
		st.mu.Unlock()
		return server, installer, nil
	}
	st.mu.Unlock()

	if f.agentDir == "" {
		return nil, nil, errors.New("the embedded LLM is unavailable: no agent directory")
	}
	// Roots come from the centralized path API; core never re-derives them.
	layout, err := embeddedllm.NewLayout(config.RuntimesDir(f.agentDir), config.EmbeddedModelDir(f.agentDir))
	if err != nil {
		return nil, nil, fmt.Errorf("embedded LLM storage layout: %w", err)
	}

	logger := f.log().With("subsystem", "embedded_llm")
	server := embeddedllm.NewServer(layout, logger)
	server.AutoUnload = f.embeddedAutoUnloadPolicy()
	server.OnState = f.onEmbeddedLLMState
	// The persisted port is a preference, not a reservation: it is re-checked
	// before every spawn and walks upward to the next free one, and a move is
	// written back to config so the generated provider base_url keeps matching
	// the socket the server bound.
	server.EnsurePort = f.embeddedEnsurePort
	// A load re-measures the accelerator before it commits to a launch shape,
	// so a GPU that appeared, disappeared or got a new driver since the install
	// is priced at launch instead of being served a shape computed for a
	// machine that no longer exists. The hook is fail-soft in core (a wedged or
	// absent probe launches the recorded plan), so wiring it can never make a
	// load fail — see Server.effectivePlan.
	server.ProbeDevices = embeddedllm.ProbeDevices
	// The launch shape must be planned with the tuning in force WHEN IT RUNS,
	// and the supervisor is built once and cached, so this is a function rather
	// than a value: it reads the live config on every load.
	server.Tuning = f.embeddedTuning
	// The effective context a ready server reports is written back to the
	// tier-1 llm.models override, which otherwise stays frozen at the install's
	// estimate and — because a tier-1 override shadows the tier-1.5 lazy probe —
	// could never be corrected by anything else.
	server.PersistContext = f.persistEmbeddedContext
	installer := embeddedllm.NewInstaller(layout, logger)
	installer.Sink = embeddedConfigSink{f: f}
	// The supervisor owns the process, so the install stop, the removal stop and
	// the shutdown stop are the same call — the signatures match by design.
	// Install stops too: a repair retires the runtime tree a resident server
	// executes from, and that server holds the memory the install's own gate is
	// about to price.
	installer.Stop = server.Stop
	// The install's step-0 stop is the one stop nobody else bounds: the install
	// runs on the never-deadlined application context, so the budget has to travel
	// with the seam instead of with a wrapped context. Without it a Load that
	// claimed the supervisor's single-instance gate before this install did would
	// park step 0 for the length of that load — up to DefaultReadyTimeout, 15 min —
	// and step 0 precedes plan, so the operator would watch an install bar at 0%
	// that never emits a progress event while the operation gate makes every other
	// embedded call refuse. Bounding it is also what makes core's force path
	// reachable: an expired budget terminates a resident child rather than waiting
	// the load out. runEmbeddedInstall re-resolves this onto its per-run copy, so a
	// tightened test seam is honoured after the first build.
	installer.StopTimeout = f.embeddedStopBudget()

	// Test seams override the core defaults; production leaves them nil.
	if st.probeFn != nil {
		installer.Probe = st.probeFn
	}
	if st.spawnFn != nil {
		server.Spawn = st.spawnFn
	}
	if st.httpClient != nil {
		server.HTTPClient = st.httpClient
	}

	f.embeddedRestore(server, layout, logger)

	st.mu.Lock()
	// A concurrent builder wins; both build the same thing from the same
	// agent dir, so discarding the loser is correct (and it never started a
	// process — construction is inert).
	if st.built {
		server, installer = st.server, st.installer
	} else {
		st.layout, st.server, st.installer, st.built = layout, server, installer, true
	}
	st.mu.Unlock()
	// Publish the winner's supervisor lock-free for the router build, which
	// runs under configMu and therefore must never take st.mu. Storing outside
	// the lock is deliberate: the value is already the settled winner, and a
	// concurrent identical store is idempotent.
	st.loader.Store(server)
	return server, installer, nil
}

// embeddedRestore reads manifest.json into memory and records whether the model
// is installed. This is the WHOLE of the startup work for this subsystem: it
// performs no download, no probe and no load, so startup neither depends on the
// network nor blocks on a multi-gigabyte weight load.
func (f *FrontendAPI) embeddedRestore(server *embeddedllm.Server, layout embeddedllm.Layout, logger *slog.Logger) {
	st := &f.embedded
	path, err := layout.ManifestPath()
	if err != nil {
		logger.Warn("embedded LLM state not restored: the manifest path is unusable", "error", err)
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		return
	}
	manifest, err := embeddedllm.ReadManifest(path)
	if err != nil {
		if !errors.Is(err, embeddedllm.ErrNotInstalled) {
			logger.Warn("embedded LLM manifest is unreadable; treating the model as not installed",
				"path", path, "error", err)
		}
		st.setInstallRecord(embeddedllm.Manifest{}, false)
		if f.embeddedConfig().Installed {
			// The bytes are gone but config.yaml still claims an install.
			// Nothing reconciles the mismatch away: the generated `embedded`
			// provider record is regenerated on EVERY config load from
			// embedded_llm.installed alone (LoadWithResult →
			// SyncEmbeddedLLMProvider), with no manifest read on that path, so
			// it survives until a Remove (ApplyRemoved) or a successful
			// reinstall clears it. Report the mismatch instead of silently
			// serving a model that cannot start.
			logger.Warn("embedded_llm.installed is true but no manifest was found; the model needs a reinstall",
				"path", path)
		}
		return
	}

	st.setInstallRecord(manifest, true)
	// ONE event for the restore: suppress the SetInstalled transition and emit
	// an explicit snapshot in initEmbeddedLLM, so a startup neither double-
	// emits nor stays silent when nothing is installed.
	st.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		logger.Warn("embedded LLM state restore refused", "error", err)
	}
	st.muteStateEvent(false)
	logger.Info("embedded LLM state restored from the manifest",
		"backend", manifest.Backend, "packing", manifest.Packing,
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"runtime_version", manifest.RuntimeVersion)
}

// initEmbeddedLLM restores the embedded local-model state and emits the initial
// embedded_llm:state snapshot. Called from desktop/startup_phases.go on the
// startup path (InitEmbeddedLLM). It never downloads, probes hardware or loads
// the model.
func (f *FrontendAPI) initEmbeddedLLM() {
	server, _, err := f.embeddedBuild()
	if err != nil {
		// Report the unusable subsystem rather than leaving the UI without an
		// initial snapshot: not_installed is the truth when there is no
		// supervisor to ask.
		f.log().Warn("embedded LLM subsystem unavailable", "error", err)
		f.emitEmbeddedLLMState(embeddedllm.StateNotInstalled, 0, err.Error())
		return
	}
	// The policy is re-applied from the live config: construction read it too,
	// but a config reload between construction and startup must still win.
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())

	snapshot := server.Status()
	// The router was first built inside NewApplication, which has no
	// FrontendAPI and therefore no loader seam. Re-attaching it here — during
	// the startup restore, before backend:ready and so before any session can
	// issue a request — is what makes a cold request to an idle-unloaded model
	// load it transparently instead of failing to connect. Skipped when nothing
	// is installed: there is no embedded provider entry to guard then, and a
	// router rebuild must not become a cost every machine pays at startup.
	if snapshot.State != embeddedllm.StateNotInstalled {
		f.rebuildRouterForEmbeddedTransport()
	}
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// stopEmbeddedLLM stops the supervised server if one is running (Shutdown). It
// is a no-op when the subsystem was never constructed — nothing can be running
// then — and idempotent, so calling it twice is safe.
//
// The stop is BOUNDED by embeddedStopBudget: core's Stop must first acquire the
// supervisor's single-instance gate, which an in-flight cold load holds for up to
// DefaultReadyTimeout, and the shutdown context Wails hands us carries no
// deadline. The bound is what keeps a quit from hanging for the length of a
// weight load, and it is what makes core's force path reachable — an expired
// budget terminates the child instead of merely reporting a busy gate. That
// matters because the child is detached and the OS will NOT reclaim it when this
// process exits (see desktop.App.stopEmbeddedLLM).
func (f *FrontendAPI) stopEmbeddedLLM(ctx context.Context) error {
	st := &f.embedded
	st.mu.Lock()
	server := st.server
	st.mu.Unlock()
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget := f.embeddedStopBudget()
	stopCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := server.Stop(stopCtx); err != nil {
		// A deadline here has exactly one likely cause worth putting in the log:
		// the supervisor's single-instance gate was still held by an in-flight
		// load when the budget ran out. Naming it matters because the child is
		// DETACHED — an unstopped llama-server outlives this process, keeps its
		// gigabytes and its loopback port, and nothing persisted identifies it.
		// Unlike the RPC paths there is no "try again" to advise: this is a quit.
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf(
				"the embedded LLM server did not stop within %s, most likely because a load is still in progress: %w",
				budget, err)
		}
		f.log().Error("failed to stop the embedded LLM server",
			"error", err, "budget", budget)
		return err
	}
	return nil
}

// embeddedAutoUnloadPolicy resolves the operator's idle policy from config.
func (f *FrontendAPI) embeddedAutoUnloadPolicy() embeddedllm.AutoUnload {
	cfg := f.embeddedConfig()
	return embeddedllm.NewAutoUnload(cfg.AutoUnload.IsEnabled(), cfg.AutoUnload.IdleMinutes())
}

// embeddedConfig returns a copy of the persisted embedded_llm section. A copy
// (not a pointer) so the caller never touches config state outside configMu.
func (f *FrontendAPI) embeddedConfig() config.EmbeddedLLMConfig {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return config.EmbeddedLLMConfig{}
	}
	return f.config.EmbeddedLLM
}

// embeddedEnsurePort is the supervisor's pre-spawn port check
// (Server.EnsurePort): it returns the first free loopback port at or above the
// persisted one, scanning upward one port at a time, and persists a move so the
// generated provider base_url keeps matching the socket the server is about to
// bind.
//
// A persistence failure is deliberately NOT fatal. The server binds the free
// port either way and the ensure-loaded transport redirects every request to
// the live port, so the model stays usable while config.yaml simply keeps the
// previous value until the next successful write; failing here would make a
// multi-gigabyte weight load unusable over an administrative write.
func (f *FrontendAPI) embeddedEnsurePort(ctx context.Context, port int) (int, error) {
	free, err := embeddedllm.SearchFreePort(ctx, port, f.embeddedPortProber())
	if err != nil {
		return 0, err
	}
	if free == port {
		return free, nil
	}
	f.log().Warn("the persisted embedded LLM port is taken; moving to the next free one",
		"persisted", port, "port", free)
	if perr := f.persistEmbeddedPort(free); perr != nil {
		f.log().Warn("failed to persist the moved embedded LLM port",
			"port", free, "error", perr)
	}
	return free, nil
}

// embeddedPortProber returns the loopback prober for the port scan: the test
// seam when one is installed, nil otherwise (core resolves nil to the real bind
// probe). Read at call time, so a test may install it after construction.
func (f *FrontendAPI) embeddedPortProber() embeddedllm.PortProber {
	return f.embedded.portProbeFn
}

// persistEmbeddedPort writes a moved loopback port to embedded_llm.port and
// regenerates the backend-owned provider record from it, so the base_url every
// router build derives stays in agreement with the socket the server bound.
//
// manifest.json keeps the port the install allocated: that is the PREFERRED
// port and the next load re-scans from it, so a port that was only temporarily
// taken is reclaimed instead of drifting upward forever.
//
// It does NOT rebuild the router. This runs inside Server.Load — on the request
// path, while the supervisor's single-instance gate is held — and a rebuild
// there would swap the router underneath an in-flight request. None is needed:
// the entry's transport redirects to the live port, and the persisted value is
// what the next rebuild (a settings save, a profile change, a restart) picks up.
func (f *FrontendAPI) persistEmbeddedPort(port int) error {
	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync, and writing a
		// port into a section that reports "not installed" would read as
		// corrupt state to an operator. The load still proceeds on the free
		// port; only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	f.config.EmbeddedLLM.Port = port
	// contextWindow 0: a port move knows nothing about the resolved RAM tier, so
	// the existing llm.models override is left exactly as the install wrote it.
	f.config.SyncEmbeddedLLMProvider(0)

	return sink.saveOrRollback(previousLLM, previousEmbedded)
}

// embeddedTuning resolves the operator's memory-plan overrides
// (embedded_llm.tuning) for a launch. It is wired onto Server.Tuning, which
// calls it on the load path, so it reads the live config rather than a snapshot
// taken when the supervisor was built.
//
// A translation failure is fail-soft and yields the zero Tuning — which IS the
// documented all-Auto plan — because a load must not fail over a config surface
// it did not write. It should be unreachable: validate() checks the same section
// through the same ToTuning call, so a config that loaded translates. Reaching
// this branch means the in-memory config was mutated past validation, and the
// all-Auto plan is the safe answer for that, not a refusal.
func (f *FrontendAPI) embeddedTuning() embeddedllm.Tuning {
	tuning, err := f.embeddedConfig().Tuning.ToTuning()
	if err != nil {
		f.log().Debug("the embedded LLM tuning could not be translated; launching the all-Auto plan",
			"error", err)
		return embeddedllm.Tuning{}
	}
	return tuning
}

// persistEmbeddedContext writes the effective context a READY server reported
// back into the tier-1 llm.models."Bonsai 2 27B".context_window override. It is
// the context analogue of persistEmbeddedPort and runs in the same place: inside
// Server.Load, after readiness, while the supervisor's single-instance gate is
// held.
//
// Why the load path and not GetConfig: the override must stay honest, and
// llm-providers.md gives a tier-1 config override precedence over the tier-1.5
// lazy probe — so a stale override can never be corrected by probing the model
// later. The load path is the only one that already has a resident server to
// ask, and it keeps GetConfig network-free.
//
// It does not rebuild the router SYNCHRONOUSLY: this runs inside Load, usually
// on behalf of an in-flight request the ensure-loaded transport is waiting on,
// which holds the supervisor's single-instance gate and the saveMu a rebuild
// needs. But unlike a port change — the transport redirects to the live port,
// so no rebuild is ever needed there — the previous CONTEXT is not "still
// valid": it is the install-time DefaultFitMinContext estimate, which
// under-represents the server and makes the router's pre-call guard refuse
// every prompt the real window would accept (observed as
// "context window exceeded ... allows 31129 (context_window=65536)" on a
// machine whose fit-sized server actually ran 262144). So a successful persist
// SCHEDULES rebuildAfterEmbeddedConfigChange — which also pushes the fresh
// metadata into every live per-session model registry — through
// scheduleEmbeddedRouterRefresh; the refresh runs on its own goroutine once
// the locks below are released. The request that triggered the load is the one
// request that still sees the stale window: its validation already ran before
// the transport dispatched. Everything after it is corrected without a restart.
//
// A persistence failure is deliberately NOT fatal: it is returned to core, which
// logs it and keeps the load successful, because the model is resident and
// serving and the manifest already carries the corrected value, so the next load
// retries. The value is range-guarded on both sides before it is written — see
// config.MaxModelContextWindow.
func (f *FrontendAPI) persistEmbeddedContext(_ context.Context, contextSize int) error {
	if contextSize <= 0 {
		// Core only calls this with a value it read off the server, but the
		// override treats <= 0 as "leave the existing one alone", and a
		// non-positive window would fail validate() on the next config load.
		return nil
	}
	if contextSize > config.MaxModelContextWindow {
		// The CEILING side of the same guard, and the reason it is a range
		// rather than a sign check. This is the one context figure in the
		// subsystem that arrives from an external process over HTTP, and the
		// override it lands in is a TIER-1 value that shadows the tier-1.5 lazy
		// probe — so a poisoned readback (a squatted loopback port answering
		// /props with an absurd n_ctx, or a perSlot*slots product that wrapped)
		// would otherwise be persisted durably, in TWO stores, and never
		// corrected: an enormous window makes every prompt "fit", so context
		// compaction silently stops triggering for this model.
		//
		// Skipping the write is the fail-soft answer, matching the <= 0 side:
		// the load stays successful, the model is resident and serving, and the
		// next load retries the readback.
		f.log().Warn("ignoring an out-of-range embedded LLM context readback",
			"context_size", contextSize, "max", config.MaxModelContextWindow)
		return nil
	}

	st := &f.embedded
	// Mirror the corrected value into the cached install record: core already
	// rewrote manifest.json before calling, and GetEmbeddedLLMStatus answers
	// from this cache, so without the mirror the UI would keep reporting the
	// install's estimate beside a config that carries the measured one.
	if record, ok := st.installRecord(); ok && record.ContextSize != contextSize {
		record.ContextSize = contextSize
		st.setInstallRecord(record, true)
	}

	sink := embeddedConfigSink{f: f}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	if !f.config.EmbeddedLLM.Installed {
		// No generated provider record exists to keep in sync. The load still
		// proceeds; only the config write is skipped.
		f.configMu.Unlock()
		return nil
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	// The port is already authoritative in the section, so this regenerates the
	// provider record from it (a no-op when it agrees) and records the context
	// window. A false return means every backend-owned value already matches —
	// the common case on a steady machine — so a value that did not move
	// produces no write and no config:updated event.
	if !f.config.SyncEmbeddedLLMProvider(contextSize) {
		f.configMu.Unlock()
		return nil
	}

	if err := sink.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	// The corrected window also reaches the LIVE sessions' emitters. The
	// scheduled refresh below updates their model REGISTRIES, but an emitter
	// caches its display basis at HandleMessage start and only re-resolves it
	// on the next message — an idle session (or the tail of a task that
	// started before this correction) would keep showing fill percentages and
	// "Compacted from X% to Y%" cards against the stale window. The emitter
	// push is model-scoped and re-broadcasts a corrected context_fill for
	// idle sessions, so the status bar heals immediately.
	f.pushDisplayContextWindow(contextSize)

	// The corrected window is durable and config:updated is on its way.
	// Schedule the async refresh so the live router stops guarding prompts
	// with the stale estimate — see scheduleEmbeddedRouterRefresh for why it
	// must not run synchronously on this path.
	f.scheduleEmbeddedRouterRefresh()
	return nil
}

// pushDisplayContextWindow forwards the measured embedded context window to
// the live session emitters (session.Manager.SetDisplayContextWindowForModel),
// model-scoped to the embedded model. Best-effort: a nil app/manager (tests,
// early startup) skips the push — the registry push and the next
// HandleMessage's re-resolution remain the authoritative correction paths,
// exactly like a skipped router rebuild.
func (f *FrontendAPI) pushDisplayContextWindow(contextSize int) {
	push := f.displayWindowPush
	if push == nil {
		if f.app == nil || f.app.Manager() == nil {
			return
		}
		push = f.app.Manager().SetDisplayContextWindowForModel
	}
	push(config.EmbeddedLLMModelName, contextSize)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// onEmbeddedLLMState is the core supervisor's StateEvent transport: it forwards
// every observable transition as the global embedded_llm:state event. It runs
// on the goroutine that made the transition and must never call back into the
// supervisor (core documents OnState as non-reentrant), which is why it only
// reads the cached install record and the config.
func (f *FrontendAPI) onEmbeddedLLMState(ev embeddedllm.StateEvent) {
	// Recorded BEFORE the mute check: muting suppresses an EVENT the caller is
	// about to replace with an explicit snapshot, not the bookkeeping behind
	// ReloadRequired. In production no muted transition is a load, so the order
	// only matters for the invariant, not for the outcome.
	f.noteEmbeddedLaunchTuning(ev.State)

	if f.embedded.stateEventMuted() {
		return
	}
	f.emitEmbeddedLLMState(ev.State, ev.Port, ev.Message)
}

// emitEmbeddedLLMState builds and emits the global embedded_llm:state payload.
// state/port/message come from the transition (or the startup snapshot); the
// install record and the auto-unload budget come from the cached manifest and
// the persisted config. Nil-guarded like every other emitter: most tests do not
// wire emitEvent.
func (f *FrontendAPI) emitEmbeddedLLMState(state embeddedllm.State, port int, message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMState, f.embeddedStatePayload(state, port, message))
}

// embeddedStatePayload merges a supervision state with the install record and
// the persisted config. Precedence is explicit: the live supervisor port wins
// over the persisted one, and the manifest (what is actually on disk) wins over
// the informational config copy of packing/backend.
//
// The two error fields are deliberately split: Error carries the supervisor's
// own cause and only while the state IS error (a failed launch, a dead process)
// — it is what the status-bar indicator renders. InstallError carries the last
// failed install run and is a Settings-only surface: a background download that
// is still retrying silently never paints the bar, and a fatal one is acted on
// in Settings, where the retry button lives.
func (f *FrontendAPI) embeddedStatePayload(state embeddedllm.State, port int, message string) EmbeddedLLMStateData {
	manifest, hasManifest := f.embedded.installRecord()
	cfg := f.embeddedConfig()

	packing, backend := cfg.Packing, cfg.Backend
	contextSize := 0
	if hasManifest {
		packing = string(manifest.Packing)
		backend = string(manifest.Backend)
		contextSize = manifest.ContextSize
	}
	if port <= 0 {
		port = cfg.Port
	}
	if port <= 0 && hasManifest {
		port = manifest.Port
	}

	errText := message
	if state != embeddedllm.StateError {
		errText = ""
	}

	return EmbeddedLLMStateData{
		Installed:         state != embeddedllm.StateNotInstalled,
		Loading:           state == embeddedllm.StateLoading,
		Loaded:            state == embeddedllm.StateLoaded,
		Packing:           packing,
		Backend:           backend,
		Port:              port,
		ContextSize:       contextSize,
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		Error:             errText,
		InstallError:      f.embedded.lastFailure(),
	}
}

// emitEmbeddedInstallProgress forwards one core Progress update as the global
// embedded_llm:install_progress event. It is the InstallOptions.Progress
// callback of a background install run, so it is called from that goroutine and
// must not block: the emit is a synchronous Wails dispatch, and the core
// downloader already throttles the byte-level updates.
func (f *FrontendAPI) emitEmbeddedInstallProgress(p embeddedllm.Progress) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventEmbeddedLLMInstallProgress, EmbeddedLLMProgressData{
		Component:  string(p.Component),
		Stage:      p.Stage,
		BytesDone:  p.BytesDone,
		BytesTotal: p.BytesTotal,
	})
}

// emitEmbeddedRuntimeError raises the user-visible toast for a background
// failure — the case where no RPC is left to carry the error. Mirrors the
// existing runtime_error emitters (id, message, error_code).
func (f *FrontendAPI) emitEmbeddedRuntimeError(code, message string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": code,
	})
}

// ---------------------------------------------------------------------------
// RPC surface
// ---------------------------------------------------------------------------

// GetEmbeddedLLMStatus returns the embedded local-model status: the supervision
// state, the install record (including the RECORDED device topology, the launch
// plan it informed and its notes), the resolved auto-unload policy and whether a
// persisted tuning change is waiting for the next load.
//
// Read-only getter, so it returns no error (desktop-frontend.md convention):
// when the subsystem cannot be constructed (no agent directory — only possible
// before startup) it reports Available=false with the not-installed state
// instead of failing. It performs no network I/O and no hardware probe — the
// topology and the plan it reports are the ones the install RECORDED, and a
// measurement of this instant is ProbeEmbeddedLLMDevices. The only disk access
// is the manifest restore on the first call.
func (f *FrontendAPI) GetEmbeddedLLMStatus() EmbeddedLLMStatus {
	cfg := f.embeddedConfig()
	status := EmbeddedLLMStatus{
		State:             string(embeddedllm.StateNotInstalled),
		Installed:         cfg.Installed,
		Packing:           cfg.Packing,
		Backend:           cfg.Backend,
		Port:              cfg.Port,
		AutoUnloadEnabled: cfg.AutoUnload.IsEnabled(),
		AutoUnloadMinutes: cfg.AutoUnload.IdleMinutes(),
		RuntimeVersion:    cfg.RuntimeVersion,
		InstalledAt:       cfg.InstalledAt,
		ModelFile:         cfg.ModelFile,
		ModelName:         config.EmbeddedLLMModelName,
		// Error stays empty unless the supervisor itself reports an error state
		// (set from the snapshot below): the status-bar indicator renders it,
		// and an install failure is a Settings-only surface (InstallError).
		Error:        "",
		InstallError: f.embedded.lastFailure(),
		Guards:       []EmbeddedLLMGuard{},
		Devices:      []EmbeddedLLMDevice{},
		Plan:         embeddedPlanDTO(nil),
	}
	if manifest, ok := f.embedded.installRecord(); ok {
		// The manifest is what is actually on disk; the config copy of
		// packing/backend is informational. The port falls back to it as well,
		// so a status read still reports the endpoint the supervisor would
		// launch even before the config was synced.
		status.Packing = string(manifest.Packing)
		status.Backend = string(manifest.Backend)
		status.ContextSize = manifest.ContextSize
		// The degradation record travels with the manifest, not the config: it
		// describes the install that produced these bytes, and config.yaml is
		// not where a support bundle looks for it.
		status.PackingReason = string(manifest.PackingReason)
		status.GPUFamily = string(manifest.GPUFamily)
		status.Guards = embeddedGuardsDTO(manifest.Guards)
		// The measured topology and the launch shape it informed are recorded
		// beside the bytes they describe, for the same reason: they are facts
		// about THIS install, not tuning a config section carries. A nil
		// Topology (no probe ever answered) leaves the zero values in place,
		// which is why TopologyProbedAt — not the empty Devices array — is what
		// a reader checks for "unknown".
		if manifest.Topology != nil {
			topology := embeddedDevicesDTO(*manifest.Topology)
			status.Devices = topology.Devices
			status.Unified = topology.Unified
			status.HostRAMGiB = topology.HostRAMGiB
			status.DeviceBudgetMiB = topology.DeviceBudgetMiB
			status.HostBudgetMiB = topology.HostBudgetMiB
			status.TopologyProbedAt = topology.ProbedAt
		}
		status.Plan = embeddedPlanDTO(manifest.Plan)
		if status.Port <= 0 {
			status.Port = manifest.Port
		}
		if status.ModelFile == "" {
			status.ModelFile = manifest.ModelFile
		}
		if status.RuntimeVersion == "" {
			status.RuntimeVersion = embeddedllm.RuntimeTag
		}
		if status.InstalledAt == "" {
			status.InstalledAt = manifest.InstalledAt
		}
	}

	server, _, err := f.embeddedBuild()
	if err != nil {
		f.log().Debug("embedded LLM status without a supervisor", "error", err)
		if status.Installed && status.Port > 0 {
			status.BaseURL = embeddedBaseURL(status.Port)
			status.ModelID = embeddedCompositeModelID()
		}
		return status
	}
	status.Available = true

	snapshot := server.Status()
	status.State = string(snapshot.State)
	status.Installed = snapshot.State != embeddedllm.StateNotInstalled
	status.Loading = snapshot.State == embeddedllm.StateLoading
	status.Loaded = snapshot.State == embeddedllm.StateLoaded
	status.Pid = snapshot.Pid
	status.FitWarning = snapshot.FitWarning
	if snapshot.State == embeddedllm.StateError {
		status.Error = snapshot.Message
	}
	if snapshot.Port > 0 {
		status.Port = snapshot.Port
	}
	status.Installing = f.embeddedInstalling()
	if status.Installed && status.Port > 0 {
		status.BaseURL = embeddedBaseURL(status.Port)
	}
	if status.Installed {
		status.ModelID = embeddedCompositeModelID()
	} else {
		status.ModelID = ""
		status.BaseURL = ""
	}
	if remaining, armed := server.IdleRemaining(); armed && remaining > 0 {
		status.IdleRemainingSeconds = int64(remaining / time.Second)
	}
	// Computed LAST, from the live supervision state: a tuning change only ever
	// reaches a process that has not been spawned yet, so the question is
	// whether the resident one was started with the overrides now on disk.
	status.ReloadRequired = f.embeddedTuningReloadRequired(status.Loading, status.Loaded)
	return status
}

// InstallEmbeddedLLM provisions the pinned runtime and weights for this machine.
//
// The synchronous part is only the gates: one install at a time, a bounded
// hardware probe, and the combined memory refusal — so a machine whose memory
// cannot hold the model gets an actionable error from THIS call and not one
// byte is downloaded. Everything heavy (the multi-gigabyte resumable download,
// verification, extraction, the macOS provisioning and smoke test) then runs on
// a background goroutine: the RPC returns as soon as the run is started.
//
// Progress arrives as embedded_llm:install_progress (one event per component
// and stage) and the outcome as embedded_llm:state. A download failure is
// visible only when it is FATAL: resumable transfer failures are retried
// silently inside core (the bar resumes from the kept partial, nothing is
// reported), while a fatal one records the friendly install_error line and
// raises a runtime_error toast with the same text, because no RPC is left to
// carry it. The install is resumable: verified artifacts are kept as cache
// hits, so a retry continues where the previous attempt stopped.
//
// The run is CANCELLABLE: CancelEmbeddedLLMInstall cancels the context the
// background goroutine runs under, and a REQUESTED cancellation is the quiet
// outcome — no toast, no recorded error, an Info log and a state event —
// because the click is the report, and core keeps the partial bytes as the
// resume point for the retry. A cancellation nobody asked for (the application
// context dying at shutdown) and a genuine failure stay reported failures.
//
// The background run's first act is core's step-0 stop of a resident server: a
// repair retires the runtime tree that server executes from — refused outright on
// Windows while the process is live — and it holds the gigabytes step 2's memory
// gate is about to price. That stop is bounded by the same embeddedStopTimeout
// every other stop-shaped path hands core (delivered as Installer.StopTimeout,
// since the install's own context must stay deadline-free for the download), so a
// repair clicked during a cold load either takes the model down through core's
// force path and proceeds, or fails with the translated "a load is still in
// progress" error. What it never does is park the progress bar at 0% — step 0
// precedes the plan, and therefore the first progress event — for the length of
// that load while holding the operation gate.
func (f *FrontendAPI) InstallEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if claimed, holder := f.beginEmbeddedOperation(embeddedOpInstall); !claimed {
		return embeddedBusyRefusal(holder, embeddedOpInstall, "starting an install")
	}

	// The run's context is a CANCELLABLE child of the application context, and
	// its cancel is published the moment the gate is claimed — before the
	// preflight and before the goroutine exists — so a cancellation click is
	// honored at every point of the run. A preflight refusal withdraws the
	// publication through refuseEmbeddedInstall (embeddedEndInstallRun), which
	// also calls the cancel, so no path leaks the context.
	installCtx, cancelInstall := context.WithCancel(f.ctx())
	f.embeddedBeginInstallRun(cancelInstall)
	// A new run retires the previous run's failure line: the retry that line
	// was asking for is happening right now, and a stale install error beside
	// a live progress bar is a contradiction (the store's beginInstall clears
	// the action error for the same reason).
	f.embedded.setError(nil)

	// The MEMORY GATE runs HERE, synchronously: a refusal the caller only
	// learns about from a toast ten minutes into a download is not actionable.
	// The probe is local and bounded (each external helper carries its own 2s
	// budget inside core). A synchronous refusal returns the error to the caller
	// and raises NO toast: the rejected promise is the report, and a toast on top
	// of it would say it twice.
	//
	// This is the SAME gate Resolve runs first, so the two cannot disagree — it
	// is exposed rather than re-derived here precisely so that a click and the
	// background install answer with one voice. It prices both memory pools
	// instead of a flat RAM floor: an unreadable ACCELERATOR budget degrades
	// rather than refuses, while an unreadable RAM total still does
	// (ErrRAMUnknown), because the host budget is derived from it on every path.
	hw, err := f.embeddedInstallPreflight()
	if err != nil {
		return err
	}

	go f.runEmbeddedInstall(installCtx, server, installer, hw)
	return nil
}

// CancelEmbeddedLLMInstall asks the in-flight background install to stop.
//
// IDEMPOTENT: with no install in flight it is a success no-op, because the
// outcome the operator wants — no install running — already holds; a late
// double click must not turn into an error. It never CLAIMS the operation gate
// (it is a reader of the run's bookkeeping, not a second operation): claiming
// would make a cancel click refuse against the very install it is meant to
// stop, and the run it addresses releases the gate itself through its own
// defer.
//
// What it does is call the run's published cancel. Core wraps the cancellation
// as context.Canceled through Install and the downloader, KEEPS the partial
// bytes as the resume point, and the run's failure branch turns the requested
// cancellation into the quiet outcome: no toast and no recorded error — the
// click is the report — plus a state event and an Info log. A retry continues
// from the partial download instead of starting over.
//
// The stop is COOPERATIVE and asynchronous: the RPC returns once the request
// is delivered, not once the run has unwound — the gate stays held (and every
// other embedded RPC keeps refusing) until the background goroutine finishes
// its own cleanup.
func (f *FrontendAPI) CancelEmbeddedLLMInstall() error {
	st := &f.embedded
	st.mu.Lock()
	cancel := st.installCancel
	if cancel != nil {
		// Recorded BEFORE the cancel fires, so the run's failure branch — which
		// may race this very call — cannot observe the cancellation cause
		// without the flag that makes it quiet.
		st.installCancelRequested = true
	}
	st.mu.Unlock()
	if cancel == nil {
		return nil
	}
	f.log().Info("cancelling the embedded LLM install at the operator's request")
	cancel()
	return nil
}

// embeddedInstallPreflight runs the two synchronous install gates — the bounded
// hardware probe and the combined memory refusal — for a caller that already
// holds the operation gate. Every failure path releases that gate through
// refuseEmbeddedInstall before returning, including a PANIC in either gate: the
// background run's release defer does not exist until its goroutine starts, so
// an unguarded panic here would leave the gate claimed for the process lifetime
// and every embedded RPC refusing until a restart — the same unrecoverable
// outcome runEmbeddedInstall's own recover exists to prevent.
func (f *FrontendAPI) embeddedInstallPreflight() (hw embeddedllm.Hardware, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("the embedded LLM install panicked before it started: %v", r)
			f.refuseEmbeddedInstall(panicErr)
			hw, err = embeddedllm.Hardware{}, panicErr
		}
	}()
	probeCtx, cancel := context.WithTimeout(f.ctx(), embeddedProbeTimeout)
	defer cancel()
	hw, err = f.embeddedProbe(probeCtx)
	if err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return hw, refusal
	}
	if err := embeddedllm.CheckMemoryBudget(embeddedllm.ResolveInput{
		MachineProfile: embeddedllm.MachineProfile{
			Platform:       hw.Platform,
			Backend:        hw.Backend,
			RAMGiB:         hw.RAMGiB,
			CUDA12Userland: hw.CUDA12Userland,
		},
	}); err != nil {
		refusal := fmt.Errorf("the embedded LLM install was refused: %w", err)
		f.refuseEmbeddedInstall(refusal)
		return hw, refusal
	}
	return hw, nil
}

// RemoveEmbeddedLLM stops a running server and removes the runtime, the weights
// and the manifest, then clears the backend-owned provider record (migrating
// llm.default_model off the embedded composite in the same operation).
//
// Blocking, and BOUNDED: the work is local file removal plus a config save, but
// it starts with a supervisor stop that must first acquire the single-instance
// gate — which an in-flight load holds for up to the supervisor's ready budget
// (minutes). The app context has no deadline, so the stop runs under
// embeddedStopTimeout instead. That budget bounds the GATE WAIT, not the whole
// stop: when it expires with a child still live, core's force path arms its own
// budget, terminates the child and returns success, so the call finishes bounded
// (rather than hanging the RPC and its UI spinner for the length of a cold weight
// load) and the TRANSLATED "a load is still in progress" error comes back only
// when no child had spawned yet.
//
// It is NOT the only actionable error this call returns. A stop that did not take
// is reported by core's terminate, and the work that FOLLOWS the stop — the three
// tree deletions and Sink.ApplyRemoved, joined — returns its own errors when the
// tree is read-only or busy, with no child involved at all.
//
// It also holds the embedded-operation gate for its whole duration, so an
// install cannot start mid-removal and race the tree deletion (see
// embeddedLLMState.busyOp). Verified artifacts of an unrelated component are
// never touched, and the flat embedding-model files sharing <agentDir>/models
// survive.
func (f *FrontendAPI) RemoveEmbeddedLLM() error {
	server, installer, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	claimed, holder := f.beginEmbeddedOperation(embeddedOpRemove)
	if !claimed {
		return embeddedBusyRefusal(holder, embeddedOpRemove, "removing the model")
	}
	defer f.endEmbeddedOperation()
	f.embedded.setError(nil)

	budget := f.embeddedStopBudget()
	removeCtx, cancel := context.WithTimeout(f.ctx(), budget)
	defer cancel()
	if err := installer.Remove(removeCtx); err != nil {
		err = embeddedBoundedStopErr("the local model removal", err, budget)
		// The failure is carried by THIS call's rejected promise (the Settings
		// error line) and the toast below — deliberately NOT recorded as the
		// install failure, which it is not (see embeddedLLMState.lastError).
		f.log().Error("embedded LLM removal failed", "error", err)
		f.emitEmbeddedRuntimeError(embeddedErrCodeRemove,
			"The local model could not be removed: "+err.Error())
		f.emitEmbeddedStateFrom(server)
		return err
	}

	// The bytes and the manifest are gone; the supervisor must not keep
	// reporting an installation that no longer exists.
	f.embedded.setInstallRecord(embeddedllm.Manifest{}, false)
	f.embedded.muteStateEvent(true)
	err = server.SetInstalled(false)
	f.embedded.muteStateEvent(false)
	f.emitEmbeddedStateFrom(server)
	if err != nil {
		// Only reachable when a process is still live, which Remove's own stop
		// should have made impossible. Report it honestly rather than claiming
		// the model is gone while it holds gigabytes.
		f.embedded.setError(err)
		return fmt.Errorf("the local model was removed from disk but its server is still running: %w", err)
	}
	return nil
}

// LoadEmbeddedLLM starts the local server and blocks until the model can
// actually answer — readiness is a non-empty /v1/models response, never a
// health check. It is idempotent (a loaded model is a no-op) and single-
// instance: concurrent calls cannot start a second process.
//
// Blocking on purpose: a load is an explicit user action whose outcome the
// caller needs, and the weight load it waits for is bounded by the supervisor's
// ready budget. The embedded_llm:state events (loading → loaded | error) let
// the UI show progress while the promise is pending, and the idle budget starts
// only once the load completes, so a slow load never eats it.
func (f *FrontendAPI) LoadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "loading the model")
	}
	f.embedded.setError(nil)

	if err := server.Load(f.ctx()); err != nil {
		if errors.Is(err, embeddedllm.ErrNotInstalled) {
			return errors.New("the embedded LLM is not installed — install it before loading it")
		}
		// The state event already carries the supervisor's cause (including the
		// tail of the server's own log); return the same detail to the caller.
		return err
	}
	return nil
}

// UnloadEmbeddedLLM stops the server process, deterministically returning its
// RAM/VRAM. The bytes stay on disk, so the state afterwards is "installed"
// (unloaded) and a later Load restarts it without a download.
//
// Blocking, idempotent and BOUNDED: unloading a model that is not resident
// succeeds. A stop that did not take is reported as an error and keeps the
// state honest (error) instead of claiming the memory was released. The
// supervisor's single-instance gate is held by an in-flight load for up to its
// ready budget (minutes) and the app context carries no deadline, so the stop
// runs under embeddedStopTimeout. That budget bounds the GATE WAIT, not the whole
// stop: when it expires with a child still live, core's force path arms its own
// budget, terminates the child and returns success, so the RPC and its UI spinner
// stay bounded instead of hanging for the length of a cold weight load, and the
// TRANSLATED "a load is still in progress" error comes back only when no child
// had spawned yet — the one case where the gate timeout is the whole truth. The
// stop that did not take, described above, stays an error of its own on BOTH the
// gated and the force path; the scoping is about that one translation, not about
// every error this RPC can return.
//
// Refused while an install or removal holds the subsystem-wide operation gate,
// for uniformity with Load, the probe and the request-path loader: during those
// operations nothing can be resident (the holder's own step-0 or removal stop
// already ran), so the unload would be a no-op — but one refusal rule for every
// reader keeps the contract a single sentence and stays correct if an install
// flow ever ends by auto-loading.
func (f *FrontendAPI) UnloadEmbeddedLLM() error {
	server, _, err := f.embeddedBuild()
	if err != nil {
		return err
	}
	if op := f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "unloading the model")
	}
	budget := f.embeddedStopBudget()
	unloadCtx, cancel := context.WithTimeout(f.ctx(), budget)
	defer cancel()
	if err := server.Unload(unloadCtx); err != nil {
		return embeddedBoundedStopErr("the local model unload", err, budget)
	}
	return nil
}

// embeddedBoundedStopErr translates a bounded stop/unload/removal failure into
// an actionable one. Only the deadline case is translated, and only because its
// cause is known: the supervisor's single-instance gate is still held by a load
// that started before this call, so the operator is told what the wait is and
// that retrying once the load settles is the fix. Every other failure is
// returned as core reported it. The original error stays wrapped so
// errors.Is(err, context.DeadlineExceeded) keeps working for callers.
func embeddedBoundedStopErr(op string, err error, budget time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf(
			"%s did not finish within %s, most likely because a load is still in progress: "+
				"wait for the model to report loaded or error, then try again: %w",
			op, budget, err)
	}
	return err
}

// SetEmbeddedLLMAutoUnload persists the idle-unload policy
// (embedded_llm.auto_unload) and applies it to the supervisor immediately: the
// timer is re-armed against the existing activity stamp while the model is
// loaded, and disarmed when the policy is off.
//
// minutes must be within 1..embeddedllm.MaxAutoUnloadMinutes — the same rule
// config validation enforces, so a rejected value never reaches config.yaml.
// The ceiling is not a tuning opinion: past it the minutes→nanoseconds multiply
// overflows int64 and the wrapped result can be a short positive budget, so an
// "effectively never" value would silently invert into "unload immediately".
// The setting is an operator preference, not install state: it survives a
// removal and applies to the next install.
func (f *FrontendAPI) SetEmbeddedLLMAutoUnload(enabled bool, minutes int) error {
	if minutes < 1 || minutes > embeddedllm.MaxAutoUnloadMinutes {
		return fmt.Errorf(
			"embedded_llm.auto_unload.minutes %d is not valid; must be within 1-%d",
			minutes, embeddedllm.MaxAutoUnloadMinutes)
	}

	server, _, err := f.embeddedBuild()
	if err != nil {
		// The subsystem being unavailable is not a reason to lose the
		// operator's setting: persist it, and report the supervisor problem.
		f.log().Warn("embedded LLM subsystem unavailable; persisting the auto-unload setting only", "error", err)
		server = nil
	}

	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	// Snapshot the whole section, not just AutoUnload: saveOrRollback is the
	// shared tail every embedded config write ends in, and it restores the pair
	// it is given. AutoUnload is the only field mutated here, and the mutation
	// REPLACES the struct with fresh pointers, so the snapshot's own pointers
	// are untouched by it.
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM
	enabledValue, minutesValue := enabled, minutes
	f.config.EmbeddedLLM.AutoUnload = config.AutoUnloadConfig{
		Enabled: &enabledValue,
		Minutes: &minutesValue,
	}
	// The shared tail: one save-or-rollback path rather than a per-method
	// variation on one. It persists under configMu (so a failed write restores
	// the exact prior value before any reader observes the new one — the
	// UpdateLLMConfig pattern), clears configLoadErrors like every sibling
	// write, releases configMu and emits config:updated.
	if err := (embeddedConfigSink{f: f}).saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}

	if server != nil {
		server.SetAutoUnload(embeddedllm.NewAutoUnload(enabled, minutes))
		f.emitEmbeddedStateFrom(server)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Background install run
// ---------------------------------------------------------------------------

// beginEmbeddedOperation claims the single embedded-operation gate for kind.
// claimed=false means another embedded operation (an install OR a removal) is
// already in flight and the caller must refuse; holder names it, read under the
// same lock acquisition so a refusal can never race the other operation's
// release and name nothing. See embeddedLLMState.busyOp for why the two
// directions share one gate.
func (f *FrontendAPI) beginEmbeddedOperation(kind embeddedOpKind) (claimed bool, holder embeddedOpKind) {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busyOp != embeddedOpIdle {
		return false, st.busyOp
	}
	st.busyOp = kind
	return true, embeddedOpIdle
}

// embeddedBusyOperation returns the kind of the embedded operation holding the
// gate, or embeddedOpIdle when it is free.
func (f *FrontendAPI) embeddedBusyOperation() embeddedOpKind {
	st := &f.embedded
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.busyOp
}

// embeddedInstalling reports whether a background INSTALL run is in flight.
//
// This is the narrow predicate, deliberately: it is what
// EmbeddedLLMStatus.Installing reports, and the frontend renders that field as
// the per-component install progress bar. A removal holds the same gate but
// must not light an install bar, so it is visible only through
// embeddedBusyOperation.
func (f *FrontendAPI) embeddedInstalling() bool {
	return f.embeddedBusyOperation() == embeddedOpInstall
}

// endEmbeddedOperation releases the embedded-operation gate.
func (f *FrontendAPI) endEmbeddedOperation() {
	st := &f.embedded
	st.mu.Lock()
	st.busyOp = embeddedOpIdle
	st.mu.Unlock()
}

// embeddedBeginInstallRun publishes the cancel of the install run that just
// claimed the gate. It runs BEFORE the preflight and before the goroutine
// starts, so a cancellation clicked while the synchronous gates are still
// running is delivered the moment the run begins: the context is already
// cancelled and core's Install fails immediately with the quiet outcome.
func (f *FrontendAPI) embeddedBeginInstallRun(cancel context.CancelFunc) {
	st := &f.embedded
	st.mu.Lock()
	st.installCancel = cancel
	st.installCancelRequested = false
	st.mu.Unlock()
}

// embeddedEndInstallRun withdraws the published install cancel. It CALLS the
// cancel too (idempotent), so the cancellable context is released on every
// exit — success, failure, a requested cancellation and a panic — instead of
// staying live until the application context is cancelled at shutdown. Both
// the run's bookkeeping defer and the preflight-refusal path go through here,
// which is what keeps a refusal from leaving a stale cancel aimed at a run
// that never started.
func (f *FrontendAPI) embeddedEndInstallRun() {
	st := &f.embedded
	st.mu.Lock()
	cancel := st.installCancel
	st.installCancel = nil
	st.installCancelRequested = false
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// embeddedInstallCancelled reports whether err is the run ending because the
// OPERATOR cancelled it. Both signals must agree: the requested flag alone
// would silence a run that failed for an unrelated reason after the click, and
// the cancellation cause alone would treat a shutdown of the application
// context as a cancellation nobody asked for.
func (f *FrontendAPI) embeddedInstallCancelled(err error) bool {
	st := &f.embedded
	st.mu.Lock()
	requested := st.installCancelRequested
	st.mu.Unlock()
	return requested && errors.Is(err, context.Canceled)
}

// embeddedBusyRefusal builds the actionable refusal every gated entry point
// returns while an embedded operation holds the single-run gate. It names the
// in-flight operation (holder) and the action that was refused, so the message
// stays true whether an install or a removal is what is running.
//
// wanted is the operation the CALLER asked for, and it decides the wording:
// "already running" is true only when the gate is held by that same operation.
// An install refused by an in-flight removal must not claim another install is
// already running — there is none — and the string is rendered verbatim in the
// Settings error line, so a wrong noun reads as a bug to the operator. For the
// same reason `refused` must be phrased so it is true in BOTH cases.
//
// A caller that is not itself a gate-holding operation (LoadEmbeddedLLM,
// UnloadEmbeddedLLM, ProbeEmbeddedLLMDevices and the request-path loader seam)
// passes embeddedOpIdle as wanted and always gets the plain wording.
func embeddedBusyRefusal(holder, wanted embeddedOpKind, refused string) error {
	if holder == wanted {
		return fmt.Errorf("an embedded LLM %s is already running; wait for it to finish before %s",
			string(holder), refused)
	}
	return fmt.Errorf("an embedded LLM %s is running; wait for it to finish before %s",
		string(holder), refused)
}

// embeddedProbe runs the hardware probe through the seam (production:
// core/embeddedllm.ProbeHardware).
func (f *FrontendAPI) embeddedProbe(ctx context.Context) (embeddedllm.Hardware, error) {
	if fn := f.embedded.probeFn; fn != nil {
		return fn(ctx, f.log())
	}
	return embeddedllm.ProbeHardware(ctx, f.log())
}

// runEmbeddedInstall performs the download/verify/extract/provision run on a
// background goroutine. ctx is the CANCELLABLE child of the application context
// InstallEmbeddedLLM derived when it claimed the gate — the run must outlive the
// call that started it and stop only when the app quits or the operator cancels
// it (CancelEmbeddedLLMInstall). The step-0 stop is the exception — it runs
// under embeddedStopBudget, handed to core as Installer.StopTimeout, because
// that context carries no deadline and the supervisor's gate is exactly the
// thing a cold load holds for minutes.
func (f *FrontendAPI) runEmbeddedInstall(ctx context.Context, server *embeddedllm.Server, installer *embeddedllm.Installer, hw embeddedllm.Hardware) {
	// The gate release is a DEFER, and it is registered FIRST so it runs LAST
	// (defers are LIFO, after the panic report below): an observer reads the
	// gate as "the run is over", so every effect of the run — the config save,
	// the supervisor state, the completion event and a panic's failure report —
	// must already be visible through this mutex by the time it flips.
	//
	// It is a defer rather than an explicit call on each exit path because the
	// release must not be reachable-or-not by accident. `run.Install` can block
	// indefinitely (its ctx is the never-cancelled application context) and any
	// future return added below would inherit the obligation; a leaked gate is
	// unrecoverable for the process lifetime — Install, Remove, Load, Unload,
	// Probe and the request-path loader would all refuse until a restart.
	defer f.endEmbeddedOperation()
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("the embedded LLM install panicked: %v", r)
			f.log().Error("panic during the embedded LLM install", "panic", r)
			f.failEmbeddedInstall(err)
			f.emitEmbeddedStateFrom(server)
		}
	}()
	// The bookkeeping withdrawal runs BEFORE the gate release (LIFO): the
	// published cancel and the requested flag are effects of this run, so they
	// must be gone by the time the gate flip says "the run is over". Calling
	// the cancel here also releases the context on every exit — including the
	// success path, where nobody else ever would.
	defer f.embeddedEndInstallRun()

	// A per-run copy of the installer pins the probe result the RPC already
	// paid for, so the background run neither re-probes nor races a second
	// install over the shared field. Copying is safe: Installer is a plain
	// struct of injectable fields with no internal synchronization.
	run := *installer
	run.Probe = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) { return hw, nil }
	// Step 0's stop budget is resolved PER RUN, exactly as the three other
	// stop-shaped paths resolve theirs per call, rather than frozen at
	// construction: ctx below is the application context and carries no deadline,
	// so this is the only bound between a repair click and a wait for the length of
	// an in-flight cold load — and its expiry is what makes core's force path
	// reachable, which terminates a resident model instead of reporting a busy
	// gate.
	stopBudget := f.embeddedStopBudget()
	run.StopTimeout = stopBudget
	if stop := run.Stop; stop != nil {
		// Translated on this run's own copy of the seam and NOT on the shared
		// installer, so RemoveEmbeddedLLM's stop keeps handing core's error to that
		// call's own translation — wrapping both would name one removal twice. A
		// step-0 deadline has exactly one likely cause worth acting on: the
		// supervisor's single-instance gate was still held by a load that started
		// before this install, which is also the only case with no child for the
		// force path to kill.
		run.Stop = func(stopCtx context.Context) error {
			return embeddedBoundedStopErr("the pre-install stop", stop(stopCtx), stopBudget)
		}
	}

	opts := embeddedllm.InstallOptions{
		Platform: hw.Platform,
		Backend:  hw.Backend,
		Progress: f.emitEmbeddedInstallProgress,
	}
	// A repair/reinstall keeps the persisted port, so the generated provider
	// base_url (always derived from it) does not churn.
	if cfg := f.embeddedConfig(); cfg.Port >= config.EmbeddedLLMMinPort && cfg.Port <= config.EmbeddedLLMMaxPort {
		opts.Port = cfg.Port
	}

	var report *embeddedllm.InstallReport
	var err error
	if fn := f.embedded.installFn; fn != nil {
		report, err = fn(ctx, &run, opts)
	} else {
		report, err = run.Install(ctx, opts)
	}
	if err == nil && report == nil {
		// Unreachable for the real Installer (a nil error always carries a
		// report); reported explicitly rather than recovered from a nil deref.
		err = errors.New("the embedded LLM install finished without reporting its result")
	}
	if err != nil {
		if f.embeddedInstallCancelled(err) {
			// The operator cancelled the run: a QUIET outcome. The click is the
			// report, so no runtime_error toast on top of it and no recorded
			// failure — a retry must not inherit a stale error line — just the
			// state snapshot and an Info log. Core kept the partial bytes as
			// the resume point, which is the whole point of cancelling instead
			// of removing.
			f.embedded.setError(nil)
			f.log().Info("embedded LLM install cancelled at the operator's request", "error", err)
			f.emitEmbeddedStateFrom(server)
			return
		}
		f.failEmbeddedInstall(err)
		f.emitEmbeddedStateFrom(server)
		return
	}

	f.embedded.setError(nil)
	f.embedded.setInstallRecord(report.Manifest, true)
	// The sink already persisted the config and rebuilt the router; what is
	// left is the supervisor's view of the disk. ONE event for the whole
	// operation: the transition is muted and the snapshot below is emitted
	// unconditionally, so a repair install (whose state does not change) still
	// reports that it finished.
	f.embedded.muteStateEvent(true)
	if err := server.SetInstalled(true); err != nil {
		f.log().Warn("the embedded LLM supervisor refused the installed state", "error", err)
	}
	f.embedded.muteStateEvent(false)
	server.SetAutoUnload(f.embeddedAutoUnloadPolicy())
	f.log().Info("embedded LLM install complete",
		"backend", report.Manifest.Backend, "packing", report.Manifest.Packing,
		"packing_reason", report.PackingReason, "gpu_family", report.Manifest.GPUFamily,
		"guards", embeddedGuardIDs(report.Guards),
		"port", report.Manifest.Port, "context_size", report.Manifest.ContextSize)
	f.emitEmbeddedStateFrom(server)
}

// refuseEmbeddedInstall releases the embedded-operation gate and records a
// SYNCHRONOUS refusal (the single-run gate, an unreadable hardware probe, the
// combined memory gate). It raises no toast: the RPC returns the error to its
// caller, which is the report, and a toast on top of it would say the same
// thing twice.
//
// It also withdraws the run's published cancel: this is the only exit between
// embeddedBeginInstallRun and the goroutine's own defers, so without it a
// refused preflight would leave a live cancel aimed at a run that never
// started (and the context itself alive until shutdown). embeddedEndInstallRun
// calls the cancel too, releasing the context.
func (f *FrontendAPI) refuseEmbeddedInstall(err error) {
	f.embeddedEndInstallRun()
	f.endEmbeddedOperation()
	f.embedded.setError(err)
	f.log().Warn("embedded LLM install refused", "error", err)
}

// failEmbeddedInstall records a BACKGROUND failure: the operator-friendly
// cause is kept for the status/event install_error field (the Settings error
// line renders it verbatim), the raw cause goes to the log, and a
// runtime_error toast is raised because no RPC is left to carry it — a fatal
// install failure must reach the operator even with the Settings dialog closed.
// Only FATAL failures reach this function: a resumable download failure is
// retried silently inside core and never becomes a reported failure at all.
// The deferred gate release in runEmbeddedInstall runs afterwards, so the
// failure is fully reported before the run looks finished.
func (f *FrontendAPI) failEmbeddedInstall(err error) {
	message := embeddedInstallFailureMessage(err)
	f.embedded.setError(errors.New(message))
	f.log().Error("embedded LLM install failed", "error", err)
	f.emitEmbeddedRuntimeError(embeddedErrCodeInstall,
		"The local model could not be installed: "+message)
}

// embeddedInstallFailureMessage translates a fatal install error into the
// sentence the Settings error line and the background-failure toast render.
// Download fatalities carry transport-level diagnostics in their raw form
// ("unexpected EOF", byte counts, partial paths) that mean nothing to an
// operator, so the download sentinels get dedicated wording that names the
// action to take (check the network / retry — the bytes are kept); everything
// else is already operator-facing at its source (the memory gate, the disk
// guard, the smoke test) and is passed through unchanged.
func embeddedInstallFailureMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Reached when the application context died mid-run without an
		// operator's cancel click (a shutdown during the download). The raw
		// cause rides along in parentheses: the sentence explains what to do,
		// the cause keeps the record diagnosable.
		return fmt.Sprintf(
			"the installation was interrupted before it finished — the bytes already downloaded are kept, and starting it again resumes from them (%v)",
			err)
	case errors.Is(err, embeddedllm.ErrAttemptsExhausted):
		if errors.Is(err, embeddedllm.ErrUnreachable) {
			return fmt.Sprintf(
				"the download server could not be reached after %d attempts — check the network connection and try again; the bytes already downloaded are kept, and the install resumes from them",
				embeddedllm.DefaultMaxFailedAttempts)
		}
		return "the download stopped making progress — the server kept dropping the transfer before any bytes arrived; the bytes already downloaded are kept, and starting the install again resumes from them"
	case errors.Is(err, embeddedllm.ErrUnreachable):
		return "the download server could not be reached — check the network connection and try again"
	case errors.Is(err, embeddedllm.ErrIncompleteTransfer):
		return "the download was interrupted before it finished — the bytes already downloaded are kept, and starting the install again resumes from them"
	default:
		return err.Error()
	}
}

// embeddedBaseURL derives the OpenAI-compatible endpoint from a loopback port.
// Deriving (never storing) it is what keeps a port reallocation from leaving a
// stale endpoint behind — the same rule backend/config applies to the generated
// provider record.
func embeddedBaseURL(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/v1", config.EmbeddedLLMHost, port)
}

// embeddedCompositeModelID is the provider/model id the router exposes for the
// local model.
func embeddedCompositeModelID() string {
	return config.EmbeddedLLMProviderName + "/" + config.EmbeddedLLMModelName
}

// emitEmbeddedStateFrom emits a state snapshot taken from the supervisor.
func (f *FrontendAPI) emitEmbeddedStateFrom(server *embeddedllm.Server) {
	if server == nil {
		return
	}
	snapshot := server.Status()
	f.emitEmbeddedLLMState(snapshot.State, snapshot.Port, snapshot.Message)
}

// ---------------------------------------------------------------------------
// Config sink (core/embeddedllm.ConfigSink)
// ---------------------------------------------------------------------------

// embeddedConfigSink is the production half of the core → config boundary: the
// only channel through which the subsystem reaches config.yaml (core cannot
// import backend/config). Its state mutation is exactly the reference
// implementation pinned by backend/config/embedded_llm_sink_test.go; on top of
// it, this one persists the file and rebuilds the LLM router so a freshly
// installed model is usable without a restart.
type embeddedConfigSink struct{ f *FrontendAPI }

var _ embeddedllm.ConfigSink = embeddedConfigSink{}

// ApplyInstalled writes embedded_llm.* from the install record, fills the
// auto-unload defaults WITHOUT overwriting an explicit operator choice (both
// knobs are pointers, so nil is distinguishable from an explicit false), and
// regenerates the backend-owned provider entry plus the context-window override
// from the authoritative state. The tuning section is operator-owned as well,
// so it is carried through verbatim: installing the model never resets a tuned
// memory plan.
func (s embeddedConfigSink) ApplyInstalled(_ context.Context, state embeddedllm.InstallState) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{
		Installed:      true,
		Packing:        string(state.Packing),
		Backend:        string(state.Backend),
		Port:           state.Port,
		ModelFile:      state.ModelFile,
		RuntimeVersion: state.RuntimeVersion,
		InstalledAt:    state.InstalledAt,
		AutoUnload:     autoUnload,
		Tuning:         tuning,
	}
	if f.config.EmbeddedLLM.AutoUnload.Enabled == nil {
		enabled := state.AutoUnloadEnabled
		f.config.EmbeddedLLM.AutoUnload.Enabled = &enabled
	}
	if f.config.EmbeddedLLM.AutoUnload.Minutes == nil {
		minutes := state.AutoUnloadMinutes
		f.config.EmbeddedLLM.AutoUnload.Minutes = &minutes
	}
	// The install is the only caller that has resolved a context tier, so it is
	// the only one that writes the llm.models override.
	f.config.SyncEmbeddedLLMProvider(state.ContextSize)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// ApplyRemoved clears the install state, migrates llm.default_model off the
// embedded composite (an empty default fails validation, so the composite must
// MOVE to another enabled model rather than simply be cleared) and drops the
// provider record. The auto-unload and tuning knobs are operator settings, not
// install state, so both survive.
func (s embeddedConfigSink) ApplyRemoved(_ context.Context) error {
	f := s.f
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previousLLM := f.config.LLM
	previousEmbedded := f.config.EmbeddedLLM

	composite := embeddedCompositeModelID()
	migrated := f.config.LLM.DefaultModel
	if migrated == composite {
		migrated = firstNonEmbeddedModelID(f.config, composite)
	}
	autoUnload := f.config.EmbeddedLLM.AutoUnload
	tuning := f.config.EmbeddedLLM.Tuning
	f.config.EmbeddedLLM = config.EmbeddedLLMConfig{AutoUnload: autoUnload, Tuning: tuning}
	f.config.LLM.DefaultModel = migrated
	// contextWindow 0: the override of a removed model is dropped with its
	// provider record, and no other model's override is touched.
	f.config.SyncEmbeddedLLMProvider(0)

	if err := s.saveOrRollback(previousLLM, previousEmbedded); err != nil {
		return err
	}
	f.rebuildAfterEmbeddedConfigChange()
	return nil
}

// saveOrRollback persists the mutated config and restores the exact prior state
// when the write fails, so a failed save is indistinguishable from a rejected
// one. Called with configMu HELD; it releases the lock before returning, and
// emits config:updated only after a successful write.
func (s embeddedConfigSink) saveOrRollback(previousLLM config.LLMConfig, previousEmbedded config.EmbeddedLLMConfig) error {
	f := s.f
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.LLM = previousLLM
		f.config.EmbeddedLLM = previousEmbedded
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist the embedded LLM state: %w", err)
	}
	f.configLoadErrors = nil
	f.configMu.Unlock()
	f.emitConfigUpdated()
	return nil
}

// embeddedLoaderRef is the lock-free Loader seam the router entry drives.
//
// It exists because of an ordering fact, not for convenience: the router is
// first built inside NewApplication — before this subsystem has been
// constructed — and every ToBuilderConfig call site runs while configMu is
// held, where st.mu must never be taken (the lock order is one-directional,
// (st.mu | st.infoMu) → configMu). A direct *Server reference would therefore
// either be nil at the first build, or require inverting the lock order and
// risk a deadlock.
//
// The indirection resolves the supervisor at CALL time instead of at build
// time, so the transport can be attached before the supervisor exists and still
// reach the real one later. The supervisor read is a single atomic load and the
// operation-gate read is one short st.mu acquisition — neither takes configMu,
// so the lock order is unchanged (see Load).
type embeddedLoaderRef struct {
	f *FrontendAPI
}

// Load makes the model resident, constructing the subsystem first when a
// request arrives before any lifecycle hook built it. That construction is pure
// local work and takes configMu.RLock internally, which is safe here: a request
// goroutine holds no configMu, so neither lock-order rule nor self-deadlock
// applies.
//
// It REFUSES while the embedded-operation gate is held — a background install
// or a removal is in flight. This is the seam all three ungated request paths
// converge on (the service-LLM gate, the router's Loader default and the
// ensure-loaded transport), so refusing here closes all three at once: without
// it a chat message arriving during a repair/reinstall cold-loads the OLD
// install, and the install's promote step then renames and deletes the tree the
// child is executing from (its working directory is that tree). The refusal is
// an error, not a wait: the caller reports it and the request is retryable,
// whereas waiting would charge a multi-gigabyte download to a request budget.
//
// Taking st.mu here is safe for the same reason embeddedBuild's is: no caller
// holds configMu. Every path in is a request goroutine (transport, service
// gate) whose only configMu use — activeModelIsEmbedded — has already returned.
func (r embeddedLoaderRef) Load(ctx context.Context) error {
	if op := r.f.embeddedBusyOperation(); op != embeddedOpIdle {
		return embeddedBusyRefusal(op, embeddedOpIdle, "loading the model for a request")
	}
	server := r.f.embedded.loader.Load()
	if server == nil {
		built, _, err := r.f.embeddedBuild()
		if err != nil {
			return err
		}
		server = built
	}
	if server == nil {
		return errors.New("the embedded LLM supervisor is unavailable")
	}
	return server.Load(ctx)
}

// MarkActivity restarts the auto-unload budget. A response that completes
// before the subsystem exists has no budget to restart, so a nil snapshot is a
// no-op rather than an error path.
func (r embeddedLoaderRef) MarkActivity() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.MarkActivity()
	}
}

// BeginRequest and EndRequest bracket one in-flight request on the supervisor's
// counter, which is what lets the idle path DEFER an expiry that lands
// mid-generation instead of stopping llama-server out from under its own
// response (see embeddedllm.RequestTracker).
//
// They resolve the supervisor the same way MarkActivity does — from the cached
// value, never by building it — and are no-ops when there is none: a counter
// that does not exist yet has nothing to protect, and the request that would
// have created it goes through Load first, which does build it. Building here
// instead would be both wrong (Load owns the gate check) and unsafe (this runs
// on a request goroutine inside http.Client.Do).
func (r embeddedLoaderRef) BeginRequest() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.BeginRequest()
	}
}

// EndRequest releases one in-flight request. The supervisor's counter is
// idempotent at zero, so a release that finds no supervisor cannot drive it
// negative.
func (r embeddedLoaderRef) EndRequest() {
	if server := r.f.embedded.loader.Load(); server != nil {
		server.EndRequest()
	}
}

// Port is the loopback port the supervisor actually bound, or 0 when no
// supervisor is cached — which the transport reads as "do not redirect" (see
// embeddedllm.PortSource).
//
// It exists because the request URL is NOT authoritative: the router builds it
// from the provider base_url, which is derived from the PERSISTED port, and the
// persisted port is only a preference the load re-checks. When it turns out to
// be squatted the supervisor moves upward and persistEmbeddedPort rewrites the
// config — but deliberately does NOT rebuild the router, because this seam is
// what makes the rebuild unnecessary. Without it the request that triggered the
// move would still be POSTed, full prompt included, to whatever unrelated local
// process holds the old port, and that stranger's answer would be reported as
// the assistant's reply.
func (r embeddedLoaderRef) Port() int {
	if server := r.f.embedded.loader.Load(); server != nil {
		return server.Port()
	}
	return 0
}

// The ref satisfies the core seam AND both of its optional capabilities; the
// assertions keep the wiring honest if either side drifts. They are written
// against the REF, not against *embeddedllm.Server, because the ref is the value
// production actually hands to EnsureLoadedClient (applyEmbeddedLoader,
// syncEmbeddedBuilderSeam) — an assertion on the supervisor alone proves nothing
// about the seam in use, which is how the in-flight bracketing and the live-port
// redirect both went dead in production while every test still passed.
var (
	_ embeddedllm.Loader         = embeddedLoaderRef{}
	_ embeddedllm.RequestTracker = embeddedLoaderRef{}
	_ embeddedllm.PortSource     = embeddedLoaderRef{}
)

// applyEmbeddedLoader attaches the ensure-loaded transport seam to a freshly
// converted BuilderConfig, so the embedded provider's router entry starts a
// cold model before its first request goes out, aims that request at the port
// the supervisor actually bound, brackets it on the in-flight counter that
// keeps an idle expiry from unloading the model mid-generation, and restarts the
// idle budget on every completed response.
//
// The middle two of those only happen because the injected value also carries
// the transport's two OPTIONAL capabilities (embeddedllm.PortSource and
// embeddedllm.RequestTracker), which it discovers by an interface assertion on
// whatever Loader it was handed rather than requiring them of the interface.
// Pin those assertions on embeddedLoaderRef — the value production actually
// injects — not only on *embeddedllm.Server, or the controls are dead code that
// every fake-loader test still passes.
//
// MUST be called with configMu held (it reads f.config) and MUST NOT take st.mu
// or call embeddedBuild — see embeddedLoaderRef for why the loader resolves
// lazily instead.
//
// The gate is the persisted install state, not the presence of a supervisor: it
// is true from the config load onwards, so the very first router build already
// carries the seam. It is also what leaves a user's own unrelated provider that
// happens to be named "embedded" untouched while the local model is not
// installed.
//
// LoadWaitTimeout deliberately stays zero so core applies
// embeddedllm.DefaultLoadWaitTimeout. Deriving it here would need
// embeddedAutoUnloadPolicy, which takes configMu.RLock and would self-deadlock
// under the caller's lock.
func (f *FrontendAPI) applyEmbeddedLoader(bc *core.BuilderConfig) {
	if bc == nil || f.config == nil || !f.config.EmbeddedLLM.Installed {
		return
	}
	bc.EmbeddedLLM = core.BuilderEmbeddedLLMConfig{
		ProviderName: config.EmbeddedLLMProviderName,
		Loader:       embeddedLoaderRef{f: f},
	}
}

// syncEmbeddedBuilderSeam mirrors the persisted install state into the BUILDER's
// default seam, the one every router build falls back to when its own
// BuilderConfig carries no Loader (core.OrchestratorBuilder.SetEmbeddedLLM).
//
// applyEmbeddedLoader decorates one BuilderConfig and therefore only reaches the
// routers built from it. The per-session orchestrator is not one of them: the
// session factory in backend/application.go converts the live config directly,
// because it was closed over inside NewApplication — before any FrontendAPI, and
// so before any supervisor, existed. Without this default the session router
// carries no ensure-loaded transport, and a chat request to a cold model is
// dispatched straight to a loopback socket nothing is listening on: the exact
// failure the transport exists to prevent, on the one path that matters most.
//
// MUST NOT be called with configMu held (embeddedConfig takes it for reading).
func (f *FrontendAPI) syncEmbeddedBuilderSeam() {
	b := f.builder()
	if b == nil {
		return
	}
	var seam core.BuilderEmbeddedLLMConfig
	if f.embeddedConfig().Installed {
		seam = core.BuilderEmbeddedLLMConfig{
			ProviderName: config.EmbeddedLLMProviderName,
			Loader:       embeddedLoaderRef{f: f},
		}
	}
	b.SetEmbeddedLLM(seam)
}

// ensureEmbeddedReadyForLLMRequest blocks until the embedded model can answer,
// and returns immediately when the request will not be served by it.
//
// It exists for the one thing the ensure-loaded transport cannot do. The
// transport gates inside http.Client.Do, so it protects the wire — but a caller
// that has ALREADY armed a short request budget gets that budget eaten by the
// wait: time the supervisor legitimately needs to map gigabytes of weights
// counts against a call configured for 2 minutes. So the short-budget paths call
// this FIRST, and create their timeout context only afterwards, which is what
// makes the requirement hold: no LLM request starts before the model is loaded
// and ready, and the load never shortens the request it precedes.
//
// The load runs under the app context, not the caller's, bounded by the
// supervisor's own wait budget: an expired service budget must not abort a load
// that is legitimately in progress (the transport detaches for the same
// reason), and a caller that walks away must not be able to cancel it either.
// Concurrent callers coalesce on the supervisor's single-instance gate.
func (f *FrontendAPI) ensureEmbeddedReadyForLLMRequest(ctx context.Context) error {
	if !f.activeModelIsEmbedded() {
		return nil
	}
	if ctx == nil {
		ctx = f.ctx()
	}
	loadCtx, cancel := context.WithTimeout(ctx, embeddedllm.DefaultLoadWaitTimeout)
	defer cancel()
	if err := (embeddedLoaderRef{f: f}).Load(loadCtx); err != nil {
		return fmt.Errorf("the embedded model could not be loaded: %w", err)
	}
	return nil
}

// Budgets of the agent-idle wait in serviceEmbeddedGate.
const (
	// embeddedServiceIdleWaitInteractive bounds the wait an INTERACTIVE service
	// caller (commit message, prompt optimization) spends before refusing. It is
	// short on purpose: these run on the RPC the user is watching, so the wait
	// is visible, and "the task just finished" is the case it exists to catch.
	embeddedServiceIdleWaitInteractive = 20 * time.Second
	// embeddedServiceIdleWaitBackground bounds the wait a BACKGROUND service
	// caller (session title generation) spends. The goroutine is tracked and
	// invisible to the user, so it can afford to wait out a task that is
	// minutes from finishing before giving up on the rename.
	embeddedServiceIdleWaitBackground = 10 * time.Minute
	// embeddedServiceIdlePoll is the active-session recheck gap. ActiveSessions
	// is an RLock'd snapshot, cheap enough to poll.
	embeddedServiceIdlePoll = 2 * time.Second
)

// serviceEmbeddedGate is the pre-dispatch gate for one-shot service LLM calls
// against the embedded model: it waits for agent idle, THEN for model ready.
//
// The wait is the cache half of the gate. The local server runs `-np 1`, so any
// request with a different prompt evicts the prompt cache the running task's
// next step would have reused — on a 40-50K-token agent prompt that eviction
// costs a fresh multi-minute prefill. So while an agent is running, service
// requests are DEFERRED until the task finishes, within the caller's budget:
// an interactive call refuses with an actionable message when the budget
// expires, a background one just skips.
//
// The model-readiness half is ensureEmbeddedReadyForLLMRequest unchanged: the
// load is never charged to the request's own timeout (ADR-066 D13), which is
// why the gate runs before the caller arms it.
func (f *FrontendAPI) serviceEmbeddedGate(ctx context.Context, waitBudget time.Duration) error {
	if !f.activeModelIsEmbedded() {
		// Not the default model: the service request never reaches the local
		// server, so no cache can be disturbed.
		return nil
	}
	if err := f.waitForEmbeddedAgentIdle(ctx, waitBudget); err != nil {
		return err
	}
	return f.ensureEmbeddedReadyForLLMRequest(ctx)
}

// waitForEmbeddedAgentIdle blocks until no session carries live background
// work, the wait budget expires, or ctx is done.
//
// "Busy" is deliberately coarse: ANY active session holds the gate while the
// embedded model is the default. The precise predicate — a session whose own
// router is on the embedded model — is not observable here (per-session model
// overrides live inside the session's router), and the false positive is cheap:
// the service call merely waits out the task, then proceeds, exactly as a call
// that followed a real eviction would have had to.
func (f *FrontendAPI) waitForEmbeddedAgentIdle(ctx context.Context, waitBudget time.Duration) error {
	if ctx == nil {
		ctx = f.ctx()
	}
	if f.activeSessionCount == nil {
		// No manager was wired (tests, a headless embedding): no agent can be
		// running, and blocking would be wrong by construction.
		return nil
	}
	if f.activeSessionCount() == 0 {
		return nil
	}
	f.log().Info("deferring an embedded service LLM request until the active task finishes",
		"active_sessions", f.activeSessionCount(), "wait_budget", waitBudget.String())
	deadline := time.NewTimer(waitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(embeddedServiceIdlePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf(
				"the embedded model is busy with an active task; the request was deferred for %s to keep that task's prompt cache — retry when it finishes",
				waitBudget)
		case <-ticker.C:
			if f.activeSessionCount() == 0 {
				return nil
			}
		}
	}
}

// serviceEmbeddedGateInteractive is serviceEmbeddedGate for the RPC-backed
// callers (GenerateCommitMessage, OptimizePrompt): a short, visible wait, then
// an actionable refusal.
func (f *FrontendAPI) serviceEmbeddedGateInteractive(ctx context.Context) error {
	return f.serviceEmbeddedGate(ctx, embeddedServiceIdleWaitInteractive)
}

// serviceEmbeddedGateBackground is serviceEmbeddedGate for the session
// manager's one-shot title generation: a long invisible wait, and the caller
// already skips the rename when the gate fails.
func (f *FrontendAPI) serviceEmbeddedGateBackground(ctx context.Context) error {
	return f.serviceEmbeddedGate(ctx, embeddedServiceIdleWaitBackground)
}

// activeModelIsEmbedded reports whether the model a one-shot service request
// resolves to is the backend-owned embedded entry.
//
// Service calls run on the cached router, whose active model is the configured
// default (buildRouter applies llm.default_model via SetModel), so resolving the
// default here answers the same question the router will. The install gate comes
// first: with nothing installed there is no embedded entry to resolve to, and a
// user's own provider that happens to be named "embedded" must not be treated as
// the local model.
func (f *FrontendAPI) activeModelIsEmbedded() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil || !f.config.EmbeddedLLM.Installed {
		return false
	}
	provider, _, err := f.config.LLM.ResolveDefaultModelProvider()
	if err != nil {
		return false
	}
	return provider.Name == config.EmbeddedLLMProviderName
}

// toBuilderConfigLocked converts the live config and attaches the embedded
// loader seam. Callers MUST hold configMu (Lock or RLock) — exactly the
// precondition every ToBuilderConfig call site already satisfies. Use this
// instead of calling ToBuilderConfig directly on a FrontendAPI path, or the
// embedded entry silently loses its transport.
func (f *FrontendAPI) toBuilderConfigLocked() *core.BuilderConfig {
	bc := ToBuilderConfig(f.config, f.modelProfilesCatalog())
	f.applyEmbeddedLoader(bc)
	return bc
}

// rebuildRouterForEmbeddedTransport re-attaches the ensure-loaded transport to
// the live router. The router is first built inside NewApplication, which has no
// FrontendAPI and therefore no loader seam; this is the one place that closes
// the gap, and it runs during the startup restore — before backend:ready, so
// before any session can issue a request. It takes saveMu because
// rebuildAfterEmbeddedConfigChange is contracted to run under it (the config
// writers it serializes against hold saveMu across their own rebuild).
func (f *FrontendAPI) rebuildRouterForEmbeddedTransport() {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.rebuildAfterEmbeddedConfigChange()
}

// rebuildAfterEmbeddedConfigChange rebuilds the judge and the LLM router so the
// provider set change takes effect for new sessions without a restart. Runs
// OUTSIDE the config write lock (readers stay responsive) but still under
// saveMu, and takes configMu.RLock across snapshot + rebuild exactly like
// UpdateLLMConfig, so a concurrent profile/model writer cannot be rolled back.
// A rebuild failure is logged, not returned: the install itself succeeded and
// the config on disk is authoritative — the next load picks it up.
//
// The builder-level seam is refreshed FIRST, and it is refreshed here rather
// than at each of the three lifecycle points because this is the one place all
// three already meet (startup restore, completed install, removal): it keeps the
// default every FUTURE per-session router is built with in step with the
// persisted install state, so a removal cannot leave a session router guarding a
// provider the user then creates themselves under the same name.
func (f *FrontendAPI) rebuildAfterEmbeddedConfigChange() {
	f.syncEmbeddedBuilderSeam()
	b := f.builder()
	if b == nil {
		return
	}
	f.configMu.RLock()
	fresh := f.toBuilderConfigLocked()
	b.RebuildJudge(fresh)
	// Push the fresh model metadata into every LIVE per-session model
	// registry. RebuildRouter below replaces the builder-cached pair — which
	// only sessions built AFTER this point see — while sessions built before
	// it keep their own registries and would otherwise guard prompts with
	// stale metadata forever. The push is an upsert of the current cfg, so it
	// is idempotent, and running it before the rebuild means the correction
	// reaches live sessions even when the rebuild fails.
	b.UpdateModelOverrides(fresh)
	err := b.RebuildRouter(fresh)
	f.configMu.RUnlock()
	if err != nil {
		f.log().Warn("failed to rebuild the LLM router after an embedded LLM config change", "error", err)
	}
}

// scheduleEmbeddedRouterRefresh runs rebuildAfterEmbeddedConfigChange outside
// the caller's critical section — on its own goroutine by default. It is the
// async half of the embedded context read-back contract: persistEmbeddedContext
// must not rebuild the router synchronously (it runs inside Server.Load, on
// behalf of the request the ensure-loaded transport is waiting on, which holds
// the supervisor's single-instance gate and the saveMu the rebuild needs), and
// a skipped rebuild is NOT acceptable either — the previous window is the
// install-time DefaultFitMinContext ESTIMATE, which under-represents the
// server and makes the router's pre-call guard refuse every prompt above
// roughly (window − output_reserve) × 95% until an unrelated rebuild.
//
// Single-flight: loads are serialized by the supervisor's gate, but the
// scheduled refresh runs AFTER the persist released its locks, so a fast
// second load can schedule while the first refresh is still running. That
// second schedule sets the dirty flag instead; the running loop reruns the
// refresh once more before closing, so no correction is ever lost — each run
// snapshots the CURRENT config, and the change itself is already durable.
//
// Tests inject embeddedRefreshDispatch to run the work synchronously and
// deterministically; production leaves it nil and gets a goroutine.
func (f *FrontendAPI) scheduleEmbeddedRouterRefresh() {
	f.embeddedRefreshMu.Lock()
	if f.embeddedRefreshScheduled {
		f.embeddedRefreshDirty = true
		f.embeddedRefreshMu.Unlock()
		return
	}
	f.embeddedRefreshScheduled = true
	f.embeddedRefreshMu.Unlock()

	dispatch := f.embeddedRefreshDispatch
	if dispatch == nil {
		dispatch = func(fn func()) { go fn() }
	}
	dispatch(f.runEmbeddedRouterRefresh)
}

// runEmbeddedRouterRefresh is the scheduled refresh body: one rebuild, plus
// exactly one more when a change landed while this window was open. It always
// ends with the scheduled flag cleared.
func (f *FrontendAPI) runEmbeddedRouterRefresh() {
	for {
		f.rebuildAfterEmbeddedConfigChange()

		f.embeddedRefreshMu.Lock()
		if !f.embeddedRefreshDirty {
			f.embeddedRefreshScheduled = false
			f.embeddedRefreshMu.Unlock()
			return
		}
		f.embeddedRefreshDirty = false
		f.embeddedRefreshMu.Unlock()
	}
}

// firstNonEmbeddedModelID picks the migration target for llm.default_model when
// the embedded model is removed. Empty when it was the only enabled model, and
// the config is then DELIBERATELY left invalid: nothing reseeds
// llm.default_model (ApplyDefaults never touches it), so the next load fails
// validate() with "llm.default_model is not set" and the operator must pick or
// add a provider. Guessing a default here would silently select a model the
// operator never chose — the same rule as a config with no provider at all,
// which is invalid independently of this subsystem. Pinned by
// config.TestEmbeddedLLMRemoveWithNoOtherModelLeavesAnEmptyDefault.
func firstNonEmbeddedModelID(cfg *config.Config, composite string) string {
	for _, id := range cfg.LLM.AllModelIDs() {
		if id != composite {
			return id
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Lifecycle surface (NOT part of the Wails RPC surface: methods on
// FrontendAPILifecycle are never bound — see frontend_api.go)
// ---------------------------------------------------------------------------

// InitEmbeddedLLM restores the embedded local-model state from manifest.json
// and emits the initial embedded_llm:state snapshot. Called from
// desktop/startup_phases.go on the startup path.
//
// It performs NO network I/O, NO hardware probe and never loads the model:
// startup must neither depend on the network nor block on a multi-gigabyte
// weight load. The whole call is a manifest read plus one event.
func (l *FrontendAPILifecycle) InitEmbeddedLLM() {
	l.f.initEmbeddedLLM()
}

// StopEmbeddedLLM stops the supervised llama-server if one is running,
// releasing its RAM/VRAM. Called from desktop Shutdown; idempotent and a no-op
// when the subsystem was never constructed.
func (l *FrontendAPILifecycle) StopEmbeddedLLM(ctx context.Context) error {
	return l.f.stopEmbeddedLLM(ctx)
}
