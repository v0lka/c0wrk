package embeddedllm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Captured fixtures
// ─────────────────────────────────────────────────────────────────────────────
//
// Every fixture below is output of the pinned fork runtime in its EXACT wire
// spelling, on an Apple M4 Max (Mac16,6) with 128 GiB of unified memory. The
// darwin/Metal fixtures are VERBATIM captures, re-captured from the pin this
// package actually ships: the `prism-b10735-842b188` macOS arm64 archive was
// downloaded from its registry URL on both 2026-09-25 and 2026-09-26, its
// SHA256 verified against `registry.go`, extracted, and `--version` reported
// "0.2.0-dev (build 10735, commit 842b18804)" before the inventory was read —
// the two captures are byte-identical.
//
// The non-darwin shapes are CONSTRUCTED, not captured, and say so beside the
// constant: this package's authors had no Linux or Windows host with a CUDA or
// Vulkan device to capture from (the 2026-09-26 attempt to execute the
// ubuntu-x64, ubuntu-vulkan-x64 and linux-cuda-12.8-x64 archives found no
// container runtime or VM on the capture machine; the three archives WERE
// downloaded and SHA256-verified against the pins, so the binaries these
// shapes describe are the ones that ship). The entry format itself is not a
// guess: it is `"<name>: <description> (<total> MiB, <free> MiB free)"`, the
// same format string the darwin captures exercise, and fixtureListDevicesNone
// was read out of the pinned build's libllama-common alongside it. The
// cross-OS device-line shapes below therefore test the PARSER's tolerance
// (names, descriptions, digit widths a bigger card would print), not a claim
// about any specific host — which the capture-date stamps in this file are
// here to keep distinguishable.
//
// The two spellings of the inventory are not interchangeable:
//
//   - `llama-server --list-devices` writes to STDOUT, exits 0 and leaves
//     stderr empty. It skips the `CPU` pseudo-device.
//   - `llama-server -lv 4 -m <weights> …` writes the parameter dump to STDERR
//     before the load, and it DOES list `CPU` (host RAM wearing a device row).
//     The supervisor already captures that stream (`Server.pumpOutput` feeds
//     both streams into its bounded `lineTail`), which is where this spelling
//     reaches c0wrk in production.
//
// `-lv 4 --list-devices` is NOT a way to get the richer form: the list-devices
// path exits before the parameter dump, so it prints exactly the stdout
// spelling and nothing else (measured with `-v`, `-lv 4` and `--verbosity 4`).

// fixtureListDevicesDarwinMetal is `llama-server --list-devices`, stdout.
const fixtureListDevicesDarwinMetal = "Available devices:\n" +
	"  MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)\n" +
	"  BLAS: Accelerate (0 MiB, 0 MiB free)\n"

// fixtureListDevicesNone is the explicit "no devices" inventory. The literal
// was read out of the pinned build's libllama-common alongside the entry
// format string (CONSTRUCTED from source rather than captured from a host —
// the darwin builds enumerate MTL0+BLAS, so this spelling needs a build with
// no accelerator at all, which no capture host here was), and the parser
// treats it as an answered probe with an empty inventory rather than as a
// failure.
const fixtureListDevicesNone = "Available devices:\n" +
	"  (none)\n"

// fixtureParamDumpDarwinMetal is the `-lv 4` parameter dump, stderr, with the
// trailing `system_info` line kept to prove a neighbouring parameter is not
// mistaken for a device.
const fixtureParamDumpDarwinMetal = "0.00.033.341 I cmn  common_param: device_info:\n" +
	"0.00.033.344 I cmn  common_param:   - MTL0    : Apple M4 Max (110100 MiB, 110100 MiB free)\n" +
	"0.00.033.345 I cmn  common_param:   - BLAS    : Accelerate (0 MiB, 0 MiB free)\n" +
	"0.00.033.350 I cmn  common_param:   - CPU     : Apple M4 Max (131072 MiB, 131072 MiB free)\n" +
	"0.00.033.370 I cmn  common_param: system_info: n_threads = 12 (n_threads_batch = 12) / 16 | MTL : EMBED_LIBRARY = 1 |\n"

// fixtureListDevicesDiscreteNVIDIA is the shape a CUDA host reports: one
// entry per physical card, each with its own memory. CONSTRUCTED, not
// captured — see the provenance note above — from the format string the
// darwin captures exercise; the card names are chosen to also drive the
// GPU-family classifier (Ada and Ampere).
const fixtureListDevicesDiscreteNVIDIA = "Available devices:\n" +
	"  CUDA0: NVIDIA GeForce RTX 4090 (24564 MiB, 24310 MiB free)\n" +
	"  CUDA1: NVIDIA GeForce RTX 3090 (24576 MiB, 24576 MiB free)\n"

// fixtureListDevicesWindowsCUDA is the Windows spelling of the CUDA shape
// (a smaller card, so the digit widths differ from the discrete pair above).
// CONSTRUCTED, not captured — no Windows host was available; see the
// provenance note above.
const fixtureListDevicesWindowsCUDA = "Available devices:\n" +
	"  CUDA0: NVIDIA GeForce RTX 4070 Ti (12282 MiB, 11904 MiB free)\n"

// fixtureListDevicesVulkanHost is the Vulkan spelling: GPU names without the
// CUDA0-style prefix and an Intel Arc description, so the parser tolerates
// both the naming difference and the family the guards know about. CONSTRUCTED,
// not captured — no Vulkan device was available; see the provenance note above.
const fixtureListDevicesVulkanHost = "Available devices:\n" +
	"  GPU0: Intel Arc A770 Graphics (16256 MiB, 15872 MiB free)\n"

// hostRAM128GiB is this project's reference machine: 128 GiB, exactly what
// `sysctl hw.memsize` reports and exactly what the runtime's own `CPU` device
// row agrees with (131072 MiB).
const hostRAM128GiB = int64(128) * gibibyte

