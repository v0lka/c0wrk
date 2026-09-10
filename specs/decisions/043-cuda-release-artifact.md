# ADR-043: CUDA Release Artifact (linux/amd64, flavor-pinned updates)

## Status

Accepted

## Context

[ADR-042](042-gpu-embedding-provider.md) added the GPU embedding execution
provider and the opt-in `make fetch-onnx-gpu` packaging target (cuda13 flavor
only), but explicitly deferred shipping the GPU flavor in official release
archives: the CUDA-flavored ONNX Runtime is a ~240 MiB download, and its
provider library links against the **system** CUDA runtime
(`libcudart.so.13`, cuBLAS, cuRAND), so a GPU build only runs on machines with
a matching CUDA major version. Since then two things changed:

1. **The updater became flavor-aware** (see below). A GPU install tree now
   has a machine-readable identity, which a release archive can carry too.
2. **The silent-degradation trap became concrete.** Without a published GPU
   artifact, a user who wants GPU embeddings must hand-build from source
   (`make build && make fetch-onnx-gpu`) — and the moment the in-app updater
   runs, the update flow must pick *some* linux/amd64 archive. The default
   CPU archive carries no CUDA provider libraries, so applying a regular
   update silently strips the GPU flavor: the provider library disappears
   from the install tree, `execution_provider: auto` quietly resolves to CPU,
   and the user is back to the exact failure mode ADR-042 was written to
   eliminate — GPU idle, no signal.

ADR-023's release matrix publishes four platform archives
(`macos-arm64.zip`, `linux-amd64.tar.gz`, `linux-arm64.tar.gz`,
`windows-amd64.zip`) plus an auto-generated `SHA256SUMS`; the in-app updater
consumes exactly that channel. Any GPU packaging decision is therefore also
an updater decision.

### The flavor-aware updater

`core/updater/` now distinguishes install flavors: `CurrentFlavor()`
([flavor.go](../../core/updater/flavor.go)) reports `cuda13` when the tree
next to the running executable contains `libonnxruntime_providers_cuda.so`
(the file `make fetch-onnx-gpu` drops there) on linux/amd64 — everything
else, including any probe error, reports `cpu` (fail-closed to the safe
default). The asset matrix
([assets.go](../../core/updater/assets.go)) lists linux/amd64 twice — the
CPU flavor and the CUDA flavor (`c0wrk-desktop-linux-amd64-cuda13.tar.gz`) —
and `SelectAsset` matches exact-first with an anchored token fallback, so the
CPU token `linux-amd64` can never match `…-linux-amd64-cuda13.tar.gz` or
vice versa. A release that carries no asset for the running flavor is a hard
`ErrNoAssetForPlatform` error, never a silent "no update".

### Forces

- **Correctness of updates outweighs artifact count.** A GPU user whose next
  update silently downgrades to CPU libraries has lost data-plane capability
  with no error anywhere — strictly worse than shipping one more archive.
- **The binary is flavor-agnostic.** The executable is byte-identical across
  flavors; the flavor lives in the install tree. A separate artifact is a
  packaging difference, not a code fork.
- **Download size is real but bounded.** The CUDA archive is ~240 MiB
  against ~25 MiB for the CPU one. Nobody downloads it by accident: it is a
  named, opt-in asset on the release page.
- **CUDA major pinning is already decided** (ADR-042): one flavor, cuda13.
  This ADR does not reopen that.

## Decision

1. **Publish a fifth release artifact — `c0wrk-desktop-linux-amd64-cuda13.tar.gz`.**
   The release workflow gains a `build-linux-cuda` job (ubuntu-24.04, amd64)
   that runs `make build`, then `make fetch-onnx-gpu` **after** it (the
   Makefile's stamp policy makes `make build` reinstall the CPU flavor on a
   stamp mismatch, so the GPU fetch must come last), and packages
   `build/bin` into the artifact. The tarball is created with `tar -czf … .`
   from inside `build/bin` so the ONNX version-stamp dot-files
   (`.onnxruntime-version`, `.onnxruntime-gpu-version`) — the exact stamps a
   later `make build`/`make fetch-onnx-gpu` compares against — land in the
   archive; a glob would skip them. The release job gains `build-linux-cuda`
   in `needs`, so a failing CUDA build blocks the release. `SHA256SUMS` and
   the updater's fail-closed digest verification extend to the new asset
   automatically (the flatten step globs `*.tar.gz`).

