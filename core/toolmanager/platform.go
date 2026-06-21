// Package toolmanager manages the lifecycle of external CLI tools required
// by c0wrk at runtime. It downloads, installs, and version-tracks tools such
// as ripgrep, rtk, uv, and markitdown into a managed tools directory under
// the agent directory (~/.c0wrk/tools/).
package toolmanager

import (
	"fmt"
	"runtime"
)

// Platform returns the canonical OS/arch string used as URL map keys.
// Format: "<os>-<arch>", e.g. "darwin-arm64".
func Platform() string {
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}

// PlatformTriple returns the upstream release naming convention used in
// archive file names and internal archive directory paths.
// Format: "<arch>-<vendor>-<os>[-<abi>]", e.g. "aarch64-apple-darwin".
func PlatformTriple() string {
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "x86_64-apple-darwin"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "aarch64-apple-darwin"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "x86_64-unknown-linux-musl"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "aarch64-unknown-linux-gnu"
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "x86_64-pc-windows-msvc"
	}
	// Fallback: use the Go convention.
	return fmt.Sprintf("%s-%s", runtime.GOARCH, runtime.GOOS)
}
