import { useCallback, useEffect } from 'react'
import { ChevronLeft, ChevronRight, Plus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useTabStore, useTabs, useActiveTabId, type Tab } from '@/stores/tabStore'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { selectSessionNameFor, selectTitleScopeFor } from '@/hooks/useWindowTitle'
import { useSessionStatusIndicator } from '@/hooks/useSessionStatusIndicator'
import { SESSION_DOT_CLASSES, SESSION_DOT_TITLES, sessionNameFromSnapshot } from '@/lib/activeSessions'
import { buildTabLabel } from '@/lib/tabLabel'
import type { TabController } from '@/hooks/useTabController'
import { useTabStripOverflow } from '@/hooks/useTabStripOverflow'
import { cn } from '@/lib/utils'

// The workspace tab bar (rendered above the main row only when
// uiStore.tabsEnabled is on — AppLayout owns that gate). Presentational:
// every interaction is routed through the single activation engine via the
// TabController prop (one useTabController mount lives in AppLayout), and
// every label/status is derived LIVE from the project/session/chat stores by
// id, so renames and lifecycle events repaint without the tab holding a
// stale copy — the same contract useWindowTitle follows. The label builder
// itself lives in lib/tabLabel.ts (pure string algebra), and the status-dot
// maps are the shared SESSION_DOT_* from lib/activeSessions (the same tokens
// and wording the session list and the radar rows use).

export interface TabBarProps {
  /** The app-level controller (AppLayout's single useTabController mount). */
  controller: TabController
}

