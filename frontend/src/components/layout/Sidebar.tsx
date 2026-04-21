import { useEffect, useState, useCallback, useMemo, useRef } from 'react'
import {
  Plus, Settings, MoreVertical, Archive, Trash2, Edit3,
  ChevronDown, FolderKanban, Check, PanelLeftClose,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionAPI } from '@/hooks/useSession'
import { useProjectAPI } from '@/hooks/useProject'
import { SettingsModal } from '@/components/settings/SettingsModal'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import { WorkspacePanel } from './WorkspacePanel'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import type { SessionInfo, ProjectInfo } from '@/lib/wails'
import { useSettingsStore } from '@/stores/settingsStore'
import { useUIStore } from '@/stores/uiStore'

// ── Helpers ──────────────────────────────────────────────────────────

function isProjectInfo(v: unknown): v is ProjectInfo {
  return typeof v === 'object' && v !== null && typeof (v as Record<string, unknown>).id === 'string' && typeof (v as Record<string, unknown>).name === 'string'
}

function isSessionInfo(v: unknown): v is SessionInfo {
  return typeof v === 'object' && v !== null && typeof (v as Record<string, unknown>).id === 'string' && typeof (v as Record<string, unknown>).name === 'string'
}

function isProjectInfoArray(data: unknown): data is ProjectInfo[] {
  return Array.isArray(data) && data.every(isProjectInfo)
}

