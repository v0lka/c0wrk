# ADR-066: Embedded LLM Runtime (Bonsai 2 27B)

## Status

Accepted

## Context

c0wrk can talk to any OpenAI-compatible endpoint, but a user who wants a **local, private** model still has to install, configure and keep running a third-party server (LM Studio, Ollama, vLLM, a hand-built `llama-server`). That is the single biggest barrier to running the agent fully offline: the app itself is one download, the model host is a separate product with its own setup, its own GPU flags and its own context-window guesses.

The decision to make was whether c0wrk ships **one supported embedded model** that the app itself downloads, provisions for the machine it runs on, supervises as a local inference server, and registers as an ordinary provider — so "use a local model" becomes one button in Settings rather than an external product.

The chosen model is **Ternary-Bonsai-2-27B** (derived from Qwen3.8-27B), distributed in the ternary packings `PQ2_0` (7,206,168,928 B ≈ 6.71 GiB) and `PTQ1_0` (5,946,648,928 B ≈ 5.54 GiB) plus the vision projector `mmproj-Q8_0` (629,246,976 B ≈ 600 MiB). Total footprint is ~7.3 GiB (`PQ2_0` + mmproj) or ~6.1 GiB (`PTQ1_0` + mmproj) before the runtime.

Forces that shaped the decision:

1. **These packings require a fork.** Stock llama.cpp does not support `PQ2_0`/`PTQ1_0`: it rejects them outright, and on plain `Q2_0` it loads but **silently produces garbage** — the worst failure mode, because nothing tells the user the output is wrong. The PrismML-Eng/llama.cpp fork implements the ternary kernels, so the runtime is a pinned fork release, not upstream.
2. **There is no MLX path for this model.** The fork's `start_mlx_server.sh` hard-`exit 1`s with "No MLX server for Bonsai 2 yet … serving through them would return wrong output". Metal, by contrast, is supported for both packings and is where the model was measured (28.1 tok/s on M5 Pro, 47 tok/s on M5 Max).
3. **The artifact is 6–8 GiB and user-initiated.** The existing binary-delivery subsystem (tool-manager) is built for startup-critical ≤1 GiB archives and is governed by an offline-first startup invariant.
4. **Anything under `~/.c0wrk/tools/bin` is agent-executable.** `Manager.PrependToPATH()` puts that directory on the agent's `PATH`, so a runtime stored there becomes callable from `bash_exec` — an ASI05 (agent compromise / privilege escalation) surface for a binary the agent has no business invoking.
5. **A local inference server is a network service.** `llama-server` ships a built-in Web UI that carries its own MCP client and its own agentic loop; left enabled on `0.0.0.0` it would be an unauthenticated second agent on the user's LAN.
6. **Hardware varies enormously** (Intel Mac with no usable GPU → Apple Silicon → CUDA 12.4/12.8/13.3 → ROCm → Vulkan → CPU-only), and both the packing and the context window must follow from what the machine can actually hold. A 27B ternary model at the fork's 262144 maximum context is memory-unaware and OOMs once layers are offloaded.
7. **The existing provider machinery must keep working.** The router, the lazy local-model context probe, the settings model picker and the chat model picker all read `llm.*` config; a bespoke provider type would fork all four.

Four of the areas this decision touches had **no spec coverage at all** — supervision of a long-lived local inference server, multi-gigabyte resumable downloads, localhost port allocation for an app-managed server, and a hardware probe for inference (RAM tiering + GPU backend selection; [ADR-045](045-gpu-embedding-provider.md) covers only ONNX/CUDA detection for embeddings). This ADR plus [domains/embedded-llm.md](../domains/embedded-llm.md) close those four gaps.

## Decision

c0wrk gains a **self-contained embedded-LLM subsystem** in `core/embeddedllm/` that downloads a pinned fork runtime plus pinned model weights, provisions them for the detected hardware, supervises `llama-server` on `127.0.0.1`, and exposes the result as an ordinary OpenAI-compatible provider named `embedded` with the single model `Bonsai 2 27B`.

