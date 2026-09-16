// PriorArtRow — the research dashboard's prior-art surface.
//
// A compact row (rendered only when the active research project has prior-art
// entries) exposing two gestures: Open (the raw prior-art.md catalog in the
// file viewer) and Deep read (dispatch the `study-paper` skill at the forced
// deep-appraisal depth over the catalog's referenced works, through the shared
// message sender — mirroring ResearchQuickActions). The Deep-read prompt is a
// constant in the audit module (paperActions.ts), never inline.

import { FileText, Microscope } from 'lucide-react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useResearchStore } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import {
  STUDY_PAPER_SKILL,
  buildDeepReadPriorArtPrompt,
} from '@/components/papers/paperActions'

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground'

interface PriorArtRowProps {
  /** Absolute path of the prior-art catalog (`<research-root>/R-NNN/prior-art.md`). */
  path: string
  /** Number of prior-art entries (shown as a count badge). */
  count: number
}

/**
 * One row: the prior-art label + count and its Open / Deep-read actions.
 * Mounting is gated by the caller (only a project with prior art renders it),
 * so an unavailable catalog never offers a dispatch.
 */
export function PriorArtRow({ path, count }: PriorArtRowProps) {
  const { send } = useMessageSender()

  const openPriorArt = () => {
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openFile(path)
  }

  // [22]a: send() renders sendMessage failures in-chat itself, but RETHROWS
  // when the auto-created session fails (the documented splash race). The
  // rejection surfaces on the research panel's own error banner — deliberately
  // NOT a global toast, which could fire while the user is typing.
  const deepRead = (newSession: boolean) => {
    Promise.resolve(
      send(
        buildDeepReadPriorArtPrompt(path),
        [STUDY_PAPER_SKILL],
        undefined,
        undefined,
        { newSession },
      ),
    ).catch((err) => {
      useResearchStore
        .getState()
        .setError(
          `Failed to dispatch ${STUDY_PAPER_SKILL}: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
    })
  }

  return (
    <div
      data-testid="research-prior-art-row"
      className="flex items-center gap-1 rounded-md border border-border bg-background/40 px-2 py-1"
    >
      <FileText className="size-3 shrink-0 text-muted-foreground" />
      <span className="text-[11px] font-medium text-foreground">Prior art</span>
      <span data-testid="research-prior-art-count" className="text-[10px] text-muted-foreground">
        {count}
      </span>
      <div className="ml-auto flex shrink-0 items-center gap-0.5">
        <button
          type="button"
          data-testid="research-prior-art-open"
          onClick={openPriorArt}
          title="Open the prior-art catalog"
          aria-label="Open the prior-art catalog"
          className={ACTION_BUTTON_CLASS}
        >
          <FileText className="size-3" />
        </button>
        <button
          type="button"
          data-testid="research-prior-art-deep-read"
          onClick={(e) => deepRead(e.shiftKey)}
          title="Deep read the prior art (Shift = new session)"
          className={ACTION_BUTTON_CLASS}
        >
          <Microscope className="size-3" />
          Deep read
        </button>
      </div>
    </div>
  )
}
