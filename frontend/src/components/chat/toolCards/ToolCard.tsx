import React from 'react'
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

export const ToolCard = React.memo(function ToolCard({ item }: { item: ToolItem }) {
  const config = resolveCardConfig(item.toolName, item.source)
  const title = config.extractTitle(item.parsedArgs, item.args)
  const hint = config.extractHint?.(item.parsedArgs, item.args)
  const Icon = config.icon
  const Body = config.Body

  const mcpBadge = item.source && item.source !== '' && item.source !== 'core'
    ? <span className="text-[10px] font-medium bg-muted-foreground/15 text-foreground px-1.5 py-0.5 rounded">MCP</span>
    : undefined

  // Determine if title references a file path (for clickable navigation)
  const isFileTool = ['write_file', 'edit_file', 'read_file', 'read_skill_resource',
    'create_directory', 'delete_file', 'delete_directory', 'list_directory'].includes(item.toolName)
  const filePath = hint && isFileTool ? hint : undefined

  const titleNode = (
    <span className="text-sm min-w-0 overflow-hidden">
      <span className="text-muted-foreground">{config.verb}: </span>
      {filePath ? (
        <FileLink path={filePath} label={title} className="text-sm" />
      ) : (
        <span title={hint}>{title}</span>
      )}
    </span>
  )

  // Body-less cards: single-line flat display
  if (!Body) {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <StatusIcon status={item.status} />
        <Icon className={`h-3.5 w-3.5 ${item.status === 'error' ? 'text-destructive' : ''}`} />
        {titleNode}
        {mcpBadge}
      </div>
    )
  }

  // Cards with body: collapsible
  return (
    <CollapsibleBlock
      icon={<Icon className={`h-3.5 w-3.5 ${item.status === 'error' ? 'text-destructive' : ''}`} />}
      label={titleNode}
      statusIcon={<StatusIcon status={item.status} />}
      badge={mcpBadge}
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
