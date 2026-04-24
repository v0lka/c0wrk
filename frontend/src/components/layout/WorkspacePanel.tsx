import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from '@/components/ui/tooltip'
import { FileTreePanel } from './FileTreePanel'
import { FolderTree, GitBranch, Brain } from 'lucide-react'

export function WorkspacePanel() {
  return (
    <TooltipProvider>
      <Tabs defaultValue="explorer" className="flex h-full flex-col gap-0">
        <TabsList className="mx-1 h-8 shrink-0" variant="line">
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="explorer" className="px-2"><FolderTree className="size-4" /></TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Explorer</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="git" className="px-2"><GitBranch className="size-4" /></TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Git</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="semantics" className="px-2"><Brain className="size-4" /></TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Semantics</TooltipContent>
          </Tooltip>
        </TabsList>

        <TabsContent value="explorer" className="flex flex-col overflow-hidden">
          <FileTreePanel />
        </TabsContent>

        <TabsContent value="git" className="flex-1 p-4 text-center text-xs text-muted-foreground">
          Git integration coming soon
        </TabsContent>

        <TabsContent value="semantics" className="flex-1 p-4 text-center text-xs text-muted-foreground">
          Semantic search coming soon
        </TabsContent>
      </Tabs>
    </TooltipProvider>
  )
}
