package embeddedllm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/sysproc"

	"github.com/v0lka/sp4rk/safeio"
)

// Progress stages reported through InstallProgressFunc. One component walks
// downloading → verifying → (extracting → signing for runtime archives) → done.
const (
	StageDownloading = "downloading"
	StageVerifying   = "verifying"
	StageExtracting  = "extracting"
	StageSigning     = "signing"
	StageDone        = "done"
)

// Progress is one per-component install update; each component reports its own
// stream so the UI can bar it separately (ADR-066 D9). See specs/domains/embedded-llm.md.
type Progress struct {
	Component  Component `json:"component"`
	Stage      string    `json:"stage"`
	BytesDone  int64     `json:"bytes_done"`
	BytesTotal int64     `json:"bytes_total"`
}

// InstallProgressFunc receives one Progress update per component and stage; may
// be nil. Distinct from download.go's ProgressFunc (raw single-artifact bytes).
type InstallProgressFunc func(Progress)

// Manifest is the durable install record — written once, atomically, only
// after every component is verified and the runtime proven to run. Startup
// can trust it outright (never the network, never a probe); reading it is
// the boot path's only disk work here. See specs/domains/embedded-llm.md.
type Manifest struct {
	Packing        Packing `json:"packing"`
	Backend        Backend `json:"backend"`
	RuntimeVersion string  `json:"runtime_version"`
	// Checksums: artifact -> SHA256, keyed by checksumKey — component name on the
	// llama family, the FILE name for every file of mtplx's multi-file components.
	Checksums map[string]string `json:"checksums"`
	Port      int               `json:"port"`

	// Engine is the runtime family described here; empty (a pre-field manifest)
	// means llama, never "unknown engine".
	Engine Engine `json:"engine,omitempty"`
	// PythonVersion / PackRevision are the mtplx engine's extra pins (empty on
	// the llama family, whose pin is RuntimeVersion + the per-asset checksums).
	PythonVersion string `json:"python_version,omitempty"`
	PackRevision  string `json:"pack_revision,omitempty"`

	// ContextSize is the LAST KNOWN EFFECTIVE context — install writes the
	// planner's figure (FitMinContext for a fit-sized plan, never 0); every
	// successful load corrects it from `/props`. See specs/domains/embedded-llm.md.
	ContextSize int `json:"context_size"`

	ModelFile   string `json:"model_file"`
	InstalledAt string `json:"installed_at"` // RFC 3339

	// Topology is the device-memory snapshot the recorded plan was made from —
	// what a fail-soft load falls back to and a support bundle reports. Nil is
	// UNKNOWN, never "no accelerator" (the zero-budget refusal the gate avoids).
	Topology *MemoryTopology `json:"topology,omitempty"`

	// Plan is the launch shape LAST APPLIED, from which a load rebuilds the same
	// shape (nil → the resolve.go policy). Persisted, not re-derived — that would
	// need the operator's Tuning, a SETTING never copied into a record.
	Plan *MemoryPlan `json:"plan,omitempty"`

	// PackingReason states WHY a non-default packing was chosen; every such value
	// is a degradation and must say so. Empty (pre-field manifest) = unknown.
	PackingReason PackingReason `json:"packing_reason,omitempty"`

	// GPUFamily is the accelerator generation the plan was classified as, so a
	// support bundle names the silicon the guards reasoned about.
	GPUFamily GPUFamily `json:"gpu_family,omitempty"`

	// Guards are the compatibility decisions in force at planning, persisted so
	// a degraded install stays explainable after the deciding process exited.
	Guards []GuardDecision `json:"guards,omitempty"`
}

// ErrNotInstalled reports that no manifest exists — a normal state, not a fault.
var ErrNotInstalled = errors.New("embedded LLM is not installed")

// ErrSmokeTestFailed reports that the fresh llama-server did not run — Gatekeeper
// on macOS, missing CUDA 12 libraries on Linux; the wrapper carries the fix.
var ErrSmokeTestFailed = errors.New("the provisioned llama-server did not run")

// ReadManifest loads the manifest at path; a missing file is ErrNotInstalled,
// not a generic OS error.
func ReadManifest(path string) (Manifest, error) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("%w (no manifest at %s)", ErrNotInstalled, path)
		}
		return Manifest{}, fmt.Errorf("embeddedllm: reading manifest %q: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("embeddedllm: parsing manifest %q: %w", path, err)
	}
	return m, nil
}

// manifestTempPattern is the sibling temporary the atomic write renames from;
// `*` makes it UNIQUE PER WRITE — writers overlap (see manifestTempStaleAfter).
const manifestTempPattern = ManifestFileName + ".*.tmp"

// manifestTempPrefix and manifestTempSuffix let sweepStaleManifestTemps and tests
// recognise a leftover without knowing the random middle os.CreateTemp picked.
const (
	manifestTempPrefix = ManifestFileName + "."
	manifestTempSuffix = ".tmp"
)

// manifestTempStaleAfter is how old a leftover temporary must be before the
// sweep removes it. The age gate is the concurrency safety: writers CAN overlap,
// so an unconditional sweep could delete a live temporary and re-create the torn
// manifest the unique name prevents; a minute is orders beyond one write.
const manifestTempStaleAfter = time.Minute

// writeManifest persists m atomically — unique sibling temporary, rename over
// the target, cleanup on every error path — beginning with sweepStaleManifestTemps
// (a crash between CreateTemp and the rename leaks an unknowable name). Shape:
// specs/domains/embedded-llm.md.
func writeManifest(path string, m Manifest, logger *slog.Logger) error {
	return writeManifestWithOps(path, m, logger, promoteManifest, os.Remove)
}

// writeManifestWithOps keeps fault injection local to one write; production
// always uses the bounded promotion and real removal above.
func writeManifestWithOps(path string, m Manifest, logger *slog.Logger, promote func(string, string, *slog.Logger) error, remove func(string) error) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("embeddedllm: encoding manifest: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := mkdirAll(dir); err != nil {
		return err
	}
	// This call's temporary does not exist yet, so the sweep only ever sees
	// PREVIOUS writers' leftovers.
	sweepStaleManifestTemps(dir, logger)
	tmp, err := os.CreateTemp(dir, manifestTempPattern)
	if err != nil {
		return fmt.Errorf("embeddedllm: creating a temporary manifest in %q: %w", dir, err)
	}
	name := tmp.Name()
	if err := writeAndClose(tmp, data); err != nil {
		return cleanupManifestTemp(name, err, remove)
	}
	if err := promote(name, path, logger); err != nil {
		return cleanupManifestTemp(name, fmt.Errorf("embeddedllm: promoting manifest into place: %w", err), remove)
	}
	return nil
}

func cleanupManifestTemp(name string, cause error, remove func(string) error) error {
	if err := remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("embeddedllm: removing temporary manifest %q: %w", name, err))
	}
	return cause
}

// A promotion rename can fail transiently on Windows — a concurrent promotion of
// the same manifest.json or a real-time scanner holding the fresh temporary
// answers Access denied for a moment. The unavailability is momentary, so a
// bounded backoff makes the collision succeed (pinned by the concurrent-writers
// test); off Windows the classifier always answers false.
const (
	// manifestPromoteAttempts bounds the promotion retry budget.
	manifestPromoteAttempts = 8

	// manifestPromoteBackoff is the base backoff between promotion retries,
	// doubled on every attempt (2ms..128ms; ~254ms in total at the cap).
	manifestPromoteBackoff = 2 * time.Millisecond
)

// manifestPromotionMu serializes this process's promotions; temp creation/write
// stays concurrent and external sharing still takes the bounded retry.
var manifestPromotionMu sync.Mutex

// promoteManifest renames the written temporary over the target under
// manifestPromotionMu with bounded Windows retries; failure keeps the destination.
func promoteManifest(tmp, path string, logger *slog.Logger) error {
	manifestPromotionMu.Lock()
	defer manifestPromotionMu.Unlock()
	return promoteManifestWithOps(tmp, path, logger, os.Rename, isTransientRenameError, time.Sleep)
}

