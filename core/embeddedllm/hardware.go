// Package embeddedllm provisions and supervises the fully local inference
// runtime: a pinned PrismML-Eng/llama.cpp fork build plus the pinned
// Ternary-Bonsai-2-27B weights, configured for the machine's actual hardware
// and served as a loopback OpenAI-compatible endpoint.
//
// This file owns the hardware probe: total system RAM and the accelerator
// backend the runtime archive must be built for. The probe is the only
// I/O-performing half of resolution — deriving the packing, the launch flags
// and the context size from a probe result is a pure function in resolve.go.
//
// Detection order and every numeric policy here mirror the fork's own demo
// scripts (PrismML-Eng/Bonsai-demo scripts/common.sh: bonsai_llama_ngl,
// bonsai_image_max_tokens, bonsai_ctx_default, pq2_0_ready_backend), so that
// c0wrk provisions exactly what the upstream demo would have provisioned on
// the same machine. See specs/domains/embedded-llm.md and ADR-066.
package embeddedllm

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/v0lka/sp4rk/sysproc"
)

// Backend is the accelerator the runtime archive was built for, and the one
// the probe selected. Resolution order is fixed (see ProbeHardware).
type Backend string

const (
	// BackendMetal is Apple Silicon's Metal build (darwin/arm64 only). The
	// fork ships no MLX server for Bonsai 2, so Metal is the macOS path.
	BackendMetal Backend = "metal"
	// BackendCUDA124 is the CUDA 12.4 asset tag (x64 only).
	BackendCUDA124 Backend = "cuda-12.4"
	// BackendCUDA128 is the CUDA 12.8 asset tag (x64 only).
	BackendCUDA128 Backend = "cuda-12.8"
	// BackendCUDA133 is the CUDA 13.3 asset tag (x64 only).
	BackendCUDA133 Backend = "cuda-13.3"
	// BackendROCm is the AMD ROCm/HIP build (x64 only).
	BackendROCm Backend = "rocm"
	// BackendVulkan is the Vulkan build. It is the one backend without
	// optimized PQ2_0 kernels, which is what forces the PTQ1_0 packing.
	BackendVulkan Backend = "vulkan"
	// BackendCPU is the always-available fallback build.
	BackendCPU Backend = "cpu"
)

// IsCUDA reports whether the backend is one of the pinned CUDA asset tags.
func (b Backend) IsCUDA() bool {
	switch b {
	case BackendCUDA124, BackendCUDA128, BackendCUDA133:
		return true
	default:
		return false
	}
}

// x64Only reports whether the backend's runtime archive is published for x64
// only. CUDA and ROCm are: a non-x64 machine that reports one of them still
// has to be provisioned with the CPU build (see effectiveBackend).
func (b Backend) x64Only() bool {
	return b.IsCUDA() || b == BackendROCm
}

// gpuAccelerated reports whether the backend offloads layers to a GPU, which
// is what -ngl 99 means. Apple Silicon's Metal counts as a GPU even though it
// shares system RAM.
func (b Backend) gpuAccelerated() bool {
	switch b {
	case BackendMetal, BackendROCm, BackendVulkan:
		return true
	default:
		return b.IsCUDA()
	}
}

// CUDA12Userland is the tri-state verdict on the CUDA 12.x USERLAND the
// runtime's launcher dynamically links against (libcudart.so.12 +
// libcublas.so.12). It is deliberately separate from Backend: the backend
// ladder answers "what build should I download", while this answers "would a
// CUDA 12.x build find its libraries at load time".
type CUDA12Userland string

