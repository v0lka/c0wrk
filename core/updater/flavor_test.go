package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeCUDAProviderLib creates the CUDA provider library file in dir,
// simulating the footprint `make fetch-onnx-gpu` leaves in an install tree.
func writeCUDAProviderLib(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, cudaProviderLibrary), []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", cudaProviderLibrary, err)
	}
}

// TestFlavorFor_LinuxAMD64_NoLibrary covers the acceptance criterion
// "linux/amd64 without the library → FlavorCPU": a linux/amd64 install tree
// without the CUDA provider library is the default CPU flavor.
func TestFlavorFor_LinuxAMD64_NoLibrary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "c0wrk-desktop")
	if got := flavorFor("linux", "amd64", exe); got != FlavorCPU {
		t.Fatalf("flavorFor(linux/amd64, no lib) = %q, want %q", got, FlavorCPU)
	}
}

// TestFlavorFor_LinuxAMD64_WithLibrary covers the acceptance criterion
// "linux/amd64 with the library → FlavorCUDA13": the provider library's
// presence next to the binary is the sole install-time CUDA signal.
func TestFlavorFor_LinuxAMD64_WithLibrary(t *testing.T) {
	dir := t.TempDir()
	writeCUDAProviderLib(t, dir)
	exe := filepath.Join(dir, "c0wrk-desktop")
	if got := flavorFor("linux", "amd64", exe); got != FlavorCUDA13 {
		t.Fatalf("flavorFor(linux/amd64, lib present) = %q, want %q", got, FlavorCUDA13)
	}
}

// TestFlavorFor_NonLinuxPlatforms covers the acceptance criterion
// "non-linux/amd64 → always FlavorCPU": the GPU flavor is published for
// linux/amd64 only, so every other platform — with or without a stray
// provider library sitting next to the binary — resolves to CPU.
func TestFlavorFor_NonLinuxPlatforms(t *testing.T) {
	dir := t.TempDir()
	writeCUDAProviderLib(t, dir)
	exe := filepath.Join(dir, "c0wrk-desktop")

	// The library is present in all cases below: only the platform gate can
	// explain a FlavorCPU answer.
	for _, p := range [][2]string{
		{"darwin", "arm64"},
		{"darwin", "amd64"},
		{"linux", "arm64"},
		{"windows", "amd64"},
	} {
		if got := flavorFor(p[0], p[1], exe); got != FlavorCPU {
			t.Errorf("flavorFor(%s/%s, lib present) = %q, want %q", p[0], p[1], got, FlavorCPU)
		}
	}
}

// TestFlavorFor_EmptyExePath covers the degenerate input: an unresolvable
// executable path (empty string) resolves to the CPU flavor instead of
// panicking or probing the working directory.
func TestFlavorFor_EmptyExePath(t *testing.T) {
	if got := flavorFor("linux", "amd64", ""); got != FlavorCPU {
		t.Fatalf("flavorFor(linux/amd64, empty exe) = %q, want %q", got, FlavorCPU)
	}
}

// TestFlavorFor_StatError covers the acceptance criterion "stat error →
// FlavorCPU, not a panic": pointing the executable at a directory that
// cannot be stat-ed (a path whose parent is a regular file) must degrade
// to the CPU flavor. os.Stat of libonnxruntime_providers_cuda.so under a
// non-existent parent returns ENOTDIR, exercising the error branch of the
// probe.
func TestFlavorFor_StatError(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(regular, []byte("file"), 0o644); err != nil {
		t.Fatalf("creating regular file: %v", err)
	}
	// ".../not-a-dir/c0wrk-desktop" cannot exist — stat'ing the provider
	// library under it fails with ENOTDIR, never with success.
	exe := filepath.Join(regular, "c0wrk-desktop")
	if got := flavorFor("linux", "amd64", exe); got != FlavorCPU {
		t.Fatalf("flavorFor(linux/amd64, unstatable exe dir) = %q, want %q", got, FlavorCPU)
	}
}

// TestCurrentFlavor_Smoke exercises the production entry point on the host
// platform. On linux/amd64 the answer must track the install tree state of
// the running test binary (which never ships the provider library); on any
// other platform the answer is unconditionally FlavorCPU.
func TestCurrentFlavor_Smoke(t *testing.T) {
	got := CurrentFlavor()
	switch {
	case runtime.GOOS != "linux" || runtime.GOARCH != "amd64":
		if got != FlavorCPU {
			t.Fatalf("CurrentFlavor() on %s/%s = %q, want %q", runtime.GOOS, runtime.GOARCH, got, FlavorCPU)
		}
	default:
		// linux/amd64 host: the test binary's directory holds no CUDA
		// provider library, so the flavor must be CPU.
		if got != FlavorCPU {
			t.Fatalf("CurrentFlavor() on linux/amd64 (no lib next to test binary) = %q, want %q", got, FlavorCPU)
		}
	}
}
