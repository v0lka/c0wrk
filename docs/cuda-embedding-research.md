# Embedder GPU inference: investigation and working PoC

> **Ported into the product on 2026-09-10 — see [ADR-045](../specs/decisions/045-gpu-embedding-provider.md).**
> This document remains the research journal (measurements, dead ends, reproduction);
> product decisions — configuration knobs, fallback semantics, packaging, stamps — are described
> in the ADR and in `specs/domains/workspace.md` (Embedding Execution Provider section).

**Date:** 2026-09-09 (restored 2026-09-10)
**Status:** PoC works and is built. Ported into the product on 2026-09-10 (see ADR-045).
**Machine:** Zabarch — RTX 5060 Ti 16 GB (Blackwell, sm_120) + RTX 2060 6 GB, CUDA 13.3, driver 610.57.04

---

## TL;DR

Indexing ran on the CPU even though a GPU build of `libonnxruntime.so` was sitting right next to
the binary. There turned out to be three causes, and each one alone is enough to keep the GPU from
working:

1. **Nobody enabled the CUDA execution provider.** ONNX Runtime does not pick the GPU by itself —
   without an explicit `AppendExecutionProviderCUDA` it executes the graph on the CPU EP, no
   matter which build of the library is placed next to the app.
2. **The app was loading a different library.** The installed package keeps the CPU build in
   `/opt/c0wrk`, and files in `build/bin` are not read at all when launching via the launcher.
3. **(Found after enabling the EP)** The CUDA EP creates a `PerThreadContext` — a cuBLAS handle
   with its own workspace, ~1 GB of video memory — for **every OS thread** that enters
   `Session.Run`. Go migrates goroutines across threads freely, the map fills up within seconds of
   indexing, and inference dies with `CUBLAS failure 3: the resource allocation failed`.

All three are fixed in the PoC. Measured on jina-v2-small, batch 32 × 512 tokens: **1.61 s on CPU
→ 0.078 s on GPU, ≈20×**. Video memory consumption after the fix is a flat ~4 GB, no growth.

---

## The symptom and what it looked like

Initially: indexing loads the CPU, the GPU idles. No errors in the logs — everything "works".

After enabling the CUDA EP: the embedder starts, sessions get created, the first ~64 embeddings
pass, then an avalanche of identical errors and the index never builds.

```
{"level":"WARN","msg":"per-text embedding failed; document will be dropped",
 "chunk_offset":32,"total":89,
 "error":"embedding document: running ONNX inference: Error running network:
   .../core/providers/cuda/cuda_call.cc:154 ... CUBLAS failure 3: the resource allocation failed ;
   GPU=0 ; hostname=Zabarch ;
   file=.../core/providers/cuda/cuda_execution_provider.cc ; line=231 ;
   expr=cublasCreate(&cublas_handle_);"}
{"level":"WARN","msg":"incremental indexing failed","error":"adding document batch: ..."}
```

The key to the puzzle is `cuda_execution_provider.cc:231`, the `PerThreadContext` constructor. Not
"the model didn't fit" but "resources ran out while creating yet another per-thread context".

---

## Cause 1: the provider is never added

ONNX Runtime uses the CPU execution provider by default. The GPU is enabled only by explicitly
appending the provider to `SessionOptions` before creating the session.

```
             BEFORE                                   NEEDED
   ┌──────────────────────────────┐        ┌──────────────────────────────┐
   │ SetSharedLibraryPath(...)    │        │ SetSharedLibraryPath(...)    │
   │ InitializeEnvironment()      │        │ InitializeEnvironment()      │
   ├──────────────────────────────┤        ├──────────────────────────────┤
   │ opts = NewSessionOptions()   │        │ opts = NewSessionOptions()   │
   │ opts.SetIntraOpNumThreads(N) │        │ opts.SetIntraOpNumThreads(N) │
   │                              │        │ cuda = NewCUDAProviderOpts() │
   │        nothing               │        │ opts.AppendExecutionProvi... │
   ├──────────────────────────────┤        ├──────────────────────────────┤
   │ NewAdvancedSession(..., opts)│        │ NewAdvancedSession(..., opts)│
   └──────────────┬───────────────┘        └──────────────┬───────────────┘
                  ▼                                       ▼
            CPUExecutionProvider                   CUDAExecutionProvider
```