export function TabBar({ controller }: TabBarProps) {
  const tabs = useTabs()
  const activeTabId = useActiveTabId()
  // Strip scroll affordances, measured off the live DOM by the hook: the
  // chevron pair renders only while the strip overflows, and each flag gates
  // its own button (the authoritative layout description lives in
  // specs/domains/frontend/tabs.md).
  const { scrollerRef, overflowing, canScrollLeft, canScrollRight, scrollNext, scrollPrev, revealTab } =
    useTabStripOverflow({ tabIds: tabs.map((tab) => tab.id), activeTabId })

  // Activation can move the active pointer without a strip click (tab-cycling
  // hotkeys, notification clicks): reveal the newly active tab so it never
  // stays hidden past a clipped edge.
  useEffect(() => {
    revealTab(activeTabId)
  }, [revealTab, activeTabId])

  const handleClose = useCallback(
    (id: string) => {
      const state = useTabStore.getState()
      const index = state.tabs.findIndex((tab) => tab.id === id)
      if (index === -1) return
      if (id !== state.activeTabId || state.tabs.length === 1) {
        // An inactive tab (or the last remaining one): the store transition
        // needs no activation. The last-tab replacement makes a fresh default
        // tab ACTIVE while the live workspace stays where it is — converge it
        // immediately, same reasoning as «+» below.
        useTabStore.getState().closeTab(id)
        if (state.tabs.length === 1) controller.writeBack()
        return
      }
      // Closing the ACTIVE tab: bring the neighbor fully live through the ONE
      // engine path FIRST (capture the outgoing context → flip → project/
      // session move → slice restore), THEN remove the closed tab. Closing
      // first would leave the neighbor active with its unapplied remembered
      // context, and the next write-back would clobber that memory with the
      // closed tab's live context. The neighbor choice mirrors the store's
      // own rule (right neighbor, else the last tab).
      const neighbor = state.tabs[index + 1] ?? state.tabs[index - 1]
      if (neighbor === undefined) return // unreachable: length > 1 here
      // `activate` never rejects: a FAILED activation rolls `activeTabId`
      // back onto `id` (this tab is still active). Closing onto a neighbor
      // whose remembered context never got applied would then let the next
      // write-back clobber that memory — so a failed activation leaves the
      // tab OPEN (the engine's logger.warn is the user-visible signal), and
      // the close only proceeds when the neighbor is genuinely live.
      void controller.activate(neighbor.id).then(() => {
        if (useTabStore.getState().activeTabId === id) return
        useTabStore.getState().closeTab(id)
      })
    },
    [controller],
  )

  const handleAdd = useCallback(() => {
    // Creation is serialized through the engine like every other activation:
    // the [create → activate] pair is ONE chain link shared with the banner
    // click, so an in-flight activation always finishes its restore + write-
    // back on the tab that was active throughout BEFORE the fresh tab exists
    // — it can never resume into a half-created tab and graft its context
    // onto it. The link reads the live context when it runs, so the new tab
    // inherits exactly what the user is looking at.
    void controller.createAndActivate()
  }, [controller])

  // Keyboard interaction on the tab strip (WAI-ARIA APG Tabs, manual
  // activation): ←/→ move FOCUS to the neighbor row (wrapping both ends),
  // Home/End jump to the first/last, Enter/Space activate the focused row —
  // mirroring the system cycling shortcuts' wrap-around. Focus-only arrows
  // keep switching an explicit commitment (Enter/Space/click); the row is a
  // div, so button semantics don't apply natively. Row keys work only when
  // the row ITSELF is focused — the nested close button is a real button
  // and keeps its native behavior.
  const handleTablistKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      const row = (e.target as HTMLElement).closest<HTMLElement>('[role="tab"]')
      if (row === null || e.target !== row) return
      switch (e.key) {
        case 'Enter':
        case ' ':
          e.preventDefault()
          void controller.activate(row.dataset.tabId ?? '')
          return
        case 'ArrowRight':
        case 'ArrowLeft':
        case 'Home':
        case 'End':
          break
        default:
          return
      }
      e.preventDefault()
      const rows = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]'))
      const nextIndex =
        e.key === 'Home'
          ? 0
          : e.key === 'End'
            ? rows.length - 1
            : (rows.indexOf(row) + (e.key === 'ArrowRight' ? 1 : -1) + rows.length) % rows.length
      rows[nextIndex]?.focus()
    },
    [controller],
  )

  // One «+» affordance shared by both layouts — in-flow right after the last
  // tab while the strip fits, pinned right of the right chevron when it
  // overflows — so exactly one button[aria-label="New tab"] exists either way.
  const addButton = (
    <Button
      variant="ghost"
      size="icon-sm"
      className="shrink-0"
      onClick={handleAdd}
      aria-label="New tab"
      title="New tab"
    >
      <Plus className="size-4" />
    </Button>
  )

  return (
    // Zoom-safe by construction: fixed token heights, no viewport units — the
    // bar is a plain flex row shrinking to zero under the h-full app shell.
    <div className="flex h-9 shrink-0 items-center gap-1 border-b border-border bg-background px-2">
      {overflowing && <ScrollChevron direction="left" enabled={canScrollLeft} onScroll={scrollPrev} />}
      {/* Strip scroll/overflow layout — hidden scrollbar, chevron pair + pinned
          «+» on overflow, in-flow «+» while the strip fits — is specified in
          specs/domains/frontend/tabs.md. The buttons stay OUTSIDE the tablist
          itself: only role="tab" rows belong in an ARIA tablist. */}
      <div
        ref={scrollerRef}
        className="no-scrollbar flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
      >
        <div
          role="tablist"
          aria-label="Workspace tabs"
          onKeyDown={handleTablistKeyDown}
          className="flex shrink-0 items-center gap-1"
        >
          {tabs.map((tab) => (
            <TabRow
              key={tab.id}
              tab={tab}
              active={tab.id === activeTabId}
              controller={controller}
              onClose={handleClose}
            />
          ))}
        </div>
        {!overflowing && addButton}
      </div>
      {overflowing && (
        <>
          <ScrollChevron direction="right" enabled={canScrollRight} onScroll={scrollNext} />
          {addButton}
        </>
      )}
    </div>
  )
}

interface ScrollChevronProps {
  /** Which edge the chevron scrolls toward. */
  direction: 'left' | 'right'
  /** False renders the chevron disabled (nothing left to scroll to). */
  enabled: boolean
  /** The strip's scrollPrev/scrollNext from useTabStripOverflow. */
  onScroll: () => void
}

