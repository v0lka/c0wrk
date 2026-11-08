# ADR-090: OpenVINO device resolution (empirical device probe replaces the plugin default)

## Status

Accepted

## Context

[ADR-089](089-openvino-embedding-flavor.md) decision 1 pinned
`vector_index.device_id` to `0` under `openvino` and recorded the
consequence as "the plugin chooses the concrete Intel device itself". The
2026-10 gate validated the provider on an Arrow Lake-P Arc iGPU; it did not
validate the device **order** the plugin reports, because on that run the
plugin's priority happened to land on the GPU.

Live re-measurement (2026-10, ORT 1.28.1 + plugin 1.8.0 on the same class of
host) shows the assumption fails on Intel Core Ultra parts that ship an NPU:

- The plugin enumerates one `OrtEpDevice` per hardware device and, through the
  ORT C API, the order is **NPU (`OrtHardwareDeviceType_NPU`), GPU
  (`_GPU`), CPU (`_CPU`)** — plus three `OpenVINOExecutionProvider.AUTO`
  meta-devices that sp4rk excludes.
- The plugin applies no cross-device fallback of its own: with several devices
  appended it logs *"OpenVINO EP factory ... is not a meta device but 3
  OpenVINO devices were specified. Using first ov_device only"* and compiles
  exclusively on the first one — the NPU.
- The NPU compiler rejects the embedding model outright: `Reshape node
  '/encoder/layer.0/attention/self/Reshape_2': expected exactly 1 dynamic
  output bound dimension, got 2` (the encoder has dynamic batch and sequence
  bounds). Session creation fails, so the whole OpenVINO rung fails and
  c0wrk's never-silent fallback fires (`vector_openvino_fallback`, CPU
  provider) — although the same model compiles on the integrated Arc GPU in
  ~0.5 s and keeps ADR-089's measured 3.9×–8.2× speedup.

The provider options that would otherwise steer this (`device_type`,
`device_id`, `device_luid`) are parsed by the plugin only to be **advised
against and ignored** — verified live: `device_type`/`device_id` with every
device appended still compiles on the NPU. The `onnxruntime_go` surface
exposes only `EpName`/`EpVendor`, so neither `OrtHardwareDevice_Type` nor the
plugin's `ov_device` metadata can be read to filter the NPU before the fact
(checked in every released wrapper version v1.29.0–v1.36.0).

## Decision

1. **The OpenVINO device is resolved empirically, inside the provider rung.**
   sp4rk's embedding package appends every real device on the first session
   attempt (the legacy behaviour, and the plugin's own choice); when that
   session cannot be created, it walks the plugin's remaining devices in
   enumeration order, rebuilding the provider options for one device at a time
   and retrying the same session creation. The first device that builds a
   session wins, every failed candidate is recorded for the fallback WARN, and
   the winner is cached process-wide (`openVINOPreferredDevice`) so later
   sessions append only it. A walk that exhausts the devices resets the
   preference and leaves the rung's failure untouched (explicit `openvino`
   still fails loudly; `auto` still continues its provider ladder).
2. **Both session-creation stages are covered.** The eager query session in
   `NewEmbedder` and the first lazily created bucket/batch session
   (`Embedder.newSessionWithFallback`) run the same walk, so bucket mode
   cannot regress to the CPU because its first session is created on first
   use.
3. **c0wrk's user-facing contract does not change.** `device_id` stays pinned
   to `0` and a non-zero value under `openvino` stays a hard configuration
   error (ADR-045's loud-validation rule). What changes is the meaning of `0`:
   from "let the plugin pick" to "let the rung resolve a device this model can
   run on". The status payload's `ov_device` verification
   (`xpu-smi utilization.compute`) remains the external verdict that the GPU —
   not the CPU device — is doing the work, and is now expected to report
   `"gpu"` on NPU-bearing hosts.

## Consequences

- Intel Core Ultra hosts (NPU present) keep the OpenVINO provider instead of
  degrading to the CPU provider; the measured cost is one failed NPU compile
  per process (~230 ms on the reference host; the GPU session that follows
  takes ~490 ms and would have been created anyway).
- A host whose first OpenVINO device is broken for any other reason (driver,
  device memory) degrades within the provider before the provider itself
  fails — a strictly better failure shape than "first device or CPU".
- A later session failure on the resolved device still surfaces loudly (a
  hardware-backed session is live and its dedicated ONNX thread cannot be
  stranded); only the *first* session and the first lazy session can re-resolve
  a device.

## Alternatives Considered

- **Ship the OpenVINO stack without `libopenvino_intel_npu_plugin.so`.**
  Verified to work (the plugin then enumerates GPU → CPU and the embedder comes
  up on the GPU), but it deletes a capability from the packaging, fixes only
  the ordering symptom, and would still fail on a host whose GPU device is
  unusable.
- **Filter by hardware-device class / `ov_device` metadata.** Not reachable:
  no released `onnxruntime_go` version exposes `OrtHardwareDevice_Type`,
  `HardwareDevice_Metadata` or any per-device accessor beyond `EpName`/
  `EpVendor`.
- **Provider options (`device_type`, `device_id`, `device_luid`).** Measured
  live: advisory only; the plugin still compiles on the first appended device.
- **Use the `OpenVINOExecutionProvider.AUTO` meta-devices.** Measured live:
  AUTO resolves to the CPU device on the reference host (the exact embedding
  output of the CPU-device run), giving up ADR-089's 8.2× GPU speedup.
- **Unsafe reflection into the wrapper's unexported `EpDevice` handle** to
  reach the ORT C API. Unmaintainable and layout-fragile; the empirical walk
  needs no private surface.
