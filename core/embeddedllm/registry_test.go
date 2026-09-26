package embeddedllm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The pins themselves
// ---------------------------------------------------------------------------

func TestValidateRegistry(t *testing.T) {
	if err := ValidateRegistry(); err != nil {
		t.Fatalf("ValidateRegistry: %v", err)
	}
}

// TestRegistryModelPinsMatchUpstreamLFS locks the model pins to the Hugging Face
// LFS metadata captured for ModelRevision. The LFS OID *is* the SHA256, so
// these values are the supply-chain anchor for ~7.3 GiB of weights (ADR-066
// D11). A change here must be a deliberate, CVE-reviewed pin bump.
func TestRegistryModelPinsMatchUpstreamLFS(t *testing.T) {
	cases := []struct {
		label string
		got   Asset
		file  string
		size  int64
		sha   string
		comp  Component
	}{
		{
			label: "PQ2_0 (6.71 GiB, default packing)",
			got:   regMustModelAsset(t, PackingPQ2_0),
			file:  "Ternary-Bonsai-2-27B-PQ2_0.gguf",
			size:  7206168928,
			sha:   "3907dc1658db1f78a9826bf8d5bcb8dc65db0d466388937af57f2294fae62ec1",
			comp:  ComponentModel,
		},
		{
			label: "PTQ1_0 (5.54 GiB, the downgrade packing)",
			got:   regMustModelAsset(t, PackingPTQ1_0),
			file:  "Ternary-Bonsai-2-27B-PTQ1_0.gguf",
			size:  5946648928,
			sha:   "53107f530aa52eb00912263ab1ee29bd199261c87cd7b4ad4ca1318c1fe33ee3",
			comp:  ComponentModel,
		},
		{
			label: "mmproj-Q8_0 (600 MiB vision projector)",
			got:   MMProjAsset(),
			file:  "Ternary-Bonsai-2-27B-mmproj-Q8_0.gguf",
			size:  629246976,
			sha:   "6807ede61d570bb86ba34b756a0fa109edc33668604de867c6ea6d8f1d631903",
			comp:  ComponentMMProj,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if tc.got.SHA256 != tc.sha {
				t.Errorf("SHA256 = %q, want the LFS OID %q", tc.got.SHA256, tc.sha)
			}
			if tc.got.SizeBytes != tc.size {
				t.Errorf("SizeBytes = %d, want %d", tc.got.SizeBytes, tc.size)
			}
			if tc.got.ArchiveName != tc.file {
				t.Errorf("ArchiveName = %q, want %q", tc.got.ArchiveName, tc.file)
			}
			if tc.got.Component != tc.comp {
				t.Errorf("Component = %q, want %q", tc.got.Component, tc.comp)
			}
			want := modelResolveBase + tc.file
			if tc.got.URL != want {
				t.Errorf("URL = %q, want %q", tc.got.URL, want)
			}
		})
	}
}

