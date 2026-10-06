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

// Progress is one per-component install update. Every component of the
// resolved set reports its own stream, so the UI can show a separate bar for
// the runtime, the Windows CUDA runtime DLLs, the weights and the projector
// (ADR-066 D9).
type Progress struct {
	Component  Component `json:"component"`
	Stage      string    `json:"stage"`
	BytesDone  int64     `json:"bytes_done"`
	BytesTotal int64     `json:"bytes_total"`
}

// InstallProgressFunc receives one Progress update per component and stage.
// It may be nil. The type is named apart from download.go's ProgressFunc —
// that one reports raw (done, total) byte counts for a single artifact, this
// one reports the component-and-stage stream the install UI renders.
type InstallProgressFunc func(Progress)

// Manifest is <models>/bonsai-2-27b/manifest.json — the durable record of what
// was installed. Startup trusts THIS, not the network and not a probe: reading
// it is the only disk work the boot path performs for this subsystem.
//
// It is written once, atomically, and only after every component has been
// downloaded, SHA256-verified and (for the runtime) extracted, signed and
// smoke-tested. A failed install therefore never leaves a manifest describing
// bytes that are not on disk.
type Manifest struct {
	Packing        Packing           `json:"packing"`
	Backend        Backend           `json:"backend"`
	RuntimeVersion string            `json:"runtime_version"`
	Checksums      map[string]string `json:"checksums"` // component -> verified sha256
	Port           int               `json:"port"`

	// ContextSize is the LAST KNOWN EFFECTIVE context of this installation —
	// the number the `llm.models."Bonsai 2 27B".context_window` override is
	// generated from, and the value a launch falls back to when no plan was
	// recorded.
	//
	// It is a record that is *corrected*, not a decision that is *frozen*:
	// install writes the planner's figure, and every successful load overwrites
	// it with the context the server itself reports through `/props` (see
	// `Server.recordEffectiveContext`). That readback is what keeps the tier-1
	// config override honest — `contracts`/`llm-providers.md` gives a tier-1
	// override precedence over the tier-1.5 lazy probe, so a stale one could
	// never be corrected by the probe and would silently misreport the model's
	// real window forever.
	//
	// A FIT-SIZED plan has no concrete context to record (the runtime's own fit
	// pass chooses it at launch, and `MemoryPlan.ContextSize` is 0 there), so
	// the install records `FitMinContext` — the floor fit is held to — as the
	// estimate the override needs, and the first successful load replaces it
	// with the measured value. It never records 0: a zero override means "leave
	// the existing one alone" to `SyncEmbeddedLLMProvider`, and a zero context
	// in a manifest would read as a lost tier.
	ContextSize int `json:"context_size"`

	ModelFile   string `json:"model_file"`
	InstalledAt string `json:"installed_at"` // RFC 3339

	// Topology is the device-memory snapshot the recorded plan was made from —
	// the accelerators the provisioned runtime could see, whether their memory
	// aliases host RAM, and the two budgets the memory gate was allowed to
	// spend. It is persisted for two reasons. A load re-probes only
	// opportunistically and must fail SOFT, so the snapshot is what a wedged or
	// absent driver query falls back to; and a support bundle carries the
	// machine's real shape as measured at provision time rather than as guessed
	// from a backend name.
	//
	// Nil means "no probe ever answered" — a first install whose runtime would
	// not enumerate its devices, or a manifest written before this field
	// existed. A reader must treat nil as UNKNOWN, never as "no accelerator":
	// `MemoryTopology`'s own contract is that its zero value means unknown, and
	// a zero budget read as a fact is the refusal the combined gate exists to
	// avoid.
	Topology *MemoryTopology `json:"topology,omitempty"`

	// Plan is the launch shape LAST APPLIED to this installation: every
	// flag-bearing value `LaunchSpec` renders, the two footprints it expected,
	// the budgets it was gated against, and the human-readable `Notes` saying
	// why each non-default decision was made. `Server.launchSpec` builds the
	// shape half of a launch from it, so a load reproduces the decision the
	// install made instead of re-deriving a different one from a coarser policy.
	//
	// Nil means "no plan was recorded" — a manifest written before this field
	// existed — and a load then falls back to the pure policy in resolve.go
	// (explicit `-ngl`, `-fit off`, the manifest's `ContextSize`). Persisting
	// the plan rather than re-deriving it at load is deliberate: re-deriving
	// would need the operator's `Tuning`, and `tuning` is an operator SETTING
	// the sink carries verbatim, not install state a record may copy — a second
	// copy would be a second source of truth that goes stale on the first edit.
	Plan *MemoryPlan `json:"plan,omitempty"`

	// PackingReason says WHY this packing was chosen. It is recorded because
	// every value other than "default" is a degradation of some kind — a backend
	// with no PQ2_0 kernels, a pin that predates an upstream fix, a GPU
	// generation that decodes the smaller packing faster, or a measured budget
	// PQ2_0 did not fit — and a degraded install must state its reason instead
	// of leaving the user to infer it from a file size. Empty on a manifest
	// written before this field existed, which readers must treat as "unknown",
	// never as "default".
	PackingReason PackingReason `json:"packing_reason,omitempty"`

	// GPUFamily is the accelerator generation the plan was classified as, empty
	// when no device probe answered. Recorded so a support bundle carries the
	// silicon the guards and the packing rule were reasoning about.
	GPUFamily GPUFamily `json:"gpu_family,omitempty"`

	// Guards are the backend compatibility decisions that were in force when
	// this install was planned — each with its typed reason, its severity, its
	// upstream issue citation, its guidance, and an Applied flag saying whether
	// the plan actually changed because of it. They are persisted, not just
	// returned, because the whole point is that a degraded install stays
	// visible: a guard that fired is a fact about THIS install and must still be
	// readable after the process that made it exited.
	Guards []GuardDecision `json:"guards,omitempty"`
}

// ErrNotInstalled reports that no manifest exists, i.e. the model is not
// installed. It is a normal state, not a fault.
var ErrNotInstalled = errors.New("embedded LLM is not installed")