func promoteManifestWithOps(tmp, path string, logger *slog.Logger, rename func(string, string) error, transient func(error) bool, wait func(time.Duration)) error {
	backoff := manifestPromoteBackoff
	for attempt := 1; ; attempt++ {
		err := rename(tmp, path)
		if err == nil {
			return nil
		}
		if !transient(err) || attempt == manifestPromoteAttempts {
			return err
		}
		logger.Debug("embeddedllm: manifest promotion hit a transient sharing error, retrying",
			"tmp", tmp, "target", path, "attempt", attempt, "backoff", backoff, "error", err)
		wait(backoff)
		backoff *= 2
	}
}

// sweepStaleManifestTemps removes the temporaries a crashed writeManifest left
// behind in dir — best-effort both ways (log-and-skip) — touching only entries
// of the temporary shape older than manifestTempStaleAfter, never live writers.
func sweepStaleManifestTemps(dir string, logger *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Debug("embeddedllm: stale manifest temporaries could not be listed",
			"dir", dir, "error", err)
		return
	}
	cutoff := time.Now().Add(-manifestTempStaleAfter)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !isManifestTempName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			logger.Debug("embeddedllm: a manifest temporary could not be stat'd for the stale sweep",
				"path", filepath.Join(dir, name), "error", err)
			continue
		}
		if info.ModTime().After(cutoff) {
			// Recent enough to be a live concurrent writer's temporary rather than
			// a crash leftover — see manifestTempStaleAfter.
			continue
		}
		stale := filepath.Join(dir, name)
		if err := os.Remove(stale); err != nil {
			logger.Debug("embeddedllm: a stale manifest temporary could not be removed",
				"path", stale, "error", err)
			continue
		}
		logger.Debug("embeddedllm: removed a manifest temporary a crashed write left behind",
			"path", stale, "age", time.Since(info.ModTime()).Truncate(time.Second))
	}
}

// isManifestTempName reports whether name has the os.CreateTemp shape from
// manifestTempPattern; the manifest itself, and empty middles, do not match.
func isManifestTempName(name string) bool {
	return len(name) > len(manifestTempPrefix)+len(manifestTempSuffix) &&
		strings.HasPrefix(name, manifestTempPrefix) &&
		strings.HasSuffix(name, manifestTempSuffix)
}

// writeAndClose writes data to f and closes it whichever way the write goes, so
// the rename sees a complete file and no descriptor leaks on the error path.
func writeAndClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("embeddedllm: writing %q: %w", f.Name(), err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("embeddedllm: syncing %q: %w", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("embeddedllm: closing %q: %w", f.Name(), err)
	}
	return nil
}

// recordableContext is the context a manifest may carry for a plan: the
// planner's own value for a computed shape, FitMinContext — the floor fit is
// held to — for a fit-sized one whose ContextSize is 0 by definition, fallback
// otherwise. Never 0: see Manifest.ContextSize / specs/domains/embedded-llm.md.
func recordableContext(plan MemoryPlan, fallback int) int {
	if plan.Fit {
		if plan.FitMinContext > 0 {
			return plan.FitMinContext
		}
		return fallback
	}
	if plan.ContextSize > 0 {
		return plan.ContextSize
	}
	return fallback
}

// InstallState is the durable install record handed to the config layer — the
// ONLY channel from this subsystem into config.yaml (an import back would cycle).
// See specs/domains/embedded-llm.md.
type InstallState struct {
	// Engine is the family that was provisioned, set explicitly for BOTH
	// families; the zero value is unset input, never a silent "llama".
	Engine         Engine
	Packing        Packing
	Backend        Backend
	Port           int
	ModelFile      string
	RuntimeVersion string
	InstalledAt    string // RFC 3339, matching Manifest.InstalledAt
	ContextSize    int

	// PythonVersion and PackRevision are the mtplx engine's extra pins
	// (empty on the llama family).
	PythonVersion string
	PackRevision  string

	// AutoUnloadEnabled / AutoUnloadMinutes are the DEFAULTS an install
	// establishes: the sink applies them only onto unset knobs — installing
	// never resets a tuned idle budget.
	AutoUnloadEnabled bool
	AutoUnloadMinutes int

	// Deliberately NO memory-tuning defaults: TuningConfig's pointer knobs keep
	// "never written" distinct from "written auto" and an install must not
	// collapse that — the sink carries tuning verbatim (see ConfigSink).
}

// ConfigSink persists install state into config.yaml; the backend supplies the
// implementation. ApplyInstalled registers the state (provider entry, context
// override, auto-unload defaults onto unset knobs only); ApplyRemoved clears the
// install and migrates llm.default_model off the embedded model. BOTH rewrite
// embedded_llm.* wholesale, must carry the operator-owned `auto_unload` and
// `tuning` sections through unchanged, and must be idempotent (retries after a
// partial failure). Contract: specs/domains/embedded-llm.md.
type ConfigSink interface {
	ApplyInstalled(ctx context.Context, state InstallState) error
	ApplyRemoved(ctx context.Context) error
}

// CommandRunner executes one external command and returns its combined output,
// so the provisioning steps are testable without the platform binaries present.
type CommandRunner func(ctx context.Context, name string, opts *RunOptions, args ...string) (string, error)

// RunOptions is the launch configuration a CommandRunner call can carry; nil
// means "no opinion" — the child inherits the parent's env and working directory.
type RunOptions struct {
	// Env is the child's complete environment; nil inherits the parent's.
	Env []string
	// Dir is the child's working directory; empty inherits the caller's.
	Dir string
}

// maxCommandOutputBytes caps one command's captured output: it is diagnostic
// only, so an uncapped buffer is an external process driving our allocation.
const maxCommandOutputBytes = 1 << 20

// defaultCommandRunner is the production runner; every call site bounds it with
// its own context timeout. A deadline alone does not bound the call, so two more
// bounds live here:
//
//   - cmd.WaitDelay: Stdout is a pipe, so a grandchild holding the write end
//     keeps cmd.Run blocked after the child exited — stranding the install
//     goroutine and everything it serializes (probe layer: same hazard, same value).
//   - limitedWriter's byte cap (io.LimitReader does not fit: cmd.Stdout is a Writer).
//
// The child is spawned without a console window too: this runner also executes
// llama-server, whose Windows console-subsystem build would flash one per click.
func defaultCommandRunner(ctx context.Context, name string, opts *RunOptions, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	sysproc.HideConsole(cmd)
	cmd.WaitDelay = probeWaitDelay
	if opts != nil {
		if opts.Env != nil {
			cmd.Env = opts.Env
		}
		if opts.Dir != "" {
			cmd.Dir = opts.Dir
		}
	}
	out := &limitedWriter{limit: maxCommandOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The command exited successfully — ErrWaitDelay replaces a nil exit
		// status, never a real one; only abandoned output was lost.
		return out.String(), nil
	}
	return out.String(), err
}

// limitedWriter captures at most limit bytes and counts the rest; Write always
// reports the full length (a short write fails os/exec's copy) under w.mu —
// stdout/stderr may copy concurrently.
type limitedWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := w.limit - w.buf.Len()
	switch {
	case room <= 0:
		w.dropped += len(p)
	case len(p) > room:
		_, _ = w.buf.Write(p[:room])
		w.dropped += len(p) - room
	default:
		_, _ = w.buf.Write(p)
	}
	return len(p), nil
}

// String is the captured output, marked when the cap dropped some of it.
func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dropped == 0 {
		return w.buf.String()
	}
	return w.buf.String() + fmt.Sprintf("\n[... %d further output bytes dropped ...]", w.dropped)
}

// Auto-unload defaults an install establishes; the minutes value mirrors
// config.EmbeddedLLMDefaultAutoUnloadMinutes (core cannot import it), pinned
// equal by backend/config's test.
const (
	DefaultAutoUnloadEnabled = true
	DefaultAutoUnloadMinutes = 60
)

// macOS provisioning budgets. Bounded like the hardware probe: a stuck
// codesign must not hang an install forever.
const (
	macOSCommandTimeout = 2 * time.Minute
	smokeTestTimeout    = 60 * time.Second
)

// macOS provisioning helpers, invoked by absolute path — see provisionDarwin for
// why PATH resolution is not an option here.
const (
	darwinXattr    = "/usr/bin/xattr"
	darwinCodesign = "/usr/bin/codesign"
)

