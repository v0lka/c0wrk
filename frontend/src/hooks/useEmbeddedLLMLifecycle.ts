// The Embedded LLM block's mutating lifecycle actions (install / cancel /
// load / unload / remove) plus its store subscription, extracted from
// EmbeddedLLMSettings next to the two commit hooks that block already delegates
// to (useEmbeddedLLMAutoUnload / useEmbeddedLLMTuning).
//
// Every flow runs through the store's `runEmbeddedLLMAction`, which holds the
// busy window open across the post-action read-back: `busy` is what the block's
// buttons read for `disabled`, so clearing it earlier would re-enable them
// while the rendered state is still the pre-action one, and a fast second click
// would fire a duplicate RPC.
//
// The Remove CONFIRMATION stays in the component (pure UI state); this hook
// exposes the action to run once the dialog is answered.

import { useCallback, useEffect } from 'react'
import {
  cancelEmbeddedLLMInstall,
  installEmbeddedLLM,
  loadEmbeddedLLM,
  removeEmbeddedLLM,
  unloadEmbeddedLLM,
} from '@/api/embedded'
import {
  refreshEmbeddedLLMStatus,
  runEmbeddedLLMAction,
  subscribeEmbeddedLLMEvents,
  useEmbeddedLLMStore,
} from '@/stores/embeddedLLMStore'

/** The block's lifecycle actions, each already bound to its busy window. */
export interface EmbeddedLLMLifecycle {
  /** Runs the synchronous install gates; a rejection is an actionable refusal. */
  install: () => void
  /** Asks the in-flight background install to stop. Idempotent and
   *  asynchronous — the RPC only delivers the request, and the run's quiet end
   *  arrives later through `embedded_llm:state`, which re-reads the snapshot
   *  (and drops `installing`, returning the block to the Install button). */
  cancelInstall: () => void
  load: () => void
  unload: () => void
  /** Runs the removal. The caller confirms first — this is irreversible. */
  remove: () => void
}

export function useEmbeddedLLMLifecycle(): EmbeddedLLMLifecycle {
  const beginInstall = useEmbeddedLLMStore((s) => s.beginInstall)

  // ONE shared event subscription (refcounted in the store, so the status-bar
  // indicator mounting alongside the block does not double-apply an event) plus
  // the initial authoritative read. Nothing here installs or loads anything —
  // both are explicit user clicks only.
  useEffect(() => {
    const unsubscribe = subscribeEmbeddedLLMEvents()
    void refreshEmbeddedLLMStatus()
    return unsubscribe
  }, [])

  const install = useCallback(() => {
    void runEmbeddedLLMAction(
      'install',
      async () => {
        // Resolving means the synchronous gates passed: the multi-gigabyte run
        // is now in the background on the app context, reported through the
        // `embedded_llm:install_progress` events.
        await installEmbeddedLLM()
        beginInstall()
      },
      refreshEmbeddedLLMStatus,
    )
  }, [beginInstall])

  const cancelInstall = useCallback(
    () => void runEmbeddedLLMAction('cancel', cancelEmbeddedLLMInstall, refreshEmbeddedLLMStatus),
    [],
  )

  const load = useCallback(
    () => void runEmbeddedLLMAction('load', loadEmbeddedLLM, refreshEmbeddedLLMStatus),
    [],
  )
  const unload = useCallback(
    () => void runEmbeddedLLMAction('unload', unloadEmbeddedLLM, refreshEmbeddedLLMStatus),
    [],
  )
  const remove = useCallback(
    () => void runEmbeddedLLMAction('remove', removeEmbeddedLLM, refreshEmbeddedLLMStatus),
    [],
  )

  return { install, cancelInstall, load, unload, remove }
}
