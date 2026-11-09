# ADR-092: Embedded LLM Inference on MLX via MTPLX (macOS)

## Status

Accepted — supersedes **D1** (Metal not MLX) of [ADR-066](./066-embedded-llm-runtime.md) **for darwin/arm64 only**. Per [META.md](../META.md) an accepted ADR is immutable, so the superseded decision is named here rather than edited in place, and ADR-066 D1 stands unchanged for every other platform: on Intel Macs, Linux and Windows there is still no MLX path, and the llama.cpp fork remains the only engine. Every other ADR-066 decision — D3 (artifacts outside `tools/`), D4 (hybrid provider representation), D5 (unload = stop the process), D7 (no reasoning-effort knob of its own, as amended), D8 (profile hint only), D9 (the Windows matrix), D10 (as superseded by ADR-067), D11 (pinned artifacts, no auto-update), D12 (no Web UI surface), D13 (the unconditional readiness guarantee) — and all of [ADR-067](./067-memory-aware-embedded-llm-provisioning.md) stand unchanged; the mtplx engine reuses their machinery rather than replacing it.

## Context

ADR-066 D1 chose llama.cpp + Metal because "there is no MLX server for Bonsai 2": the fork's own `start_mplx_server.sh` hard-exited with "serving through them would return wrong output". That premise expired on 22 September 2026, when [MTPLX](https://github.com/youssofal/MTPLX) 2.12.0 shipped a verified MLX path for exactly this checkpoint: GPU kernels for Prism ML's ternary rotation and matrix multiply, the Qwen3.8-27B multi-token-prediction head drafting ahead with exact rejection sampling (correct at any temperature), an exactness baseline against Prism ML's own runtime measured into the pack (`mean KL 4.0e-6` over 1,920 text positions, top-1 agreement 0.999 in float16 production), and an OpenAI-compatible server. On the same M5 Max chip the pack decodes 64.4 tok/s against the fork's 47, in less than half the memory (11.4 GB peak against 23.9).

The forces that shaped ADR-066 still apply and shaped this decision the same way:

1. **The supply chain is still fail-closed.** MTPLX is a Python stack (CPython + `mlx==0.32.2` + forty-four more distributions); a `pip install mtplx` at install time would be an unpinned, index-resolved dependency tree — precisely what ADR-066 D11 forbids for the fork binary.
2. **The engine is a supervised subprocess**, and its state must never mix with an operator's own MTPLX install (`~/.mtplx`).
3. **The weights are 8.85 GB** of MLX-format safetensors (trunk + MTP head + vision tower in one pack), pinned per file.
4. **A wrong-context launch OOMs or starves**, so the memory decision must be made from measurements before any byte is fetched.
5. **The provider machinery must keep working unchanged** — the embedded model is `openai_compatible.embedded` / `Bonsai 2 27B`, and the reasoning wire (`chat_template_kwargs` + native `reasoning_effort`) is already correct for this server.

Two facts made the integration smaller than it sounds: the pinned `python-build-standalone` CPython ships `pip` (26.2.1), so the engine stack is a RELOCATABLE plain-file tree with no venv and no `uv` dependency; and MTPLX accepts any non-retrieval model id in requests (a stale chat id falls through to the served model), so c0wrk keeps sending `Bonsai 2 27B`.

## Decision

`core/embeddedllm/` gains a second runtime family — the **engine** — selected by a new operator setting `embedded_llm.engine: auto|mtplx|llama` (default `auto`: mtplx on darwin/arm64, llama everywhere else). The following decisions are recorded; D-numbers are this ADR's own.

