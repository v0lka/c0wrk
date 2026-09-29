# ADR-073: Userland-aware guard substitution and the universal runtime smoke test

## Status

Accepted

## Context

Two failure classes in the embedded-LLM install path were discovered against
the pinned `prism-b10735-842b188` runtime, both shaped the same way: c0wrk
committed a multi-gigabyte download to a plan that could not work on the
machine it was about to land on, and the failure surfaced only after the
download or at first load.

1. **The `#222` substitution could produce an unloadable fallback.** The
   `cuda-13.3-crash` compatibility guard substitutes `cuda-12.8` on
   linux-amd64 because the pinned 13.3 build segfaults on some systems
   (upstream `llama.cpp#222`). But the cuda-12.8 build dynamically links the
   CUDA 12.x userland (`libcudart.so.12` + `libcublas.so.12`), and nothing
   checked whether that userland exists on the target machine. A system with a
   13.3 driver and **no** CUDA 12.x runtime libraries — or the documented
   packaging trap of a `.so.12` symlink pointing at a 13-series ELF — would
   swap a working (if crash-prone on some machines) 13.3 build for a 12.8
   build that fails at load time: the guard converted a documented upstream
   risk into a guaranteed local failure. The symmetric problem existed for
   `unknown`: a machine nobody had measured must not be treated as one where
   the fallback is safe.

2. **The runtime was proven to run on macOS only.** The install flow ran the
   `llama-server --version` smoke test against the staged tree as part of the
   darwin-only provisioning branch (quarantine-clear + ad-hoc codesign). On
   Linux and Windows the first proof that the downloaded binary could execute
   at all came after the weights were fetched — a Linux CUDA runtime whose
   shared libraries are missing failed the install only after a multi-gigabyte
   weight download, with the actionable diagnosis (`libcudart`, `libcublas`)
   arriving late or not at all.

Both fixes are bounded by the same constraint that shaped ADR-066/067: the
plan must stay **honest** — every degradation and every withheld substitution
is recorded with its reason — and must never trade a documented upstream risk
for a strictly worse local one.

## Decision

Two decisions, recorded together because they harden the same pre-download
phase of the install:

