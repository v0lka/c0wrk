// Backend sync for the embedded local-model UI state.
//
// The module-level counterpart of `./embeddedLLMStore`: the store file holds
// ONLY state, reducers and selectors, and every RPC side effect — the two
// authoritative re-reads, the one shared mutating runner and the refcounted
// event subscription — lives here, following the paperStore convention so the
// zustand reducers stay pure and trivially testable.
//
// These are functions, NOT actions, on purpose: they orchestrate several
// reducers plus a round trip, and keeping them out of the store means the store
// has no async surface at all. `./embeddedLLMStore` re-exports all four, so the
// historical import path (`@/stores/embeddedLLMStore`) keeps working.
//
// One deliberate cross-store side effect lives in `refreshEmbeddedLLMStatus` —
// here, NOT in a reducer, so the reducers stay pure: an install/remove
// transition changes which providers `GetConfig` reports, so the shared
// `useConfigData` model cache (the chat toolbar picker's source) is invalidated
// there. That function's comment explains why load/unload is excluded.

import {
  getEmbeddedLLMStatus,
  onEmbeddedLLMInstallProgress,
  onEmbeddedLLMState,
} from '@/api/embedded'
import { getEmbeddedLLMTuning } from '@/api/embeddedTuning'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { logger } from '@/lib/logger'
import { useEmbeddedLLMStore, type EmbeddedLLMBusyAction } from './embeddedLLMStore'

/** Re-read the authoritative snapshot. NEVER throws: an unavailable backend
 *  (no Wails bindings — dev-frontend, vitest) or a schema drift is logged and
 *  leaves the previous snapshot in place, so a status surface degrades to "not
 *  installed" instead of unmounting. Returns whether the snapshot was applied. */
export async function refreshEmbeddedLLMStatus(): Promise<boolean> {
  const { setStatusLoading, setStatus } = useEmbeddedLLMStore.getState()
  // `statusLoading` is the FIRST-read spinner, so it is armed only while there
  // is no snapshot yet. A later re-read (an `embedded_llm:state` invalidation, a
  // post-RPC read-back, a `backend:ready` retry) refreshes the snapshot in
  // place: re-arming the flag there would blink every piece of helper text that
  // renders from it, which is exactly the "still reading" lie the field's
  // contract forbids. The busy window (`busy`) is what covers a re-read.
  if (useEmbeddedLLMStore.getState().status === null) setStatusLoading(true)
  try {
    const status = await getEmbeddedLLMStatus()
    const prev = useEmbeddedLLMStore.getState().status
    setStatus(status)
    // The install state decides whether the local model EXISTS as a provider at
    // all: the backend generates `llm.openai_compatible.embedded` from it, so an
    // install/remove transition changes `GetConfig().llm.all_models` — the list
    // the chat toolbar's ModelCombobox renders. That list is served from
    // useConfigData's module-level cache, which no other embedded path
    // invalidates (the settings dialog re-reads config on every open, which is
    // why only the chat picker went stale after an install). Invalidate on the
    // transition ONLY: load/unload/auto-unload changes neither add nor remove a
    // selectable model — an unloaded model stays listed, since the first request
    // to it loads it — so invalidating there would refetch config on every load
    // for nothing. The FIRST read (prev === null) is skipped deliberately: at
    // startup the config cache is fetched after the backend has already synced
    // the provider, so there is no stale entry to drop.
    if (prev !== null && prev.installed !== status.installed) {
      invalidateConfigCache()
    }
    return true
  } catch (err) {
    // A failed READ is not a failed action: it must not paint the action-error
    // line (which the user reads as "my install broke"). Log, drop the spinner
    // and leave the previous snapshot in place.
    logger.warn('[embedded-llm] status read failed', err)
    setStatusLoading(false)
    return false
  }
}

// How many tuning reads are in flight. Module state rather than store state
// because nothing renders a count — the store exposes only the derived
// `tuningLoading` flag this tally drives.
let tuningReadsInFlight = 0

/** Re-read the authoritative tuning overrides. NEVER throws — the exact
 *  contract as `refreshEmbeddedLLMStatus`: a failed read is logged, the
 *  previous snapshot stays and no action error is painted. Returns whether the
 *  snapshot was applied.
 *
 *  `tuningLoading` is a COUNTED window, not a flag this function owns outright:
 *  it drives the tuning controls' `disabled`, and two overlapping commits each
 *  start a read of their own (see `runEmbeddedLLMAction` for the gesture that
 *  produces them). The FIRST read to land must not clear it — its snapshot was
 *  taken before the sibling's write, so every knob still renders a value the
 *  next commit would fold into a patch the user never saw. The window closes
 *  only when the last pending read has landed. */
export async function refreshEmbeddedLLMTuning(): Promise<boolean> {
  const { tuningLoading, setTuningLoading, setTuning } = useEmbeddedLLMStore.getState()
  // `tuningLoading` is the single source of truth for "a read is in flight" —
  // this function is its only writer — so a flag that is already down means the
  // tally is stale (a read that never settled, a store `reset()`). Resync from
  // the flag instead of counting on top of it, so an abandoned read can never
  // wedge the window shut. The one instant the two disagree is between
  // `setTuning` and the `finally` below, which holds no `await`, so no entry
  // can observe it.
  if (!tuningLoading) tuningReadsInFlight = 0
  tuningReadsInFlight += 1
  setTuningLoading(true)
  try {
    setTuning(await getEmbeddedLLMTuning())
    return true
  } catch (err) {
    logger.warn('[embedded-llm] tuning read failed', err)
    return false
  } finally {
    tuningReadsInFlight = Math.max(0, tuningReadsInFlight - 1)
    // `setTuning` clears the flag as part of applying a snapshot, so a sibling
    // read still in flight has to re-arm it here. This `finally` is the one
    // place the window closes, and it closes only at zero.
    setTuningLoading(tuningReadsInFlight > 0)
  }
}