const (
	// CUDA12Present — both libcudart.so.12 and libcublas.so.12 were found AND
	// each ELF's embedded DT_SONAME is its own ".so.12" name.
	CUDA12Present CUDA12Userland = "present"
	// CUDA12Absent — no candidate for at least one library, or a candidate's
	// SONAME names another series (the ".so.12" symlink pointing at a 13 ELF).
	CUDA12Absent CUDA12Userland = "absent"
	// CUDA12Unknown — a probe could not decide: ldconfig itself is missing, or
	// a candidate was found but could not be inspected (unreadable, truncated,
	// a stripped ELF without DT_SONAME).
	CUDA12Unknown CUDA12Userland = "unknown"
)

// Hardware is the probe result: everything resolution may depend on.
//
// It answers "which archive do I download, and how big a context can this
// machine hold", and it deliberately carries NO device-memory field: at probe
// time the runtime is not on disk yet, so there is nothing to ask what it can
// see. Accelerator memory is a second, LATER probe over the installed binary —
// `ProbeDevices` and `MemoryTopology` in topology.go, which is why the two
// results are kept apart rather than folded into one struct.
type Hardware struct {
	// Platform is the "<goos>-<goarch>" key (the toolmanager.Platform()
	// shape), e.g. "darwin-arm64", "windows-amd64".
	Platform string
	// Arch is the bare GOARCH: "amd64" | "arm64".
	Arch string
	// RAMGiB is total system RAM in GiB (bytes / 2^30), not GB.
	RAMGiB float64
	// Backend is the probed accelerator; BackendCPU when nothing else was
	// found. It reports what was DETECTED, before the x64-only rule of
	// effectiveBackend is applied.
	Backend Backend
	// CUDATag is the driver-derived CUDA asset tag ("12.4" | "12.8" |
	// "13.3"), empty when the backend is not CUDA.
	CUDATag string
	// CUDA12Userland is the tri-state verdict on the CUDA 12.x userland
	// (libcudart.so.12 + libcublas.so.12 with matching DT_SONAMEs). present
	// requires BOTH libraries to be genuine .12 ELFs; absent covers both "no
	// candidate found" and "found but it is really a .13 ELF" — the two cases
	// a CUDA 12.x build treats identically (dlopen fails either way). unknown
	// covers the cases the probe refuses to guess about: ldconfig itself
	// missing, or a candidate that could not be inspected (unreadable file,
	// ELF without a DT_SONAME, unparsable ELF).
	CUDA12Userland CUDA12Userland
}

// probeCommandTimeout bounds a single external accelerator-probe invocation.
// A healthy nvidia-smi or nvcc answers in tens of milliseconds; the cap exists
// so a wedged driver can never stall installation. It matches the budget the
// vector-index GPU probe uses (embedding.gpuProbeTimeout). On timeout the
// context kills the child and the probe reports failure — it never hangs.
const probeCommandTimeout = 2 * time.Second

// probeWaitDelay is how long a probe may keep its output pipes open after the
// process itself is gone. Killing the child is not enough to bound the read:
// a grandchild that inherited stdout — a driver helper, or a shell's own
// `sleep` — keeps the pipe's write end open, and the copy that feeds
// cmd.Output would block until IT exits, long after probeCommandTimeout fired.
// The delay makes the documented bound real: once it expires, os/exec closes
// the pipes and Wait returns (with exec.ErrWaitDelay), which every caller here
// already treats as "the probe did not answer".
const probeWaitDelay = 500 * time.Millisecond

// probeCommandAttempts and probeCommandRetryBackoff bound the one retry
// runProbeCommand allows: a bounded number of re-execs, spaced by a short
// backoff, all inside the single probeCommandTimeout budget. Four attempts
// (three retries) with a 20 ms gap is far more than the transient ETXTBSY
// window needs — the writer's descriptor is gone within microseconds of the
// write — while still keeping the worst case (~60 ms) negligible against the
// 2 s cap.
const (
	probeCommandAttempts     = 4
	probeCommandRetryBackoff = 20 * time.Millisecond
)

// gibibyte is the divisor turning a byte count into GiB. The fork's demo
// scripts divide by exactly this value, so the RAM tiers line up with them.
const gibibyte = 1 << 30