// toolMissing reports whether a provisioning helper is simply not installed —
// an absolute path surfaces fs.ErrNotExist where LookPath gives exec.ErrNotFound;
// either way a warning: the smoke test is the authority on runnability.
func toolMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// Extraction guards: defence-in-depth against a checksum-valid-but-malicious
// archive. Sized for this subsystem rather than core/toolmanager's (its 512 MiB
// per-entry cap is below one CUDA runtime library), per ADR-066.
const (
	maxExtractEntryBytes int64 = 2 << 30
	maxExtractTotalBytes int64 = 8 << 30
)

// Installer orchestrates Install and Remove over a Layout. The zero value is not
// usable — build one with NewInstaller; every external effect is a field, so the
// whole flow tests without a network, artifacts or platform binaries.
type Installer struct {
	// Layout is the on-disk footprint. Required.
	Layout Layout
	// Sink persists install state into config.yaml — required: no bytes without
	// a provider, no provider without bytes.
	Sink ConfigSink
	// Logger receives the subsystem's diagnostics; nil → a discard logger,
	// never the global slog.Default.
	Logger *slog.Logger
	// Downloader fetches and verifies artifacts. nil → NewDownloader(nil, Logger).
	Downloader *Downloader
	// Probe reads the machine's RAM and accelerator. nil → ProbeHardware.
	Probe func(ctx context.Context, logger *slog.Logger) (Hardware, error)
	// ProbeDevices reads a provisioned runtime's accelerator inventory so the
	// install learns the GPU generation the packing rule and guards need.
	// nil → ProbeDevices. Fail-soft: a false answer never fails an install.
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// RunCommand executes xattr/codesign/--version. nil → defaultCommandRunner.
	RunCommand CommandRunner
	// AllocatePort reserves a free loopback port. nil → ephemeralLoopbackPort;
	// server.go owns persistence and the pre-load collision re-check.
	AllocatePort func(ctx context.Context) (int, error)
	// Stop terminates a running server before Install retires the runtime tree
	// it executes from and before Remove deletes its files. nil is legal — both
	// proceed, and the OS releases the files at process exit.
	Stop func(ctx context.Context) error
	// StopTimeout bounds Install's step-0 Stop alone — the whole call, gate wait
	// included (0, the default, inherits the caller's ctx). SHOULD be set with a
	// supervisor-wired Stop: the gate can be held a full ready budget, step 0
	// precedes the first progress event, and expiry is what makes the no-gate
	// force path reachable. NOT Server.StopTimeout. See specs/domains/embedded-llm.md.
	StopTimeout time.Duration
	// Now stamps InstalledAt. nil → time.Now.
	Now func() time.Time
	// HostOS overrides the OS the macOS provisioning branch keys off. Empty →
	// runtime.GOOS. Tests set it to "darwin" to exercise quarantine-clear,
	// ad-hoc signing and the smoke test on any platform.
	HostOS string
}

// NewInstaller returns an Installer with production defaults; wire Sink before
// Install or Remove.
func NewInstaller(layout Layout, logger *slog.Logger) *Installer {
	return &Installer{Layout: layout, Logger: logger}
}

// InstallOptions parameterises one install run.
type InstallOptions struct {
	// Port is the loopback port to persist; zero allocates a free one.
	Port int
	// Platform overrides the probed "<goos>-<goarch>" key. Empty → probed.
	Platform string
	// Backend overrides the probed accelerator; resolution still degrades an
	// override it cannot serve. Empty → probed.
	Backend Backend
	// Engine selects the runtime family; the ZERO value is llama. The config
	// layer resolves `auto` through ResolveEngine first; EngineMTPLX runs the
	// MLX flow (install_mlx.go).
	Engine Engine
	// Tuning is the operator's memory-plan override the plan is made from — the
	// same `embedded_llm.tuning` the preflight gate priced (the llama family
	// replans from the live setting at load). Zero = the all-Auto plan.
	Tuning Tuning
	// Progress receives one update per component and stage. May be nil.
	Progress InstallProgressFunc
}

// InstallReport is the outcome of a successful install.
type InstallReport struct {
	Manifest     Manifest
	Resolution   Resolution
	Hardware     Hardware
	RuntimeDir   string
	ServerBinary string
	ModelFile    string

	// Guards is the complete compatibility record — applied decisions and
	// disclosures alike, the same list the Manifest persists, lifted so a caller
	// sees a degraded install without walking nested records.
	Guards []GuardDecision

	// PackingReason is why the installed packing is what it is, lifted from the
	// resolution for the same reason.
	PackingReason PackingReason
}

// Install provisions the pinned runtime and weights for this machine.
//
// A resident server is stopped FIRST (step 0), symmetrically with Remove; the
// fixed gate order after it, the bounded stop and the force path are specified
// in specs/domains/embedded-llm.md. Any-step failure leaves no manifest and no
// provider — a half-installed model is never visible to the router — while
// verified bytes stay as cache hits, making the install resumable, not restartable.
func (in *Installer) Install(ctx context.Context, opts InstallOptions) (*InstallReport, error) {
	if in == nil {
		return nil, errors.New("embeddedllm: nil Installer")
	}
	if in.Sink == nil {
		return nil, errors.New("embeddedllm: no ConfigSink wired — refusing to install " +
			"bytes that could not be registered as a provider")
	}
	if _, err := in.Layout.ManifestPath(); err != nil {
		return nil, err
	}

	// Step 0: stop a resident server before anything else — it executes out of
	// the tree this install retires and holds the memory the gates below price.
	// Bounded-stop/force-path semantics: Installer.StopTimeout / the embedded-LLM spec.
	if in.Stop != nil {
		if err := in.stopBeforeInstall(ctx); err != nil {
			return nil, fmt.Errorf("embeddedllm: stopping the inference server before installation: %w", err)
		}
	}

	// The mtplx engine branches after the step-0 stop (its rebuild replaces the
	// tree a resident server executes from) and before any llama probe/pack/guard
	// machinery.
	if opts.Engine == EngineMTPLX {
		return in.installMLX(ctx, opts)
	}

	hw, platform, res, topology, err := in.plan(ctx, opts)
	if err != nil {
		return nil, err
	}

	if err := in.Layout.EnsureRoots(); err != nil {
		return nil, err
	}
	if err := in.checkWholeSetDiskSpace(res.Assets); err != nil {
		return nil, err
	}

	port := opts.Port
	if port == 0 {
		port, err = in.allocatePort(ctx)
		if err != nil {
			return nil, fmt.Errorf("embeddedllm: allocating a loopback port: %w", err)
		}
	}

	runtimeAssets, weightAssets := splitRuntimeAssets(res.Assets)
	checksums := make(map[string]string, len(res.Assets))

	staging, err := in.Layout.RuntimeStagingDir(res.Backend)
	if err != nil {
		return nil, err
	}
	if err := in.prepareStaging(staging); err != nil {
		return nil, err
	}
	if err := in.fetchComponents(ctx, opts, runtimeAssets, staging, checksums); err != nil {
		return nil, err
	}

	serverBinary, err := in.provisionRuntime(ctx, opts, res, runtimeAssets)
	if err != nil {
		return nil, err
	}

	// The staged runtime can now answer the device probe — the last cheap
	// moment to refine, before the weights are fetched. A guard wanting a
	// DIFFERENT runtime is past its moment and becomes guidance; a memory
	// refusal stops the install; a refinement keeps the topology+plan pair
	// describing one another (see refineWithStagedDevices).
	res, weightAssets, refinedTopology, err := in.refineWithStagedDevices(ctx, platform, hw, res, serverBinary, weightAssets)
	if err != nil {
		return nil, err
	}
	if refinedTopology != nil {
		topology = refinedTopology
	}

	if err := in.fetchComponents(ctx, opts, weightAssets, "", checksums); err != nil {
		return nil, err
	}

	modelFile, err := in.Layout.ModelFile(res.Packing)
	if err != nil {
		return nil, err
	}
	installedAt := in.now().UTC().Format(time.RFC3339)
	plan := res.Memory
	manifest := Manifest{
		Packing:        res.Packing,
		Backend:        res.Backend,
		RuntimeVersion: RuntimeTag,
		Checksums:      checksums,
		Port:           port,
		ContextSize:    recordableContext(plan, res.ContextSize),
		ModelFile:      modelFile,
		InstalledAt:    installedAt,
		PackingReason:  res.PackingReason,
		GPUFamily:      res.GPU,
		Guards:         res.Guards,
		Topology:       topology,
		Plan:           &plan,
	}

	manifestPath, err := in.Layout.ManifestPath()
	if err != nil {
		return nil, err
	}
	if err := writeManifest(manifestPath, manifest, in.logger()); err != nil {
		return nil, err
	}
	in.logger().Info("embedded LLM installed",
		"backend", manifest.Backend, "packing", manifest.Packing,
		"packing_reason", manifest.PackingReason, "gpu_family", manifest.GPUFamily,
		"guards", guardIDsForLog(manifest.Guards),
		"port", manifest.Port, "context_size", manifest.ContextSize,
		"fit", plan.FitArg(), "kv_type", plan.KVType,
		"topology_probed", topology != nil,
		"model_file", manifest.ModelFile)

	state := InstallState{
		Engine:            EngineLlama,
		Packing:           manifest.Packing,
		Backend:           manifest.Backend,
		Port:              manifest.Port,
		ModelFile:         manifest.ModelFile,
		RuntimeVersion:    manifest.RuntimeVersion,
		InstalledAt:       manifest.InstalledAt,
		ContextSize:       manifest.ContextSize,
		AutoUnloadEnabled: DefaultAutoUnloadEnabled,
		AutoUnloadMinutes: DefaultAutoUnloadMinutes,
	}
	if err := in.Sink.ApplyInstalled(ctx, state); err != nil {
		// Bytes and manifest are correct; only registration failed. No rollback
		// — a retry is idempotent (cache-hit downloads, manifest rewritten).
		return nil, fmt.Errorf("embeddedllm: registering the installed model in config: %w", err)
	}

	runtimeDir, err := in.Layout.RuntimeDir(res.Backend)
	if err != nil {
		return nil, err
	}
	return &InstallReport{
		Manifest:      manifest,
		Resolution:    res,
		Hardware:      hw,
		RuntimeDir:    runtimeDir,
		ServerBinary:  serverBinary,
		ModelFile:     modelFile,
		Guards:        manifest.Guards,
		PackingReason: manifest.PackingReason,
	}, nil
}

