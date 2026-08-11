import { useState, useEffect, useRef } from 'react'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useResize } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { Sidebar } from './Sidebar'
import { ChatArea } from '@/components/chat/ChatArea'
import { StatusBar } from '@/components/layout/StatusBar'
import { FileViewerPanel } from '@/components/fileViewer/FileViewerPanel'

// --- Constants ---

const SIDEBAR_MIN = 180
const SIDEBAR_MAX = 500
const VIEWER_MIN = 250
const VIEWER_MAX = 900
const COLLAPSED_WIDTH = 40

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

function getDefaultSidebarWidth(): number {
  return clamp(Math.round(window.innerWidth / 5), SIDEBAR_MIN, SIDEBAR_MAX)
}

export function AppLayout() {
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUIStore((s) => s.toggleSidebarCollapsed)

  const viewerWidth = useFileViewerStore((s) => s.width)
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed)
  const viewerPinned = useFileViewerStore((s) => s.pinned)
  const setViewerWidth = useFileViewerStore((s) => s.setWidth)
  const setViewerCollapsed = useFileViewerStore((s) => s.setCollapsed)

  const [sidebarWidth, setSidebarWidth] = useState(getDefaultSidebarWidth)

  // Ref to the floating (unpinned) viewer container so a global
  // pointer/focus listener can detect focus moving outside it and collapse it.
  const floatingViewerRef = useRef<HTMLDivElement>(null)

  const sidebarResize = useResize({
    initialWidth: sidebarCollapsed ? COLLAPSED_WIDTH : sidebarWidth,
    min: SIDEBAR_MIN,
    max: SIDEBAR_MAX,
    onChange: setSidebarWidth,
  })

  const viewerResize = useResize({
    initialWidth: viewerWidth,
    min: VIEWER_MIN,
    max: VIEWER_MAX,
    direction: -1,
    onChange: setViewerWidth,
  })

  // In unpinned (floating) mode, collapse the viewer when focus (pointer or
  // keyboard) lands anywhere outside it. Disabled while pinned or already
  // collapsed. This keeps the floating panel from permanently covering the
  // chat on narrow displays — it recedes as soon as the user works elsewhere.
  const floating = !viewerPinned && !viewerCollapsed
  useEffect(() => {
    if (!floating) return
    const node = floatingViewerRef.current
    const handleOutside = (e: Event) => {
      const target = e.target as Node | null
      if (node && target && !node.contains(target)) {
        setViewerCollapsed(true)
      }
    }
    document.addEventListener('pointerdown', handleOutside)
    document.addEventListener('focusin', handleOutside)
    return () => {
      document.removeEventListener('pointerdown', handleOutside)
      document.removeEventListener('focusin', handleOutside)
    }
  }, [floating, setViewerCollapsed])

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-background text-foreground">
      {/* Sidebar */}
      <Sidebar
        width={sidebarCollapsed ? COLLAPSED_WIDTH : sidebarWidth}
        collapsed={sidebarCollapsed}
        onToggleCollapse={toggleSidebar}
      />

      {/* Resize handle between sidebar and main */}
      {!sidebarCollapsed && (
        <ResizeHandle
          onMouseDown={sidebarResize.handleMouseDown}
          onKeyDown={sidebarResize.handleKeyDown}
        />
      )}

      {/* Main content area — relative so the floating viewer can overlay it */}
      <div className="relative flex min-w-0 flex-1 flex-col">
        <ChatArea />
        <StatusBar />

        {/* Floating (unpinned, expanded) viewer: absolute overlay anchored to
            the right edge of the main column. Covers ~3/5 of the central chat
            area by default (the persisted, user-resizable width), stays
            resizable via its left-edge handle, and auto-collapses on outside
            focus. Note: a collapsed floating viewer does NOT render here — it
            is drawn by the docked block below as a slim in-flow bar (so it
            never overlaps the chat), and expanding from that bar returns here. */}
        {!viewerPinned && !viewerCollapsed && (
          <div
            ref={floatingViewerRef}
            className="absolute right-0 top-0 bottom-0 z-20 flex flex-col border-l border-border bg-background shadow-xl"
            style={{ width: viewerWidth }}
          >
            <ResizeHandle
              // Left-edge handle of a right-aligned panel: drag left grows it.
              onMouseDown={viewerResize.handleMouseDown}
              onKeyDown={viewerResize.handleKeyDown}
              className="absolute left-0 top-0 bottom-0 w-1 h-auto"
            />
            <FileViewerPanel />
          </div>
        )}
      </div>

      {/* Docked (in-flow) viewer: rendered whenever pinned OR collapsed.
          When a floating (unpinned) viewer collapses — whether via the tab-bar
          collapse button or the focus-outside auto-collapse — it "artificially"
          docks so a slim 40px expand affordance stays visible (in-flow, so it
          never overlaps the chat) instead of vanishing entirely. Expanding
          restores the floating overlay because the user's `pinned` preference
          (false) is preserved: collapse only changes *rendering*, never intent. */}
      {(viewerPinned || viewerCollapsed) && (
        <>
          {/* Resize handle between main and file viewer */}
          {!viewerCollapsed && (
            <ResizeHandle
              onMouseDown={viewerResize.handleMouseDown}
              onKeyDown={viewerResize.handleKeyDown}
            />
          )}

          {/* File viewer */}
          <div
            className="flex shrink-0 flex-col border-l border-border bg-background"
            style={{ width: viewerCollapsed ? COLLAPSED_WIDTH : viewerWidth }}
          >
            <FileViewerPanel />
          </div>
        </>
      )}
    </div>
  )
}
