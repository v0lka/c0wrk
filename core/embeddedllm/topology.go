// Device-memory topology: the second I/O half of "how much memory does this
// machine have for inference?".
//
// hardware.go answers "which archive do I download": a platform key, total
// system RAM and an accelerator BACKEND NAME. It has no VRAM field, because
// nothing in the install plan needs one — `Resolve` sizes the context from RAM
// and picks the archive from the backend. Whether the pinned model FITS is a
// different question, and RAM alone cannot answer it: an accelerator has its
// own memory, and on a growing list of machines that memory IS system RAM.
//
// This file owns that second answer. `ProbeDevices` asks the runtime itself
// what it can see (`llama-server --list-devices`), which is the only source
// that agrees with the runtime's own allocation decisions — an OS-level VRAM
// query would describe the hardware, not what this build was compiled to use.
//
// THE ONE RULE THIS FILE EXISTS FOR: a unified pool must never be summed with
// host RAM. Measured on this project's reference machine (Apple M4 Max,
// 2026-09-25, pinned fork runtime, `--list-devices` verbatim):
//
//	Available devices:
//	  MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)
//	  BLAS: Accelerate (0 MiB, 0 MiB free)
//
// against 128 GiB (131072 MiB) of host RAM. The Metal device's 110100 MiB is
// NOT extra memory — it is the working set the OS carved OUT of that same RAM,
// so a naive "RAM + VRAM" sum reports 235 GiB on a 128 GiB machine, a 1.84x
// overcount that would wave through a launch the machine cannot serve.
//
// Unification is therefore DETECTED, never assumed from GOOS: unified pools
// exist well beyond macOS (AMD APUs / Strix Halo through GTT and stolen
// memory, Intel iGPUs the same way, Jetson/Grace-Hopper class CUDA SoCs), and
// a discrete GPU on a Mac would be the mirror-image mistake. When the evidence
// is inconclusive the classification falls back to UNIFIED, because that is the
// conservative direction: treating a discrete card as unified can only shrink
// its budget (through the min() below), while treating a unified pool as
// discrete overcounts capacity that does not exist.
//
// Like hardware.go, the I/O and the reasoning are split: `ProbeDevices` does
// the bounded spawn and nothing else, and every decision (parsing, deduping,
// classification, budget arithmetic) is a pure function over values, so the
// whole platform x device matrix is table-testable without a runtime present.
package embeddedllm