// ErrSmokeTestFailed reports that the freshly provisioned llama-server did not
// run. On macOS the cause is almost always Gatekeeper still blocking an
// unsigned binary; on Linux with a GPU backend it is missing CUDA 12 runtime
// libraries. The wrapped message carries the platform-appropriate fix.
var ErrSmokeTestFailed = errors.New("the provisioned llama-server did not run")

// ReadManifest loads the manifest at path. A missing file is reported as
// ErrNotInstalled rather than a generic OS error, because "not installed" is
// the state it encodes.
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

// manifestTempPattern is the shape of the sibling temporary file the atomic write
// renames from. The `*` makes the name UNIQUE PER WRITE: Installer.Install holds
// no supervisor gate while Server.recordEffectiveContext writes under one, so two
// writers can overlap, and a shared fixed name would let one of them rename a file
// the other is still writing — promoting a torn manifest that the next ReadManifest
// then fails on, reporting the model as not installed until a reinstall.
const manifestTempPattern = ManifestFileName + ".*.tmp"

// manifestTempPrefix and manifestTempSuffix let a test — and the cleanup,
// sweepStaleManifestTemps — recognise a leftover temporary without knowing the
// random middle os.CreateTemp picked.
const (
	manifestTempPrefix = ManifestFileName + "."
	manifestTempSuffix = ".tmp"
)

// manifestTempStaleAfter is how old a leftover temporary must be before
// sweepStaleManifestTemps will remove it.
//
// The age gate is what keeps the sweep safe against the very concurrency the
// unique name exists for: Installer.Install holds no supervisor gate while
// Server.recordEffectiveContext writes under one, so two writers CAN overlap, and
// an unconditional sweep would delete a temporary another writer is between
// creating and renaming — failing its write, which is the torn-manifest outcome
// this pattern was introduced to prevent. A manifest write is a few kilobytes, a
// sync and a rename, so a minute is orders of magnitude beyond one and still far
// short of the "forever" a crashed writer's leftover would otherwise last.
const manifestTempStaleAfter = time.Minute

// writeManifest persists m atomically: bytes land in a uniquely named sibling
// temporary file and are renamed over the target, so a crash mid-write leaves the
// previous manifest intact instead of a truncated one, and a concurrent writer
// cannot interleave with this one. Every error path attempts temporary cleanup;
// a failed cleanup is reported alongside the original write/promotion error.
//
// The one path that cannot clean up after itself is a crash BETWEEN CreateTemp and
// the rename, so the write begins by sweeping leftovers of that shape out of the
// directory (sweepStaleManifestTemps) — otherwise a uniquely named temporary is
// also a permanently leaked one, since nothing else knows its name.
//
// logger may be nil, which discards: the package's rule for an uninjected logger.
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
	// The temporary this write is about to create does not exist yet, so the
	// sweep can only ever see PREVIOUS writers' leftovers — never this call's own.
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

// A promotion rename can fail transiently on Windows: two concurrent
// writeManifest callers promote onto the SAME manifest.json (Install holds no
// supervisor gate while Server.recordEffectiveContext writes under one), and
// one MoveFileEx(REPLACE_EXISTING) can hold the destination while it swaps,
// answering Access denied to the other for a moment; a real-time scanner
// holding a freshly written temporary does the same. The lock is momentary —
// neither file is permanently unavailable — so a short bounded backoff turns
// the collision into a successful promotion instead of a failed manifest
// write, which is exactly what the concurrent-writers test pins. Off Windows
// the classifier always answers false and the first attempt decides.
const (
	// manifestPromoteAttempts bounds the promotion retry budget.
	manifestPromoteAttempts = 8

	// manifestPromoteBackoff is the base backoff between promotion retries,
	// doubled on every attempt (2ms..128ms; ~254ms in total at the cap).
	manifestPromoteBackoff = 2 * time.Millisecond
)

// manifestPromotionMu prevents this process's writers from colliding while
// replacing a manifest. Temp creation/write/sync stays concurrent. External
// sharing handles still use the bounded retry; no destination is removed.
var manifestPromotionMu sync.Mutex

// promoteManifest renames the fully written temporary over the target manifest,
// serializing in-process promotions and retrying transient Windows sharing
// errors with the same bounded backoff. Failure leaves the destination intact
// and returns the final rename error for the caller's cleanup/reporting.
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
// behind in dir, reporting each removal at Debug.
//
// It is best-effort in both directions: an unreadable directory or an unremovable
// entry is logged and skipped, because a leftover temporary costs a few kilobytes
// of disk and must never fail the manifest write that happened to notice it. Only
// entries whose name is the temporary shape AND whose modification time is older
// than manifestTempStaleAfter are touched — never the manifest itself, never a
// directory, and never a concurrent writer's live temporary.
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

// isManifestTempName reports whether name is the shape os.CreateTemp produces from
// manifestTempPattern: the manifest's own name, a random middle, and the temporary
// suffix. The manifest itself does not match, and neither does a name with nothing
// between the prefix and the suffix.
func isManifestTempName(name string) bool {
	return len(name) > len(manifestTempPrefix)+len(manifestTempSuffix) &&
		strings.HasPrefix(name, manifestTempPrefix) &&
		strings.HasSuffix(name, manifestTempSuffix)
}

// writeAndClose writes data to f and closes it whichever way the write goes, so
// the rename always operates on a complete file and no descriptor leaks on the
// error path.
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

// recordableContext is the context a MANIFEST may carry for a plan.
//
// A planner-computed shape has one: `MemoryPlan.ContextSize` is always positive
// when `Fit` is false, because with fit off nothing else would size it. A
// FIT-SIZED shape does not — `ContextSize` is 0 there by definition, since the
// runtime's own fit pass chooses the value at launch — but a manifest still owes
// its readers a number: `ContextSize` is what the `llm.models` `context_window`
// override is generated from, and a 0 there means "leave the existing override
// alone" rather than "the window is zero", so the honest figure would be lost.
//
// `FitMinContext` is that figure. It is the floor fit is held to (`-fitc`), so
// it is the smallest context the launch can legitimately end up with, and the
// first successful load replaces it with the value the server actually reports
// through `/props` — see `Manifest.ContextSize` and
// `Server.recordEffectiveContext`. Recording the floor instead of 0 is the
// conservative direction in both uses: an override that under-reports the window
// costs compaction headroom, while one that claims 0 costs the model its place
// in the context accounting entirely.
//
// fallback is the value a plan-less resolution already carries (`Resolution.
// ContextSize`), used when the plan is absent and for the RAM-tiered path.
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

