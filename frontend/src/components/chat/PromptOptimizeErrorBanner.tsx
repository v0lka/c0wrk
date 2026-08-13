// PromptOptimizeErrorBanner shows a dismissible warning when prompt
// optimization fails (e.g. the model produces no usable output). Reads the
// transient `promptOptimizeError` from the attachmentsStore and renders
// nothing when the error is null. Styled per the design system
// (warning color #d19a66 via Tailwind `text-warning` / `bg-warning` tokens).

import { Sparkles, X } from 'lucide-react'
import { useAttachmentsStore } from '@/stores/attachmentsStore'

export function PromptOptimizeErrorBanner(): React.JSX.Element | null {
  const promptOptimizeError = useAttachmentsStore((s) => s.promptOptimizeError)
  const setPromptOptimizeError = useAttachmentsStore((s) => s.setPromptOptimizeError)

  if (!promptOptimizeError) return null

  return (
    <div className="flex items-center gap-2 px-3 py-1.5 shrink-0 border-b border-warning/30 bg-warning/5">
      <Sparkles className="size-3.5 shrink-0 text-warning" />
      <span className="text-xs text-warning flex-1 min-w-0">{promptOptimizeError}</span>
      <button
        type="button"
        onClick={() => setPromptOptimizeError(null)}
        className="inline-flex items-center justify-center size-4 rounded-sm text-warning/70 hover:text-warning hover:bg-warning/10 shrink-0"
        title="Dismiss"
        aria-label="Dismiss prompt optimization error"
      >
        <X className="size-3" />
      </button>
    </div>
  )
}