import (
	"context"
	"log/slog"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// listDevicesArg is the runtime flag that prints the accelerator inventory and
// exits. It is a probe, not a server launch: it needs no model, writes to
// stdout and returns 0, so it is safe to run against an install that has never
// been loaded. Measured on the pinned fork build: the three lines quoted in
// this file's header, on stdout, with nothing on stderr.
const listDevicesArg = "--list-devices"

// deviceNameCPU is the pseudo-device the runtime's own parameter dump lists
// for host memory. `--list-devices` deliberately skips it (measured: the
// Metal build prints MTL0 and BLAS only), so it can only appear in the richer
// `-lv 4` form — where it is a useful CROSS-CHECK on the host RAM probe
// (measured: `CPU : Apple M4 Max (131072 MiB, 131072 MiB free)` on a machine
// whose `sysctl hw.memsize` is exactly 128 GiB), never an accelerator.
const deviceNameCPU = "CPU"

// deviceListHeader is the line `--list-devices` prints before its entries. Its
// presence is what makes an EMPTY inventory distinguishable from a probe that
// produced garbage: a CPU-only build can print the header and then nothing at
// all, and that is a real answer, not a failure.
const deviceListHeader = "Available devices:"

// deviceNoneLine matches the runtime's explicit "no devices" entry, printed as
// two spaces plus "(none)".
var deviceNoneLine = regexp.MustCompile(`^\s{2}\(none\)\s*$`)

// deviceListLine matches one `--list-devices` entry. It is the runtime's own
// format string, read out of the pinned build's `libllama-common`
// (`  %s: %s (%zu MiB, %zu MiB free)` — the two leading spaces are part of the
// format, and both sizes are integer MiB) and confirmed against the captured
// output above. The match is strictly line-anchored and applied per line, so
// nothing that is not exactly an entry can slip through: a log line, a warning
// or a stack trace simply does not match and is ignored.
var deviceListLine = regexp.MustCompile(`^\s{2}(\S+): (.*) \((\d+) MiB, (\d+) MiB free\)$`)

// deviceParamLine matches the same inventory in the runtime's richer parameter
// dump, which `-lv 4` prints to STDERR during a real launch and which the
// supervisor already captures into its bounded output tail (`Server.pumpOutput`
// feeds both streams into `lineTail`). Format string, read out of the pinned
// build: `cmn  %12.*s:   - %-8s: %s (%zu MiB, %zu MiB free)`, rendered as
//
//	0.00.030.532 I cmn  common_param:   - MTL0    : Apple M4 Max (110100 MiB, 110100 MiB free)
//
// so the device name is left-aligned in a fixed width of 8 and the whole line
// carries a timestamp/level prefix. This form additionally lists the `CPU`
// pseudo-device, which is why the parser keeps it separate.
var deviceParamLine = regexp.MustCompile(`:\s+-\s+(\S+)\s*: (.*) \((\d+) MiB, (\d+) MiB free\)\s*$`)

// Budget policy. Two deductions turn a raw capacity into a number a launch can
// be planned against; both are floors under the measurement, never a guess at
// the model's own footprint (that is memory.go's job).
const (
	// hostReserveFloorMiB is the RAM never handed to the runtime, whatever the
	// machine's size. It has three tenants: the OS and the window server, c0wrk
	// itself (the Wails webview, the Go heap, the PTYs, the session store), and
	// the VECTOR INDEX — the ONNX Runtime session, the embedding model and a
	// large workspace's in-memory index, all host-resident whether or not the
	// embedded LLM is. 4 GiB leaves a 16 GiB machine a 12 GiB budget, which is
	// still comfortably above the ~600 MiB of host memory the pinned model holds
	// at full offload (memory.go's `ProjectHostMiB`). The same derivation is
	// exposed as `DefaultHostReserveGiB` so a gate with no topology holds back
	// exactly what a gate with one does.
	hostReserveFloorMiB = 4 * 1024

	// hostReserveRatio scales the reserve with RAM above the floor: a bigger
	// machine runs a bigger everything-else. 1/8 of a 128 GiB machine is a
	// 16 GiB reserve, which sits just above the ~20.5 GiB the OS itself kept
	// back from the Metal working set (131072 - 110100 MiB) — close enough
	// that the device term of the min() below, not this one, is what binds on
	// Apple Silicon.
	hostReserveRatio = 0.125

	// deviceMarginMiB is the accelerator-side slack on top of a projected
	// footprint: the runtime's own bookkeeping, allocator fragmentation and
	// alignment. The fork's demo documents "~1.2 GiB overhead" for the pinned
	// family (`Bonsai-demo/README.md`'s peak-memory table); 1.5 GiB is that
	// figure rounded up to the next half-GiB, because it is a whole-family
	// approximation rather than a measurement of this build.
	//
	// It deliberately does NOT include the vision projector's 849 MiB device
	// reserve: a topology is model-agnostic, and the consumer that projects a
	// footprint already adds `ModelMemoryProfile.MMProjReserveDeviceMiB`.
	// Folding it in here would count the projector twice.
	deviceMarginMiB = 1536
)

// DeviceMemory is one accelerator as the pinned runtime reports it: a name
// ("MTL0", "CUDA0", "Vulkan0", "HIP0"), a human description ("Apple M4 Max",
// "NVIDIA GeForce RTX 4090") and the device's total and currently-free memory
// in whole MiB.
//
// `Free` is a snapshot of the instant the probe ran and is informational: on
// this project's reference machine it equals `Total` because nothing else was
// using the GPU. The budget below is derived from `Total`, because a capacity
// plan that changed with whatever else happened to be running would not be
// reproducible — a consumer that wants a live check reads `Free` explicitly.
type DeviceMemory struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TotalMiB    int64  `json:"total_mib"`
	FreeMiB     int64  `json:"free_mib"`
}

