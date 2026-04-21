import { FolderTree, GitBranch, Database } from 'lucide-react'
import { useState } from 'react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { FileTreePanel } from './FileTreePanel'

const WORKSPACE_TABS = [
  { value: 'explorer', label: 'Explorer', icon: FolderTree },
  { value: 'git', label: 'Git', icon: GitBranch },
  { value: 'semantics', label: 'Semantics', icon: Database },
] as const

function TBDPlaceholder() {
  return (
    <div className="flex-1 flex items-center justify-center">
      <span className="text-sm text-muted-foreground">TBD</span>
    </div>
  )
}

export function WorkspacePanel() {
  const [activeTab, setActiveTab] = useState<string>('explorer')
  const activeLabel = WORKSPACE_TABS.find((t) => t.value === activeTab)?.label ?? ''

  return (
    <TooltipProvider>
      <Tabs value={activeTab} onValueChange={setActiveTab} className="h-full flex flex-col">
        <div className="shrink-0 flex items-center gap-2 border-b border-border px-2 h-8">
          <TabsList className="grid grid-cols-3 h-auto rounded-none bg-transparent p-0 gap-0.5">
            {WORKSPACE_TABS.map(({ value, label, icon: Icon }) => (
              <Tooltip key={value}>
                <TooltipTrigger asChild>
                  <TabsTrigger
                    value={value}
                    aria-label={label}
                    className="h-6 w-6 rounded-sm data-[state=active]:border-foreground data-[state=active]:bg-transparent data-[state=active]:shadow-none p-0"
                  >
                    <Icon className="size-3.5" />
                  </TabsTrigger>
                </TooltipTrigger>
                <TooltipContent side="bottom" sideOffset={4}>
                  {label}
                </TooltipContent>
              </Tooltip>
            ))}
          </TabsList>
          <span className="text-xs text-muted-foreground select-none uppercase font-bold">{activeLabel}</span>
        </div>

        <TabsContent value="explorer" className="flex-1 min-h-0 mt-0">
          <FileTreePanel />
        </TabsContent>

        <TabsContent value="git" className="flex-1 min-h-0 mt-0">
          <TBDPlaceholder />
        </TabsContent>

        <TabsContent value="semantics" className="flex-1 min-h-0 mt-0">
          <TBDPlaceholder />
        </TabsContent>

      </Tabs>
    </TooltipProvider>
  )
}
