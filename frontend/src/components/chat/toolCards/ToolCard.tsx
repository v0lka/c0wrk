import React, { useMemo } from 'react'
import { Check, X, Loader2, AlertTriangle } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import type { DisplayItem } from '@/types/messages'
import { useAttachmentName } from '@/stores/attachmentsStore'
import { resolveCardConfig } from './toolCardRegistry'
import { extractAttachmentId, extractFileLine } from './extractors'
import { FileLink } from './shared/FileLink'
import { EllipsisHint } from './shared/EllipsisHint'

type ToolItem = Extract<DisplayItem, { kind: 'tool' }>

function StatusIcon({ status }: { status: ToolItem['status'] }) {
  // `shrink-0` is required: inside a width-capped flex header the SVG is a flex
  // item, and for SVGs `min-width: auto` resolves to 0 (SVG has overflow:hidden),
  // so without `flex-shrink:0` the status glyph shrinks toward 0 the more the
  // title overflows — verified in headless Chromium (14px -> 7.77px without it).
  if (status === 'success') return <Check className="h-3.5 w-3.5 shrink-0 text-success" />
  if (status === 'error') return <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
  if (status === 'awaiting_confirmation') return <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning" />
  return <Loader2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground animate-spin" />
}

/** Parse cached result header: "[Lines X-Y of Z from cached TOOL result | hash: H]" */
function parseCacheRange(result?: string): { start: number; end: number; total: number } | null {
  if (!result) return null
  const m = result.match(/^\[Lines (\d+)-(\d+) of (\d+) from cached/)
  if (!m) return null
  return { start: +m[1]!, end: +m[2]!, total: +m[3]! }
}

export const ToolCard = React.memo(function ToolCard({ item }: { item: ToolItem }) {
  const config = resolveCardConfig(item.toolName, item.source)

  // Detect cached tool (tool_result_read emitted as "original_tool (cached)")
  const isCached = item.toolName.endsWith(' (cached)')
  // Detect batched tool (batch sub-call emitted as "original_tool (batched)")
  const isBatched = item.toolName.endsWith(' (batched)')
  const cacheRange = useMemo(() => parseCacheRange(item.result), [item.result])

  // read_attachment: resolve the opaque attachment id to the original file
  // name so the card shows "Read: report.pdf" instead of "Read: att-42".
  // Cached/batched variants never apply (read_attachment is non-cacheable).
  // Prefer the persisted name (authoritative — baked into tool-call metadata
  // by the backend, so it survives restart); fall back to the in-memory cache
  // (useAttachmentName) for live calls whose event predates the backend
  // resolver. Falls back to the id (config.extractTitle) when neither resolves.
  const isReadAttachment = !isCached && !isBatched && item.toolName === 'read_attachment'
  const storeName = useAttachmentName(
    isReadAttachment ? extractAttachmentId(item.parsedArgs, item.args) : undefined,
  )
  const attachmentName = isReadAttachment ? (item.attachmentName ?? storeName) : undefined

  // For cached/batched tools, args are from tool_result_read or batch, not the original tool.
  // Show the original tool name as title; range info is patched into the header.
  const title = isBatched
    ? item.toolName.replace(' (batched)', '')
    : isCached
    ? item.toolName.replace(' (cached)', '')
    : attachmentName ?? config.extractTitle(item.parsedArgs, item.args)
  const hint = isCached ? undefined : config.extractHint?.(item.parsedArgs, item.args)
  const Icon = config.icon
  const Body = config.Body

  const mcpBadge = useMemo(() =>
    item.source && item.source !== '' && item.source !== 'core'
      ? <span className="text-[10px] font-medium bg-muted-foreground/15 text-foreground px-1.5 py-0.5 rounded">MCP</span>
      : null
  , [item.source])

  // Cached badge
  const cachedBadge = useMemo(() =>
    isCached
      ? <span className="text-[10px] font-medium bg-info/15 text-info px-1.5 py-0.5 rounded">cached</span>
      : null
  , [isCached])

  // Batched badge
  const batchedBadge = useMemo(() =>
    isBatched
      ? <span className="text-[10px] font-medium bg-info/15 text-info px-1.5 py-0.5 rounded">batched</span>
      : null
  , [isBatched])

  // Cached tools never get file links — we don't have the original path.
  // Batched tools DO carry the sub-call's own args, so file links are possible.
  const baseToolName = isBatched ? item.toolName.replace(' (batched)', '') : isCached ? item.toolName.replace(' (cached)', '') : item.toolName
  const isFileTool = !isCached && ['write_file', 'edit_file', 'read_file', 'read_skill_resource',
    'create_directory', 'delete_file', 'delete_directory', 'list_directory'].includes(baseToolName)
  const filePath = hint && isFileTool ? hint : undefined
  const fileLine = useMemo(
    () => (filePath ? extractFileLine(item.parsedArgs, item.args) : undefined),
    [filePath, item.parsedArgs, item.args],
  )

  // fullText mirrors the visible title so the tooltip reveals exactly what the
  // header shows (verb + value) rather than a bare hint. This matters when
  // extractHint omits the value (e.g. bash commands ≤60 chars where the
  // command IS the title): without this, fullText would fall back to a bare
  // hint that is undefined, and the tooltip would never appear.
  const fullText = filePath
    ? `${config.verb}: ${filePath}`
    : hint
      ? `${config.verb}: ${hint}`
      : title
        ? `${config.verb}: ${title}`
        : config.verb
  const titleNode = useMemo(() => (
    <EllipsisHint fullText={fullText} alwaysShow={Boolean(filePath)} className="text-sm truncate">
      {filePath ? (
        <>
          <span className="text-muted-foreground">{config.verb}: </span>
          <FileLink path={filePath} line={fileLine} label={title} className="text-sm" nativeTitle={false} />
        </>
      ) : title ? (
        <>
          <span className="text-muted-foreground">{config.verb}: </span>
          {title}
        </>
      ) : (
        <span className="text-muted-foreground">{config.verb}</span>
      )}
    </EllipsisHint>
  ), [config.verb, filePath, fileLine, title, fullText])

  const cacheRangeNode = useMemo(() => cacheRange ? (
    <span className="text-xs text-hljs-comment">
      fragment: lines {cacheRange.start}–{cacheRange.end} of {cacheRange.total}
    </span>
  ) : null
  , [cacheRange])

  // Body-less cards: single-line flat display
  if (!Body) {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground min-w-0">
        <StatusIcon status={item.status} />
        <Icon className={`h-3.5 w-3.5 shrink-0 ${item.status === 'error' ? 'text-destructive' : ''}`} />
        {titleNode}
        {cachedBadge}
        {batchedBadge}
        {mcpBadge}
        {cacheRangeNode}
      </div>
    )
  }

  // Cards with body: collapsible
  return (
    <CollapsibleBlock
      icon={<Icon className={`h-3.5 w-3.5 shrink-0 ${item.status === 'error' ? 'text-destructive' : ''}`} />}
      label={titleNode}
      statusIcon={<StatusIcon status={item.status} />}
      badge={<>{cachedBadge}{batchedBadge}{mcpBadge}</>}
      headerExtra={cacheRangeNode}
      defaultOpen={item.status === 'error'}
    >
      <Body
        parsedArgs={item.parsedArgs}
        args={item.args}
        result={item.result}
        resultLen={item.resultLen}
        status={item.status}
      />
    </CollapsibleBlock>
  )
})