// metalDeviceTotalMiB is the measured Apple M4 Max Metal working set. It is
// 20972 MiB BELOW the 131072 MiB of host RAM — the OS carved it out of that
// RAM, which is the whole reason the two must never be added together.
const metalDeviceTotalMiB = int64(110100)

func mib(n int64) int64 { return n * bytesPerMiB }

func gib(n int64) int64 { return n * gibibyte }

// ─────────────────────────────────────────────────────────────────────────────
// The pure parser
// ─────────────────────────────────────────────────────────────────────────────

// TestParseDeviceListingCapturedFixtures is the parser's contract against the
// real inventories above.
func TestParseDeviceListingCapturedFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		out         string
		wantOK      bool
		wantDevices []DeviceMemory
		wantHost    *DeviceMemory // non-nil when the form lists the CPU device
	}{
		{
			name:   "darwin metal list-devices",
			out:    fixtureListDevicesDarwinMetal,
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name:        "MTL0",
				Description: "Apple M4 Max",
				TotalMiB:    metalDeviceTotalMiB,
				FreeMiB:     metalDeviceTotalMiB,
			}},
		},
		{
			name:        "explicit none",
			out:         fixtureListDevicesNone,
			wantOK:      true,
			wantDevices: []DeviceMemory{},
		},
		{
			name:        "header only, no entries",
			out:         "Available devices:\n",
			wantOK:      true,
			wantDevices: []DeviceMemory{},
		},
		{
			name:   "lv4 parameter dump with the CPU device",
			out:    fixtureParamDumpDarwinMetal,
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name:        "MTL0",
				Description: "Apple M4 Max",
				TotalMiB:    metalDeviceTotalMiB,
				FreeMiB:     metalDeviceTotalMiB,
			}},
			wantHost: &DeviceMemory{
				Name:        deviceNameCPU,
				Description: "Apple M4 Max",
				TotalMiB:    131072,
				FreeMiB:     131072,
			},
		},
		{
			name:   "two discrete cards",
			out:    fixtureListDevicesDiscreteNVIDIA,
			wantOK: true,
			wantDevices: []DeviceMemory{
				{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24564, FreeMiB: 24310},
				{Name: "CUDA1", Description: "NVIDIA GeForce RTX 3090", TotalMiB: 24576, FreeMiB: 24576},
			},
		},
		{
			name:   "windows cuda shape",
			out:    fixtureListDevicesWindowsCUDA,
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name: "CUDA0", Description: "NVIDIA GeForce RTX 4070 Ti",
				TotalMiB: 12282, FreeMiB: 11904,
			}},
		},
		{
			name:   "vulkan host shape",
			out:    fixtureListDevicesVulkanHost,
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name: "GPU0", Description: "Intel Arc A770 Graphics",
				TotalMiB: 16256, FreeMiB: 15872,
			}},
		},
		{
			name:   "crlf line endings",
			out:    strings.ReplaceAll(fixtureListDevicesDarwinMetal, "\n", "\r\n"),
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name:        "MTL0",
				Description: "Apple M4 Max",
				TotalMiB:    metalDeviceTotalMiB,
				FreeMiB:     metalDeviceTotalMiB,
			}},
		},
		{
			name:   "surrounding log noise is ignored",
			out:    "ggml_metal_init: allocated\n" + fixtureListDevicesDarwinMetal + "main: done\n",
			wantOK: true,
			wantDevices: []DeviceMemory{{
				Name:        "MTL0",
				Description: "Apple M4 Max",
				TotalMiB:    metalDeviceTotalMiB,
				FreeMiB:     metalDeviceTotalMiB,
			}},
		},
		{
			name:        "empty output is not an inventory",
			out:         "",
			wantOK:      false,
			wantDevices: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listing, ok := parseDeviceListing(tt.out)
			if ok != tt.wantOK {
				t.Fatalf("parseDeviceListing ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				if len(listing.devices) != 0 {
					t.Errorf("an unrecognized listing carried %d devices, want none", len(listing.devices))
				}
				return
			}

			if len(listing.devices) != len(tt.wantDevices) {
				t.Fatalf("devices = %+v (n=%d), want %+v (n=%d)",
					listing.devices, len(listing.devices), tt.wantDevices, len(tt.wantDevices))
			}
			for i, want := range tt.wantDevices {
				if listing.devices[i] != want {
					t.Errorf("devices[%d] = %+v, want %+v", i, listing.devices[i], want)
				}
			}

			if tt.wantHost == nil {
				if listing.hostFound {
					t.Errorf("host device = %+v, want none (this form does not list CPU)", listing.host)
				}
				return
			}
			if !listing.hostFound {
				t.Fatal("the CPU device was not captured from the parameter dump")
			}
			if listing.host != *tt.wantHost {
				t.Errorf("host device = %+v, want %+v", listing.host, *tt.wantHost)
			}
		})
	}
}

