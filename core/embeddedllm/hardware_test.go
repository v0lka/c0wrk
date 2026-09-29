package embeddedllm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCudaBackendFor pins the driver-version → asset-tag map. The direction
// matters: a build for a newer toolkit will not load on an older driver, so an
// in-between version always rounds DOWN to a tag the driver can still run.
func TestCudaBackendFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		major, minor int
		wantOK       bool
		wantBackend  Backend
		wantTag      string
	}{
		// The newest tag absorbs anything at or above it.
		{major: 13, minor: 3, wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},
		{major: 13, minor: 4, wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},
		{major: 13, minor: 9, wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},
		{major: 14, minor: 0, wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},
		{major: 99, minor: 0, wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},

		// A 13.x driver below 13.3 takes the newest 12.x tag.
		{major: 13, minor: 0, wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8"},
		{major: 13, minor: 1, wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8"},
		{major: 13, minor: 2, wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8"},

		// 12.8 and up take the 12.8 tag.
		{major: 12, minor: 8, wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8"},
		{major: 12, minor: 9, wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8"},

		// Everything else in the 12.x series takes the oldest tag.
		{major: 12, minor: 0, wantOK: true, wantBackend: BackendCUDA124, wantTag: "12.4"},
		{major: 12, minor: 4, wantOK: true, wantBackend: BackendCUDA124, wantTag: "12.4"},
		{major: 12, minor: 7, wantOK: true, wantBackend: BackendCUDA124, wantTag: "12.4"},

		// Older than every pinned archive: not a hit, so the ladder keeps
		// looking instead of provisioning a build the driver cannot load.
		{major: 11, minor: 8, wantOK: false},
		{major: 11, minor: 0, wantOK: false},
		{major: 10, minor: 2, wantOK: false},
		{major: 0, minor: 0, wantOK: false},
	}

	for _, tc := range cases {
		backend, tag, ok := cudaBackendFor(tc.major, tc.minor)

		if ok != tc.wantOK {
			t.Errorf("cudaBackendFor(%d, %d) ok = %v, want %v", tc.major, tc.minor, ok, tc.wantOK)
			continue
		}
		if !tc.wantOK {
			if backend != "" || tag != "" {
				t.Errorf("cudaBackendFor(%d, %d) = (%q, %q), want empty on a miss",
					tc.major, tc.minor, backend, tag)
			}
			continue
		}
		if backend != tc.wantBackend {
			t.Errorf("cudaBackendFor(%d, %d) backend = %q, want %q",
				tc.major, tc.minor, backend, tc.wantBackend)
		}
		if tag != tc.wantTag {
			t.Errorf("cudaBackendFor(%d, %d) tag = %q, want %q",
				tc.major, tc.minor, tag, tc.wantTag)
		}
		// The tag and the backend constant must never disagree: the backend IS
		// "cuda-" plus the tag.
		if string(backend) != "cuda-"+tag {
			t.Errorf("cudaBackendFor(%d, %d) = (%q, %q): backend is not cuda-<tag>",
				tc.major, tc.minor, backend, tag)
		}
	}
}

// TestParseNvidiaSMICUDAVersion covers the driver-version field of the
// nvidia-smi header table, including the placeholder prose drivers substitute
// when they cannot report a CUDA version.
func TestParseNvidiaSMICUDAVersion(t *testing.T) {
	t.Parallel()

	const header = `Wed Sep 23 12:00:00 2026
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 580.65.06              Driver Version: 580.65.06      CUDA Version: 13.0     |
|-----------------------------------------+------------------------+----------------------+
| GPU  Name                 Persistence-M | Bus-Id          Disp.A | Volatile Uncorr. ECC |
|   0  NVIDIA GeForce RTX 4090        Off | 00000000:01:00.0    On |                  N/A |
+-----------------------------------------+------------------------+----------------------+
`

	cases := []struct {
		name      string
		out       string
		wantOK    bool
		wantMajor int
		wantMinor int
	}{
		{name: "driver reporting 13.0", out: header, wantOK: true, wantMajor: 13, wantMinor: 0},
		{name: "new driver reports CUDA UMD version", out: "| NVIDIA-SMI 610.88                 KMD Version: 610.88        CUDA UMD Version: 13.3     |", wantOK: true, wantMajor: 13, wantMinor: 3},
		{name: "CUDA UMD version with no space after the colon", out: "CUDA UMD Version:12.8", wantOK: true, wantMajor: 12, wantMinor: 8},
		{name: "CUDA UMD version reports N/A", out: "CUDA UMD Version: N/A", wantOK: false},
		{
			name:   "driver reporting 12.4",
			out:    "| NVIDIA-SMI 550.54.15   Driver Version: 550.54.15   CUDA Version: 12.4     |",
			wantOK: true, wantMajor: 12, wantMinor: 4,
		},
		{
			name:   "no space after the colon",
			out:    "CUDA Version:12.8",
			wantOK: true, wantMajor: 12, wantMinor: 8,
		},
		{
			name:   "windows CRLF line endings",
			out:    "Driver Version: 580.65\r\nCUDA Version: 13.3\r\n",
			wantOK: true, wantMajor: 13, wantMinor: 3,
		},
		{name: "driver reports N/A", out: "CUDA Version: N/A", wantOK: false},
		{name: "not supported", out: "CUDA Version: [Not Supported]", wantOK: false},
		{name: "field absent", out: "NVIDIA-SMI 580.65.06   Driver Version: 580.65.06", wantOK: false},
		{name: "empty output", out: "", wantOK: false},
		{name: "major only", out: "CUDA Version: 12", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			major, minor, ok := parseNvidiaSMICUDAVersion(tc.out)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (major=%d minor=%d)", ok, tc.wantOK, major, minor)
			}
			if !tc.wantOK {
				return
			}
			if major != tc.wantMajor || minor != tc.wantMinor {
				t.Errorf("parsed %d.%d, want %d.%d", major, minor, tc.wantMajor, tc.wantMinor)
			}
		})
	}
}

// TestParseNvccRelease covers the toolkit-version fallback used when
// nvidia-smi is absent (a toolkit-only install, or a container without the
// driver userspace).
func TestParseNvccRelease(t *testing.T) {
	t.Parallel()

	const full = `nvcc: NVIDIA (R) Cuda compiler driver
Copyright (c) 2005-2025 NVIDIA Corporation
Built on Wed_Aug_14_10:10:22_PDT_2025
Cuda compilation tools, release 12.4, V12.4.131
Build cuda_12.4.r12.4/compiler.00000000_0
`

	cases := []struct {
		name      string
		out       string
		wantOK    bool
		wantMajor int
		wantMinor int
	}{
		{name: "full nvcc output", out: full, wantOK: true, wantMajor: 12, wantMinor: 4},
		{name: "release 13.3", out: "Cuda compilation tools, release 13.3, V13.3.88", wantOK: true, wantMajor: 13, wantMinor: 3},
		{name: "no release field", out: "nvcc: NVIDIA (R) Cuda compiler driver", wantOK: false},
		{name: "empty output", out: "", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			major, minor, ok := parseNvccRelease(tc.out)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && (major != tc.wantMajor || minor != tc.wantMinor) {
				t.Errorf("parsed %d.%d, want %d.%d", major, minor, tc.wantMajor, tc.wantMinor)
			}
		})
	}
}

// TestNvccAndNvidiaSmiAgreeOnTag checks the two CUDA sources feed the same tag
// map, so an install driven by nvcc resolves the same archive as one driven by
// nvidia-smi.
func TestNvccAndNvidiaSmiAgreeOnTag(t *testing.T) {
	t.Parallel()

	smiMajor, smiMinor, ok := parseNvidiaSMICUDAVersion("CUDA Version: 12.4")
	if !ok {
		t.Fatal("nvidia-smi parse failed")
	}
	nvccMajor, nvccMinor, ok := parseNvccRelease("Cuda compilation tools, release 12.4, V12.4.131")
	if !ok {
		t.Fatal("nvcc parse failed")
	}
	if smiMajor != nvccMajor || smiMinor != nvccMinor {
		t.Fatalf("the two CUDA sources parsed the same version differently: %d.%d vs %d.%d",
			smiMajor, smiMinor, nvccMajor, nvccMinor)
	}

	smiBackend, smiTag, ok := cudaBackendFor(smiMajor, smiMinor)
	if !ok {
		t.Fatal("cudaBackendFor rejected the nvidia-smi version")
	}
	nvccBackend, nvccTag, ok := cudaBackendFor(nvccMajor, nvccMinor)
	if !ok {
		t.Fatal("cudaBackendFor rejected the nvcc version")
	}
	if smiBackend != nvccBackend || smiTag != nvccTag {
		t.Errorf("the two CUDA sources disagree: (%q,%q) vs (%q,%q)",
			smiBackend, smiTag, nvccBackend, nvccTag)
	}
	if smiBackend != BackendCUDA124 {
		t.Errorf("a 12.4 driver resolved to %q, want %q", smiBackend, BackendCUDA124)
	}
}

// TestBackendFromCUDAOutput covers the pure half of the CUDA step: which source
// wins, and when a detected version must NOT count as a hit.
func TestBackendFromCUDAOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		nvidiaSMI   string
		nvcc        string
		wantOK      bool
		wantBackend Backend
		wantTag     string
	}{
		{
			name: "driver answers", nvidiaSMI: "CUDA Version: 12.4",
			wantOK: true, wantBackend: BackendCUDA124, wantTag: "12.4",
		},
		{
			name:   "toolkit answers when the driver userspace is absent",
			nvcc:   "Cuda compilation tools, release 13.3, V13.3.88",
			wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3",
		},
		{
			name: "driver wins over the toolkit", nvidiaSMI: "CUDA Version: 12.4",
			nvcc:   "Cuda compilation tools, release 13.3, V13.3.88",
			wantOK: true, wantBackend: BackendCUDA124, wantTag: "12.4",
		},
		{
			// A driver that reports no CUDA version leaves the toolkit to decide.
			name: "driver reports N/A so the toolkit decides", nvidiaSMI: "CUDA Version: N/A",
			nvcc:   "Cuda compilation tools, release 12.8, V12.8.61",
			wantOK: true, wantBackend: BackendCUDA128, wantTag: "12.8",
		},
		{
			// A driver too old for every pinned archive is not a hit: the ladder
			// must keep looking rather than install a build that cannot load.
			name: "driver too old for any pinned asset", nvidiaSMI: "CUDA Version: 11.8",
			wantOK: false,
		},
		{
			name: "both sources too old", nvidiaSMI: "CUDA Version: 11.4",
			nvcc:   "Cuda compilation tools, release 11.8, V11.8.89",
			wantOK: false,
		},
		{name: "new driver reports CUDA UMD version", nvidiaSMI: "| NVIDIA-SMI 610.88                 KMD Version: 610.88        CUDA UMD Version: 13.3     |", wantOK: true, wantBackend: BackendCUDA133, wantTag: "13.3"},
		{name: "neither source answered", wantOK: false},
		{name: "unparsable output", nvidiaSMI: "garbage", nvcc: "garbage", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend, tag, ok := backendFromCUDAOutput(tc.nvidiaSMI, tc.nvcc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (backend=%q tag=%q)", ok, tc.wantOK, backend, tag)
			}
			if !tc.wantOK {
				if backend != "" || tag != "" {
					t.Errorf("got (%q, %q), want empty on a miss", backend, tag)
				}
				return
			}
			if backend != tc.wantBackend || tag != tc.wantTag {
				t.Errorf("got (%q, %q), want (%q, %q)", backend, tag, tc.wantBackend, tc.wantTag)
			}
		})
	}
}

