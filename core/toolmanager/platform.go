// Package toolmanager manages the lifecycle of the external CLI tools c0wrk
// needs at runtime — downloading, installing, and version-tracking rg, uv,
// and markitdown into ~/.c0wrk/tools/.
package toolmanager

import (
	"fmt"
	"log/slog"
	"runtime"
)

// Platform returns the canonical "<os>-<arch>" URL-map key, e.g. "darwin-arm64".
func Platform() string {
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}

// PlatformTriple returns the upstream release naming convention used in
// archive names and in-archive paths (e.g. "aarch64-apple-darwin").
// Unsupported platforms get a synthetic <goarch>-<goos> fallback instead of
// an error — an unknown platform must not block startup; its tools have no
// URLs/checksums and are skipped. Supporting one: add the triple and verify
// the upstream URLs. logger (nil-safe) warns on the fallback path.
func PlatformTriple(logger *slog.Logger) string {
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
	if logger != nil {
		logger.Warn("unsupported platform, using fallback triple",
			"os", runtime.GOOS, "arch", runtime.GOARCH)
	}
	return fmt.Sprintf("%s-%s", runtime.GOARCH, runtime.GOOS)
}