There was no `AppendExecutionProvider` line **anywhere** — neither in c0wrk nor in sp4rk. The only
thing being configured was the number of intra-op threads in
`sp4rk/embedding/onnx.go:buildSessionOptions`, and with `intraOpThreads <= 0` the function
returned `nil` and the session was created with zero options.

The session-creation point is unexported (`buildSessionOptions`, `newONNXSession`), so c0wrk
cannot reach it — **the change has to go into sp4rk**.

---

## Cause 2: the wrong library was being loaded

`resolveONNXLibPath()` in `desktop/startup.go` looks for `libonnxruntime.so` next to the
executable. The `/usr/bin/c0wrk-desktop` wrapper does `exec /opt/c0wrk/c0wrk-desktop`, so
`os.Executable()` points into `/opt/c0wrk` — which holds the CPU build from the
`c0wrk-bin-0.7.3` package (24.3 MB vs 27.6 MB for the GPU build).

**For the PoC, launch `build/bin/c0wrk-desktop` directly**, never the `c0wrk` from the menu. For
the GPU to work through the launcher, `/opt/c0wrk` must contain both the new binary and the GPU
libraries (`libonnxruntime.so` + `libonnxruntime_providers_cuda.so` +
`libonnxruntime_providers_shared.so`).

---

## Cause 3: per-thread CUDA contexts (the main one, and non-obvious)

The CUDA EP in ONNX Runtime keeps separate state per OS thread that calls `Session.Run`: its own
cuBLAS handle with its own workspace. The Go scheduler freely migrates goroutines between threads,
and `EmbedDocuments` is called from different goroutines of the indexer. The `Embedder.mu` mutex
serializes the calls but **does not pin them to a thread** — the calls are strictly sequential and
yet keep arriving from new threads.

Measurement: 60 inferences with batch=32, the only variable being which thread the call arrives
from.

```
        GPU used (MiB), RTX 5060 Ti 16 GB
16000 ┤                    ╭──────────────────  new thread per call
      │              ╭─────╯                    (ceiling → CUBLAS failure 3)
12000 ┤         ╭────╯
      │     ╭───╯
 8000 ┤  ╭──╯
      │╭─╯        ╭──────────────────────────   single thread: plateau 5969
 4000 ┤╯   ╭──────╯
      │────────────────────────────────────────  pinned thread: exactly 3792
    0 ┼────┬────┬────┬────┬────┬────┬────┬────
      0    10   20   30   40   50   60  runs
```

| scenario | GPU used: start → plateau | conclusion |
|---|---|---|
| one OS thread | 1755 → **5969** MiB | the ORT arena grows and stabilizes |
| new thread per call | 1755 → **15839** MiB in ~20 runs | ~1 GB per thread, the card runs out |
| pinned thread, calls from 60 different ones | 1854 → **3792** MiB, flat | the fix |

**The fix:** every touch of ONNX Runtime executes on a single pinned
`runtime.LockOSThread()` thread.

---

## Dead ends (don't waste time on them)

- **`arena_extend_strategy: kSameAsRequested`** — verified, doesn't help. The growth is the same:
  11573 MiB by the 10th run. So it's not the allocator arena that grows, it's the contexts.
- **`gpu_mem_limit`** — didn't try it and don't recommend it as a solution: it caps the arena but
  not the number of cuBLAS handles, and would merely turn the overflow into an allocation failure
  sooner.
- **Installing cuDNN** — **not needed**. Verified: cuDNN isn't in the system at all, the CUDA EP
  loads `libcudnn.so` lazily via `dlopen`, and jina-v2 (BERT-like: MatMul/Gemm/LayerNorm/Softmax)
  has no cuDNN kernels at all. The session is created and computes without it. `ldd` on
  `libonnxruntime_providers_cuda.so` confirms: no cuDNN among `NEEDED`.
- **An ORT environment variable to pick the provider** — there is none. API only.

---

## Verified facts about the environment

- `libonnxruntime_providers_cuda.so` is linked against `libcudart.so.13`, `libcublas.so.13`,
  `libcublasLt.so.13`, `libcurand.so.10` — this is a **CUDA 13** build, matching the installed
  `/opt/cuda` 13.3. A cu12 build would not have come up.
