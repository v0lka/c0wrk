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
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
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

// cudaVersionRE matches the CUDA-version field of the nvidia-smi header table.
// Two spellings exist and BOTH must be accepted: drivers up to the 5xx series
// print "CUDA Version: 12.4", while newer drivers (610.x) renamed the column to
// "CUDA UMD Version: 13.3" and no longer emit the legacy field at all. Matching
// only the legacy spelling is what silently dropped a CUDA-capable machine to
// the Vulkan rung of the detection ladder (and thus to the PTQ1_0 packing).
// nvidia-smi prints "N/A" or "[Not Supported]" there when the driver reports no
// CUDA support, which simply does not match.
var cudaVersionRE = regexp.MustCompile(`CUDA (?:UMD )?Version:\s*(\d+)\.(\d+)`)

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
		Platform: runtime.GOOS + "-" + runtime.GOARCH,
		Arch:     runtime.GOARCH,
		RAMGiB:   ramGiB,
		Backend:  backend,
		CUDATag:  cudaTag,
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
	if smiErr == nil && strings.TrimSpace(smiOut) != "" {
		logCUDAProbeMiss(logger, "nvidia-smi", smiOut)
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

// logProbeFailure records a probe that was installed but could not answer. An
// absent tool is the normal case on a machine without that accelerator and is
// deliberately not logged: it would drown the useful diagnostics.
func logProbeFailure(logger *slog.Logger, tool string, err error) {
	if err != nil && !errors.Is(err, errProbeToolAbsent) {
		logger.Debug("embedded LLM accelerator probe failed", "tool", tool, "error", err)
	}
}

// logCUDAProbeMiss records a probe that ran but yielded no usable CUDA version,
// together with a bounded excerpt of its output. The nvidia-smi header is the
// exact field this package parses, and a driver release can rename that column
// (610.x prints "CUDA UMD Version" where older drivers print "CUDA Version"),
// so without the raw header a future rename is indistinguishable from "this is
// not a CUDA machine" — which is precisely how a CUDA-capable host silently
// fell through to the Vulkan rung. Debug-only, and bounded by
// cudaProbeExcerptLines so a support bundle stays readable.
func logCUDAProbeMiss(logger *slog.Logger, tool, out string) {
	logger.Debug("embedded LLM CUDA probe found no usable version",
		"tool", tool, "output", excerptLines(out, cudaProbeExcerptLines))
}

// cudaProbeExcerptLines bounds how many leading lines of a probe's output are
// logged on a miss. The nvidia-smi version field sits in the first few lines of
// the header table, so a handful is ample.
const cudaProbeExcerptLines = 5

// excerptLines returns the first n lines of s, normalizing CRLF so the excerpt
// reads the same on every platform.
func excerptLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
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
