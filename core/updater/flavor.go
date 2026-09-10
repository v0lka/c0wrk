package updater

import (
	"os"
	"path/filepath"
	"runtime"
)

// Flavor identifies which ONNX Runtime packaging a running installation
// carries: the default CPU flavor or the opt-in CUDA 13 flavor. The flavor
// does NOT live in the binary — the executable is byte-identical across
// flavors — but in the install tree: `make fetch-onnx-gpu` (ADR-036) drops
// the CUDA provider library next to the binary, and that file's presence is
// the only install-time signal of a GPU-flavored tree.
//
// The updater needs the flavor to pick the correct update payload: a CPU
// tree must keep receiving CPU libraries, and a CUDA tree must not be
// silently downgraded by a CPU-flavored update archive.
type Flavor string

const (
	// FlavorCPU is the default installation flavor: ONNX Runtime CPU
	// execution provider only, no GPU libraries in the install tree.
	FlavorCPU Flavor = "cpu"

	// FlavorCUDA13 is the opt-in GPU flavor (ADR-036): a CUDA 13-flavored
	// ONNX Runtime with libonnxruntime_providers_cuda.so installed next to
	// the binary. One CUDA major is pinned deliberately (see ADR-036
	// "Packaging: separate opt-in target, cuda13 flavor only") — a cuda12
	// flavor is explicitly not supported.
	FlavorCUDA13 Flavor = "cuda13"
)

// cudaProviderLibrary is the file whose presence in the executable's
// directory marks a CUDA-flavored install tree. It matches ONNX_GPU_CUDA_LIB
// in the Makefile's fetch-onnx-gpu target and the library ONNX Runtime
// dlopens for the CUDA execution provider (symmetric to the rule sp4rk's
// embedding package documents for GPU-enabled LibraryPath builds).
const cudaProviderLibrary = "libonnxruntime_providers_cuda.so"

// CurrentFlavor determines the flavor of the running installation by
// inspecting the install tree (the directory containing the executable).
//
// It reports FlavorCUDA13 only when every condition holds:
//   - the binary runs on linux/amd64 — the only platform for which a GPU
//     flavor is published (the Makefile's fetch-onnx-gpu target refuses
//     every other platform), and
//   - libonnxruntime_providers_cuda.so exists next to the executable —
//     the install-time footprint `make fetch-onnx-gpu` leaves behind.
//
// Everything else — other platforms, a CPU install tree, or any error
// resolving/stat-ing the executable path — reports FlavorCPU. The function
// is therefore fail-closed: it never panics and never returns an error,
// because a wrong flavor guess must degrade to the safe default (CPU)
// rather than break the update flow.
func CurrentFlavor() Flavor {
	exePath, err := os.Executable()
	if err != nil {
		return FlavorCPU
	}
	return flavorFor(runtime.GOOS, runtime.GOARCH, exePath)
}

// flavorFor is the parameterized core of CurrentFlavor: it resolves the
// flavor for an explicit goos/goarch pair and executable path so the
// platform gate and the library probe are unit-testable on any host (the
// test cannot change runtime.GOOS, and os.Executable always succeeds once
// a process has started). The lookup rule mirrors CurrentFlavor: CUDA 13
// only on linux/amd64 with the provider library present beside the binary;
// any error or mismatch resolves to FlavorCPU.
func flavorFor(goos, goarch, exePath string) Flavor {
	if goos != "linux" || goarch != "amd64" {
		return FlavorCPU
	}
	if exePath == "" {
		return FlavorCPU
	}
	libPath := filepath.Join(filepath.Dir(exePath), cudaProviderLibrary)
	if _, err := os.Stat(libPath); err != nil {
		return FlavorCPU
	}
	return FlavorCUDA13
}