// errProbeToolAbsent marks a probe helper that is simply not installed. It is
// a normal outcome, never a failure: the detection ladder treats it as "keep
// looking" and the caller decides whether to log it.
var errProbeToolAbsent = errors.New("probe tool not found in PATH")

// ErrRAMUnknown is returned when total system RAM cannot be determined. The
// memory gate is a safety gate and its HOST budget is derived from this figure
// on every path — measured topology or not — so an unreadable RAM size refuses
// the install instead of assuming the machine is big enough. An unreadable
// DEVICE budget is the opposite case and is deliberately not fatal: see
// deviceUnreadable in plan.go.
var ErrRAMUnknown = errors.New("cannot determine total system RAM")

// cudaVersionRE matches the "CUDA Version: 12.4" field of the nvidia-smi
// header table. nvidia-smi prints "N/A" there when the driver reports no CUDA
// support, which simply does not match.
var cudaVersionRE = regexp.MustCompile(`CUDA Version:\s*(\d+)\.(\d+)`)

// nvccReleaseRE matches "Cuda compilation tools, release 12.4, V12.4.131".
var nvccReleaseRE = regexp.MustCompile(`release\s+(\d+)\.(\d+)`)

// ProbeHardware detects total RAM and the accelerator backend, in the fork's
// fixed precedence order:
//
//  1. nvidia-smi          -> CUDA driver version -> asset tag
//  2. nvcc --version      -> CUDA toolkit fallback when nvidia-smi is absent
//  3. rocminfo/rocm-smi/hipcc -> rocm
//  4. vulkaninfo          -> vulkan
//  5. darwin/arm64        -> metal
//  6. otherwise           -> cpu
//
// First hit wins. A detected CUDA version older than the oldest pinned asset
// tag is NOT a hit: the ladder keeps looking, because provisioning an archive
// the driver cannot load would fail later with a far less useful error.
//
// RAM is a hard requirement, not a best-effort signal: if it cannot be read,
// ProbeHardware returns ErrRAMUnknown rather than guessing, because the memory
// gate derives its host budget from it.
//
// logger may be nil, in which case a discard logger is used. ctx governs
// cancellation of every external probe; nil is treated as context.Background().
func ProbeHardware(ctx context.Context, logger *slog.Logger) (Hardware, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	ramGiB, err := probeRAMGiB(ctx, logger)
	if err != nil {
		return Hardware{}, err
	}

	backend, cudaTag := probeBackend(ctx, logger)

	return Hardware{
		Platform:       runtime.GOOS + "-" + runtime.GOARCH,
		Arch:           runtime.GOARCH,
		RAMGiB:         ramGiB,
		Backend:        backend,
		CUDATag:        cudaTag,
		CUDA12Userland: probeCUDA12Userland(ctx, logger),
	}, nil
}

// probeRAMGiB reads total system RAM and converts it to GiB.
func probeRAMGiB(ctx context.Context, logger *slog.Logger) (float64, error) {
	total, err := platformTotalRAMBytes(ctx)
	if err != nil {
		logger.Debug("embedded LLM RAM probe failed", "error", err)
		return 0, fmt.Errorf("%w: %w", ErrRAMUnknown, err)
	}
	if total == 0 {
		logger.Debug("embedded LLM RAM probe reported zero bytes")
		return 0, ErrRAMUnknown
	}
	return float64(total) / gibibyte, nil
}