2. **Updates are always same-flavor; flavor changes are manual reinstalls.**
   A cuda13 install checks for `…-linux-amd64-cuda13.tar.gz`; a CPU install
   checks for `…-linux-amd64.tar.gz`. The flavor is read from the install
   tree at check time (`CurrentFlavor()`), not persisted in config, not
   user-selectable in the updater UI. Switching flavors (either direction)
   is an explicit manual step: download the other archive and replace the
   install tree — the same manual install path that already exists for every
   platform. The updater never migrates flavors on its own.

3. **CI exercises the GPU recipe.** The linux/amd64 CI leg runs
   `make fetch-onnx-gpu` after `make build` as a recipe smoke test — a
   broken URL, a stale pinned digest, or a recipe regression fails the PR
   instead of the release. (~240 MiB per run is the price of early
   detection; skipped on arm64 where the pinned artifact has no build and
   the target exits 1 by design.)

## Consequences

**Positive:**

- GPU users get in-app updates that preserve the GPU flavor; the ADR-042
  deferred item "packaging the GPU flavor into official release archives" is
  closed.
- The silent CPU-downgrade-on-update trap is eliminated by construction:
  flavor mismatch is a hard `ErrNoAssetForPlatform`, never a quiet wrong-
  archive download.
- End users no longer need a toolchain to run GPU embeddings — download the
  cuda13 archive, install the CUDA 13 runtime, done.
- `supportedPlatforms` in [assets.go](../../core/updater/assets.go) exactly
  mirrors the five produced assets, as its doc comment promises.

**Negative:**

- Release size grows by ~240 MiB per release and every release run pays the
  GPU download; CI pays it on every linux/amd64 push/PR too.
- A cuda13 user whose release has no cuda13 asset (e.g. a release cut before
  this ADR) gets a visible error rather than a CPU downgrade — correct, but
  it is a new failure surface to explain in docs.
- The artifact matrix stays in three places by hand (`release.yml`,
  `supportedPlatforms`, README/CONTRIBUTING tables); only the updater table
  is code-checked. Drift is caught by the ci.yml smoke test, not by a
  compile error.
- Flavor migration stays manual forever unless a future ADR adds it; the
  `.old` rollback tree from an update is same-flavor by construction.

## Alternatives Considered

- **Single archive with both library sets.** Rejected: every linux/amd64
  user would download ~240 MiB of CUDA libraries they mostly cannot use, and
  the install tree would carry a `libonnxruntime_providers_cuda.so` that
  `CurrentFlavor()` reads as a cuda13 flavor even on CPU-only machines — the
  flavor signal itself would become a lie.
- **Runtime download of GPU libraries.** Rejected: c0wrk's supply-chain
  posture is pinned-digest, fail-closed verification of every fetched byte
  (tool-manager, ONNX fetch, updater). An ad-hoc runtime downloader for GPU
  libraries would fork that posture, add a network dependency at first GPU
  use, and need its own trust anchoring — a second, weaker update channel.
- **CPU fallback with a warning on flavor loss.** Rejected: it silently
  removes capability the user explicitly installed (the GPU flavor is
  opt-in) and recreates the "GPU idle, user uninformed" failure mode ADR-042
  was written to kill; the WARN lives in logs most users never open.
- **Silent CPU fallback.** Rejected for the same reason, only louder: this
  is exactly the silent-slide behavior ADR-042's research documented and
  rejected; preserving it in the updater would contradict the reason the
  flavor system exists.

## Related Specs

- [ADR-042](042-gpu-embedding-provider.md) — GPU embedding execution
  provider, cuda13-only packaging, flavor-aware version stamps (this ADR
  closes its deferred "official release packaging" item and widens the
  release matrix).
- [ADR-023](023-auto-update.md) — self-update model, SHA256 fail-closed
  verification, unsigned-artifact trust posture (this ADR extends its
  release matrix from four artifacts to five).
- [domains/workspace.md](../domains/workspace.md) — Embedding Execution
  Provider section (GPU flavor packaging and runtime requirements).
