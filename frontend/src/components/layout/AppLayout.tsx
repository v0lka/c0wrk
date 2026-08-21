import { useEffect, useRef } from 'react'
import { useUIStore, SIDEBAR_MIN, SIDEBAR_MAX } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useResize } from '@/hooks/useResize'
import { ResizeHandle } from '@/components/ResizeHandle'
import { Sidebar } from './Sidebar'
import { ChatArea } from '@/components/chat/ChatArea'
import { StatusBar } from '@/components/layout/StatusBar'
import { FileViewerPanel } from '@/components/fileViewer/FileViewerPanel'
import { getApp, isWailsReady, subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'

// --- Constants ---

const VIEWER_MIN = 250
const VIEWER_MAX = 900
const COLLAPSED_WIDTH = 40
// Debounce for window-resize → persist: avoids a disk write per resize frame;
// the final size is captured shortly after dragging stops.
const WINDOW_PERSIST_DEBOUNCE_MS = 400

/**
 * Resolve the control that opened a Radix popper portal (dropdown menu, select,
 * popover, …). Radix renders such popovers in a portal at `document.body`, so a
 * plain `node.contains(target)` check can't tell that the popover logically
 * belongs to a control inside the floating viewer — which would collapse the
 * viewer the moment one of its popovers opens (e.g. the review/file hunk
 * comboboxes). The portal wrapper holds the content element; Radix pairs that
 * content with its trigger via `id` / `aria-controls`, so we walk back to the
 * trigger and re-test containment against the viewer.
 */
function resolveRadixPortalTrigger(target: Node | null): Element | null {
  const element = target instanceof Element ? target : (target?.parentElement ?? null)
  const wrapper = element?.closest('[data-radix-popper-content-wrapper]')
  if (!wrapper) return null
  const content = wrapper.firstElementChild
  if (!(content instanceof HTMLElement) || !content.id) return null
  return document.querySelector(`[aria-controls="${content.id}"]`)
}

/** Whether a node is the document root (`<body>`/`<html>`). */
function isDocumentRoot(node: Node): boolean {
  return node === document.body || node === document.documentElement
}

/** Extract viewport client coordinates from a pointer/mouse event, if present. */
function getClientPoint(event: Event): { x: number; y: number } | null {
  const e = event as { clientX?: number; clientY?: number }
  if (typeof e.clientX === 'number' && typeof e.clientY === 'number') {
    return { x: e.clientX, y: e.clientY }
  }
  return null
}

/**
 * Whether an open Radix popper (menu/select/popover) whose trigger lives inside
 * the viewer is currently mounted. While one is open, Radix's modal layer sets
 * `pointer-events: none` on `<body>` and, on dismissal, briefly moves focus to
 * the document root before restoring it to the trigger — both surface as events
 * whose target is `<body>`/`<html>`, which must not collapse the viewer.
 */
function hasOpenRadixPopupInside(viewer: HTMLElement): boolean {
  const wrappers = document.querySelectorAll('[data-radix-popper-content-wrapper]')
  for (const wrapper of wrappers) {
    const content = wrapper.firstElementChild
    if (!(content instanceof HTMLElement) || !content.id) continue
    if (content.getAttribute('data-state') !== 'open') continue
    const trigger = document.querySelector(`[aria-controls="${content.id}"]`)
    if (trigger && viewer.contains(trigger)) return true
  }
  return false
}

/**
 * Whether a pointer/focus event is considered "inside" the floating viewer:
 *  - directly in its DOM subtree, or
 *  - inside a Radix portal whose trigger (the `aria-controls` owner) is inside
 *    the viewer, or
 *  - a `pointerdown` whose coordinates land over the viewer — Radix modal
 *    popovers redirect the target to the document root by disabling pointer
 *    events on `<body>`, so the *target* is unreliable while the *point* is
 *    still over the viewer (this is what makes a re-click on a hunk combobox
 *    collapse the panel), or
 *  - a `focusin` on the document root while a viewer-anchored popup is open
 *    (Radix restores focus to the trigger a tick later; the root focus is a
 *    transient artefact of closing the popup).
 */
function isInsideViewer(target: Node, viewer: HTMLElement, event?: Event): boolean {
  if (viewer.contains(target)) return true

  const trigger = resolveRadixPortalTrigger(target)
  if (trigger !== null && viewer.contains(trigger)) return true

  if (event?.type === 'pointerdown') {
    const point = getClientPoint(event)
    if (point) {
      const rect = viewer.getBoundingClientRect()
      if (
        point.x >= rect.left &&
        point.x <= rect.right &&
        point.y >= rect.top &&
        point.y <= rect.bottom
      ) {
        return true
      }
    }
  }

  if (event?.type === 'focusin' && isDocumentRoot(target) && hasOpenRadixPopupInside(viewer)) {
    return true
  }

  return false
}

export function AppLayout() {
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUIStore((s) => s.toggleSidebarCollapsed)
  const sidebarWidth = useUIStore((s) => s.sidebarWidth)
  const setSidebarWidth = useUIStore((s) => s.setSidebarWidth)

  const viewerWidth = useFileViewerStore((s) => s.width)
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed)
  const viewerPinned = useFileViewerStore((s) => s.pinned)
  const setViewerWidth = useFileViewerStore((s) => s.setWidth)
  const setViewerCollapsed = useFileViewerStore((s) => s.setCollapsed)

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

  // Persist the OS window geometry (size + maximize) across restarts. The
  // resize listener is debounced so we don't hit disk on every drag frame; the
  // Go backend (PersistWindowBounds) reads the live window size from the Wails
  // runtime and writes window_state.json atomically. A final save also runs on
  // app shutdown from the Go side. If the Wails bridge isn't ready yet on
  // mount, we defer attaching until backend:ready fires rather than silently
  // dropping the listener.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    let detachResize: (() => void) | undefined
    let unsubReady: (() => void) | undefined

    const persist = () => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => {
        getApp()
          .PersistWindowBounds()
          .catch((err: unknown) => logger.warn('PersistWindowBounds RPC failed', err))
      }, WINDOW_PERSIST_DEBOUNCE_MS)
    }

    const attach = () => {
      // Capture the initial geometry once.
      persist()
      window.addEventListener('resize', persist)
      detachResize = () => window.removeEventListener('resize', persist)
    }

    if (isWailsReady()) {
      attach()
    } else if (typeof window !== 'undefined' && window.runtime) {
      // Wails bridge present but not fully ready — attach on backend:ready.
      unsubReady = subscribe('backend:ready', () => {
        unsubReady?.()
        attach()
      })
    }
    // else: pure browser dev session — no Wails bridge, nothing to persist.

    return () => {
      unsubReady?.()
      detachResize?.()
      if (timer) clearTimeout(timer)
    }
  }, [])

  // In unpinned (floating) mode, collapse the viewer when focus (pointer or
  // keyboard) lands anywhere outside it. Disabled while pinned or already
  // collapsed. This keeps the floating panel from permanently covering the
  // chat on narrow displays — it recedes as soon as the user works elsewhere.
  // Radix popovers opened from inside the viewer render in a portal outside its
  // DOM subtree; `isInsideViewer` accounts for them so interacting with their
  // content (e.g. picking a hunk) — and re-clicking the trigger to close them —
  // doesn't collapse the panel.
  const floating = !viewerPinned && !viewerCollapsed
  useEffect(() => {
    if (!floating) return
    const node = floatingViewerRef.current
    const handleOutside = (e: Event) => {
      const target = e.target as Node | null
      if (node && target && !isInsideViewer(target, node, e)) {
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
          collapse button, the focus-outside auto-collapse, or the empty-tabs
          auto-collapse — it "artificially" docks so a slim 40px expand
          affordance stays visible (in-flow, so it never overlaps the chat)
          instead of vanishing entirely. Expanding restores the floating overlay
          because the user's `pinned` preference (false) is preserved: collapse
          only changes *rendering*, never intent. */}
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
