# Arch Linux packaging

Two PKGBUILDs, both installing the same file tree:

| Package     | Source                                                        |
|-------------|---------------------------------------------------------------|
| `c0wrk-bin` | Repackages the published GitHub Release tarball. No compiler.  |
| `c0wrk-git` | Clones the repository and builds from source with Wails.       |

They are mutually exclusive (`conflicts`), and both `provides=(c0wrk)`.

## Layout

Everything lands in a single directory under `/opt`:

```
/opt/c0wrk/c0wrk-desktop
/opt/c0wrk/libonnxruntime.so
/opt/c0wrk/models/jina-v2-small.onnx
/opt/c0wrk/models/jina-v2-small-tokenizer.json
/usr/bin/c0wrk-desktop            launcher script (see below)
/usr/bin/c0wrk                    -> c0wrk-desktop
/usr/share/applications/c0wrk-desktop.desktop
/usr/share/icons/hicolor/512x512/apps/c0wrk-desktop.png
/usr/share/licenses/<pkgname>/LICENSE
```

The single-directory layout is not cosmetic. The app locates its ONNX Runtime
library and its embedding models relative to `os.Executable()` — see
`resolveONNXLibPath` and `resolveModelPath` in `desktop/startup.go`. Splitting
the tree across `/usr/lib` and `/usr/share` would break both lookups.

`/usr/bin/c0wrk-desktop` is a small launcher script, not a symlink. It exports
`C0WRK_START_HIDDEN=false` and then `exec`s the real binary.

The app creates its main window hidden (`StartHidden` in `main.go`) and reveals
it later from the startup sequence via `runtime.WindowShow`. When the backend
starts fast enough — a few milliseconds, which is what an unconfigured or
unparseable `config.yaml` produces — that show request lands before GTK has
realized the window. It is lost (glib logs `assertion 'GDK_IS_WINDOW (window)'
failed`), the window stays Withdrawn, and the app runs with a clean startup log
and no visible window. Creating the window visible up front removes the race;
the later `WindowShow` calls become no-ops. An explicit `C0WRK_START_HIDDEN`
from the environment still wins, so upstream's default remains reachable.

This is a workaround for an upstream bug, not a packaging requirement — the
same race affects the official release archives. Drop the launcher and go back
to plain symlinks once it is fixed in `desktop/startup_phases.go`.

Because the launcher ends in `exec`, `/proc/self/exe` — and with it Go's
`os.Executable()` — still points at `/opt/c0wrk/c0wrk-desktop`, so the app keeps
finding `libonnxruntime.so` and `models/` next to itself.

## Size

The installed package is roughly 200 MB, of which ~130 MB is the
`jina-embeddings-v2-small-en` ONNX model and ~24 MB is ONNX Runtime. Both ship
inside the upstream release archive, so local semantic search works with no
extra download.

## Building

```bash
cd c0wrk-bin   # or c0wrk-git
makepkg -si
```

`c0wrk-git` additionally needs Go, Node and the WebKitGTK development headers;
it installs the Wails CLI into a build-local GOPATH so no AUR dependency is
required.

## Automation

Releases are frequent enough that none of the per-release bookkeeping below is
meant to be done by hand. Two scripts do it, and CI runs both.

| Script | Does | Runs |
|---|---|---|
| `bump-bin.sh <tag>` | Points `c0wrk-bin` at a release: rewrites `_tag`, resets `pkgrel`, refreshes every `sha256sums` entry, regenerates `.SRCINFO`. | `Packaging bump` workflow, called by `release.yml` after each release; also on demand. |
| `check-pins.sh` | Fails if `c0wrk-git`'s pinned build inputs have drifted from the `Makefile` or `release.yml`. | `Packaging` workflow, on every PR touching `packaging/` or the `Makefile`. |

Both are ordinary scripts — run them locally the same way CI does.

### Updating c0wrk-bin for a new release

```bash
packaging/archlinux/bump-bin.sh v0.8.0
```

Archive digests come from the release's own `SHA256SUMS` asset rather than from
downloading 200 MB and hashing it locally: faster, and a stronger claim — the
PKGBUILD asserts the bytes upstream published, not the bytes one machine
happened to fetch. `LICENSE` and the icon are not covered by `SHA256SUMS`, so
they are fetched and hashed; local files are hashed from the working tree.

The script needs `makepkg` for `.SRCINFO`. Without it the PKGBUILD is still
updated but the script exits non-zero, so CI cannot ship a stale `.SRCINFO`.

`pkgver` is derived from `_tag`, not stored separately. Arch forbids `-` in
`pkgver`, so a prerelease tag like `v0.7-beta` becomes `0.7_beta` while the
download URLs keep using the real tag.

### Updating the pinned build inputs in c0wrk-git

`c0wrk-git` needs no per-release edit — its `pkgver()` derives from
`git describe`. What it does need is for its pinned inputs to keep matching
their sources of truth:

| PKGBUILD | Source of truth |
|---|---|
| `_onnx_ver`, `sha256sums_x86_64`, `sha256sums_aarch64` | `Makefile`: `ONNX_VERSION`, `ONNX_SHA256` |
| model and tokenizer digests | `Makefile`: `EMBEDDING_MODEL_SHA256`, `EMBEDDING_TOKENIZER_SHA256` |
| `_wails_ver` | `release.yml`: `WAILS_VERSION` |

Nothing propagates those automatically, and a mismatch is invisible until
somebody builds the package — so `check-pins.sh` asserts it on every PR. It
reads the `Makefile` by asking `make` to evaluate the variables for a given
platform, so the `ifeq` blocks keyed on `uname` resolve exactly as they would
during a real build, and it reads the PKGBUILD by sourcing it. Neither side is
pattern-matched.

When a pin does change, copy the new value across, bump `_onnx_ver` or
`_hf_rev`, and regenerate `.SRCINFO`.

Unlike the `Makefile`, the model URL pins an explicit Hugging Face revision
instead of `main`, so a moving branch cannot silently invalidate the digest.

## Publishing to the AUR

Neither package is registered on the AUR yet. The first push has to be done by
hand — the AUR creates a package repository on first push and there is no way to
bootstrap that from CI:

```bash
git clone ssh://aur@aur.archlinux.org/c0wrk-bin.git
cp c0wrk-bin/{PKGBUILD,.SRCINFO,c0wrk-desktop.desktop,c0wrk.install,c0wrk-launcher.sh} c0wrk-bin.git/
```

After that, CI can keep it current. The `Packaging bump` workflow pushes both
packages to the AUR, but only once the secrets exist — with none set it prints
what it skipped and moves on.

| Secret | Required | Purpose |
|---|---|---|
| `AUR_SSH_KEY` | for AUR publishing | Private SSH key whose public half is on the AUR account. Its presence is what enables the publish step. |
| `AUR_KNOWN_HOSTS` | no, but preferred | Output of `ssh-keyscan aur.archlinux.org`. Without it the host keys are learned on first contact, which the job cannot verify. |
| `PACKAGING_PR_TOKEN` | no | PAT used to push the branch and open the PR. Without it the default `GITHUB_TOKEN` is used, which works — but GitHub does not run workflows on events authored by `GITHUB_TOKEN`, so the packaging PR gets no CI. |

Using `GITHUB_TOKEN` also requires *Allow GitHub Actions to create and approve
pull requests* to be enabled in the repository's Actions settings.

The file list pushed to the AUR comes from `git ls-files`, so build artifacts
and downloaded sources sitting in the working tree can never leak into the AUR
repository.

The `.desktop`, `.install` and launcher files are duplicated in both package
directories on purpose — AUR repositories are flat and cannot reference files
outside themselves.
