import { useRef } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData, invalidateConfigCache } from '@/hooks/useConfigData'
import { setDefaultModel } from '@/api/config'
import { ModelPickerMenu } from '@/components/ui/ModelPickerMenu'

/**
 * ModelCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message model override. "Default" means the global default_model is
 * used. The selection is persisted in inputModeStore and survives restarts.
 *
 * The dropdown UI itself is the shared {@link ModelPickerMenu} (also used by
 * the Settings → LLM default-model picker); this component adds the chat
 * toolbar's persistence semantics only.
 *
 * Persist the picked model as the global default_model (LLM section of the
 * config). UpdateLLMConfig is a partial merge so only default_model is
 * written — provider configs and API keys are untouched. The per-message
 * override (selectedModel) is applied optimistically so the next message
 * uses the picked model immediately, even before the rebuilt router
 * propagates.
 *
 * Race handling: save requests run in click order. A failed request only
 * rolls back a still-active optimistic selection to the most recent value
 * confirmed by the backend; it never restores an earlier optimistic choice
 * that may itself be queued to fail. The config cache is invalidated on
 * settle regardless, so every consumer re-syncs with backend state.
 */
export function ModelCombobox({ disabled = false }: { disabled?: boolean }) {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const setSelectedModel = useInputModeStore((s) => s.setSelectedModel)

  const { allModels: modelInfos, defaultModel, loaded } = useConfigData()

  // The first model pick starts immediately; while it is in flight, later
  // picks are chained here so backend mutations retain user click order.
  const pendingDefaultSavesRef = useRef(0)
  const defaultSaveQueueRef = useRef<Promise<void>>(Promise.resolve())
  // Every local choice gets a monotonically increasing version. A queued save
  // may update the rollback point only when it still represents the user's
  // latest choice; otherwise a later pick (including "Default") wins.
  const selectionVersionRef = useRef(0)
  // Keep the last selection confirmed by a successful backend save separate
  // from the optimistic store value. A queued request must never use a prior
  // optimistic choice as rollback state: that prior request may fail too.
  const confirmedSelectedModelRef = useRef<string | null>(useInputModeStore.getState().selectedModel)

  const handleSelectModel = (id: string | null) => {
    if (id === null) {
      // "Default" is an immediately valid local choice (null = use the
      // persisted global default). Invalidate every queued override before
      // fixing the fallback, so a late success cannot overwrite it after
      // this user choice.
      selectionVersionRef.current += 1
      confirmedSelectedModelRef.current = null
      setSelectedModel(null)
      return
    }

    const selectionVersion = ++selectionVersionRef.current
    setSelectedModel(id)
    const persist = async () => {
      try {
        await setDefaultModel(id)
        if (selectionVersionRef.current === selectionVersion) {
          confirmedSelectedModelRef.current = id
        }
      } catch {
        // A later click may have selected another model while this request was
        // in flight. Only this request's still-current optimistic value may be
        // reverted; an older rejection must not overwrite that newer choice.
        if (selectionVersionRef.current === selectionVersion && useInputModeStore.getState().selectedModel === id) {
          setSelectedModel(confirmedSelectedModelRef.current)
        }
      } finally {
        invalidateConfigCache()
      }
    }

    // Start the first request immediately for responsive feedback. Only later
    // selections wait for it, preserving click order without delaying a lone
    // selection to a microtask.
    const enqueue = () => {
      pendingDefaultSavesRef.current += 1
      if (pendingDefaultSavesRef.current === 1) {
        defaultSaveQueueRef.current = persist()
      } else {
        defaultSaveQueueRef.current = defaultSaveQueueRef.current.then(persist)
      }
      defaultSaveQueueRef.current = defaultSaveQueueRef.current.finally(() => {
        pendingDefaultSavesRef.current -= 1
      })
    }
    enqueue()
  }

  return (
    <ModelPickerMenu
      models={modelInfos}
      defaultModel={defaultModel}
      loaded={loaded}
      value={selectedModel}
      onSelect={handleSelectModel}
      disabled={disabled}
      ariaLabel="Select model"
    />
  )
}