// TestParseDeviceListingDropsMemorylessDevices locks the 0/0 rule. It is not
// cosmetic: the measured Metal inventory carries `BLAS: Accelerate (0 MiB,
// 0 MiB free)`, and the runtime's own fit logic documents that such a non-GPU
// accelerator reports no memory and falls back to host memory. Counting it as
// a device would add a zero to a discrete sum — or win the unified max() on a
// machine whose real accelerator was misparsed.
func TestParseDeviceListingDropsMemorylessDevices(t *testing.T) {
	t.Parallel()

	out := "Available devices:\n" +
		"  BLAS: Accelerate (0 MiB, 0 MiB free)\n" +
		"  OPENCL0: Mystery (0 MiB, 0 MiB free)\n"
	listing, ok := parseDeviceListing(out)
	if !ok {
		t.Fatal("parseDeviceListing ok = false, want true: the header answered")
	}
	if len(listing.devices) != 0 {
		t.Errorf("devices = %+v, want every 0/0 entry dropped", listing.devices)
	}
	if listing.dropped != 2 {
		t.Errorf("dropped = %d, want 2: the drop must be observable, not silent", listing.dropped)
	}

	// A device that reports SOME memory is kept, including the asymmetric
	// shapes: only Total==0 AND Free==0 is the "no pool of its own" signal.
	out = "Available devices:\n" +
		"  BLAS: Accelerate (0 MiB, 0 MiB free)\n" +
		"  MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)\n" +
		"  Vulkan0: Fully Used (8192 MiB, 0 MiB free)\n"
	listing, ok = parseDeviceListing(out)
	if !ok {
		t.Fatal("parseDeviceListing ok = false, want true")
	}
	if len(listing.devices) != 2 {
		t.Fatalf("devices = %+v, want the two that report memory", listing.devices)
	}
	if listing.devices[0].Name != "MTL0" || listing.devices[1].Name != "Vulkan0" {
		t.Errorf("devices = %+v, want MTL0 then Vulkan0 in printed order", listing.devices)
	}
	if listing.dropped != 1 {
		t.Errorf("dropped = %d, want 1: only the BLAS row reports no memory", listing.dropped)
	}
}

// TestParseDeviceListingRejectsUnrecognizedOutput proves the anchors are real:
// near-miss lines must NOT become devices, and an output with nothing but
// near-misses must report ok=false rather than an empty inventory. An empty
// inventory is a verdict about the machine; an unrecognized one is a probe
// that did not answer.
func TestParseDeviceListingRejectsUnrecognizedOutput(t *testing.T) {
	t.Parallel()

	entry := "  MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)"
	tests := []struct {
		name string
		line string
	}{
		{"one space of indent", " MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)"},
		{"no indent", "MTL0: Apple M4 Max (110100 MiB, 110100 MiB free)"},
		{"missing free size", "  MTL0: Apple M4 Max (110100 MiB)"},
		{"missing sizes entirely", "  MTL0: Apple M4 Max"},
		{"trailing garbage after the entry", entry + " trailing"},
		{"sizes in GiB", "  MTL0: Apple M4 Max (107 GiB, 107 GiB free)"},
		{"negative size", "  MTL0: Apple M4 Max (-1 MiB, 0 MiB free)"},
		{"unrelated log line", "ggml_backend_metal_buffer_type_alloc: pool size 0"},
		{"error text", "error: invalid value for --list-devices"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listing, ok := parseDeviceListing(tt.line + "\n")
			if ok {
				t.Errorf("parseDeviceListing ok = true for %q, want false", tt.line)
			}
			if len(listing.devices) != 0 {
				t.Errorf("devices = %+v, want none", listing.devices)
			}
		})
	}

	// A near-miss line NEXT TO a real entry must not disturb the real one.
	listing, ok := parseDeviceListing(entry + "\n" + "MTL1: no indent (1 MiB, 1 MiB free)\n")
	if !ok {
		t.Fatal("parseDeviceListing ok = false, want true")
	}
	if len(listing.devices) != 1 || listing.devices[0].Name != "MTL0" {
		t.Errorf("devices = %+v, want only the well-formed MTL0 entry", listing.devices)
	}
}