// MemoryTopology is the probe result: every accelerator the runtime can see,
// whether their memory aliases host RAM, and the two budgets a fit decision is
// allowed to spend. It is JSON-serializable so an install can record it next to
// the `Manifest`, and so a support bundle can carry the machine's real shape.
//
// The zero value means "unknown", never "no memory": a caller must check the
// `ok` half of `ProbeDevices` rather than reading a zero budget as a refusal.
type MemoryTopology struct {
	// Devices is the accelerator inventory, in the order the runtime printed
	// it, without the entries that report no memory of their own and without
	// duplicates. It is empty (not nil-vs-empty significant) when the machine
	// has no accelerator this build can use.
	Devices []DeviceMemory `json:"devices"`

	// HostRAMGiB is total system RAM in GiB, from the same probe
	// `Hardware.RAMGiB` carries, so the two can never disagree about the host.
	HostRAMGiB float64 `json:"host_ram_gib"`

	// Unified reports whether the device pool and host RAM are the SAME
	// memory. It decides whether the two budgets may be spent independently
	// and it is the field a reader must trust over any GOOS intuition.
	Unified bool `json:"unified"`

	// DeviceBudgetBytes is the accelerator memory a launch may plan on, after
	// the margin — and, on a unified machine, after clamping to what the host
	// pool can actually back. Zero means "no accelerator memory".
	DeviceBudgetBytes int64 `json:"device_budget_bytes"`

	// HostBudgetBytes is the system RAM a launch may plan on, after the OS
	// reserve. It is NOT reduced by a device footprint on a unified machine:
	// the two are the same bytes, and the clamp lives in DeviceBudgetBytes so
	// the pair can never be added together to describe one pool twice.
	HostBudgetBytes int64 `json:"host_budget_bytes"`

	// ProbedAt stamps the inventory in RFC 3339 UTC, matching the manifest's
	// `InstalledAt`. A topology is a snapshot: free memory and even the device
	// list change when a driver or a build changes.
	ProbedAt string `json:"probed_at"`
}

// DevicePoolMiB returns the accelerator memory the budget was derived from, in
// MiB — the largest single device on a unified machine (one physical pool, so
// a second entry is an alias of it and adding them would double-count) and the
// sum over devices otherwise (independent VRAM pools the runtime tensor-splits
// across).
func (t MemoryTopology) DevicePoolMiB() int64 {
	return devicePoolMiB(t.Devices, t.Unified)
}

// DeviceBudgetMiB and HostBudgetMiB are the two budgets in memory.go's unit,
// floored. The projections a gate compares them against (`ProjectDeviceMiB`,
// `ProjectHostMiB`) are MiB, and flooring is the conservative direction.
func (t MemoryTopology) DeviceBudgetMiB() int64 {
	return t.DeviceBudgetBytes / bytesPerMiB
}

// HostBudgetMiB is HostBudgetBytes in MiB, floored.
func (t MemoryTopology) HostBudgetMiB() int64 {
	return t.HostBudgetBytes / bytesPerMiB
}

// deviceListing is the raw parse result: the accelerators, plus the CPU
// pseudo-device when the richer form carried one.
type deviceListing struct {
	// devices holds every accelerator entry that survived the no-memory drop
	// and the deduplication.
	devices []DeviceMemory
	// host is the `CPU` entry of the `-lv 4` parameter dump, and hostFound
	// says whether there was one. It never appears in devices: it is host RAM
	// wearing a device row.
	host      DeviceMemory
	hostFound bool
	// dropped counts the entries thrown away for reporting no memory of their
	// own. It is a diagnostic, not a budget input: a machine whose only
	// accelerator failed to report a size lands here instead of silently
	// looking CPU-only.
	dropped int
}