// probeBackend walks the accelerator ladder and returns the first usable hit
// plus its CUDA asset tag (empty for every non-CUDA backend).
func probeBackend(ctx context.Context, logger *slog.Logger) (backend Backend, cudaTag string) {
	// Every rung of the ladder reports itself the same way, so a support bundle
	// always shows which detector fired.
	detected := func(b Backend, tag, via string) (Backend, string) {
		logger.Debug("embedded LLM backend probe", "backend", b, "cuda_tag", tag, "via", via)
		return b, tag
	}

	if cuda, tag, ok := probeCUDA(ctx, logger); ok {
		return detected(cuda, tag, "nvidia")
	}

	// ROCm/HIP: presence of any of the three userspace tools is enough, which
	// is exactly how the demo's bonsai_llama_ngl decides.
	for _, tool := range []string{"rocminfo", "rocm-smi", "hipcc"} {
		if commandAvailable(tool) {
			return detected(BackendROCm, "", tool)
		}
	}

	if commandAvailable("vulkaninfo") {
		return detected(BackendVulkan, "", "vulkaninfo")
	}

	// Apple Silicon. Intel Macs have no Metal compute path for this runtime,
	// so they deliberately fall through to the CPU build.
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return detected(BackendMetal, "", "darwin/arm64")
	}

	return detected(BackendCPU, "", "fallback")
}

// probeCUDA tries nvidia-smi first and nvcc second, mapping the reported CUDA
// version onto a pinned asset tag. It reports ok=false when neither tool is
// installed, neither answers, or the version predates every pinned CUDA asset —
// in which case the ladder keeps looking instead of provisioning a build the
// driver cannot load.
//
// nvcc is only spawned when nvidia-smi did not already answer, so a machine
// with the driver userspace pays for exactly one probe.
func probeCUDA(ctx context.Context, logger *slog.Logger) (Backend, string, bool) {
	smiOut, smiErr := runProbeCommand(ctx, "nvidia-smi")
	logProbeFailure(logger, "nvidia-smi", smiErr)

	if backend, tag, ok := backendFromCUDAOutput(smiOut, ""); ok {
		logger.Debug("embedded LLM CUDA probe", "backend", backend, "cuda_tag", tag, "via", "nvidia-smi")
		return backend, tag, true
	}

	// Toolkit fallback: a container or a toolkit-only install can have nvcc
	// without the driver userspace.
	nvccOut, nvccErr := runProbeCommand(ctx, "nvcc", "--version")
	logProbeFailure(logger, "nvcc", nvccErr)

	if backend, tag, ok := backendFromCUDAOutput("", nvccOut); ok {
		logger.Debug("embedded LLM CUDA probe", "backend", backend, "cuda_tag", tag, "via", "nvcc")
		return backend, tag, true
	}

	return "", "", false
}

// backendFromCUDAOutput is the pure half of the CUDA step: given the raw output
// of nvidia-smi and nvcc, it decides the backend and asset tag. nvidia-smi wins
// because the driver's supported version is what a binary actually has to run
// against; the toolkit version is only a fallback signal. An empty string for
// either source means "this probe did not answer".
func backendFromCUDAOutput(nvidiaSMI, nvcc string) (Backend, string, bool) {
	if major, minor, ok := parseNvidiaSMICUDAVersion(nvidiaSMI); ok {
		if backend, tag, ok := cudaBackendFor(major, minor); ok {
			return backend, tag, true
		}
	}
	if major, minor, ok := parseNvccRelease(nvcc); ok {
		if backend, tag, ok := cudaBackendFor(major, minor); ok {
			return backend, tag, true
		}
	}
	return "", "", false
}

// ─────────────────────────────────────────────────────────────────────────────
// CUDA 12 userland probe: would a CUDA 12.x build find its libraries?

// CUDA 12.x runtime archives are published for linux/amd64 only (see
// x64Only), which is the only platform this probe classifies. darwin loads no
// libcudart at all and Windows names its import libraries *.lib, so a verdict
// there would be fiction; the probe reports unknown rather than pretend.
var cuda12UserlandProbeSupported = runtime.GOOS == "linux" && runtime.GOARCH == "amd64"

