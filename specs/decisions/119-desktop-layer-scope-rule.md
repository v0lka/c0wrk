# ADR-119: Desktop Layer Scope — Lifecycle, Bindings, OS Integration, HITL

## Status

Accepted

## Context

The layer audit of `desktop/` (issue #42) found that roughly a third of the package's non-test lines (~3,200, ≈37% — the audit's own estimate with a stated ±15% error bar) carried no Wails dependency: the web-access sidecar orchestration with its TLS policy, the startup composition root, the tool judge's LLM evaluation, the GC memory-limit policy, dialog/window state persistence, the CSP policy, the event-coalescing machine, and the webview-GPU failure state machine. The migration plan that moved this code down-layer (issue #42, items P-01…P-14, executed on `refactor/desktop-layering`) is done, but nothing recorded the *criterion* by which a symbol's home is decided. `specs/architecture/layers.md` describes where things live now — it is a state map, not a decision rule — and that criterion gap is how the debt formed: "GUI-adjacent" was treated as "belongs in `desktop/`".

Two constraints shape the rule:

- **Zero-Wails invariant.** `backend/` and `core/` carry no `github.com/wailsapp/wails` imports (verified by the audit, re-verified after the migration). A move that drags a Wails type into a lower layer is forbidden outright; a file that straddles the boundary is split — the portable part moves down, a thin Wails-facing adapter stays.
- **Epic #84 overlap** (v2: daemon + TUI/ACP clients, [#84](https://github.com/v0lka/c0wrk/issues/84)). Its phases 1a/1b/1c (#74 composition root into `backend/appbuild`, #75 HITL funnels + judge into `backend/interaction`, #76 daemon event stream) change the fate of two rule components — the startup/lifecycle glue and the HITL plumbing. The audit's governance note (its §9.3) requires the rule to state explicitly what happens to the HITL clause when that phase lands, instead of leaving the collision to be discovered mid-epic; it also flagged combining both decisions into one ADR as the alternative.

## Decision

A top-level symbol stays in `desktop/` if and only if it satisfies at least one of:

- **W1 — Wails lifecycle/API surface**: uses Wails types (`wailsRuntime.*`, `options.*`, `assetserver.Middleware`, `logger.Logger`) or is a lifecycle method (`Startup`, `Shutdown`, `DomReady`, `OnBeforeClose`, `OnFileDrop`).
- **W2 — Wails binding**: an exported `*App` method visible to the `frontend/wailsjs` binding generator.
- **W3 — HITL plumbing** *(sunset clause — see refinement 2)*: the pending-prompt plumbing between backend and frontend (the `sync.Map` of unresolved confirmations, the delivery channels, `GetPendingActions`).
- **W4 — native OS-shell integration**: window, notifications, single-instance, power/wake, X11 activation, webview GPU — host integration that carries no domain logic.

Everything else moves down: orchestration, persistence, configuration → `backend/`; dependency-free logic → `core/`. A file straddling categories splits: the portable part moves, the W1/W2 adapter remains. The criterion doubles as a standing review rule: a change that adds a top-level symbol to `desktop/` must name its W-category, or the symbol moves down-layer.

Three refinements pin the edges:

1. **W4 keeps ownership, not only adapters.** A policy core may live one layer down when it is transport-neutral (e.g. the webview-GPU watchdog machine in `backend/webview_gpu_watchdog.go`), while ownership and launch stay with the GUI host — the daemon never starts GUI-only services (the audit §9.1 refinement to its P-13). "Moved to backend" never means "owned by the daemon".
2. **W3 is valid until epic #84 phase 1b.** When #75 moves the HITL funnels into `backend/interaction`, that decision supersedes the W3 clause: `desktop/` keeps only the W1/W2 adapter glue and the rule degenerates to "desktop = lifecycle + bindings + OS". The sunset is recorded as a dated Historical Note on this ADR (per the META update rules — the decision body is not rewritten), `specs/architecture/layers.md` drops W3 in the same change, and until then layers.md keeps W3 in the desktop section.
3. **No internal reshuffles as a substitute for down-moves.** Reorganizing inside `desktop/` (e.g. the proposed `desktop/pending_state.go`) is rejected when the code's final owner is a planned lower layer — land code where its final owner lives, even when that owner arrives with a future epic phase (the audit §9.1 cancellation of its A-11 internal move, to avoid a double migration).

The zero-Wails invariant is part of this decision, not an incidental fact: W1 works as a boundary test only while no Wails type leaks downward. The invariant is maintained today by convention and the import cycles a violation would create (`depguard` pins only `core ↛ backend`); a leak must be treated as a violation of this ADR, never as a new classification datum.

## Consequences

**Positive:**

- Placement is mechanical: any future symbol's home is decidable from the W-criterion without debate, and the audit's classification checklist (its §1.4) becomes a permanent review rule.
- Epic #84 phases land on prepared seams — backend-owned startup phases (#74 wraps them in `backend/appbuild`), the transport-neutral judge (#75 absorbs it), the `core/instancelock` primitive (#79's daemon lock reuses it) — with no second migration of the same code.
- The zero-Wails invariant and the GUI-only ownership boundary have a named decision record; review and the daemon topology work can both be checked against this ADR.

**Negative:**

- OS-integration work may touch two packages: the policy core down-layer, the host adapter in `desktop/`.
- W3's sunset creates a documented future obligation: the #75 successor ADR must carry the supersession and layers.md must drop W3 in the same change, or the spec and the code drift.
- Thin adapters remain in `desktop/` permanently — callback builders, emit funcs and the event funnel are W1/W2 by nature and never move.

## Alternatives Considered

- **No ADR — rely on `specs/architecture/layers.md`.** Rejected: layers.md describes current state, not the classification criterion; every future placement would re-litigate "GUI-adjacent", which is exactly how the audited debt formed.
- **One combined ADR covering this rule and the epic's daemon topology** (the audit §9.3 option). Rejected: the epic's ADR does not exist yet; coupling this governance rule to it would either block the migration branch or force speculative content. The W3 sunset clause plus the supersede-on-landing obligation decouple the two decisions cleanly.
- **Stricter rule — "desktop = lifecycle + bindings only"**, moving the HITL plumbing out now. Rejected: W3 plumbing is inseparable from Wails event delivery until #75 builds `backend/interaction`; moving it now duplicates that work and re-touches the wire surface for no immediate gain.
- **Looser rule — "desktop keeps GUI-adjacent code".** Rejected: undecidable; it is the status quo ante that produced the audit's findings A-1…A-9 (~37% of the package misplaced).

## Related

- [ADR-008](./008-backend-sp4rk-direct-import.md) — desktop imports `core`/sp4rk directly; the import freedom this rule's down-moves rely on.
- [ADR-075](./075-single-instance-lock.md) — the W4 case refinement 1 protects: the lock primitive moved to `core/instancelock`, the relay/focus semantics stayed in `desktop/`.
- [specs/architecture/layers.md](../architecture/layers.md) — the current-state mapping this rule classifies; its `desktop/` section and invariants carry the criterion and the zero-Wails invariant.
