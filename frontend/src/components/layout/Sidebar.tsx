import React, { useEffect, useState, useCallback, useMemo, useRef } from 'react'
import {
  Plus, Settings, MoreVertical, Archive, Trash2, Edit3,
  ChevronDown, FolderKanban, Check,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ScrollArea, ScrollBar } from '@/components/ui/scroll-area'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionAPI } from '@/hooks/useSession'
import { useProjectAPI } from '@/hooks/useProject'
import { SettingsModal } from '@/components/settings/SettingsModal'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import { FileTreePanel } from './FileTreePanel'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import type { SessionInfo } from '@/lib/wails'

// ── Helpers ──────────────────────────────────────────────────────────

function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMs / 3600000)
  const diffDays = Math.floor(diffMs / 86400000)

  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  if (diffHours < 24) return `${diffHours}h ago`
  if (diffDays < 7) return `${diffDays}d ago`
  return date.toLocaleDateString()
}

// ── Vertical resize hook (for session/file-tree divider) ─────────

const SESSIONS_MIN_HEIGHT = 150
const FILETREE_MIN_HEIGHT = 100

function useVerticalResize(
  containerRef: React.RefObject<HTMLDivElement | null>,
  defaultRatio: number = 0.6
) {
  const [sessionsRatio, setSessionsRatio] = useState(defaultRatio)
  const dragging = useRef(false)
  const startY = useRef(0)
  const startRatio = useRef(0)
  const moveRef = useRef<((e: MouseEvent) => void) | null>(null)
  const upRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    return () => {
      if (moveRef.current) {
        document.removeEventListener('mousemove', moveRef.current)
        moveRef.current = null
      }
      if (upRef.current) {
        document.removeEventListener('mouseup', upRef.current)
        upRef.current = null
      }
      dragging.current = false
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
  }, [])

  const onMouseDown = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault()
      const container = containerRef.current
      if (!container) return

      dragging.current = true
      startY.current = e.clientY
      startRatio.current = sessionsRatio

      const containerHeight = container.getBoundingClientRect().height
      const dividerHeight = 6 // approx

      const onMouseMove = (ev: MouseEvent) => {
        if (!dragging.current) return
        const delta = ev.clientY - startY.current
        const available = containerHeight - dividerHeight
        const newRatio = startRatio.current + delta / available

        // Enforce min heights
        const minSessionsRatio = SESSIONS_MIN_HEIGHT / available
        const maxSessionsRatio = 1 - FILETREE_MIN_HEIGHT / available
        setSessionsRatio(Math.max(minSessionsRatio, Math.min(maxSessionsRatio, newRatio)))
      }

      const onMouseUp = () => {
        dragging.current = false
        document.removeEventListener('mousemove', onMouseMove)
        document.removeEventListener('mouseup', onMouseUp)
        document.body.style.cursor = ''
        document.body.style.userSelect = ''
        moveRef.current = null
        upRef.current = null
      }

      moveRef.current = onMouseMove
      upRef.current = onMouseUp
      document.addEventListener('mousemove', onMouseMove)
      document.addEventListener('mouseup', onMouseUp)
      document.body.style.cursor = 'row-resize'
      document.body.style.userSelect = 'none'
    },
    [containerRef, sessionsRatio]
  )

  return { sessionsRatio, onMouseDown }
}

// ── SessionItem ──────────────────────────────────────────────────

interface SessionItemProps {
  session: SessionInfo
  isActive: boolean
  onSelect: () => void
  onRename: (name: string) => void
  onArchive: () => void
  onDelete: () => void
}

