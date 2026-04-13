import { useState, useEffect, useCallback } from 'react'
import {
  Plus,
  Pencil,
  Trash2,
  RefreshCw,
  ChevronDown,
  ChevronRight,
  Server,
  AlertCircle,
  CheckCircle2,
  X,
  Download,
  Loader2,
  Cpu,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from '@/components/ui/dialog'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  GetMCPStatus,
  GetMCPServers,
  GetToolList,
  UpdateMCPServers,
  CheckCodebaseMemoryMCP,
  InstallCodebaseMemoryMCP,
} from '../../../wailsjs/go/desktop/App'
import { mcp, desktop } from '../../../wailsjs/go/models'

interface MCPServerConfig {
  transport: string
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
}
import { logger } from '@/lib/logger'
import { useWails } from '@/hooks/useWails'

type TransportType = 'stdio' | 'http'

interface ServerFormData {
  name: string
  transport: TransportType
  command: string
  args: string
  env: Record<string, string>
  url: string
  headers: Record<string, string>
}

interface KeyValueEntry {
  key: string
  value: string
}

const emptyFormData: ServerFormData = {
  name: '',
  transport: 'stdio',
  command: '',
  args: '',
  env: {},
  url: '',
  headers: {},
}

export function MCPSettings() {
  const [servers, setServers] = useState<mcp.ServerStatus[]>([])
  const [serverConfigs, setServerConfigs] = useState<Record<string, MCPServerConfig>>({})
  const [tools, setTools] = useState<desktop.ToolInfo[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedServers, setExpandedServers] = useState<Set<string>>(new Set())

  // Form dialog state
  const [isFormOpen, setIsFormOpen] = useState(false)
  const [editingServerName, setEditingServerName] = useState<string | null>(null)
  const [formData, setFormData] = useState<ServerFormData>(emptyFormData)
  const [envEntries, setEnvEntries] = useState<KeyValueEntry[]>([])
  const [headerEntries, setHeaderEntries] = useState<KeyValueEntry[]>([])
  const [formError, setFormError] = useState<string | null>(null)

  // Delete confirmation state
  const [deleteConfirmName, setDeleteConfirmName] = useState<string | null>(null)

  // Codebase Memory state
  const { runtime } = useWails()
  const [cmInstalled, setCmInstalled] = useState(false)
  const [cmPath, setCmPath] = useState('')
  const [installProgress, setInstallProgress] = useState<string | null>(null)
  const [installError, setInstallError] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const [statusResult, toolResult, configsResult] = await Promise.all([
        GetMCPStatus(),
        GetToolList(),
        GetMCPServers(),
      ])
      setServers(statusResult || [])
      setTools(toolResult || [])
      setServerConfigs(configsResult || {})
    } catch (err) {
      logger.error('Failed to load MCP status:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  // Check Codebase Memory status on mount
  useEffect(() => {
    const checkStatus = async () => {
      try {
        const result = await CheckCodebaseMemoryMCP()
        setCmInstalled(result.installed)
        setCmPath(result.path)
      } catch (err) {
        logger.error('Failed to check Codebase Memory status:', err)
      }
    }
    checkStatus()
  }, [])

  // Listen for Codebase Memory events
  useEffect(() => {
    if (!runtime) return

    const unsubProgress = runtime.EventsOn('codememory:install-progress', (data: unknown) => {
      const progress = data as string
      setInstallProgress(progress)
      if (progress === 'done') {
        setInstallProgress(null)
      } else if (progress === 'error') {
        setInstallProgress(null)
      }
    })

    const unsubStatus = runtime.EventsOn('codememory:status', (data: unknown) => {
      const status = data as { installed: boolean; path: string }
      setCmInstalled(status.installed)
      setCmPath(status.path)
      if (status.installed) {
        setInstallProgress(null)
        setInstallError(null)
      }
    })

    return () => {
      unsubProgress()
      unsubStatus()
    }
  }, [runtime])

  const handleInstallCodebaseMemory = async () => {
    setInstallProgress('downloading')
    setInstallError(null)
    try {
      await InstallCodebaseMemoryMCP()
    } catch (err) {
      setInstallError(err instanceof Error ? err.message : String(err))
      setInstallProgress(null)
    }
  }

  const toggleExpanded = (serverName: string) => {
    setExpandedServers((prev) => {
      const next = new Set(prev)
      if (next.has(serverName)) {
        next.delete(serverName)
      } else {
        next.add(serverName)
      }
      return next
    })
  }

  const openAddForm = () => {
    setEditingServerName(null)
    setFormData(emptyFormData)
    setEnvEntries([])
    setHeaderEntries([])
    setFormError(null)
    setIsFormOpen(true)
  }

  const openEditForm = (server: mcp.ServerStatus) => {
    setEditingServerName(server.name)
    // Load config from serverConfigs state
    const cfg = serverConfigs[server.name]
    const isStdio = server.transport === 'stdio'
    setFormData({
      name: server.name,
      transport: isStdio ? 'stdio' : 'http',
      command: cfg?.command || '',
      args: cfg?.args?.join(', ') || '',
      env: cfg?.env || {},
      url: cfg?.url || '',
      headers: cfg?.headers || {},
    })
    // Convert env and headers to entries
    setEnvEntries(
      Object.entries(cfg?.env || {}).map(([key, value]) => ({ key, value: String(value) }))
    )
    setHeaderEntries(
      Object.entries(cfg?.headers || {}).map(([key, value]) => ({ key, value: String(value) }))
    )
    setFormError(null)
    setIsFormOpen(true)
  }

  const handleDelete = async (serverName: string) => {
    setIsSaving(true)
    try {
      // Build new server map without the deleted server
      const newServers = { ...serverConfigs }
      delete newServers[serverName]
      await UpdateMCPServers(newServers)
      setDeleteConfirmName(null)
      setServerConfigs(newServers)
      await loadData()
    } catch (err) {
      logger.error('Failed to delete server:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setIsSaving(false)
    }
  }

  const handleSave = async () => {
    setFormError(null)

    // Validate
    if (!formData.name.trim()) {
      setFormError('Server name is required')
      return
    }

    if (formData.name.includes(' ') || formData.name.includes('.')) {
      setFormError('Server name cannot contain spaces or dots')
      return
    }

    // Check for duplicate name on add
    if (!editingServerName && servers.some((s) => s.name === formData.name)) {
      setFormError('A server with this name already exists')
      return
    }

    // Build env and headers from entries
    const env: Record<string, string> = {}
    envEntries.forEach((e) => {
      if (e.key.trim()) {
        env[e.key.trim()] = e.value
      }
    })

    const headers: Record<string, string> = {}
    headerEntries.forEach((e) => {
      if (e.key.trim()) {
        headers[e.key.trim()] = e.value
      }
    })

    // Build new config
    const newConfig: MCPServerConfig = {
      transport: formData.transport,
      command: formData.transport === 'stdio' ? formData.command : '',
      args:
        formData.transport === 'stdio'
          ? formData.args
              .split(',')
              .map((a) => a.trim())
              .filter((a) => a)
          : [],
      env: formData.transport === 'stdio' ? env : {},
      url: formData.transport === 'http' ? formData.url : '',
      headers: formData.transport === 'http' ? headers : {},
    }

    setIsSaving(true)
    try {
      // Build complete server map from existing configs
      const newServers = { ...serverConfigs }

      // Remove old name if editing (in case name changed)
      if (editingServerName) {
        delete newServers[editingServerName]
      }

      // Add/update the edited server
      newServers[formData.name] = newConfig

      await UpdateMCPServers(newServers)
      setIsFormOpen(false)
      setServerConfigs(newServers)
      await loadData()
    } catch (err) {
      logger.error('Failed to save server:', err)
      setFormError(err instanceof Error ? err.message : String(err))
    } finally {
      setIsSaving(false)
    }
  }

  const addEnvEntry = () => {
    setEnvEntries([...envEntries, { key: '', value: '' }])
  }

  const updateEnvEntry = (index: number, field: 'key' | 'value', value: string) => {
    const updated = [...envEntries]
    const entry = updated[index]
    if (entry) {
      entry[field] = value
      setEnvEntries(updated)
    }
  }

  const removeEnvEntry = (index: number) => {
    setEnvEntries(envEntries.filter((_, i) => i !== index))
  }

  const addHeaderEntry = () => {
    setHeaderEntries([...headerEntries, { key: '', value: '' }])
  }

  const updateHeaderEntry = (index: number, field: 'key' | 'value', value: string) => {
    const updated = [...headerEntries]
    const entry = updated[index]
    if (entry) {
      entry[field] = value
      setHeaderEntries(updated)
    }
  }

  const removeHeaderEntry = (index: number) => {
    setHeaderEntries(headerEntries.filter((_, i) => i !== index))
  }

  const getToolsForServer = (serverName: string): desktop.ToolInfo[] => {
    return tools.filter((t) => t.source === serverName)
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading MCP settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Codebase Memory Section */}
      <div className="border rounded-lg p-4">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-lg bg-muted">
            <Cpu className="h-5 w-5 text-muted-foreground" />
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2 mb-1">
              <span className="font-medium text-sm">Codebase Memory</span>
              {cmInstalled ? (
                <Badge variant="secondary" className="text-xs bg-green-500/10 text-green-600 border-green-500/20">
                  Installed
                </Badge>
              ) : (
                <Badge variant="secondary" className="text-xs bg-orange-500/10 text-orange-600 border-orange-500/20">
                  Not Installed
                </Badge>
              )}
            </div>
            <p className="text-xs text-muted-foreground mb-3">
              High-performance code intelligence for your projects
            </p>
            {cmInstalled ? (
              <p className="text-xs text-muted-foreground font-mono truncate" title={cmPath}>
                {cmPath}
              </p>
            ) : installProgress ? (
              <div className="flex items-center gap-2">
                <Button size="sm" disabled>
                  <Loader2 className="h-3 w-3 mr-1 animate-spin" />
                  {installProgress === 'downloading' && 'Downloading...'}
                  {installProgress === 'installing' && 'Installing...'}
                  {installProgress === 'configuring' && 'Configuring...'}
                </Button>
              </div>
            ) : installError ? (
              <div className="space-y-2">
                <p className="text-xs text-destructive">{installError}</p>
                <Button size="sm" variant="outline" onClick={handleInstallCodebaseMemory}>
                  <RefreshCw className="h-3 w-3 mr-1" />
                  Retry
                </Button>
              </div>
            ) : (
              <Button size="sm" onClick={handleInstallCodebaseMemory}>
                <Download className="h-3 w-3 mr-1" />
                Install
              </Button>
            )}
          </div>
        </div>
      </div>

      {/* Header with refresh button */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Server className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">MCP Servers</span>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={loadData} disabled={isLoading}>
            <RefreshCw className="h-3 w-3" />
          </Button>
          <Button variant="default" size="sm" onClick={openAddForm}>
            <Plus className="h-3 w-3 mr-1" />
            Add Server
          </Button>
        </div>
      </div>

      {/* Error display */}
      {error && (
        <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
          <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <p>{error}</p>
        </div>
      )}

      {/* Server list */}
      {servers.length === 0 ? (
        <div className="text-sm text-muted-foreground py-4 text-center">
          No MCP servers configured. Click "Add Server" to add one.
        </div>
      ) : (
        <div className="space-y-2">
          {servers.map((server) => {
            const isExpanded = expandedServers.has(server.name)
            const serverTools = getToolsForServer(server.name)

            return (
              <Collapsible
                key={server.name}
                open={isExpanded}
                onOpenChange={() => toggleExpanded(server.name)}
              >
                <div className="border rounded-lg overflow-hidden">
                  {/* Server row */}
                  <CollapsibleTrigger asChild>
                    <div className="flex items-center gap-3 p-3 cursor-pointer hover:bg-muted/50 transition-colors">
                      {isExpanded ? (
                        <ChevronDown className="h-4 w-4 text-muted-foreground" />
                      ) : (
                        <ChevronRight className="h-4 w-4 text-muted-foreground" />
                      )}

                      {/* Status dot */}
                      {server.connected ? (
                        <CheckCircle2 className="h-4 w-4 text-green-500" />
                      ) : (
                        <AlertCircle className="h-4 w-4 text-red-500" />
                      )}

                      {/* Server name */}
                      <span className="font-medium text-sm flex-1">{server.name}</span>

                      {/* Transport badge */}
                      <Badge variant="secondary" className="text-xs">
                        {server.transport}
                      </Badge>

                      {/* Tool count */}
                      <span className="text-xs text-muted-foreground">
                        {server.tool_count} tools
                      </span>
                    </div>
                  </CollapsibleTrigger>

                  {/* Expanded content */}
                  <CollapsibleContent>
                    <div className="px-3 pb-3 pt-0 space-y-3 border-t">
                      {/* Error message */}
                      {server.error && (
                        <div className="mt-3 flex items-start gap-2 p-2 rounded bg-destructive/10 text-xs">
                          <AlertCircle className="h-3 w-3 text-destructive flex-shrink-0 mt-0.5" />
                          <code className="text-destructive break-all">{server.error}</code>
                        </div>
                      )}

                      {/* Tools list */}
                      {serverTools.length > 0 && (
                        <div className="mt-3">
                          <p className="text-xs text-muted-foreground mb-1">Discovered tools:</p>
                          <div className="flex flex-wrap gap-1">
                            {serverTools.map((tool) => (
                              <Badge key={tool.name} variant="outline" className="text-xs">
                                {tool.name}
                              </Badge>
                            ))}
                          </div>
                        </div>
                      )}

                      {/* Actions */}
                      <div className="flex gap-2 pt-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            openEditForm(server)
                          }}
                        >
                          <Pencil className="h-3 w-3 mr-1" />
                          Edit
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-destructive hover:bg-destructive/10"
                          onClick={(e) => {
                            e.stopPropagation()
                            setDeleteConfirmName(server.name)
                          }}
                        >
                          <Trash2 className="h-3 w-3 mr-1" />
                          Delete
                        </Button>
                      </div>
                    </div>
                  </CollapsibleContent>
                </div>
              </Collapsible>
            )
          })}
        </div>
      )}

      {/* Add/Edit Dialog */}
      <Dialog open={isFormOpen} onOpenChange={setIsFormOpen}>
        <DialogContent className="sm:max-w-[500px]">
          <DialogHeader>
            <DialogTitle>
              {editingServerName ? 'Edit MCP Server' : 'Add MCP Server'}
            </DialogTitle>
            <DialogDescription>
              Configure an MCP server connection. Choose transport type and provide the required
              configuration.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            {formError && (
              <div className="flex items-start gap-2 p-2 rounded bg-destructive/10 text-sm">
                <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
                <p className="text-destructive">{formError}</p>
              </div>
            )}

            {/* Name field */}
            <div className="space-y-2">
              <label className="text-xs text-muted-foreground">Server Name</label>
              <Input
                placeholder="my-mcp-server"
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                disabled={!!editingServerName}
                className="h-9"
              />
              <p className="text-xs text-muted-foreground">
                Unique identifier for this server (no spaces or dots)
              </p>
            </div>

            {/* Transport picker */}
            <div className="space-y-2">
              <label className="text-xs text-muted-foreground">Transport Type</label>
              <div className="flex gap-2 p-1 bg-muted rounded-lg">
                <Button
                  variant={formData.transport === 'stdio' ? 'secondary' : 'ghost'}
                  size="sm"
                  className="flex-1"
                  onClick={() => setFormData({ ...formData, transport: 'stdio' })}
                >
                  stdio
                </Button>
                <Button
                  variant={formData.transport === 'http' ? 'secondary' : 'ghost'}
                  size="sm"
                  className="flex-1"
                  onClick={() => setFormData({ ...formData, transport: 'http' })}
                >
                  http
                </Button>
              </div>
            </div>

            {/* stdio fields */}
            {formData.transport === 'stdio' && (
              <>
                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Command</label>
                  <Input
                    placeholder="/usr/local/bin/mcp-server"
                    value={formData.command}
                    onChange={(e) => setFormData({ ...formData, command: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Arguments (comma-separated)</label>
                  <Input
                    placeholder="--port, 8080, --verbose"
                    value={formData.args}
                    onChange={(e) => setFormData({ ...formData, args: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Environment Variables</label>
                  {envEntries.map((entry, index) => (
                    <div key={index} className="flex gap-2">
                      <Input
                        placeholder="KEY"
                        value={entry.key}
                        onChange={(e) => updateEnvEntry(index, 'key', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Input
                        placeholder="value"
                        value={entry.value}
                        onChange={(e) => updateEnvEntry(index, 'value', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0"
                        onClick={() => removeEnvEntry(index)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ))}
                  <Button variant="outline" size="sm" onClick={addEnvEntry}>
                    <Plus className="h-3 w-3 mr-1" />
                    Add Variable
                  </Button>
                </div>
              </>
            )}

            {/* http fields */}
            {formData.transport === 'http' && (
              <>
                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">URL</label>
                  <Input
                    placeholder="http://localhost:8080/mcp"
                    value={formData.url}
                    onChange={(e) => setFormData({ ...formData, url: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">
                    Headers (use {'${VAR}'} for env vars)
                  </label>
                  {headerEntries.map((entry, index) => (
                    <div key={index} className="flex gap-2">
                      <Input
                        placeholder="Authorization"
                        value={entry.key}
                        onChange={(e) => updateHeaderEntry(index, 'key', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Input
                        placeholder="Bearer ${API_KEY}"
                        value={entry.value}
                        onChange={(e) => updateHeaderEntry(index, 'value', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0"
                        onClick={() => removeHeaderEntry(index)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ))}
                  <Button variant="outline" size="sm" onClick={addHeaderEntry}>
                    <Plus className="h-3 w-3 mr-1" />
                    Add Header
                  </Button>
                </div>
              </>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setIsFormOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={isSaving}>
              {isSaving ? 'Saving...' : 'Save'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog
        open={!!deleteConfirmName}
        onOpenChange={(open) => !open && setDeleteConfirmName(null)}
      >
        <DialogContent className="sm:max-w-[400px]">
          <DialogHeader>
            <DialogTitle>Delete Server</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete "{deleteConfirmName}"? This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteConfirmName(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => deleteConfirmName && handleDelete(deleteConfirmName)}
              disabled={isSaving}
            >
              {isSaving ? 'Deleting...' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