// TestRegistryRuntimePinsMatchUpstreamRelease locks every runtime and cudart
// pin to the GitHub REST per-asset `digest` field captured for RuntimeTag
// (`prism-b10735-842b188`, published 2026-09-24). These digests are the
// supply-chain anchor for a NATIVE BINARY c0wrk later spawns (ADR-066 D11,
// ASI04), so a change here must be a deliberate, CVE-reviewed pin bump — never
// a retyped or "refreshed" value.
//
// Keying the table by archive name (rather than by platform/backend) also pins
// the mapping: darwin-arm64 deliberately resolves Metal and CPU to the SAME
// archive, and windows-amd64 has no cuda-12.8 row at all. The release's other
// 6 assets (android-arm64, the iOS xcframework, macos-arm64-kleidiai, the two
// windows arm64 builds and their cuda-13.4 cudart) are out of scope (D9) and
// are not captured.
func TestRegistryRuntimePinsMatchUpstreamRelease(t *testing.T) {
	type pin struct {
		sha  string
		size int64
	}
	release := map[string]pin{
		"llama-" + RuntimeTag + "-bin-macos-x64.tar.gz":           {"c246b099d4c29cda21861333d2349477eba0c168a53a8ec3d770b4c300beda5e", 11552821},
		"llama-" + RuntimeTag + "-bin-macos-arm64.tar.gz":         {"a5c7a4d1f4f4ac7571aefdda5d1861e92ae196588dabb92525265d4c4ccbb7bb", 11509540},
		"llama-" + RuntimeTag + "-bin-ubuntu-x64.tar.gz":          {"f97eb58e365e4a2dadaf4c1eabbc45a4cc30a055d141abc10e533d88267f3008", 17380722},
		"llama-" + RuntimeTag + "-bin-ubuntu-arm64.tar.gz":        {"1fc79ccda103880adc33083920eda71f752af7996df576ea90889814baa04cc3", 13792856},
		"llama-" + RuntimeTag + "-bin-ubuntu-vulkan-x64.tar.gz":   {"2858b6a8f013736efbf429c99570e49162f0e382b3afd8fe7819937acaa3f107", 35515317},
		"llama-" + RuntimeTag + "-bin-ubuntu-vulkan-arm64.tar.gz": {"5146495910072dd3543eadebba125fecd7ae598c46851e139c2f011d43618f61", 28398701},
		"llama-" + RuntimeTag + "-bin-ubuntu-rocm-7.2-x64.tar.gz": {"47017372214be55a545aa31090be4e02e67e0d3c68d5733fc88a946412f02c3f", 139691397},
		"llama-" + RuntimeTag + "-bin-linux-cuda-12.4-x64.tar.gz": {"b58caa10e38ea2d3af419bc1c908f785b5f8756b07c98cb1752d50e1e985d4c6", 261907543},
		"llama-" + RuntimeTag + "-bin-linux-cuda-12.8-x64.tar.gz": {"5cbac5269804e4eb63676aeaec1ee7d4cff6e22b87da91de9b05795bf635998e", 168052249},
		"llama-" + RuntimeTag + "-bin-linux-cuda-13.3-x64.tar.gz": {"a84e28e22f108fb1f9b71bbde01e42fbe4626d2f0519b13394dd1ca0b91e9039", 147328305},
		"llama-" + RuntimeTag + "-bin-win-cpu-x64.zip":            {"f0b2b80710fc00a38dfd4c9345c97c928ea3e05114e51654b7dad617bc010615", 19030039},
		"llama-" + RuntimeTag + "-bin-win-vulkan-x64.zip":         {"dbc74e6eb3835a3d6d94e947bd0135d3786765117136dc2b797277ad8baf0787", 30982836},
		"llama-" + RuntimeTag + "-bin-win-hip-radeon-x64.zip":     {"24f7259c18a6b3e0f6afdd250bd5304e6bd7164b686327836d79b38f881112ee", 321722730},
		"llama-" + RuntimeTag + "-bin-win-cuda-12.4-x64.zip":      {"a6fe7fe4a5d72d729d593e5da3b47b30d24ffebd52e61b616f0150447e07f5dd", 254452483},
		"llama-" + RuntimeTag + "-bin-win-cuda-13.3-x64.zip":      {"80e950d34b03a5fc011a5d5aa74a07c7aef58dca0218b6b62ea8fc3177ff1655", 146021367},
		"cudart-llama-bin-win-cuda-12.4-x64.zip":                  {"8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6", 391443627},
		"cudart-llama-bin-win-cuda-13.3-x64.zip":                  {"1462a050eb4c684921ba51dcc4cc488a036674c3e73e9945ee705b854808d03e", 390970417},
	}

	seen := map[string]int{}
	check := func(what string, tables map[string]map[Backend]Asset) {
		for _, platform := range SupportedPlatforms() {
			for backend, a := range tables[platform] {
				where := fmt.Sprintf("%s %s/%s", what, platform, backend)
				want, ok := release[a.ArchiveName]
				if !ok {
					t.Errorf("%s: archive %q is not in the captured release table", where, a.ArchiveName)
					continue
				}
				seen[a.ArchiveName]++
				if a.SHA256 != want.sha {
					t.Errorf("%s: SHA256 = %q, want the release digest %q", where, a.SHA256, want.sha)
				}
				if a.SizeBytes != want.size {
					t.Errorf("%s: SizeBytes = %d, want the exact release size %d", where, a.SizeBytes, want.size)
				}
				if wantURL := runtimeReleaseBase + a.ArchiveName; a.URL != wantURL {
					t.Errorf("%s: URL = %q, want %q", where, a.URL, wantURL)
				}
			}
		}
	}
	check("runtime", runtimeAssets)
	check("cudart", cudartAssets)

	// A captured asset no registry entry references means the mapping moved
	// (or a pin was deleted), not that the release changed.
	for name := range release {
		if seen[name] == 0 {
			t.Errorf("release asset %q is captured but pinned by no registry entry", name)
		}
	}
	// The macOS arm64 build IS the Metal build, so both darwin-arm64 backends
	// share one archive and one digest.
	if got := seen["llama-"+RuntimeTag+"-bin-macos-arm64.tar.gz"]; got != 2 {
		t.Errorf("bin-macos-arm64 is pinned by %d entries, want 2 (Metal + CPU share one archive)", got)
	}
}

// TestRegistryPinsAreImmutableRefs asserts the URLs are pinned refs, never
// floating ones: a `/resolve/main/` model URL or a `/latest/download/` runtime
// URL would silently re-point at whatever upstream published last, defeating
// the whole pin (ADR-066 D11).
func TestRegistryPinsAreImmutableRefs(t *testing.T) {
	floating := []string{"/resolve/main/", "/latest/download/", "/releases/latest/", "/resolve/HEAD/"}
	for _, asset := range regAllAssets() {
		for _, bad := range floating {
			if strings.Contains(asset.URL, bad) {
				t.Errorf("%s: URL %q contains the floating ref %q", asset.Component, asset.URL, bad)
			}
		}
		if !strings.HasPrefix(asset.URL, "https://") {
			t.Errorf("%s: URL %q is not HTTPS", asset.Component, asset.URL)
		}
		switch asset.Component {
		case ComponentRuntime, ComponentCudart:
			if !strings.Contains(asset.URL, RuntimeTag) {
				t.Errorf("%s: URL %q does not carry the runtime pin %q", asset.Component, asset.URL, RuntimeTag)
			}
			if strings.Contains(asset.URL, "huggingface.co") {
				t.Errorf("%s: URL %q points at Hugging Face, want the GitHub release", asset.Component, asset.URL)
			}
		case ComponentModel, ComponentMMProj:
			if !strings.Contains(asset.URL, ModelRevision) {
				t.Errorf("%s: URL %q does not carry the HF revision pin %q", asset.Component, asset.URL, ModelRevision)
			}
			if strings.Contains(asset.URL, "github.com") {
				t.Errorf("%s: URL %q points at GitHub, want the HF resolve URL", asset.Component, asset.URL)
			}
		default:
			t.Errorf("asset %q has the unknown component %q", asset.URL, asset.Component)
		}
	}
	if RuntimeTag == "" || RuntimeTag == "main" || RuntimeTag == "latest" {
		t.Errorf("RuntimeTag = %q, want a concrete release tag", RuntimeTag)
	}
	if len(ModelRevision) != 40 {
		t.Errorf("ModelRevision = %q (%d chars), want a 40-char commit SHA", ModelRevision, len(ModelRevision))
	}
}