// The libraries checked are libcudart.so.12 and libcublas.so.12 — the shared
// objects a CUDA 12.x build's launcher dynamically links and therefore must
// find at load time. The soname — not the path — is the ground truth: a
// "libcudart.so.12" path can be a symlink whose target is a 13-series ELF (a
// packaging accident that leaves ldconfig listing only ".so.13" entries while
// ".so.12" paths still resolve), and dlopen would then fail. Reading the
// soname settles what the loader would actually register.

// cuda12UserlandFallbackGlobs are fixed fallback locations checked in addition
// to ldconfig's cache. The cache can under-report exactly the files this probe
// exists to arbitrate: where a mis-packaged ".so.12" symlink points at a
// ".so.13" ELF, the cache carries only ".so.13" entries and the ".so.12"
// candidates would be missed entirely. The fallbacks merge with — never
// replace — ldconfig's answers. cuda*/ and the multiarch directory cover the
// common distro layouts; anything exotic is expected to be in ldconfig's
// cache anyway.
var cuda12UserlandFallbackGlobs = []string{
	"/usr/lib/x86_64-linux-gnu/libcudart.so.12*",
	"/usr/lib/x86_64-linux-gnu/libcublas.so.12*",
	"/usr/lib/libcudart.so.12*",
	"/usr/lib/libcublas.so.12*",
	"/usr/lib64/libcudart.so.12*",
	"/usr/lib64/libcublas.so.12*",
	"/usr/local/cuda*/lib64/libcudart.so.12*",
	"/usr/local/cuda*/lib64/libcublas.so.12*",
	"/opt/cuda*/lib64/libcudart.so.12*",
	"/opt/cuda*/lib64/libcublas.so.12*",
}

// cuda12LibVerdict is the per-library outcome of inspecting every candidate
// found for one shared object.
type cuda12LibVerdict int

const (
	// cuda12LibGenuine — at least one candidate carries the library's own
	// ".so.12" DT_SONAME: the loader would register it under the wanted name.
	cuda12LibGenuine cuda12LibVerdict = iota
	// cuda12LibMismatch — candidates exist but every inspectable one embeds a
	// DIFFERENT series' soname: the ".so.12"-named symlink pointing at a
	// ".so.13" ELF. dlopen under the ".so.12" name would fail.
	cuda12LibMismatch
	// cuda12LibMissing — no candidate found anywhere discovery looked.
	cuda12LibMissing
	// cuda12LibIndeterminate — candidates exist but none could be inspected
	// (unreadable, non-ELF, truncated, stripped of DT_SONAME): not present,
	// but not confidently absent either.
	cuda12LibIndeterminate
)

// String renders the verdict for probe logging.
func (v cuda12LibVerdict) String() string {
	switch v {
	case cuda12LibGenuine:
		return "genuine"
	case cuda12LibMismatch:
		return "mismatch"
	case cuda12LibMissing:
		return "missing"
	default:
		return "indeterminate"
	}
}

// probeCUDA12Userland classifies the CUDA 12.x userland as present / absent /
// unknown. It is the I/O half: ldconfig is spawned once, candidates are
// inspected on disk, and the verdict logic is the pure
// classifyCUDA12Userland. "unknown" is reserved for the cases the probe
// refuses to guess about: a platform the CUDA archives do not target,
// ldconfig itself missing, or candidates that could not be inspected.
func probeCUDA12Userland(ctx context.Context, logger *slog.Logger) CUDA12Userland {
	if !cuda12UserlandProbeSupported {
		return CUDA12Unknown
	}

	ldconfigOut, haveLdconfig := runLdconfigCache(ctx, logger)
	ldcache := parseLdconfigEntries(ldconfigOut)

	cudart := inspectCUDA12Lib("libcudart.so.12",
		cuda12UserlandCandidates("libcudart.so.12", ldcache, filepath.Glob))
	cublas := inspectCUDA12Lib("libcublas.so.12",
		cuda12UserlandCandidates("libcublas.so.12", ldcache, filepath.Glob))

	verdict := classifyCUDA12Userland(cudart, cublas, haveLdconfig)
	logger.Debug("embedded LLM CUDA 12 userland probe",
		"verdict", string(verdict),
		"cudart", cudart.String(),
		"cublas", cublas.String(),
		"ldconfig", haveLdconfig)
	return verdict
}

