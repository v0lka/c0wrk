# ADR-045: GPU Embedding Execution Provider (CUDA, auto with loud fallback)

## Status

Accepted

## Context

The vector-index embedder runs on ONNX Runtime with a local, quantized model. Historically it always executed on the CPU execution provider — indexing loaded the CPU while an NVIDIA GPU sat idle, and nothing in the logs indicated a problem. A research pass (`docs/cuda-embedding-research.md`, PoC verified 2026-09-09/10 on an RTX 5060 Ti, CUDA 13.3) established three facts that shape this decision:

1. **The provider is opt-in API, not library flavor.** ONNX Runtime never selects the GPU on its own; without an explicit `AppendExecutionProviderCUDA` on the session options the graph runs on the CPU EP regardless of which build of `libonnxruntime.so` is installed. The session-options construction lives in sp4rk (`embedding/onnx.go`), so the fix cannot be made from c0wrk alone.
2. **The CUDA EP keeps per-OS-thread state.** Every thread entering `Session.Run` gets its own `PerThreadContext` — a cuBLAS handle with a workspace, ~1 GiB VRAM each. Go migrates goroutines across OS threads freely, so even strictly serialized (mutex-held) inference calls arrive from ever-new threads: measured VRAM climbed from 1755 MiB to 15839 MiB within ~20 rounds and died with `CUBLAS failure 3: the resource allocation failed`. Pinning all ORT touches to one `runtime.LockOSThread` thread holds VRAM flat (~3792 MiB).
3. **The CPU slide can be invisible from inside the process.** In a previous attempt the ORT CUDA library silently fell back to CPU: calls kept succeeding, no error surfaced, and the GPU stayed idle. `onnxruntime_go` (v1.27.0 wrapper) exposes no provider introspection — there is no `GetAvailableProviders` equivalent — so the effective provider is unobservable from Go code. A naive "try CUDA, fall back to CPU" therefore degrades silently, which is unacceptable: a user who installed GPU libraries and got CPU gets no signal.

The PoC measured the payoff: jina-v2-small, batch 32 × 512 tokens — 1.61 s on CPU vs 0.078 s on GPU, ≈20×. Packaging is also nontrivial: the CUDA-flavored ORT release is a ~240 MiB archive whose `libonnxruntime_providers_cuda.so` (280 MB) links against the **system** CUDA runtime (`libcudart.so.13`, cuBLAS, cuRAND — cuDNN is loaded lazily and unused by BERT-like models), so a GPU build only works on a machine with a matching major CUDA version. Finally, the Makefile's ORT install is short-circuited by a version stamp; a hand-installed GPU library stamped `1.28.1-gpu` mismatches `ONNX_VERSION=1.28.1`, and a later `make build` silently overwrites the GPU library with the CPU one.

## Decision

1. **Default `execution_provider: auto`.** New config knobs `vector_index.execution_provider` (`auto` | `cpu` | `cuda`, default `auto`) and `vector_index.device_id` (GPU index, default `0`) in `backend/config`, threaded into sp4rk's `EmbedderConfig` (`ExecutionProvider`, `DeviceID`). `auto` attempts the CUDA provider when the GPU build's provider libraries are present next to the binary and falls back to CPU otherwise; `cpu` forces CPU; `cuda` requests the GPU and, when CUDA init fails, the **c0wrk startup layer** retries the embedder creation on the CPU provider so vector search stays available — but loudly (WARN log, `runtime_error` toast `error_code: vector_cuda_fallback`, requested-vs-effective mismatch in every status payload); at the sp4rk layer an explicit `cuda` remains a hard error (the fallback lives in c0wrk glue, not the library). Unknown provider values are rejected, never degraded (sp4rk `buildSessionOptions`). The PoC-era environment knobs (`C0WRK_ONNX_EP`, `C0WRK_ONNX_DEVICE`) are removed. *(Revised 2026-09-10 per user ruling: fallback-with-loud-notification beats fail-closed — an explicit GPU request must not silently kill vector search, but the warning must be unmissable.)*
2. **Fallback is tracked and warned, never silent — split across the two layers.** Because the silent slide happens inside the ORT wrapper boundary, the `auto` fallback detection and its WARN log live in sp4rk's embedding package (where session creation happens); the explicit-`cuda` fallback lives in c0wrk's startup glue (`desktop/startup_phases.go`), which catches the `NewEmbedder` error, retries on CPU, and additionally emits the `runtime_error` toast. c0wrk's contract is observability: the effective provider is always reflected in the vector-index status payload (requested vs. effective, fallback reason, nvidia-smi `cuda_verified` verdict when the effective provider is `cuda`), preserving a future-UI surface (status payload/getters/config keys) without building UI now.
3. **GPU-execution tests verify externally via `nvidia-smi` PID.** Tests that claim the model runs on CUDA must assert the process's own PID appears in `nvidia-smi --query-compute-apps=pid,used_gpu_memory` — not merely that inference calls succeed. This is the only reliable signal because the wrapper exposes no provider introspection.
4. **All ORT touches under a GPU provider go through the single locked thread.** sp4rk's `ortRunner` (`runtime.LockOSThread`, never unlocked, unbuffered `chan func()`, panic captured on the runner thread and re-thrown to the caller so a panicking job cannot strand future calls) executes environment init, session-options build, session creation, lazy batch-session creation, both inference paths, and teardown on one pinned OS thread. With a CPU provider the runner is nil and every call runs inline — the CPU path is bit-for-bit unchanged.
5. **Packaging: separate opt-in target, cuda13 flavor only.** `make fetch-onnx-gpu` installs the CUDA-flavored ORT (cuda13) with the same fail-closed SHA256 verification as the CPU target; the default `make build` / `make fetch-onnx` path is unchanged and keeps installing the CPU library. One flavor is pinned deliberately: the CUDA provider library hard-links the system CUDA runtime's major version, so a cuda12 flavor would not load on a CUDA 13 machine (and vice versa) — supporting both would double the ~240 MiB artifact and the verification matrix for no benefit on the target hardware.
6. **Stamp policy: the flavor is part of the stamp.** Each fetch target owns its expected stamp value (`1.28.1` for CPU, `1.28.1-gpu` for the CUDA flavor) and short-circuits only on an exact match with its own value. A CPU `make build` therefore never silently reinstalls over a GPU install: the stamp mismatch makes it visible (and the GPU target reinstalls GPU bytes). This closes the PoC-era trap where the stamp said `1.28.1-gpu` while the Makefile compared against `1.28.1` and overwrote the GPU library.