- ORT 1.28.1, `libonnxruntime_providers_shared.so` is next to it — the runtime finds the
  providers by `dlopen`ing them from the main library's directory.
- RTX 5060 Ti (Blackwell, sm_120) is supported: the session is created and computes correctly.
- sha256 of the working GPU build: `4680895afc920629c16fd4aea9d04e1b40cf6d66cbb1495dead4953de9377e6c`
  (27 668 560 bytes). Useful for cross-checking after `make`.

### ⚠️ The version stamp trap

The `Makefile` short-circuits the ORT install by comparing `build/bin/.onnxruntime-version`
against `ONNX_VERSION` (`Makefile:212`). Right now the stamp holds `1.28.1-gpu` while
`ONNX_VERSION` = `1.28.1` — **the values differ, so `make build` would silently download the CPU
archive over the GPU libraries.**

Until this is resolved, build bypassing `fetch-onnx`:

```bash
wails build -tags webkit2_41 -ldflags "-X github.com/v0lka/c0wrk/core/version.Version=$(git describe --tags --dirty) -X github.com/v0lka/c0wrk/core/version.GitCommit=$(git rev-parse --short HEAD) -X github.com/v0lka/c0wrk/core/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

Either bring the stamp back to `1.28.1` (then the guard works again but the marker that this is
the GPU flavor is lost), or — better — teach the `Makefile` a GPU build flavor so the stamp and
the expected value converge. That's part of the packaging task below.

---

## What was changed

### `/home/zab/Git/sp4rk` (not committed, branch off `5e2a034`)

**`embedding/onnx.go`**
- Constants `ExecutionProviderCPU = "cpu"`, `ExecutionProviderCUDA = "cuda"`.
- New signature: `buildSessionOptions(provider string, deviceID, intraOpThreads int)`.
  The early `nil, nil` return now fires **only** for CPU — for CUDA the options are always needed,
  regardless of the thread count.
- Function `appendCUDAProvider(opts, deviceID)`: `NewCUDAProviderOptions` → `Update{device_id}` →
  `AppendExecutionProviderCUDA` → `Destroy`. The provider options are destroyed right after the
  append — ownership is not transferred to the session.
- An unknown provider value is rejected with an error instead of degrading to CPU.

**`embedding/runner.go`** (new)
- `ortRunner`: a goroutine with `runtime.LockOSThread()` (never unlocked), an unbuffered
  `chan func()`, a `do(fn)` method that synchronously executes the job on that thread, and a
  `stop()` that closes the channel.
- A panic inside a job is caught on the worker thread and rethrown to the caller. If it were
  allowed to escape, the worker goroutine would die and every subsequent `do` would hang forever
  on a channel nobody reads. c0wrk relies on its own `recover` in the vector-index goroutine.

**`embedding/embedder.go`**
- Fields `ExecutionProvider string` and `DeviceID int` in `EmbedderConfig`.
- Field `runner *ortRunner` in `Embedder`; created in `NewEmbedder` **only** for the GPU provider,
  stays `nil` for CPU.
- Helpers `runOnORTThread(r, fn)` (a free function, needed before the `Embedder` exists) and
  `(*Embedder).onORTThread(fn)`. With a `nil` runner they call `fn()` directly — the CPU path is
  unchanged.
- **All** ORT touches go through the runner: `initONNXRuntime`, `buildSessionOptions`,
  fast-path session creation, lazy batch-session creation (`ensureBatchSession`), both inference
  paths (`e.sess.run` for a single text and `e.batchSess.runBatch` for a batch), and the whole
  teardown in `Close` as one job — after which the runner is stopped.
- Provider and device id made it into the `embedder initialized` log.

**`embedding/runner_test.go`** (new) — 4 tests: starting the runner, executing a job, panic
propagation, stopping.

**`embedding/embedder_test.go`** — the existing `TestBuildSessionOptions_*` rewritten for the new
signature, plus `TestBuildSessionOptions_UnknownProvider` added.

### `/home/zab/Git/c0wrk` (not committed, branch `embedding-gpu-support`)

**`desktop/startup_phases.go`** — in `startVectorIndexBackground`, `C0WRK_ONNX_EP` and
`C0WRK_ONNX_DEVICE` are read (via `strconv.Atoi`, 0 on error) and passed into `EmbedderConfig`.
The embedder-creation error log was extended: provider, device id, library path.

**`go.work`** (new, in `.gitignore`) — per ADR-031 it lives in the c0wrk root:

```
go 1.27.1

