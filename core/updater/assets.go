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
//	linux/arm64   → c0wrk-desktop-linux-arm64.tar.gz
//	windows/amd64 → c0wrk-desktop-windows-amd64.zip
var supportedPlatforms = []platformSpec{
	{goos: "darwin", goarch: "arm64", basename: "c0wrk-desktop-macos-arm64.zip", token: "macos-arm64"},
	{goos: "linux", goarch: "amd64", basename: "c0wrk-desktop-linux-amd64.tar.gz", token: "linux-amd64"},
	{goos: "linux", goarch: "arm64", basename: "c0wrk-desktop-linux-arm64.tar.gz", token: "linux-arm64"},
	{goos: "windows", goarch: "amd64", basename: "c0wrk-desktop-windows-amd64.zip", token: "windows-amd64"},
}

// AssetNameForPlatform returns the canonical release asset filename for the
// given GOOS/GOARCH pair. It returns ErrNoAssetForPlatform when the platform
// is not part of the release matrix (e.g. linux/riscv64, darwin/amd64).
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
//
// Matching is exact-first: it prefers the canonical archive filename for the
// platform (compared case-insensitively against either the asset's Name or the
// filename portion of its BrowserDownloadURL). Only when that exact name is
// absent does it fall back to a substring match on the platform token, and that
// fallback is restricted to recognised archive extensions so that companion
// files (detached signatures, checksums) never win over the archive itself.
//
// It returns ErrNoAssetForPlatform when the platform is unsupported or when no
// asset in the release matches it.
func SelectAsset(assets []ReleaseAsset, goos, goarch string) (ReleaseAsset, error) {
	basename, err := AssetNameForPlatform(goos, goarch)
	if err != nil {
		return ReleaseAsset{}, err
	}
	basename = strings.ToLower(basename)

	// First pass: the canonical name is the strongest signal. Prefer it even
	// if unrelated assets appear earlier in the list.
	for _, a := range assets {
		if assetFilename(a) == basename {
			return a, nil
		}
	}

	// Second pass: fall back to the platform token, but only within the
	// filename (never the whole URL) and only for archive files.
	token := strings.ToLower(platformToken(goos, goarch))
	for _, a := range assets {
		name := assetFilename(a)
		if name == "" {
			continue
		}
		if isArchiveName(name) && strings.Contains(name, token) {
			return a, nil
		}
	}

	return ReleaseAsset{}, ErrNoAssetForPlatform
}

// assetFilename returns the lower-cased filename of an asset: the Name field
// when present, otherwise the last path segment of BrowserDownloadURL.
func assetFilename(a ReleaseAsset) string {
	if a.Name != "" {
		return strings.ToLower(a.Name)
	}
	return urlPathBasename(a.BrowserDownloadURL)
}

// urlPathBasename returns the lower-cased last path segment of a URL, ignoring
// query strings and fragments. It returns "" when the URL has no path segment.
func urlPathBasename(rawurl string) string {
	if rawurl == "" {
		return ""
	}
	if i := strings.IndexAny(rawurl, "?#"); i >= 0 {
		rawurl = rawurl[:i]
	}
	rawurl = strings.TrimRight(rawurl, "/")
	if rawurl == "" {
		return ""
	}
	if i := strings.LastIndex(rawurl, "/"); i >= 0 {
		rawurl = rawurl[i+1:]
	}
	return strings.ToLower(rawurl)
}

// archiveExtensions lists the archive suffixes produced by the release matrix
// (.github/workflows/release.yml). Only files with one of these suffixes are
// eligible for token-based matching.
var archiveExtensions = []string{".zip", ".tar.gz"}

// isArchiveName reports whether name ends with a recognised archive extension.
func isArchiveName(name string) bool {
	for _, ext := range archiveExtensions {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// CurrentPlatform returns the GOOS/GOARCH the running binary was built for.
// It is a convenience wrapper over the runtime package, exposed so callers can
// override it (mainly for tests) without touching globals.
func CurrentPlatform() (goos, goarch string) {
	return runtime.GOOS, runtime.GOARCH
}