// ---------------------------------------------------------------------------
// Fail-closed lookups
// ---------------------------------------------------------------------------

// TestRegistryFailClosedOnUnpinnedPlatforms is the ASI04 guarantee at the
// registry layer: a platform with no pinned artifact yields no Asset and an
// ErrArtifactNotPinned set — never a neighbouring platform's bytes, and never
// an Asset with an empty digest that a lenient caller might download anyway.
func TestRegistryFailClosedOnUnpinnedPlatforms(t *testing.T) {
	unpinned := []string{
		"windows-arm64", // explicitly out of scope (ADR-066 D9)
		"freebsd-amd64",
		"linux-386",
		"darwin-arm",
		"android-arm64", // the release ships an asset for it; c0wrk does not pin it
		"",
		"DARWIN-ARM64",
	}
	backends := []Backend{BackendMetal, BackendCUDA124, BackendCUDA128, BackendCUDA133, BackendROCm, BackendVulkan, BackendCPU}

	for _, platform := range unpinned {
		for _, backend := range backends {
			if asset, ok := RuntimeAsset(platform, backend); ok {
				t.Errorf("RuntimeAsset(%q, %q) = %+v, want ok=false", platform, backend, asset)
			}
			if asset, ok := CudartAsset(platform, backend); ok {
				t.Errorf("CudartAsset(%q, %q) = %+v, want ok=false", platform, backend, asset)
			}
		}
		for _, packing := range append(SupportedPackings(), Packing("Q8_0"), Packing("")) {
			set, err := ArtifactSet(platform, BackendCPU, packing)
			if set != nil {
				t.Errorf("ArtifactSet(%q, cpu, %q) returned %d asset(s), want nil", platform, packing, len(set))
			}
			if !errors.Is(err, ErrArtifactNotPinned) {
				t.Errorf("ArtifactSet(%q, cpu, %q) err = %v, want ErrArtifactNotPinned", platform, packing, err)
			}
		}
		if IsSupportedPlatform(platform) {
			t.Errorf("IsSupportedPlatform(%q) = true, want false", platform)
		}
	}
}

// TestRegistryFailClosedOnUnknownBackendAndPacking covers the other two axes:
// an invented backend or packing must refuse, not approximate.
func TestRegistryFailClosedOnUnknownBackendAndPacking(t *testing.T) {
	for _, backend := range []Backend{Backend(""), Backend("cuda"), Backend("CUDA-12.4"), Backend("metal-2"), Backend("cuda-13.4")} {
		for _, platform := range SupportedPlatforms() {
			if asset, ok := RuntimeAsset(platform, backend); ok {
				t.Errorf("RuntimeAsset(%q, %q) = %+v, want ok=false", platform, backend, asset)
			}
			if _, err := ArtifactSet(platform, backend, PackingPQ2_0); !errors.Is(err, ErrArtifactNotPinned) {
				t.Errorf("ArtifactSet(%q, %q) err = %v, want ErrArtifactNotPinned", platform, backend, err)
			}
		}
	}
	for _, packing := range []Packing{Packing(""), Packing("pq2_0"), Packing("Q2_0"), Packing("F16"), Packing("PQ2_0 ")} {
		if asset, ok := ModelAsset(packing); ok {
			t.Errorf("ModelAsset(%q) = %+v, want ok=false", packing, asset)
		}
		if _, err := ArtifactSet(PlatformDarwinARM64, BackendMetal, packing); !errors.Is(err, ErrArtifactNotPinned) {
			t.Errorf("ArtifactSet(darwin-arm64, metal, %q) err = %v, want ErrArtifactNotPinned", packing, err)
		}
	}
}