// InstallState is the durable install record handed to the config layer. It
// maps one-to-one onto config.EmbeddedLLMConfig plus the resolved context
// tier, and it is the ONLY channel through which this subsystem reaches
// config.yaml — core never imports backend/config (backend/config sits above
// core and already imports core packages, so an import back would cycle).
type InstallState struct {
	Packing        Packing
	Backend        Backend
	Port           int
	ModelFile      string
	RuntimeVersion string
	InstalledAt    string // RFC 3339, matching Manifest.InstalledAt
	ContextSize    int

	// AutoUnloadEnabled and AutoUnloadMinutes are the DEFAULTS the install
	// establishes. The sink must apply them only where the operator has not
	// chosen explicitly (config.AutoUnloadConfig keeps both as pointers, so
	// "unset" is distinguishable from "explicitly false"), and must never
	// overwrite an existing choice: installing the model does not reset a
	// tuned idle budget.
	AutoUnloadEnabled bool
	AutoUnloadMinutes int

	// There are deliberately NO memory-tuning defaults here. `Tuning`'s zero
	// value IS the all-Auto plan, and config.TuningConfig keeps every knob a
	// pointer precisely so "the operator never wrote this" stays
	// distinguishable from "the operator wrote auto" — so an install that
	// shipped defaults for the sink to apply would collapse that distinction
	// on the first provision. What the sink owes the tuning section is
	// narrower and stronger: carry it through verbatim (see ConfigSink).
}

// ConfigSink persists install state into config.yaml. The backend layer
// supplies the implementation (backend/frontend_api_embedded.go):
//
//   - ApplyInstalled writes embedded_llm.* from state, (re)applies the
//     auto-unload defaults without overwriting explicit operator values, calls
//     Config.SyncEmbeddedLLMProvider(state.ContextSize) so the backend-owned
//     llm.openai_compatible.embedded entry and the llm.models context_window
//     override are generated from the authoritative state, persists, and
//     rebuilds the router.
//   - ApplyRemoved clears embedded_llm.installed and the informational fields,
//     migrates llm.default_model off "embedded/Bonsai 2 27B" (otherwise the
//     next load fails validation and the next settings save is rejected as
//     dangling), calls SyncEmbeddedLLMProvider(0) so the provider entry is
//     dropped, and persists.
//
// BOTH rewrite embedded_llm.* wholesale, so both must carry the two
// operator-owned sub-sections through unchanged: `auto_unload` (applying
// state.AutoUnload* to unset knobs only) and `tuning` (verbatim — it is a
// setting, not a record, so neither provisioning nor uninstalling the model
// may reset the memory plan the operator chose).
//
// Both must be idempotent: Install and Remove may be retried after a partial
// failure.
type ConfigSink interface {
	ApplyInstalled(ctx context.Context, state InstallState) error
	ApplyRemoved(ctx context.Context) error
}

// CommandRunner executes one external command and returns its combined output.
// It exists so the macOS provisioning steps (xattr, codesign, the --version
// smoke test) are testable on every platform without those binaries present.
//
// opts carries the launch configuration (see RunOptions): the smoke test passes
// one (the launch environment), every other call site passes nil and runs bare.
type CommandRunner func(ctx context.Context, name string, opts *RunOptions, args ...string) (string, error)

// RunOptions is the launch configuration a CommandRunner call can carry beyond
// the command itself. A nil pointer means "no opinion": the child inherits the
// parent's environment and working directory. The fields mirror exec.Cmd's
// semantics one-to-one.
type RunOptions struct {
	// Env is the child's complete environment; nil inherits the parent's.
	Env []string
	// Dir is the child's working directory; empty inherits the caller's.
	Dir string
}

// maxCommandOutputBytes caps what one provisioning command may contribute to the
// captured output. The output is diagnostic only — runBounded wraps it into an
// error message and the smoke test logs it — so an uncapped buffer is an
// unbounded allocation driven by an external process.
const maxCommandOutputBytes = 1 << 20

// defaultCommandRunner is the production runner. Every call site bounds it with
// its own context timeout, so a wedged codesign cannot stall the install.
//
// A context deadline alone does not bound the call, which is why two more bounds
// live here:
//
//   - cmd.WaitDelay. Killing the child is not enough to end the read: Stdout is
//     not an *os.File, so os/exec copies the child's output through a pipe, and a
//     grandchild that inherited the write end keeps that copy blocked long after
//     the child is gone. cmd.Run would then never return — and it runs on the
//     goroutine that owns the whole install for its duration, so that goroutine is
//     stranded for the lifetime of the process and whatever it serializes stays
//     serialized with it. No caller-side convention recovers that: a release
//     written as a defer never runs, and one written on the exit paths is never
//     reached. This is the hazard hardware.go's probe layer documents, and it
//     takes the same value.
//   - a cap on the captured bytes (limitedWriter — the write-side twin of
//     io.LimitReader, which does not fit because cmd.Stdout is a Writer).
//
// The child is spawned without a console window: since the smoke test became
// universal, this runner also executes llama-server, whose Windows build is a
// console-subsystem binary that would otherwise flash a console window (and
// steal focus) on every install click. HideConsole is a no-op off Windows, so
// the xattr/codesign calls are unaffected.
//
// opts configures the launch (env/dir); nil means "no opinion" — the child
// inherits the parent's environment and working directory.
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
		// The command itself exited successfully — ErrWaitDelay replaces a nil
		// exit status, never a real one — and only its output had to be abandoned
		// because something else kept the pipe open. The exit status is the
		// authority on whether xattr, codesign or --version worked, so this is not
		// a failure; the captured output may simply be short.
		return out.String(), nil
	}
	return out.String(), err
}

// limitedWriter captures at most limit bytes and counts the rest. Write always
// reports the full length: a short write would fail os/exec's copy and turn a
// successful command into an error. It is mutex-guarded because os/exec may copy
// stdout and stderr concurrently into the same writer.
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