// TestParseDeviceListingDedupesRepeatedInventory covers the input a real log
// tail produces: both spellings of the same inventory in one buffer. A
// duplicated pool would be summed twice on a discrete machine.
func TestParseDeviceListingDedupesRepeatedInventory(t *testing.T) {
	t.Parallel()

	listing, ok := parseDeviceListing(fixtureListDevicesDarwinMetal + fixtureParamDumpDarwinMetal)
	if !ok {
		t.Fatal("parseDeviceListing ok = false, want true")
	}
	if len(listing.devices) != 1 {
		t.Fatalf("devices = %+v, want MTL0 exactly once", listing.devices)
	}
	if listing.devices[0].Name != "MTL0" {
		t.Errorf("devices[0].Name = %q, want MTL0", listing.devices[0].Name)
	}
	if !listing.hostFound {
		t.Error("the CPU cross-check row was lost")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unified-memory classification
// ─────────────────────────────────────────────────────────────────────────────

// TestClassifyUnified is the detection table. Unification is a property of the
// machine, never of GOOS: the same linux-amd64 platform is unified with an APU
// and discrete with a Radeon RX.
func TestClassifyUnified(t *testing.T) {
	t.Parallel()

	metal := DeviceMemory{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: metalDeviceTotalMiB}
	tests := []struct {
		name     string
		platform string
		devices  []DeviceMemory
		want     bool
		why      string
	}{
		{
			name: "darwin arm64 metal is unified", platform: PlatformDarwinARM64,
			devices: []DeviceMemory{metal}, want: true,
			why: "Apple Silicon's Metal working set is carved out of system RAM",
		},
		{
			name: "darwin arm64 with no devices", platform: PlatformDarwinARM64,
			devices: nil, want: true,
			why: "no pool to sum, and unified is the default anyway",
		},
		{
			name: "intel mac with a metal-named device", platform: PlatformDarwinAMD64,
			devices: []DeviceMemory{metal}, want: true,
			why: "a Metal device only ever exists on Apple's unified parts",
		},
		{
			name: "linux amd64 dual nvidia", platform: PlatformLinuxAMD64,
			devices: []DeviceMemory{
				{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24564},
				{Name: "CUDA1", Description: "NVIDIA GeForce RTX 3090", TotalMiB: 24576},
			}, want: false,
			why: "two independent VRAM pools; tensor-splitting across them is additive",
		},
		{
			name: "windows amd64 radeon rx", platform: PlatformWindowsAMD64,
			devices: []DeviceMemory{{Name: "HIP0", Description: "AMD Radeon RX 7900 XTX", TotalMiB: 24576}},
			want:    false,
			why:     "Radeon RX is a discrete card even though HIP is also an APU path",
		},
		{
			name: "linux amd64 amd apu", platform: PlatformLinuxAMD64,
			devices: []DeviceMemory{{Name: "HIP0", Description: "AMD Radeon(TM) Graphics", TotalMiB: 126629}},
			want:    true,
			why:     "an APU's iGPU borrows host RAM through GTT/stolen memory",
		},
		{
			name: "linux amd64 strix halo style rdna igpu", platform: PlatformLinuxAMD64,
			devices: []DeviceMemory{{Name: "HIP0", Description: "AMD Radeon 890M", TotalMiB: 65536}},
			want:    true,
			why:     "an RDNA iGPU name (nnnM) is integrated; Strix Halo reports this shape",
		},
		{
			name: "windows amd64 intel iris xe", platform: PlatformWindowsAMD64,
			devices: []DeviceMemory{{Name: "Vulkan0", Description: "Intel(R) Iris(R) Xe Graphics", TotalMiB: 16384}},
			want:    true,
			why:     "Intel integrated graphics share host RAM",
		},
		{
			name: "windows amd64 intel arc integrated", platform: PlatformWindowsAMD64,
			devices: []DeviceMemory{{Name: "Vulkan0", Description: "Intel(R) Arc(TM) Graphics 140V", TotalMiB: 32768}},
			want:    true,
			why:     "Lunar Lake's integrated Arc reports 'Arc(TM) Graphics' with no model number",
		},
		{
			name: "windows amd64 intel arc discrete", platform: PlatformWindowsAMD64,
			devices: []DeviceMemory{{Name: "Vulkan0", Description: "Intel(R) Arc(TM) A770 Graphics", TotalMiB: 16384}},
			want:    false,
			why:     "a discrete Arc carries a model number, which the marker matches first",
		},
		{
			name: "linux arm64 jetson class cuda", platform: "linux-arm64",
			devices: []DeviceMemory{{Name: "CUDA0", Description: "NVIDIA Orin (nvgpu)", TotalMiB: 65536}},
			want:    true,
			why:     "CUDA on a non-amd64 platform is an SoC, not a card",
		},
		{
			name: "hybrid laptop dgpu plus igpu", platform: PlatformWindowsAMD64,
			devices: []DeviceMemory{
				{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4070 Laptop GPU", TotalMiB: 8188},
				{Name: "Vulkan1", Description: "Intel(R) Iris(R) Xe Graphics", TotalMiB: 16384},
			}, want: true,
			why: "one device aliases host RAM, so the pools cannot be added",
		},
		{
			name: "unknown accelerator is treated as unified", platform: PlatformLinuxAMD64,
			devices: []DeviceMemory{{Name: "GGML0", Description: "Mystery Accelerator 9000", TotalMiB: 32768}},
			want:    true,
			why:     "unsure means unified: it can only shrink a budget, never invent memory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := classifyUnified(tt.platform, tt.devices); got != tt.want {
				t.Errorf("classifyUnified(%q, %+v) = %v, want %v (%s)",
					tt.platform, tt.devices, got, tt.want, tt.why)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Budget derivation
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildTopologyUnifiedNeverSumsPools is the headline invariant, measured on
// the reference machine: 110100 MiB of "VRAM" on a 128 GiB host is ONE pool of
// 128 GiB, not 235 GiB of memory. A naive sum overcounts this machine by
// 1.84x and would wave through a launch it cannot serve.
func TestBuildTopologyUnifiedNeverSumsPools(t *testing.T) {
	t.Parallel()

	metal := DeviceMemory{Name: "MTL0", Description: "Apple M4 Max",
		TotalMiB: metalDeviceTotalMiB, FreeMiB: metalDeviceTotalMiB}
	listing, ok := parseDeviceListing(fixtureListDevicesDarwinMetal)
	if !ok {
		t.Fatal("the captured fixture did not parse")
	}

	topology := buildTopology(PlatformDarwinARM64, hostRAM128GiB, listing, "2026-09-25T10:00:00Z")

	if !topology.Unified {
		t.Error("Unified = false on darwin-arm64 Metal, want true")
	}
	if len(topology.Devices) != 1 || topology.Devices[0] != metal {
		t.Errorf("Devices = %+v, want the single MTL0 entry", topology.Devices)
	}
	if topology.HostRAMGiB != 128 {
		t.Errorf("HostRAMGiB = %v, want 128", topology.HostRAMGiB)
	}
	if topology.ProbedAt != "2026-09-25T10:00:00Z" {
		t.Errorf("ProbedAt = %q, want the stamped value", topology.ProbedAt)
	}

	// The device pool is the Metal working set alone, and the budget is that
	// minus the runtime margin — the host term (112 GiB) does not bind here.
	if got := topology.DevicePoolMiB(); got != metalDeviceTotalMiB {
		t.Errorf("DevicePoolMiB() = %d, want %d", got, metalDeviceTotalMiB)
	}
	if got, want := topology.DeviceBudgetMiB(), metalDeviceTotalMiB-deviceMarginMiB; got != want {
		t.Errorf("DeviceBudgetMiB() = %d, want %d (pool minus the margin)", got, want)
	}
	// 128 GiB minus the 1/8 scaled reserve = 112 GiB.
	if got, want := topology.HostBudgetMiB(), int64(112)*1024; got != want {
		t.Errorf("HostBudgetMiB() = %d, want %d", got, want)
	}

	// THE INVARIANT. A unified pool can never describe more memory than the
	// machine physically has, and the device budget can never exceed the host
	// budget it is clamped to. The naive sum this replaces is asserted too, so
	// a regression that reintroduces it fails loudly instead of silently
	// reporting 235 GiB.
	if topology.DeviceBudgetBytes > hostRAM128GiB {
		t.Errorf("DeviceBudgetBytes = %d exceeds the machine's %d bytes of RAM",
			topology.DeviceBudgetBytes, hostRAM128GiB)
	}
	if topology.DeviceBudgetBytes > topology.HostBudgetBytes {
		t.Errorf("DeviceBudgetBytes = %d exceeds the host budget %d on a unified machine",
			topology.DeviceBudgetBytes, topology.HostBudgetBytes)
	}
	naiveSum := hostRAM128GiB + mib(metalDeviceTotalMiB)
	if topology.DeviceBudgetBytes+topology.HostBudgetBytes >= naiveSum {
		t.Errorf("the budgets sum to %d bytes, at or above the naive RAM+VRAM sum of %d",
			topology.DeviceBudgetBytes+topology.HostBudgetBytes, naiveSum)
	}
	t.Logf("128 GiB host, MTL0 %d MiB -> unified, device budget %d MiB, host budget %d MiB (naive sum would claim %d MiB)",
		metalDeviceTotalMiB, topology.DeviceBudgetMiB(), topology.HostBudgetMiB(), naiveSum/bytesPerMiB)
}

// TestBuildTopologyUnifiedClampsToTheHostPool covers the direction the min()
// exists for: an accelerator that reports the WHOLE pool (an AMD APU with its
// UMA frame carved out of host RAM) must not get a budget the host cannot
// back, and two aliasing entries must not be added together.
func TestBuildTopologyUnifiedClampsToTheHostPool(t *testing.T) {
	t.Parallel()

	t.Run("apu reporting almost all of host ram", func(t *testing.T) {
		t.Parallel()

		// Strix Halo shape: linux-amd64, 128 GiB, the iGPU reporting the
		// unified allocation as its own total.
		listing := deviceListing{devices: []DeviceMemory{
			{Name: "HIP0", Description: "AMD Radeon(TM) Graphics", TotalMiB: 126629, FreeMiB: 126629},
		}}
		topology := buildTopology(PlatformLinuxAMD64, hostRAM128GiB, listing, "")

		if !topology.Unified {
			t.Fatal("Unified = false for an AMD APU, want true")
		}
		// The host term binds: 126629 - 1536 MiB is far above the 112 GiB the
		// host can spare, so the clamp decides.
		if topology.DeviceBudgetMiB() != topology.HostBudgetMiB() {
			t.Errorf("DeviceBudgetMiB() = %d, want it clamped to HostBudgetMiB() = %d",
				topology.DeviceBudgetMiB(), topology.HostBudgetMiB())
		}
		if topology.DeviceBudgetBytes > hostRAM128GiB {
			t.Errorf("DeviceBudgetBytes = %d exceeds the machine's RAM", topology.DeviceBudgetBytes)
		}
	})

	t.Run("aliasing entries are never added", func(t *testing.T) {
		t.Parallel()

		// The measured BLAS row reports 0/0 and is dropped, but an aliasing
		// entry that reported a real size must not inflate the pool either:
		// a unified machine has ONE pool, so the pool is the largest device.
		listing := deviceListing{devices: []DeviceMemory{
			{Name: "MTL0", Description: "Apple M4 Max", TotalMiB: metalDeviceTotalMiB},
			{Name: "OPENCL0", Description: "Apple M4 Max", TotalMiB: 131072},
		}}
		topology := buildTopology(PlatformDarwinARM64, hostRAM128GiB, listing, "")

		if !topology.Unified {
			t.Fatal("Unified = false on darwin-arm64, want true")
		}
		if got := topology.DevicePoolMiB(); got != 131072 {
			t.Errorf("DevicePoolMiB() = %d, want the largest device (131072), not the sum (%d)",
				got, metalDeviceTotalMiB+131072)
		}
		if topology.DeviceBudgetBytes > hostRAM128GiB {
			t.Errorf("DeviceBudgetBytes = %d exceeds the machine's %d bytes of RAM",
				topology.DeviceBudgetBytes, hostRAM128GiB)
		}
	})
}

// TestBuildTopologyDiscreteBudgetsAreIndependent locks the other half of the
// rule: with real VRAM the two budgets are separate allowances, and the sum of
// independent cards IS additive.
func TestBuildTopologyDiscreteBudgetsAreIndependent(t *testing.T) {
	t.Parallel()

	listing, ok := parseDeviceListing(fixtureListDevicesDiscreteNVIDIA)
	if !ok {
		t.Fatal("the discrete fixture did not parse")
	}
	// 32 GiB of host RAM beside 48 GiB of VRAM: the device budget must be
	// allowed to exceed the host budget, which is exactly what "independent"
	// means. A unified classification would have clamped it to 28 GiB.
	topology := buildTopology(PlatformLinuxAMD64, gib(32), listing, "")

	if topology.Unified {
		t.Fatal("Unified = true for two discrete NVIDIA cards, want false")
	}
	if got, want := topology.DevicePoolMiB(), int64(24564+24576); got != want {
		t.Errorf("DevicePoolMiB() = %d, want the sum %d", got, want)
	}
	if got, want := topology.DeviceBudgetMiB(), int64(24564+24576)-deviceMarginMiB; got != want {
		t.Errorf("DeviceBudgetMiB() = %d, want %d", got, want)
	}
	if got, want := topology.HostBudgetMiB(), int64(28)*1024; got != want {
		t.Errorf("HostBudgetMiB() = %d, want %d (32 GiB minus the 4 GiB floor)", got, want)
	}
	if topology.DeviceBudgetBytes <= topology.HostBudgetBytes {
		t.Errorf("DeviceBudgetBytes = %d did not exceed HostBudgetBytes = %d; a discrete device budget must not be clamped to host RAM",
			topology.DeviceBudgetBytes, topology.HostBudgetBytes)
	}
}

// TestBuildTopologyBudgetTable is the arithmetic table: the reserve floor, the
// scaled reserve, the margin, the no-device case and the clamps at zero.
func TestBuildTopologyBudgetTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		platform      string
		hostRAMBytes  int64
		devices       []DeviceMemory
		wantUnified   bool
		wantDeviceMiB int64
		wantHostMiB   int64
	}{
		{
			name: "no devices at all", platform: PlatformLinuxAMD64,
			hostRAMBytes: gib(32), devices: nil,
			wantUnified: true, wantDeviceMiB: 0, wantHostMiB: 28 * 1024,
		},
		{
			name: "reserve floor binds below 32 GiB", platform: PlatformDarwinARM64,
			hostRAMBytes: gib(16), devices: []DeviceMemory{{Name: "MTL0", TotalMiB: 10922}},
			wantUnified: true, wantDeviceMiB: 10922 - deviceMarginMiB, wantHostMiB: 12 * 1024,
		},
		{
			name: "scaled reserve binds above 32 GiB", platform: PlatformDarwinARM64,
			hostRAMBytes: gib(64), devices: []DeviceMemory{{Name: "MTL0", TotalMiB: 54613}},
			wantUnified: true, wantDeviceMiB: 54613 - deviceMarginMiB, wantHostMiB: 56 * 1024,
		},
		{
			name: "a device smaller than the margin gets no budget", platform: PlatformLinuxAMD64,
			hostRAMBytes: gib(32), devices: []DeviceMemory{{Name: "Vulkan0", Description: "Intel(R) UHD Graphics", TotalMiB: 1024}},
			wantUnified: true, wantDeviceMiB: 0, wantHostMiB: 28 * 1024,
		},
		{
			name: "host ram below the reserve floor gets no host budget", platform: PlatformLinuxAMD64,
			hostRAMBytes: gib(2), devices: []DeviceMemory{{Name: "CUDA0", Description: "NVIDIA T400", TotalMiB: 4096}},
			wantUnified: false, wantDeviceMiB: 4096 - deviceMarginMiB, wantHostMiB: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			topology := buildTopology(tt.platform, tt.hostRAMBytes,
				deviceListing{devices: tt.devices}, "")

			if topology.Unified != tt.wantUnified {
				t.Errorf("Unified = %v, want %v", topology.Unified, tt.wantUnified)
			}
			if got := topology.DeviceBudgetMiB(); got != tt.wantDeviceMiB {
				t.Errorf("DeviceBudgetMiB() = %d, want %d", got, tt.wantDeviceMiB)
			}
			if got := topology.HostBudgetMiB(); got != tt.wantHostMiB {
				t.Errorf("HostBudgetMiB() = %d, want %d", got, tt.wantHostMiB)
			}
			if topology.DeviceBudgetBytes < 0 || topology.HostBudgetBytes < 0 {
				t.Errorf("budgets went negative: device=%d host=%d",
					topology.DeviceBudgetBytes, topology.HostBudgetBytes)
			}
		})
	}
}

// TestBuildTopologyCarriesAnEmptyDeviceSlice keeps the serialized shape
// stable: a machine with no accelerator writes `[]`, not `null`, so a manifest
// reader never has to special-case it.
func TestBuildTopologyCarriesAnEmptyDeviceSlice(t *testing.T) {
	t.Parallel()

	topology := buildTopology(PlatformLinuxAMD64, gib(32), deviceListing{}, "")
	if topology.Devices == nil {
		t.Fatal("Devices is nil, want an empty slice")
	}
	if len(topology.Devices) != 0 {
		t.Errorf("Devices = %+v, want empty", topology.Devices)
	}
	if topology.DevicePoolMiB() != 0 || topology.DeviceBudgetMiB() != 0 {
		t.Errorf("pool = %d, budget = %d, want zero with no devices",
			topology.DevicePoolMiB(), topology.DeviceBudgetMiB())
	}
}

// TestMemoryTopologyRoundTripsThroughJSON is the manifest contract: the
// topology is JSON-serializable, its keys are the documented snake_case, and
// nothing is lost on the way back.
func TestMemoryTopologyRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()

	listing, ok := parseDeviceListing(fixtureListDevicesDarwinMetal)
	if !ok {
		t.Fatal("the captured fixture did not parse")
	}
	want := buildTopology(PlatformDarwinARM64, hostRAM128GiB, listing, "2026-09-25T10:00:00Z")

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshaling the topology: %v", err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("the marshaled topology is not a JSON object: %v", err)
	}
	for _, key := range []string{
		"devices", "host_ram_gib", "unified",
		"device_budget_bytes", "host_budget_bytes", "probed_at",
	} {
		if _, found := keys[key]; !found {
			t.Errorf("the JSON object has no %q key; got %v", key, keys)
		}
	}

	var got MemoryTopology
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshaling the topology: %v", err)
	}
	if got.HostRAMGiB != want.HostRAMGiB || got.Unified != want.Unified ||
		got.DeviceBudgetBytes != want.DeviceBudgetBytes || got.HostBudgetBytes != want.HostBudgetBytes ||
		got.ProbedAt != want.ProbedAt {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if len(got.Devices) != len(want.Devices) {
		t.Fatalf("round-tripped %d devices, want %d", len(got.Devices), len(want.Devices))
	}
	for i := range want.Devices {
		if got.Devices[i] != want.Devices[i] {
			t.Errorf("devices[%d] = %+v, want %+v", i, got.Devices[i], want.Devices[i])
		}
	}
}

// TestCrossCheckHostRAM covers the consistency check the richer form enables:
// the runtime's own `CPU` row (measured 131072 MiB) against the OS RAM probe
// (measured 128 GiB) — silent when they agree, a Debug line when they do not.
func TestCrossCheckHostRAM(t *testing.T) {
	t.Parallel()

	listing, ok := parseDeviceListing(fixtureParamDumpDarwinMetal)
	if !ok || !listing.hostFound {
		t.Fatal("the parameter-dump fixture did not yield a CPU device row")
	}

	t.Run("agreement is silent", func(t *testing.T) {
		t.Parallel()

		logs := &logCapture{}
		topology := buildTopology(PlatformDarwinARM64, hostRAM128GiB, listing, "")
		crossCheckHostRAM(logs.logger(), topology, listing)

		if got := logs.String(); got != "" {
			t.Errorf("the cross-check logged on agreeing sources: %s", got)
		}
	})

	t.Run("disagreement is reported", func(t *testing.T) {
		t.Parallel()

		logs := &logCapture{}
		// A container limit the OS call cannot see: half the RAM the runtime
		// believes the CPU device has.
		topology := buildTopology(PlatformDarwinARM64, gib(64), listing, "")
		crossCheckHostRAM(logs.logger(), topology, listing)

		got := logs.String()
		if !strings.Contains(got, "disagrees") {
			t.Errorf("the cross-check stayed silent on a 2x disagreement; log = %q", got)
		}
	})

	t.Run("no cpu row means no check", func(t *testing.T) {
		t.Parallel()

		logs := &logCapture{}
		plain, ok := parseDeviceListing(fixtureListDevicesDarwinMetal)
		if !ok {
			t.Fatal("the list-devices fixture did not parse")
		}
		topology := buildTopology(PlatformDarwinARM64, gib(16), plain, "")
		crossCheckHostRAM(logs.logger(), topology, plain)

		if got := logs.String(); got != "" {
			t.Errorf("the cross-check logged without a CPU row: %s", got)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// The I/O half
// ─────────────────────────────────────────────────────────────────────────────

// assertUnprobed checks the fail-soft contract: a probe that did not answer
// returns the zero topology, which a caller must read as "unknown" and never
// as "no memory".
func assertUnprobed(t *testing.T, topology MemoryTopology) {
	t.Helper()

	if len(topology.Devices) != 0 {
		t.Errorf("Devices = %+v, want none", topology.Devices)
	}
	if topology.HostRAMGiB != 0 || topology.Unified || topology.DeviceBudgetBytes != 0 ||
		topology.HostBudgetBytes != 0 || topology.ProbedAt != "" {
		t.Errorf("topology = %+v, want the zero value", topology)
	}
}

// TestProbeDevicesIsFailSoft covers every way the spawn can go wrong. None of
// them may produce an error: a topology probe refines a load decision, so it
// must never be the reason a load fails.
func TestProbeDevicesIsFailSoft(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "missing binary",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "no-such-llama-server")
			},
		},
		{
			name: "empty path",
			path: func(*testing.T) string { return "" },
		},
		{
			name: "a file that is not a program",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "llama-server")
				if err := os.WriteFile(path, []byte("this is not an executable\n"), 0o644); err != nil {
					t.Fatalf("staging the file: %v", err)
				}
				return path
			},
		},
		{
			name: "a directory",
			path: func(t *testing.T) string { return t.TempDir() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logs := &logCapture{}
			topology, ok := ProbeDevices(t.Context(), tt.path(t), logs.logger())
			if ok {
				t.Fatal("ProbeDevices ok = true, want false")
			}
			assertUnprobed(t, topology)
		})
	}

	t.Run("a cancelled context", func(t *testing.T) {
		t.Parallel()

		logs := &logCapture{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		topology, ok := ProbeDevices(ctx, "sh", logs.logger())
		if ok {
			t.Error("ProbeDevices ok = true on a cancelled context, want false")
		}
		assertUnprobed(t, topology)
	})

	t.Run("nil context and nil logger are tolerated", func(t *testing.T) {
		t.Parallel()

		//nolint:staticcheck // the nil arguments are the contract under test.
		topology, ok := ProbeDevices(nil, "", nil)
		if ok {
			t.Error("ProbeDevices ok = true for an empty path, want false")
		}
		assertUnprobed(t, topology)
	})
}

