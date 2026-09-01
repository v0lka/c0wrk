// Radar button for the sidebar header: a global, cross-project indicator of
// live sessions. The button carries an overlapping dot cluster
// (ActiveSessionsBadge) anchored to its bottom-right corner summarizing WHAT
// is live across all projects:
//
//   red (failed) → yellow (awaiting you) → green (running) → gray (paused)
//
// It is disabled (and dotless) when no session anywhere is live. Clicking it
// opens the sessions dropdown: every live session of every project as a
// navigation surface (see ActiveSessionRow) — jump straight to a running or
// pending task, switching project when needed. No management actions here.
//
// Data wiring (per lib/activeSessions): DB snapshot + pending override come
// from activeSessionsStore, live execution flags from chatStore, folded by
// pure helpers inside useMemo (never inside selectors — React #185). Refresh
// triggers (mount + live set transitions) come from useActiveSessionsRefresh;
// opening the dropdown additionally calls refreshNow() + sweepPendingActions()
// for immediate freshness plus the pending-HITL sweep.

import { useMemo, useState } from 'react'
import { Radar } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import {
  deriveBadgeFlags,
  deriveLiveSessionFlags,
  mergePendingOverride,
  sortedLiveRows,
  type LiveChatSnapshot,
} from '@/lib/activeSessions'
import { sweepPendingActions, useActiveSessionsRefresh, useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useChatStore } from '@/stores/chatStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { ActiveSessionRow } from './ActiveSessionRow'
import { ActiveSessionsBadge } from './ActiveSessionsBadge'

/**
 * Layout-space guard. The sidebar clamps to 180px (uiStore.SIDEBAR_MIN) and
 * the CHAT/CODE toggle already fills nearly all of that row — a permanently
 * visible extra icon button would push Settings out of the header at the
 * minimum width. SidebarHeader marks its row as a size container
 * (`@container`): the button leaves layout below 228px and renders
 * inline-flex from 228px (native Tailwind v4 container queries), leaving
 * slack over the worst-case row (≈214px). Below 228px the header renders
 * exactly as it did before this component existed.
 */
const LAYOUT_FIT_CLASSES = 'hidden @min-[228px]:inline-flex'

/** Global live-sessions indicator: Radar icon button + badge, store-wired.
 *  Click opens the live-sessions dropdown. */
export function ActiveSessionsIndicator() {
  useActiveSessionsRefresh()

  // Direct store-field selectors only (React #185); the snapshot object and
  // the aggregations allocate inside useMemo, never inside a selector.
  const sessions = useActiveSessionsStore((s) => s.sessions)
  const pendingOverride = useActiveSessionsStore((s) => s.pendingOverride)
  const taskActive = useChatStore((s) => s.taskActive)
  const paused = useChatStore((s) => s.paused)
  const messageOrder = useChatStore((s) => s.messageOrder)
  const messages = useChatStore((s) => s.messages)
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const selectSession = useSessionStore((s) => s.selectSession)
  const switchProjectWithState = useProjectSwitchState()

  const [open, setOpen] = useState(false)

  const chatSnapshot: LiveChatSnapshot = useMemo(
    () => ({ taskActive, paused, messageOrder, messages }),
    [taskActive, paused, messageOrder, messages],
  )
  const flags = useMemo(
    () => deriveBadgeFlags(sessions, chatSnapshot, pendingOverride),
    [sessions, chatSnapshot, pendingOverride],
  )
  const liveFlags = useMemo(
    () => mergePendingOverride(deriveLiveSessionFlags(chatSnapshot), pendingOverride),
    [chatSnapshot, pendingOverride],
  )
  const rows = useMemo(() => sortedLiveRows(sessions, liveFlags), [sessions, liveFlags])

  // CHAT (No Project) rows are the sessions owned by an is_no_project project.
  const chatProjectIds = useMemo(() => {
    const ids = new Set<string>()
    for (const project of projects ?? []) {
      if (project.is_no_project) ids.add(project.id)
    }
    return ids
  }, [projects])

  // project_id → project name, for the per-row project label (CODE sessions
  // only; the row suppresses it for CHAT via isChat).
  const projectNameById = useMemo(() => {
    const map = new Map<string, string>()
    for (const project of projects ?? []) {
      map.set(project.id, project.name)
    }
    return map
  }, [projects])

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen)
    if (!nextOpen) return
    void useActiveSessionsStore.getState().refreshNow()
    void sweepPendingActions()
  }

  /** Jump to a live session: switch project first when it lives in another
   *  one (switchProjectWithState restores that project's UI state), then
   *  select the session. The dropdown closes only on success — a failed
   *  switch (toast surfaced globally) leaves the list open to retry. */
  const handleSelect = async (session: { id: string; project_id: string }) => {
    try {
      if (session.project_id !== activeProjectId) {
        await switchProjectWithState(session.project_id)
      }
      selectSession(session.id, session.project_id)
    } catch {
      return
    }
    setOpen(false)
  }

  return (
    <DropdownMenu open={open} onOpenChange={handleOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={!flags.anyLive}
          aria-label="Active sessions"
          title="Active sessions"
          className={cn('relative', LAYOUT_FIT_CLASSES)}
        >
          <Radar className="size-4" />
          <ActiveSessionsBadge flags={flags} />
        </Button>
      </DropdownMenuTrigger>
      {/* Dynamic width: the list grows with its content (project label + title)
          from the previous fixed w-80 up to 4/3 of it (20rem × 4/3 ≈ 26.67rem),
          then the title truncates. max-h + overflow-y-auto + custom-scrollbar
          come from the shared DropdownMenuContent base classes (Radix-aware
          available height); a plain list is fine — a handful of live sessions
          needs no virtualization. */}
      <DropdownMenuContent align="start" className="min-w-80 max-w-[26.67rem]">
        {rows.length === 0 ? (
          <div className="px-2 py-1.5 text-sm text-muted-foreground">No active sessions</div>
        ) : (
          rows.map(({ session, status }) => (
            <DropdownMenuItem
              key={session.id}
              onSelect={(event) => {
                // Keep the menu open until navigation settles (see
                // handleSelect).
                event.preventDefault()
                void handleSelect(session)
              }}
            >
              <ActiveSessionRow
                session={session}
                status={status}
                isActive={session.id === activeSessionId}
                isChat={chatProjectIds.has(session.project_id)}
                projectName={projectNameById.get(session.project_id)}
              />
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