// Auto-unload defaults established by an install. The minutes value mirrors
// config.EmbeddedLLMDefaultAutoUnloadMinutes; core cannot import that constant
// (see InstallState), so it is declared here and pinned equal by
// TestEmbeddedAutoUnloadDefaultsMatchCore in backend/config.
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

// macOS provisioning helpers, invoked by absolute path. Both ship with the base
// system, so there is nothing to resolve — and resolving through PATH would let
// an attacker-controlled PATH element substitute a binary that runs during an
// install click. See provisionDarwin.
const (
	darwinXattr    = "/usr/bin/xattr"
	darwinCodesign = "/usr/bin/codesign"
)

// toolMissing reports whether a provisioning helper is simply not installed. An
// absolute path is not resolved through exec.LookPath, so a missing one surfaces
// as fs.ErrNotExist from the spawn rather than as exec.ErrNotFound; both mean the
// same thing here, and both stay a warning rather than a failure — the smoke test
// is the authority on whether the runtime runs.
func toolMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// Extraction guards. Defence-in-depth against a checksum-valid-but-malicious
// archive, mirroring core/toolmanager's posture but sized for this subsystem:
// the tool-manager's 512 MiB per-entry cap is below a single CUDA runtime
// library in these archives (ADR-066, "Not a tool-manager extension"), so the
// caps are per-entry 2 GiB and per-archive 8 GiB — far above the ~1.5 GiB a
// fully extracted runtime actually occupies.
const (
	maxExtractEntryBytes int64 = 2 << 30
	maxExtractTotalBytes int64 = 8 << 30
)

// Installer orchestrates Install and Remove over a Layout.
//
// The zero value is not usable; build one with NewInstaller. Every external
// effect is reachable through a field, so the whole flow is testable without
// a network, without multi-gigabyte artifacts and without macOS binaries:
// Downloader (HTTP client, free-space probe), Probe (hardware), RunCommand
// (xattr/codesign/smoke test), AllocatePort and Now.
type Installer struct {
	// Layout is the on-disk footprint. Required.
	Layout Layout
	// Sink persists install state into config.yaml. Required by Install and
	// Remove: an install that cannot be registered would leave bytes on disk
	// with no provider, and a removal that cannot clear the config would leave
	// a provider pointing at nothing.
	Sink ConfigSink
	// Logger receives the subsystem's diagnostics. nil → a discard logger
	// (never the global slog.Default), matching the package's own
	// no-global-logging rule.
	Logger *slog.Logger
	// Downloader fetches and verifies artifacts. nil → NewDownloader(nil, Logger).
	Downloader *Downloader
	// Probe reads the machine's RAM and accelerator. nil → ProbeHardware.
	Probe func(ctx context.Context, logger *slog.Logger) (Hardware, error)
	// ProbeDevices reads the accelerator INVENTORY of a provisioned runtime, so
	// an install can learn the GPU generation — which the packing rule and the
	// compatibility guards both need — instead of guessing it from the backend.
	// nil → ProbeDevices (the package function). Fail-soft like every other
	// probe here: an answer of false leaves the plan on its statically
	// decidable guards, it never fails an install.
	ProbeDevices func(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool)
	// RunCommand executes xattr/codesign/--version. nil → defaultCommandRunner.
	RunCommand CommandRunner
	// AllocatePort reserves a free loopback port. nil → ephemeralLoopbackPort.
	// The supervisor (server.go) owns port persistence and the pre-load
	// collision re-check; it may substitute a richer allocator.
	AllocatePort func(ctx context.Context) (int, error)
	// Stop terminates a running server before Install replaces the runtime tree
	// it executes from, and before Remove deletes its files. nil is legal and
	// means "no supervisor is wired yet" — both then proceed, and the OS
	// releases the files when the process exits.
	Stop func(ctx context.Context) error
	// StopTimeout bounds Install's step-0 Stop. Zero — the default — inherits
	// the caller's context, deadline and all: an install run on a
	// never-deadlined context then waits out whatever holds the supervisor.
	//
	// A caller that wires Stop to a supervisor SHOULD set it, because
	// Server.Stop acquires the single-instance gate FIRST and an in-flight Load
	// holds that gate for up to DefaultReadyTimeout (15 min). An expired budget
	// is not a worse outcome than the wait: it is what makes the supervisor's
	// no-gate force path reachable at all, so a resident child is terminated on
	// the force path's own detached budget instead of being waited out — and
	// step 0 runs before plan, so an unbounded wait here is an install bar stuck
	// at 0% with no progress event ever emitted.
	//
	// This is NOT Server.StopTimeout, which is the graceful window between the
	// termination signal and the kill. This one bounds the whole step-0 call,
	// gate wait included. Remove's stop is left to the ctx its caller passes
	// (the backend wraps the entire Remove in the same budget), so the field
	// governs step 0 only.
	StopTimeout time.Duration
	// Now stamps InstalledAt. nil → time.Now.
	Now func() time.Time
	// HostOS overrides the OS the macOS provisioning branch keys off. Empty →
	// runtime.GOOS. Tests set it to "darwin" to exercise quarantine-clear,
	// ad-hoc signing and the smoke test on any platform.
	HostOS string
}

// NewInstaller returns an Installer with production defaults for the given
// layout. Wire Sink before calling Install or Remove.
func NewInstaller(layout Layout, logger *slog.Logger) *Installer {
	return &Installer{Layout: layout, Logger: logger}
}

// InstallOptions parameterises one install run.
type InstallOptions struct {
	// Port is the loopback port to persist. Zero means "allocate a free one".
	Port int
	// Platform overrides the probed "<goos>-<goarch>" key. Empty → probed.
	Platform string
	// Backend overrides the probed accelerator. Empty → probed. Resolution
	// still degrades an override it cannot serve.
	Backend Backend
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

	// Guards is the complete compatibility record of this install: every
	// decision the guard table made for this machine, whether c0wrk acted on it
	// (Applied=true, e.g. a CUDA 13.3 plan swapped to 12.8 before anything was
	// downloaded) or could only disclose it (Applied=false, e.g. a substitution
	// discovered after the runtime archive was already staged, or an advisory
	// whose workaround trades away something the user may want to keep).
	//
	// It is the same list the Manifest persists, lifted to the top level because
	// a degraded install is the first thing a caller should be able to see
	// without walking the report's nested records.
	Guards []GuardDecision

	// PackingReason is why the installed packing is what it is, lifted from the
	// resolution for the same reason.
	PackingReason PackingReason
}

