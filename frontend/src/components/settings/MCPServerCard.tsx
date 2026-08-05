import { ChevronDown, ChevronRight, CheckCircle2, AlertCircle, Loader2, Pencil, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { MCPServerStatus, ToolInfo } from '@/types/models'

interface MCPServerCardProps {
  server: MCPServerStatus
  tools: ToolInfo[]
  expanded: boolean
  onToggleExpand: () => void
  onEdit: () => void
  onDelete: () => void
}

export function MCPServerCard({ server, tools, expanded, onToggleExpand, onEdit, onDelete }: MCPServerCardProps) {
  // The "_gateway" sentinel is a synthetic gateway-wide entry produced by
  // GetMCPStatus — NOT a real, user-configured server. It surfaces two
  // transient states and must never reach the editable/deletable Collapsible:
  //   • starting=true  → gateway startup still in flight (neutral spinner)
  //   • error non-empty → gateway failed to start (friendly red error)
  // It is not editable/deletable and carries no transport badge or tools.
  if (server.name === '_gateway') {
    if (server.starting) {
      return (
        <div className="border rounded-lg">
          <div className="flex items-center gap-3 p-3">
            <Loader2 className="h-4 w-4 text-warning animate-spin" />
            <span className="font-medium text-sm flex-1">MCP Gateway</span>
            <span className="text-xs text-muted-foreground">Starting…</span>
          </div>
        </div>
      )
    }
    // Gateway startup failed. Show a friendly header instead of the raw
    // sentinel name and the (empty) transport badge the generic path renders.
    return (
      <div className="border rounded-lg">
        <div className="flex items-start gap-3 p-3">
          <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <div className="flex-1 min-w-0">
            <span className="font-medium text-sm">MCP Gateway</span>
            {server.error && (
              <p className="mt-1 text-xs text-destructive break-all">{server.error}</p>
            )}
          </div>
        </div>
      </div>
    )
  }

  return (
    <Collapsible open={expanded} onOpenChange={onToggleExpand}>
      <div className="border rounded-lg overflow-hidden">
        <CollapsibleTrigger asChild>
          <div className="flex items-center gap-3 p-3 cursor-pointer hover:bg-muted/50 transition-colors">
            {expanded
              ? <ChevronDown className="h-4 w-4 text-muted-foreground" />
              : <ChevronRight className="h-4 w-4 text-muted-foreground" />
            }
            {server.connected
              ? <CheckCircle2 className="h-4 w-4 text-success" />
              : <AlertCircle className="h-4 w-4 text-destructive" />
            }
            <span className="font-medium text-sm flex-1">{server.name}</span>
            <Badge variant="secondary" className="text-xs">{server.transport}</Badge>
            <span className="text-xs text-muted-foreground">{server.tool_count} tools</span>
          </div>
        </CollapsibleTrigger>

        <CollapsibleContent>
          <div className="px-3 pb-3 pt-0 space-y-3 border-t">
            {server.error && (
              <div className="mt-3 flex items-start gap-2 p-2 rounded bg-destructive/10 text-xs">
                <AlertCircle className="h-3 w-3 text-destructive flex-shrink-0 mt-0.5" />
                <code className="text-destructive break-all">{server.error}</code>
              </div>
            )}

            {tools.length > 0 && (
              <div className="mt-3">
                <p className="text-xs text-muted-foreground mb-1">Discovered tools:</p>
                <div className="flex flex-wrap gap-1">
                  {tools.map((tool) => (
                    <Badge key={tool.name} variant="outline" className="text-xs">
                      {tool.name}
                    </Badge>
                  ))}
                </div>
              </div>
            )}

            <div className="flex gap-2 pt-2">
              <Button variant="outline" size="sm" onClick={(e) => { e.stopPropagation(); onEdit() }}>
                <Pencil className="h-3 w-3 mr-1" />
                Edit
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="text-destructive hover:bg-destructive/10"
                onClick={(e) => { e.stopPropagation(); onDelete() }}
              >
                <Trash2 className="h-3 w-3 mr-1" />
                Delete
              </Button>
            </div>
          </div>
        </CollapsibleContent>
      </div>
    </Collapsible>
  )
}
