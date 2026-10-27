// Device-memory topology: the SECOND, post-install hardware probe. hardware.go
// answers "which archive do I download" (no VRAM field — the runtime is not on
// disk yet); this file asks the installed runtime itself (`--list-devices`)
// what accelerator memory it can see, detects whether that pool aliases host
// RAM, and derives the two budgets a fit decision may spend.
//
// The one rule this file exists for: a unified pool is NEVER summed with host
// RAM (a naive sum reports 235 GiB on the 128 GiB reference machine), and
// unification is detected per device, never inferred from GOOS, falling back
// to unified — the conservative direction. I/O and decisions are split so the
// platform × device matrix is table-testable without a runtime present.
// See specs/domains/embedded-llm.md (Device memory topology probe).
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

// listDevicesArg is a probe, not a server launch: it needs no model, prints
// the inventory to stdout and exits 0.
const listDevicesArg = "--list-devices"

// deviceNameCPU is the host-memory pseudo-device only the richer `-lv 4`
// parameter dump lists — the cross-check input, never an accelerator.
const deviceNameCPU = "CPU"

// deviceListHeader is what makes an EMPTY inventory a real answer rather than
// an unrecognized probe: a CPU-only build can print it and nothing else.
const deviceListHeader = "Available devices:"

// deviceNoneLine matches the runtime's explicit "no devices" entry.
var deviceNoneLine = regexp.MustCompile(`^\s{2}\(none\)\s*$`)

// deviceListLine matches one `--list-devices` stdout entry; the strict line
// anchoring is what keeps log noise from parsing as a device.
var deviceListLine = regexp.MustCompile(`^\s{2}(\S+): (.*) \((\d+) MiB, (\d+) MiB free\)$`)

// deviceParamLine matches the same inventory in the `-lv 4` stderr parameter
// dump the supervisor captures into its output tail; that form additionally
// lists the CPU pseudo-device, which is why the parser keeps it separate.
var deviceParamLine = regexp.MustCompile(`:\s+-\s+(\S+)\s*: (.*) \((\d+) MiB, (\d+) MiB free\)\s*$`)

// Budget policy: two floors under the measurement, never a guess at the
// model's own footprint (memory.go's job). Derivation and tenants:
// specs/domains/embedded-llm.md (Device memory topology probe).
const (
	// hostReserveFloorMiB is RAM never handed to the runtime — OS + window
	// server, c0wrk itself, the vector index; also exposed as
	// DefaultHostReserveGiB so a gate with no topology holds back the same.
	hostReserveFloorMiB = 4 * 1024

	// hostReserveRatio scales the reserve with RAM: a bigger machine runs a
	// bigger everything-else.
	hostReserveRatio = 0.125

	// deviceMarginMiB is accelerator-side slack (runtime bookkeeping,
	// fragmentation, alignment); it deliberately excludes the vision
	// projector's reserve, which the footprint projection already adds
	// (ModelMemoryProfile.MMProjReserveDeviceMiB) — folding it in here would
	// count the projector twice.
	deviceMarginMiB = 1536
)

// DeviceMemory is one accelerator as the pinned runtime reports it: name,
// human description, and total/free memory in whole MiB. Budgets derive from
// `Total`, never `Free` — a capacity plan must be reproducible; a consumer
// wanting a live check reads `FreeMiB` explicitly.
type DeviceMemory struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TotalMiB    int64  `json:"total_mib"`
	FreeMiB     int64  `json:"free_mib"`
}

// MemoryTopology is the probe result: the accelerator inventory, whether their
// memory aliases host RAM, and the two budgets a fit decision may spend. It is
// JSON-serializable so an install can record it next to the Manifest. The zero
// value means "unknown", never "no memory": check the ok half of ProbeDevices.
type MemoryTopology struct {
	// Devices is the deduplicated inventory in print order; entries that
	// report no memory of their own are dropped. Empty means no accelerator
	// this build can use.
	Devices []DeviceMemory `json:"devices"`

	// HostRAMGiB is total system RAM in GiB, from the same probe
	// Hardware.RAMGiB carries, so the two can never disagree about the host.
	HostRAMGiB float64 `json:"host_ram_gib"`

	// Unified reports whether the device pool and host RAM are the SAME
	// memory; it decides whether the two budgets may be spent independently.
	Unified bool `json:"unified"`

	// DeviceBudgetBytes is the accelerator memory a launch may plan on, after
	// the margin and — on a unified machine — after clamping to what the host
	// pool can back (so the pair can never describe one pool twice).
	DeviceBudgetBytes int64 `json:"device_budget_bytes"`

	// HostBudgetBytes is the system RAM a launch may plan on after the OS
	// reserve; not reduced by a device footprint even on a unified machine.
	HostBudgetBytes int64 `json:"host_budget_bytes"`

	// ProbedAt stamps the snapshot in RFC 3339 UTC, matching the manifest's
	// InstalledAt.
	ProbedAt string `json:"probed_at"`
}