// stopBeforeInstall applies StopTimeout to the step-0 stop — as a helper, so a
// future call-site edit cannot lose the bound — cancelling the derived context
// when the stop returns.
func (in *Installer) stopBeforeInstall(ctx context.Context) error {
	if in.StopTimeout <= 0 {
		return in.Stop(ctx)
	}
	stopCtx, cancel := context.WithTimeout(ctx, in.StopTimeout)
	defer cancel()
	return in.Stop(stopCtx)
}

// plan runs the pure gates — hardware probe, memory gate (Resolve's first
// check), resolution — and the optional DEVICE PROBE, obtained for free from a
// still-present previous runtime so guards and the packing rule classify the
// artifacts about to be fetched. A first install keeps GPUFamilyUnknown here;
// Install refines from the staged runtime later. Without a topology the gate
// derives the host budget from RAM and leaves the device side unreadable —
// degraded, never refused, never a capacity downgrade (FitsPQ2_0 stays Unknown).
func (in *Installer) plan(ctx context.Context, opts InstallOptions) (Hardware, string, Resolution, *MemoryTopology, error) {
	hw, err := in.probe()(ctx, in.logger())
	if err != nil {
		return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: hardware probe: %w", err)
	}
	platform := hw.Platform
	if opts.Platform != "" {
		platform = opts.Platform
	}
	backend := hw.Backend
	if opts.Backend != "" {
		backend = opts.Backend
	}

	// The CUDA 12.x userland verdict rides with the profile so both passes gate
	// the Linux #222 substitution on the same measured fact.
	profile := MachineProfile{
		Platform:       platform,
		Backend:        backend,
		RAMGiB:         hw.RAMGiB,
		CUDA12Userland: hw.CUDA12Userland,
	}
	res, err := ResolveProfile(profile)
	if err != nil {
		return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: %w", err)
	}

	// probed is returned only when it INFORMED the resolution: the manifest
	// promises a topology+plan pair a load's fail-soft path reads as one fact,
	// and a mismatched pair costs a launch shape nobody reasoned about.
	var probed *MemoryTopology
	if topology, ok := in.memoryTopologyFromBinary(ctx, in.existingServerBinary(res.Backend)); ok {
		gpu := ClassifyGPUs(topology.Devices)
		profile.GPU = gpu
		refined, refineErr := Resolve(ResolveInput{MachineProfile: profile, Topology: &topology})
		if refineErr != nil {
			// A MEMORY refusal is the measured budget refusing the machine —
			// it must stop the install, not fall back to a plan that OOMs.
			if errors.Is(refineErr, ErrInsufficientMemory) {
				return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: %w", refineErr)
			}
			// Anything else: the first plan stays valid; only the extra
			// information is lost, degrading to the statically decidable guards.
			in.logger().Debug("embedded LLM device-aware re-plan skipped",
				"gpu_family", gpu, "error", refineErr)
			res.GPU = gpu
			return hw, platform, res, nil, nil
		}
		if refined.Backend != res.Backend {
			in.logger().Info("embedded LLM plan changed after the device probe",
				"gpu_family", gpu, "probed_backend", backend,
				"from", res.Backend, "to", refined.Backend,
				"guards", guardIDsForLog(refined.Guards))
		}
		res = refined
		measured := topology
		probed = &measured
	}
	return hw, platform, res, probed, nil
}

// existingServerBinary returns an already-provisioned runtime's llama-server,
// or "" — letting a repair answer the device probe before any download.
func (in *Installer) existingServerBinary(backend Backend) string {
	runtimeDir, err := in.Layout.RuntimeDir(backend)
	if err != nil {
		return ""
	}
	binary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return ""
	}
	if _, err := os.Stat(binary); err != nil {
		return ""
	}
	return binary
}

// memoryTopologyFromBinary asks a provisioned runtime what accelerator memory it
// can see — FAIL-SOFT in every direction (no binary, a hung one, an unrecognized
// inventory all yield false, keeping the caller's plan). The whole topology is
// returned because its consumers need different halves: FAMILY for the guards
// and packing rule, BUDGETS for the memory gate.
func (in *Installer) memoryTopologyFromBinary(ctx context.Context, binaryPath string) (MemoryTopology, bool) {
	if binaryPath == "" {
		return MemoryTopology{}, false
	}
	topology, ok := in.probeDevices()(ctx, binaryPath, in.logger())
	if !ok {
		return MemoryTopology{}, false
	}
	if ClassifyGPUs(topology.Devices) == GPUFamilyUnknown {
		in.logger().Debug("embedded LLM device probe recognized no accelerator family",
			"devices", len(topology.Devices))
	}
	return topology, true
}