// TestRegistryWindowsCUDA128Gap documents a real hole in the pinned release,
// verified against its 23 assets: prism-b10735-842b188 ships win-cuda-12.4-x64,
// win-cuda-13.3-x64 and win-cuda-13.4-arm64 — but NO windows cuda-12.8 archive
// and no cuda-12.8 cudart, while linux DOES have bin-linux-cuda-12.8-x64.
//
// Resolution must therefore never select (windows-amd64, cuda-12.8): it has to
// clamp to a pinned CUDA tag or fall back to another backend. This test fails
// if the gap is closed upstream (prompting a pin) or if someone papers over it
// by aliasing 12.8 to a different archive.
func TestRegistryWindowsCUDA128Gap(t *testing.T) {
	if asset, ok := RuntimeAsset(PlatformWindowsAMD64, BackendCUDA128); ok {
		t.Errorf("windows-amd64 + cuda-12.8 resolved to %+v; this pin has no such archive", asset)
	}
	if asset, ok := CudartAsset(PlatformWindowsAMD64, BackendCUDA128); ok {
		t.Errorf("windows-amd64 + cuda-12.8 has a cudart %+v; no such archive exists", asset)
	}
	if _, err := ArtifactSet(PlatformWindowsAMD64, BackendCUDA128, PackingPQ2_0); !errors.Is(err, ErrArtifactNotPinned) {
		t.Errorf("ArtifactSet(windows-amd64, cuda-12.8) err = %v, want ErrArtifactNotPinned", err)
	}
	// The linux 12.8 archive DOES exist, so the gap is windows-specific.
	if _, ok := RuntimeAsset(PlatformLinuxAMD64, BackendCUDA128); !ok {
		t.Error("linux-amd64 + cuda-12.8 is missing; the gap is supposed to be windows-only")
	}
	// Windows CUDA that IS pinned must still resolve, with its paired cudart.
	for _, backend := range []Backend{BackendCUDA124, BackendCUDA133} {
		if _, ok := RuntimeAsset(PlatformWindowsAMD64, backend); !ok {
			t.Errorf("windows-amd64 + %s runtime is missing", backend)
		}
		if _, ok := CudartAsset(PlatformWindowsAMD64, backend); !ok {
			t.Errorf("windows-amd64 + %s cudart is missing", backend)
		}
	}
}

// TestRegistryMetalIsAppleSiliconOnly encodes D1: Metal is darwin/arm64, and an
// Intel Mac has no GPU backend to pin (it runs the CPU build with -ngl 0).
func TestRegistryMetalIsAppleSiliconOnly(t *testing.T) {
	if _, ok := RuntimeAsset(PlatformDarwinARM64, BackendMetal); !ok {
		t.Error("darwin-arm64 + metal is not pinned; Apple Silicon is the primary target")
	}
	for _, platform := range []string{PlatformDarwinAMD64, PlatformLinuxAMD64, PlatformLinuxARM64, PlatformWindowsAMD64} {
		if asset, ok := RuntimeAsset(platform, BackendMetal); ok {
			t.Errorf("RuntimeAsset(%q, metal) = %+v, want ok=false (Metal is darwin/arm64 only)", platform, asset)
		}
	}
	// The Intel Mac still has an installable CPU path.
	cpu, ok := RuntimeAsset(PlatformDarwinAMD64, BackendCPU)
	if !ok {
		t.Fatal("darwin-amd64 + cpu is not pinned; an Intel Mac could not install at all")
	}
	if !strings.Contains(cpu.ArchiveName, "macos-x64") {
		t.Errorf("darwin-amd64 cpu archive = %q, want the macos-x64 build", cpu.ArchiveName)
	}
}

// ---------------------------------------------------------------------------
// Artifact sets
// ---------------------------------------------------------------------------