// ProbeDevices asks an installed llama-server what accelerator memory it can
// see and turns the answer into a MemoryTopology.
//
// binaryPath is the runtime executable — in production the value
// `ServerBinaryPath` located inside the install's runtime tree, which is the
// only path the layout's containment checks vouch for. This function performs
// no path validation of its own and adds none: it hands the path to the same
// hardened spawn the accelerator probe uses (`runProbeCommand`: `exec.LookPath`
// resolution, the `probeCommandTimeout` bound, `sysproc.HideConsole`).
//
// It is FAIL-SOFT in every direction: a missing binary, a hung one, a nonzero
// exit, unreadable host RAM or output the parser does not recognize all yield
// (zero, false) and a Debug log. A topology probe is an optional refinement of
// a load decision, so it must never be the reason a load fails — a caller that
// cannot get one treats the accelerator budget as UNREADABLE (deviceUnreadable
// in plan.go), which degrades the memory gate to the host pool rather than
// refusing the machine.
//
// logger may be nil, in which case a discard logger is used. ctx governs
// cancellation; nil is treated as context.Background().
func ProbeDevices(ctx context.Context, binaryPath string, logger *slog.Logger) (MemoryTopology, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	if binaryPath == "" {
		logger.Debug("embedded LLM device probe skipped", "reason", "no binary path")
		return MemoryTopology{}, false
	}

	// Host RAM is a hard input here too: without it neither budget can be
	// derived, and a topology with a zeroed HostRAMGiB would look like a
	// machine with no RAM rather than an unanswered probe.
	hostBytes, err := platformTotalRAMBytes(ctx)
	if err != nil || hostBytes == 0 {
		logger.Debug("embedded LLM device probe skipped", "reason", "host RAM unreadable", "error", err)
		return MemoryTopology{}, false
	}

	out, err := runProbeCommand(ctx, binaryPath, listDevicesArg)
	if err != nil {
		// An absent binary is the normal "not installed yet" case and is not
		// logged: the ladder treats it as "keep looking", exactly as the
		// accelerator probe does.
		logProbeFailure(logger, binaryPath, err)
		return MemoryTopology{}, false
	}

	listing, ok := parseDeviceListing(out)
	if !ok {
		logger.Debug("embedded LLM device probe produced no recognizable inventory",
			"binary", binaryPath, "output_lines", countLines(out))
		return MemoryTopology{}, false
	}

	if listing.dropped > 0 {
		// An entry that reports no memory of its own contributes no pool. On
		// the reference machine that is the measured `BLAS: Accelerate (0 MiB,
		// 0 MiB free)` row beside MTL0; on a machine whose only accelerator
		// failed to report a size it is the whole inventory, which is why the
		// count is written down instead of silently discarded.
		logger.Debug("embedded LLM device probe dropped memoryless entries",
			"dropped", listing.dropped, "kept", len(listing.devices))
	}

	topology := buildTopology(runtime.GOOS+"-"+runtime.GOARCH, int64(hostBytes), listing,
		time.Now().UTC().Format(time.RFC3339))

	crossCheckHostRAM(logger, topology, listing)

	logger.Debug("embedded LLM device memory topology",
		"platform", runtime.GOOS+"-"+runtime.GOARCH,
		"devices", len(topology.Devices),
		"unified", topology.Unified,
		"device_budget_mib", topology.DeviceBudgetMiB(),
		"host_budget_mib", topology.HostBudgetMiB(),
		"host_ram_gib", topology.HostRAMGiB)

	return topology, true
}

// parseDeviceListing is the pure half of the probe: it turns captured runtime
// output into an inventory. Both spellings of the inventory are accepted — the
// `--list-devices` stdout form and the `-lv 4` parameter-dump form — and they
// are mutually exclusive per line, since the latter always carries a
// timestamp/level prefix that the former's two-space anchor rejects.
//
// ok is false when the output is not recognizable AT ALL (no header, no
// `(none)`, no entry of either form). That distinction matters: an empty
// inventory is a real answer from a machine with no accelerator this build can
// use, while unrecognized output is a probe that did not answer, and reporting
// the second as the first would turn a broken binary into a "no GPU" verdict.
//
// Two normalizations are applied to whatever matched:
//
//   - Entries reporting no memory of their own (Total == 0 AND Free == 0) are
//     DROPPED, and how many were dropped stays on the listing so the probe can
//     log it. This is not cosmetic: the measured Metal output above carries
//     `BLAS: Accelerate (0 MiB, 0 MiB free)`, and the pinned fork says exactly
//     that at `common/fit.cpp:117` — "Some non-GPU accelerator backends, such
//     as BLAS, report 0/0 and rely on the host-memory fallback." The same code
//     KEEPS 0/0 for a GPU/IGPU-typed device and then refuses to place anything
//     on it ("device %s did not report memory; --fit will not use it"), which
//     is what a dropped entry amounts to here: a device with no reported pool
//     contributes nothing to a budget. Counting such a row as a device would
//     instead add a zero to a discrete sum, or win the unified max() on a
//     machine whose real accelerator was misparsed.
//   - Duplicate names are collapsed, keeping the FIRST. A log tail can contain
//     both spellings of the same inventory, and a duplicated pool would be
//     summed twice.
func parseDeviceListing(out string) (deviceListing, bool) {
	var listing deviceListing
	recognized := false
	seen := make(map[string]struct{}, 4)

	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSuffix(line, "\r")

		switch {
		case strings.TrimSpace(line) == deviceListHeader:
			recognized = true

		case deviceNoneLine.MatchString(line):
			// An explicit "no devices": recognized, and nothing to add.
			recognized = true

		case deviceListLine.MatchString(line):
			recognized = true
			if device, ok := deviceFromMatch(deviceListLine.FindStringSubmatch(line)); ok {
				listing.add(device, seen)
			}

		case deviceParamLine.MatchString(line):
			recognized = true
			m := deviceParamLine.FindStringSubmatch(line)
			device, ok := deviceFromMatch(m)
			if !ok {
				continue
			}
			// The parameter dump lists host memory as a device row. Keep it
			// out of the accelerator inventory and aside as the cross-check.
			if device.Name == deviceNameCPU {
				listing.host, listing.hostFound = device, true
				continue
			}
			listing.add(device, seen)
		}
	}

	return listing, recognized
}

