# ADR-061: Package-Runner Resolution and the Exec-Scope Criterion (C10)

## Status

Accepted — amends [ADR-052](./052-flowsh-command-analysis.md) (criteria set → C1–C10, digest → `sp4rk-shell-analysis/v4`) and [ADR-057](./057-flow-based-network-verdicts.md) (whose renumbering to C1–C9 this ADR extends).

## Context

The silent-mode audit corpus (2026-09-18; `core/tools/testdata/silent_corpus/corpus.ndjson`) recorded **21 events** invoking the JS toolchain through a package runner or a project-local bin path — `cd frontend && npx vitest run …`, `npx tsc -b`, `./node_modules/.bin/tsc -b` — every one of them escalated on the non-canonical **C6** (`command_unbounded_analysis`), and **20 have ground truth `allow`** (13 TRUE_ALLOW + 7 recorded as FALSE_DENY). The mechanism: the flowsh knowledge base deliberately has **no `npx` signature** (a KB signature would have bounded the runner form while the path form stayed ⊤, splitting the audited 963134/963140 retry pair — the pinned canonical-key equality), so `npx` resolved to **unknown → ⊤ → C6**. The workspace-scoped verification marker could not rescue the shape deterministically: an unresolved runner statement never reaches the binder, produces no `CommandCall`, and breaks the marker's call-coverage condition (`len(CommandCalls) == Commands`), so the marker stayed **off** and the silent-mode judge decided without positive evidence — a live CONFIRM auto-denial (`npx vitest run src/__scratch__/cpuRepro.test.tsx --reporter=basic 2>&1 | tail -40`) demonstrated the non-determinism of that path.

At the same time, the corpus holds exactly one TRUE_DENY in this family: **969588** — `npx vitest run /tmp/debug_completion.test.ts …`, executing a test file from **outside the session roots** (host `/tmp`, not the session temp). The silent-mode recommendations (an internal audit artifact, §4/§5, kept out of this repository) forbid blanket non-canonical-C6→allow because, at audit time, 3 of 8 TRUE_DENY rode on C6; any change that un-bounds the runner family therefore needs a deterministic replacement reason for the out-of-roots member, or the TD 8/8 invariant breaks.

## Decision

Two changes, one in flowsh (engine) and one in sp4rk (criteria):

1. **Binder-level runner and bin-path resolution (flowsh `bind/runner.go`, the endgame the KB note in flowsh `kb/data/toolchain.yaml` prescribed).** After the exact-name PATH lookup, the binder resolves:
   - a **package runner** (`npx`, `bunx`) to its first non-flag **literal** operand — the runner's own words (flags, `-p`/`--package` values, the `--` terminator) are consumed — and
   - a **`node_modules/.bin/<bin>` path form** (relative or absolute) to the bare binary name,
   
   then looks the resulting name up in the knowledge base and binds that command's signature with the surviving argv. `npx vitest run …` binds vitest's signature; `npx tsc -b` and `./node_modules/.bin/tsc -b` bind tsc's; both keep one canonical identity (the retry-pair pin now holds with **both forms bounded**, replacing "both ⊤"). The comparable-FORM normalization (`CommandCall`) moves to `bind.NormalizeBinaryName` as the single runner/path vocabulary shared with the canonical layer.
   
   **Fail-closed shapes are unchanged** — each stays ⊤ (unknown → C6): an operand outside the knowledge base (`npx some-unmodelled-pkg`; a registry fetch whose code the analysis cannot see), a dynamic operand (`npx $PKG`), a runner that names no operand, and `-c`/`--call` (an arbitrary shell string — the cradle shape; never boundable by name resolution). The runner's **conditional registry fetch is deliberately not modelled**, matching the shipped `npm run`/`exec` and `bun` entries: the executed binary's signature is the bound surface.
