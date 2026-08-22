# ADR-026: Linux arm64 Release Support

## Status

Proposed

## Context

Releases cover three platforms (macOS arm64, Linux amd64, Windows amd64). The
release matrix is mirrored by the in-app self-updater's `supportedPlatforms`
registry (`core/updater/assets.go`): a platform missing from the registry gets
`ErrNoAssetForPlatform` on every update check. Linux arm64 was listed as a
follow-up in `CONTRIBUTING.md` ("Linux arm64 build — add a
`c0wrk-desktop-linux-arm64.tar.gz` target").

Lower layers were already arm64-ready: the Makefile downloads
`onnxruntime-linux-aarch64` for `aarch64` hosts, the tool-manager ships
linux-arm64 URLs and SHA256 checksums for uv and ripgrep, and the embedding
model is architecture-independent.

ADR-023 (auto-update) is immutable per `specs/META.md`; this ADR extends the
platform matrix it describes without modifying it.

## Decision

Add Linux arm64 as a fully supported release platform:

- `supportedPlatforms` in `core/updater/assets.go` gains
  `{goos: "linux", goarch: "arm64", basename: "c0wrk-desktop-linux-arm64.tar.gz", token: "linux-arm64"}`.
- CI (`ci.yml`) and release (`release.yml`) Linux jobs become
  `strategy.matrix` over `[{amd64, ubuntu-24.04}, {arm64, ubuntu-24.04-arm}]`
  — one source of truth per pipeline, no duplicated jobs to drift.
- Release packaging parameterizes the tar name, the upload-artifact name, and
  its path with `${{ matrix.arch }}` (upload-artifact v4 fails on duplicate
  artifact names within a run).
- Release matrix keeps `fail-fast: true` (default): a failed arm64 leg cancels
  the release rather than risk partial publication — consistent with the
  fail-closed philosophy of the updater.

## Consequences

- arm64 users can download releases and self-update in-app, like every other
  platform.
- Every PR now pays two Linux CI legs; Go/frontend lint runs on the amd64 leg
  only (architecture-independent, slow to install on arm64).
- Updater integration tests that previously skipped on linux/arm64 hosts now
  run for real — expected green (the pipeline is archive-format agnostic).
- Until the first arm64 release is published, hand-built arm64 binaries return
  `ErrNoAssetForPlatform` on update checks (unchanged behavior, now with a
  canonical asset to look forward to).

## Alternatives Considered

- **Duplicating jobs** (copy `build-linux`/`linux` per arch): zero risk to the
  current pipeline but invites config drift between the copies — rejected for a
  solo-maintained upstream.
- **Cross-compiling from amd64 runners**: Wails on Linux requires CGO against
  webkit2gtk/gtk; cross-building native GTK deps or QEMU-emulating the full GUI
  build is slow and brittle — rejected in favor of native arm64 runners.