// stageFakeRuntime writes a POSIX shell script that stands in for
// llama-server. The probe spawns a REAL child through the hardened path, so
// the two tests below are the only ones that need a platform; they skip on
// Windows, where the parser, classifier and budget tables above already carry
// every decision the spawn would feed.
func stageFakeRuntime(t *testing.T, body string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the fake runtime is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("staging the fake runtime: %v", err)
	}
	return path
}

// TestProbeDevicesSpawnsTheListDevicesProbe is the end-to-end happy path: the
// flag the probe uses, the inventory it returns, and the stamp it applies.
func TestProbeDevicesSpawnsTheListDevicesProbe(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := stageFakeRuntime(t, fmt.Sprintf(
		"printf '%%s\\n' \"$@\" > %s\ncat <<'EOF'\n%sEOF\n", argsFile, fixtureListDevicesDarwinMetal))

	logs := &logCapture{}
	start := time.Now()
	topology, ok := ProbeDevices(t.Context(), script, logs.logger())
	elapsed := time.Since(start)

	if !ok {
		t.Fatalf("ProbeDevices ok = false; log = %s", logs.String())
	}
	if elapsed > probeCommandTimeout {
		t.Errorf("a healthy probe took %v, over the %v bound", elapsed, probeCommandTimeout)
	}

	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the fake runtime did not record its arguments: %v", err)
	}
	if got := string(recorded); got != listDevicesArg+"\n" {
		t.Errorf("the runtime was invoked with %q, want exactly %q", got, listDevicesArg)
	}

	if len(topology.Devices) != 1 || topology.Devices[0].Name != "MTL0" {
		t.Errorf("Devices = %+v, want the single MTL0 entry", topology.Devices)
	}
	if topology.DevicePoolMiB() != metalDeviceTotalMiB {
		t.Errorf("DevicePoolMiB() = %d, want %d", topology.DevicePoolMiB(), metalDeviceTotalMiB)
	}
	// The fixture's `BLAS: Accelerate (0 MiB, 0 MiB free)` row is dropped, and
	// the drop must leave a trace: a machine whose only accelerator failed to
	// report a size would otherwise look CPU-only with nothing in the log to
	// explain it.
	if !strings.Contains(logs.String(), "dropped memoryless entries") {
		t.Errorf("the 0/0 entry was dropped without a diagnostic; log = %q", logs.String())
	}
	// The host RAM half comes from the real machine, so only its shape is
	// asserted: a stamp that parses, a nonzero host budget, and a budget that
	// never exceeds the machine's RAM on a unified classification.
	if _, err := time.Parse(time.RFC3339, topology.ProbedAt); err != nil {
		t.Errorf("ProbedAt = %q is not RFC 3339: %v", topology.ProbedAt, err)
	}
	if topology.HostRAMGiB <= 0 {
		t.Errorf("HostRAMGiB = %v, want this machine's RAM", topology.HostRAMGiB)
	}
	if topology.HostBudgetBytes <= 0 || topology.HostBudgetBytes > int64(topology.HostRAMGiB*gibibyte) {
		t.Errorf("HostBudgetBytes = %d, want it inside (0, host RAM]", topology.HostBudgetBytes)
	}
	if topology.Unified && topology.DeviceBudgetBytes > int64(topology.HostRAMGiB*gibibyte) {
		t.Errorf("DeviceBudgetBytes = %d exceeds this machine's RAM on a unified pool",
			topology.DeviceBudgetBytes)
	}
	t.Logf("this machine: unified=%v host=%.1fGiB devices=%+v device_budget=%dMiB host_budget=%dMiB (%s)",
		topology.Unified, topology.HostRAMGiB, topology.Devices,
		topology.DeviceBudgetMiB(), topology.HostBudgetMiB(), elapsed.Round(time.Millisecond))
}

