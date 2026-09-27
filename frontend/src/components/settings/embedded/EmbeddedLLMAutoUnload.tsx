// The idle-budget controls of the embedded local model.
//
// The master switch plus its minutes field. The switch uses the app's
// peer-based toggle (design tokens only, the same markup as UpdateSettings and
// ProxySettings); the minutes field is a DRAFT that commits on blur/Enter, so
// typing does not fire an RPC per keystroke, and an out-of-range entry reverts
// locally instead of paying a round trip for a guaranteed backend refusal
// (`SetEmbeddedLLMAutoUnload` requires MIN_AUTO_UNLOAD_MINUTES ≤ minutes ≤
// MAX_AUTO_UNLOAD_MINUTES; the ceiling mirrors `embeddedllm.
// MaxAutoUnloadMinutes`, above which the backend's minutes→nanoseconds multiply
// overflows and an "effectively never" budget inverts into "unload at once").
//
// The setting is an operator preference, not install state: the backend keeps
// it across a removal, and the values rendered here always come from the
// authoritative status snapshot — there is no optimistic copy.

import { Input } from '@/components/ui/input'
import { MAX_AUTO_UNLOAD_MINUTES, MIN_AUTO_UNLOAD_MINUTES } from '@/api/embedded'

export function EmbeddedLLMAutoUnload({
  enabled,
  minutes,
  draft,
  disabled,
  onToggle,
  onDraftChange,
  onCommit,
}: {
  enabled: boolean
  minutes: number
  draft: string | null
  disabled: boolean
  onToggle: (enabled: boolean) => void
  onDraftChange: (draft: string) => void
  onCommit: () => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <label className="relative inline-flex items-center">
        <input
          type="checkbox"
          className="sr-only peer"
          checked={enabled}
          disabled={disabled}
          aria-label="Auto unload"
          data-testid="embedded-llm-auto-unload"
          onChange={(e) => onToggle(e.target.checked)}
        />
        <div className="h-5 w-9 rounded-full bg-muted transition-colors after:absolute after:top-0.5 after:start-[2px] after:h-4 after:w-4 after:rounded-full after:bg-background after:transition-all after:content-[''] peer-checked:bg-primary peer-disabled:opacity-50" />
      </label>
      <span className="text-sm text-foreground">Auto unload</span>
      <div className="flex items-center gap-2">
        <label htmlFor="embedded-llm-auto-unload-minutes" className="text-xs text-muted-foreground">
          after
        </label>
        <Input
          id="embedded-llm-auto-unload-minutes"
          data-testid="embedded-llm-auto-unload-minutes"
          type="number"
          min={MIN_AUTO_UNLOAD_MINUTES}
          max={MAX_AUTO_UNLOAD_MINUTES}
          step={1}
          className="h-8 w-20 text-sm"
          value={draft ?? String(minutes)}
          disabled={disabled}
          onChange={(e) => onDraftChange(e.target.value)}
          onBlur={onCommit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommit()
          }}
        />
        <span className="text-xs text-muted-foreground">min idle</span>
      </div>
    </div>
  )
}
