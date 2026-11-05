package updater

import (
	"os"
	"path/filepath"
	"runtime"
)

// Flavor identifies which ONNX Runtime packaging an installation carries:
// CPU by default or an opt-in GPU flavor (CUDA 13, OpenVINO). The flavor
// lives in the install tree's marker libraries, not the binary
// (byte-identical across flavors); the updater reads it to pick the
// same-flavor update payload. See specs/decisions/046-cuda-release-artifact.md
// and specs/decisions/089-openvino-embedding-flavor.md.
type Flavor string

const (
	// FlavorCPU is the default: ONNX Runtime CPU provider only, no GPU
	// libraries in the install tree.
	FlavorCPU Flavor = "cpu"

	// FlavorCUDA13 is the opt-in NVIDIA flavor (ADR-045/ADR-046): a CUDA-13
	// ONNX Runtime with the provider library beside the binary. One CUDA
	// major is pinned deliberately; a cuda12 flavor is explicitly unsupported.
	FlavorCUDA13 Flavor = "cuda13"

	// FlavorOpenVINO is the opt-in Intel flavor (ADR-089): the stock CPU
	// ONNX Runtime plus the additive OpenVINO provider plugin stack beside
	// the binary. The main library is not replaced, so the flavor coexists
	// with the CPU version stamps but stays mutually exclusive with CUDA
	// (the Makefile fetch targets refuse to mix them).
	FlavorOpenVINO Flavor = "openvino"
)

// cudaProviderLibrary marks a CUDA-flavored tree by its presence beside the
// executable (ONNX_GPU_CUDA_LIB in the Makefile's fetch-onnx-gpu target; the
// library ONNX Runtime dlopens for the CUDA execution provider).
const cudaProviderLibrary = "libonnxruntime_providers_cuda.so"

// openvinoPluginLibrary marks an OpenVINO-flavored tree by its presence
// beside the executable (ONNX_OV_PLUGIN_LIB in the Makefile's
// fetch-onnx-openvino target; registered at run time via
// RegisterExecutionProviderLibrary).
const openvinoPluginLibrary = "libonnxruntime_providers_openvino_plugin.so"

// CurrentFlavor determines the running installation's flavor from the
// install tree beside the executable (ADR-046: the flavor is read at check
// time, never persisted). GPU flavors exist only on linux/amd64 — the only
// platform they are published for — and require their marker library; with
// both markers present (unreachable via the Makefile's mutual exclusion)
// CUDA wins deterministically. Everything else — other platforms, a CPU
// tree, any error resolving or stat-ing the executable path — is fail-closed
// FlavorCPU: a wrong guess must degrade to the safe default, never break the
// update flow.
func CurrentFlavor() Flavor {
	exePath, err := os.Executable()
	if err != nil {
		return FlavorCPU
	}
	return flavorFor(runtime.GOOS, runtime.GOARCH, exePath)
}

// flavorFor is the parameterized core of CurrentFlavor, taking an explicit
// goos/goarch pair and executable path so the platform gate and library
// probes are unit-testable on any host; the lookup rule mirrors
// CurrentFlavor.
func flavorFor(goos, goarch, exePath string) Flavor {
	if goos != "linux" || goarch != "amd64" {
		return FlavorCPU
	}
	if exePath == "" {
		return FlavorCPU
	}
	dir := filepath.Dir(exePath)
	if _, err := os.Stat(filepath.Join(dir, cudaProviderLibrary)); err == nil {
		return FlavorCUDA13
	}
	if _, err := os.Stat(filepath.Join(dir, openvinoPluginLibrary)); err == nil {
		return FlavorOpenVINO
	}
	return FlavorCPU
}
