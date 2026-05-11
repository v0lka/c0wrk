const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  lmstudio: 'LM Studio',
  openai_compatible: 'OpenAI Compatible',
  chatgpt: 'ChatGPT',
}

const PROVIDER_KEYS = ['anthropic', 'gemini', 'lmstudio', 'openai_compatible', 'chatgpt']

interface ProviderSelectorProps {
  activeProvider: string
  onProviderChange: (provider: string) => void
}

export function ProviderSelector({ activeProvider, onProviderChange }: ProviderSelectorProps) {
  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-muted-foreground">Provider</label>
      <div className="flex items-center gap-3">
        <select
          value={activeProvider}
          onChange={(e) => onProviderChange(e.target.value)}
          className="c0-input h-9 px-3 rounded-md border border-input text-sm focus:outline-none min-w-[180px]"
        >
          {PROVIDER_KEYS.map((key) => (
            <option key={key} value={key}>
              {PROVIDER_DISPLAY_NAMES[key]}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}