**D1 — the Linux `#222` substitution is gated on a measured CUDA 12.x
userland verdict.** `core/embeddedllm/hardware.go` gains a tri-state
`CUDA12Userland` probe (`probeCUDA12Userland`): on linux-amd64 it spawns
`ldconfig -p` once, inspects candidate ELFs for `libcudart.so.12` and
`libcublas.so.12`, and classifies via the pure `classifyCUDA12Userland`:
`present` (both found, each ELF's embedded DT_SONAME is its own `.so.12`
name), `absent` (a candidate missing with a complete discovery, or a
`.so.12`-named symlink onto another series' ELF), `unknown` (a platform the
CUDA archives do not target, ldconfig missing or failed, or candidates that
could not be inspected — every indeterminate inspection maps here, never a
guess). The zero value IS `unknown`, so an unprobed caller is
indistinguishable from an unanswerable one and lands on the conservative side.
`MachineProfile.CUDA12Userland` carries the verdict (via the `Hardware` the
probes produce), `applyCompatGuards(table, platform, backend, gpu, cu12)`
takes it, and the Linux `cuda-13.3-crash` branch fires only on
`CUDA12Present`: `absent`/`unknown` keep the probed `cuda-13.3` backend and
record the decision with `Applied=false` plus verdict-specific guidance
("found no usable CUDA 12.x runtime libraries…" / "could not determine whether
this system has the CUDA 12.x runtime libraries…"). The Windows half of `#222`
is unconditional — the paired `cudart` companion archive bundles the CUDA
runtime, so there is no userland to probe — and every other guard is decided
without the verdict.

**D2 — the `--version` smoke test runs on every platform, before any weight
byte is fetched.** `provisionRuntime` keeps the darwin-only signing branch
(`xattr` + ad-hoc `codesign`) but every platform now runs `smokeTest` against
the staged runtime tree immediately after extraction — before `promoteRuntime`
and before the model/mmproj downloads. A failing smoke test is fatal
(`ErrSmokeTestFailed`), leaves no manifest and no provider entry, keeps the
staging tree for diagnosis, and its message composes a platform- and
backend-specific hint (`smokeTestHint`): the Gatekeeper remedy on macOS;
"the `<backend>` runtime most likely cannot load its CUDA 12 libraries
(libcudart, libcublas). Install the CUDA 12 runtime libraries for your
distribution and install again." for a CUDA backend; run-the-binary loader
diagnostics otherwise.

Both changes are recorded in the compatibility record that already flows
`Resolution.Guards` → `InstallReport.Guards` → `Manifest.guards` →
`EmbeddedLLMStatus.guards`, so an install that kept 13.3 states why in
Settings.

## Consequences

**Positive.**

- A Linux machine without CUDA 12.x userland gets an install that *runs*
  (the probed 13.3 build) instead of one that fails at load; the substitution
  still fires wherever it is genuinely safe.
- A runtime that cannot execute fails the install in seconds with an
  actionable hint, on every platform — never after a multi-gigabyte weight
  download. A Linux CUDA install with missing libraries now names
  `libcudart`/`libcublas` and the fix, before any weight byte moves.
- The guard record stays complete: a withheld substitution is disclosed
  (`Applied=false` + guidance), not silently dropped — the existing
  "no decision is dropped" invariant is preserved and now covers the new
  condition.
- The verdict is measured once per probe run and threaded through both
  decision points (`plan()` pre-download and `refineWithStagedDevices()`
  post-staging), so the plan and its refinement gate on the same fact.

**Negative.**

- A Linux machine whose CUDA 12.x libraries are genuinely present but
  unprobeable (stripped ELFs, a broken ldconfig, exotic layouts) keeps the
  13.3 build and loses the substitution it could have had — accepted,
  because the error in this direction is a documented crash risk while the
  error in the other is a guaranteed unloadable runtime.
- The hardware probe now spawns `ldconfig` on Linux, adding one
  `probeCommandTimeout`-bounded subprocess to the probe phase.
- The smoke test on every platform adds one `--version` invocation (60 s
  budget) per install; a false negative there (a loader quirk that blocks
  `--version` but not serving) aborts an otherwise viable install.
- An existing installed 13.3 runtime on a `present` machine keeps running
  until the next install/repair click: the guard re-evaluates at plan time,
  so a repair applies the substitution, but c0wrk does not force a reinstall.

## Alternatives Considered

- **Substitute unconditionally and fail at load (previous behavior).**
  Rejected: it converts a documented upstream crash on *some* systems into a
  guaranteed failure on machines without the 12.x userland — strictly worse
  for that class, and the failure message pointed nowhere actionable.
- **Downgrade to the CPU build when the 12.x userland is absent.** Rejected:
  the 13.3 build demonstrably loads and runs on these machines (only *some*
  systems hit #222); throwing away GPU acceleration to avoid a risk the
  machine may not have is a needless degradation, and it contradicts the
  "never refuse, degrade honestly" rule the resolution layer is built on.
- **Treat `unknown` as `present`** (probe only when it answers `absent`).
  Rejected: unknown means nobody measured, which is not evidence of safety;
  firing the substitution on a guess reintroduces the original failure for
  exactly the machines hardest to reason about. The zero value must be the
  conservative side, as with `BudgetFit`.
- **Probe the userland by dlopen-ing the libraries from the c0wrk process.**
  Rejected: loading arbitrary system libraries into the app's address space
  to ask a yes/no question is a needless attack surface; SONAME inspection of
  candidate files answers the same question read-only.
- **Keep the smoke test darwin-only and add a Linux-only library check.**
  Rejected: a static library check cannot prove the binary executes (loader,
  CPU features, packaging), and a platform-specific check for every future
  platform-specific failure mode does not scale. The binary's own `--version`
  is the one test that is authoritative on every platform, which is exactly
  why macOS already used it as the final word after signing.
- **Move the smoke test after the weights.** Rejected: the whole point is to
  fail before the multi-gigabyte download; after the weights it protects
  nothing that the first load would not reveal, and the weights would already
  have been paid for.