// inspectCUDA12Lib maps one library's candidate paths onto a per-library
// verdict. Best evidence wins: one genuine ".so.12" ELF settles the library
// regardless of any mismatched siblings; otherwise a mismatch (the
// symlink-to-13 trap) beats an indeterminate inspection, which in turn beats
// having found nothing.
func inspectCUDA12Lib(lib string, candidates []string) cuda12LibVerdict {
	if len(candidates) == 0 {
		return cuda12LibMissing
	}
	verdict := cuda12LibIndeterminate
	for _, path := range candidates {
		soname, ok := elfSONAME(path)
		if !ok {
			continue
		}
		if soname == lib {
			return cuda12LibGenuine
		}
		verdict = cuda12LibMismatch
	}
	return verdict
}

// classifyCUDA12Userland is the pure verdict over the two per-library results.
//
//	any library indeterminate            -> unknown (never guess)
//	any library mismatched               -> absent  (the trap: dlopen would fail)
//	both genuine                         -> present
//	both missing, ldconfig answered      -> absent  (discovery was complete)
//	both missing, ldconfig absent        -> unknown (discovery was not)
//
// A genuine finding does not depend on ldconfig: the fixed fallback globs are
// checked on every run, so present stands even where the cache is unreadable.
func classifyCUDA12Userland(cudart, cublas cuda12LibVerdict, haveLdconfig bool) CUDA12Userland {
	if cudart == cuda12LibIndeterminate || cublas == cuda12LibIndeterminate {
		return CUDA12Unknown
	}
	if cudart == cuda12LibMismatch || cublas == cuda12LibMismatch {
		return CUDA12Absent
	}
	if cudart == cuda12LibGenuine && cublas == cuda12LibGenuine {
		return CUDA12Present
	}
	if haveLdconfig {
		return CUDA12Absent
	}
	return CUDA12Unknown
}

// runLdconfigCache runs `ldconfig -p` and returns its raw stdout. The bool is
// false when ldconfig itself is missing or failed — the machine may still have
// the libraries, so this downgrades a later "no candidate found" to unknown
// rather than absent. It reuses the shared probe budget: a wedged ldconfig is
// exactly the "driver tool that cannot answer" case probeCommandTimeout
// exists for.
func runLdconfigCache(ctx context.Context, logger *slog.Logger) (string, bool) {
	out, err := runProbeCommand(ctx, "ldconfig", "-p")
	if err != nil {
		logProbeFailure(logger, "ldconfig", err)
		return "", false
	}
	return out, true
}

// parseLdconfigEntries is the pure half of cache discovery: `ldconfig -p`
// lines of the shape
//
//	libcudart.so.13 (libc6,x86-64) => /opt/cuda/lib64/libcudart.so.13
//
// are folded into soname → paths, first-seen order preserved. A malformed or
// unrelated line (the "N caches found" prologue, locales) is skipped rather
// than guessed at; the same soname may legitimately appear several times.
func parseLdconfigEntries(out string) map[string][]string {
	entries := map[string][]string{}
	for line := range strings.SplitSeq(out, "\n") {
		_, path, found := strings.Cut(line, " => ")
		if !found || path == "" {
			continue
		}
		// A CR from a CRLF-emitting ldconfig is output noise, not part of
		// the path: a "\r"-suffixed path would fail to open and downgrade a
		// working CUDA 12 userland to an unsupported unknown.
		path = strings.TrimRight(path, "\r")
		name, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		// A cache entry is "<soname> (<cache-brief>) => <path>"; the
		// parenthesized brief is what distinguishes it from prose that merely
		// contains an arrow.
		if name == "" || !strings.HasPrefix(rest, "(") {
			continue
		}
		entries[name] = append(entries[name], path)
	}
	return entries
}