// TestProbeDevicesUnrecognizedOutputIsNotAnEmptyInventory separates "the
// machine has no accelerator" from "the binary did not answer": a runtime that
// prints something else entirely must not be read as a zero-device machine.
func TestProbeDevicesUnrecognizedOutputIsNotAnEmptyInventory(t *testing.T) {
	t.Parallel()

	script := stageFakeRuntime(t, "echo 'error: unknown option --list-devices'\n")

	logs := &logCapture{}
	topology, ok := ProbeDevices(t.Context(), script, logs.logger())
	if ok {
		t.Errorf("ProbeDevices ok = true for unrecognized output; topology = %+v", topology)
	}
	assertUnprobed(t, topology)
	if !strings.Contains(logs.String(), "no recognizable inventory") {
		t.Errorf("the rejection was not logged at Debug; log = %q", logs.String())
	}
}

// TestProbeDevicesIsBoundedOnAHungBinary proves the timeout is real, and that
// it holds even when the child leaves a grandchild holding the stdout pipe —
// the shape a wedged driver actually produces. Without a wait deadline the
// read end would stay open and the probe would block for the child's own
// duration, which is exactly the stall the bound exists to prevent.
func TestProbeDevicesIsBoundedOnAHungBinary(t *testing.T) {
	t.Parallel()

	script := stageFakeRuntime(t, "sleep 15\n")

	logs := &logCapture{}
	start := time.Now()
	topology, ok := ProbeDevices(t.Context(), script, logs.logger())
	elapsed := time.Since(start)

	limit := probeCommandTimeout + probeWaitDelay + 3*time.Second
	if elapsed > limit {
		t.Errorf("a hung probe took %v, want it bounded by %v (timeout %v + wait delay %v)",
			elapsed, limit, probeCommandTimeout, probeWaitDelay)
	}
	if ok {
		t.Errorf("ProbeDevices ok = true for a hung binary; topology = %+v", topology)
	}
	assertUnprobed(t, topology)
	t.Logf("the hung probe returned after %v (bound %v)", elapsed.Round(10*time.Millisecond), limit)
}