/**
 * One overflow chevron. Radix tooltip, not a native title: the hint must
 * survive the button's disabled state, where a native title does not render
 * (specs/domains/frontend/button-tooltips.md R4).
 */
function ScrollChevron({ direction, enabled, onScroll }: ScrollChevronProps) {
  const label = direction === 'left' ? 'Scroll tabs left' : 'Scroll tabs right'
  const Icon = direction === 'left' ? ChevronLeft : ChevronRight
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="shrink-0"
          onClick={onScroll}
          disabled={!enabled}
          aria-label={label}
        >
          <Icon className="size-4" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

interface TabRowProps {
  tab: Tab
  active: boolean
  controller: TabController
  onClose: (id: string) => void
}

/**
 * One tab. Its own component so the per-tab status hook
 * (useSessionStatusIndicator → deriveSessionStatus — the SAME single
 * derivation every status dot uses) subscribes per session.
 */
function TabRow({ tab, active, controller, onClose }: TabRowProps) {
  // Live title segments by the tab's ids — primitives, so unrelated store
  // updates (activity bumps, ui snapshots) never re-render the row.
  const scope = useProjectStore((s) => selectTitleScopeFor(s, tab.projectId))
  // The per-project store holds ONLY the active project's sessions, so an
  // inactive tab's remembered sessionId is gone from it the moment another
  // tab (different project) is activated — the label degraded to the bare
  // project scope until re-activation. Fall back to the global cross-project
  // snapshot (kept across switches, refreshed on them + ~30s poll); the live
  // store still wins when it has the id, so renames in the ACTIVE project
  // repaint instantly. Both selectors return primitives (React #185 safe).
  const liveName = useSessionStore((s) => selectSessionNameFor(s, tab.sessionId))
  const snapshotName = useActiveSessionsStore((s) =>
    sessionNameFromSnapshot(s.sessions, tab.sessionId),
  )
  const sessionName = liveName ?? snapshotName
  // The SessionInfo backing the dot's DB fallback (may be absent for another
  // project's session — the live chatStore flags still apply).
  const session = useSessionStore((s) =>
    tab.sessionId !== null ? (s.sessions?.find((sess) => sess.id === tab.sessionId) ?? null) : null,
  )
  const status = useSessionStatusIndicator(
    tab.sessionId,
    session?.unfinished_task_status ?? '',
    session?.archived ?? false,
  )

  const label = buildTabLabel(scope, sessionName)
  const dotClass = SESSION_DOT_CLASSES[status]
  const dotTitle = SESSION_DOT_TITLES[status]

  return (
    <div
      role="tab"
      aria-selected={active}
      tabIndex={active ? 0 : -1}
      data-tab-id={tab.id}
      title={label}
      onClick={() => void controller.activate(tab.id)}
      onMouseDown={(e) => {
        // Middle-click closes (browser-tab convention); preventDefault stops
        // the autoscroll latch.
        if (e.button === 1) {
          e.preventDefault()
          onClose(tab.id)
        }
      }}
      className={cn(
        'group/tab flex h-7 min-w-0 max-w-48 shrink-0 cursor-pointer items-center gap-1.5 rounded-md px-2 text-xs transition-colors',
        active
          ? 'bg-muted text-foreground'
          : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground',
      )}
    >
      {dotClass !== undefined && (
        <span className={cn('size-1.5 shrink-0 rounded-full', dotClass)} title={dotTitle} />
      )}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <button
        type="button"
        aria-label={`Close ${label}`}
        title="Close tab"
        onClick={(e) => {
          e.stopPropagation()
          onClose(tab.id)
        }}
        // Keep the row's middle-click handler out of the close button.
        onMouseDown={(e) => e.stopPropagation()}
        className="rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:bg-muted hover:text-foreground group-hover/tab:opacity-100 focus-visible:opacity-100"
      >
        <X className="size-3" />
      </button>
    </div>
  )
}
