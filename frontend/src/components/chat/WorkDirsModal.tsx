import { useEffect, useMemo, useState, useCallback } from 'react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { FolderPlus, Trash2, Loader2, AlertTriangle } from 'lucide-react'
import { useWorkDirsStore } from '@/stores/workDirsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { onGlobalEvent } from '@/api/runtime'
import { pickDirectory } from '@/api/projects'
import type { WorkDirectoryRecord, WorkDirScope } from '@/api/workdirs'

/**
 * Returns a human-readable warning if the path is sensitive (SSH keys,
 * credentials, config) or — for regular projects — outside the workspace tree.
 * Returns null when the path is safe. This is a frontend advisory warning; the
 * backend independently normalizes and validates the path. The warning ensures
 * the user understands they are granting the agent unconfirmed (auto-approved)
 * access to everything beneath the chosen root.
 */
function sensitivePathReason(path: string, workspacePath: string | null): string | null {
  const normalized = path.replace(/\/+$/, '')

  // The filesystem root is never acceptable.
  if (normalized === '/' || normalized === '') {
    return 'The filesystem root cannot be added as a work directory.'
  }

  // Well-known sensitive directories — grant is too broad and dangerous.
  const sensitiveSuffixes = ['.ssh', '.aws', '.gnupg', '.config', '.kube']
  for (const suffix of sensitiveSuffixes) {
    if (normalized.endsWith('/' + suffix)) {
      return `This is a sensitive directory (${suffix}) — the agent will have unconfirmed access to everything beneath it.`
    }
  }

  // For regular projects, warn when the path is outside the workspace tree.
  if (workspacePath) {
    const wsNormalized = workspacePath.replace(/\/+$/, '')
    if (!normalized.startsWith(wsNormalized + '/') && normalized !== wsNormalized) {
      return 'This directory is outside the project workspace. The agent will gain unconfirmed read access to everything beneath it for this project/session.'
    }
  }

  return null
}

/** A single working-directory row: path + editable description + delete. */
function WorkDirRow({
  record,
  scope,
  onUpdate,
  onDelete,
}: {
  record: WorkDirectoryRecord
  scope: WorkDirScope
  onUpdate: (scope: WorkDirScope, id: string, description: string) => Promise<void>
  onDelete: (scope: WorkDirScope, id: string) => Promise<void>
}) {
  const [desc, setDesc] = useState(record.description)
  const [saving, setSaving] = useState(false)
  const [emptyHint, setEmptyHint] = useState(false)

  // Keep local field in sync if the record changes after a refetch.
  useEffect(() => {
    setDesc(record.description)
  }, [record.description])

  // Auto-dismiss the empty-description hint after a short delay.
  useEffect(() => {
    if (!emptyHint) return
    const timer = setTimeout(() => setEmptyHint(false), 2500)
    return () => clearTimeout(timer)
  }, [emptyHint])

  const commit = useCallback(async () => {
    // No change — nothing to do.
    if (desc === record.description) return
    // Empty description is not allowed: revert to the stored value and hint.
    if (!desc.trim()) {
      setDesc(record.description)
      setEmptyHint(true)
      return
    }
    setSaving(true)
    try {
      await onUpdate(scope, record.id, desc.trim())
    } finally {
      setSaving(false)
    }
  }, [desc, record.description, record.id, scope, onUpdate])

  const handleRemove = useCallback(async () => {
    await onDelete(scope, record.id)
  }, [scope, record.id, onDelete])

  return (
    <div className="flex flex-col gap-1 py-1.5">
      <span
        className="min-w-0 truncate font-mono text-xs text-muted-foreground"
        title={record.path}
      >
        {record.path}
      </span>
      <div className="flex items-center gap-2">
        <Input
          value={desc}
          onChange={(e) => {
            setDesc(e.target.value)
            if (emptyHint) setEmptyHint(false)
          }}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              commit()
            }
          }}
          placeholder="Description"
          className="h-7 flex-1 text-xs"
          disabled={saving}
          aria-invalid={emptyHint}
        />
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={handleRemove}
          title="Remove directory"
          aria-label="Remove directory"
          className="text-muted-foreground hover:text-destructive"
        >
          <Trash2 className="size-3.5" />
        </Button>
      </div>
      {emptyHint && (
        <span className="text-[10px] text-warning pl-1" role="status">
          Description cannot be empty — reverted to previous value.
        </span>
      )}
    </div>
  )
}