// guardIDsForLog renders the applied guard IDs for one log line; the record
// itself carries every decision.
func guardIDsForLog(decisions []GuardDecision) string {
	ids := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Applied {
			ids = append(ids, string(decision.Guard))
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}

// refineWithStagedDevices is the second, device-aware pass over a plan — run
// after the runtime is staged, BEFORE the weights are fetched; it never
// downloads. Outcomes: no answer → untouched; a guard or packing change with an
// unchanged BACKEND → the refined resolution and its weight assets (the case
// the refinement exists for); a guard wanting a DIFFERENT BACKEND → recorded as
// guidance, the archive already staged and signed; a measured memory refusal →
// the install stops. Guards merge rather than replace, so a statically applied
// decision keeps its Applied flag; the returned topology is non-nil ONLY when
// the returned resolution was refined by it — a pair describing one another.
func (in *Installer) refineWithStagedDevices(ctx context.Context, platform string, hw Hardware,
	res Resolution, serverBinary string, weightAssets []Asset,
) (Resolution, []Asset, *MemoryTopology, error) {
	if res.GPU != GPUFamilyUnknown {
		return res, weightAssets, nil, nil
	}
	topology, ok := in.memoryTopologyFromBinary(ctx, serverBinary)
	if !ok {
		return res, weightAssets, nil, nil
	}
	gpu := ClassifyGPUs(topology.Devices)

	refined, err := Resolve(ResolveInput{MachineProfile: MachineProfile{
		Platform:       platform,
		Backend:        res.Backend,
		RAMGiB:         hw.RAMGiB,
		GPU:            gpu,
		CUDA12Userland: hw.CUDA12Userland,
	}, Topology: &topology})
	if err != nil {
		if errors.Is(err, ErrInsufficientMemory) {
			return res, nil, nil, fmt.Errorf("embeddedllm: %w", err)
		}
		// Anything else: the unrefined plan stays valid; only information is lost.
		in.logger().Debug("embedded LLM device-aware refinement skipped",
			"gpu_family", gpu, "error", err)
		res.GPU = gpu
		return res, weightAssets, nil, nil
	}

	if refined.Backend != res.Backend {
		in.logger().Warn("a compatibility guard wants a different runtime than the one already staged",
			"gpu_family", gpu, "staged_backend", res.Backend, "preferred_backend", refined.Backend,
			"guards", guardIDsForLog(refined.Guards))
		res.GPU = gpu
		res.Guards = mergeGuardDecisions(res.Guards, markGuardsUnappliable(refined.Guards))
		return res, weightAssets, nil, nil
	}

	res = refined
	_, weights := splitRuntimeAssets(refined.Assets)
	if refined.PackingReason != PackingReasonDefault {
		in.logger().Info("embedded LLM packing refined by the device probe",
			"gpu_family", gpu, "packing", refined.Packing, "reason", refined.PackingReason)
	}
	// Returned only where the resolution was actually refined by the topology —
	// earlier branches keep the plan, and its topology, they came in with.
	measured := topology
	return res, weights, &measured, nil
}

// markGuardsUnappliable rewrites decisions computed for a substitution that is
// no longer possible (the runtime archive is already on disk): backend
// substitutions lose their Applied flag and gain the reason, so the record
// explains itself instead of looking like an oversight.
func markGuardsUnappliable(decisions []GuardDecision) []GuardDecision {
	marked := make([]GuardDecision, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Action == GuardActionPreferBackend {
			decision.Applied = false
			decision.Guidance += " (this was only discovered after a runtime had already been " +
				"staged, so the recommended " + string(decision.Backend) + " build was NOT provisioned: " +
				"reinstall to apply it)"
		}
		marked = append(marked, decision)
	}
	return marked
}

// splitRuntimeAssets separates the runtime archives from the weights; the two
// groups are fetched in different phases.
func splitRuntimeAssets(assets []Asset) (runtimeAssets, weightAssets []Asset) {
	runtimeAssets = make([]Asset, 0, 2)
	weightAssets = make([]Asset, 0, 2)
	for _, asset := range assets {
		if asset.Component == ComponentRuntime || asset.Component == ComponentCudart {
			runtimeAssets = append(runtimeAssets, asset)
			continue
		}
		weightAssets = append(weightAssets, asset)
	}
	return runtimeAssets, weightAssets
}

// prepareStaging clears and recreates the extraction tree — a leftover staging
// tree would mix old and new bytes.
func (in *Installer) prepareStaging(staging string) error {
	if err := in.removeOwned(staging); err != nil {
		return err
	}
	return mkdirAll(staging)
}

// fetchComponents downloads and verifies each asset, recording its digest under
// checksumKey. Non-empty staging means runtime archives: extract into the
// staging tree, deferring StageDone to provisionRuntime so the component's
// stream ends when the runtime is usable, not when bytes landed.
func (in *Installer) fetchComponents(ctx context.Context, opts InstallOptions, assets []Asset,
	staging string, checksums map[string]string,
) error {
	deferDone := staging != ""
	for _, asset := range assets {
		digest, err := in.fetchOne(ctx, opts, asset)
		if err != nil {
			return err
		}
		checksums[checksumKey(asset)] = digest

		if deferDone {
			in.emit(opts, Progress{Component: asset.Component, Stage: StageExtracting,
				BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
			path, derr := in.Layout.Destination(asset)
			if derr != nil {
				return derr
			}
			if xerr := extractArchive(path, staging); xerr != nil {
				return fmt.Errorf("embeddedllm: extracting %s archive: %w", asset.Component, xerr)
			}
			continue
		}
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}
	return nil
}

// checksumKey is the manifest.Checksums key for one verified artifact: the
// historical component-name key on the llama family, whose components are
// single artifacts; the FILE name on mtplx's multi-artifact components, which
// would otherwise collapse into whichever file downloaded last.
func checksumKey(asset Asset) string {
	switch asset.Component {
	case ComponentEngineWheels, ComponentMLXPack:
		return asset.ArchiveName
	default:
		return string(asset.Component)
	}
}

// fetchOne downloads one artifact through the verification gate — a second,
// independent check beyond Downloader.Download's own no-promotion rule.
// Fail-closed (ASI04): "probably fine" is how an unverified binary executes.
func (in *Installer) fetchOne(ctx context.Context, opts InstallOptions, asset Asset) (string, error) {
	dst, err := in.Layout.Destination(asset)
	if err != nil {
		return "", err
	}
	return in.fetchOneTo(ctx, opts, asset, dst)
}

// fetchOneTo is fetchOne with the destination given — the mtplx pack is staged
// and swapped whole so a mid-fetch failure cannot mix two pack revisions.
func (in *Installer) fetchOneTo(ctx context.Context, opts InstallOptions, asset Asset, dst string) (string, error) {
	in.emit(opts, Progress{Component: asset.Component, Stage: StageDownloading,
		BytesDone: 0, BytesTotal: asset.SizeBytes})

	result, err := in.downloader().Download(ctx, asset, dst, func(done, total int64) {
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDownloading,
			BytesDone: done, BytesTotal: total})
	})
	if err != nil {
		return "", fmt.Errorf("embeddedllm: downloading %s: %w", asset.Component, err)
	}

	in.emit(opts, Progress{Component: asset.Component, Stage: StageVerifying,
		BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})

	pinned := strings.ToLower(asset.SHA256)
	if !result.Verified || result.SHA256 != pinned {
		return "", fmt.Errorf("embeddedllm: %s failed the verification gate (verified=%t, "+
			"digest=%q, pinned=%q): %w", asset.Component, result.Verified, result.SHA256, pinned,
			ErrChecksumMismatch)
	}
	in.logger().Info("embedded LLM component verified",
		"component", asset.Component, "sha256", result.SHA256,
		"bytes", result.SizeBytes, "cached", result.Cached, "resumed", result.Resumed)
	return result.SHA256, nil
}

// provisionRuntime provisions the staged runtime, smoke-tests it on EVERY
// platform, swaps it into place and closes out the runtime components' progress
// streams — an unrunnable runtime must fail here, in seconds, before the weights.
func (in *Installer) provisionRuntime(ctx context.Context, opts InstallOptions, res Resolution,
	runtimeAssets []Asset,
) (string, error) {
	staging, err := in.Layout.RuntimeStagingDir(res.Backend)
	if err != nil {
		return "", err
	}
	if in.hostOS() == "darwin" {
		total := TotalBytes(runtimeAssets)
		in.emit(opts, Progress{Component: ComponentRuntime, Stage: StageSigning,
			BytesDone: total, BytesTotal: total})
		if err := in.provisionDarwin(ctx, res.Backend, staging); err != nil {
			return "", err
		}
	} else if err := in.smokeTest(ctx, res.Backend, staging); err != nil {
		return "", err
	}

	runtimeDir, err := in.promoteRuntime(res.Backend)
	if err != nil {
		return "", err
	}
	serverBinary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return "", err
	}
	for _, asset := range runtimeAssets {
		in.emit(opts, Progress{Component: asset.Component, Stage: StageDone,
			BytesDone: asset.SizeBytes, BytesTotal: asset.SizeBytes})
	}
	return serverBinary, nil
}

