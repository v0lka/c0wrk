import { FolderTree, GitBranch, Database, ClipboardList } from 'lucide-react'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { FileTreePanel } from './FileTreePanel'

const WORKSPACE_TABS = [
  { value: 'explorer', label: 'Workspace Explorer', icon: FolderTree },
  { value: 'git', label: 'Git Repository', icon: GitBranch },
  { value: 'vectors', label: 'Vector Storage', icon: Database },
  { value: 'blackboard', label: 'Blackboard', icon: ClipboardList },
] as const

function TBDPlaceholder() {
  return (
    <div className="flex-1 flex items-center justify-center">
      <span className="text-sm text-muted-foreground">TBD</span>
    </div>
  )
}

export function WorkspacePanel() {
  return (
    <TooltipProvider>
      <Tabs defaultValue="explorer" className="h-full flex flex-col">
        <TabsList className="w-full shrink-0 grid grid-cols-4 h-8 rounded-none border-b border-border bg-transparent p-0">
          {WORKSPACE_TABS.map(({ value, label, icon: Icon }) => (
            <Tooltip key={value}>
              <TooltipTrigger asChild>
                <TabsTrigger
                  value={value}
                  aria-label={label}
                  className="h-full rounded-none border-b-2 border-transparent data-[state=active]:border-foreground data-[state=active]:bg-transparent data-[state=active]:shadow-none"
                >
                  <Icon className="size-4" />
                </TabsTrigger>
              </TooltipTrigger>
              <TooltipContent side="bottom" sideOffset={4}>
                {label}
              </TooltipContent>
            </Tooltip>
          ))}
        </TabsList>

        <TabsContent value="explorer" className="flex-1 min-h-0 mt-0">
          <FileTreePanel />
        </TabsContent>

        <TabsContent value="git" className="flex-1 min-h-0 mt-0">
          <TBDPlaceholder />
        </TabsContent>

        <TabsContent value="vectors" className="flex-1 min-h-0 mt-0">
          <TBDPlaceholder />
        </TabsContent>

        <TabsContent value="blackboard" className="flex-1 min-h-0 mt-0">
          <TBDPlaceholder />
        </TabsContent>
      </Tabs>
    </TooltipProvider>
  )
}
