// A paper artifact rendered as Markdown (the note / appraisal / compare /
// flashcards / literature sections), with honest loading / error / empty states.

import { Markdown } from '@/lib/markdownConfig'
import type { PaperArtifact } from './usePaperArtifacts'

interface PaperMarkdownSectionProps {
  artifact: PaperArtifact
  /** Shown when none of the section's candidate files exist. */
  emptyText: string
  /** Absolute path of the rendered document (relative images resolve against it). */
  baseFilePath?: string | null
  testId: string
}

export function PaperMarkdownSection({
  artifact,
  emptyText,
  baseFilePath,
  testId,
}: PaperMarkdownSectionProps) {
  if (artifact.loading) {
    return (
      <p data-testid={`${testId}-loading`} className="px-3 py-4 text-center text-[11px] text-muted-foreground">
        Loading…
      </p>
    )
  }
  if (artifact.error !== null) {
    return (
      <p data-testid={`${testId}-error`} className="px-3 py-4 text-center text-[11px] text-destructive">
        {artifact.error}
      </p>
    )
  }
  if (artifact.content === '') {
    return (
      <p data-testid={`${testId}-empty`} className="px-3 py-4 text-center text-[11px] text-muted-foreground">
        {emptyText}
      </p>
    )
  }
  return (
    <div
      data-testid={`${testId}-markdown`}
      className="min-h-0 flex-1 overflow-auto custom-scrollbar px-3 py-2"
    >
      <Markdown content={artifact.content} baseFilePath={baseFilePath ?? null} workspaceRoot={null} />
    </div>
  )
}