// Install provisions the pinned runtime and weights for this machine.
//
// A resident server is stopped BEFORE the first numbered step, symmetrically
// with Remove: it executes out of the runtime tree step 7 retires (on Windows
// renaming a live process's own tree and working directory is refused
// outright), and the gigabytes it holds would otherwise be priced as
// unavailable by step 1's probe and step 2's memory gate. That stop is bounded
// by StopTimeout when the caller set one; Remove's is bounded by the ctx its
// caller passes instead.
//
// The order is fixed and each step is a gate. The numbering starts at 1 because
// the stop above is step 0 — a precondition the rest of the sequence relies on,
// not one of the install's own gates:
//
//  1. hardware probe — RAM is a hard input (ErrRAMUnknown refuses);
//  2. Resolve — the combined memory gate is its FIRST check, so a machine
//     whose memory no modelled shape fits plans no assets, creates no
//     directory and downloads nothing (ErrInsufficientMemory, naming both
//     pools with both numbers);
//  3. a whole-set disk guard, before the first byte is fetched;
//  4. port allocation, persisted in both the manifest and the config;
//  5. the runtime archives (and the Windows CUDA companion): download with
//     resume and per-component progress → SHA256 verification gate → extract
//     into a staging tree;
//  6. runtime provisioning: quarantine-clear + ad-hoc codesign on macOS, then
//     a --version smoke test on EVERY platform, all against the staged tree —
//     before the weights, so an unrunnable runtime (Gatekeeper, missing CUDA
//     12 libraries) fails in seconds instead of after a multi-gigabyte
//     download;
//  7. the staged runtime replaces the previous one — the old tree is retired
//     only after every new byte is secured and proven to run;
//  8. the weights (model, mmproj): download → SHA256 verification gate,
//     straight to their final paths;
//  9. manifest.json, written atomically and only now;
//
// 10. the config sink, which registers the provider entry.
//
// A failure at any step leaves no manifest and registers no provider: a
// half-installed model is never visible to the router. Bytes that DID verify
// are deliberately kept — they are cache hits for the next attempt, which is
// what makes a multi-gigabyte install resumable rather than restartable.
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

	// Step 0, and the reason it precedes the probe: a server that is still
	// resident executes out of the runtime tree this install is about to retire,
	// and holds the very memory the gate below is about to price. Stopping first
	// is what makes a repair safe on Windows (renaming a live process's own tree
	// and working directory is ERROR_ACCESS_DENIED there) and honest everywhere
	// else. It is a no-op when nothing is running.
	//
	// The wait is BOUNDED by StopTimeout when the caller set one, and the bound
	// is the difference between a repair and a stall: the supervisor's Stop
	// takes its single-instance gate first, and a Load that claimed that gate
	// before this install did can hold it for DefaultReadyTimeout (15 min) —
	// which, step 0 running before plan and therefore before the first
	// install_progress event, would present as an install bar at 0% that never
	// moves while every other embedded operation refuses. An EXPIRED budget is
	// not a failure mode, it is the force path: core then stops the resident
	// child without the gate, on a detached budget of its own, so the model is
	// terminated rather than waited out. Only a stop that finds no child to kill
	// — a Load still reading the manifest or scanning for a port — reports the
	// gate timeout as an error, and that error is the whole truth.
	if in.Stop != nil {
		if err := in.stopBeforeInstall(ctx); err != nil {
			return nil, fmt.Errorf("embeddedllm: stopping the inference server before installation: %w", err)
		}
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

	// Phase order is deliberate: the runtime is downloaded, extracted,
	// provisioned (macOS signing where applicable) and smoke-tested BEFORE the
	// multi-gigabyte weights are fetched, so a runtime that cannot execute
	// (Gatekeeper, a missing GPU library) fails the install in seconds instead
	// of after an hour of downloading.
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

	// The staged runtime can now answer the device probe, which is the last
	// moment a refinement is still cheap: the multi-gigabyte weights have not
	// been fetched yet. A first install learns its GPU generation here (a
	// repair already learned it in plan), so the generation-aware packing rule
	// and the device-dependent guards apply to the weights that are about to be
	// downloaded. A guard that wants a DIFFERENT RUNTIME is past its moment —
	// the archive is on disk and signed — so it is recorded as guidance rather
	// than silently dropped.
	res, weightAssets, refinedTopology, err := in.refineWithStagedDevices(ctx, platform, hw, res, serverBinary, weightAssets)
	if err != nil {
		// The measured budget refused the machine after the runtime was staged:
		// stop before the multi-gigabyte weights are fetched.
		return nil, err
	}
	if refinedTopology != nil {
		// The staged runtime answered, and the resolution that goes with it was
		// refined by that answer, so the pair the manifest records is the pair
		// the weights about to be downloaded were chosen for. A repair keeps the
		// topology plan() measured off the pre-existing tree; a first install
		// learns it here.
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
	// The plan is recorded because a load must reproduce the shape this
	// machine was provisioned for, and it can only do that from a record: the
	// operator's `tuning` is a setting the sink carries verbatim, not install
	// state a manifest may copy, so re-deriving the plan at load would need a
	// second source of truth that goes stale on the first edit.
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
		// The bytes are fine and the manifest describes them accurately; only
		// the registration failed. Removing the manifest again would throw away
		// a correct record, so the error is returned as-is and a retry is
		// idempotent (the downloads are cache hits, the manifest is rewritten).
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

// stopBeforeInstall performs Install's step-0 stop, under StopTimeout when one
// is set and under the caller's ctx alone otherwise. The caller has already
// checked Stop for nil. The helper exists so the bound cannot be lost by a future
// edit to the call site, and so the derived context is cancelled as soon as the
// stop returns instead of living for the rest of a multi-gigabyte install.
func (in *Installer) stopBeforeInstall(ctx context.Context) error {
	if in.StopTimeout <= 0 {
		return in.Stop(ctx)
	}
	stopCtx, cancel := context.WithTimeout(ctx, in.StopTimeout)
	defer cancel()
	return in.Stop(stopCtx)
}

// plan runs the pure gates — the hardware probe, the memory gate and
// resolution — and returns both results together with the platform key the plan
// was made for. The memory refusal lives inside Resolve as its first check, so
// nothing is planned and nothing is downloaded on a machine that cannot hold
// the model.
//
// The DEVICE PROBE is an optional refinement, and plan tries to obtain it
// without spending anything: a repair or a reinstall still has the previously
// provisioned runtime on disk, which can answer `--list-devices` before a
// single byte is downloaded. When it answers, the plan is refined with BOTH
// halves of the answer — the classified GPU generation, so the
// backend-substituting compatibility guards (KNOWN_ISSUES #197, #223) and the
// generation-aware packing rule apply to the artifacts about to be fetched
// rather than to a record written after the fact, AND the measured memory
// budgets, so the gate and the launch shape are priced against the card that is
// actually there. A first install has no binary to ask and keeps
// GPUFamilyUnknown here; Install probes the staged runtime later and refines
// what is still refinable.
//
// Without a topology the gate does NOT fall back to a RAM floor. It derives the
// host budget from the RAM probe with the same reserve policy a topology
// carries, and classifies the accelerator axis statically: a backend whose
// memory is independent of host RAM (a discrete CUDA or ROCm card on amd64)
// leaves the device side UNREADABLE, which degrades the gate to the host pool
// and says so in `MemoryPlan.Notes` rather than refusing — that refusal is the
// bug this gate replaced. A unified or absent accelerator is priced from the RAM
// probe alone, which measures both. FitsPQ2_0 is still left Unknown, so no
// capacity downgrade is ever inferred from an unmeasured budget.
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

	// The CUDA 12.x userland verdict rides with the profile so BOTH passes —
	// this statically decidable one and the device-aware refinement below —
	// gate the Linux #222 substitution on the same measured fact.
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

	// probed is returned only when it INFORMED the resolution that goes with
	// it. A topology the re-plan rejected is still a true measurement, but
	// recording it beside a plan that was not made from it would break the pair
	// the manifest promises — and a load's fail-soft path reads them as one
	// fact. Losing the measurement costs a support bundle one line; a
	// mismatched pair costs a launch shape nobody reasoned about.
	var probed *MemoryTopology
	if topology, ok := in.memoryTopologyFromBinary(ctx, in.existingServerBinary(res.Backend)); ok {
		gpu := ClassifyGPUs(topology.Devices)
		profile.GPU = gpu
		refined, refineErr := Resolve(ResolveInput{MachineProfile: profile, Topology: &topology})
		if refineErr != nil {
			// A MEMORY refusal is not "lost information" — it is the measured
			// budget saying this machine cannot hold the model, discovered
			// before a single byte was fetched. It must stop the install
			// rather than fall back to a plan that would OOM at load.
			if errors.Is(refineErr, ErrInsufficientMemory) {
				return Hardware{}, "", Resolution{}, nil, fmt.Errorf("embeddedllm: %w", refineErr)
			}
			// Anything else: the first plan is valid and the refinement only
			// adds information. Losing it degrades to the guards that are
			// statically decidable.
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

// existingServerBinary returns the llama-server of an already-provisioned
// runtime for a backend, or "" when there is none. It exists so a repair or a
// reinstall can answer the device probe before downloading anything, and it
// never creates anything: a missing tree is the normal first-install case.
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

// memoryTopologyFromBinary asks a provisioned runtime what accelerator memory
// it can see. It is FAIL-SOFT in every direction, like the probe it wraps: no
// binary, a hung one or an unrecognized inventory all yield (zero, false), and
// the caller keeps the plan it already had. A memory measurement refines a
// plan; it is never a precondition of one.
//
// It returns the whole topology rather than only the GPU generation it used to,
// because the two consumers of the probe need different halves of it and the
// probe is expensive enough to run once: the compatibility guards and the
// packing rule need the FAMILY, and the memory gate needs the BUDGETS. A first
// install has no binary to ask, so its gate runs on the derived host budget and
// treats the device side as unreadable — see gateBudgetsFor.
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

// guardIDsForLog renders the guards that a plan change acted on, for one log
// line. Only the applied ones are interesting there: the record itself carries
// every decision, applied or not.
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

// refineWithStagedDevices is the second, device-aware pass over a plan: it
// probes the freshly staged runtime, classifies the accelerator, and folds what
// that reveals into the resolution BEFORE the weights are fetched.
//
// It never downloads. Four outcomes, in increasing order of what could still be
// changed:
//
//   - no answer (no probe, an unrecognized inventory, or a plan that already
//     knew its GPU family): the resolution is returned untouched;
//   - a guard or the packing rule wants something different and the BACKEND is
//     unchanged: the refined resolution and its weight assets are returned, so
//     the weights that are about to be downloaded are the right ones. This is
//     the case the refinement exists for — an Ada card that should get PTQ1_0,
//     or #223's garbled-output guard on a machine whose iGPU only a device
//     probe could name;
//   - a guard wants a DIFFERENT BACKEND: the runtime archive for the planned
//     backend is already staged, signed and smoke-tested, so the substitution
//     is recorded as guidance instead of applied. The install completes and the
//     record says why it may not work, which is the difference between a
//     documented upstream failure and a mystery;
//   - the measured budget REFUSES the machine: ErrInsufficientMemory is returned
//     and the install stops. This is the one failure, and it is not "lost
//     information" — it is the same verdict `Installer.plan` treats as a hard
//     stop when the same Resolve produces it before anything is staged. Both
//     passes run the same host-budget arithmetic today, so the branch is not
//     reachable in practice; making it a stop rather than a Debug line keeps that
//     equivalence from being load-bearing, because a future divergence would
//     otherwise spend an hour downloading multi-gigabyte weights for a machine
//     the measured topology has just refused.
//
// Guards are merged rather than replaced, so a decision that WAS applied in
// plan (a static one, like #222) keeps its Applied flag and its place in the
// record.
//
// The third result is the measured `MemoryTopology` — non-nil ONLY in the second
// and third outcomes' successful form, i.e. only when the returned resolution
// was actually refined by it, so the caller can persist a topology and a plan
// that describe one another. A branch that kept the resolution it came in with
// returns nil and the caller keeps whatever `plan` measured.
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
		// Anything else: the unrefined plan is valid and only the extra
		// information is lost.
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
	// The measurement is returned only here, where the resolution that goes
	// with it was actually refined by it. Every earlier branch keeps the plan
	// it came in with, so it keeps the topology (if any) that plan was made
	// from — see Installer.plan for why the pair must not be split.
	measured := topology
	return res, weights, &measured, nil
}

// markGuardsUnappliable rewrites a set of decisions that were computed for a
// substitution c0wrk is no longer able to make: the runtime archive is already
// on disk. Every backend substitution loses its Applied flag and gains the
// reason it could not be applied, so the record explains itself instead of
// looking like an oversight.
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

// splitRuntimeAssets separates the runtime archives (the server build plus the
// Windows CUDA companion) from the weights (model, mmproj). The two groups are
// fetched in different phases — see Install.
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

// prepareStaging clears and recreates the tree a runtime is extracted into. A
// staging tree left behind by a previous failed attempt would mix old and new
// bytes, so extraction always starts from an empty directory.
func (in *Installer) prepareStaging(staging string) error {
	if err := in.removeOwned(staging); err != nil {
		return err
	}
	return mkdirAll(staging)
}

// fetchComponents downloads and verifies each asset, recording its digest in
// checksums. When staging is non-empty the assets are runtime archives: each is
// extracted into the staging tree, and its StageDone is deferred to
// provisionRuntime so the component's stream ends when the runtime is actually
// usable rather than when its bytes landed.
func (in *Installer) fetchComponents(ctx context.Context, opts InstallOptions, assets []Asset,
	staging string, checksums map[string]string,
) error {
	deferDone := staging != ""
	for _, asset := range assets {
		digest, err := in.fetchOne(ctx, opts, asset)
		if err != nil {
			return err
		}
		checksums[string(asset.Component)] = digest

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

// fetchOne downloads one artifact and enforces the verification gate.
//
// Downloader.Download already refuses to promote unverified bytes, so this is
// a second, independent gate rather than a re-hash of a multi-gigabyte file:
// a result that is not marked verified, or whose digest differs from the pin,
// aborts the install. Fail-closed is the only acceptable behaviour here
// (ASI04) — "probably fine" is how an unverified binary ends up executed.
func (in *Installer) fetchOne(ctx context.Context, opts InstallOptions, asset Asset) (string, error) {
	in.emit(opts, Progress{Component: asset.Component, Stage: StageDownloading,
		BytesDone: 0, BytesTotal: asset.SizeBytes})

	dst, err := in.Layout.Destination(asset)
	if err != nil {
		return "", err
	}
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

// provisionRuntime provisions the staged runtime, smoke-tests it on every
// platform, swaps it into place, locates the server binary and closes out the
// runtime components' progress streams.
//
// The macOS-only halves (quarantine-clear and ad-hoc codesign) run first when
// the host is darwin; the --version smoke test runs on EVERY platform. The
// order matters: a Linux CUDA runtime that cannot load its libraries and a
// macOS binary Gatekeeper still blocks are the same failure — an unrunnable
// runtime — and both must fail the install here, in seconds, before the
// multi-gigabyte weights are fetched.
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
// applies an ad-hoc signature to every Mach-O image in it, and proves the
// result actually executes.
//
// Why: artifacts fetched outside a browser still carry
// com.apple.quarantine, and arm64 macOS refuses to execute an image whose
// signature is missing or invalid. The pinned fork archives are unsigned CI
// builds, so both steps are required before llama-server will run at all.
//
// Both helpers are invoked by ABSOLUTE path. They are part of the base system,
// and resolving them through PATH instead would let a PATH element an attacker
// controls (a hijacked shell profile plus an app relaunch — the app loads the
// login shell's environment at startup) substitute a `codesign` that runs
// arbitrary code during a later install click.
//
// A missing helper is still a warning, not a failure: the smoke test is the
// authority on whether the runtime runs, and refusing to install on a machine
// without /usr/bin/codesign would be a worse outcome than trying.
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

// smokeTest runs "llama-server --version" against the staged tree and turns a
// failure into an actionable message. On macOS the overwhelmingly likely cause
// is Gatekeeper; on Linux a GPU-accelerated build it is missing CUDA 12
// runtime libraries. "the install failed" with no next step is not an
// acceptable outcome for a user who just downloaded 7 GiB.
//
// The binary runs under the LAUNCH environment — the same launchEnv the
// supervisor's spawn builds, with the runtime's binary directory prepended to
// the platform's dynamic-library search path, and with the same working
// directory — so the smoke test exercises the loader path the resident server
// will use. A Linux CUDA or ROCm build that resolves its sibling libraries
// through the launch-provided search path would otherwise fail this test (and
// with it the whole install) with a library-not-found error the real launch
// would never hit.
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
// failed smoke test. The backend names the build that failed; on Linux the
// CUDA builds are the ones whose failure mode is an environment problem
// (missing CUDA 12 runtime libraries) rather than a broken download, so the
// hint points there. Windows CUDA builds are excluded: they ship their CUDA
// runtime as a bundled companion archive and never consult the system CUDA
// userland (ADR-073), so a Windows failure cannot be the missing-libraries
// case — the generic run-the-binary diagnostics below name the real suspects
// (a missing MSVC runtime, an incomplete extraction, a Defender block).
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

// promoteRuntime swaps the staged tree into place. The previous tree is
// retired rather than deleted first, so a failed rename rolls back instead of
// leaving the install with no runtime at all — the subsystem's version of
// "secure the new bytes before destroying the old".
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

// RemoveScope names what a Remove deletes. The scope only chooses WHICH
// embedded-LLM bytes are deleted — the manifest and the config registration
// are cleared under every scope, so a partial removal leaves a cache, never a
// half-registered install.
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

// valid reports whether s is one of the four known scopes. The zero value is
// deliberately invalid: a caller that forgot to choose must not get "all".
func (s RemoveScope) valid() bool {
	switch s {
	case RemoveAll, RemoveRuntime, RemoveWeights, RemoveProjection:
		return true
	default:
		return false
	}
}

// ParseRemoveScope maps a wire-scope string onto a RemoveScope. The empty
// string means RemoveAll — the historical, scope-less removal — so an older
// caller (or a generated binding invoked without the argument) keeps its
// meaning. Anything else unknown is refused, never defaulted.
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

// Leftovers reports which embedded-LLM artifacts are still on disk. While an
// install is recorded these fields are not interesting (everything present is
// accounted for by the manifest); their use is the NOT-installed state, where
// they describe what a scoped removal left behind — a cache a reinstall
// re-verifies, or bytes a further removal can reclaim.
type Leftovers struct {
	// Runtime — at least one "llama-*" tree (installed, staged or retired) or
	// the archive staging area exists under the runtimes root.
	Runtime bool
	// Weights — at least one pinned model GGUF exists in the model root.
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

// RemoveWithScope stops a running server and removes the parts of the
// installation the scope names, plus — under EVERY scope — the manifest and
// the config registration. A partial removal is therefore uniform: the
// on-disk state becomes "not installed", and whatever the scope spared is a
// cache, not an install. A later Install verifies the surviving artifacts
// against their pins and re-downloads only what is missing (the downloader's
// verified-cache fast path).
//
// The config is cleared even when a deletion failed, and the deletion error is
// returned afterwards: a leftover directory wastes disk and a retry reclaims
// it, while a config that still claims an install whose files are gone points
// the router at a dead provider. Deletions are gated on Layout.Owns, so no
// path outside the two embedded-LLM trees can be removed — in particular never
// the flat embedding-model files that share <agentDir>/models and never
// anything under <toolsDir>/bin.
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
	// The manifest goes with EVERY scope: it is install state, not artifact
	// bytes, and a record describing files a scoped removal just deleted — or
	// one describing an install coexisting with no runtime tree — would point
	// the supervisor at bytes that may no longer be there. It goes FIRST, so
	// the on-disk state is "not installed" before anything else happens.
	if manifest, err := in.Layout.ManifestPath(); err != nil {
		errs = append(errs, err)
	} else if err := in.removeOwned(manifest); err != nil {
		errs = append(errs, err)
	}

	removed := 0
	switch scope {
	case RemoveRuntime:
		removed = in.removeScopeRuntimeTrees(&errs)
	case RemoveWeights:
		in.removeScopeWeights(&errs)
	case RemoveProjection:
		in.removeScopeProjection(&errs)
	default: // RemoveAll — the historical full removal
		if err := in.removeOwned(in.Layout.ModelRoot); err != nil {
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

// removeScopeRuntimeTrees is RemoveWithScope's runtime half: every llama-* tree
// plus the archive staging area. Returns the number of trees deleted.
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
// GGUFs, so the cleanup reaches a packing the recorded install did not use too.
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

// DetectLeftovers reports which embedded-LLM artifacts are on disk right now.
// It is a cheap existence scan — one directory listing and per-file stats,
// never a walk of multi-gigabyte trees and never a hash — so the status read
// can afford it. It reports what EXISTS; whether those bytes are still wanted
// is the caller's question (it differs between an installed model and a
// not-installed one with residue).
func (in *Installer) DetectLeftovers() (Leftovers, error) {
	var lo Leftovers
	entries, err := os.ReadDir(in.Layout.RuntimesRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return lo, fmt.Errorf("embeddedllm: listing %q: %w", in.Layout.RuntimesRoot, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), runtimeDirPrefix) {
			lo.Runtime = true
			break
		}
	}
	// The archive staging area counts as runtime residue: its contents are
	// runtime/cudart archives (and their resumable partials).
	if downloads, derr := in.Layout.DownloadsDir(); derr == nil && pathExists(downloads) {
		lo.Runtime = true
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

// removeRuntimeTrees deletes every "llama-*" tree under the runtime root —
// the installed one, plus any .staging or .old leftover from an interrupted
// install — and reports how many were removed.
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
		if !strings.HasPrefix(entry.Name(), runtimeDirPrefix) {
			continue
		}
		// Built through the layout's containment-checked join, then deleted
		// through the single gated primitive: neither step can reach outside
		// the runtime root.
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

// removeOwned deletes path only when the layout owns it. It is the single
// destructive primitive in this subsystem, so the agent-isolation invariant
// (nothing under <toolsDir>/bin, nothing outside the two trees) is enforced in
// exactly one place.
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

// checkWholeSetDiskSpace refuses up front when the volume cannot hold the
// entire resolved set plus headroom, so a doomed 7 GiB download fails in
// milliseconds instead of after an hour. Measurement failure is best-effort
// and non-fatal, matching the Downloader's per-artifact guard: a genuinely
// full disk still fails safely on ENOSPC, which leaves a resumable partial.
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

// extractArchive unpacks a .tar.gz/.tgz or .zip archive into destDir. Entries
// are containment-checked through pathutil, so a traversal entry is skipped
// rather than written outside the tree, and both a per-entry and a total
// decompressed-size cap bound a checksum-valid-but-malicious archive.
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
// whether it may be written. Containment is decided by pathutil, never by an
// inline prefix comparison; an escaping entry is skipped, matching the
// tool-manager's behaviour.
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
// would escape destDir: a symlink pointing outside the tree would let the
// runtime load (or let a later deletion follow) an arbitrary path.
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
	_ = os.Remove(target)
	if err := os.Symlink(linkname, target); err != nil {
		return fmt.Errorf("creating symlink %q: %w", target, err)
	}
	return nil
}

// machOImages lists the files in a macOS runtime tree that need a signature:
// the llama-* executables and the dylibs they load. arm64 macOS refuses to
// execute an image without a valid signature, so signing only the server would
// leave it unable to load its own libraries.
func machOImages(runtimeDir string) ([]string, error) {
	var images []string
	walkErr := filepath.WalkDir(runtimeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !needsAdHocSignature(d.Name()) {
			return nil
		}
		// An entry whose containment cannot be resolved is skipped instead of
		// signed: signing is a mutation, and fail-closed is the only acceptable
		// default for a path this subsystem did not itself write.
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
// reading the assignment back. It is the production allocator, used whenever
// Installer.AllocatePort is nil; the install flow persists the port it returns,
// and server.go's EnsurePort seam may substitute a different one before a spawn.
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