// DevicePoolMiB returns the accelerator memory the budget was derived from, in
// MiB — the largest single device on a unified machine (one physical pool),
// the sum over devices otherwise (independent VRAM the runtime splits across).
func (t MemoryTopology) DevicePoolMiB() int64 {
	return devicePoolMiB(t.Devices, t.Unified)
}

// DeviceBudgetMiB is DeviceBudgetBytes in memory.go's unit, floored (the
// conservative direction).
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
	// dropped counts memoryless entries — a diagnostic, not a budget input:
	// a machine whose only accelerator failed to report a size lands here
	// instead of silently looking CPU-only.
	dropped int
}

// ProbeDevices asks an installed llama-server what accelerator memory it can
// see and turns the answer into a MemoryTopology. binaryPath is handed to the
// same hardened spawn the accelerator probe uses (no path validation here;
// production passes ServerBinaryPath, the only path the layout vouches for).
// logger may be nil (a discard logger is used); ctx governs cancellation.
//
// FAIL-SOFT in every direction: a missing/hung/failing binary, unreadable host
// RAM or unrecognized output all yield (zero, false) and a Debug log — a
// topology probe refines a load decision and must never be the reason a load
// fails (the caller degrades to deviceUnreadable in plan.go).
// See specs/domains/embedded-llm.md (Device memory topology probe).
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

	// Host RAM is a hard input: a topology with a zeroed HostRAMGiB would look
	// like a machine with no RAM rather than an unanswered probe.
	hostBytes, err := platformTotalRAMBytes(ctx)
	if err != nil || hostBytes == 0 {
		logger.Debug("embedded LLM device probe skipped", "reason", "host RAM unreadable", "error", err)
		return MemoryTopology{}, false
	}

	out, err := runProbeCommand(ctx, binaryPath, listDevicesArg)
	if err != nil {
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
// output into an inventory, accepting both spellings (the `--list-devices`
// stdout form and the `-lv 4` parameter-dump form; mutually exclusive per
// line). ok is false only when NOTHING is recognizable — an empty inventory is
// a real answer, unrecognized output is a probe that did not answer, and
// conflating them would turn a broken binary into a "no GPU" verdict.
// Normalizations (memoryless entries dropped and counted, duplicate names
// collapsed to the first) and their measured rationale:
// specs/domains/embedded-llm.md (Device memory topology probe).
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

// add appends a device unless it reports no memory of its own (counted, so the
// probe can say so), or its name was already seen.
func (l *deviceListing) add(device DeviceMemory, seen map[string]struct{}) {
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
// richer inventory form, when there is one: the two read the same physical
// memory through different paths, so a large disagreement (typically a
// container limit the OS call does not see) is worth writing into a support
// bundle. Debug-only; never changes the result.
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
	// 2% covers whole-MiB rounding and the few MiB a kernel keeps for itself.
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
		// ONE pool seen from two sides: this min() is the whole point of the
		// classification.
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

// devicePoolMiB aggregates the inventory into one pool size, in MiB — unified:
// the LARGEST device (a second entry is another view of the same bytes, and
// summing aliases would double the machine's memory); discrete: the SUM
// (independent pools the runtime tensor-splits across). Both readings err
// towards the smaller number, the direction a capacity plan wants.
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
// the description a device reports. The discrete list is checked first, per
// device, so a hybrid machine (dGPU beside iGPU) is decided by its iGPU.
var (
	// discreteDeviceMarkers name accelerators that always own their memory:
	// NVIDIA parts, AMD's discrete Radeon RX/Pro/Instinct, and Intel's discrete
	// Arc cards (model numbers, unlike the integrated "Arc(TM) Graphics").
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
// same bytes. Six ordered first-hit rules, unsure-means-unified:
// specs/domains/embedded-llm.md (Device memory topology probe) carries the
// table — misclassifying a discrete card only clamps its budget, while
// misclassifying a unified pool invents memory.
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