export function WorkDirsModal() {
  const open = useWorkDirsStore((s) => s.open)
  const setOpen = useWorkDirsStore((s) => s.setOpen)
  const projectDirs = useWorkDirsStore((s) => s.projectDirs)
  const sessionDirs = useWorkDirsStore((s) => s.sessionDirs)
  const loading = useWorkDirsStore((s) => s.loading)
  const loadAll = useWorkDirsStore((s) => s.loadAll)
  const add = useWorkDirsStore((s) => s.add)
  const update = useWorkDirsStore((s) => s.updateDescription)
  const remove = useWorkDirsStore((s) => s.remove)

  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  // Detect "No Project" via the active project's is_no_project flag.
  const isNoProject = useMemo(
    () => Boolean(activeProjectId && projects?.find((p) => p.id === activeProjectId)?.is_no_project),
    [activeProjectId, projects],
  )
  const projectIdForLoad = isNoProject || !activeProjectId ? null : activeProjectId

  // Resolve scope → ownerID for row mutations (the store's update/remove now
  // require an ownerID scope-guard, matching the backend).
  const handleUpdate = useCallback(
    async (scope: WorkDirScope, id: string, description: string) => {
      const ownerID = scope === 'project' ? activeProjectId : activeSessionId
      if (!ownerID) return
      await update(scope, ownerID, id, description)
    },
    [activeProjectId, activeSessionId, update],
  )
  const handleDelete = useCallback(
    async (scope: WorkDirScope, id: string) => {
      const ownerID = scope === 'project' ? activeProjectId : activeSessionId
      if (!ownerID) return
      await remove(scope, ownerID, id)
    },
    [activeProjectId, activeSessionId, remove],
  )

  // Reload whenever the modal opens or the active project/session switches.
  useEffect(() => {
    if (!open) return
    void loadAll(projectIdForLoad, activeSessionId)
  }, [open, projectIdForLoad, activeSessionId, loadAll])

  // Refetch on backend mutation events while the modal is open.
  useEffect(() => {
    if (!open) return
    return onGlobalEvent('workdirs:changed', () => {
      const st = useWorkDirsStore.getState()
      void loadAll(st.loadedProjectId, st.loadedSessionId)
    })
  }, [open, loadAll])

  // Workspace path for the sensitivity/outside-workspace check (#2b).
  const workspacePath = useMemo(() => {
    if (isNoProject || !activeProjectId) return null
    return projects?.find((p) => p.id === activeProjectId)?.workspace_path ?? null
  }, [isNoProject, activeProjectId, projects])

  const handleOpenChange = useCallback(
    (isOpen: boolean) => {
      if (!isOpen) setOpen(false)
    },
    [setOpen],
  )

  // --- Add flow ---
  const [pendingPath, setPendingPath] = useState<string | null>(null)
  const [pendingDesc, setPendingDesc] = useState('')
  const [pendingScope, setPendingScope] = useState<WorkDirScope>('session')
  const [addError, setAddError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)

  const pathWarning = useMemo(
    () => (pendingPath ? sensitivePathReason(pendingPath, workspacePath) : null),
    [pendingPath, workspacePath],
  )

  const handlePick = useCallback(async () => {
    setAddError(null)
    try {
      const path = await pickDirectory()
      if (path) {
        setPendingPath(path)
        setPendingScope('session')
        setPendingDesc('')
      }
    } catch {
      // User cancelled or picker unavailable — no error state.
    }
  }, [])

  const handleConfirmAdd = useCallback(async () => {
    if (!pendingPath) return
    // Hard-block the filesystem root — it is never a valid work directory.
    if (pendingPath.replace(/\/+$/, '') === '/') {
      setAddError('The filesystem root cannot be added as a work directory.')
      return
    }
    if (!pendingDesc.trim()) {
      setAddError('Description is required')
      return
    }
    const ownerID = pendingScope === 'project' ? activeProjectId : activeSessionId
    if (!ownerID) {
      setAddError(`No active ${pendingScope} to attach to`)
      return
    }
    setAdding(true)
    try {
      await add(pendingScope, ownerID, pendingPath, pendingDesc.trim())
      setPendingPath(null)
      setPendingDesc('')
      setAddError(null)
    } catch (err) {
      setAddError(err instanceof Error ? err.message : String(err))
    } finally {
      setAdding(false)
    }
  }, [pendingPath, pendingDesc, pendingScope, activeProjectId, activeSessionId, add])
  const cancelAdd = useCallback(() => {
    setPendingPath(null)
    setPendingDesc('')
    setAddError(null)
  }, [])

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-[640px] max-h-[80vh] flex flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>Working Directories</DialogTitle>
        </DialogHeader>

        <div className="flex-1 overflow-y-auto min-h-0 py-2 custom-scrollbar">
          {loading ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
            </div>
          ) : (
            <>
              {!isNoProject && (
                <section className="mb-4">
                  <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1">
                    Project-scoped
                  </h3>
                  {projectDirs.length === 0 ? (
                    <p className="text-xs text-muted-foreground/70 py-2">No project directories.</p>
                  ) : (
                    projectDirs.map((rec) => (
                      <WorkDirRow
                        key={`p-${rec.id}`}
                        record={rec}
                        scope="project"
                        onUpdate={handleUpdate}
                        onDelete={handleDelete}
                      />
                    ))
                  )}
                </section>
              )}

              <section className="mb-2">
                <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1">
                  Session-scoped
                </h3>
                {sessionDirs.length === 0 ? (
                  <p className="text-xs text-muted-foreground/70 py-2">No session directories.</p>
                ) : (
                  sessionDirs.map((rec) => (
                    <WorkDirRow
                      key={`s-${rec.id}`}
                      record={rec}
                      scope="session"
                      onUpdate={handleUpdate}
                      onDelete={handleDelete}
                    />
                  ))
                )}
              </section>

              {/* Add flow */}
              <div className="mt-3 pt-3 border-t border-border">
                {!pendingPath ? (
                  <Button variant="outline" size="sm" onClick={handlePick}>
                    <FolderPlus className="size-4" />
                    Add directory
                  </Button>
                ) : (
                  <div className="flex flex-col gap-2">
                    <span
                      className="font-mono text-xs text-muted-foreground truncate"
                      title={pendingPath}
                    >
                      {pendingPath}
                    </span>
                    {pathWarning && (
                      <div
                        className="flex items-start gap-1.5 rounded-md border border-warning/30 bg-warning/10 p-2"
                        role="alert"
                      >
                        <AlertTriangle className="size-3.5 shrink-0 text-warning mt-0.5" />
                        <span className="text-xs text-warning">{pathWarning}</span>
                      </div>
                    )}
                    <div className="flex items-center gap-2">
                      <Input
                        value={pendingDesc}
                        onChange={(e) => {
                          setPendingDesc(e.target.value)
                          setAddError(null)
                        }}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') {
                            e.preventDefault()
                            void handleConfirmAdd()
                          } else if (e.key === 'Escape') {
                            cancelAdd()
                          }
                        }}
                        placeholder="Description (required)"
                        className="h-8 flex-1 text-xs"
                        autoFocus
                      />
                      {!isNoProject && (
                        <div className="flex items-center rounded-md border border-input overflow-hidden">
                          <button
                            type="button"
                            onClick={() => setPendingScope('project')}
                            className={`px-2 py-1 text-xs transition-colors ${pendingScope === 'project' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted/50'}`}
                          >
                            Project
                          </button>
                          <button
                            type="button"
                            onClick={() => setPendingScope('session')}
                            className={`px-2 py-1 text-xs transition-colors ${pendingScope === 'session' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-muted/50'}`}
                          >
                            Session
                          </button>
                        </div>
                      )}
                    </div>
                    {addError && (
                      <span className="text-xs text-destructive" role="alert">
                        {addError}
                      </span>
                    )}
                    <div className="flex items-center gap-2">
                      <Button size="sm" onClick={handleConfirmAdd} disabled={adding}>
                        {adding ? <Loader2 className="size-3.5 animate-spin" /> : 'Save'}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={cancelAdd}>
                        Cancel
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
