// Package toolmanager manages the lifecycle of external CLI tools required
// by c0wrk at runtime. It downloads, installs, and version-tracks tools such
// as ripgrep, rtk, uv, and markitdown into a managed tools directory under
// the agent directory (~/.c0wrk/tools/).
package toolmanager

import (
	"fmt"
	"log/slog"
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
// Unsupported platforms receive a synthetic "<goarch>-<goos>" fallback;
// the caller should handle missing tool URLs/checksums for those platforms.
//
// Decision (2026-06-21): this function uses a fallback for unsupported
// platforms (synthetic GOARCH-GOOS triple) rather than returning an error.
// This ensures the tool-manager doesn't block startup on unknown platforms;
// tools without URLs for the platform are simply skipped. If a new platform
// needs explicit support, add its triple to the switch and verify upstream
// URLs exist.
func PlatformTriple() (string, error) {
	switch {
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "x86_64-apple-darwin", nil
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "aarch64-apple-darwin", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "x86_64-unknown-linux-musl", nil
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "aarch64-unknown-linux-gnu", nil
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "x86_64-pc-windows-msvc", nil
	}
	// Fallback for platforms not explicitly listed. The caller will
	// receive a synthetic triple and can proceed — tool URLs/checksums
	// will be empty for this platform, causing the tool to be skipped.
	slog.Warn("unsupported platform, using fallback triple",
		"os", runtime.GOOS, "arch", runtime.GOARCH)
	return fmt.Sprintf("%s-%s", runtime.GOARCH, runtime.GOOS), nil
}
