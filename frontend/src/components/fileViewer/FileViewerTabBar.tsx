import { useCallback, useRef, useState } from "react";
import { X, ChevronDown, PanelRightClose, PanelRightOpen, Pin, PinOff } from "lucide-react";
import { useFileViewerStore } from "@/stores/fileViewerStore";
import { FileIcon } from "@/components/layout/FileIcon";
import { useFileIcon } from "@/hooks/useFileIcon";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { fileNameFromPath } from "@/lib/fileViewerUtils";
import { FileViewerTabContextMenu } from "./FileViewerTabContextMenu";

interface FileViewerTabBarProps {
  onToggleCollapse: () => void;
  collapsed?: boolean;
}

function TabFileIcon({ path }: { path: string }) {
  const iconData = useFileIcon(path);
  return <FileIcon isDir={false} icon={iconData?.icon} iconColor={iconData?.icon_color} />;
}

export function FileViewerTabBar({ onToggleCollapse, collapsed }: FileViewerTabBarProps) {
  const openTabs = useFileViewerStore((s) => s.openTabs);
  const activeFile = useFileViewerStore((s) => s.activeFile);
  const setActiveFile = useFileViewerStore((s) => s.setActiveFile);
  const closeFile = useFileViewerStore((s) => s.closeFile);
  const pinned = useFileViewerStore((s) => s.pinned);
  const setPinned = useFileViewerStore((s) => s.setPinned);

  const tabsRef = useRef<HTMLDivElement>(null);

  const [contextMenu, setContextMenu] = useState<{ path: string; x: number; y: number } | null>(null);

  const handleTabContextMenu = useCallback((e: React.MouseEvent, path: string) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({ path, x: e.clientX, y: e.clientY });
  }, []);

  const handleCloseContextMenu = useCallback(() => setContextMenu(null), []);

  const handleClose = useCallback(
    (e: React.MouseEvent, path: string) => {
      e.stopPropagation();
      closeFile(path);
    },
    [closeFile],
  );

  const handleWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    const el = tabsRef.current;
    if (!el) return;
    // Only intercept vertical scroll — let native horizontal scroll pass through
    if (e.deltaY === 0) return;
    e.preventDefault();
    el.scrollBy({ left: e.deltaY, behavior: "instant" });
  }, []);

  const scrollToTab = useCallback((path: string) => {
    const el = tabsRef.current?.querySelector<HTMLElement>(`[data-file-path="${CSS.escape(path)}"]`);
    el?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, []);

  const handleTabClick = useCallback(
    (path: string) => {
      setActiveFile(path);
      scrollToTab(path);
    },
    [setActiveFile, scrollToTab],
  );

  if (openTabs.length === 0) return null;

  return (
    <div className="flex flex-col shrink-0 border-b border-border bg-secondary/50 h-10">
      {/* Tab strip + controls */}
      <div className="flex items-end flex-1 min-h-0">
        {/* Scrollable tab strip */}
        <div ref={tabsRef} onWheel={handleWheel} className="flex-1 flex overflow-x-auto no-scrollbar min-w-0">
          {openTabs.map((path) => {
            const name = fileNameFromPath(path);
            const isActive = path === activeFile;
            return (
              <button
                data-file-path={path}
                className={cn(
                  "group flex items-center gap-1 px-2 py-1 h-full text-xs whitespace-nowrap select-none",
                  "border-r border-border transition-colors shrink-0",
                  isActive
                    ? "bg-background text-foreground"
                    : "text-muted-foreground hover:bg-muted/30 hover:text-foreground",
                )}
                onClick={() => handleTabClick(path)}
                onContextMenu={(e) => handleTabContextMenu(e, path)}
              >
                <span className="shrink-0">
                  <TabFileIcon path={path} />
                </span>
                <span className="truncate max-w-30">{name}</span>
                <span
                  role="button"
                  tabIndex={-1}
                  aria-label={`Close ${name}`}
                  className={cn(
                    "ml-0.5 p-0.5 rounded hover:bg-accent/20 transition-colors",
                    isActive ? "opacity-60 hover:opacity-100" : "opacity-0 group-hover:opacity-60",
                  )}
                  onClick={(e) => handleClose(e, path)}
                >
                  <X className="h-3 w-3" />
                </span>
              </button>
            );
          })}
        </div>

        {/* Right-side controls */}
        <div className="flex items-center self-center shrink-0 pr-2">
          {openTabs.length > 1 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="flex items-center justify-center w-6 h-full text-muted-foreground hover:text-foreground hover:bg-muted/30 transition-colors shrink-0"
                  aria-label="View all open files"
                >
                  <ChevronDown className="h-3.5 w-3.5" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-64">
                {openTabs.map((path) => {
                  const name = fileNameFromPath(path);
                  return (
                    <DropdownMenuItem
                      key={path}
                      className="flex items-center gap-2 cursor-pointer"
                      onSelect={() => handleTabClick(path)}
                    >
                      <TabFileIcon path={path} />
                      <span className="truncate text-xs">{name}</span>
                      {path === activeFile && <span className="ml-auto text-[10px] text-muted-foreground">active</span>}
                    </DropdownMenuItem>
                  );
                })}
              </DropdownMenuContent>
            </DropdownMenu>
          )}

          <Button
            variant="ghost"
            size="icon-xs"
            className="shrink-0"
            onClick={() => setPinned(!pinned)}
            title={pinned ? "Unpin file viewer (float over chat)" : "Pin file viewer (dock in place)"}
          >
            {pinned ? <Pin className="size-4" /> : <PinOff className="size-4 text-primary" />}
          </Button>

          <Button
            variant="ghost"
            size="icon-xs"
            className="shrink-0"
            onClick={onToggleCollapse}
            title={collapsed ? "Expand file viewer" : "Collapse file viewer"}
          >
            {collapsed ? <PanelRightOpen className="size-4" /> : <PanelRightClose className="size-4" />}
          </Button>
        </div>
      </div>
      <FileViewerTabContextMenu
        path={contextMenu?.path ?? ""}
        position={contextMenu ? { x: contextMenu.x, y: contextMenu.y } : null}
        onClose={handleCloseContextMenu}
      />
    </div>
  );
}