// cuda12UserlandCandidates merges the discovery sources for one library into
// a de-duplicated candidate list: ldconfig's cache first (the loader's own
// view, and first-seen there wins at load time), then the fixed fallback
// globs. globFn is filepath.Glob in production; tests inject a resolver over
// fixture directories instead. A failing glob is skipped — discovery is
// best-effort across independent sources, and one bad pattern must not
// discard the others.
func cuda12UserlandCandidates(lib string, ldcache map[string][]string, globFn func(string) ([]string, error)) []string {
	seen := map[string]bool{}
	var candidates []string
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			candidates = append(candidates, path)
		}
	}
	for _, path := range ldcache[lib] {
		add(path)
	}
	for _, pattern := range cuda12UserlandFallbackGlobs {
		matches, err := globFn(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			add(path)
		}
	}
	return candidates
}

// elfSONAME reads the DT_SONAME of the shared object at path. ok is false for
// every case the verdict must not guess about: an unreadable file, a non-ELF,
// a truncated or otherwise unparsable object, and a stripped object with no
// DT_SONAME at all. The library is parsed through the open descriptor
// (os.File is an io.ReaderAt), so the kernel faults in only the ELF header,
// section table and .dynamic/.dynstr pages instead of reading a potentially
// very large library into memory.
func elfSONAME(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	ef, err := elf.NewFile(f)
	if err != nil {
		return "", false
	}
	defer func() { _ = ef.Close() }()

	names, err := ef.DynString(elf.DT_SONAME)
	if err != nil || len(names) == 0 {
		return "", false
	}
	return names[0], true
}

// logProbeFailure records a probe that was installed but could not answer. An
// absent tool is the normal case on a machine without that accelerator and is
// deliberately not logged: it would drown the useful diagnostics.
func logProbeFailure(logger *slog.Logger, tool string, err error) {
	if err != nil && !errors.Is(err, errProbeToolAbsent) {
		logger.Debug("embedded LLM accelerator probe failed", "tool", tool, "error", err)
	}
}

// cudaBackendFor maps a detected CUDA version onto the pinned asset tag:
//
//	>= 13.3          -> 13.3
//	13.0-13.2        -> 12.8   (newest 12.x asset; a 13.x driver loads it)
//	12.8-12.x        -> 12.8
//	12.0-12.7        -> 12.4
//	anything older   -> no hit (the ladder keeps looking)
//
// The tag is what the artifact registry keys its runtime archives by, and what
// Hardware.CUDATag reports.
func cudaBackendFor(major, minor int) (Backend, string, bool) {
	switch {
	case major > 13:
		return BackendCUDA133, "13.3", true
	case major == 13:
		if minor >= 3 {
			return BackendCUDA133, "13.3", true
		}
		return BackendCUDA128, "12.8", true
	case major == 12:
		if minor >= 8 {
			return BackendCUDA128, "12.8", true
		}
		return BackendCUDA124, "12.4", true
	default:
		return "", "", false
	}
}

// parseNvidiaSMICUDAVersion extracts the maximum CUDA version the installed
// driver supports from plain nvidia-smi output ("CUDA Version: 12.4"). The
// parser is deliberately forgiving: drivers substitute "N/A" or
// "[Not Supported]" in several states, and Windows emits \r\n line endings.
func parseNvidiaSMICUDAVersion(out string) (major, minor int, ok bool) {
	return parseVersionPair(cudaVersionRE, out)
}

// parseNvccRelease extracts the toolkit version from "nvcc --version" output
// ("Cuda compilation tools, release 12.4, V12.4.131").
func parseNvccRelease(out string) (major, minor int, ok bool) {
	return parseVersionPair(nvccReleaseRE, out)
}