// TestArtifactSetComposition walks the whole pinned matrix and asserts the set
// shape: the runtime first, a paired cudart only where one exists, then the
// weights, then the always-present vision projector.
func TestArtifactSetComposition(t *testing.T) {
	for _, platform := range SupportedPlatforms() {
		for _, backend := range regAllBackends() {
			runtime, runtimeOK := RuntimeAsset(platform, backend)
			for _, packing := range SupportedPackings() {
				set, err := ArtifactSet(platform, backend, packing)
				if !runtimeOK {
					if !errors.Is(err, ErrArtifactNotPinned) {
						t.Errorf("ArtifactSet(%q, %q, %q) err = %v, want ErrArtifactNotPinned", platform, backend, packing, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("ArtifactSet(%q, %q, %q): %v", platform, backend, packing, err)
					continue
				}
				label := fmt.Sprintf("%s/%s/%s", platform, backend, packing)

				if set[0].Component != ComponentRuntime || set[0] != runtime {
					t.Errorf("%s: first asset = %+v, want the pinned runtime", label, set[0])
				}
				if last := set[len(set)-1]; last.Component != ComponentMMProj {
					t.Errorf("%s: last asset is %q, want %q (the projector is always installed)", label, last.Component, ComponentMMProj)
				}
				if last := set[len(set)-1]; last != MMProjAsset() {
					t.Errorf("%s: mmproj = %+v, want the single pinned projector", label, last)
				}
				model, ok := ModelAsset(packing)
				if !ok {
					t.Fatalf("%s: packing %q is supported but not pinned", label, packing)
				}
				if got := set[len(set)-2]; got != model {
					t.Errorf("%s: second-to-last asset = %+v, want the %q weights", label, got, packing)
				}

				cudart, cudartOK := CudartAsset(platform, backend)
				if cudartOK {
					if len(set) != 4 {
						t.Fatalf("%s: set has %d assets, want 4 (runtime + cudart + model + mmproj)", label, len(set))
					}
					if set[1] != cudart {
						t.Errorf("%s: second asset = %+v, want the paired cudart %+v", label, set[1], cudart)
					}
				} else if len(set) != 3 {
					t.Fatalf("%s: set has %d assets, want 3 (runtime + model + mmproj)", label, len(set))
				}

				// Every component appears exactly once: no double-download and
				// no missing piece.
				seen := map[Component]int{}
				for _, a := range set {
					seen[a.Component]++
				}
				for comp, n := range seen {
					if n != 1 {
						t.Errorf("%s: component %q appears %d times, want 1", label, comp, n)
					}
				}
				for _, a := range set {
					if !validSHA256Hex(a.SHA256) {
						t.Errorf("%s: %s has an invalid digest %q", label, a.Component, a.SHA256)
					}
					if a.SizeBytes <= 0 {
						t.Errorf("%s: %s has a non-positive size", label, a.Component)
					}
				}
			}
		}
	}
}

// TestArtifactSetSizesAreArtifactSized proves the disk guard has real numbers
// to work with. The totals are asserted in exact bytes — a GiB "band" would
// hide a swapped archive — and the documented weights-only footprint
// (~7.3 GiB PQ2_0, ~6.1 GiB PTQ1_0) falls straight out of them.
func TestArtifactSetSizesAreArtifactSized(t *testing.T) {
	const (
		pq2_0  = int64(7206168928)
		ptq1_0 = int64(5946648928)
		mmproj = int64(629246976)
	)
	cases := []struct {
		platform string
		backend  Backend
		packing  Packing
		runtime  int64
		cudart   int64
	}{
		{PlatformDarwinARM64, BackendMetal, PackingPQ2_0, 11509540, 0},
		{PlatformDarwinARM64, BackendMetal, PackingPTQ1_0, 11509540, 0},
		{PlatformDarwinAMD64, BackendCPU, PackingPQ2_0, 11552821, 0},
		// The largest install: Windows CUDA = runtime + cudart + weights + projector.
		{PlatformWindowsAMD64, BackendCUDA124, PackingPQ2_0, 254452483, 391443627},
		{PlatformWindowsAMD64, BackendCUDA133, PackingPQ2_0, 146021367, 390970417},
		// The heaviest linux runtime still needs no cudart.
		{PlatformLinuxAMD64, BackendCUDA124, PackingPQ2_0, 261907543, 0},
		{PlatformLinuxARM64, BackendVulkan, PackingPTQ1_0, 28398701, 0},
	}
	for _, tc := range cases {
		set, err := ArtifactSet(tc.platform, tc.backend, tc.packing)
		if err != nil {
			t.Fatalf("ArtifactSet(%q, %q, %q): %v", tc.platform, tc.backend, tc.packing, err)
		}
		label := fmt.Sprintf("%s/%s/%s", tc.platform, tc.backend, tc.packing)

		weights := pq2_0
		if tc.packing == PackingPTQ1_0 {
			weights = ptq1_0
		}
		want := tc.runtime + tc.cudart + weights + mmproj
		if got := TotalBytes(set); got != want {
			t.Errorf("%s: TotalBytes = %d, want %d (runtime %d + cudart %d + weights %d + mmproj %d)",
				label, got, want, tc.runtime, tc.cudart, weights, mmproj)
		}
		if got := regSumSizes(set); got != want {
			t.Errorf("%s: independent sum = %d, want %d", label, got, want)
		}
		if want <= 0 {
			t.Fatalf("%s: computed a non-positive total", label)
		}
	}

	// PTQ1_0 is always the smaller download, on every provisionable pair.
	for _, platform := range SupportedPlatforms() {
		for _, backend := range regAllBackends() {
			big, err := ArtifactSet(platform, backend, PackingPQ2_0)
			if err != nil {
				continue
			}
			small, err := ArtifactSet(platform, backend, PackingPTQ1_0)
			if err != nil {
				t.Errorf("%s/%s: PQ2_0 resolved but PTQ1_0 did not", platform, backend)
				continue
			}
			if TotalBytes(small) >= TotalBytes(big) {
				t.Errorf("%s/%s: PTQ1_0 (%d B) is not smaller than PQ2_0 (%d B)",
					platform, backend, TotalBytes(small), TotalBytes(big))
			}
			if diff := TotalBytes(big) - TotalBytes(small); diff != pq2_0-ptq1_0 {
				t.Errorf("%s/%s: packing delta = %d B, want %d B (only the weights may differ)",
					platform, backend, diff, pq2_0-ptq1_0)
			}
		}
	}
}

// TestCudartOnlyForWindowsCUDA pins the D9 rule: the paired DLL archive exists
// for Windows CUDA builds only. Linux CUDA links the system CUDA runtime.
func TestCudartOnlyForWindowsCUDA(t *testing.T) {
	wantCudart := map[string]bool{
		"windows-amd64/cuda-12.4": true,
		"windows-amd64/cuda-13.3": true,
	}
	for _, platform := range SupportedPlatforms() {
		for _, backend := range regAllBackends() {
			asset, ok := CudartAsset(platform, backend)
			key := platform + "/" + string(backend)
			if wantCudart[key] != ok {
				t.Errorf("CudartAsset(%q, %q) ok = %v, want %v", platform, backend, ok, wantCudart[key])
				continue
			}
			if !ok {
				continue
			}
			if asset.Component != ComponentCudart {
				t.Errorf("%s: component = %q, want %q", key, asset.Component, ComponentCudart)
			}
			if !strings.HasPrefix(asset.ArchiveName, "cudart-llama-bin-win-cuda-") {
				t.Errorf("%s: archive = %q, want a cudart-llama-bin-win-cuda-* name", key, asset.ArchiveName)
			}
			// A cudart may only exist where its runtime exists, otherwise the
			// set would download a DLL bundle for a server it cannot run.
			if _, runtimeOK := RuntimeAsset(platform, backend); !runtimeOK {
				t.Errorf("%s: cudart pinned without a runtime", key)
			}
		}
	}
}

// TestEveryRegistryEntryIsReachable guards the accessors' fail-closed filter
// from accidentally hiding a pinned entry (which would silently make a
// platform uninstallable).
func TestEveryRegistryEntryIsReachable(t *testing.T) {
	for platform, byBackend := range runtimeAssets {
		for backend, want := range byBackend {
			got, ok := RuntimeAsset(platform, backend)
			if !ok {
				t.Errorf("RuntimeAsset(%q, %q) = false, but the table pins %+v", platform, backend, want)
				continue
			}
			if got != want {
				t.Errorf("RuntimeAsset(%q, %q) = %+v, want %+v", platform, backend, got, want)
			}
			set, err := ArtifactSet(platform, backend, PackingPQ2_0)
			if err != nil {
				t.Errorf("ArtifactSet(%q, %q, PQ2_0): %v", platform, backend, err)
				continue
			}
			if set[0] != want {
				t.Errorf("ArtifactSet(%q, %q) first asset = %+v, want %+v", platform, backend, set[0], want)
			}
		}
	}
	for platform, byBackend := range cudartAssets {
		for backend, want := range byBackend {
			got, ok := CudartAsset(platform, backend)
			if !ok || got != want {
				t.Errorf("CudartAsset(%q, %q) = (%+v, %v), want (%+v, true)", platform, backend, got, ok, want)
			}
		}
	}
	for _, packing := range SupportedPackings() {
		if _, ok := ModelAsset(packing); !ok {
			t.Errorf("ModelAsset(%q) = false, but the packing is listed as supported", packing)
		}
	}
}

// TestRegistryCoverageCounts pins the size of the matrix so an accidental
// deletion (or an unnoticed upstream addition) shows up in review.
func TestRegistryCoverageCounts(t *testing.T) {
	var runtimes, cudarts int
	for _, byBackend := range runtimeAssets {
		runtimes += len(byBackend)
	}
	for _, byBackend := range cudartAssets {
		cudarts += len(byBackend)
	}
	if runtimes != 16 {
		t.Errorf("registry pins %d runtime archives, want 16", runtimes)
	}
	if cudarts != 2 {
		t.Errorf("registry pins %d cudart archives, want 2 (win cuda-12.4 + cuda-13.3)", cudarts)
	}
	if got := len(SupportedPlatforms()); got != 5 {
		t.Errorf("SupportedPlatforms has %d entries, want 5", got)
	}
	if got := len(SupportedPackings()); got != 2 {
		t.Errorf("SupportedPackings has %d entries, want 2", got)
	}
}

func TestSupportedPlatformsAreSortedUniqueAndToolmanagerShaped(t *testing.T) {
	got := SupportedPlatforms()
	seen := map[string]bool{}
	for i, platform := range got {
		if seen[platform] {
			t.Errorf("platform %q is listed twice", platform)
		}
		seen[platform] = true
		if i > 0 && platform < got[i-1] {
			t.Errorf("SupportedPlatforms is not sorted: %q follows %q", platform, got[i-1])
		}
		goos, goarch, found := strings.Cut(platform, "-")
		if !found || goos == "" || goarch == "" || strings.Contains(goarch, "-") {
			t.Errorf("platform %q is not in the toolmanager.Platform() \"<goos>-<goarch>\" shape", platform)
		}
	}
	// Mutating the returned slice must not corrupt the registry.
	got[0] = "tampered"
	if SupportedPlatforms()[0] == "tampered" {
		t.Error("SupportedPlatforms returned a reference to the registry's own slice")
	}

	// The five keys must be exactly the goos/goarch pairs c0wrk supports.
	want := map[string]bool{
		"darwin-amd64": true, "darwin-arm64": true,
		"linux-amd64": true, "linux-arm64": true,
		"windows-amd64": true,
	}
	for platform := range seen {
		if !want[platform] {
			t.Errorf("unexpected platform key %q", platform)
		}
	}
	for platform := range want {
		if !seen[platform] {
			t.Errorf("platform key %q is missing", platform)
		}
	}
	if IsSupportedPlatform("windows-arm64") {
		t.Error("windows-arm64 must stay out of scope (ADR-066 D9)")
	}
}

// ---------------------------------------------------------------------------
// The validator has teeth
// ---------------------------------------------------------------------------

// TestValidateRegistryDetectsBrokenPins proves ValidateRegistry is not vacuous:
// each mutation of a pin table must be caught. Without this, a validator that
// always returned nil would look identical to a correct one.
//
// The mutations are applied to a LOCAL fixture, never to the package-level
// registry: those maps are shared by every other test in the package (Resolve
// consults them through pinnedRegistry), so a leaked mutation would silently
// change what other tests resolve to.
func TestValidateRegistryDetectsBrokenPins(t *testing.T) {
	if err := ValidateRegistry(); err != nil {
		t.Fatalf("the real registry must validate cleanly before this test can mean anything: %v", err)
	}

	// The fixture is assembled from the real pins (read-only), so a mutation
	// is the only difference between "valid" and "invalid".
	newTables := func() regTables {
		tb := regTables{
			runtimes:  map[string]map[Backend]Asset{},
			cudarts:   map[string]map[Backend]Asset{},
			models:    map[Packing]Asset{},
			mmproj:    MMProjAsset(),
			platforms: []string{PlatformDarwinARM64, PlatformWindowsAMD64},
		}
		tb.putRuntime(PlatformDarwinARM64, BackendMetal, regMustRuntime(t, PlatformDarwinARM64, BackendMetal))
		tb.putRuntime(PlatformDarwinARM64, BackendCPU, regMustRuntime(t, PlatformDarwinARM64, BackendCPU))
		tb.putRuntime(PlatformWindowsAMD64, BackendCPU, regMustRuntime(t, PlatformWindowsAMD64, BackendCPU))
		tb.putRuntime(PlatformWindowsAMD64, BackendCUDA124, regMustRuntime(t, PlatformWindowsAMD64, BackendCUDA124))
		cudart, ok := CudartAsset(PlatformWindowsAMD64, BackendCUDA124)
		if !ok {
			t.Fatal("windows-amd64 + cuda-12.4 has no pinned cudart")
		}
		tb.putCudart(PlatformWindowsAMD64, BackendCUDA124, cudart)
		for _, packing := range SupportedPackings() {
			model, ok := ModelAsset(packing)
			if !ok {
				t.Fatalf("packing %q is not pinned", packing)
			}
			tb.models[packing] = model
		}
		return tb
	}

	// Sanity: the untouched fixture validates.
	if err := newTables().validate(); err != nil {
		t.Fatalf("the fixture itself does not validate: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*regTables)
		wantSub string
	}{
		{
			name:    "blank model digest",
			mutate:  func(tb *regTables) { a := tb.models[PackingPQ2_0]; a.SHA256 = ""; tb.models[PackingPQ2_0] = a },
			wantSub: "model PQ2_0",
		},
		{
			name:    "short model digest",
			mutate:  func(tb *regTables) { a := tb.models[PackingPQ2_0]; a.SHA256 = "abc123"; tb.models[PackingPQ2_0] = a },
			wantSub: "model PQ2_0",
		},
		{
			name: "uppercase model digest",
			mutate: func(tb *regTables) {
				a := tb.models[PackingPQ2_0]
				a.SHA256 = strings.ToUpper(a.SHA256)
				tb.models[PackingPQ2_0] = a
			},
			wantSub: "model PQ2_0",
		},
		{
			name:    "zero model size",
			mutate:  func(tb *regTables) { a := tb.models[PackingPQ2_0]; a.SizeBytes = 0; tb.models[PackingPQ2_0] = a },
			wantSub: "model PQ2_0",
		},
		{
			name:    "negative model size",
			mutate:  func(tb *regTables) { a := tb.models[PackingPTQ1_0]; a.SizeBytes = -5; tb.models[PackingPTQ1_0] = a },
			wantSub: "model PTQ1_0",
		},
		{
			name:    "empty model URL",
			mutate:  func(tb *regTables) { a := tb.models[PackingPQ2_0]; a.URL = ""; tb.models[PackingPQ2_0] = a },
			wantSub: "model PQ2_0",
		},
		{
			name: "model URL off the pinned revision",
			mutate: func(tb *regTables) {
				a := tb.models[PackingPQ2_0]
				a.URL = "https://huggingface.co/x/y/resolve/main/f.gguf"
				tb.models[PackingPQ2_0] = a
			},
			wantSub: "model PQ2_0",
		},
		{
			name:    "empty model archive name",
			mutate:  func(tb *regTables) { a := tb.models[PackingPQ2_0]; a.ArchiveName = ""; tb.models[PackingPQ2_0] = a },
			wantSub: "model PQ2_0",
		},
		{
			name: "model archive name with a path",
			mutate: func(tb *regTables) {
				a := tb.models[PackingPQ2_0]
				a.ArchiveName = "sub/dir/f.gguf"
				a.URL = modelResolveBase + a.ArchiveName
				tb.models[PackingPQ2_0] = a
			},
			wantSub: "not a plain file name",
		},
		{
			name: "model with the wrong component",
			mutate: func(tb *regTables) {
				a := tb.models[PackingPQ2_0]
				a.Component = ComponentMMProj
				tb.models[PackingPQ2_0] = a
			},
			wantSub: "model PQ2_0",
		},
		{
			name: "blank runtime digest",
			mutate: func(tb *regTables) {
				a := tb.runtimes[PlatformDarwinARM64][BackendMetal]
				a.SHA256 = ""
				tb.putRuntime(PlatformDarwinARM64, BackendMetal, a)
			},
			wantSub: "runtime darwin-arm64/metal",
		},
		{
			name: "runtime URL off the pinned release",
			mutate: func(tb *regTables) {
				a := tb.runtimes[PlatformDarwinARM64][BackendCPU]
				a.URL = "https://github.com/x/y/releases/latest/a.tar.gz"
				tb.putRuntime(PlatformDarwinARM64, BackendCPU, a)
			},
			wantSub: "runtime darwin-arm64/cpu",
		},
		{
			name: "runtime URL mismatching its archive name",
			mutate: func(tb *regTables) {
				a := tb.runtimes[PlatformDarwinARM64][BackendCPU]
				a.ArchiveName = "something-else.tar.gz"
				tb.putRuntime(PlatformDarwinARM64, BackendCPU, a)
			},
			wantSub: "runtime darwin-arm64/cpu",
		},
		{
			name: "runtime with the wrong component",
			mutate: func(tb *regTables) {
				a := tb.runtimes[PlatformDarwinARM64][BackendCPU]
				a.Component = ComponentModel
				tb.putRuntime(PlatformDarwinARM64, BackendCPU, a)
			},
			wantSub: "runtime darwin-arm64/cpu",
		},
		{
			name:    "missing CPU fallback for a platform",
			mutate:  func(tb *regTables) { delete(tb.runtimes[PlatformDarwinARM64], BackendCPU) },
			wantSub: "no CPU fallback runtime pinned",
		},
		{
			name:    "cudart orphaned from its runtime",
			mutate:  func(tb *regTables) { delete(tb.runtimes[PlatformWindowsAMD64], BackendCUDA124) },
			wantSub: "cudart pinned without a runtime",
		},
		{
			name: "cudart with a blank digest",
			mutate: func(tb *regTables) {
				a := tb.cudarts[PlatformWindowsAMD64][BackendCUDA124]
				a.SHA256 = ""
				tb.putCudart(PlatformWindowsAMD64, BackendCUDA124, a)
			},
			wantSub: "cudart windows-amd64/cuda-12.4",
		},
		{
			name: "cudart with the wrong component",
			mutate: func(tb *regTables) {
				a := tb.cudarts[PlatformWindowsAMD64][BackendCUDA124]
				a.Component = ComponentRuntime
				tb.putCudart(PlatformWindowsAMD64, BackendCUDA124, a)
			},
			wantSub: "cudart windows-amd64/cuda-12.4",
		},
		{
			name:    "mmproj with a blank digest",
			mutate:  func(tb *regTables) { tb.mmproj.SHA256 = "" },
			wantSub: "mmproj",
		},
		{
			name:    "mmproj with the wrong component",
			mutate:  func(tb *regTables) { tb.mmproj.Component = ComponentModel },
			wantSub: "mmproj",
		},
		{
			name:    "mmproj URL off the pinned revision",
			mutate:  func(tb *regTables) { tb.mmproj.URL = "https://huggingface.co/x/y/resolve/main/mmproj.gguf" },
			wantSub: "mmproj",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTables()
			tc.mutate(&tb)
			err := tb.validate()
			if err == nil {
				t.Fatalf("validateTables accepted a broken pin table (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}

	// The shared registry must be untouched by all of the above.
	if err := ValidateRegistry(); err != nil {
		t.Fatalf("the package registry was mutated by this test: %v", err)
	}
	if _, ok := RuntimeAsset(PlatformWindowsAMD64, BackendCUDA124); !ok {
		t.Fatal("windows-amd64 + cuda-12.4 vanished from the shared registry; a test leaked a mutation")
	}
}

// regTables is a self-contained copy of the registry's pin tables, so the
// validator can be tested against deliberately broken pins without touching
// the package-level maps.
type regTables struct {
	runtimes  map[string]map[Backend]Asset
	cudarts   map[string]map[Backend]Asset
	models    map[Packing]Asset
	mmproj    Asset
	platforms []string
}

func (tb regTables) putRuntime(platform string, backend Backend, a Asset) {
	if tb.runtimes[platform] == nil {
		tb.runtimes[platform] = map[Backend]Asset{}
	}
	tb.runtimes[platform][backend] = a
}

func (tb regTables) putCudart(platform string, backend Backend, a Asset) {
	if tb.cudarts[platform] == nil {
		tb.cudarts[platform] = map[Backend]Asset{}
	}
	tb.cudarts[platform][backend] = a
}

func (tb regTables) validate() error {
	return validateTables(tb.runtimes, tb.cudarts, tb.models, tb.mmproj, tb.platforms)
}

func TestValidSHA256Hex(t *testing.T) {
	valid := "3907dc1658db1f78a9826bf8d5bcb8dc65db0d466388937af57f2294fae62ec1"
	cases := []struct {
		in   string
		want bool
	}{
		{valid, true},
		{strings.Repeat("0", 64), true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{strings.Repeat("0", 63), false},
		{strings.Repeat("0", 65), false},
		{strings.ToUpper(valid), false},
		{strings.Repeat("z", 64), false},
		{strings.Repeat("g", 64), false},
		{"sha256:" + valid, false},
	}
	for _, tc := range cases {
		if got := validSHA256Hex(tc.in); got != tc.want {
			t.Errorf("validSHA256Hex(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func regMustModelAsset(t *testing.T, packing Packing) Asset {
	t.Helper()
	asset, ok := ModelAsset(packing)
	if !ok {
		t.Fatalf("packing %q is not pinned", packing)
	}
	return asset
}

// regMustRuntime reads a pinned runtime asset without mutating the registry.
func regMustRuntime(t *testing.T, platform string, backend Backend) Asset {
	t.Helper()
	asset, ok := RuntimeAsset(platform, backend)
	if !ok {
		t.Fatalf("platform %q backend %q has no pinned runtime", platform, backend)
	}
	return asset
}

func regAllBackends() []Backend {
	return []Backend{BackendMetal, BackendCUDA124, BackendCUDA128, BackendCUDA133, BackendROCm, BackendVulkan, BackendCPU}
}

// regAllAssets flattens every pinned asset in the registry.
func regAllAssets() []Asset {
	out := make([]Asset, 0, 24)
	for _, byBackend := range runtimeAssets {
		for _, a := range byBackend {
			out = append(out, a)
		}
	}
	for _, byBackend := range cudartAssets {
		for _, a := range byBackend {
			out = append(out, a)
		}
	}
	for _, a := range modelAssets {
		out = append(out, a)
	}
	out = append(out, mmprojAsset)
	return out
}

func regSumSizes(assets []Asset) int64 {
	var total int64
	for _, a := range assets {
		total += a.SizeBytes
	}
	return total
}