use (
	.
	../sp4rk
)
```

---

## How to build and run

Building — see the version stamp trap above. Running:

```bash
C0WRK_ONNX_EP=cuda ./build/bin/c0wrk-desktop
```

- `C0WRK_ONNX_EP` — `cuda` or `cpu`. Unset = CPU, the previous behavior.
- `C0WRK_ONNX_DEVICE` — GPU index, default `0` (RTX 5060 Ti). `1` is the RTX 2060.
- No CPU fallback by design: on a CUDA failure vector search is disabled and a
  `vector search unavailable` with the ONNX Runtime error text lands in the log.

**Signs of success:** an `embedder initialized` with `executionProvider=cuda` in the log; no
`per-text embedding failed` and no `vector indexing failed`; in `nvidia-smi` the `c0wrk-desktop`
consumption plateaus at ~2–4 GB and **does not grow** further.

---

## What is verified and what is not

**Verified:**
- The CUDA EP comes up on this machine, inference is correct (meaningful output values).
- CPU vs GPU measurement: 1.61 s vs 0.078 s on a 32×512 batch, ≈20×.
- The fix keeps memory flat: 20 rounds × 89 documents (exactly the load shape that crashed in the
  log) from 20 different OS threads — a stable ~4030 MiB, not a single failure.
- The CPU path is untouched: the same run without `C0WRK_ONNX_EP` doesn't touch the GPU.
- `gofmt`, `go vet ./...`, `go test ./...` — clean in both repositories.

**Not verified:**
- `make lint` — `golangci-lint` isn't installed on the system. Run before porting into the
  product.
- Live indexing of a large project from the UI after the fix — it was tested on an isolated
  embedder run, not on the full app.
- Behavior on a machine without a GPU/driver with `C0WRK_ONNX_EP=cuda` — a loud error and disabled
  vector search is expected, but it wasn't tested.
- Windows and macOS weren't touched.

---

## What is needed to port into the product

### Mandatory

1. **Thread pinning is not an optimization, it's a working precondition.** Without it CUDA looks
   healthy exactly until the first indexing. This is the first thing to protect with a test during
   any refactor.

2. **CPU fallback.** Right now any `NewEmbedder` error disables vector search entirely. In the
   product it needs to be: try CUDA → fails → log the reason → recreate the embedder on CPU.
   Be careful with the `sync.Once` in `initONNXRuntime`: **the ORT environment is initialized once
   per process and cannot be reinitialized**, the first `libraryPath` is final. So the fallback
   must recreate only the `SessionOptions` and the sessions, not the environment.

3. **A config knob instead of env.** The obvious choice is `vector_index.execution_provider` and
   `vector_index.device_id` next to the existing `embedding_threads` / `embedding_batch_size`
   (`backend/config/config.go`). Requires: validation, defaults in `ApplyDefaults`,
   `config.example.yaml`, config tests, domain spec update.

4. **Packaging.** The `Makefile` pulls `onnxruntime-linux-x64-$(ONNX_VERSION).tgz` (CPU) with a
   pinned sha256. The GPU flavor is `onnxruntime-linux-x64-gpu-1.28.1.tgz`; the
   `libonnxruntime_providers_cuda.so` alone is 279 MB. Bundling it for everyone hardly makes
   sense. Options: a separate package, download on demand, or "if the GPU libraries are next to
   the binary — use them". This also includes reconciling the version stamp (see the trap above).

### Open questions

- **Cross-platform.** If the field is named `execution_provider`, decide up front: is it
  `cpu|cuda` or `auto|cpu|cuda|coreml|directml`. On macOS the analog is the CoreML EP
  (`onnxruntime_go` supports it), on Windows — DirectML/CUDA. The config shape and the packaging
  scope depend on the answer.
- **Batch size.** On the GPU a batch-32 inference takes ~78 ms, and the bottleneck will most
  likely move to data preparation (`prep_workers: 2`, reading/hashing/chunking). It may be worth
  raising `embedding_batch_size` — but only after measuring on real indexing, not speculatively.
  Note: a comment in sp4rk (`DefaultBatchSize`) records that on **CPU** throughput plateaus at
  ~42 docs/s already at 32, and larger batches only add latency. On the GPU that curve is surely
  different — it needs re-measuring, not inheriting.
- **The second GPU.** Whether to expose device selection to the user or whether device 0 is
  enough.
- **The ~4 GB plateau** — a bit much for a background load on a 16 GB card, and on a 6 GB
  RTX 2060 it may not fit alongside the desktop. If it ever needs shrinking — `gpu_mem_limit`
  and a smaller batch are in reserve.

---

## Dual repo: watch your step

The change lives in **two** repositories, tied together by `go.work`, which is **not committed**
(ADR-015/025/031).

- `GOWORK=off go build` resolves the published sp4rk pin (`5e2a034`, without the changes) and
  **silently** yields a CPU build without a single error. If the GPU "stopped working" — first
  check that `go.work` is in place.
- No `replace` in `go.mod` (ADR-015/001).
- `go.work` goes into the c0wrk root, **not** the parent directory — ADR-031 moved it away from
  there precisely because it leaked into all neighboring projects.
- The `go.mod` header about the "mid-cycle state (ADR-025)" points to an outdated ADR — ADR-031
  has already replaced it. A trifle, but worth fixing on the next `go.mod` edit.
- Publishing order: commit+push sp4rk → `GOWORK=off go get github.com/v0lka/sp4rk@main && go mod tidy`
  → `GOWORK=off go build ./...` → `make build` / `make lint` / `make test` → commit+push c0wrk.
  CI on `main` is green only at the pin-shift points.

---

## Reproduction scripts

### Measuring CPU vs CUDA directly via `onnxruntime_go`

A module with `require github.com/yalue/onnxruntime_go v1.27.0`. The essentials:

```go
ort.SetSharedLibraryPath("/home/zab/Git/c0wrk/build/bin/libonnxruntime.so")
ort.InitializeEnvironment()
opts, _ := ort.NewSessionOptions()
cuda, _ := ort.NewCUDAProviderOptions()
cuda.Update(map[string]string{"device_id": "0"})
opts.AppendExecutionProviderCUDA(cuda)
cuda.Destroy()