// TestExcerptLines pins the bounding helper the miss logger uses: it clips to
// the first n lines and normalizes CRLF so a Windows probe excerpt reads the
// same as a Unix one.
func TestExcerptLines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "fewer lines than the cap", in: "a\nb\n", n: 5, want: "a\nb\n"},
		{name: "exactly the cap", in: "a\nb\nc", n: 3, want: "a\nb\nc"},
		{name: "clips to the first n lines", in: "a\nb\nc\nd", n: 2, want: "a\nb"},
		{name: "normalizes CRLF", in: "a\r\nb\r\nc", n: 5, want: "a\nb\nc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := excerptLines(tc.in, tc.n); got != tc.want {
				t.Errorf("excerptLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// TestParseMeminfoMemTotal covers the Linux RAM source. The kernel reports
// kilobytes, and the parser must not guess when the field is malformed.
func TestParseMeminfoMemTotal(t *testing.T) {
	t.Parallel()

	const meminfo = `MemTotal:       32768000 kB
MemFree:         1234567 kB
MemAvailable:   16777216 kB
Buffers:          234567 kB
Cached:          8388608 kB
SwapTotal:             0 kB
`

	cases := []struct {
		name    string
		content string
		wantOK  bool
		want    uint64
	}{
		{name: "real meminfo", content: meminfo, wantOK: true, want: 32768000 * 1024},
		{name: "first field", content: "MemTotal:       16384 kB\n", wantOK: true, want: 16384 * 1024},
		{name: "no unit", content: "MemTotal: 16384\n", wantOK: true, want: 16384 * 1024},
		{name: "lowercase unit", content: "MemTotal: 16384 kb\n", wantOK: true, want: 16384 * 1024},
		{name: "extra spaces around the key", content: "  MemTotal : 16384 kB\n", wantOK: true, want: 16384 * 1024},
		{name: "crlf", content: "MemTotal:\t16384 kB\r\n", wantOK: true, want: 16384 * 1024},
		{name: "field absent", content: "MemFree: 1234 kB\n", wantOK: false},
		{name: "empty content", content: "", wantOK: false},
		{name: "non-numeric value", content: "MemTotal: unknown kB\n", wantOK: false},
		{name: "value absent", content: "MemTotal:\n", wantOK: false},
		{name: "wrong unit", content: "MemTotal: 16384 MB\n", wantOK: false},
		{name: "negative value", content: "MemTotal: -1 kB\n", wantOK: false},
		// A similarly named field must not be mistaken for MemTotal.
		{name: "memtotal lowercase key differs", content: "MemTotalFree: 16384 kB\n", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseMeminfoMemTotal(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %d)", ok, tc.wantOK, got)
			}
			if tc.wantOK && got != tc.want {
				t.Errorf("bytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParseDecimalBytes covers the darwin "sysctl -n hw.memsize" output shape.
func TestParseDecimalBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		out    string
		wantOK bool
		want   uint64
	}{
		{name: "24 GiB with trailing newline", out: "25769803776\n", wantOK: true, want: 25769803776},
		{name: "16 GiB bare", out: "17179869184", wantOK: true, want: 17179869184},
		{name: "crlf", out: "17179869184\r\n", wantOK: true, want: 17179869184},
		{name: "surrounding spaces", out: "  17179869184  ", wantOK: true, want: 17179869184},
		{name: "zero", out: "0", wantOK: true, want: 0},
		{name: "empty", out: "", wantOK: false},
		{name: "key=value form", out: "hw.memsize: 17179869184", wantOK: false},
		{name: "non-numeric", out: "not a number", wantOK: false},
		{name: "negative", out: "-1", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseDecimalBytes(tc.out)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && got != tc.want {
				t.Errorf("bytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestGiBConversionMatchesDemoArithmetic checks the byte → GiB divisor against
// the values the demo script's tiers assume, so a nominal machine size lands in
// the tier a user would expect.
func TestGiBConversionMatchesDemoArithmetic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		nominal string
		bytes   uint64
		want    float64
	}{
		{nominal: "16 GB", bytes: 16 * gibibyte, want: 16},
		{nominal: "24 GB", bytes: 24 * gibibyte, want: 24},
		{nominal: "32 GB", bytes: 32 * gibibyte, want: 32},
		{nominal: "36 GB", bytes: 36 * gibibyte, want: 36},
		{nominal: "64 GB", bytes: 64 * gibibyte, want: 64},
		{nominal: "128 GB", bytes: 128 * gibibyte, want: 128},
	}

	for _, tc := range cases {
		got := float64(tc.bytes) / gibibyte
		if got != tc.want {
			t.Errorf("%s: %d bytes = %v GiB, want %v", tc.nominal, tc.bytes, got, tc.want)
		}
		// The conversion must not lose a whole GiB: the memory gate derives a
		// host budget from this figure, and a budget short by a GiB is a refusal
		// on a machine that has the RAM.
		if int(got) != int(tc.want) {
			t.Errorf("%s: %d bytes = %v GiB, want a whole %v GiB", tc.nominal, tc.bytes, got, tc.want)
		}
	}

	// Linux reports MemTotal slightly below the nominal size; flooring is what
	// keeps such a machine in the tier the demo would pick.
	linux24GB := uint64(25_132_748) * 1024 // ~23.97 GiB, a typical 24 GB MemTotal
	if got := float64(linux24GB) / gibibyte; int(got) != 23 {
		t.Errorf("a 24 GB Linux MemTotal floors to %d GiB, want 23", int(got))
	}
}

// TestBackendPredicates locks the three predicates the resolution rules are
// built from.
func TestBackendPredicates(t *testing.T) {
	t.Parallel()

	all := []Backend{BackendMetal, BackendCUDA124, BackendCUDA128, BackendCUDA133, BackendROCm, BackendVulkan, BackendCPU}

	wantCUDA := map[Backend]bool{BackendCUDA124: true, BackendCUDA128: true, BackendCUDA133: true}
	wantX64Only := map[Backend]bool{
		BackendCUDA124: true, BackendCUDA128: true, BackendCUDA133: true, BackendROCm: true,
	}
	wantGPU := map[Backend]bool{
		BackendMetal: true, BackendCUDA124: true, BackendCUDA128: true,
		BackendCUDA133: true, BackendROCm: true, BackendVulkan: true,
	}

	for _, b := range all {
		if got := b.IsCUDA(); got != wantCUDA[b] {
			t.Errorf("%q.IsCUDA() = %v, want %v", b, got, wantCUDA[b])
		}
		if got := b.x64Only(); got != wantX64Only[b] {
			t.Errorf("%q.x64Only() = %v, want %v", b, got, wantX64Only[b])
		}
		if got := b.gpuAccelerated(); got != wantGPU[b] {
			t.Errorf("%q.gpuAccelerated() = %v, want %v", b, got, wantGPU[b])
		}
	}

	// The CPU build is the only backend that is neither accelerated nor
	// architecture-restricted, which is what makes it a safe universal
	// fallback.
	if BackendCPU.IsCUDA() || BackendCPU.x64Only() || BackendCPU.gpuAccelerated() {
		t.Error("BackendCPU must be unaccelerated and unrestricted to serve as the fallback")
	}
}

// TestProbeHardwareOnThisMachine is the end-to-end smoke test: whatever this
// machine is, the probe must produce a coherent, supported Hardware value
// without hanging. It skips only when the RAM source itself is unavailable,
// which is a sandbox limitation rather than a probe defect.
func TestProbeHardwareOnThisMachine(t *testing.T) {
	// A nil logger is explicitly supported and is what the RPC layer passes when it
	// has nothing to add; ProbeHardware substitutes a discard handler.
	hw, err := ProbeHardware(t.Context(), nil)
	if errors.Is(err, ErrRAMUnknown) {
		t.Skipf("total RAM is unreadable in this environment: %v", err)
	}
	if err != nil {
		t.Fatalf("ProbeHardware error = %v, want success", err)
	}

	wantPlatform := runtime.GOOS + "-" + runtime.GOARCH
	if hw.Platform != wantPlatform {
		t.Errorf("Platform = %q, want %q", hw.Platform, wantPlatform)
	}
	if hw.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", hw.Arch, runtime.GOARCH)
	}
	if !IsSupportedPlatform(hw.Platform) {
		t.Errorf("Platform %q has no pinned artifacts", hw.Platform)
	}
	if hw.RAMGiB <= 0 {
		t.Errorf("RAMGiB = %v, want a positive size", hw.RAMGiB)
	}

	known := map[Backend]bool{
		BackendMetal: true, BackendCUDA124: true, BackendCUDA128: true,
		BackendCUDA133: true, BackendROCm: true, BackendVulkan: true, BackendCPU: true,
	}
	if !known[hw.Backend] {
		t.Errorf("Backend = %q, which is not one of the known backends", hw.Backend)
	}

	// Metal is Apple Silicon's alone, and a CUDA tag travels only with a CUDA
	// backend.
	if hw.Backend == BackendMetal && hw.Platform != PlatformDarwinARM64 {
		t.Errorf("Backend = metal on platform %q, want it only on %q", hw.Platform, PlatformDarwinARM64)
	}
	if hw.Backend.IsCUDA() {
		if hw.CUDATag == "" {
			t.Error("a CUDA backend was probed without a CUDA asset tag")
		}
		if string(hw.Backend) != "cuda-"+hw.CUDATag {
			t.Errorf("Backend %q and CUDATag %q disagree", hw.Backend, hw.CUDATag)
		}
	} else if hw.CUDATag != "" {
		t.Errorf("CUDATag = %q on the non-CUDA backend %q, want empty", hw.CUDATag, hw.Backend)
	}

	// Whatever was probed must be provisionable, or degrade to something that
	// is: the pair feeds straight into Resolve.
	if CheckMemoryBudget(ResolveInput{MachineProfile: MachineProfile{
		Platform: hw.Platform, Backend: hw.Backend, RAMGiB: hw.RAMGiB,
	}}) == nil {
		res, err := ResolveMachine(hw.Platform, hw.Backend, hw.RAMGiB)
		if err != nil {
			t.Fatalf("ResolveMachine(%q, %q, %v) error = %v, want a provisionable plan",
				hw.Platform, hw.Backend, hw.RAMGiB, err)
		}
		if len(res.Assets) == 0 {
			t.Error("Resolve returned an empty asset set")
		}
		if !strings.Contains(string(res.Packing), "Q") {
			t.Errorf("Packing = %q, want a ternary packing", res.Packing)
		}
		t.Logf("this machine: platform=%s backend=%s ram=%.1fGiB -> packing=%s ngl=%d ctx=%d imageMaxTokens=%d",
			hw.Platform, hw.Backend, hw.RAMGiB, res.Packing, res.Layers, res.ContextSize, res.ImageMaxTokens)
	}
}

// TestRunProbeCommandAbsentToolIsNotAnError locks the "absent helper is a
// normal outcome" semantics the detection ladder depends on: a machine without
// nvidia-smi is not broken, it just is not a CUDA machine.
func TestRunProbeCommandAbsentToolIsNotAnError(t *testing.T) {
	t.Parallel()

	// A name that cannot exist in PATH.
	out, err := runProbeCommand(t.Context(), "c0wrk-definitely-not-installed-probe")
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
	if !errors.Is(err, errProbeToolAbsent) {
		t.Errorf("error = %v, want it to wrap errProbeToolAbsent", err)
	}
	if commandAvailable("c0wrk-definitely-not-installed-probe") {
		t.Error("commandAvailable reported a tool that is not installed")
	}
	// The shell itself is always present, so the positive path is exercised too.
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	if !commandAvailable(shell) {
		t.Errorf("commandAvailable(%q) = false on %s, want true", shell, runtime.GOOS)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ETXTBSY retry: the transient spawn refusal runProbeCommand absorbs
// ─────────────────────────────────────────────────────────────────────────────

// TestIsTransientExecError pins the ONE errno runProbeCommand retries. Getting
// it wrong in either direction is costly: retrying a real failure re-runs a
// child, and NOT retrying ETXTBSY turns a momentary kernel hiccup into a
// fail-soft "no accelerator" verdict.
func TestIsTransientExecError(t *testing.T) {
	t.Parallel()

	forkExec := &os.PathError{Op: "fork/exec", Path: "/tmp/llama-server", Err: syscall.ETXTBSY}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"a bare ETXTBSY errno", syscall.ETXTBSY, true},
		{"the fork/exec PathError the kernel actually returns", forkExec, true},
		{"ETXTBSY wrapped again by a caller", fmt.Errorf("probe: %w", forkExec), true},
		{"a missing binary is a real answer", syscall.ENOENT, false},
		{"a permission refusal is a real answer", syscall.EACCES, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isTransientExecError(tc.err); got != tc.want {
				t.Errorf("isTransientExecError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// holdExecTargetForWriting stages a fake runtime and opens it for writing, then
// PROVES that this kernel refuses to exec a file held open for writing — the
// ETXTBSY condition runProbeCommand retries. Linux enforces that at execve;
// macOS and Windows do not, so there the test skips rather than certifying a
// condition the platform cannot produce (and the assertion cannot silently
// rot into certifying nothing). The descriptor is left open; the caller
// releases it to shape the scenario.
func holdExecTargetForWriting(t *testing.T, body string) (file *os.File, script string) {
	t.Helper()

	script = stageFakeRuntime(t, body)
	file, err := os.OpenFile(script, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening the fake runtime for writing: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	if _, err := runProbeOnce(t.Context(), script, nil); !errors.Is(err, syscall.ETXTBSY) {
		t.Skipf("this kernel does not refuse exec of a file open for writing (exec error = %v); ETXTBSY-on-exec is enforced on Linux only", err)
	}
	return file, script
}

// TestRunProbeCommandRetriesWhileTheTargetIsOpenForWriting is the happy path
// the retry exists for: execve is refused with ETXTBSY while the target is held
// open for writing, and the call still succeeds once the writer lets go —
// without the caller ever seeing a failure. Holding the descriptor HERE
// reproduces the kernel condition deterministically (the check is process-wide,
// not tied to the fork that runs the child); a sibling parallel test would only
// ever hit it by accident.
func TestRunProbeCommandRetriesWhileTheTargetIsOpenForWriting(t *testing.T) {
	t.Parallel()

	f, script := holdExecTargetForWriting(t, "echo retried\n")

	released := make(chan struct{})
	go func() {
		defer close(released)
		// Hold for one backoff: the first attempt loses the race, and an
		// attempt a couple of backoffs later is guaranteed to win it.
		time.Sleep(probeCommandRetryBackoff)
		_ = f.Close()
	}()
	t.Cleanup(func() { <-released })

	out, err := runProbeCommand(t.Context(), script)
	if err != nil {
		t.Fatalf("runProbeCommand error = %v, want it to retry past ETXTBSY", err)
	}
	if out != "retried\n" {
		t.Errorf("runProbeCommand output = %q, want %q", out, "retried\n")
	}
}

// TestRunProbeCommandGivesUpOnAPersistentlyBusyTarget proves the retry is
// BOUNDED: a target held open for writing for the whole call surfaces the
// errno once the attempts are spent rather than being retried forever. The
// elapsed floor also proves the retries really happened instead of the first
// refusal being returned immediately.
func TestRunProbeCommandGivesUpOnAPersistentlyBusyTarget(t *testing.T) {
	t.Parallel()

	// The descriptor stays open for the whole call; t.Cleanup closes it.
	_, script := holdExecTargetForWriting(t, "echo unreachable\n")

	start := time.Now()
	_, err := runProbeCommand(t.Context(), script)
	elapsed := time.Since(start)

	if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("runProbeCommand error = %v, want it to surface ETXTBSY once the attempts are spent", err)
	}
	if elapsed < probeCommandRetryBackoff {
		t.Errorf("runProbeCommand returned after %v, want at least one retry (>= %v)", elapsed, probeCommandRetryBackoff)
	}
	if elapsed > probeCommandTimeout {
		t.Errorf("runProbeCommand took %v, want it inside the %v probe budget", elapsed, probeCommandTimeout)
	}
}

// TestRunProbeCommandDoesNotRetryANonTransientFailure guards the other
// direction: a child that RUNS and exits nonzero is a real answer, so the spawn
// must not be repeated — the counter proves the script executed exactly once.
func TestRunProbeCommandDoesNotRetryANonTransientFailure(t *testing.T) {
	t.Parallel()

	counter := filepath.Join(t.TempDir(), "runs.txt")
	script := stageFakeRuntime(t, fmt.Sprintf("echo run >> %s\nexit 7\n", counter))

	_, err := runProbeCommand(t.Context(), script)
	if err == nil {
		t.Fatal("runProbeCommand error = nil, want the child's nonzero exit to surface")
	}
	if errors.Is(err, errProbeToolAbsent) || errors.Is(err, syscall.ETXTBSY) {
		t.Errorf("runProbeCommand error = %v, want the child's own exit failure, not a spawn verdict", err)
	}
	body, readErr := os.ReadFile(counter)
	if readErr != nil {
		t.Fatalf("reading the run counter: %v", readErr)
	}
	if runs := strings.Count(string(body), "run\n"); runs != 1 {
		t.Errorf("the script ran %d time(s), want exactly 1 — a nonzero exit must not be retried", runs)
	}
}
