import { Input } from '@/components/ui/input'

interface ModelSelectorProps {
  activeProvider: string
  model: string
  models: string[]
  modelsLoading: boolean
  modelsError: string | null
  disabled: boolean
  placeholder: string
  onModelChange: (model: string) => void
}

export function ModelSelector({
  model,
  models,
  modelsLoading,
  modelsError,
  disabled,
  placeholder,
  onModelChange,
}: ModelSelectorProps) {
  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-muted-foreground">Model</label>
      <div className="flex flex-col gap-2">
        {models.length > 0 && !modelsLoading ? (
          // Non-editable select when models are successfully fetched
          <select
            value={model}
            onChange={(e) => onModelChange(e.target.value)}
            disabled={disabled}
            className="h-9 px-3 rounded-md border border-input bg-background text-sm focus:outline-none"
          >
            <option value="">Select a model...</option>
            {models.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        ) : (
          // Plain text input when no models available or fetch failed
          <Input
            placeholder={placeholder}
            value={model}
            onChange={(e) => onModelChange(e.target.value)}
            disabled={disabled || modelsLoading}
            className="h-9 text-sm"
          />
        )}
        {modelsLoading && (
          <span className="text-xs text-muted-foreground">Loading models...</span>
        )}
        {modelsError && (
          <span className="text-xs text-destructive">{modelsError}</span>
        )}
      </div>
    </div>
  )
}
