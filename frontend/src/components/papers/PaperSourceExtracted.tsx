// The Extracted sub-view of the Source section — the paper's source.md
// rendered as REAL markdown.
//
// The document is split into blank-line-separated blocks (lib/
// paperSourceBlocks), each carrying the source line range it was built from.
// Every block renders through the shared Markdown component inside a
// div[data-paper-line] keyed by the block's first source line, so a resolved
// anchor (a 0-based line index from lib/paperAnchors) scrolls to the block
// that CONTAINS the hit line and highlights it. The block views are memoized
// (the shared Markdown already caches its parse), so an anchor click — which
// flips the highlight of at most two blocks — never re-parses the rest.

import { memo, useEffect, useMemo, useRef } from 'react'
import { Markdown } from '@/lib/markdownConfig'
import { cn } from '@/lib/utils'
import { blockContainingLine, splitSourceBlocks } from '@/lib/paperSourceBlocks'
import type { PendingAnchor } from './PaperSourceView'
import type { PaperArtifact } from './usePaperArtifacts'

interface PaperSourceBlockViewProps {
  startLine: number
  text: string
  highlighted: boolean
  baseFilePath: string | null
}

/** One block rendered as markdown. */
const PaperSourceBlockView = memo(
  function PaperSourceBlockView({ startLine, text, highlighted, baseFilePath }: PaperSourceBlockViewProps) {
    return (
      <div
        data-paper-line={startLine}
        className={cn('my-1 rounded px-2 transition-colors', highlighted && 'bg-highlight/20')}
      >
        <Markdown content={text} baseFilePath={baseFilePath} workspaceRoot={null} />
      </div>
    )
  },
  (prev, next) =>
    prev.startLine === next.startLine &&
    prev.text === next.text &&
    prev.highlighted === next.highlighted &&
    prev.baseFilePath === next.baseFilePath,
)

interface PaperSourceExtractedProps {
  artifact: PaperArtifact
  pending: PendingAnchor | null
  /** Absolute path of source.md (relative image resolution base). */
  baseFilePath?: string | null
}

export function PaperSourceExtracted({ artifact, pending, baseFilePath }: PaperSourceExtractedProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const blocks = useMemo(() => splitSourceBlocks(artifact.content), [artifact.content])
  const hitBlock = pending !== null ? blockContainingLine(blocks, pending.line) : null

  // A pending anchor scrolls to the block containing the hit line. The nonce
  // in `pending` re-triggers the scroll even when a second anchor lands in
  // the same block.
  useEffect(() => {
    if (pending === null) return
    const block = blockContainingLine(blocks, pending.line)
    if (block === null) return
    const el = containerRef.current?.querySelector(`[data-paper-line="${block.startLine}"]`)
    if (el instanceof HTMLElement && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ block: 'center' })
    }
  }, [pending, blocks])

  return (
    <div
      ref={containerRef}
      data-testid="paper-source"
      className="min-h-0 flex-1 overflow-auto custom-scrollbar py-2"
    >
      {artifact.loading ? (
        <p className="px-2 py-3 text-center text-muted-foreground">Loading…</p>
      ) : artifact.error !== null ? (
        <p data-testid="paper-source-error" className="px-2 py-3 text-center text-destructive">
          {artifact.error}
        </p>
      ) : artifact.content === '' ? (
        <p data-testid="paper-source-empty" className="px-2 py-3 text-center text-muted-foreground">
          No source text captured for this paper yet.
        </p>
      ) : (
        blocks.map((block) => (
          <PaperSourceBlockView
            key={block.startLine}
            startLine={block.startLine}
            text={block.text}
            highlighted={hitBlock !== null && hitBlock.startLine === block.startLine}
            baseFilePath={baseFilePath ?? null}
          />
        ))
      )}
    </div>
  )
}
