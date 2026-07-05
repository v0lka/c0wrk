import { useEffect } from 'react'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useConfigData } from '@/hooks/useConfigData'
import { useDropdown } from '@/hooks/useDropdown'
import { findModelInfo } from '@/lib/modelId'

/**
 * ReasoningCombobox renders a compact dropdown in the chat toolbar for selecting
 * a per-message reasoning effort. It reads AllModels from GetConfig to find the
 * native reasoning options for the currently selected model's family.
 * "Default" means the family default (= maximum reasoning) is used.
 * The selection is persisted in inputModeStore and survives restarts.
 */
export function ReasoningCombobox() {
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const selectedReasoning = useInputModeStore((s) => s.selectedReasoning)
  const setSelectedReasoning = useInputModeStore((s) => s.setSelectedReasoning)

  const { allModels, defaultModel, loaded } = useConfigData()
  const { isOpen, setIsOpen, containerRef } = useDropdown()

  // When selectedModel changes, reset reasoning if the new model's family doesn't support it.
  // selectedModel may be a composite "provider/name" id (see ModelCombobox) or
  // null (use the global default_model, which may itself be bare or composite),
  // so resolve via findModelInfo which handles both forms.
  useEffect(() => {
    if (!selectedReasoning) return
    const effectiveModel = selectedModel ?? defaultModel
    const info = findModelInfo(allModels, effectiveModel)
    const reasoning = info?.reasoning
    if (!reasoning || !reasoning.options.includes(selectedReasoning)) {
      setSelectedReasoning(null)
    }
  }, [selectedModel, defaultModel, allModels, selectedReasoning, setSelectedReasoning])

  // Find reasoning info for the effective model.
  const effectiveModel = selectedModel ?? defaultModel
  const modelInfo = findModelInfo(allModels, effectiveModel)
  const reasoning = modelInfo?.reasoning ?? null

  // If still loading config, render nothing (matches pre-loaded behavior).
  if (!loaded) return null

  // If family doesn't support reasoning, render nothing.
  if (!reasoning) return null

  const familyDefault = reasoning.default
  const options = reasoning.options

  const displayLabel = selectedReasoning ?? `Auto (${familyDefault})`

  return (
    <div className="relative shrink-0" ref={containerRef}>
      <button
        type="button"
        className="flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-input bg-background hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors max-w-[150px] truncate"
        onClick={() => setIsOpen((v) => !v)}
        title={`Reasoning: ${displayLabel}`}
      >
        <span className="truncate">{displayLabel}</span>
        <svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {isOpen && (
        <div className="absolute bottom-full left-0 mb-1 w-48 rounded-md border bg-popover shadow-md z-50 max-h-64 overflow-y-auto custom-scrollbar">
          {/* Default option */}
          <button
            type="button"
            className={`flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted ${!selectedReasoning ? 'bg-primary/10 font-medium' : ''}`}
            onClick={() => { setSelectedReasoning(null); setIsOpen(false) }}
          >
            <span className="flex-1 text-left">
              Default ({familyDefault})
            </span>
            {!selectedReasoning && (
              <span className="text-[10px] text-primary">active</span>
            )}
          </button>

          {/* Specific options */}
          {options.map((opt) => {
            const isSelected = selectedReasoning === opt
            return (
              <button
                key={opt}
                type="button"
                className={`flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted ${isSelected ? 'bg-primary/10 font-medium' : ''}`}
                onClick={() => { setSelectedReasoning(opt); setIsOpen(false) }}
              >
                <span className="flex-1 text-left">{opt}</span>
                {isSelected && (
                  <span className="text-[10px] text-primary">selected</span>
                )}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