// add appends a device unless it reports no memory of its own, or its name was
// already seen.
func (l *deviceListing) add(device DeviceMemory, seen map[string]struct{}) {
	// A 0/0 entry reports no pool of its own — see parseDeviceListing and the
	// fork's `common/fit.cpp:117`. It is counted so the probe can say so.
	if device.TotalMiB == 0 && device.FreeMiB == 0 {
		l.dropped++
		return
	}
	if _, dup := seen[device.Name]; dup {
		return
	}
	seen[device.Name] = struct{}{}
	l.devices = append(l.devices, device)
}

// deviceFromMatch converts a capture group set (name, description, total MiB,
// free MiB) into a DeviceMemory. An unparsable size — which the `\d+` anchors
// make reachable only through integer overflow — drops the entry rather than
// reporting a device with a garbage capacity.
func deviceFromMatch(m []string) (DeviceMemory, bool) {
	if len(m) != 5 {
		return DeviceMemory{}, false
	}
	total, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return DeviceMemory{}, false
	}
	free, err := strconv.ParseInt(m[4], 10, 64)
	if err != nil {
		return DeviceMemory{}, false
	}
	return DeviceMemory{
		Name:        m[1],
		Description: m[2],
		TotalMiB:    total,
		FreeMiB:     free,
	}, true
}

