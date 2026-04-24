import { useState, useEffect, useCallback } from 'react'
import { Plus, RefreshCw, Server, AlertCircle, Download, Loader2, Cpu, Terminal } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from '@/components/ui/dialog'
import { getMCPStatus, getMCPServers, getToolList, updateMCPServers, checkCodebaseMemoryMCP, installCodebaseMemoryMCP, checkRtk, installRtk } from '@/api/mcp'
import { subscribe } from '@/api/runtime'
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

  // Installers
  const [cmInstalled, setCmInstalled] = useState(false)
  const [cmPath, setCmPath] = useState('')
  const [cmProgress, setCmProgress] = useState<string | null>(null)
  const [cmError, setCmError] = useState<string | null>(null)
  const [rtkInstalled, setRtkInstalled] = useState(false)
  const [rtkPath, setRtkPath] = useState('')
  const [rtkVersion, setRtkVersion] = useState('')
  const [rtkProgress, setRtkProgress] = useState<string | null>(null)
  const [rtkError, setRtkError] = useState<string | null>(null)

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

  // Check installer statuses
  useEffect(() => {
    checkCodebaseMemoryMCP().then((r) => { setCmInstalled(r.installed); setCmPath(r.path) }).catch((e) => logger.error('CM check failed:', e))
    checkRtk().then((r) => { setRtkInstalled(r.installed); setRtkPath(r.path); setRtkVersion(r.version) }).catch((e) => logger.error('RTK check failed:', e))
  }, [])

  // Installer events
  useEffect(() => {
    const unsubs = [
      subscribe('codememory:install-progress', (d) => { if (typeof d === 'string') { setCmProgress(d === 'done' || d === 'error' ? null : d) } }),
      subscribe('codememory:status', (d) => { const s = d as { installed: boolean; path: string }; if (s?.installed !== undefined) { setCmInstalled(s.installed); setCmPath(s.path ?? ''); if (s.installed) { setCmProgress(null); setCmError(null) } } }),
      subscribe('rtk:install-progress', (d) => { if (typeof d === 'string') { setRtkProgress(d === 'done' || d === 'error' ? null : d); if (d === 'done') checkRtk().then((r) => { setRtkInstalled(r.installed); setRtkPath(r.path); setRtkVersion(r.version) }).catch(() => {}) } }),
      subscribe('rtk:status', (d) => { const s = d as { installed: boolean; path: string; version: string }; if (s?.installed !== undefined) { setRtkInstalled(s.installed); setRtkPath(s.path ?? ''); setRtkVersion(s.version ?? ''); if (s.installed) { setRtkProgress(null); setRtkError(null) } } }),
    ]
    return () => unsubs.forEach((u) => u())
  }, [])

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
      <InstallerCard icon={<Cpu className="h-5 w-5 text-muted-foreground" />} title="Codebase Memory" desc="High-performance code intelligence" installed={cmInstalled} path={cmPath} progress={cmProgress} error={cmError} onInstall={() => { setCmProgress('downloading'); setCmError(null); installCodebaseMemoryMCP().catch((e) => { setCmError(e instanceof Error ? e.message : String(e)); setCmProgress(null) }) }} onRetry={() => { setCmProgress('downloading'); setCmError(null); installCodebaseMemoryMCP().catch((e) => { setCmError(e instanceof Error ? e.message : String(e)); setCmProgress(null) }) }} />
      <InstallerCard icon={<Terminal className="h-5 w-5 text-muted-foreground" />} title="RTK (Command Optimizer)" desc="Compresses command output (60-90% savings)" installed={rtkInstalled} path={`${rtkPath}${rtkVersion ? ` (${rtkVersion})` : ''}`} progress={rtkProgress} error={rtkError} onInstall={() => { setRtkProgress('downloading'); setRtkError(null); installRtk().catch((e) => { setRtkError(e instanceof Error ? e.message : String(e)); setRtkProgress(null) }) }} onRetry={() => { setRtkProgress('downloading'); setRtkError(null); installRtk().catch((e) => { setRtkError(e instanceof Error ? e.message : String(e)); setRtkProgress(null) }) }} />

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

function InstallerCard({ icon, title, desc, installed, path, progress, error, onInstall, onRetry }: {
  icon: React.ReactNode; title: string; desc: string; installed: boolean; path: string
  progress: string | null; error: string | null; onInstall: () => void; onRetry: () => void
}) {
  return (
    <div className="border rounded-lg p-4">
      <div className="flex items-start gap-3">
        <div className="p-2 rounded-lg bg-muted">{icon}</div>
        <div className="flex-1">
          <div className="flex items-center gap-2 mb-1">
            <span className="font-medium text-sm">{title}</span>
            <Badge variant="secondary" className={installed ? 'text-xs bg-success/10 text-success border-success/20' : 'text-xs bg-warning/10 text-warning border-warning/20'}>{installed ? 'Installed' : 'Not Installed'}</Badge>
          </div>
          <p className="text-xs text-muted-foreground mb-3">{desc}</p>
          {installed ? <p className="text-xs text-muted-foreground font-mono truncate" title={path}>{path}</p>
            : progress ? <Button size="sm" disabled><Loader2 className="h-3 w-3 mr-1 animate-spin" />{progress === 'downloading' ? 'Downloading...' : progress === 'installing' ? 'Installing...' : 'Configuring...'}</Button>
            : error ? <div className="space-y-2"><p className="text-xs text-destructive">{error}</p><Button size="sm" variant="outline" onClick={onRetry}><RefreshCw className="h-3 w-3 mr-1" />Retry</Button></div>
            : <Button size="sm" onClick={onInstall}><Download className="h-3 w-3 mr-1" />Install</Button>}
        </div>
      </div>
    </div>
  )
}