2. **Criterion C10 `command_exec_outside_roots` (sp4rk `tools/shellanalysis.go`).** A Direct `CodeExec`/`ProcSpawn` effect whose path-shaped target resolves **outside every session root** fires a **hard, non-canonical** reason — the exec sibling of C4 (destructive write outside the roots) and C9 (FS\* outside the roots, soft). It runs the same containment walk as C9 (path-shape filter, working-directory anchoring, harmless-device exemption) over `CodeExec`/`ProcSpawn` kinds, with **no system-path exclusion** (the question is whether the executed code is inside the trusted roots, not who owns the path). It fires only on a **bounded** report (⊤ is C6's territory) and only where session roots are attached (no roots ⇒ cannot fire, matching C9's semantics). The canonical set is **unchanged**: C10 is a judgment shape (scratch scripts in the host temp dir are routine), clearable by the judges; the verification marker stays off for it by its own containment condition (an out-of-roots operand), so a silent-mode stub denies it without positive evidence.
   
   The criteria set is therefore renumbered **C1–C10** (C10 appended last — no existing code renumbers), the digest schema is bumped to **`sp4rk-shell-analysis/v4`** (flowsh `v0.4.0`), and winner selection becomes **severity-first** (ties broken by the fixed priority order), so the hard C10 is never masked by the lower-numbered soft C9. The strict-judge system prompt, the marker doctrine prose, and the digest goldens are updated in the same change.

Baseline effect (silent-corpus replay, stub judge): **TD 8/8 preserved** — 969588 now denies deterministically on C10; **TA-denied 26 → 25** — event 960726 (`npx tsc --noEmit … && npx vitest run …`) retires, while the allow-twin 969558 (the same scratch test written to `/tmp` and run) does not: it now hard-fires C10 as well (a single new false deny the exec-scope criterion introduces on an out-of-roots operand); **FD 14 unchanged** (the FD set is unrelated to the runner family and keeps a hard C6 with the marker off); **FA 0 unchanged**. 19 of the 21 audited npx/bin-path events now carry no criterion at all.

## Consequences

Positive:

- The everyday JS verification loop (`npx vitest run …`, `npx tsc -b`, `./node_modules/.bin/…`) is **deterministically allowed** on in-root operands — no judge, no C6, no marker dependence — closing the audit's largest false-deny family and the live silent-mode denial that motivated this ADR.
- Retry recognition improves: runner and path forms now agree at the binder level, not only in the canonical form.
- The out-of-roots exec shape gains a deterministic reason; silent-mode deny precision on it no longer depends on judge temperament.

Negative / risk:

- Bounding `npx <known-bin>` does **not** model the conditional registry fetch; a hostile local `node_modules/.bin` shadow is inside the trusted roots by the operator-trust premise (same premise as workspace auto-approval and the marker). The *repository-as-untrusted-source* caveat recorded for the marker applies equally here.
- One more criterion and one more digest version for hosts to track; C10 can fire on shapes previously silent **on bounded reports** (e.g. `node /tmp/x.js` is unaffected — node is a bash-frontend sink — but a KB-bound driver pointed out-of-roots newly escalates). The corpus replay measured the drift at one intended TA retirement (960726, now bounded) plus one new false deny (969558, the out-of-roots allow-twin C10 now denies).
- `-c`/`--call` keep the call unbounded while the canonical form still normalizes past them; the ⊤ dominates the verdict, but the two layers deliberately differ there (documented in `bind/runner.go`).

## Alternatives Considered

- **Give `npx` a KB signature** (generic COMMAND operand, like `bun`): rejected — it bounds `npx <unmodelled-pkg>` as a known quantity (an under-approximation of a registry fetch+execute) and, before binder path resolution existed, split the 963134/963140 retry pair. The KB note explicitly deferred to this ADR's approach.
- **Judge-only mitigation** (teach the strict judge to always ALLOW marked runner commands): rejected — it keeps the escalation on every verification call (latency, memo-signature churn), keeps the decision non-deterministic in silent mode, and §4 of the recommendations forbids blanket C6 clearing.
- **Make C10 canonical**: rejected — executing out-of-root code is a scope/judgment shape, not a structural danger (the session itself writes scratch scripts to the host temp); canonicality would also hard-block verify-on-edit's unattended path on such verification commands.
- **Extend C9 to exec effects with a severity bump**: rejected — C9's `outside_session_roots` code is soft by contract and consumed as such by the stub doctrine (ALLOW on soft-only); the audited TRUE_DENY needs a hard reason to stay denied.
