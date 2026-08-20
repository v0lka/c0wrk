# c0wrk

> [!WARNING]
> **Early Alpha Stage** — This project is under active development and not yet stable.
> Features, APIs, and configuration formats may change without notice.
> Use at your own risk.

A desktop AI agent for work that won't fit in a single reply — research, coding, writing, anything multi-step.

![c0wrk main view](docs/screenshots/main_view.png)

## What makes c0wrk different

- **Plans before it acts — and resumes where it stopped.** For non-trivial work, c0wrk maps the task to a graph-based plan that you can review. Independent steps run as isolated subagents in parallel; successful plan steps are reused on retry, while failed, pending, or explicitly targeted steps can run again. Pause creates a resumable checkpoint for ordinary, goal, and delegated work, and graceful app shutdown preserves active work for later resume.
- **Goals that run to the finish.** Start a message with `goal` option enabled and c0wrk commits to a concrete, checkable definition of "done" that you approve up front, then keeps working and verifying progress until the goal is met, blocked, out of budget, or paused.
- **Two stable modes, plus experimental research.** **CODE** works in a real project with file edits, diffs, git, semantic search, and a per-session terminal. **CHAT** provides an isolated scratch workspace without requiring git and code-modifying agent tools. With the all-or-nothing **Experimental Features** switch enabled, **RESEARCH** adds a workspace-contained `.research` methodology, hypothesis graph, metrics, and seeded research skills; experimental behavior can change.
- **Stay in the loop without stopping the loop.** Send a follow-up into a running or paused ordinary task and c0wrk queues it for the next step boundary.

- **The whole code loop stays in one window.** Review changed files as real diffs, comment on hunks or files, approve or request another pass, then stage, commit, push, inspect branches, and browse a lane graph of history.
- **Layered safety controls, not a sandbox.** Every tool belongs to a capability group with `allow`, `user_confirm`, or `deny` policy; mutating groups require confirmation by default. Path containment, symlink checks, execute blacklists, untrusted-content framing, and an optional conservative Smart Approve judge add independent gates. See [SECURITY.md](SECURITY.md).
- **Bring your own model.** Works with Anthropic, OpenAI, ChatGPT, and OpenAI- or Anthropic-compatible endpoints. Runtime probes can discover the effective context window exposed by self-hosted OpenAI-compatible servers; explicit config overrides remain authoritative. See the [self-hosted model guide](docs/self-hosted-models.md) for tool-parser and chat-template setup.
- **Runs well on small models.** The experimental, manually enabled Small-LLM profile can narrow the visible tool set, swap in a compact system prompt, tighten sampling and loop breakers, and compact context earlier. Every optimization has its own switch; the profile is inert unless both Experimental Features and Small-LLM are enabled.
- **Knows your codebase without indexing everything.** Hybrid semantic + lexical search respects `.gitignore` and `.aiignore`, skips oversized/pathological files using configurable file/chunk limits, and runs embeddings locally through bundled ONNX Runtime.
- **Plays nice with corporate networks.** HTTP/HTTPS proxy support includes custom CA certificates and bypass lists for outbound LLM, web, MCP, model-registry, and update traffic.

## Download & install

Prebuilt desktop builds are published on the [GitHub Releases](https://github.com/v0lka/c0wrk/releases) page. Builds are currently **unsigned** — see the per-OS notes below for the one-time workaround.

Every release also publishes `SHA256SUMS`. The in-app updater downloads over HTTPS, refuses an archive with a missing or mismatched SHA256 entry, stages an install-tree swap, and keeps a `.old` rollback copy. SHA256 proves that the downloaded archive matches the bytes published with the release; because the artifacts are unsigned, it does **not** prove release authorship if the release account and checksums are compromised. Updates and automatic checks can be disabled independently in Settings or under `updates` in `~/.c0wrk/config.yaml`.

Each release bundles three platform archives:

| Artifact                           | Target                |
| ---------------------------------- | --------------------- |
| `c0wrk-desktop-macos-arm64.zip`    | macOS (Apple Silicon) |
| `c0wrk-desktop-linux-amd64.tar.gz` | Linux (amd64)         |
| `c0wrk-desktop-windows-amd64.zip`  | Windows (amd64)       |

The ONNX Runtime library and the embedding models ship inside every archive, so vector search works without an extra download on your end.

### macOS (Apple Silicon)

1. Download `c0wrk-desktop-macos-arm64.zip` and unzip it.

2. The app is unsigned, so macOS Gatekeeper will block first launch. Clear the quarantine attribute:
   
   ```bash
   xattr -cr /path/to/c0wrk-desktop.app
   ```
   
   Alternatively, right-click `c0wrk-desktop.app` → **Open** on first launch, then confirm in the Gatekeeper dialog.

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

**Runtime dependencies** (end users only need these shared libraries, not the `-dev` packages):

```bash
# Ubuntu/Debian
sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0
```

### Windows (amd64)

1. Download `c0wrk-desktop-windows-amd64.zip` and extract it.
2. The app is unsigned, so Windows SmartScreen may warn "Windows protected your PC". Click **More info** → **Run anyway**.
3. Run `c0wrk-desktop.exe`.

On first launch c0wrk creates its config automatically, then — once all runtime dependencies are in place — opens a dialog to set up an LLM provider.

## Contributing

Interested in hacking on c0wrk? Architecture, build instructions, and the release process live in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Licensed under the [MIT License](LICENSE).

## About

Built by the c0wrk team with warmth, love, and c0wrk.
