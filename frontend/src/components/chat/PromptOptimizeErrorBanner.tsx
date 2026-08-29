// PromptOptimizeErrorBanner shows a dismissible warning when prompt
// optimization fails (e.g. the model produces no usable output). Reads the
// ACTIVE session's transient `optimizeError` from the chatInputStore (keyed
// per session — an optimize failure on a background session surfaces only
// when its session is active) and renders nothing when the error is null.
// Styled per the design system (warning color #d19a66 via Tailwind
// `text-warning` / `bg-warning` tokens).

import { Sparkles, X } from 'lucide-react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatInputStore, getInputState, NULL_SESSION_KEY } from '@/stores/chatInputStore'

export function PromptOptimizeErrorBanner(): React.JSX.Element | null {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const promptOptimizeError = useChatInputStore((s) =>
    getInputState(s.inputs, activeSessionId).optimizeError,
  )
  const setOptimizeError = useChatInputStore((s) => s.setOptimizeError)

  if (!promptOptimizeError) return null

  return (
    <div className="flex items-center gap-2 px-3 py-1.5 shrink-0 border-b border-warning/30 bg-warning/5">
      <Sparkles className="size-3.5 shrink-0 text-warning" />
      <span className="text-xs text-warning flex-1 min-w-0">{promptOptimizeError}</span>
      <button
        type="button"
        onClick={() => setOptimizeError(activeSessionId ?? NULL_SESSION_KEY, null)}
        className="inline-flex items-center justify-center size-4 rounded-sm text-warning/70 hover:text-warning hover:bg-warning/10 shrink-0"
        title="Dismiss"
        aria-label="Dismiss prompt optimization error"
      >
        <X className="size-3" />
      </button>
    </div>
  )
}
