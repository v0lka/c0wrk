// Package version exposes build-time metadata injected via linker flags
// (-ldflags "-X .../version.Version=v1.2.3").
//
// The package-level vars hold compile-time defaults so that a plain
// `go build`/`go test` (no ldflags) always produces a valid, non-empty
// value. CI/release builds override them through the Makefile and
// release.yml workflows.
package version

var (
	// Version is the application version string. Defaults to "dev" for local
	// builds; overridden to the git tag (e.g. "v1.0.0") in release builds.
	Version = "dev"

	// GitCommit is the short commit hash the binary was built from, or "none"
	// when not injected.
	GitCommit = "none"

	// BuildDate is the UTC build timestamp (RFC3339), or "unknown" when not
	// injected.
	BuildDate = "unknown"
)