## Consequences

**Positive:**

- GPU indexing is ~20× faster on supported hardware, and `auto` makes it work out of the box for anyone who installs the GPU flavor, with zero effect on default (CPU) installs.
- The effective provider is always observable; a silent CPU slide can no longer hide.
- VRAM stays flat under sustained indexing (single cuBLAS context), so long indexing runs cannot exhaust the card.
- The CPU path (the overwhelming default) is untouched — same inline call pattern, no extra goroutine, no behavior change when no GPU is configured.

**Negative:**

- The GPU flavor requires the system CUDA 13 runtime; users on CUDA 12 (or without a toolkit) must stay on CPU — the `auto` probe handles this, but it is a real constraint.
- The cuda13-only pin means a future CUDA major bump requires a new flavor, new digests, and a stamp-value decision; packaging the GPU flavor into official release archives is explicitly deferred.
- External `nvidia-smi` verification ties the GPU test to NVIDIA hardware presence — such tests must skip (not fail) on machines without an NVIDIA GPU, and cannot run in CI as-is.
- The locked runner serializes all ORT work on one thread: embedding throughput is bounded by a single session (acceptable — the embedder was already mutex-serialized; GPU inference at ~78 ms/batch leaves headroom, and re-measuring the CPU-tuned batch-size plateau is future work, not inherited).
- No UI yet: provider state is reachable only through the status payload/config until a settings surface is built.

## Alternatives Considered

- **Do nothing (CPU-only).** Rejected: a 20× indexing speedup with local-only inference is too large to leave on the table, and the failure mode that motivated the research (silent CPU while GPU idles) is invisible to users.
- **Ship GPU as the only flavor.** Rejected: the CUDA provider adds ~280 MB of libraries and a hard dependency on the system CUDA runtime; forcing it on every install breaks CPU-only and non-NVIDIA machines.
- **Support both cuda12 and cuda13 flavors.** Rejected: doubled artifact size and digest matrix for hardware the project does not target; one pinned flavor keeps verification tractable. Revisit when a real cuda12 need appears.
- **`gpu_mem_limit` / `arena_extend_strategy` to tame VRAM growth.** Rejected (measured dead end): the growth is per-thread cuBLAS contexts, not the allocator arena — `kSameAsRequested` changed nothing (11573 MiB by round 10), and a memory limit would only convert the blowup into an earlier allocation failure.
- **Provider introspection from Go to detect the slide.** Rejected (unavailable): `onnxruntime_go` v1.27.0 exposes no `GetAvailableProviders`/session-provider query; the external `nvidia-smi` PID check is the only trustworthy evidence of GPU execution.
- **Fallback detection in c0wrk glue instead of sp4rk.** Rejected: the silent slide happens below the wrapper boundary at session creation; only the layer that creates the session (sp4rk `embedding`) can distinguish "CUDA session created" from "ORT slid to CPU". c0wrk keeps the observability half (status payload) where it owns the RPC surface.
- **Keep environment-variable knobs (`C0WRK_ONNX_EP`).** Rejected: env knobs are invisible in the settings surface, cannot be persisted per user, and were explicitly PoC-only scaffolding.
