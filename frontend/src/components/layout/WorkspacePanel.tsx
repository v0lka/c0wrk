import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip";
import { FileTreePanel } from "./FileTreePanel";
import { VectorStorePanel } from "./VectorStorePanel";
import { GitPanel } from "@/components/GitPanel";
import { useProjectStore } from "@/stores/projectStore";
import { FolderTree, GitBranch, Search } from "lucide-react";

export function WorkspacePanel() {
  const isNoProject = useProjectStore((s) => {
    // When projects is null (loading), treat as not No Project to avoid
    // flashing disabled tabs that then change state once data arrives.
    // The tabs will show as enabled during loading; if the active project
    // turns out to be No Project, they'll disable on the next render —
    // which is a less noticeable transition than enabled → disabled.
    if (s.projects === null) return false;
    const active = s.projects.find((p) => p.id === s.activeProjectId);
    return active?.is_no_project === true;
  });

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
      <Tabs defaultValue="explorer" className="flex h-full flex-col gap-0">
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