function isSessionInfoArray(data: unknown): data is SessionInfo[] {
  return Array.isArray(data) && data.every(isSessionInfo)
}

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
  const openSettings = useSettingsStore(s => s.openSettings)
  const toggleSidebarCollapsed = useUIStore(s => s.toggleSidebarCollapsed)
  const [createProjectOpen, setCreateProjectOpen] = useState(false)
  const [renamingProjectId, setRenamingProjectId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renamingSessionId, setRenamingSessionId] = useState<string | null>(null)
  const [sessionRenameValue, setSessionRenameValue] = useState('')
  const [sessionSearch, setSessionSearch] = useState('')

  const activeProject = useMemo(
    () => projects?.find(p => p.id === activeProjectId) ?? null,
    [projects, activeProjectId]
  )

  // ── Load projects (extracted so it can be triggered from multiple places) ──
  const projectFetchIdRef = useRef(0)
  const loadProjects = useCallback(async () => {
    const myId = ++projectFetchIdRef.current
    try {
      const list = await projectAPI.listProjects()
      if (myId !== projectFetchIdRef.current) return // stale response
      if (list) {
        setProjects(list)
        // Auto-select the most recent project when there are projects
        if (list.length > 0) {
          const first = list[0]
          if (first) {
            setActiveProject(first.id)
            await projectAPI.switchProject(first.id)
          }
        }
      }
    } catch (err) {
      if (myId !== projectFetchIdRef.current) return
      logger.error('Failed to load projects:', err)
    }
  }, [projectAPI, setProjects, setActiveProject])

  // ── Listen for early project data (emitted before heavy backend init) ──
  useEffect(() => {
    if (!window?.runtime) return
    const cancel = window.runtime.EventsOn('projects:loaded', (data: unknown) => {
      if (!isProjectInfoArray(data) || data.length === 0) return
      setProjects(data)
      const first = data[0]
      if (first) {
        setActiveProject(first.id)
        projectAPI.switchProject(first.id)
      }
    })
    return () => { cancel() }
  }, [setProjects, setActiveProject, projectAPI])

  // ── Listen for early session data (emitted before heavy backend init) ──
  useEffect(() => {
    if (!window?.runtime) return
    const cancel = window.runtime.EventsOn('sessions:loaded', (data: unknown) => {
      if (!isSessionInfoArray(data)) return
      setSessions(data)
      const first = data[0]
      if (first) {
        setActiveSession(first.id)
      }
    })
    return () => { cancel() }
  }, [setSessions, setActiveSession])

  // ── Load projects on mount + listen for backend:ready ──
  useEffect(() => {
    // Attempt immediately (works if backend is already up)
    loadProjects()

    // Also listen for the backend readiness event (covers the race where
    // the component mounts before Go's OnStartup finishes).
    // The backend may pre-emit project data alongside the event to avoid
    // an extra round-trip.
    if (!window?.runtime) return
    const cancel = window.runtime.EventsOn('backend:ready', (data?: unknown) => {
      if (isProjectInfoArray(data) && data.length > 0) {
        // Pre-emitted projects — use directly
        setProjects(data)
        const first = data[0]
        if (first) {
          setActiveProject(first.id)
          projectAPI.switchProject(first.id)
        }
      } else {
        // No pre-emitted data — fetch manually
        loadProjects()
      }
    })
    return () => { cancel() }
  }, [loadProjects, projectAPI, setActiveProject, setProjects])

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
  }, [activeProjectId, sessionAPI, setSessions, setActiveSession])

  // ── Subscribe to backend project events ──
  useEffect(() => {
    if (!window?.runtime) return
    const unsubs = [
      window.runtime.EventsOn('project:created', (data: unknown) => {
        if (!isProjectInfo(data)) return
        addProject(data)
      }),
      window.runtime.EventsOn('project:deleted', (data: unknown) => {
        if (typeof data !== 'string') return
        removeProject(data)
      }),
      window.runtime.EventsOn('project:renamed', (data: unknown) => {
        if (typeof data !== 'object' || data === null) return
        const d = data as Record<string, unknown>
        if (typeof d.id !== 'string' || typeof d.name !== 'string') return
        updateProject(d.id, { name: d.name })
      }),
      window.runtime.EventsOn('project:switched', (data: unknown) => {
        if (typeof data !== 'object' || data === null) return
        const p = data as Record<string, unknown>
        if (typeof p.id !== 'string') return
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
      const remaining = useProjectStore.getState().projects ?? []
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
    try {
      // Backend DeleteSession will cancel any active task and wait for it to stop.
      await sessionAPI.deleteSession(id)
      removeSession(id)
      // If the deleted session was active, reset the UI task state so input is re-enabled.
      if (useSessionStore.getState().activeSessionId === null) {
        useChatStore.getState().clearSessionUIState()
      }
    } catch (err) { logger.error('Failed to delete session:', err) }
  }, [sessionAPI, removeSession])

  const handleFinishSessionRename = useCallback(() => {
    if (!renamingSessionId) return
    const trimmed = sessionRenameValue.trim()
    if (trimmed) {
      handleRename(renamingSessionId, trimmed)
    }
    setRenamingSessionId(null)
  }, [renamingSessionId, sessionRenameValue, handleRename])

  const [activeSessions, archivedSessions] = useMemo(() => [
    (sessions ?? []).filter(s => !s.archived),
    (sessions ?? []).filter(s => s.archived),
  ], [sessions])

  const totalSessionCount = activeSessions.length + archivedSessions.length

  const filteredActiveSessions = useMemo(() => {
    if (!sessionSearch) return activeSessions
    const q = sessionSearch.toLowerCase()
    return activeSessions.filter(s => s.name.toLowerCase().includes(q))
  }, [activeSessions, sessionSearch])

  const filteredArchivedSessions = useMemo(() => {
    if (!sessionSearch) return archivedSessions
    const q = sessionSearch.toLowerCase()
    return archivedSessions.filter(s => s.name.toLowerCase().includes(q))
  }, [archivedSessions, sessionSearch])

  const activeSessionInfo = useMemo(
    () => (sessions ?? []).find(s => s.id === activeSessionId) ?? null,
    [sessions, activeSessionId]
  )

  const hasProject = (projects?.length ?? 0) > 0 && activeProjectId

  // ── Session dropdown item renderer ──
  const renderSessionItem = useCallback((s: SessionInfo) => (
    <DropdownMenuItem
      key={s.id}
      className="flex items-center justify-between gap-2"
      onClick={() => setActiveSession(s.id)}
    >
      <div className="flex-1 min-w-0">
        <div className="text-sm truncate">{s.name}</div>
        <div className="text-xs text-muted-foreground">
          {formatRelativeTime(s.last_active_at || s.created_at)}
        </div>
      </div>
      <div className="flex items-center gap-1 flex-shrink-0">
        {s.id === activeSessionId && <Check className="h-3.5 w-3.5" />}
        {s.active && <div className="w-2 h-2 rounded-full bg-success" />}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="p-0.5 rounded hover:bg-accent"
              onClick={e => e.stopPropagation()}
              aria-label="Session actions"
            >
              <MoreVertical className="h-3 w-3" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-36">
            <DropdownMenuItem onClick={e => { e.stopPropagation(); setRenamingSessionId(s.id); setSessionRenameValue(s.name) }}>
              <Edit3 className="h-4 w-4 mr-2" /> Rename
            </DropdownMenuItem>
            <DropdownMenuItem onClick={e => { e.stopPropagation(); handleArchive(s.id, s.archived) }}>
              <Archive className="h-4 w-4 mr-2" /> {s.archived ? 'Unarchive' : 'Archive'}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="text-destructive focus:text-destructive"
              onClick={e => { e.stopPropagation(); handleDeleteSession(s.id) }}
            >
              <Trash2 className="h-4 w-4 mr-2" /> Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </DropdownMenuItem>
  ), [activeSessionId, setActiveSession, handleArchive, handleDeleteSession])

  // ── Render ──

  return (
    <div className="h-full flex flex-col bg-card">
      {/* ═══ Header: Row 1 — Project selector + actions ═══ */}
      <div className="flex flex-col border-b border-border flex-shrink-0">
        <div className="flex items-center gap-1 p-2">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 flex-shrink-0"
            onClick={toggleSidebarCollapsed}
            title="Collapse sidebar"
          >
            <PanelLeftClose className="h-3.5 w-3.5" />
          </Button>
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
              <DropdownMenuContent align="start" className="w-56 custom-scrollbar max-h-80">
                {(projects ?? []).map(p => (
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
            onClick={() => openSettings()}
            title="Settings"
          >
            <Settings className="h-3.5 w-3.5" />
          </Button>
        </div>

        {/* ═══ Header: Row 2 — Session selector + New Session ═══ */}
        {hasProject && (
          <div className="flex items-center gap-1 px-2 pb-2">
            <DropdownMenu onOpenChange={(open) => { if (!open) setSessionSearch('') }}>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="sm" className="flex-1 justify-between min-w-0 gap-1 px-2">
                  <div className="flex items-center gap-1.5 min-w-0">
                    {activeSessionInfo?.active && (
                      <div className="w-2 h-2 rounded-full bg-success flex-shrink-0" />
                    )}
                    <span className={cn(
                      'truncate text-sm font-medium',
                      !activeSessionInfo && 'text-muted-foreground'
                    )}>
                      {activeSessionInfo?.name ?? 'No sessions'}
                    </span>
                  </div>
                  <ChevronDown className="h-3.5 w-3.5 flex-shrink-0 opacity-60" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-64 custom-scrollbar max-h-80">
                {totalSessionCount >= 5 && (
                  <div className="p-2">
                    <input
                      type="text"
                      placeholder="Search sessions..."
                      className="w-full min-w-0 bg-secondary border border-border rounded px-1.5 py-0.5 text-xs text-foreground placeholder:text-muted-foreground/50 outline-none focus:border-ring transition-colors"
                      value={sessionSearch}
                      onChange={e => setSessionSearch(e.target.value)}
                      onClick={e => e.stopPropagation()}
                    />
                  </div>
                )}
                {filteredActiveSessions.map(renderSessionItem)}

                {filteredArchivedSessions.length > 0 && filteredActiveSessions.length > 0 && (
                  <DropdownMenuSeparator />
                )}

                {filteredArchivedSessions.length > 0 && (
                  <div className="px-2 py-1.5">
                    <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                      Archived
                    </span>
                  </div>
                )}

                {filteredArchivedSessions.map(renderSessionItem)}

                {filteredActiveSessions.length === 0 && filteredArchivedSessions.length === 0 && (
                  <div className="text-center py-4 text-muted-foreground text-sm">
                    {sessionSearch ? 'No matching sessions' : 'No sessions yet'}
                  </div>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={handleCreateSession}>
                  <Plus className="h-4 w-4 mr-2" /> New Session...
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>

            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 flex-shrink-0"
              onClick={handleCreateSession}
              title="New session"
            >
              <Plus className="h-3.5 w-3.5" />
            </Button>
          </div>
        )}
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

      {/* ═══ Inline session rename ═══ */}
      {renamingSessionId && (
        <div className="px-2 py-1.5 border-b border-border flex-shrink-0">
          <Input
            value={sessionRenameValue}
            onChange={(e) => setSessionRenameValue(e.target.value)}
            onBlur={handleFinishSessionRename}
            onKeyDown={(e) => {
              if (e.key === 'Enter') handleFinishSessionRename()
              else if (e.key === 'Escape') setRenamingSessionId(null)
            }}
            autoFocus
            className="h-7 text-sm"
            placeholder="Session name"
          />
        </div>
      )}

      {/* ═══ Body: file tree only ═══ */}
      {hasProject ? (
        <div className="flex-1 min-h-0 overflow-hidden">
          <WorkspacePanel />
        </div>
      ) : projects === null ? (
        /* ═══ Projects still loading — show nothing ═══ */
        <div className="flex-1" />
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
      <SettingsModal />
      <CreateProjectDialog open={createProjectOpen} onOpenChange={setCreateProjectOpen} />
    </div>
  )
}