// TestProbeDevicesNonZeroExitIsFailSoft covers the third failure shape: a
// binary that runs and rejects the flag.
func TestProbeDevicesNonZeroExitIsFailSoft(t *testing.T) {
	t.Parallel()

	script := stageFakeRuntime(t, "echo 'Available devices:'\nexit 3\n")

	logs := &logCapture{}
	topology, ok := ProbeDevices(t.Context(), script, logs.logger())
	if ok {
		t.Errorf("ProbeDevices ok = true for a nonzero exit; topology = %+v", topology)
	}
	assertUnprobed(t, topology)
}

// ─────────────────────────────────────────────────────────────────────────────
// Source-level guards
// ─────────────────────────────────────────────────────────────────────────────

// TestTopologySourceDoesNoPathContainment enforces the centralized path API
// rule for this file: topology.go spawns a binary whose path the CALLER
// resolved through the layout's containment checks, so it must not grow an
// ad-hoc path predicate of its own.
func TestTopologySourceDoesNoPathContainment(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("reading topology.go: %v", err)
	}
	content := string(source)

	for _, banned := range []string{"strings.HasPrefix", "filepath.Rel", "filepath.Join", `"../"`} {
		if strings.Contains(content, banned) {
			t.Errorf("topology.go contains %s; path construction and containment belong to the centralized path API (backend/config/paths.go, sp4rk/pathutil)",
				banned)
		}
	}

	// The hardened spawn path is the only way this file may reach a process.
	if !strings.Contains(content, "runProbeCommand(ctx, binaryPath, listDevicesArg)") {
		t.Error("topology.go no longer spawns the probe through runProbeCommand; the LookPath resolution, the timeout bound and HideConsole would be lost")
	}
}
