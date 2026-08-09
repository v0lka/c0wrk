// Package updater checks for newer c0wrk-desktop releases published on
// GitHub and selects the downloadable asset matching the running platform.
//
// The package is intentionally decoupled from transport and version sources:
// the HTTP client is injected (so corporate proxies configured via the
// proxy package are honoured) and the current version is supplied by the
// caller. This keeps the checker pure, deterministic and unit-testable with
// an httptest mock server.
package updater

import (
	"errors"
	"runtime"
	"strings"
)

// ErrNoAssetForPlatform is returned when no release asset matches the
// current GOOS/GOARCH combination.
var ErrNoAssetForPlatform = errors.New("no asset for platform")

// platformSpec describes a single supported build target and the release
// asset that carries it.
type platformSpec struct {
	goos     string // GOOS value, e.g. "darwin"
	goarch   string // GOARCH value, e.g. "arm64"
	basename string // canonical asset filename, e.g. "c0wrk-desktop-macos-arm64.zip"
	token    string // stable substring used to match an asset regardless of version, e.g. "macos-arm64"
}

// supportedPlatforms mirrors the build matrix in .github/workflows/release.yml.
// The asset names are produced by the "Package …" steps of each platform job.
//
//	darwin/arm64  → c0wrk-desktop-macos-arm64.zip   (ditto of the .app bundle)
//	linux/amd64   → c0wrk-desktop-linux-amd64.tar.gz
//	windows/amd64 → c0wrk-desktop-windows-amd64.zip
var supportedPlatforms = []platformSpec{
	{goos: "darwin", goarch: "arm64", basename: "c0wrk-desktop-macos-arm64.zip", token: "macos-arm64"},
	{goos: "linux", goarch: "amd64", basename: "c0wrk-desktop-linux-amd64.tar.gz", token: "linux-amd64"},
	{goos: "windows", goarch: "amd64", basename: "c0wrk-desktop-windows-amd64.zip", token: "windows-amd64"},
}

// AssetNameForPlatform returns the canonical release asset filename for the
// given GOOS/GOARCH pair. It returns ErrNoAssetForPlatform when the platform
// is not part of the release matrix (e.g. linux/arm64, darwin/amd64).
func AssetNameForPlatform(goos, goarch string) (string, error) {
	for _, p := range supportedPlatforms {
		if p.goos == goos && p.goarch == goarch {
			return p.basename, nil
		}
	}
	return "", ErrNoAssetForPlatform
}

// platformToken returns the stable substring identifying the asset for a
// platform, or "" when unsupported. It is used to match against the filenames
// returned by the GitHub API, which embed the version in some flows.
func platformToken(goos, goarch string) string {
	for _, p := range supportedPlatforms {
		if p.goos == goos && p.goarch == goarch {
			return p.token
		}
	}
	return ""
}

// SelectAsset picks the release asset whose name matches the given platform.
// Matching is case-insensitive on the token derived from GOOS/GOARCH, which is
// unique enough to ignore unrelated assets (checksums, source archives).
//
// It returns ErrNoAssetForPlatform when the platform is unsupported or when no
// asset in the release matches it.
func SelectAsset(assets []ReleaseAsset, goos, goarch string) (ReleaseAsset, error) {
	token := platformToken(goos, goarch)
	if token == "" {
		return ReleaseAsset{}, ErrNoAssetForPlatform
	}
	needle := strings.ToLower(token)
	for _, a := range assets {
		if a.Name == "" && a.BrowserDownloadURL == "" {
			continue
		}
		// Match against the filename portion of either field so that URLs
		// (…/download/v1.2.3/c0wrk-desktop-macos-arm64.zip) resolve too.
		if strings.Contains(strings.ToLower(a.Name), needle) ||
			strings.Contains(strings.ToLower(a.BrowserDownloadURL), needle) {
			return a, nil
		}
	}
	return ReleaseAsset{}, ErrNoAssetForPlatform
}

// CurrentPlatform returns the GOOS/GOARCH the running binary was built for.
// It is a convenience wrapper over the runtime package, exposed so callers can
// override it (mainly for tests) without touching globals.
func CurrentPlatform() (goos, goarch string) {
	return runtime.GOOS, runtime.GOARCH
}
