# c0wrk

> [!NOTE]
> **Stable beta** — c0wrk is stable enough for everyday work, but you may still run into bugs.
> Features, APIs, and configuration formats can change between releases.
> If something breaks, please report it — every report helps us make c0wrk better.

c0wrk is for work that won't fit in a single reply — research, coding, writing, anything that takes more than one step to get right. It's a desktop app built so the whole loop — plan, implement, review, revise, and ship — feels like one continuous flow instead of a dozen disconnected tools. And you never hand over control blindly: you review the plan before work starts and approve your definition of "done" before the goal loop runs.

![c0wrk main view](docs/screenshots/main_view.png)

## What makes c0wrk different

- **One place for the whole build loop.** From a task to a shipped change without switching tools: review the plan, let c0wrk implement in parallel, review real diffs with per-hunk and per-file comments, request revisions, then stage, commit, push, inspect branches, and browse a lane graph of history.
- **A plan you review before work starts.** For non-trivial work, c0wrk maps the task to a graph-based plan. Independent steps run as isolated subagents in parallel; pause creates a resumable checkpoint (ordinary, goal, and delegated work), graceful app shutdown preserves active work, and successful steps are reused on retry while failed, pending, or targeted steps run again.
- **Goals that run to the finish.** With the `goal` option, c0wrk commits to a concrete, checkable definition of "done" that you approve up front, then keeps working and verifying progress with evidence until the goal is met, blocked, out of budget, or paused. Where other tools' goal modes re-check a condition you typed, c0wrk makes the success condition itself an approved contract before work starts.
- **Built for more than code.** **CODE** works in a real project with file edits, diffs, git, semantic search, and a per-session terminal. **CHAT** gives an isolated scratch workspace without git or code-modifying tools. With **Experimental Features** enabled, **RESEARCH** adds a workspace-contained `.research` methodology, hypothesis graph, metrics, and seeded skills.
- **Layered safety, not just a sandbox.** Every tool belongs to a capability group with `allow`, `user_confirm`, or `deny` policy; mutating groups confirm by default. Path containment, symlink checks, execute blacklists, untrusted-content framing, and an optional conservative Smart Approve judge add independent gates. See [SECURITY.md](SECURITY.md).
- **Your model, your machine.** Works with Anthropic, OpenAI, ChatGPT, and OpenAI- or Anthropic-compatible endpoints. Hybrid semantic + lexical search respects `.gitignore` and `.aiignore`, skips oversized or pathological files, and runs embeddings locally through bundled ONNX Runtime.
- **Self-hosted and corporate-friendly.** HTTP/HTTPS proxy support with custom CA certificates and bypass lists for outbound LLM, web, MCP, model-registry, and update traffic. An optional, experimental Small-LLM profile narrows the tool set, swaps in a compact system prompt, tightens sampling, and compacts context earlier for small or local models.

## How c0wrk compares to other desktop agents

Compared in **August 2026**; desktop features move fast, so verify against each product's current docs.

| | **c0wrk** | **OpenCode Desktop** | **Codex Desktop** | **Claude Code Desktop** |
| --- | --- | --- | --- | --- |
| **Scope** | Research + coding + writing | Code-focused | Code-focused | Code-focused |
| **Plan review** | Reviewable graph (DAG) of parallel subagents | No reviewable plan graph | Summary pane / proposed plan | Linear, read-only Plan mode |
| **Goal mode** | Up-front approved "done" + evidence self-verification | — | `/goal` persistent objective | `/goal` per-turn condition |
| **Full build loop in one place** | Native: plan → implement → review → revise → commit/push | Partial | Partial | Strong diff review |
| **Model freedom** | Any OpenAI/Anthropic-compatible + local | Any model | OpenAI only | Anthropic only |
| **Local embeddings** | Bundled ONNX (offline) | Local-first | Cloud | Cloud |
| **License** | Open source (MIT) | Open source (MIT) | Proprietary | Proprietary |

## Download & install

Prebuilt desktop builds are published on the [GitHub Releases](https://github.com/v0lka/c0wrk/releases) page. Builds are currently **unsigned** — see the per-OS notes below for the one-time workaround.

Every release also publishes `SHA256SUMS`. The in-app updater downloads over HTTPS, refuses an archive with a missing or mismatched SHA256 entry, stages an install-tree swap, and keeps a `.old` rollback copy. SHA256 proves that the downloaded archive matches the bytes published with the release; because the artifacts are unsigned, it does **not** prove release authorship if the release account and checksums are compromised. Updates and automatic checks can be disabled independently in Settings or under `updates` in `~/.c0wrk/config.yaml`.

Each release bundles four platform archives:

| Artifact                           | Target                |
| ---------------------------------- | --------------------- |
| `c0wrk-desktop-macos-arm64.zip`    | macOS (Apple Silicon) |
| `c0wrk-desktop-linux-amd64.tar.gz` | Linux (amd64)         |
| `c0wrk-desktop-linux-arm64.tar.gz` | Linux (arm64)         |
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

### Linux (amd64 and arm64)

1. Download `c0wrk-desktop-linux-amd64.tar.gz` and extract it:
   
   ```bash
   tar -xzf c0wrk-desktop-linux-amd64.tar.gz
   ```

2. Run the binary:
   
   ```bash
   ./c0wrk-desktop
   ```

3. If the binary can't find `libonnxruntime.so`, run it from the extraction directory or add that directory to `LD_LIBRARY_PATH`.

**arm64**: download `c0wrk-desktop-linux-arm64.tar.gz` instead; the steps are
identical to amd64, including the `LD_LIBRARY_PATH` note if the binary cannot
find `libonnxruntime.so`.

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