The recorded decisions (`D1`…`D12` match the approved roadmap's numbering, so later specs and code can cite them; `D13` was added by the follow-up that made the readiness guarantee unconditional):

**D1 — macOS runs on llama.cpp + Metal, not MLX.** There is no MLX server for Bonsai 2 (the fork's `start_mlx_server.sh` exits 1 and warns that serving through MLX "would return wrong output"). Metal is supported for both packings and is the configuration the model's throughput was measured on. Consequence: **one code path for all three operating systems** — the same fork runtime, the same `llama-server` flags, the same supervision — with the accelerator selected at probe time rather than a per-OS server implementation.

**D2 — the packing follows the detected backend.** `PQ2_0` (6.71 GiB) is the default; `PTQ1_0` (5.54 GiB) is chosen on Vulkan, which has no `PQ2_0` kernels, and whenever memory is short. The vision projector `mmproj-Q8_0` (600 MiB) is always installed, so image input works regardless of backend. Because the packing is a *resolution outcome* and not a user choice, the Settings UI shows an **informational label of the packing actually selected** (plus backend, context size and port) so the user can see what they got.

**D3 — artifacts live outside `tools/`.** Runtimes go to `~/.c0wrk/runtimes/llama-<tag>-<backend>/` and weights to `~/.c0wrk/models/bonsai-2-27b/`. Nothing is ever placed under `<toolsDir>/bin`, because that directory is prepended to the agent's `PATH` (`Manager.PrependToPATH()`) and would make the inference runtime directly invokable from `bash_exec` (ASI05). The embedded runtime is a c0wrk-supervised subprocess, never an agent tool.

**D4 — hybrid provider representation.** A dedicated `embedded_llm:` config section owns install/runtime state (installed flag, packing, backend, port, model file, runtime version, install time, auto-unload). From that authoritative state the **backend itself generates and protects** the `llm.openai_compatible.embedded` provider entry (`base_url: http://127.0.0.1:<port>/v1`, empty API key, `models: ["Bonsai 2 27B"]`). Protection is required because `UpdateLLMConfig` replaces the whole `openai_compatible` map when the request carries one: the backend re-injects `embedded` from the authoritative state after building the candidate, so a settings save cannot delete it. The router, the lazy local-model probe and both model pickers therefore work **unchanged** — the embedded model is just another OpenAI-compatible entry.

**D5 — unload means stopping the process.** `Load` spawns `llama-server`; `Unload` terminates it gracefully (kill after a timeout). This deterministically returns RAM/VRAM to the system; an in-process unload is not available in the fork's public materials. Process termination also gives crash isolation: a segfault in the inference server cannot take the app down.

**D6 — hard 16 GiB RAM floor.** Install refuses below 16 GiB of system RAM with an actionable, typed error. There is deliberately **no 8–16 GiB tier**: a 27B ternary model below that line does not degrade gracefully, it thrashes or OOMs, and a slow wrong answer is worse than a clear refusal.

**D7 — reasoning effort is not set.** The server's own default applies; the user changes it through the existing mechanism (`HandleOptions.ReasoningEffort` / the UI selector). The subsystem adds no second knob for the same thing.

**D8 — the Model Profile is a hint only.** `suggestModelProfileID` gains an explicit mapping `Bonsai 2 27B → qwen3.8-27b` (the model is derived from Qwen3.8-27B, whose predefined profile encodes the right sampling/loop-hardening knobs); the existing name normalization cannot produce that match. Nothing is auto-applied: installing the embedded model never flips `model_profiles.enabled` and never changes `active_profile`, preserving the standing invariant that `suggested_profile_id` is a hint, never auto-applied.

**D9 — Windows gets the full backend matrix.** CPU, Vulkan, CUDA 12.4/12.8/13.3 and HIP/ROCm. CUDA builds need the paired `cudart-llama-bin-win-cuda-{12.4,13.3}-x64.zip` (~391/390 MB) as a **second component with its own progress bar and its own checksum**. Windows arm64 is out of scope: `PlatformTriple()` maps only darwin amd64/arm64, linux amd64/arm64 and windows amd64 (any other platform receives a synthetic fallback triple with no registry URLs or checksums), so there is no windows-arm64 runtime artifact to pin.

**D10 — the context window is RAM-tiered and never `-c 0`.** The fork's maximum (262144) is memory-unaware and OOMs with layer offload, so the demo scripts' tiering is adopted: ≤11 GiB → 8192, ≤23 → 16384, ≤35 → 32768, ≤71 → 65536, >71 → 131072 (KV cache ≈ 64 KiB/token). The resolved tier is also written as the `llm.models` `context_window` override — deterministically, without probing, because the server may not be running when config is read — which keeps token budgets honest while preserving the documented precedence (config override > probe > static catalog).

**D11 — the fork release is pinned: `prism-b10709-9a9394a`.** The subsystem performs **no upstream version queries and no auto-updates**; the runtime and every checksum are compile-time pins, bumped by hand with a CVE review, exactly like the tool-manager's supply-chain posture. Runtime-archive checksums are taken from the GitHub REST per-asset `digest` field; model checksums are the Hugging Face LFS OID (which *is* the SHA256).

**D12 — the built-in Web UI is disabled and the bind is strictly `127.0.0.1`.** Left on, `llama-server` publishes a chat UI with its own MCP client and agentic loop; on a non-loopback bind that is an unauthenticated agent reachable from the LAN. The embedded server is an inference endpoint for c0wrk and nothing else.

**D13 — readiness is a precondition of every request, and the load is not charged to it.** Three properties, adopted after the first implementation showed all three were needed:

1. *No LLM request may start before the model is resident.* The gate is therefore installed where it cannot be missed. `OrchestratorBuilder.buildRouter` is the one place every router is constructed, and it resolves the seam from a builder-level default (`SetEmbeddedLLM`) whenever the `BuilderConfig` carries no `Loader`. Per-config injection alone is insufficient: the per-session orchestrator is built from a config converted inside the session factory, which was closed over in `NewApplication` — before any `FrontendAPI`, and so before any supervisor, existed — and that router silently carried no transport at all, dispatching chat requests to a loopback socket nothing was listening on. A guarantee that has to be remembered at every conversion site will eventually be forgotten at one; the failure is a connection error, not a warning.
2. *The load must not consume the request's own budget.* `http.Client.Timeout` covers the whole exchange, gate included, and `timeouts.llmRequestTimeout` (600 s) is shorter than the supervisor's ready allowance (15 min) — so a client-level timeout lets a legitimate cold load eat the generation's budget entirely. The embedded entry's client therefore carries **no** `Timeout`; `EnsureLoadedClient` moves that budget into the transport, which arms it on the request only once the model can answer and releases it when the response body closes. This narrows the [llm-providers.md](../domains/llm-providers.md) invariant rather than breaking it: an entry client must not cap inference at the 30 s proxy budget, and it does not — the budget survives, it simply starts later.
3. *Callers that arm their own short deadline must gate before arming it.* The transport gates inside `Do`, which is too late for the one-shot service requests: they create a `serviceLLMRequestTimeout` context (120 s) first and would spend it on the load. `OptimizePrompt`, `GenerateCommitMessage` and session-title generation call the gate first and skip the request when it fails, so a cold model costs a wait, not a lost title.

The alternative — gating only in the transport — was the original design and is kept as defence in depth (it still covers the judge, retries, streaming and any future caller), but on its own it satisfied the letter of the requirement while violating its purpose.

### Not a tool-manager extension

`ManagedTools()` / `core/toolmanager` is **not** reused — not its registry, not its downloader, not its directory. The reasons are the recorded limits and invariants of that subsystem, each of which this artifact violates:

| tool-manager constraint | Value | Embedded LLM requirement |
| --- | --- | --- |
| `maxDownloadBytes` (pre-verification disk-exhaustion guard, `core/toolmanager/install.go`) | **1 GiB** per archive | a single 6.71 GiB model file |
| `maxExtractEntryBytes` (zip-bomb guard, `core/toolmanager/install.go`) | **512 MiB** per decompressed entry | runtime archives and weights above it |
| Download HTTP client timeout (`core/toolmanager/download.go`, `manager.go`) | **5 minutes** for the whole transfer | hours on a slow link, with resume across restarts |
| Pre-download disk guard (`checkDiskSpace`) | **200 MiB** free | ~7.3 GiB + runtime, sized per artifact |
| Startup invariant ([ADR-032](032-offline-first-tool-reconciliation.md)) | startup is local-disk-only; the network runs in a background pass and never blocks or degrades startup | a 6–8 GiB download that must never start implicitly — it runs only on an explicit user click |
| `Manager.PrependToPATH()` → `<toolsDir>/bin` | every managed binary is agent-resolvable by name | the inference runtime must **not** be agent-invokable (D3, ASI05) |

What *is* reused is the **pattern**, not the code: compile-time pins with no upstream queries, fail-closed SHA256 verification (a missing or empty checksum refuses rather than accepting unverified bytes — ASI04), and "secure the replacement bytes on disk before destroying anything old". `core/embeddedllm/download.go` is therefore a separate downloader: HTTP `Range` resume, throttled progress callbacks (~100 ms), context cancellation, no size ceiling, and a disk guard sized to the actual artifact.

## Consequences

**Positive:**

- "Run fully local" becomes a single Settings action; no third-party server product, no manual GPU flags, no guesswork about context size.
- One code path across macOS/Linux/Windows: the accelerator is a probe result, not a per-OS implementation, so behavior and flags stay reviewable in one place.
- The embedded model is an ordinary `openai_compatible` entry, so the router, the lazy context probe, both model pickers, session pinning, TLS/proxy handling and every existing provider test keep working untouched.
- Process-per-model gives a clean resource story: unload (manual or idle-timed) provably returns RAM/VRAM, and an inference crash is a status + event, not an app crash.
- Loopback-only with the Web UI off keeps the local attack surface to c0wrk's own client; no unauthenticated chat UI or MCP client appears on the machine.
- Supply-chain posture matches the tool-manager: pinned fork release, fail-closed checksums from GitHub `digest` / HF LFS OID, manual bump with CVE review, no auto-update.
- Closes four previously undocumented areas, so future work has a spec to consult instead of re-deriving the hardware matrix.

**Negative / trade-offs:**

- A second delivery subsystem with its own downloader, disk layout, manifest and progress plumbing — parallel code that must be kept honest alongside the tool-manager rather than folded into it.
- ~7.3 GiB of disk per install, and a download that can span hours; a partial transfer must be resumable or the feature is unusable on bad links. Whether the Hugging Face resolve URL honours `Range`/`206` is the first thing the implementation must confirm; if it does not, the fallback is a full restart **with an explicit message**, never a silent partial.
- Two model packings and six backend variants multiply the artifact matrix; a machine whose backend changes (driver update, new GPU) needs the other packing downloaded.
- The runtime is a fork. c0wrk inherits its bugs and its release cadence, and every bump is a manual CVE-reviewed pin change.
- The `openai_compatible.embedded` entry is backend-owned state materialized into a user-editable map, so every write path that replaces that map must re-inject it — an invariant that has to be regression-tested, not assumed.
- A localhost port is now persisted app state: collisions must be re-checked at every load, and the port must survive config edits.
- First-token latency on a cold model is the server's weight-load time; the request path has to absorb it (bounded, coalesced ensure-loaded) or the first message looks hung. The idle timer must not count that load against the user's inactivity budget.

## Alternatives Considered

- **MLX server on macOS** — rejected: no MLX server exists for Bonsai 2 (the fork's script exits 1 and warns of wrong output), and it would fork the code path per OS for no measured gain over Metal.
- **Stock llama.cpp** — rejected: `PQ2_0`/`PTQ1_0` are unsupported, and `Q2_0` loads while silently emitting garbage. Silent wrongness is disqualifying.
- **Extend the tool-manager (`ManagedTools()` + `toolmanager.Download`)** — rejected on the recorded limits and invariants (1 GiB download cap, 512 MiB extract-entry cap, 5-minute HTTP timeout, 200 MiB disk guard, offline-first startup, `tools/bin` PATH exposure). Raising those caps would weaken the guarantees the startup-critical tools depend on; see "Not a tool-manager extension".
- **Store under `~/.c0wrk/tools/`** — rejected: `<toolsDir>/bin` is prepended to the agent's `PATH`, making the runtime agent-invokable (ASI05).
- **In-process bindings (cgo llama.cpp) instead of a subprocess** — rejected: no in-process unload is available in the fork's public materials, so freeing 6–8 GiB of weights would require restarting the app; a subprocess also isolates inference crashes and lets the OS reclaim VRAM deterministically.
- **A first-class `embedded` provider type in the router** — rejected: it would fork the router, the lazy context probe and both pickers. Representing it as `openai_compatible` (D4) reuses all of them.
- **Leaving the built-in Web UI enabled** — rejected: it is a chat UI with its own MCP client and agentic loop; c0wrk must not spawn a second, unmanaged agent surface.
- **Binding `0.0.0.0` (LAN access)** — rejected: an unauthenticated inference endpoint on the local network, with no auth story for the fork's server.
- **Default context `-c 0` / 262144** — rejected: memory-unaware and OOMs with `-ngl`; the RAM-tiered table (D10) is the demo scripts' own mitigation.
- **An 8–16 GiB RAM tier** — rejected: no packing degrades gracefully there; a typed refusal (D6) is more honest than a thrashing model.
- **Auto-applying the `qwen3.8-27b` Model Profile on install** — rejected: violates the standing "suggested_profile_id is a hint, never auto-applied" invariant; the mapping stays a suggestion (D8).
- **Auto-update / upstream version queries for the runtime** — rejected: reopens exactly the supply-chain surface the pinned registry exists to close (D11).
- **Bundling the weights in the release archive** — rejected: it would multiply every platform artifact by ~7 GiB and force the model on users who never enable it.
- **Reusing `<agentDir>/models/` root directly for the weights** — rejected as a layout: that directory already holds the flat embedding-model files resolved by `desktop/startup.go` `resolveModelPath`. The weights live in the dedicated `bonsai-2-27b/` subdirectory so `Remove` deletes only that subtree and never the embedding model, and the flat-file resolver stays unaffected.

## Related

- [domains/embedded-llm.md](../domains/embedded-llm.md) — the domain spec this ADR introduces: supervision state machine, download/verify contract, port allocation, hardware probe and resolution tables.
- [domains/tool-manager.md](../domains/tool-manager.md) — the subsystem deliberately *not* reused; its limits and offline-first invariant are the reason.
- [ADR-010: Tool Manager for External Binary Dependencies](010-tool-manager.md) — the pinned-registry, no-auto-update pattern this subsystem copies.
- [ADR-032: Offline-First Tool Reconciliation](032-offline-first-tool-reconciliation.md) — the startup invariant an on-click 7 GiB download must not violate.
- [ADR-045: GPU Embedding Execution Provider](045-gpu-embedding-provider.md) — the only prior hardware-detection decision (ONNX/CUDA for embeddings); the inference probe here is broader.
- [ADR-023: Self-Update](023-auto-update.md) — SHA256-only fail-closed artifact verification precedent (ASI04).
- [ADR-054: Per-Provider TLS Pinning](054-per-provider-tls-pinning.md) — the `ProviderEntry.HTTPClient` hook the ensure-loaded transport shares.
- [domains/llm-providers.md](../domains/llm-providers.md) — provider config, the lazy context probe and the context-window precedence this decision writes into.
- [domains/model-profiles.md](../domains/model-profiles.md) — the hint-only `suggested_profile_id` invariant D8 preserves.
- [architecture/security-model.md](../architecture/security-model.md) and `SECURITY.md` — ASI04 (supply chain) and ASI05 (agent-invokable binaries) constraints behind D3, D11 and D12.
