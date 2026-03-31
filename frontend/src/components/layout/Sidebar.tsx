import { useEffect, useState, useCallback } from 'react'
import { Plus, Settings, MoreVertical, Archive, Trash2, Edit3 } from 'lucide-react'
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
import { useSessionAPI } from '@/hooks/useSession'
import { SettingsModal } from '@/components/settings/SettingsModal'
import type { SessionInfo } from '@/lib/wails'

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

interface SessionItemProps {
  session: SessionInfo
  isActive: boolean
  onSelect: () => void
  onRename: (name: string) => void
  onArchive: () => void
  onDelete: () => void
}

function SessionItem({ session, isActive, onSelect, onRename, onArchive, onDelete }: SessionItemProps) {
  const [isEditing, setIsEditing] = useState(false)
  const [editName, setEditName] = useState(session.name)

  const handleRename = () => {
    if (editName.trim() && editName !== session.name) {
      onRename(editName.trim())
    }
    setIsEditing(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleRename()
    } else if (e.key === 'Escape') {
      setEditName(session.name)
      setIsEditing(false)
    }
  }

  return (
    <div
      className={`
        group flex items-center gap-2 px-3 py-2 rounded-md cursor-pointer
        transition-colors duration-150
        ${isActive 
          ? 'bg-accent text-accent-foreground' 
          : 'hover:bg-accent/50 text-foreground'
        }
      `}
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
            <div className="text-sm font-medium truncate">
              {session.name}
            </div>
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
          >
            <MoreVertical className="h-3 w-3" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-40">
          <DropdownMenuItem onClick={() => setIsEditing(true)}>
            <Edit3 className="h-4 w-4 mr-2" />
            Rename
          </DropdownMenuItem>
          <DropdownMenuItem onClick={onArchive}>
            <Archive className="h-4 w-4 mr-2" />
            {session.archived ? 'Unarchive' : 'Archive'}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem 
            onClick={onDelete}
            className="text-destructive focus:text-destructive"
          >
            <Trash2 className="h-4 w-4 mr-2" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

export function Sidebar() {
  const sessions = useSessionStore(s => s.sessions)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const setSessions = useSessionStore(s => s.setSessions)
  const addSession = useSessionStore(s => s.addSession)
  const removeSession = useSessionStore(s => s.removeSession)
  const setActiveSession = useSessionStore(s => s.setActiveSession)
  const updateSession = useSessionStore(s => s.updateSession)
  
  const api = useSessionAPI()
  const [settingsOpen, setSettingsOpen] = useState(false)

  // Load sessions on mount
  useEffect(() => {
    const loadSessions = async () => {
      try {
        const list = await api.listSessions()
        if (list) {
          setSessions(list)
          // Auto-select the most recent session if none is selected
          if (list.length > 0 && !activeSessionId) {
            setActiveSession(list[0].id)
          }
        }
      } catch (err) {
        console.error('Failed to load sessions:', err)
      }
    }
    loadSessions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleCreateSession = useCallback(async () => {
    try {
      const session = await api.createSession()
      if (session) {
        addSession(session)
        setActiveSession(session.id)
      }
    } catch (err) {
      console.error('Failed to create session:', err)
      // Show error to user - use alert since there's no toast component
      const errorMessage = err instanceof Error ? err.message : String(err)
      alert(`Failed to create session: ${errorMessage}`)
    }
  }, [api, addSession, setActiveSession])

  const handleRename = useCallback(async (id: string, name: string) => {
    try {
      await api.renameSession(id, name)
      updateSession(id, { name })
    } catch (err) {
      console.error('Failed to rename session:', err)
    }
  }, [api, updateSession])

  const handleArchive = useCallback(async (id: string, currentArchived: boolean) => {
    try {
      await api.archiveSession(id)
      updateSession(id, { archived: !currentArchived })
    } catch (err) {
      console.error('Failed to archive session:', err)
    }
  }, [api, updateSession])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await api.deleteSession(id)
      removeSession(id)
    } catch (err) {
      console.error('Failed to delete session:', err)
    }
  }, [api, removeSession])

  // Separate active and archived sessions
  const activeSessions = sessions.filter(s => !s.archived)
  const archivedSessions = sessions.filter(s => s.archived)

  return (
    <div className="h-full flex flex-col bg-card">
      {/* Header */}
      <div className="p-3 border-b border-border">
        <Button 
          onClick={handleCreateSession}
          className="w-full justify-start gap-2"
          variant="outline"
        >
          <Plus className="h-4 w-4" />
          New Session
        </Button>
      </div>

      {/* Session list */}
      <ScrollArea className="flex-1" type="auto">
        <div className="w-full min-w-0 p-2 space-y-1">
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
                  onDelete={() => handleDelete(session.id)}
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
                  onDelete={() => handleDelete(session.id)}
                />
              ))}
            </>
          )}
        </div>
        <ScrollBar orientation="horizontal" />
      </ScrollArea>

      {/* Footer */}
      <div className="p-3 border-t border-border">
        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start gap-2 text-muted-foreground"
          onClick={() => setSettingsOpen(true)}
        >
          <Settings className="h-4 w-4" />
          Settings
        </Button>
      </div>

      {/* Settings Modal */}
      <SettingsModal open={settingsOpen} onOpenChange={setSettingsOpen} />
    </div>
  )
}
