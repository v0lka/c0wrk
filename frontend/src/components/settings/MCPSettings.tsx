import { useState, useEffect, useCallback } from 'react'
import { Plus, RefreshCw, Server, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from '@/components/ui/dialog'
import { getMCPStatus, getMCPServers, getToolList, updateMCPServers } from '@/api/mcp'
import { logger } from '@/lib/logger'
import { MCPServerCard } from './MCPServerCard'
import { MCPServerForm } from './MCPServerForm'
import type { MCPServerStatus, MCPServerConfig, ToolInfo } from '@/types/models'

export function MCPSettings() {
  const [servers, setServers] = useState<MCPServerStatus[]>([])
  const [configs, setConfigs] = useState<Record<string, MCPServerConfig>>({})
  const [tools, setTools] = useState<ToolInfo[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  // Form state
  const [formOpen, setFormOpen] = useState(false)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [editServer, setEditServer] = useState<{ name: string; transport: string } | undefined>()
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const [st, tl, cf] = await Promise.all([getMCPStatus(), getToolList(), getMCPServers()])
      setServers(st || [])
      setTools(tl || [])
      setConfigs(cf || {})
    } catch (err) {
      logger.error('Failed to load MCP status:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally { setIsLoading(false) }
  }, [])

  useEffect(() => { loadData() }, [loadData])

  const handleSave = async (newServers: Record<string, MCPServerConfig>): Promise<string | null> => {
    setIsSaving(true)
    try { await updateMCPServers(newServers); setConfigs(newServers); await loadData(); return null }
    catch (err) { return err instanceof Error ? err.message : String(err) }
    finally { setIsSaving(false) }
  }

  const handleDelete = async (name: string) => {
    const next = { ...configs }; delete next[name]
    setIsSaving(true)
    try { await updateMCPServers(next); setConfigs(next); await loadData() }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setIsSaving(false); setDeleteConfirm(null) }
  }

  const openAdd = () => { setEditingName(null); setEditServer(undefined); setFormOpen(true) }
  const openEdit = (s: MCPServerStatus) => { setEditingName(s.name); setEditServer({ name: s.name, transport: s.transport }); setFormOpen(true) }

  if (isLoading) return <div className="flex items-center justify-center py-8"><span className="text-sm text-muted-foreground">Loading MCP settings...</span></div>

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2"><Server className="h-4 w-4 text-muted-foreground" /><span className="text-sm font-medium">MCP Servers</span></div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={loadData} disabled={isLoading}><RefreshCw className="h-3 w-3" /></Button>
          <Button variant="default" size="sm" onClick={openAdd}><Plus className="h-3 w-3 mr-1" />Add Server</Button>
        </div>
      </div>

      {error && <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm"><AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" /><p>{error}</p></div>}

      {servers.length === 0
        ? <div className="text-sm text-muted-foreground py-4 text-center">No MCP servers configured.</div>
        : <div className="space-y-2">{servers.map((s) => <MCPServerCard key={s.name} server={s} tools={tools.filter((t) => t.source === s.name)} expanded={expanded.has(s.name)} onToggleExpand={() => setExpanded((prev) => { const n = new Set(prev); if (n.has(s.name)) { n.delete(s.name) } else { n.add(s.name) } return n })} onEdit={() => openEdit(s)} onDelete={() => setDeleteConfirm(s.name)} />)}</div>
      }

      {formOpen && <MCPServerForm open={formOpen} onOpenChange={setFormOpen} editingName={editingName} serverConfigs={configs} editServer={editServer} isSaving={isSaving} onSave={handleSave} />}

      <Dialog open={!!deleteConfirm} onOpenChange={(o) => { if (!o) setDeleteConfirm(null) }}>
        <DialogContent className="sm:max-w-[400px]">
          <DialogHeader><DialogTitle>Delete Server</DialogTitle><DialogDescription>Are you sure you want to delete &quot;{deleteConfirm}&quot;?</DialogDescription></DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteConfirm(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => deleteConfirm && handleDelete(deleteConfirm)} disabled={isSaving}>{isSaving ? 'Deleting...' : 'Delete'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
