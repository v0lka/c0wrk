import { useCallback, useEffect, useRef, useState } from 'react'
import { GitBranch, Tag } from 'lucide-react'
import { formatRelativeTime } from '@/lib/formatters'
import { totalChanges } from '@/lib/commitStats'
import { cn } from '@/lib/utils'
import { refColor } from '@/components/GitPanel/gitGraphRender'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { CommitStats } from '@/types/models'

/** Display label of a ref decoration: git log's `tag: ` prefix is dropped. */
const refLabel = (ref: string): string => ref.replace(/^tag:\s*/, '')

/** A tag decoration (git log's `tag: vX.Y` form) — everything else is a branch/HEAD ref. */
const isTagRef = (ref: string): boolean => ref.startsWith('tag:')

interface GitGraphCardMetaProps {
  /** Commit author display name and ISO date, rendered as «author · time». */
  author: string
  date: string
  /** Ref decorations (branches, tags, HEAD) — the graph node's refs. */
  refs: readonly string[]
  /** Per-commit stats triple; hidden when absent or all-zero. */
  stats: CommitStats | undefined
}

/**
 * The commit card's second header line: «author · time», the +/~/− stats
 * triple and the ref decorations right-aligned past them. On measured
 * overflow the refs fold into a first-ref + «+N» badge whose tooltip lists
 * the folded refs.
 *
 * The collapse decision is measured, not guessed: the hidden absolute layer
 * below always renders the line's content at natural width with ALL refs,
 * so the comparison is invariant to the collapse state and the flag cannot
 * oscillate. Behavior contract: specs/domains/frontend/git-history.md
 * (viewer Git-graph page).
 */
export function GitGraphCardMeta({ author, date, refs, stats }: GitGraphCardMetaProps) {
  const [collapsed, setCollapsed] = useState(false)
  /** Clipping box = the line's available content width. */
  const clipRef = useRef<HTMLDivElement>(null)
  /** Natural-width render of the whole line's content. */
  const naturalRef = useRef<HTMLDivElement>(null)

  const measure = useCallback(() => {
    const clip = clipRef.current
    const natural = naturalRef.current
    if (!clip || !natural) return
    const overflows = natural.offsetWidth > clip.offsetWidth
    setCollapsed((prev) => (prev === overflows ? prev : overflows))
  }, [])

  const refsKey = refs.join('\n')
  // Measure on mount and whenever the content's shape changes — every dep
  // here feeds the natural width.
  useEffect(() => {
    measure()
  }, [measure, refsKey, author, date, stats])

  // Re-measure when the available width changes; the clipping box tracks the
  // line width, so observing it covers viewer resizes. Guarded: jsdom has no
  // ResizeObserver constructor (the mount effect above still measures once).
  useEffect(() => {
    const clip = clipRef.current
    if (!clip || typeof ResizeObserver === 'undefined') return undefined
    const observer = new ResizeObserver(measure)
    observer.observe(clip)
    return () => observer.disconnect()
  }, [measure])

  // Collapsed form: the first ref stays, the rest fold into the «+N» badge.
  const shown = collapsed ? refs.slice(0, 1) : refs
  const hidden = collapsed ? refs.slice(1) : []

  return (
    <div className="relative flex items-center gap-2 pl-7 text-xs leading-none text-muted-foreground">
      {/* truncate: the visible author may ellipsize when even the collapsed
          form cannot fit — a long author gives way, it never pushes the refs
          area (and the «+N» badge) out of the card. The measurement layer
          above stays untruncated so the comparison sees the natural width. */}
      <MetaContent author={author} date={date} stats={stats} refs={shown} truncate collapsedBadge={hidden.length > 0 ? <CollapsedRefsBadge hidden={hidden} /> : undefined} />
      {/* Measurement layer: the line's content at natural width inside the
          line's true available width (the inset skips the pl-7 indent).
          Hidden from layout, a11y and pointer events; never renders the
          collapsed badge. */}
      <div
        aria-hidden
        ref={clipRef}
        data-testid="meta-clip"
        className="pointer-events-none invisible absolute inset-y-0 right-0 left-7 overflow-hidden"
      >
        <div ref={naturalRef} className="flex h-max w-max items-center gap-2">
          <MetaContent author={author} date={date} stats={stats} refs={refs} />
        </div>
      </div>
    </div>
  )
}

/** The line's children: author · time, the stats triple, the refs area.
 *  Rendered twice — truncated in the visible line, natural-width in the
 *  measurement layer (truncate=false keeps the author at full length). */
function MetaContent({
  author,
  date,
  stats,
  refs,
  truncate = false,
  collapsedBadge,
}: {
  author: string
  date: string
  stats: CommitStats | undefined
  refs: readonly string[]
  /** Visible line: the author span may ellipsize. Measurement layer: never. */
  truncate?: boolean
  /** The «+N» collapsed-refs badge (visible collapsed line only). */
  collapsedBadge?: React.ReactNode
}) {
  return (
    <>
      <span className={truncate ? 'min-w-0 truncate select-text' : 'shrink-0 whitespace-nowrap select-text'}>
        {author} · {formatRelativeTime(date)}
      </span>
      {stats && totalChanges(stats) > 0 && (
        <span className="flex shrink-0 items-center gap-1 tabular-nums">
          <span className="text-success">+{stats.added}</span>
          <span className="text-warning">~{stats.modified}</span>
          <span className="text-destructive">−{stats.deleted}</span>
        </span>
      )}
      <div className="ml-auto flex shrink-0 items-center gap-1">
        {refs.map((ref) => (
          <RefBadge key={ref} name={ref} />
        ))}
        {collapsedBadge}
      </div>
    </>
  )
}

/** One ref decoration: the colored pill the Git panel's rows use. */
function RefBadge({ name }: { name: string }) {
  return (
    <span
      className={cn(
        'shrink-0 rounded bg-muted/40 px-1 py-px text-xs font-medium leading-none select-text',
        refColor(name),
      )}
    >
      {refLabel(name)}
    </span>
  )
}

/** Icon for a ref's kind — a tag gets the Tag glyph, anything else GitBranch. */
function RefIcon({ name, className }: { name: string; className?: string }) {
  return isTagRef(name) ? <Tag className={className} /> : <GitBranch className={className} />
}

/** The «+N» badge for the refs that did not fit: the first hidden ref's kind
 *  icon + the hidden count; hovering opens a tooltip listing every one. */
function CollapsedRefsBadge({ hidden }: { hidden: readonly string[] }) {
  const first = hidden[0]!
  return (
    /* Local provider: the badge must work wherever the card renders — the
       app mounts a global one, tests and isolated hosts may not. The delay
       falls back to the shared TOOLTIP_DELAY_MS default. */
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <span
            data-testid="collapsed-refs-badge"
            className="flex shrink-0 items-center gap-0.5 rounded bg-muted/40 px-1 py-px text-xs font-medium leading-none select-none"
          >
            <RefIcon name={first} className={cn('size-3 shrink-0', refColor(first))} />
            <span className="text-muted-foreground">+{hidden.length}</span>
          </span>
        </TooltipTrigger>
        <TooltipContent className="p-2">
          <div className="flex flex-col gap-1">
            {hidden.map((ref) => (
              <span key={ref} className={cn('flex items-center gap-1 text-xs font-medium', refColor(ref))}>
                <RefIcon name={ref} className="size-3 shrink-0" />
                {refLabel(ref)}
              </span>
            ))}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
