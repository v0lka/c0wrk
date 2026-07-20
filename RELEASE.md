# Release Guide

This document describes how to cut a release of **c0wrk**, the artifact inventory published by each release, and how end users install the (currently **unsigned**) desktop builds on macOS, Linux, and Windows.

> Signed builds are not available yet. See [Follow-ups](#follow-ups-not-done-in-v1) for the notarization / code-signing roadmap.

---

## Cutting a release

Releases are produced by the `release.yml` GitHub Actions workflow. It builds the app for all three operating systems and publishes a GitHub Release with auto-generated notes and three downloadable artifacts.

### Standard flow: tag and push

```bash
git tag v0.1.0
git push origin v0.1.0
```

What happens next:

1. Pushing a tag named `v*` triggers the **Release** workflow (`.github/workflows/release.yml`).
2. The workflow builds the app for **3 OSes** (macOS arm64, Linux amd64, Windows amd64), bundling the ONNX Runtime shared library and embedding the vector-search models.
3. On success it **publishes a GitHub Release** tied to that tag, with **auto-generated release notes** and **3 downloadable artifacts**.

Verify the result under **Releases** → your tag, then promote / announce the release URL.

---

## Manual test run (no tag push)

To exercise the workflow without cutting a real release:

1. Open the repo on GitHub → **Actions** tab.
2. Select the **Release** workflow in the left sidebar.
3. Click **Run workflow**.
4. Enter a `tag_name` (e.g. `v0.1.0-test`) in the prompt.
5. Watch the run complete and review the generated GitHub Release + artifacts.
6. **Delete the generated release** (Releases → the test tag → `Delete`) so it does not appear in the project's public release history. Also delete the test tag if you don't want it to linger.

> A manual run still creates a real (draft-quality) GitHub Release — always clean it up after verifying.

---

## Artifact inventory

Each release publishes three archives. Unzip/extract the one matching your OS and architecture.

| Filename                              | Target                       | Contents                                                                          |
| ------------------------------------- | ---------------------------- | --------------------------------------------------------------------------------- |
| `c0wrk-desktop-macos-arm64.zip`       | macOS (Apple Silicon, arm64) | `c0wrk-desktop.app` bundle with bundled `libonnxruntime.dylib` + embedding models |
| `c0wrk-desktop-linux-amd64.tar.gz`    | Linux (amd64)                | `c0wrk-desktop` binary + `libonnxruntime.so` + embedding models                   |
| `c0wrk-desktop-windows-amd64.zip`     | Windows (amd64)              | `c0wrk-desktop.exe` + `onnxruntime.dll` + embedding models                        |

> The ONNX Runtime shared library and the quantized embedding model + tokenizer are bundled so vector search works out of the box — no extra download step is required on the user's machine.

---

## Installation

### macOS (arm64)

1. Download `c0wrk-desktop-macos-arm64.zip` and unzip it.
2. The app is **unsigned**, so macOS Gatekeeper will block first launch. Clear the quarantine attribute:

   ```bash
   xattr -cr /path/to/c0wrk-desktop.app
   ```

   Alternatively, **right-click** `c0wrk-desktop.app` → **Open** on first launch, then confirm in the Gatekeeper dialog.

3. Launch `c0wrk-desktop.app`.

### Linux (amd64)

1. Download `c0wrk-desktop-linux-amd64.tar.gz` and extract it:

   ```bash
   tar -xzf c0wrk-desktop-linux-amd64.tar.gz
   ```

2. Run the binary:

   ```bash
   ./c0wrk-desktop
   ```

3. If the binary can't find `libonnxruntime.so`, run it from the extraction directory or add that directory to `LD_LIBRARY_PATH`.

### Windows (amd64)

1. Download `c0wrk-desktop-windows-amd64.zip` and extract it.
2. The app is **unsigned**, so Windows **SmartScreen** may warn "Windows protected your PC". Click **More info** → **Run anyway**.
3. Run `c0wrk-desktop.exe`.

---

## Follow-ups (not done in v1)

These items are deliberately **out of scope** for the first release and tracked here for future work:

- **Apple notarization + Developer ID** — produce a notarized, stapled macOS app so Gatekeeper does not block it.
- **Windows code-signing certificate** — sign `c0wrk-desktop.exe` to silence SmartScreen.
- **Universal macOS binary** — ship a single `c0wrk-desktop-macos-universal` artifact covering both arm64 and amd64.
- **Linux arm64 build** — add a `c0wrk-desktop-linux-arm64.tar.gz` target.
- **Optional in-app version stamping** — inject the release version into the app via `-ldflags` (e.g. `-X main.version=v0.1.0`) so the UI can show the running build.