// provisionDarwin clears the download quarantine from the whole runtime tree,
// ad-hoc signs every Mach-O image in it, and proves the result executes:
// non-browser downloads still carry com.apple.quarantine, and arm64 macOS
// refuses images with a missing or invalid signature — the pinned archives are
// unsigned CI builds. The helpers run by ABSOLUTE PATH: the app loads the login
// shell's environment, so a hijacked PATH element could otherwise substitute a
// `codesign` running arbitrary code during an install click. A missing helper
// is a warning — the smoke test is the authority on runnability.
// See specs/domains/embedded-llm.md (macOS provisioning helpers run by absolute path).
func (in *Installer) provisionDarwin(ctx context.Context, backend Backend, runtimeDir string) error {
	if err := in.runBounded(ctx, macOSCommandTimeout, darwinXattr, "-cr", runtimeDir); err != nil {
		if toolMissing(err) {
			in.logger().Warn("xattr is unavailable; quarantine attributes were not cleared",
				"path", runtimeDir, "error", err)
		} else {
			return fmt.Errorf("embeddedllm: clearing the download quarantine on %q: %w", runtimeDir, err)
		}
	}

	images, err := machOImages(runtimeDir)
	if err != nil {
		return err
	}
	for _, image := range images {
		err := in.runBounded(ctx, macOSCommandTimeout, darwinCodesign, "--force", "--sign", "-", image)
		if err != nil {
			if toolMissing(err) {
				in.logger().Warn("codesign is unavailable; the runtime keeps its upstream signatures",
					"path", runtimeDir)
				break
			}
			return fmt.Errorf("embeddedllm: ad-hoc signing %q: %w", image, err)
		}
	}
	in.logger().Info("embedded LLM runtime provisioned for macOS",
		"path", runtimeDir, "signed_images", len(images))

	return in.smokeTest(ctx, backend, runtimeDir)
}

// smokeTest runs "llama-server --version" against the staged tree, turning a
// failure into an actionable message. It runs under the LAUNCH environment —
// launchEnv plus the binary dir on the dynamic-library search path — exercising
// the loader path the resident server will use; anything else would false-fail.
// See specs/domains/embedded-llm.md (the smoke test runs in the launch environment).
func (in *Installer) smokeTest(ctx context.Context, backend Backend, runtimeDir string) error {
	binary, err := ServerBinaryPath(runtimeDir, in.hostOS())
	if err != nil {
		return err
	}
	binaryDir := filepath.Dir(binary)
	smokeCtx, cancel := context.WithTimeout(ctx, smokeTestTimeout)
	defer cancel()

	out, err := in.runner()(smokeCtx, binary, &RunOptions{
		Env: launchEnv(binaryDir, in.hostOS(), os.Environ()),
		Dir: binaryDir,
	}, "--version")
	if err != nil {
		return fmt.Errorf("%w: %s --version failed: %v\n"+
			"%s\n"+
			"Command output: %s",
			ErrSmokeTestFailed, binary, err, in.smokeTestHint(backend, runtimeDir),
			strings.TrimSpace(out))
	}
	in.logger().Debug("embedded LLM runtime smoke test passed", "binary", binary,
		"output", strings.TrimSpace(out))
	return nil
}

// smokeTestHint is the platform- and backend-specific next step appended to a
// failed smoke test. Linux CUDA builds fail on the environment (missing CUDA 12
// runtime libraries), so the hint points there; Windows CUDA builds are excluded
// — their CUDA ships bundled, never the system userland (ADR-073).
func (in *Installer) smokeTestHint(backend Backend, runtimeDir string) string {
	switch {
	case in.hostOS() == "darwin":
		return "macOS Gatekeeper is most likely still blocking the unsigned runtime. To fix it, run:\n" +
			"  xattr -dr com.apple.quarantine " + runtimeDir + "\n" +
			"or open System Settings → Privacy & Security, find the blocked llama-server " +
			`entry and click "Allow Anyway", then install again.`
	case backend.IsCUDA() && in.hostOS() != "windows":
		return fmt.Sprintf("the %s runtime most likely cannot load its CUDA 12 libraries "+
			"(libcudart, libcublas). Install the CUDA 12 runtime libraries for your "+
			"distribution and install again.", backend)
	default:
		binaryName := ServerBinaryName
		if in.hostOS() == "windows" {
			binaryName += ".exe"
		}
		return fmt.Sprintf("the %s runtime failed to execute. The download is verified by "+
			"SHA256, so a system library or an unsupported CPU feature is the most likely "+
			"cause — run the binary directly for the loader's diagnostics:\n"+
			"  %s/build/bin/%s --version", backend, runtimeDir, binaryName)
	}
}

