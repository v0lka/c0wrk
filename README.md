# c0wrk

> [!WARNING]
> **Early Alpha Stage** — This project is under active development and not yet stable.
> Features, APIs, and configuration formats may change without notice.
> Use at your own risk.

A desktop AI agent for work that won't fit in a single reply — research, coding, writing, anything multi-step.

![c0wrk main view](docs/screenshots/main_view.png)

## What makes c0wrk different

- **Plans before it acts.** For anything non-trivial, c0wrk maps the work to a plan as a dependency graph you can review and approve before it runs a single step — so a wrong approach gets caught in seconds, not after twenty minutes of work. Big jobs get split into isolated sub-tasks handed to sub-agents that run in parallel, each with its own focus, so the independent parts finish together instead of one after another.
- **Goals that run to the finish.** Start a message with `/goal` and c0wrk commits to a concrete, checkable definition of "done" that you sign off on up front — then keeps working turn after turn, checking its own progress, until it can prove the goal is met, it's stuck, the budget runs out, or you hit pause. No stopping at the first plausible-looking answer.
- **Two modes for two kinds of work.** **CODE** points the agent at a real project: full toolset, in-place file edits, inline diffs, and git. **CHAT** is for when you want to reason, research, or draft without touching a codebase — each CHAT session gets its own isolated scratch workspace, code-modifying tools are switched off, and no git is required.
- **The whole loop stays in one window.** Once the code is written you don't switch tools to finish the job. Open the built-in review page and walk every changed file as a real diff, leave comments on individual hunks or whole files, then approve or send it back for another pass — and when it's ready, stage, commit (with a message the model drafts from the diff itself), and push straight from the git panel, no terminal required. Branch info, commit history, and the graph live in the same panel, and the working tree stays in sync so what you review is always what's on disk.
- **Safe by default, never in your way.** c0wrk stays inside your project and checks every risky step *before* it runs, so nothing destructive ever happens by surprise — and an optional AI judge quietly clears the routine calls so you're asked only about what genuinely matters, not every file write. Since the web pages and files it reads can't hijack it either, you can hand it real, messy work and trust it to stay on task.
- **Bring your own model.** Works with Anthropic, OpenAI, ChatGPT and any OpenAI- or Anthropic-compatible endpoint. Set it up once, switch models per task.
- **Knows your codebase.** Semantic + lexical search means c0wrk finds the right file or symbol even when you describe it loosely, not just when keywords happen to match.
- **Plays nice with corporate networks.** HTTP/HTTPS proxy support with custom CA certs and bypass lists.

## Download & install

Prebuilt desktop builds are published on the [GitHub Releases](https://github.com/v0lka/c0wrk/releases) page. Builds are currently **unsigned** — see the per-OS notes below for the one-time workaround.

Each release bundles three artifacts:

| Artifact                            | Target                   |
| ----------------------------------- | ------------------------ |
| `c0wrk-desktop-macos-arm64.zip`     | macOS (Apple Silicon)    |
| `c0wrk-desktop-linux-amd64.tar.gz`  | Linux (amd64)            |
| `c0wrk-desktop-windows-amd64.zip`   | Windows (amd64)          |

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
