// The tooltip builder for the one-shot service-call buttons (Optimize prompt,
// Generate commit message). Extracted so BOTH surfaces gate the long-wait
// wording on the same rule and share one copy of it — see
// lib/embeddedColdLoadHint for why the gate exists.

import { useCallback } from 'react'
import { useConfigData } from '@/hooks/useConfigData'
import { useEmbeddedLLMStatus } from '@/stores/embeddedLLMStore'
import { isColdEmbeddedTarget, serviceCallTitle } from '@/lib/embeddedColdLoadHint'

/** Build the `title` of a service-call button from its in-flight flag.
 *
 *  `effectiveModelId` is the model the service RPC ACTUALLY RUNS ON — not the
 *  per-message override a chat surface would send with a task. Today that is
 *  always the configured global default (`llm.default_model`): `OptimizePrompt`
 *  and `GenerateCommitMessage` take no model argument, and the backend's
 *  cold-load gate resolves the default from the cached router. So every caller
 *  passes `null`, meaning "the configured default", which this hook resolves
 *  from the shared config cache. Pass a real id ONLY if a service RPC ever grows
 *  a model parameter — passing a per-message pick here would gate the hint on a
 *  model the call never touches, which both mis-warns (a cold embedded pick
 *  beside a remote default) and mis-silences (the reverse).
 *
 *  The resolved model is compared against the embedded snapshot's `model_id`, so
 *  a remote provider — the default configuration — never sees the cold-load
 *  wording, and neither does an already-resident embedded model.
 *
 *  The returned callback is stable across renders while the gate is unchanged. */
export function useServiceCallTitle(
  effectiveModelId: string | null,
): (pending: boolean, pendingLabel: string, idleTitle: string) => string {
  const { defaultModel } = useConfigData()
  const status = useEmbeddedLLMStatus()
  const coldEmbedded = isColdEmbeddedTarget(effectiveModelId ?? defaultModel, status)
  return useCallback(
    (pending: boolean, pendingLabel: string, idleTitle: string) =>
      serviceCallTitle({ pending, coldEmbedded, pendingLabel, idleTitle }),
    [coldEmbedded],
  )
}
