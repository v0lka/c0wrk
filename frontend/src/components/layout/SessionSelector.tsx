import { useState, useMemo, useCallback, useRef } from 'react'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/sessionStore'
import { createSession, renameSession, archiveSession, deleteSession } from '@/api/sessions'
import { formatRelativeTime } from '@/lib/formatters'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuSeparator, DropdownMenuLabel, DropdownMenuSub,
  DropdownMenuSubTrigger, DropdownMenuSubContent,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { ChevronDown, Check, Plus, Pencil, Archive, Trash2 } from 'lucide-react'

export function SessionSelector() {
  const sessions = useSessionStore((s) => s.sessions)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const setActiveSessionId = useSessionStore((s) => s.setActiveSessionId)
  const addSession = useSessionStore((s) => s.addSession)
  const removeSession = useSessionStore((s) => s.removeSession)
  const updateSession = useSessionStore((s) => s.updateSession)

  const [search, setSearch] = useState('')
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [dropdownOpen, setDropdownOpen] = useState(false)
  const renameRef = useRef<HTMLInputElement>(null)

  const activeSessionsList = useMemo(
    () => (sessions ?? []).filter((s) => !s.archived),
    [sessions],
  )
  const archivedList = useMemo(
    () => (sessions ?? []).filter((s) => s.archived),
    [sessions],
  )

  const totalCount = activeSessionsList.length + archivedList.length
  const showSearch = totalCount >= 5

  const filterFn = useCallback((name: string) => {
    if (!search) return true
    return name.toLowerCase().includes(search.toLowerCase())
  }, [search])

  const activeSession = sessions?.find((s) => s.id === activeSessionId)

  const handleNewSession = useCallback(async () => {
    try {
      const session = await createSession()
      addSession(session)
      setActiveSessionId(session.id)
    } catch { /* ignore */ }
  }, [addSession, setActiveSessionId])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await deleteSession(id)
      removeSession(id)
    } catch { /* ignore */ }
  }, [removeSession])

  const handleArchive = useCallback(async (id: string, isArchived: boolean) => {
    try {
      await archiveSession(id)
      updateSession(id, { archived: !isArchived })
    } catch { /* ignore */ }
  }, [updateSession])

  const startRename = useCallback((id: string, currentName: string) => {
    setRenamingId(id)
    setRenameValue(currentName)
    setTimeout(() => renameRef.current?.focus(), 50)
  }, [])

  const commitRename = useCallback(async () => {
    if (!renamingId || !renameValue.trim()) { setRenamingId(null); return }
    try {
      await renameSession(renamingId, renameValue.trim())
      updateSession(renamingId, { name: renameValue.trim() })
    } catch { /* ignore */ }
    setRenamingId(null)
  }, [renamingId, renameValue, updateSession])

  if (renamingId) {
    return (
      <div className="px-2 py-1">
        <Input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') commitRename()
            if (e.key === 'Escape') setRenamingId(null)
          }}
          onBlur={commitRename}
          className="h-7 text-sm"
        />
      </div>
    )
  }

  return (
    <div className="border-b border-border px-2 py-1">
      <DropdownMenu open={dropdownOpen} onOpenChange={(o) => { setDropdownOpen(o); if (!o) setSearch('') }}>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" className="h-7 w-full justify-between gap-1 px-2 text-sm">
            <span className="truncate text-muted-foreground">
              {activeSession?.name ?? 'Select session'}
            </span>
            <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-64">
          {showSearch && (
            <>
              <div className="px-2 py-1">
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="Search sessions..."
                  className="h-7 text-sm"
                  autoFocus
                />
              </div>
              <DropdownMenuSeparator />
            </>
          )}

          {activeSessionsList.filter((s) => filterFn(s.name)).map((session) => (
            <SessionItem
              key={session.id}
              session={session}
              isActive={session.id === activeSessionId}
              onSelect={() => setActiveSessionId(session.id)}
              onRename={() => startRename(session.id, session.name)}
              onArchive={() => handleArchive(session.id, session.archived)}
              onDelete={() => handleDelete(session.id)}
            />
          ))}

          {archivedList.filter((s) => filterFn(s.name)).length > 0 && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuLabel className="text-xs text-muted-foreground">Archived</DropdownMenuLabel>
              {archivedList.filter((s) => filterFn(s.name)).map((session) => (
                <SessionItem
                  key={session.id}
                  session={session}
                  isActive={session.id === activeSessionId}
                  onSelect={() => setActiveSessionId(session.id)}
                  onRename={() => startRename(session.id, session.name)}
                  onArchive={() => handleArchive(session.id, session.archived)}
                  onDelete={() => handleDelete(session.id)}
                />
              ))}
            </>
          )}

          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={handleNewSession}>
            <Plus className="size-3.5" />
            New Session...
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

// --- Session Item ---
interface SessionItemProps {
  session: { id: string; name: string; active: boolean; archived: boolean; last_active_at: string }
  isActive: boolean
  onSelect: () => void
  onRename: () => void
  onArchive: () => void
  onDelete: () => void
}
function SessionItem({ session, isActive, onSelect, onRename, onArchive, onDelete }: SessionItemProps) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger className="gap-2" onClick={(e) => { e.preventDefault(); onSelect() }}>
        <div className="flex flex-1 items-center gap-1.5 truncate">
          {session.active && <span className="size-1.5 shrink-0 rounded-full bg-success" />}
          {isActive && <Check className="size-3.5 shrink-0" />}
          <span className={cn('truncate', isActive && 'font-medium')}>{session.name}</span>
        </div>
        <span className="ml-auto text-[10px] text-muted-foreground">{formatRelativeTime(session.last_active_at)}</span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        <DropdownMenuItem onClick={onRename}><Pencil className="size-3.5" />Rename</DropdownMenuItem>
        <DropdownMenuItem onClick={onArchive}><Archive className="size-3.5" />{session.archived ? 'Unarchive' : 'Archive'}</DropdownMenuItem>
        <DropdownMenuItem variant="destructive" onClick={onDelete}><Trash2 className="size-3.5" />Delete</DropdownMenuItem>
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}
