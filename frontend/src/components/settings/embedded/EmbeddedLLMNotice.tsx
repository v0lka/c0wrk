// EmbeddedLLMNotice — the supervisor-notice banner of the embedded local
// model's Settings block: one non-terminal finding from core (the load-time
// free-memory pre-flight), delivered by the global `embedded_llm:notice`
// event into the store and rendered here until dismissed or replaced.
//
// It is deliberately a WARNING beside the status, never a status: the notice
// carries no transition (the state machine's own verdict still travels through
// `embedded_llm:state`), and its message already says what the launch will do
// anyway — proceed on the unchanged plan. The evidence figures ride beside the
// prose so an operator can compare them with the live device-memory panel
// without re-deriving anything.
//
// Zoom-safety: plain flow, an inline icon, no viewport units, no anchored
// placement.

import { X, TriangleAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useEmbeddedLLMStore, useEmbeddedLLMNotice } from '@/stores/embeddedLLMStore'

export function EmbeddedLLMNotice() {
  const notice = useEmbeddedLLMNotice()
  // A store action is a stable function reference, so this selector never
  // re-renders on its own.
  const dismiss = useEmbeddedLLMStore((s) => s.dismissNotice)
  if (!notice) return null

  return (
    <div
      className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2"
      role="status"
      data-testid="embedded-llm-notice"
      data-notice-kind={notice.kind}
    >
      <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-warning" aria-hidden />
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="text-xs text-warning">{notice.message}</span>
        <span className="text-xs text-muted-foreground">
          {notice.free_device_mib} MiB free against a {notice.expected_device_mib} MiB projection.
        </span>
      </div>
      <Button
        variant="ghost"
        size="sm"
        className="ml-auto h-5 w-5 shrink-0 p-0"
        onClick={dismiss}
        aria-label="Dismiss notice"
        title="Dismiss this notice"
        data-testid="embedded-llm-notice-dismiss"
      >
        <X className="size-3" aria-hidden />
      </Button>
    </div>
  )
}