// countLines is the diagnostics helper: how much text the parser rejected.
func countLines(out string) int {
	if out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// crossCheckHostRAM compares the host RAM probe against the `CPU` row of the
// richer inventory form, when there is one. It is a Debug-only consistency
// check: the two read the same physical memory through different paths
// (`sysctl hw.memsize` / `/proc/meminfo` / `GlobalMemoryStatusEx` versus the
// runtime's own CPU device), so a large disagreement means one of them is
// lying — typically a container limit the OS call does not see. It never
// changes the result and never fails anything.
func crossCheckHostRAM(logger *slog.Logger, topology MemoryTopology, listing deviceListing) {
	if !listing.hostFound {
		return
	}
	reported := listing.host.TotalMiB * bytesPerMiB
	host := topology.HostRAMGiB * gibibyte
	diff := int64(host) - reported
	if diff < 0 {
		diff = -diff
	}
	// 2% covers the rounding the runtime applies (it prints whole MiB) and the
	// few MiB a kernel keeps for itself; anything beyond that is a real
	// disagreement worth writing into a support bundle.
	if tolerance := int64(host) / 50; diff > tolerance {
		logger.Debug("embedded LLM device probe: host RAM disagrees with the runtime's CPU device",
			"host_ram_gib", topology.HostRAMGiB,
			"runtime_cpu_device_mib", listing.host.TotalMiB)
	}
}

// buildTopology is the pure half of ProbeDevices: given a platform key, the
// host RAM in bytes and a parsed inventory, it classifies the memory model and
// derives the two budgets. No I/O, no clock — probedAt is a parameter so the
// whole matrix is table-testable.
func buildTopology(platform string, hostRAMBytes int64, listing deviceListing, probedAt string) MemoryTopology {
	devices := listing.devices
	if devices == nil {
		devices = []DeviceMemory{}
	}
	unified := classifyUnified(platform, devices)

	hostBudget := hostRAMBytes - hostReserveBytes(hostRAMBytes)
	if hostBudget < 0 {
		hostBudget = 0
	}

	// The device pool is what the runtime can put tensors in; the margin is
	// its own bookkeeping on top of a projected footprint.
	deviceBudget := devicePoolMiB(devices, unified)*bytesPerMiB - deviceMarginMiB*bytesPerMiB
	if unified {
		// ONE pool seen from two sides: the device budget can never exceed
		// what the host can back, or the pair would describe the same bytes
		// twice. This min() is the whole point of the classification.
		deviceBudget = min(deviceBudget, hostBudget)
	}
	if deviceBudget < 0 {
		deviceBudget = 0
	}

	return MemoryTopology{
		Devices:           devices,
		HostRAMGiB:        float64(hostRAMBytes) / gibibyte,
		Unified:           unified,
		DeviceBudgetBytes: deviceBudget,
		HostBudgetBytes:   hostBudget,
		ProbedAt:          probedAt,
	}
}

// hostReserveBytes is the RAM kept out of every budget: the larger of the
// fixed floor and hostReserveRatio of the machine's RAM.
func hostReserveBytes(hostRAMBytes int64) int64 {
	floor := int64(hostReserveFloorMiB) * bytesPerMiB
	return max(floor, int64(float64(hostRAMBytes)*hostReserveRatio))
}

// devicePoolMiB aggregates the inventory into one pool size, in MiB:
//
//   - unified  → the LARGEST device. A unified machine has one physical pool,
//     so any further entry is another view of the same bytes (the measured
//     `BLAS: Accelerate` row is exactly that, and it is dropped for reporting
//     zero — but an aliasing entry that reported the full pool would survive
//     the drop, and summing it would double the machine's memory).
//   - discrete → the SUM. Independent VRAM pools are additive: the runtime
//     tensor-splits the model across them.
//
// Both readings err towards the smaller number for their own case, which is
// the direction a capacity plan wants.
func devicePoolMiB(devices []DeviceMemory, unified bool) int64 {
	if len(devices) == 0 {
		return 0
	}
	if unified {
		largest := int64(0)
		for _, device := range devices {
			largest = max(largest, device.TotalMiB)
		}
		return largest
	}
	total := int64(0)
	for _, device := range devices {
		total += device.TotalMiB
	}
	return total
}

// Metal device names ("MTL0", "MTL1") and the CUDA/HIP inventory names
// ("CUDA0", "HIP0"). Matched as patterns rather than prefix comparisons so the
// file carries no ad-hoc string containment of its own.
var (
	metalDeviceName = regexp.MustCompile(`(?i)\bmtl\d+\b`)
	cudaDeviceName  = regexp.MustCompile(`^cuda\d+$`)
	hipDeviceName   = regexp.MustCompile(`^hip\d+$`)
)

// Accelerator identities, matched case-insensitively against BOTH the name and
// the description a device reports. The two lists are deliberately made of
// patterns that cannot both match one device: the discrete list is checked
// first, per device, so a hybrid machine (a dGPU beside an iGPU) is decided by
// the iGPU — see classifyUnified.
var (
	// discreteDeviceMarkers name accelerators that always own their memory:
	// every NVIDIA part (GeForce/Quadro/RTX/GTX/Tesla), AMD's discrete Radeon
	// RX / Radeon Pro / Instinct lines, and Intel's discrete Arc cards, whose
	// names carry a model number ("Arc(TM) A770 Graphics", "Arc(TM) B580")
	// where the integrated part does not ("Arc(TM) Graphics 140V").
	discreteDeviceMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bnvidia\b`),
		regexp.MustCompile(`(?i)\bgeforce\b`),
		regexp.MustCompile(`(?i)\brtx\b`),
		regexp.MustCompile(`(?i)\bgtx\b`),
		regexp.MustCompile(`(?i)\bquadro\b`),
		regexp.MustCompile(`(?i)\btesla\b`),
		regexp.MustCompile(`(?i)\binstinct\b`),
		regexp.MustCompile(`(?i)\bradeon(\(tm\))? (rx|pro)\b`),
		regexp.MustCompile(`(?i)\barc(\(tm\))? [ab]\d{3}\b`),
	}

	// unifiedDeviceMarkers name accelerators that borrow host RAM: Apple's
	// SoCs, AMD's APUs (whose iGPU reports as "Radeon(TM) Graphics" or
	// "Radeon 780M"/"890M" — Strix Halo included), Intel's integrated Iris /
	// UHD / Arc graphics, and the ARM and Qualcomm SoC GPUs.
	unifiedDeviceMarkers = []*regexp.Regexp{
		metalDeviceName,
		regexp.MustCompile(`(?i)\bapple m\d`),
		regexp.MustCompile(`(?i)\bryzen\b`),
		regexp.MustCompile(`(?i)\bstrix halo\b`),
		regexp.MustCompile(`(?i)\bradeon(\(tm\))? graphics\b`),
		regexp.MustCompile(`(?i)\bradeon \d{3}m\b`),
		regexp.MustCompile(`(?i)\biris\b`),
		regexp.MustCompile(`(?i)\buhd graphics\b`),
		regexp.MustCompile(`(?i)\barc(\(tm\))? graphics\b`),
		regexp.MustCompile(`(?i)\bintel\(r\) graphics\b`),
		regexp.MustCompile(`(?i)\badreno\b`),
		regexp.MustCompile(`(?i)\bmali\b`),
	}
)

// classifyUnified decides whether the accelerator memory and host RAM are the
// same bytes. The rules run in order and the first one that fires wins:
//
//  1. No devices → unified. There is nothing to sum, and unified is the
//     default everything else falls back to anyway.
//  2. darwin-arm64 → unified. Apple Silicon's Metal working set is carved out
//     of system RAM (measured: 110100 MiB against 131072 MiB). A discrete eGPU
//     attached to such a machine is misclassified here, which is the safe
//     direction: the min() in buildTopology can only shrink its budget.
//  3. A Metal-named device on any platform → unified (Metal exists only on
//     Apple's unified-memory parts).
//  4. A CUDA or HIP device on a non-amd64 platform → unified. That shape is a
//     Jetson / Grace-Hopper class SoC, where the GPU shares the system memory;
//     a discrete card is only ever provisioned on amd64 (see the registry's
//     x64-only rule and `effectiveBackend`).
//  5. Per device: a discrete marker → discrete candidate, otherwise an
//     integrated marker → unified candidate. Any integrated device makes the
//     machine unified, because that device's memory IS host RAM and a sum
//     would count it twice; a machine whose every device is definitely
//     discrete is discrete.
//  6. Otherwise → unified. Unsure means unified: misclassifying a discrete
//     card only clamps its budget to a host pool that is bigger than it, while
//     misclassifying a unified pool invents memory.
func classifyUnified(platform string, devices []DeviceMemory) bool {
	if len(devices) == 0 {
		return true
	}
	if platform == PlatformDarwinARM64 {
		return true
	}

	for _, device := range devices {
		if metalDeviceName.MatchString(device.Name) || metalDeviceName.MatchString(device.Description) {
			return true
		}
	}

	// A CUDA/HIP inventory on anything but amd64 is an SoC, not a card.
	if platformArch(platform) != "amd64" {
		for _, device := range devices {
			if cudaDeviceName.MatchString(strings.ToLower(device.Name)) ||
				hipDeviceName.MatchString(strings.ToLower(device.Name)) {
				return true
			}
		}
	}

	discrete, integrated := false, false
	for _, device := range devices {
		switch {
		case matchesAny(discreteDeviceMarkers, device):
			discrete = true
		case matchesAny(unifiedDeviceMarkers, device):
			integrated = true
		}
	}
	if integrated {
		return true
	}
	return !discrete
}

// matchesAny reports whether either of a device's identity strings matches one
// of the marker patterns.
func matchesAny(markers []*regexp.Regexp, device DeviceMemory) bool {
	return slices.ContainsFunc(markers, func(re *regexp.Regexp) bool {
		return re.MatchString(device.Name) || re.MatchString(device.Description)
	})
}