/** Mutating actions currently in flight. The runner's OWN bookkeeping — it is
 *  deliberately not part of the store's snapshot, because no surface renders a
 *  count: they render `busy`, which the tally exists to protect. It is
 *  re-derived from that flag on entry (see below), so an abandoned action can
 *  never wedge the window shut. */
let actionsInFlight = 0

/** Run one mutating embedded-LLM RPC inside the store's busy window.
 *
 *  The window covers the READ-BACK, and that is the whole point: `busy` is what
 *  every embedded-LLM control reads for `disabled`, so clearing it before the
 *  fresh snapshot lands would re-enable the buttons while the rendered state is
 *  still the pre-action one — and a fast second click in that window fires a
 *  duplicate RPC. One shared runner keeps every mutating flow (install / load /
 *  unload / remove, the auto-unload budget, a tuning patch) on that shape.
 *
 *  This runner owns `busy` END-TO-END: it arms the window, and its `finally` is
 *  the only thing that clears it. Nothing inside `rpc` may touch the flag — an
 *  action that also raises local state mid-flight (`beginInstall` does) leaves
 *  `busy` alone, or the window it is running in closes early.
 *
 *  The window is also COUNTED, because `disabled` is a render-time property and
 *  cannot cover a control that is already on screen: a Radix menu opened before
 *  the first action keeps its portaled items clickable (the trigger is what
 *  `disabled` reaches), and one physical click on such an item can produce TWO
 *  actions — the `focusout` of a dirty number field flushes its commit
 *  synchronously during `mousedown`, then the item's own `click` commits again.
 *  So the flag clears at zero in-flight actions only: whichever read-back lands
 *  first leaves the window open for its sibling, instead of re-enabling every
 *  tuning control while another write is still pending. The FIRST action names
 *  the window; an overlapping sibling keeps that name (the long `load` is never
 *  renamed by a quick `tuning` write that happens to overlap it), and the first
 *  action also owns the error reset — only it clears the previous window's
 *  error, so a sibling entering an open window cannot erase a failure the
 *  first action already painted (the last-settling action's error wins, which
 *  is inherent to a shared field).
 *
 *  The RPC's own rejection is captured as the action error — a refusal (an
 *  install already in flight, a stop that did not take, an out-of-range patch)
 *  is also a state the surfaces must show honestly — and `readBack` runs in
 *  BOTH outcomes. NEVER throws. */
export async function runEmbeddedLLMAction(
  action: EmbeddedLLMBusyAction,
  rpc: () => Promise<void>,
  readBack: () => Promise<unknown>,
): Promise<void> {
  const { busy, setBusy, setError } = useEmbeddedLLMStore.getState()
  // `busy` is the single source of truth for "an action is in flight" — this
  // runner is its only writer — so an idle window means the tally is stale (a
  // read-back that never settled, a store `reset()`). Resync from the flag
  // instead of counting on top of it. A window that IS open keeps the name its
  // first action gave it.
  if (busy === null) {
    actionsInFlight = 0
    setBusy(action)
    // Only the FIRST action of a window clears the previous error: a sibling
    // entering an already-open window must not — its setError(null) would erase
    // a failure the first action had already painted before this one's own
    // outcome lands.
    setError(null)
  }
  actionsInFlight += 1
  try {
    await rpc()
  } catch (err) {
    logger.warn(`[embedded-llm] ${action} failed`, err)
    // Wails rejects with a Go error string, so the message IS the report.
    setError(err instanceof Error ? err.message : String(err))
  }
  try {
    await readBack()
  } catch (err) {
    // The read-backs never throw by contract; a drift must still not wedge the
    // busy window shut.
    logger.warn(`[embedded-llm] ${action} read-back failed`, err)
  } finally {
    actionsInFlight = Math.max(0, actionsInFlight - 1)
    if (actionsInFlight === 0) setBusy(null)
  }
}

/** Live subscription count. Both embedded-LLM surfaces (the Settings block and
 *  the status-bar indicator) can be mounted at once; one shared Wails
 *  subscription keeps a single event from being applied twice. */
let eventSubscribers = 0
let eventUnsubscribe: (() => void) | null = null

/** Subscribe to the two global `embedded_llm:*` events and feed the store.
 *
 *  Refcounted: the returned unsubscribe tears the shared Wails subscription down
 *  only when the LAST consumer goes away, and is idempotent (a double call from
 *  a StrictMode effect cannot drop another consumer's subscription). Safe to
 *  call when the runtime is absent — @/api/runtime's `subscribe` no-ops there. */
export function subscribeEmbeddedLLMEvents(): () => void {
  eventSubscribers += 1
  if (eventUnsubscribe === null) {
    const offState = onEmbeddedLLMState(() => {
      // A transition is an invalidation, not a patch: re-read the authoritative
      // snapshot (which also carries the fields the event payload omits).
      void refreshEmbeddedLLMStatus()
    })
    const offProgress = onEmbeddedLLMInstallProgress((data) => {
      useEmbeddedLLMStore.getState().applyProgress(data)
    })
    eventUnsubscribe = () => {
      offState()
      offProgress()
    }
  }

  let released = false
  return () => {
    if (released) return
    released = true
    eventSubscribers -= 1
    if (eventSubscribers > 0 || eventUnsubscribe === null) return
    eventUnsubscribe()
    eventUnsubscribe = null
  }
}
