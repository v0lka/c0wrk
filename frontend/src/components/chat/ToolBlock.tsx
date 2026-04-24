import React from 'react'
import { Check, X, Loader2, AlertTriangle, Wrench } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { ToolContentBlock, parseArgs } from '@/components/chat/ToolContentBlock'
import type { DisplayItem } from '@/types/messages'

type ToolItem = Extract<DisplayItem, { kind: 'tool' }>

function statusIcon(status: ToolItem['status']) {
  if (status === 'success') return <Check className="h-3.5 w-3.5 text-success" />
  if (status === 'error') return <X className="h-3.5 w-3.5 text-destructive" />
  if (status === 'awaiting_confirmation') return <AlertTriangle className="h-3.5 w-3.5 text-warning" />
  return <Loader2 className="h-3.5 w-3.5 text-muted-foreground animate-spin" />
}

export const ToolBlock = React.memo(function ToolBlock({ item }: { item: ToolItem }) {
  const formattedArgs = parseArgs(item.args, item.parsedArgs)

  const mcpBadge = item.source && item.source !== '' && item.source !== 'core'
    ? <span className="text-[10px] font-medium bg-muted-foreground/15 text-foreground px-1.5 py-0.5 rounded">MCP</span>
    : undefined

  return (
    <CollapsibleBlock
      icon={<Wrench className="h-3.5 w-3.5" />}
      label={item.toolName}
      statusIcon={statusIcon(item.status)}
      badge={mcpBadge}
    >
      <ToolContentBlock
        args={formattedArgs}
        result={item.result}
        resultLen={item.resultLen}
      />
    </CollapsibleBlock>
  )
})