// promoteRuntime swaps the staged tree into place; the previous tree is retired
// before deletion so a failed rename can roll back.
func (in *Installer) promoteRuntime(backend Backend) (string, error) {
	final, err := in.Layout.RuntimeDir(backend)
	if err != nil {
		return "", err
	}
	staging, err := in.Layout.RuntimeStagingDir(backend)
	if err != nil {
		return "", err
	}
	retired, err := in.Layout.RuntimeRetiredDir(backend)
	if err != nil {
		return "", err
	}

	// Clear a retire dir left behind by a crashed previous run.
	if err := in.removeOwned(retired); err != nil {
		return "", err
	}
	hadPrevious := pathExists(final)
	if hadPrevious {
		if err := os.Rename(final, retired); err != nil {
			return "", fmt.Errorf("embeddedllm: retiring the previous runtime %q: %w", final, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if hadPrevious {
			if rbErr := os.Rename(retired, final); rbErr != nil {
				in.logger().Error("failed to roll back to the previous runtime",
					"path", final, "error", rbErr, "original", err)
			}
		}
		return "", fmt.Errorf("embeddedllm: moving the provisioned runtime into place: %w", err)
	}
	if hadPrevious {
		if err := in.removeOwned(retired); err != nil {
			in.logger().Warn("the previous runtime could not be deleted",
				"path", retired, "error", err)
		}
	}
	return final, nil
}

// RemoveScope names what a Remove deletes — the manifest and the config
// registration are cleared under every scope regardless.
type RemoveScope string

const (
	// RemoveAll — the manifest, the whole model root (weights, vision
	// projector), every runtime tree and the archive staging area: the
	// historical full removal.
	RemoveAll RemoveScope = "all"
	// RemoveRuntime — the "llama-*" trees (installed, staged, retired) and the
	// archive staging area. The weights and the projector survive on disk as a
	// verified cache: a later install re-verifies them without re-downloading.
	RemoveRuntime RemoveScope = "runtime"
	// RemoveWeights — the pinned model GGUFs (both packings). The runtime tree
	// and the projector survive.
	RemoveWeights RemoveScope = "weights"
	// RemoveProjection — the vision projector GGUF. The runtime tree and the
	// weights survive.
	RemoveProjection RemoveScope = "projection"
)

// valid reports whether s is a known scope; the zero value is deliberately
// invalid — a caller that forgot to choose must not get "all".
func (s RemoveScope) valid() bool {
	switch s {
	case RemoveAll, RemoveRuntime, RemoveWeights, RemoveProjection:
		return true
	default:
		return false
	}
}

// ParseRemoveScope maps a wire-scope string: empty means RemoveAll (the
// historical form), anything else unknown is refused, never defaulted.
func ParseRemoveScope(scope string) (RemoveScope, error) {
	if scope == "" {
		return RemoveAll, nil
	}
	parsed := RemoveScope(strings.ToLower(strings.TrimSpace(scope)))
	if !parsed.valid() {
		return "", fmt.Errorf("embeddedllm: unknown remove scope %q (want one of %q, %q, %q, %q)",
			scope, RemoveAll, RemoveRuntime, RemoveWeights, RemoveProjection)
	}
	return parsed, nil
}

// Leftovers reports which embedded-LLM artifacts remain on disk — meaningful only
// when NOT installed: what a scoped removal left behind (cache, or reclaimables).
type Leftovers struct {
	// Runtime — a "llama-*"/"mtplx-*" tree or the archive staging area exists.
	Runtime bool
	// Weights — a pinned model GGUF or the mtplx pack exists in the model root.
	Weights bool
	// Projection — the vision projector GGUF exists in the model root.
	Projection bool
}

// Any reports whether anything at all is left on disk.
func (l Leftovers) Any() bool { return l.Runtime || l.Weights || l.Projection }

// Remove stops the server and removes the whole installation: the historical,
// scope-less form every existing caller means.
func (in *Installer) Remove(ctx context.Context) error {
	return in.RemoveWithScope(ctx, RemoveAll)
}

// ErrNoProjectionOnMLX reports RemoveProjection against an mtplx install: the
// vision tower lives inside the pack. Callers hide the scope; this is the
// fail-closed backstop.
var ErrNoProjectionOnMLX = errors.New("embeddedllm: the mtplx engine has no separate vision projector to remove")

// RemoveWithScope stops a running server and removes what the scope names, plus
// — under EVERY scope — the manifest and the config registration: the on-disk
// state becomes "not installed" and whatever the scope spared is a cache, not
// an install. The config clears even when a deletion failed (a config claiming
// gone files points the router at a dead provider); deletions are gated on
// Layout.Owns — never the shared embedding-model files or <toolsDir>/bin.
func (in *Installer) RemoveWithScope(ctx context.Context, scope RemoveScope) error {
	if in == nil {
		return errors.New("embeddedllm: nil Installer")
	}
	if in.Sink == nil {
		return errors.New("embeddedllm: no ConfigSink wired — refusing to delete an install " +
			"whose provider entry could not be cleared")
	}
	if !scope.valid() {
		return fmt.Errorf("embeddedllm: unknown remove scope %q", string(scope))
	}
	if in.Stop != nil {
		if err := in.Stop(ctx); err != nil {
			return fmt.Errorf("embeddedllm: stopping the inference server before removal: %w", err)
		}
	}

	var errs []error
	// The recorded engine is read BEFORE the manifest goes — the scope's
	// meaning (projection is llama-only) depends on it.
	engine := in.recordedEngine()
	// The manifest goes with EVERY scope, FIRST: it is install state, not
	// artifact bytes, and a record describing deleted files would point the
	// supervisor at bytes that may no longer exist.
	if manifest, err := in.Layout.ManifestPath(); err != nil {
		errs = append(errs, err)
	} else if err := in.removeOwned(manifest); err != nil {
		errs = append(errs, err)
	}

	// The runtime/weights halves reach BOTH engines' trees (a scope names a
	// KIND of artifact, not an engine).
	removed := 0
	switch scope {
	case RemoveRuntime:
		removed = in.removeScopeRuntimeTrees(&errs)
	case RemoveWeights:
		in.removeScopeWeights(&errs)
	case RemoveProjection:
		if engine == EngineMTPLX {
			errs = append(errs, ErrNoProjectionOnMLX)
		} else {
			in.removeScopeProjection(&errs)
		}
	default: // RemoveAll — the historical full removal
		if err := in.removeOwned(in.Layout.ModelRoot); err != nil {
			errs = append(errs, err)
		}
		if err := in.removeOwned(in.Layout.MLXModelRoot); err != nil {
			errs = append(errs, err)
		}
		var err error
		removed, err = in.removeRuntimeTrees()
		if err != nil {
			errs = append(errs, err)
		}
		if downloads, derr := in.Layout.DownloadsDir(); derr != nil {
			errs = append(errs, derr)
		} else if err := in.removeOwned(downloads); err != nil {
			errs = append(errs, err)
		}
	}

	if serr := in.Sink.ApplyRemoved(ctx); serr != nil {
		errs = append(errs, fmt.Errorf("clearing the config: %w", serr))
	} else {
		in.logger().Info("embedded LLM removed", "scope", string(scope),
			"runtime_trees_deleted", removed)
	}
	return errors.Join(errs...)
}

// recordedEngine reads the manifest's engine; a missing or pre-field manifest is
// the llama family — the only family that could have written it.
func (in *Installer) recordedEngine() Engine {
	manifestPath, err := in.Layout.ManifestPath()
	if err != nil {
		return EngineLlama
	}
	manifest, err := ReadManifest(manifestPath)
	if err != nil || manifest.Engine == "" {
		return EngineLlama
	}
	return manifest.Engine
}

// removeScopeRuntimeTrees is RemoveWithScope's runtime half: every engine's
// runtime trees plus the archive staging area. Returns the trees deleted.
func (in *Installer) removeScopeRuntimeTrees(errs *[]error) int {
	removed, err := in.removeRuntimeTrees()
	if err != nil {
		*errs = append(*errs, err)
	}
	if downloads, derr := in.Layout.DownloadsDir(); derr != nil {
		*errs = append(*errs, derr)
	} else if err := in.removeOwned(downloads); err != nil {
		*errs = append(*errs, err)
	}
	return removed
}

// removeScopeWeights is RemoveWithScope's weights half: both pinned packings'
// GGUFs and the mtplx pack directory, reaching weights an install never recorded.
func (in *Installer) removeScopeWeights(errs *[]error) {
	for _, packing := range []Packing{PackingPQ2_0, PackingPTQ1_0} {
		file, ferr := in.Layout.ModelFile(packing)
		if ferr != nil {
			*errs = append(*errs, ferr)
			continue
		}
		if err := in.removeOwned(file); err != nil {
			*errs = append(*errs, err)
		}
	}
	if err := in.removeOwned(in.Layout.MLXModelRoot); err != nil {
		*errs = append(*errs, err)
	}
}

// removeScopeProjection is RemoveWithScope's projection half: the vision
// projector GGUF.
func (in *Installer) removeScopeProjection(errs *[]error) {
	file, ferr := in.Layout.Destination(MMProjAsset())
	if ferr != nil {
		*errs = append(*errs, ferr)
		return
	}
	if err := in.removeOwned(file); err != nil {
		*errs = append(*errs, err)
	}
}

// DetectLeftovers reports which embedded-LLM artifacts exist right now — a cheap
// scan of listings and stats, never a walk, never a hash. It reports existence
// only; whether the bytes are still wanted is the caller's question.
func (in *Installer) DetectLeftovers() (Leftovers, error) {
	var lo Leftovers
	entries, err := os.ReadDir(in.Layout.RuntimesRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return lo, fmt.Errorf("embeddedllm: listing %q: %w", in.Layout.RuntimesRoot, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), runtimeDirPrefix) ||
			strings.HasPrefix(entry.Name(), mlxRuntimePrefix) {
			lo.Runtime = true
			break
		}
	}
	// The archive staging area counts as runtime residue: its contents are
	// runtime/cudart archives (and their resumable partials).
	if downloads, derr := in.Layout.DownloadsDir(); derr == nil && pathExists(downloads) {
		lo.Runtime = true
	}
	if packDir, perr := in.Layout.MLXPackDir(); perr == nil && pathExists(packDir) {
		lo.Weights = true
	}
	for _, packing := range []Packing{PackingPQ2_0, PackingPTQ1_0} {
		file, ferr := in.Layout.ModelFile(packing)
		if ferr != nil {
			continue
		}
		if pathExists(file) {
			lo.Weights = true
			break
		}
	}
	if file, ferr := in.Layout.Destination(MMProjAsset()); ferr == nil && pathExists(file) {
		lo.Projection = true
	}
	return lo, nil
}

// removeRuntimeTrees deletes every engine's runtime tree under the runtime root
// — installed, staged or retired — and reports how many were removed.
func (in *Installer) removeRuntimeTrees() (int, error) {
	entries, err := os.ReadDir(in.Layout.RuntimesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("embeddedllm: listing %q: %w", in.Layout.RuntimesRoot, err)
	}
	removed := 0
	var errs []error
	for _, entry := range entries {
		// Both engines' trees: "llama-<tag>-<backend>" and "mtplx-<ver>"
		// (plus their staging/retired/work suffixes, which share the prefix).
		if !strings.HasPrefix(entry.Name(), runtimeDirPrefix) &&
			!strings.HasPrefix(entry.Name(), mlxRuntimePrefix) {
			continue
		}
		// Containment-checked join + the single gated primitive: neither step
		// can reach outside the runtime root.
		target, jerr := in.Layout.safeJoin(in.Layout.RuntimesRoot, entry.Name())
		if jerr != nil {
			errs = append(errs, jerr)
			continue
		}
		if err := in.removeOwned(target); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// removeOwned deletes path only when the layout owns it — the subsystem's single
// destructive primitive, where the agent-isolation invariant (nothing under
// <toolsDir>/bin, nothing outside the two trees) is enforced once.
func (in *Installer) removeOwned(path string) error {
	if path == "" {
		return errors.New("embeddedllm: refusing to delete an empty path")
	}
	if !pathExists(path) {
		return nil
	}
	if !in.Layout.Owns(path) {
		return fmt.Errorf("embeddedllm: refusing to delete %q — it is outside the "+
			"embedded-LLM storage layout", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("embeddedllm: deleting %q: %w", path, err)
	}
	return nil
}

// checkWholeSetDiskSpace refuses up front when the volume cannot hold the whole
// resolved set plus headroom, so a doomed 7 GiB download fails in milliseconds.
// Measurement failure is non-fatal: a genuinely full disk still fails safely on
// ENOSPC, leaving a resumable partial.
func (in *Installer) checkWholeSetDiskSpace(assets []Asset) error {
	required := RequiredFreeBytes(TotalBytes(assets))
	free, err := in.freeSpace()(in.Layout.ModelRoot)
	if err != nil {
		in.logger().Warn("disk space check unavailable, proceeding without the whole-set guard",
			"path", in.Layout.ModelRoot, "required", required, "error", err)
		return nil //nolint:nilerr // best-effort guard; the write fails safely on ENOSPC
	}
	if free < required {
		return fmt.Errorf("%w for the full embedded-LLM set: %s available at %s, %s required "+
			"(artifacts %s + %s headroom)",
			ErrInsufficientDisk, formatBytes(free), in.Layout.ModelRoot,
			formatBytes(required), formatBytes(required-DefaultDiskHeadroom),
			formatBytes(DefaultDiskHeadroom))
	}
	return nil
}

// ── extraction ──

// extractArchive unpacks a .tar.gz/.tgz or .zip into destDir: entries are
// containment-checked (a traversal entry is skipped, not written) and bounded
// by the per-entry and total size caps.
func extractArchive(archivePath, destDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"), strings.HasSuffix(archivePath, ".tgz"):
		return extractTarGz(archivePath, destDir)
	case strings.HasSuffix(archivePath, ".zip"):
		return extractZip(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format %q", filepath.Ext(archivePath))
	}
}

// extractQuota tracks the total decompressed bytes of one archive.
type extractQuota struct {
	written int64
}

func (q *extractQuota) add(n int64) error {
	q.written += n
	if q.written > maxExtractTotalBytes {
		return fmt.Errorf("archive extracts to more than %d bytes (possible archive bomb)",
			maxExtractTotalBytes)
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := safeio.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	var quota extractQuota
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}
		target, ok, err := extractTarget(destDir, hdr.Name)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeExtractEntry(target, io.LimitReader(tr, maxExtractEntryBytes+1),
				os.FileMode(hdr.Mode), &quota); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := createExtractSymlink(destDir, target, hdr.Linkname); err != nil {
				return err
			}
		}
	}
}

func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer func() { _ = r.Close() }()

	var quota extractQuota
	for _, f := range r.File {
		target, ok, err := extractTarget(destDir, f.Name)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if f.FileInfo().IsDir() {
			if err := mkdirAll(target); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry %q: %w", f.Name, err)
		}
		writeErr := writeExtractEntry(target, io.LimitReader(rc, maxExtractEntryBytes+1),
			f.Mode(), &quota)
		_ = rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

// extractTarget resolves one archive entry name against destDir and reports
// whether it may be written — containment decided by pathutil, never an inline
// prefix comparison.
func extractTarget(destDir, name string) (target string, writable bool, err error) {
	if name == "" {
		return "", false, nil
	}
	target = filepath.Join(destDir, name)
	within, werr := pathutil.IsWithinPath(destDir, target)
	if werr != nil {
		return "", false, fmt.Errorf("checking containment of archive entry %q: %w", name, werr)
	}
	if !within {
		return "", false, nil
	}
	return target, true, nil
}

func writeExtractEntry(target string, src io.Reader, mode fs.FileMode, quota *extractQuota) error {
	if err := mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	n, copyErr := io.Copy(out, src)
	if cerr := out.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return fmt.Errorf("writing %q: %w", target, copyErr)
	}
	if n > maxExtractEntryBytes {
		_ = os.Remove(target)
		return fmt.Errorf("archive entry %q exceeds %d bytes (possible archive bomb)",
			filepath.Base(target), maxExtractEntryBytes)
	}
	if err := quota.add(n); err != nil {
		_ = os.Remove(target)
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := os.Chmod(target, mode.Perm()); err != nil {
		return fmt.Errorf("setting permissions on %q: %w", target, err)
	}
	return nil
}

// createExtractSymlink reproduces an in-archive symlink, refusing a target that
// would escape destDir — it could redirect the runtime's loads, or a later
// deletion, to an arbitrary path.
func createExtractSymlink(destDir, target, linkname string) error {
	if linkname == "" {
		return nil
	}
	resolved := linkname
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(target), linkname)
	}
	within, err := pathutil.IsWithinPath(destDir, resolved)
	if err != nil {
		return fmt.Errorf("checking symlink target %q: %w", linkname, err)
	}
	if !within {
		return fmt.Errorf("archive symlink %q points outside the runtime tree", linkname)
	}
	// The managed-CPython archive carries no directory entries and its first
	// entry is a symlink deep inside the tree, so the parent must be created
	// lazily here, like writeExtractEntry does for regular files.
	if err := mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	_ = os.Remove(target)
	if err := os.Symlink(linkname, target); err != nil {
		return fmt.Errorf("creating symlink %q: %w", target, err)
	}
	return nil
}

// machOImages lists the macOS runtime tree's Mach-O images — the llama-*
// executables and the dylibs they load. Signing only the server would leave it
// unable to load its own libraries.
func machOImages(runtimeDir string) ([]string, error) {
	var images []string
	walkErr := filepath.WalkDir(runtimeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !needsAdHocSignature(d.Name()) {
			return nil
		}
		// An entry whose containment cannot be resolved is skipped, not signed:
		// signing is a mutation, so fail-closed is the only default.
		within, werr := pathutil.IsWithinPath(runtimeDir, path)
		if werr != nil || !within {
			return nil //nolint:nilerr // skipping an unverifiable entry is the safe outcome
		}
		images = append(images, path)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("embeddedllm: scanning %q for Mach-O images: %w", runtimeDir, walkErr)
	}
	return images, nil
}

// needsAdHocSignature reports whether a file name is one of the runtime's
// Mach-O images: the llama-* executables or a .dylib dependency.
func needsAdHocSignature(name string) bool {
	return strings.HasPrefix(name, "llama") || strings.HasSuffix(name, ".dylib")
}

// ── seams ──

// emit reports one progress update, tolerating a nil callback.
func (in *Installer) emit(opts InstallOptions, p Progress) {
	if opts.Progress != nil {
		opts.Progress(p)
	}
}

func (in *Installer) logger() *slog.Logger {
	if in == nil || in.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return in.Logger
}

func (in *Installer) downloader() *Downloader {
	if in.Downloader != nil {
		return in.Downloader
	}
	return NewDownloader(nil, in.logger())
}

func (in *Installer) probe() func(context.Context, *slog.Logger) (Hardware, error) {
	if in.Probe != nil {
		return in.Probe
	}
	return ProbeHardware
}

func (in *Installer) probeDevices() func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
	if in.ProbeDevices != nil {
		return in.ProbeDevices
	}
	return ProbeDevices
}

