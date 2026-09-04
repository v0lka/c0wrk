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
/usr/bin/c0wrk-desktop            -> /opt/c0wrk/c0wrk-desktop
/usr/bin/c0wrk                    -> /opt/c0wrk/c0wrk-desktop
/usr/share/applications/c0wrk-desktop.desktop
/usr/share/icons/hicolor/512x512/apps/c0wrk-desktop.png
/usr/share/licenses/<pkgname>/LICENSE
```

The single-directory layout is not cosmetic. The app locates its ONNX Runtime
library and its embedding models relative to `os.Executable()` — see
`resolveONNXLibPath` and `resolveModelPath` in `desktop/startup.go`. Splitting
the tree across `/usr/lib` and `/usr/share` would break both lookups.

The `/usr/bin` entries are symlinks rather than wrapper scripts: on Linux
`os.Executable()` reads `/proc/self/exe`, which is already fully resolved, so
the executable directory is `/opt/c0wrk` regardless of which symlink was
invoked.

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

## Updating c0wrk-bin for a new release

1. Bump `pkgver`, reset `pkgrel=1`.
2. Replace the two `sha256sums_*` archive digests with the values published in
   the release's `SHA256SUMS` asset:
   ```bash
   curl -sL https://github.com/v0lka/c0wrk/releases/download/vX.Y.Z/SHA256SUMS
   ```
3. Refresh the `LICENSE` and `appicon.png` digests only if those files changed.
4. Regenerate `.SRCINFO`:
   ```bash
   makepkg --printsrcinfo > .SRCINFO
   ```

## Updating the pinned build inputs in c0wrk-git

The ONNX Runtime archive and the embedding model are fetched by `makepkg` from
`source=()` with digests copied verbatim from the repository `Makefile`
(`ONNX_SHA256`, `EMBEDDING_MODEL_SHA256`, `EMBEDDING_TOKENIZER_SHA256`). When
upstream bumps `ONNX_VERSION` or the model, copy the new values across and bump
`_onnx_ver` / `_hf_rev` accordingly.

Unlike the `Makefile`, the model URL pins an explicit Hugging Face revision
instead of `main`, so a moving branch cannot silently invalidate the digest.

## Publishing to the AUR

Each directory is a self-contained AUR package. Copy it into the AUR clone,
commit `PKGBUILD`, `.SRCINFO` and the auxiliary files, and push:

```bash
git clone ssh://aur@aur.archlinux.org/c0wrk-bin.git
cp c0wrk-bin/{PKGBUILD,.SRCINFO,c0wrk-desktop.desktop,c0wrk.install} c0wrk-bin.git/
```

The `.desktop` and `.install` files are duplicated in both directories on
purpose — AUR repositories are flat and cannot reference files outside
themselves.