// parseVersionPair applies re (which must carry two capture groups) and
// converts them to integers.
func parseVersionPair(re *regexp.Regexp, out string) (major, minor int, ok bool) {
	m := re.FindStringSubmatch(out)
	if len(m) != 3 {
		return 0, 0, false
	}
	parsedMajor, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, false
	}
	parsedMinor, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, false
	}
	return parsedMajor, parsedMinor, true
}

// parseMeminfoMemTotal extracts MemTotal from Linux /proc/meminfo content and
// returns it in BYTES. The kernel reports kilobytes, e.g.
// "MemTotal:       32768000 kB". A missing field, an unparsable number or an
// unexpected unit is reported as not-ok rather than guessed.
func parseMeminfoMemTotal(content string) (uint64, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != "MemTotal" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		if len(fields) > 1 && !strings.EqualFold(fields[1], "kB") {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// parseDecimalBytes parses a bare byte count such as the output of
// "sysctl -n hw.memsize", tolerating surrounding whitespace and CRLF.
func parseDecimalBytes(out string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// commandAvailable reports whether a probe helper is installed in PATH. It is
// the Go equivalent of the shell's "command -v", which is what the demo
// scripts use for the ROCm and Vulkan steps.
func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runProbeCommand runs an external probe and returns its stdout. An absent
// tool yields errProbeToolAbsent, which callers treat as "keep looking".
//
// A probe may exec a freshly written artefact — the fake runtime the tests
// stage, or a system tool a package manager is mid-replace — and the kernel
// answers execve with ETXTBSY ("text file busy") for as long as ANY process
// still holds that file open for writing. That window belongs to the writer,
// not to this probe, and it is transient by construction: it closes the
// instant the writer's descriptor goes away. Every caller here is fail-soft,
// so letting an ETXTBSY through would misreport a momentary kernel hiccup as
// the definitive "this tool did not answer" — for ProbeDevices that reads as a
// machine with no accelerator at all. The spawn, and only the spawn, is
// therefore retried on that one transient errno, inside the same
// probeCommandTimeout budget. Nothing that is not ETXTBSY is retried, and
// ETXTBSY is raised before the child ever runs, so a retry can never repeat a
// child's side effects.
func runProbeCommand(ctx context.Context, name string, args ...string) (string, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, errProbeToolAbsent)
	}

	ctx, cancel := context.WithTimeout(ctx, probeCommandTimeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		out, err := runProbeOnce(ctx, bin, args)
		if err == nil {
			return out, nil
		}
		if attempt >= probeCommandAttempts || !isTransientExecError(err) {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if !waitForRetry(ctx, probeCommandRetryBackoff) {
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}
}

// runProbeOnce performs one bounded, console-suppressed spawn. A failure here
// may be the kernel's pre-exec ETXTBSY refusal (the child never ran) or a real
// error from the child, and the caller distinguishes the two; nothing is
// retried that is not the former.
func runProbeOnce(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	// Bound the read as well as the process: see probeWaitDelay.
	cmd.WaitDelay = probeWaitDelay
	// Suppress the console window a GUI-subsystem host would otherwise
	// allocate for the child probe process (CREATE_NO_WINDOW on Windows).
	sysproc.HideConsole(cmd)

	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// isTransientExecError reports whether err is the kernel refusing an execve
// because the target is momentarily held open for writing (ETXTBSY). It is the
// ONLY condition runProbeCommand retries: it is provably transient, and it is
// raised before the child runs, so re-execing cannot duplicate anything. A
// missing, non-executable or denied binary is a real answer and is returned
// unchanged. On Windows ETXTBSY is never produced by a spawn, so this is
// effectively always false there — which is exactly the intended behaviour.
func isTransientExecError(err error) bool {
	return errors.Is(err, syscall.ETXTBSY)
}

// waitForRetry sleeps for d, reporting false if ctx is done first so a retry
// never outlives the probeCommandTimeout budget.
func waitForRetry(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