func (in *Installer) runner() CommandRunner {
	if in.RunCommand != nil {
		return in.RunCommand
	}
	return defaultCommandRunner
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Installer) hostOS() string {
	if in.HostOS != "" {
		return in.HostOS
	}
	return runtime.GOOS
}

func (in *Installer) freeSpace() func(string) (int64, error) {
	if in.Downloader != nil && in.Downloader.FreeSpace != nil {
		return in.Downloader.FreeSpace
	}
	return platformFreeSpace
}

// allocatePort reserves a loopback port for the server.
func (in *Installer) allocatePort(ctx context.Context) (int, error) {
	if in.AllocatePort != nil {
		return in.AllocatePort(ctx)
	}
	return ephemeralLoopbackPort(ctx)
}

// ephemeralLoopbackPort asks the OS for a free loopback port by binding :0 and
// reading the assignment back — the production allocator behind a nil
// Installer.AllocatePort; server.go's EnsurePort may substitute one pre-spawn.
func ephemeralLoopbackPort(ctx context.Context) (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, errors.New("the OS did not report a usable loopback port")
	}
	return addr.Port, nil
}

// runBounded runs one external command under its own timeout so a wedged
// helper cannot stall the install indefinitely.
func (in *Installer) runBounded(ctx context.Context, timeout time.Duration,
	name string, args ...string,
) error {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := in.runner()(bounded, name, nil, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w (output: %s)", name, strings.Join(args, " "),
			err, strings.TrimSpace(out))
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
