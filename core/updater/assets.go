// Package updater checks for newer c0wrk-desktop releases published on
// GitHub and selects the downloadable asset matching the running platform
// and packaging flavor.
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
// current GOOS/GOARCH/flavor combination.
var ErrNoAssetForPlatform = errors.New("no asset for platform")

// platformSpec describes a single supported build target and the release
// asset that carries it.
type platformSpec struct {
	goos     string // GOOS value, e.g. "darwin"
	goarch   string // GOARCH value, e.g. "arm64"
	flavor   Flavor // packaging flavor the asset carries; FlavorCPU everywhere, plus FlavorCUDA13 on linux/amd64
	basename string // canonical asset filename, e.g. "c0wrk-desktop-macos-arm64.zip"
	token    string // suffix token used to match an asset regardless of version, e.g. "macos-arm64"
}

// supportedPlatforms mirrors the build matrix in .github/workflows/release.yml.
// The asset names are produced by the "Package …" steps of each platform job.
// linux/amd64 is listed twice: the default CPU flavor and the opt-in CUDA 13
// flavor (ADR-036), whose archive name appends "-cuda13" after the arch token.
//
//	darwin/arm64       → c0wrk-desktop-macos-arm64.zip   (ditto of the .app bundle)
//	linux/amd64 cpu    → c0wrk-desktop-linux-amd64.tar.gz
//	linux/amd64 cuda13 → c0wrk-desktop-linux-amd64-cuda13.tar.gz
//	linux/arm64        → c0wrk-desktop-linux-arm64.tar.gz
//	windows/amd64      → c0wrk-desktop-windows-amd64.zip
var supportedPlatforms = []platformSpec{
	{goos: "darwin", goarch: "arm64", flavor: FlavorCPU, basename: "c0wrk-desktop-macos-arm64.zip", token: "macos-arm64"},
	{goos: "linux", goarch: "amd64", flavor: FlavorCPU, basename: "c0wrk-desktop-linux-amd64.tar.gz", token: "linux-amd64"},
	{goos: "linux", goarch: "amd64", flavor: FlavorCUDA13, basename: "c0wrk-desktop-linux-amd64-cuda13.tar.gz", token: "linux-amd64-cuda13"},
	{goos: "linux", goarch: "arm64", flavor: FlavorCPU, basename: "c0wrk-desktop-linux-arm64.tar.gz", token: "linux-arm64"},
	{goos: "windows", goarch: "amd64", flavor: FlavorCPU, basename: "c0wrk-desktop-windows-amd64.zip", token: "windows-amd64"},
}

// AssetNameFor returns the canonical release asset filename for the given
// GOOS/GOARCH pair and packaging flavor. It returns ErrNoAssetForPlatform
// when the combination is not part of the release matrix (e.g.
// linux/riscv64, darwin/amd64, or a CUDA flavor on a platform that ships no
// flavor variants).
func AssetNameFor(goos, goarch string, flavor Flavor) (string, error) {
	for _, p := range supportedPlatforms {
		if p.goos == goos && p.goarch == goarch && p.flavor == flavor {
			return p.basename, nil
		}
	}
	return "", ErrNoAssetForPlatform
}

// platformToken returns the suffix identifying the asset for a platform and
// flavor, or "" when unsupported. It is used to match against the filenames
// returned by the GitHub API, which embed the version in some flows.
func platformToken(goos, goarch string, flavor Flavor) string {
	for _, p := range supportedPlatforms {
		if p.goos == goos && p.goarch == goarch && p.flavor == flavor {
			return p.token
		}
	}
	return ""
}

// SelectAsset picks the release asset whose name matches the given platform
// and packaging flavor.
//
// Matching is exact-first: it prefers the canonical archive filename for the
// platform+flavor (compared case-insensitively against either the asset's Name
// or the filename portion of its BrowserDownloadURL). Only when that exact
// name is absent does it fall back to a token match, and that fallback is
// anchored: the filename must end with "<token><archive-extension>" so that
// companion files (detached signatures, checksums) and differently-flavored
// archives never win over the archive itself. The anchoring is load-bearing
// for the CPU flavor on linux/amd64: its token "linux-amd64" is a substring
// of the CUDA asset name "…-linux-amd64-cuda13.tar.gz", and a plain Contains
// match would silently hand a CPU installation a CUDA archive (or vice versa).
//
// It returns ErrNoAssetForPlatform when the platform+flavor is unsupported or
// when no asset in the release matches it.
func SelectAsset(assets []ReleaseAsset, goos, goarch string, flavor Flavor) (ReleaseAsset, error) {
	basename, err := AssetNameFor(goos, goarch, flavor)
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

	// Second pass: fall back to the platform token as an anchored suffix
	// "<token><ext>", checked only within the filename (never the whole URL).
	token := strings.ToLower(platformToken(goos, goarch, flavor))
	for _, a := range assets {
		name := assetFilename(a)
		if name == "" {
			continue
		}
		if matchesTokenExt(name, token) {
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
// (.github/workflows/release.yml). Only files ending in "<token><ext>" with
// one of these extensions are eligible for token-based matching.
var archiveExtensions = []string{".zip", ".tar.gz"}

// matchesTokenExt reports whether name ends with the token immediately
// followed by a recognised archive extension. Requiring the token to sit
// flush against the extension anchors the match at the end of the filename:
// "linux-amd64" matches "c0wrk-desktop-linux-amd64.tar.gz" but NOT
// "c0wrk-desktop-linux-amd64-cuda13.tar.gz", whose arch token is followed by
// the "-cuda13" flavor infix. A substring Contains has no such guarantee.
func matchesTokenExt(name, token string) bool {
	for _, ext := range archiveExtensions {
		if strings.HasSuffix(name, token+ext) {
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
