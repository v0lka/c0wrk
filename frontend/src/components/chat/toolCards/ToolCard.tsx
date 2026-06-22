import React, { useMemo } from 'react'
import { Check, X, Loader2, AlertTriangle } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import type { DisplayItem } from '@/types/messages'
import { resolveCardConfig } from './toolCardRegistry'
import { FileLink } from './shared/FileLink'

type ToolItem = Extract<DisplayItem, { kind: 'tool' }>

function StatusIcon({ status }: { status: ToolItem['status'] }) {
  if (status === 'success') return <Check className="h-3.5 w-3.5 text-success" />
  if (status === 'error') return <X className="h-3.5 w-3.5 text-destructive" />
  if (status === 'awaiting_confirmation') return <AlertTriangle className="h-3.5 w-3.5 text-warning" />
  return <Loader2 className="h-3.5 w-3.5 text-muted-foreground animate-spin" />
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

  // For cached/batched tools, args are from tool_result_read or batch, not the original tool.
  // Show the original tool name as title; range info is patched into the header.
  const title = isBatched
    ? item.toolName.replace(' (batched)', '')
    : isCached
    ? item.toolName.replace(' (cached)', '')
    : config.extractTitle(item.parsedArgs, item.args)
  const hint = (isCached || isBatched) ? undefined : config.extractHint?.(item.parsedArgs, item.args)
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

  // Determine if title references a file path (for clickable navigation)
  // Cached/batched tools never get file links — we don't have the original path.
  const isFileTool = !isCached && !isBatched && ['write_file', 'edit_file', 'read_file', 'read_skill_resource',
    'create_directory', 'delete_file', 'delete_directory', 'list_directory'].includes(item.toolName)
  const filePath = hint && isFileTool ? hint : undefined

  const titleNode = useMemo(() => (
    <span className="text-sm min-w-0 truncate" title={filePath ? undefined : (hint || title)}>
      <span className="text-muted-foreground">{config.verb}: </span>
      {filePath ? (
        <FileLink path={filePath} label={title} className="text-sm" />
      ) : (
        title
      )}
    </span>
  ), [config.verb, filePath, title, hint])

  const cacheRangeNode = useMemo(() => cacheRange ? (
    <span className="text-xs text-hljs-comment">
      fragment: lines {cacheRange.start}–{cacheRange.end} of {cacheRange.total}
    </span>
  ) : null
  , [cacheRange])

  // Body-less cards: single-line flat display
  if (!Body) {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground flex-wrap">
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
      icon={<Icon className={`h-3.5 w-3.5 ${item.status === 'error' ? 'text-destructive' : ''}`} />}
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
