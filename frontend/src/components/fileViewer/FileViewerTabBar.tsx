import { useCallback, useRef } from 'react'
import { X, ChevronDown, PanelRightClose } from 'lucide-react'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { FileIcon } from '@/components/layout/FileIcon'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from '@/components/ui/tooltip'

export function FileViewerTabBar() {
  const openFiles = useFileViewerStore((s) => s.openFiles)
  const activeFilePath = useFileViewerStore((s) => s.activeFilePath)
  const setActiveFile = useFileViewerStore((s) => s.setActiveFile)
  const closeFile = useFileViewerStore((s) => s.closeFile)
  const toggleCollapsed = useFileViewerStore((s) => s.toggleCollapsed)

  const tabsRef = useRef<HTMLDivElement>(null)

  const handleClose = useCallback(
    (e: React.MouseEvent, path: string) => {
      e.stopPropagation()
      closeFile(path)
    },
    [closeFile],
  )

  const handleTabClick = useCallback(
    (path: string) => {
      setActiveFile(path)
    },
    [setActiveFile],
  )

  // Scroll active tab into view
  const scrollToTab = useCallback((path: string) => {
    const el = tabsRef.current?.querySelector<HTMLElement>(`[data-file-path="${CSS.escape(path)}"]`)
    el?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [])

  if (openFiles.length === 0) return null

  return (
    <div className="flex items-end border-b border-border bg-secondary/50 flex-shrink-0 h-10">
      {/* Scrollable tab strip */}
      <div
        ref={tabsRef}
        className="flex-1 flex overflow-x-auto no-scrollbar min-w-0"
      >
        {openFiles.map((file) => {
          const isActive = file.path === activeFilePath
          return (
            <Tooltip key={file.path}>
              <TooltipTrigger asChild>
                <button
                  data-file-path={file.path}
                  className={`
                    group flex items-center gap-1 px-2 py-1 h-full text-xs whitespace-nowrap
                    border-r border-border transition-colors flex-shrink-0
                    ${isActive
                      ? 'bg-background text-foreground'
                      : 'text-muted-foreground hover:bg-muted/30 hover:text-foreground'
                    }
                  `}
                  onClick={() => {
                    handleTabClick(file.path)
                    scrollToTab(file.path)
                  }}
                >
                  <span className="flex-shrink-0">
                    <FileIcon name={file.name} isDir={false} />
                  </span>
                  <span className="truncate max-w-[120px]">{file.name}</span>
                  <span
                    role="button"
                    tabIndex={-1}
                    aria-label={`Close ${file.name}`}
                    className={`
                      ml-0.5 p-0.5 rounded hover:bg-accent/20 transition-colors
                      ${isActive ? 'opacity-60 hover:opacity-100' : 'opacity-0 group-hover:opacity-60 hover:!opacity-100'}
                    `}
                    onClick={(e) => handleClose(e, file.path)}
                  >
                    <X className="h-3 w-3" />
                  </span>
                </button>
              </TooltipTrigger>
              <TooltipContent side="bottom" className="font-mono text-[10px] max-w-xs truncate">
                {file.path}
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>

      {/* Right-side controls: vertically centered */}
      <div className="flex items-center self-center flex-shrink-0">
        {/* Open files dropdown */}
        {openFiles.length > 1 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className="flex items-center justify-center w-6 h-full text-muted-foreground hover:text-foreground hover:bg-muted/30 transition-colors flex-shrink-0"
                aria-label="View all open files"
              >
                <ChevronDown className="h-3.5 w-3.5" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-64">
              {openFiles.map((file) => (
                <DropdownMenuItem
                  key={file.path}
                  className="flex items-center gap-2 cursor-pointer"
                  onSelect={() => {
                    handleTabClick(file.path)
                    scrollToTab(file.path)
                  }}
                >
                  <FileIcon name={file.name} isDir={false} />
                  <span className="truncate text-xs">{file.name}</span>
                  {file.path === activeFilePath && (
                    <span className="ml-auto text-[10px] text-muted-foreground">active</span>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}

        {/* Collapse inspector button */}
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 flex-shrink-0"
          onClick={toggleCollapsed}
          title="Collapse inspector"
        >
          <PanelRightClose className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  )
}