// batch=32, seq=512, hidden=512; inputs input_ids/attention_mask/token_type_ids (int64),
// output last_hidden_state (float32)
sess, _ := ort.NewAdvancedSession(modelPath, inputNames, outputNames, inputs, outputs, opts)
for i := 0; i < 5; i++ { t := time.Now(); sess.Run(); fmt.Println(time.Since(t)) }
```

To reproduce the memory exhaustion — call `sess.Run()` from a fresh goroutine with
`runtime.LockOSThread()` on every iteration and print
`nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits -i 0`.

### Verifying the fix on the real embedder

A module with `replace github.com/v0lka/sp4rk => /home/zab/Git/sp4rk`, build with `GOWORK=off`:

```go
emb, err := embedding.NewEmbedder(embedding.EmbedderConfig{
    ModelPath:         bin + "/models/jina-v2-small.onnx",
    TokenizerPath:     bin + "/models/jina-v2-small-tokenizer.json",
    LibraryPath:       bin + "/libonnxruntime.so",
    MaxSeqLength:      512, HiddenDim: 512, BatchSize: 32,
    ExecutionProvider: "cuda",
})
// 20 rounds × 89 documents, each round — a new goroutine with runtime.LockOSThread(),
// calls serialized by a mutex. Print GPU used after every round.
```

Expected: a flat ~4000 MiB. Growth means some ORT touch point remained unpinned.

---

## Useful coordinates in the code

| what | where |
|---|---|
| embedder creation, env knob | `desktop/startup_phases.go`, `startVectorIndexBackground` |
| looking up the library next to the binary | `desktop/startup.go`, `resolveONNXLibPath` |
| vector index config | `backend/config/config.go`, fields `VectorIndex.*` |
| ORT download/install, version stamp | `Makefile`, target `fetch-onnx`, `ONNX_STAMP` (line 212) |
| session options and provider | `../sp4rk/embedding/onnx.go`, `buildSessionOptions` |
| the pinned thread | `../sp4rk/embedding/runner.go` |
| embedder lifecycle | `../sp4rk/embedding/embedder.go`, `NewEmbedder` / `Close` |
| the dual-repo ADR | `specs/decisions/031-gowork-repo-root.md` (and 015, 025) |
