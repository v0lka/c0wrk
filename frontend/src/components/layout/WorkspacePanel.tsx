import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip";
import { FileTreePanel } from "./FileTreePanel";
import { VectorStorePanel } from "./VectorStorePanel";
import { GitPanel } from "@/components/GitPanel";
import { useProjectStore, selectIsNoProject } from "@/stores/projectStore";
import { useUIStore } from "@/stores/uiStore";
import { FolderTree, GitBranch, Search } from "lucide-react";

export function WorkspacePanel() {
  const isNoProject = useProjectStore(selectIsNoProject);
  const workspaceTab = useUIStore((s) => s.workspaceTab);
  const setWorkspaceTab = useUIStore((s) => s.setWorkspaceTab);

  // In CHAT (No Project) mode, hide the tab strip entirely — only show the file
  // explorer with file-name search. Git and Semantics are unavailable anyway.
  if (isNoProject) {
    return (
      <TooltipProvider>
        <div className="flex h-full flex-col overflow-hidden">
          <FileTreePanel />
        </div>
      </TooltipProvider>
    );
  }

  return (
    <TooltipProvider>
      <Tabs value={workspaceTab} onValueChange={(v) => setWorkspaceTab(v as typeof workspaceTab)} className="flex h-full flex-col gap-0">
        <TabsList className="mx-1 h-8 shrink-0" variant="line">
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="explorer" className="px-2">
                <FolderTree className="size-4" />
              </TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Explorer</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="git" className="px-2">
                <GitBranch className="size-4" />
              </TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Git</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <TabsTrigger value="semantics" className="px-2">
                <Search className="size-4" />
              </TabsTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">Search</TooltipContent>
          </Tooltip>
        </TabsList>

        <TabsContent value="explorer" className="flex flex-col overflow-hidden">
          <FileTreePanel />
        </TabsContent>

        <TabsContent value="git" className="flex-1 overflow-hidden">
          <GitPanel />
        </TabsContent>

        <TabsContent value="semantics" className="flex flex-col overflow-hidden">
          <VectorStorePanel />
        </TabsContent>
      </Tabs>
    </TooltipProvider>
  );
}
