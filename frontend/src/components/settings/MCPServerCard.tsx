import { ChevronDown, ChevronRight, CheckCircle2, AlertCircle, TriangleAlert, CircleOff, Loader2, Pencil, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Combobox } from '@/components/ui/combobox'
import type { MCPServerConfig, MCPServerStatus, ToolInfo } from '@/types/models'

type ServerMode = NonNullable<MCPServerConfig['mode']>
const MODE_OPTIONS = ['auto', 'manual', 'disabled'].map((value) => ({ value, label: value }))
const MODE_EXPLANATIONS: Record<ServerMode, string> = {
  auto: 'Connects at startup; tools are always available.',
  manual: 'Connects at startup; tools are available for a task only after a /server-name or /mcp: server-name mention.',
  disabled: 'Never connects or starts a process; configuration is kept.',
}

interface MCPServerCardProps {
  server: MCPServerStatus
  tools: ToolInfo[]
  expanded: boolean
  onToggleExpand: () => void
  onEdit: () => void
  onDelete: () => void
  mode: ServerMode
  isSaving: boolean
  onModeChange: (mode: ServerMode) => void
}

export function MCPServerCard({ server, tools, expanded, onToggleExpand, onEdit, onDelete, mode, isSaving, onModeChange }: MCPServerCardProps) {
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
            {server.mode === 'disabled'
              ? <CircleOff className="h-4 w-4 text-muted-foreground" />
              : server.connected
                ? (server.unhealthy
                    ? <TriangleAlert className="h-4 w-4 text-warning" />
                    : <CheckCircle2 className="h-4 w-4 text-success" />)
                : <AlertCircle className="h-4 w-4 text-destructive" />
            }
            <span className="font-medium text-sm flex-1">{server.name}</span>
            {/* Mode badge: only the non-default modes carry one — "auto" is
                the implicit default and would be noise on every card. Manual
                is the mention-driven mode, disabled is inert-by-design (and
                renders neutrally: no red state, see the icon above). */}
            {server.mode === 'manual' && (
              <Badge variant="outline" className="text-xs text-info border-info/50 gap-1">
                manual
              </Badge>
            )}
            {server.mode === 'disabled' && (
              <Badge variant="outline" className="text-xs text-muted-foreground gap-1">
                disabled
              </Badge>
            )}
            {server.unhealthy && (
              <Badge variant="outline" className="text-xs border-warning text-warning gap-1">
                <TriangleAlert className="h-3 w-3" />
                Unhealthy
              </Badge>
            )}
            <Badge variant="secondary" className="text-xs">{server.transport}</Badge>
            <span className="text-xs text-muted-foreground">{server.tool_count} tools</span>
          </div>
        </CollapsibleTrigger>

        <CollapsibleContent>
          <div className="px-3 pb-3 pt-0 space-y-3 border-t">
            {server.unhealthy && (
              <div className="mt-3 flex items-start gap-2 p-2 rounded bg-warning/10 text-xs">
                <TriangleAlert className="h-3 w-3 text-warning flex-shrink-0 mt-0.5" />
                <span className="text-warning">This server is reachable but unhealthy — recent tool calls are failing or timing out. Check its configuration and logs.</span>
              </div>
            )}

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

            <div className="flex flex-wrap items-center gap-2 pt-2">
              <Button variant="outline" size="sm" disabled={isSaving} onClick={(e) => { e.stopPropagation(); onEdit() }}>
                <Pencil className="h-3 w-3 mr-1" />
                Edit
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="text-destructive hover:bg-destructive/10"
                disabled={isSaving}
                onClick={(e) => { e.stopPropagation(); onDelete() }}
              >
                <Trash2 className="h-3 w-3 mr-1" />
                Delete
              </Button>
              <Combobox
                ariaLabel={`${server.name} activation mode`}
                value={mode}
                options={MODE_OPTIONS}
                disabled={isSaving}
                onChange={(value) => onModeChange(value as ServerMode)}
                fitToOptions
                contentClassName="min-w-36"
                className="h-8 px-2 text-xs"
              />
              <span className="text-xs text-muted-foreground flex-1 min-w-[180px]">{MODE_EXPLANATIONS[mode]}</span>
            </div>
          </div>
        </CollapsibleContent>
      </div>
    </Collapsible>
  )
}
