// Package version exposes build-time metadata injected via linker flags
// (-ldflags "-X .../version.Version=v1.2.3"). The vars hold compile-time defaults
// so a plain `go build`/`go test` still yields valid, non-empty values; CI and
// release builds override them via the Makefile and release.yml.
package version

var (
	// Version is the release git tag (e.g. "v1.0.0"); "dev" without ldflags.
	Version = "dev"

	// GitCommit is the short commit hash of the build; "none" when not injected.
	GitCommit = "none"

	// BuildDate is the UTC RFC3339 build timestamp; "unknown" when not injected.
	BuildDate = "unknown"
)