**D1 — the engine is pinned four layers deep.** `mlxpins.go` pins the MTPLX release (`2.12.2`), the managed CPython (`cpython-3.12.15+20261013-aarch64-apple-darwin-install_only.tar.gz`, SHA256 verified by the same Downloader), the **closed wheel set** (46 distributions — `mtplx 2.12.2`, `mlx==0.32.2` (upstream's exactness pin), `mlx-metal 0.32.2`, `mlx-lm 0.31.3`, `transformers 5.16.1`, `llguidance 1.9.1`, … — each with its `files.pythonhosted.org` URL, SHA256 and exact size, resolved once at bump time by `uv pip compile --generate-hashes` for cp312/aarch64-apple-darwin at deployment target 14.0 and reduced to the one wheel that platform installs), and the weight pack (HF revision `03bd60bb…`, per-file SHA256: LFS OIDs for the three LFS files, content hashes captured from the revision for the rest). No upstream version queries, no auto-updates; a bump re-records the whole table. `pip` runs `--no-index --no-deps --only-binary=:all: --require-hashes --find-links <verified downloads>` against a requirements file generated from the pin table itself (`name==version --hash=sha256:…` per wheel) — the engine cannot resolve a single byte the registry did not pin, enforced by the downloader AND again by pip.

**D2 — the engine tree is relocatable plain files, rebuilt in place.** The llama path's staging→retire→promote swap protects a live BINARY; the mtplx stack has no binary to sign and no venv to break (pip installed into the managed CPython directly), so the tree is built in a work directory, smoke-tested (the pinned engine imports and reports its version in the provisioned interpreter — deliberately NOT importing MLX/Metal: the load path's readiness poll is the proof the GPU stack runs), and renamed into place in one final atomic swap after the step-0 stop guarantees nothing is resident. A failed rebuild never deletes the previous working engine tree — the old tree is removed only by that final swap, so an existing install keeps its record and registration and the previous engine keeps working; a first-time failure (nothing on disk yet) leaves no install record and no partial registration. The downloads survive as the verified cache.

**D3 — state isolation is environmental.** The child runs with `MTPLX_CONFIG=<runtimeDir>/config.toml` and `MTPLX_MODEL_DIR=<runtimeDir>`: an operator's MTPLX app, cache and models are invisible to this engine and vice versa. The model is passed as the verified pack DIRECTORY (`--model <MLXModelRoot>/pack`), never an HF id — the runtime cannot resolve it to different bytes, and nothing ever runs `mtplx pull`.

**D4 — the memory decision is one number, from the pack's own measurements.** MLX on Apple Silicon has no device/host split to plan, no offload, no fit pass and no KV-precision choice: the only decision is the context window, and `PlanMLX` (mlxplan.go — pure, import-banned from I/O like `Plan`) computes it from the pinned pack's `mtplx_runtime.json` memory evidence: engine budget = ¾ × physical RAM (the measured fraction on every class: 16 GiB → 12.0, 18 → 13.5, 24 → 18.0; Apple's nominal GB are the binary GiB the probe reports); fixed footprint = weights 8,834,412,216 B + runtime transients 3,489,660,928 B + session bank 23,957,832 B; KV counted at its DENSE 65,536 B/token (quantization can only widen the margin). The three measured RAM classes take the pack's own resolved windows verbatim (8,192 / 20,480 / 94,208); every other size takes the arithmetic ceiling with a ⅛ policy margin, snapped to 4,096 granules, clamped to [8,192; 262,144]. The gate is one unified-pool comparison and its refusal (`MLXInsufficientMemoryError`) carries the arithmetic and unwraps to BOTH `ErrMemoryPlanInfeasible` and `ErrInsufficientMemory`. The window is launched explicitly (`--context-window` — the runtime's documented "explicit still wins"), the runtime's own admission check is the backstop, and the readback machinery records the effective value from `/health`'s top-level `context_window` (fail-soft, clamped to the launched window) with the same merge-and-clamp semantics as `/props`.

**D5 — the llama override vocabulary does not exist on this engine.** There is no `-ngl`, no fit, no KV type, no packing, no slots. `PlanMLX` refuses every llama-only tuning key BY NAME (`ErrTuningInvalid`), the config load refuses the same list beside `engine: mtplx` (`ForbiddenLlamaTuning` — one list, two layers, no drift), `LaunchSpec.Validate` refuses llama fields on an mtplx spec, and `context.mode: exact` keeps working. `Packing` stays empty with `PackingReason "mlx_pack"` explaining the absence.

**D6 — supervision is shared, the shaped halves are separate.** The single-instance gate, readiness polling, crash detection, idle unload, stop budgets and the ensure-loaded transport are runtime-agnostic and untouched. The engine-shaped halves are: argv (`mlxArgs` — `-I -m mtplx.cli serve --model <packDir> --host 127.0.0.1 --port N --context-window N --rate-limit 0`; the isolated `-I` interpreter form was chosen over the pip console script because the script's shebang pins the staging tree's absolute interpreter path, which the promote rename invalidates), validation (`mlxValidate`), launch identity (`launchSpecMLX` — interpreter + pack dir, both containment-checked: `OwnsMLXPack` is the weights half, the interpreter derives from the runtime tree), readiness (`/v1/models` with an added WINDOW requirement — the mtplx listing names the configured model before its backend resolves a context) and the readback (`/health` instead of `/props`). `Args` remains the llama argv's ONLY home: the rendering guard's ratchet treats `Engine` like `ServerBinary` — identity that selects the renderer in `spawn`, never an argument.

**D7 — the reasoning wire and the provider representation are unchanged.** MTPLX reads the same wire the fork does: per-request `reasoning_effort` (`low|medium|high|xhigh`; `off`→`low` keeps thinking on) plus `chat_template_kwargs: {"enable_thinking": false}` for the real off — exactly `ReasoningWireChatTemplateKwargs`. Tool calls arrive as OpenAI `delta.tool_calls`, vision through the pack's built-in tower (no mmproj artifact; the `projection` removal scope is refused with `ErrNoProjectionOnMLX` and its menu entry self-disables). The model name, the provider entry, the port persistence and the pickers are untouched.

**D8 — attribution is shipped, not just licensed.** MTPLX is Apache-2.0 with a NOTICE clause requiring "Powered by MTPLX" inside the product. The Settings install record renders that line — and the project link — when, and only when, the engine on disk is mtplx; the pack's `NOTICE.txt` is installed with the weights.

**D9 — macOS 14+ is a real floor.** The pinned `mlx` wheel's lowest platform tag is `macosx_14_0_arm64`; the engine is pinned for darwin/arm64 and refused everywhere else (`ErrEngineUnsupportedPlatform`, with `engine: llama` named as the alternative).

## Consequences

**Easier**

- Apple Silicon users get ~1.4× decode throughput and half the memory footprint of the Metal path; 16 GB Macs gain a working 27B local model at all.
- One machine shape (unified pool) makes the memory gate strictly simpler than ADR-067's two-pool arithmetic — and the pack ships its own measured evidence, so the planner's constants are upstream's, not ours.
- The engine stack needs no Homebrew, no system Python and no `uv`: one pinned tarball plus pinned wheels.

**Harder / costs**

- Two engines now share the subsystem: every new embedded-LLM feature must answer "which engine does this apply to, and where is that refused?" (the vocabulary, the validators and the tests are arranged so the answer is structural).
- The wheel table is a second pin surface a bump must re-record in full (46 entries) — the non-vacuous validator and the shape tests (46 wheels, 16 pack files, exact weights byte-total) are the guardrails.
- MTPLX is a fast-moving upstream; the pin makes that a reviewed bump, not a drift.

## Alternatives Considered

- **Plain `mlx-lm`**: it cannot run this checkpoint — the ternary container `prism_hadamard_qwen35` needs MTPLX's kernels; serving it through stock MLX is the "wrong output" failure ADR-066 D1 originally refused.
- **Keep llama.cpp on Metal**: forgoes the measured 64.4 vs 47 tok/s and the 16 GB tier; the fork remains the engine for every non-Apple-Silicon machine.
- **Let MTPLX download its own model (`mtplx pull`)**: an unpinned HF fetch outside the registry — rejected on the ADR-066 D11 posture.
- **Replicate MTPLX's context planner exactly**: its per-chip policy (wired limits, bank floors) is not in the pack contract; c0wrk computes conservatively from the published measured terms, launches explicitly, and lets the runtime's own admission re-check backstop.

## Related

- [domains/embedded-llm.md](../domains/embedded-llm.md) — the engine vocabulary, flows and invariants
- [ADR-066](./066-embedded-llm-runtime.md) — the subsystem; D1 superseded for darwin/arm64, the rest standing
- [ADR-067](./067-memory-aware-embedded-llm-provisioning.md) — the llama-family planner this ADR mirrors with `PlanMLX`
- [MTPLX](https://github.com/youssofal/MTPLX) — the upstream engine; NOTICE attribution reproduced in the Settings install record