const SessionItem = React.memo(function SessionItem({
  session, isActive, onSelect, onRename, onArchive, onDelete,
}: SessionItemProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editName, setEditName] = useState(session.name)

  useEffect(() => {
    if (!isEditing) setEditName(session.name)
  }, [session.name, isEditing])

  const handleRename = () => {
    if (editName.trim() && editName !== session.name) onRename(editName.trim())
    setIsEditing(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') handleRename()
    else if (e.key === 'Escape') { setEditName(session.name); setIsEditing(false) }
  }

  return (
    <div
      className={cn(
        'group flex items-center gap-2 px-3 py-2 rounded-md cursor-pointer transition-colors duration-150',
        isActive ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50 text-foreground'
      )}
      onClick={onSelect}
    >
      <div className="flex-1 min-w-0">
        {isEditing ? (
          <Input
            value={editName}
            onChange={(e) => setEditName(e.target.value)}
            onBlur={handleRename}
            onKeyDown={handleKeyDown}
            onClick={(e) => e.stopPropagation()}
            autoFocus
            className="h-6 text-sm py-0"
          />
        ) : (
          <>
            <div className="text-sm font-medium truncate">{session.name}</div>
            <div className="text-xs text-muted-foreground">
              {formatRelativeTime(session.last_active_at || session.created_at)}
            </div>
          </>
        )}
      </div>

      {session.active && (
        <div className="w-2 h-2 rounded-full bg-green-500 flex-shrink-0" title="Active" />
      )}

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6 opacity-0 group-hover:opacity-100 transition-opacity"
            onClick={(e: React.MouseEvent) => e.stopPropagation()}
            aria-label="Session options"
          >
            <MoreVertical className="h-3 w-3" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-40">
          <DropdownMenuItem onClick={() => { setEditName(session.name); setIsEditing(true) }}>
            <Edit3 className="h-4 w-4 mr-2" />
            Rename
          </DropdownMenuItem>
          <DropdownMenuItem onClick={onArchive}>
            <Archive className="h-4 w-4 mr-2" />
            {session.archived ? 'Unarchive' : 'Archive'}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={onDelete} className="text-destructive focus:text-destructive">
            <Trash2 className="h-4 w-4 mr-2" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
})

// ── Sidebar ──────────────────────────────────────────────────────

export function Sidebar() {
  // ── Project state ──
  const projects = useProjectStore(s => s.projects)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const setProjects = useProjectStore(s => s.setProjects)
  const setActiveProject = useProjectStore(s => s.setActiveProject)
  const addProject = useProjectStore(s => s.addProject)
  const removeProject = useProjectStore(s => s.removeProject)
  const updateProject = useProjectStore(s => s.updateProject)
  const projectAPI = useProjectAPI()

  // ── Session state ──
  const sessions = useSessionStore(s => s.sessions)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const setSessions = useSessionStore(s => s.setSessions)
  const addSession = useSessionStore(s => s.addSession)
  const removeSession = useSessionStore(s => s.removeSession)
  const setActiveSession = useSessionStore(s => s.setActiveSession)
  const updateSession = useSessionStore(s => s.updateSession)
  const sessionAPI = useSessionAPI()

  // ── Local UI state ──
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [createProjectOpen, setCreateProjectOpen] = useState(false)
  const [renamingProjectId, setRenamingProjectId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')

  // ── Vertical resize for sessions / file tree ──
  const bodyRef = useRef<HTMLDivElement>(null)
  const { sessionsRatio, onMouseDown: onDividerMouseDown } = useVerticalResize(bodyRef)

  const activeProject = useMemo(
    () => projects.find(p => p.id === activeProjectId) ?? null,
    [projects, activeProjectId]
  )

  // ── Load projects on mount ──
  useEffect(() => {
    const load = async () => {
      try {
        const list = await projectAPI.listProjects()
        if (list && list.length > 0) {
          setProjects(list)
          // Auto-select the most recent project
          const first = list[0]
          if (first) {
            setActiveProject(first.id)
            await projectAPI.switchProject(first.id)
          }
        }
      } catch (err) {
        logger.error('Failed to load projects:', err)
      }
    }
    load()
  // Intentionally omit projectAPI/setProjects/setActiveProject — they are stable store selectors/API hooks;
  // we only want to load projects once on initial mount
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ── Load sessions when active project changes ──
  const sessionFetchIdRef = useRef(0)
  useEffect(() => {
    if (!activeProjectId) {
      setSessions([])
      setActiveSession(null)
      return
    }
    const myId = ++sessionFetchIdRef.current
    const load = async () => {
      try {
        const list = await sessionAPI.listSessions()
        if (myId !== sessionFetchIdRef.current) return // stale response — project changed again
        if (list) {
          setSessions(list)
          if (list.length > 0) setActiveSession(list[0]!.id)
          else setActiveSession(null)
        }
      } catch (err) {
        if (myId !== sessionFetchIdRef.current) return
        logger.error('Failed to load sessions:', err)
      }
    }
    load()
  // Intentionally omit sessionAPI/setSessions/setActiveSession — they are stable store selectors/API hooks;
  // we only want to re-fetch sessions when the active project changes
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId])

  // ── Subscribe to backend project events ──
  useEffect(() => {
    if (!window?.runtime) return
    const unsubs = [
      window.runtime.EventsOn('project:created', (data: unknown) => {
        const p = data as import('@/lib/wails').ProjectInfo
        addProject(p)
      }),
      window.runtime.EventsOn('project:deleted', (data: unknown) => {
        // Backend emits a bare string ID
        const id = data as string
        removeProject(id)
      }),
      window.runtime.EventsOn('project:renamed', (data: unknown) => {
        const d = data as { id: string; name: string }
        updateProject(d.id, { name: d.name })
      }),
      window.runtime.EventsOn('project:switched', (data: unknown) => {
        // Backend emits a full ProjectInfo object
        const p = data as import('@/lib/wails').ProjectInfo
        setActiveProject(p.id)
      }),
    ]
    return () => { unsubs.forEach(fn => fn()) }
  }, [addProject, removeProject, updateProject, setActiveProject])

  // ── Project actions ──
  const handleSwitchProject = useCallback(async (id: string) => {
    if (id === activeProjectId) return
    try {
      setActiveProject(id)
      await projectAPI.switchProject(id)
    } catch (err) {
      logger.error('Failed to switch project:', err)
    }
  }, [activeProjectId, setActiveProject, projectAPI])

  const handleDeleteProject = useCallback(async (id: string) => {
    try {
      await projectAPI.deleteProject(id)
      removeProject(id)
      // If we just deleted the active project, pick the next one
      const remaining = useProjectStore.getState().projects
      if (remaining.length > 0) {
        handleSwitchProject(remaining[0]!.id)
      }
    } catch (err) {
      logger.error('Failed to delete project:', err)
    }
  }, [projectAPI, removeProject, handleSwitchProject])

  const handleStartRename = useCallback((id: string, currentName: string) => {
    setRenamingProjectId(id)
    setRenameValue(currentName)
  }, [])

  const handleFinishRename = useCallback(async () => {
    if (!renamingProjectId) return
    const trimmed = renameValue.trim()
    if (trimmed) {
      try {
        await projectAPI.renameProject(renamingProjectId, trimmed)
        updateProject(renamingProjectId, { name: trimmed })
      } catch (err) {
        logger.error('Failed to rename project:', err)
      }
    }
    setRenamingProjectId(null)
  }, [renamingProjectId, renameValue, projectAPI, updateProject])

  // ── Session actions (unchanged logic) ──
  const handleCreateSession = useCallback(async () => {
    try {
      const session = await sessionAPI.createSession()
      if (session) { addSession(session); setActiveSession(session.id) }
    } catch (err) { logger.error('Failed to create session:', err) }
  }, [sessionAPI, addSession, setActiveSession])

  const handleRename = useCallback(async (id: string, name: string) => {
    try { await sessionAPI.renameSession(id, name); updateSession(id, { name }) }
    catch (err) { logger.error('Failed to rename session:', err) }
  }, [sessionAPI, updateSession])

  const handleArchive = useCallback(async (id: string, currentArchived: boolean) => {
    try { await sessionAPI.archiveSession(id); updateSession(id, { archived: !currentArchived }) }
    catch (err) { logger.error('Failed to archive session:', err) }
  }, [sessionAPI, updateSession])

  const handleDeleteSession = useCallback(async (id: string) => {
    try { await sessionAPI.deleteSession(id); removeSession(id) }
    catch (err) { logger.error('Failed to delete session:', err) }
  }, [sessionAPI, removeSession])

  const [activeSessions, archivedSessions] = useMemo(() => [
    sessions.filter(s => !s.archived),
    sessions.filter(s => s.archived),
  ], [sessions])

  const hasProject = projects.length > 0 && activeProjectId

  // ── Render ──

  return (
    <div className="h-full flex flex-col bg-card">
      {/* ═══ Header: Project selector + actions ═══ */}
      <div className="flex items-center gap-1 p-2 border-b border-border flex-shrink-0">
        {hasProject ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="flex-1 justify-between min-w-0 gap-1 px-2">
                <span className="truncate text-sm font-medium">
                  {activeProject?.name ?? 'Select project'}
                </span>
                <ChevronDown className="h-3.5 w-3.5 flex-shrink-0 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-56">
              {projects.map(p => (
                <DropdownMenuItem key={p.id} className="justify-between" onClick={() => handleSwitchProject(p.id)}>
                  <span className="truncate">{p.name}</span>
                  <div className="flex items-center gap-1 flex-shrink-0 ml-2">
                    {p.id === activeProjectId && <Check className="h-3.5 w-3.5" />}
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          className="p-0.5 rounded hover:bg-accent"
                          onClick={(e) => e.stopPropagation()}
                          aria-label="Project actions"
                        >
                          <MoreVertical className="h-3 w-3" />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-36">
                        <DropdownMenuItem onClick={(e) => { e.stopPropagation(); handleStartRename(p.id, p.name) }}>
                          <Edit3 className="h-4 w-4 mr-2" />
                          Rename
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          className="text-destructive focus:text-destructive"
                          onClick={(e) => { e.stopPropagation(); handleDeleteProject(p.id) }}
                        >
                          <Trash2 className="h-4 w-4 mr-2" />
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </DropdownMenuItem>
              ))}
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => setCreateProjectOpen(true)}>
                <Plus className="h-4 w-4 mr-2" />
                New Project...
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="flex-1 justify-start gap-2 px-2 text-muted-foreground"
            onClick={() => setCreateProjectOpen(true)}
          >
            <FolderKanban className="h-4 w-4" />
            Create Project
          </Button>
        )}

        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 flex-shrink-0"
          onClick={() => setCreateProjectOpen(true)}
          title="New project"
        >
          <Plus className="h-3.5 w-3.5" />
        </Button>

        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 flex-shrink-0"
          onClick={() => setSettingsOpen(true)}
          title="Settings"
        >
          <Settings className="h-3.5 w-3.5" />
        </Button>
      </div>

      {/* ═══ Inline project rename ═══ */}
      {renamingProjectId && (
        <div className="px-2 py-1.5 border-b border-border flex-shrink-0">
          <Input
            value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)}
            onBlur={handleFinishRename}
            onKeyDown={(e) => {
              if (e.key === 'Enter') handleFinishRename()
              else if (e.key === 'Escape') setRenamingProjectId(null)
            }}
            autoFocus
            className="h-7 text-sm"
            placeholder="Project name"
          />
        </div>
      )}

      {/* ═══ Body: sessions + divider + file tree ═══ */}
      {hasProject ? (
        <div ref={bodyRef} className="flex-1 flex flex-col min-h-0 overflow-hidden">
          {/* Sessions section */}
          <div className="flex flex-col min-h-0 overflow-hidden" style={{ flex: `${sessionsRatio} 1 0%` }}>
            {/* New session button */}
            <div className="p-2 flex-shrink-0">
              <Button
                onClick={handleCreateSession}
                className="w-full justify-start gap-2"
                variant="outline"
                size="sm"
              >
                <Plus className="h-4 w-4" />
                New Session
              </Button>
            </div>

            {/* Session list */}
            <ScrollArea className="flex-1" type="auto">
              <div className="w-full min-w-0 px-2 pb-2 space-y-1">
                {activeSessions.length === 0 && archivedSessions.length === 0 ? (
                  <div className="text-center py-8 text-muted-foreground text-sm">
                    No sessions yet
                  </div>
                ) : (
                  <>
                    {activeSessions.map(session => (
                      <SessionItem
                        key={session.id}
                        session={session}
                        isActive={session.id === activeSessionId}
                        onSelect={() => setActiveSession(session.id)}
                        onRename={(name) => handleRename(session.id, name)}
                        onArchive={() => handleArchive(session.id, session.archived)}
                        onDelete={() => handleDeleteSession(session.id)}
                      />
                    ))}

                    {archivedSessions.length > 0 && activeSessions.length > 0 && (
                      <div className="pt-4 pb-2 px-3">
                        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Archived
                        </span>
                      </div>
                    )}

                    {archivedSessions.map(session => (
                      <SessionItem
                        key={session.id}
                        session={session}
                        isActive={session.id === activeSessionId}
                        onSelect={() => setActiveSession(session.id)}
                        onRename={(name) => handleRename(session.id, name)}
                        onArchive={() => handleArchive(session.id, session.archived)}
                        onDelete={() => handleDeleteSession(session.id)}
                      />
                    ))}
                  </>
                )}
              </div>
              <ScrollBar orientation="horizontal" />
            </ScrollArea>
          </div>

          {/* ─── Horizontal divider (draggable) ─── */}
          <div
            className="h-1.5 flex-shrink-0 bg-border hover:bg-ring active:bg-ring transition-colors cursor-row-resize"
            onMouseDown={onDividerMouseDown}
          />

          {/* File tree section */}
          <div className="min-h-0 overflow-hidden" style={{ flex: `${1 - sessionsRatio} 1 0%` }}>
            <FileTreePanel />
          </div>
        </div>
      ) : (
        /* ═══ No-project empty state in sidebar ═══ */
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="flex flex-col items-center gap-3 text-center">
            <FolderKanban className="h-10 w-10 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">No projects yet</p>
            <Button size="sm" onClick={() => setCreateProjectOpen(true)}>
              <Plus className="h-4 w-4 mr-1" />
              New Project
            </Button>
          </div>
        </div>
      )}

      {/* ═══ Modals ═══ */}
      <SettingsModal open={settingsOpen} onOpenChange={setSettingsOpen} />
      <CreateProjectDialog open={createProjectOpen} onOpenChange={setCreateProjectOpen} />
    </div>
  )
}
