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
  Terminal,
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
  CheckRtk,
  InstallRtk,
} from '../../../wailsjs/go/desktop/App'
import { mcp, backend } from '../../../wailsjs/go/models'

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
  id: number
  key: string
  value: string
}

let nextEntryId = 1
function makeEntry(key = '', value = ''): KeyValueEntry {
  return { id: nextEntryId++, key, value }
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

// ---------- useMCPServers hook ----------

interface UseMCPServersReturn {
  servers: mcp.ServerStatus[]
  serverConfigs: Record<string, MCPServerConfig>
  tools: backend.ToolInfo[]
  isLoading: boolean
  isSaving: boolean
  error: string | null
  expandedServers: Set<string>
  setError: (error: string | null) => void
  setIsSaving: (saving: boolean) => void
  setServerConfigs: (configs: Record<string, MCPServerConfig>) => void
  loadData: () => Promise<void>
  toggleExpanded: (serverName: string) => void
  handleSave: (
    formData: ServerFormData,
    editingServerName: string | null,
    envEntries: KeyValueEntry[],
    headerEntries: KeyValueEntry[],
  ) => Promise<string | null>
  handleDelete: (serverName: string) => Promise<void>
  getToolsForServer: (serverName: string) => backend.ToolInfo[]
}

function useMCPServers(): UseMCPServersReturn {
  const [servers, setServers] = useState<mcp.ServerStatus[]>([])
  const [serverConfigs, setServerConfigs] = useState<Record<string, MCPServerConfig>>({})
  const [tools, setTools] = useState<backend.ToolInfo[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedServers, setExpandedServers] = useState<Set<string>>(new Set())

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

  const handleDelete = async (serverName: string) => {
    setIsSaving(true)
    try {
      const newServers = { ...serverConfigs }
      delete newServers[serverName]
      await UpdateMCPServers(newServers)
      setServerConfigs(newServers)
      await loadData()
    } catch (err) {
      logger.error('Failed to delete server:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setIsSaving(false)
    }
  }

  const handleSave = async (
    formData: ServerFormData,
    editingServerName: string | null,
    envEntries: KeyValueEntry[],
    headerEntries: KeyValueEntry[],
  ): Promise<string | null> => {
    // Validate
    if (!formData.name.trim()) {
      return 'Server name is required'
    }

    if (formData.name.includes(' ') || formData.name.includes('.')) {
      return 'Server name cannot contain spaces or dots'
    }

    // Check for duplicate name on add
    if (!editingServerName && servers.some((s) => s.name === formData.name)) {
      return 'A server with this name already exists'
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
      const newServers = { ...serverConfigs }
      if (editingServerName) {
        delete newServers[editingServerName]
      }
      newServers[formData.name] = newConfig
      await UpdateMCPServers(newServers)
      setServerConfigs(newServers)
      await loadData()
      return null // success
    } catch (err) {
      logger.error('Failed to save server:', err)
      return err instanceof Error ? err.message : String(err)
    } finally {
      setIsSaving(false)
    }
  }

  const getToolsForServer = (serverName: string): backend.ToolInfo[] => {
    return tools.filter((t) => t.source === serverName)
  }

  return {
    servers,
    serverConfigs,
    tools,
    isLoading,
    isSaving,
    error,
    expandedServers,
    setError,
    setIsSaving,
    setServerConfigs,
    loadData,
    toggleExpanded,
    handleSave,
    handleDelete,
    getToolsForServer,
  }
}

// ---------- useMCPForm hook ----------

interface UseMCPFormReturn {
  isFormOpen: boolean
  editingServerName: string | null
  formData: ServerFormData
  envEntries: KeyValueEntry[]
  headerEntries: KeyValueEntry[]
  formError: string | null
  deleteConfirmName: string | null
  setFormError: (error: string | null) => void
  setDeleteConfirmName: (name: string | null) => void
  setIsFormOpen: (open: boolean) => void
  openAddForm: () => void
  openEditForm: (server: mcp.ServerStatus, serverConfigs: Record<string, MCPServerConfig>) => void
  closeForm: () => void
  setFormData: (data: ServerFormData) => void
  addEnvEntry: () => void
  updateEnvEntry: (index: number, field: 'key' | 'value', value: string) => void
  removeEnvEntry: (index: number) => void
  addHeaderEntry: () => void
  updateHeaderEntry: (index: number, field: 'key' | 'value', value: string) => void
  removeHeaderEntry: (index: number) => void
}

function useMCPForm(): UseMCPFormReturn {
  const [isFormOpen, setIsFormOpen] = useState(false)
  const [editingServerName, setEditingServerName] = useState<string | null>(null)
  const [formData, setFormData] = useState<ServerFormData>(emptyFormData)
  const [envEntries, setEnvEntries] = useState<KeyValueEntry[]>([])
  const [headerEntries, setHeaderEntries] = useState<KeyValueEntry[]>([])
  const [formError, setFormError] = useState<string | null>(null)
  const [deleteConfirmName, setDeleteConfirmName] = useState<string | null>(null)

  const openAddForm = () => {
    setEditingServerName(null)
    setFormData(emptyFormData)
    setEnvEntries([])
    setHeaderEntries([])
    setFormError(null)
    setIsFormOpen(true)
  }

  const openEditForm = (server: mcp.ServerStatus, serverConfigs: Record<string, MCPServerConfig>) => {
    setEditingServerName(server.name)
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
    setEnvEntries(
      Object.entries(cfg?.env || {}).map(([key, value]) => makeEntry(key, String(value)))
    )
    setHeaderEntries(
      Object.entries(cfg?.headers || {}).map(([key, value]) => makeEntry(key, String(value)))
    )
    setFormError(null)
    setIsFormOpen(true)
  }

  const closeForm = () => {
    setIsFormOpen(false)
  }

  const addEnvEntry = () => {
    setEnvEntries([...envEntries, makeEntry()])
  }

  const updateEnvEntry = (index: number, field: 'key' | 'value', value: string) => {
    setEnvEntries(envEntries.map((e, i) => i === index ? { ...e, [field]: value } : e))
  }

  const removeEnvEntry = (index: number) => {
    setEnvEntries(envEntries.filter((_, i) => i !== index))
  }

  const addHeaderEntry = () => {
    setHeaderEntries([...headerEntries, makeEntry()])
  }

  const updateHeaderEntry = (index: number, field: 'key' | 'value', value: string) => {
    setHeaderEntries(headerEntries.map((e, i) => i === index ? { ...e, [field]: value } : e))
  }

  const removeHeaderEntry = (index: number) => {
    setHeaderEntries(headerEntries.filter((_, i) => i !== index))
  }

  return {
    isFormOpen,
    editingServerName,
    formData,
    envEntries,
    headerEntries,
    formError,
    deleteConfirmName,
    setFormError,
    setDeleteConfirmName,
    setIsFormOpen,
    openAddForm,
    openEditForm,
    closeForm,
    setFormData,
    addEnvEntry,
    updateEnvEntry,
    removeEnvEntry,
    addHeaderEntry,
    updateHeaderEntry,
    removeHeaderEntry,
  }
}

// ---------- MCPSettings component ----------

export function MCPSettings() {
  const mcpServers = useMCPServers()
  const mcpForm = useMCPForm()

  // Codebase Memory state
  const { runtime } = useWails()
  const [cmInstalled, setCmInstalled] = useState(false)
  const [cmPath, setCmPath] = useState('')
  const [installProgress, setInstallProgress] = useState<string | null>(null)
  const [installError, setInstallError] = useState<string | null>(null)

  // RTK state
  const [rtkInstalled, setRtkInstalled] = useState(false)
  const [rtkPath, setRtkPath] = useState('')
  const [rtkVersion, setRtkVersion] = useState('')
  const [rtkInstallProgress, setRtkInstallProgress] = useState<string | null>(null)
  const [rtkInstallError, setRtkInstallError] = useState<string | null>(null)

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

  // Check RTK status on mount
  useEffect(() => {
    const checkRtkStatus = async () => {
      try {
        const result = await CheckRtk()
        setRtkInstalled(result.installed)
        setRtkPath(result.path)
        setRtkVersion(result.version)
      } catch (err) {
        logger.error('Failed to check RTK status:', err)
      }
    }
    checkRtkStatus()
  }, [])

  // Listen for Codebase Memory events
  useEffect(() => {
    if (!runtime) return

    const unsubProgress = runtime.EventsOn('codememory:install-progress', (data: unknown) => {
      if (typeof data !== 'string') return
      setInstallProgress(data)
      if (data === 'done' || data === 'error') {
        setInstallProgress(null)
      }
    })

    const unsubStatus = runtime.EventsOn('codememory:status', (data: unknown) => {
      if (typeof data !== 'object' || data === null || typeof (data as Record<string, unknown>).installed !== 'boolean') return
      const status = data as { installed: boolean; path: string }
      setCmInstalled(status.installed)
      setCmPath(status.path ?? '')
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

  // Listen for RTK events
  useEffect(() => {
    if (!runtime) return

    const unsubProgress = runtime.EventsOn('rtk:install-progress', (data: unknown) => {
      if (typeof data !== 'string') return
      setRtkInstallProgress(data)
      if (data === 'done') {
        setRtkInstallProgress(null)
        // Re-check status after install
        CheckRtk().then((result) => {
          setRtkInstalled(result.installed)
          setRtkPath(result.path)
          setRtkVersion(result.version)
        }).catch((err) => { logger.warn('Failed to re-check RTK status:', err) })
      } else if (data === 'error') {
        setRtkInstallProgress(null)
      }
    })

    const unsubStatus = runtime.EventsOn('rtk:status', (data: unknown) => {
      if (typeof data !== 'object' || data === null || typeof (data as Record<string, unknown>).installed !== 'boolean') return
      const status = data as { installed: boolean; path: string; version: string }
      setRtkInstalled(status.installed)
      setRtkPath(status.path ?? '')
      setRtkVersion(status.version ?? '')
      if (status.installed) {
        setRtkInstallProgress(null)
        setRtkInstallError(null)
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

  const handleInstallRtk = async () => {
    setRtkInstallProgress('downloading')
    setRtkInstallError(null)
    try {
      await InstallRtk()
    } catch (err) {
      setRtkInstallError(err instanceof Error ? err.message : String(err))
      setRtkInstallProgress(null)
    }
  }

  const handleSaveForm = async () => {
    mcpForm.setFormError(null)
    const err = await mcpServers.handleSave(
      mcpForm.formData,
      mcpForm.editingServerName,
      mcpForm.envEntries,
      mcpForm.headerEntries,
    )
    if (err) {
      mcpForm.setFormError(err)
    } else {
      mcpForm.closeForm()
    }
  }

  const handleDeleteConfirm = async (serverName: string) => {
    await mcpServers.handleDelete(serverName)
    mcpForm.setDeleteConfirmName(null)
  }

  if (mcpServers.isLoading) {
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
                <Badge variant="secondary" className="text-xs bg-success/10 text-success border-success/20">
                  Installed
                </Badge>
              ) : (
                <Badge variant="secondary" className="text-xs bg-warning/10 text-warning border-warning/20">
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

      {/* RTK Section */}
      <div className="border rounded-lg p-4">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-lg bg-muted">
            <Terminal className="h-5 w-5 text-muted-foreground" />
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2 mb-1">
              <span className="font-medium text-sm">RTK (Command Optimizer)</span>
              {rtkInstalled ? (
                <Badge variant="secondary" className="text-xs bg-success/10 text-success border-success/20">
                  Installed
                </Badge>
              ) : (
                <Badge variant="secondary" className="text-xs bg-warning/10 text-warning border-warning/20">
                  Not Installed
                </Badge>
              )}
            </div>
            <p className="text-xs text-muted-foreground mb-3">
              Compresses command output for reduced token usage (60-90% savings)
            </p>
            {rtkInstalled ? (
              <p className="text-xs text-muted-foreground font-mono truncate" title={rtkPath}>
                {rtkPath}{rtkVersion ? ` (${rtkVersion})` : ''}
              </p>
            ) : rtkInstallProgress ? (
              <div className="flex items-center gap-2">
                <Button size="sm" disabled>
                  <Loader2 className="h-3 w-3 mr-1 animate-spin" />
                  {rtkInstallProgress === 'downloading' && 'Downloading...'}
                  {rtkInstallProgress === 'installing' && 'Installing...'}
                </Button>
              </div>
            ) : rtkInstallError ? (
              <div className="space-y-2">
                <p className="text-xs text-destructive">{rtkInstallError}</p>
                <Button size="sm" variant="outline" onClick={handleInstallRtk}>
                  <RefreshCw className="h-3 w-3 mr-1" />
                  Retry
                </Button>
              </div>
            ) : (
              <Button size="sm" onClick={handleInstallRtk}>
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
          <Button variant="outline" size="sm" onClick={mcpServers.loadData} disabled={mcpServers.isLoading}>
            <RefreshCw className="h-3 w-3" />
          </Button>
          <Button variant="default" size="sm" onClick={mcpForm.openAddForm}>
            <Plus className="h-3 w-3 mr-1" />
            Add Server
          </Button>
        </div>
      </div>

      {/* Error display */}
      {mcpServers.error && (
        <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
          <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <p>{mcpServers.error}</p>
        </div>
      )}

      {/* Server list */}
      {mcpServers.servers.length === 0 ? (
        <div className="text-sm text-muted-foreground py-4 text-center">
          No MCP servers configured. Click "Add Server" to add one.
        </div>
      ) : (
        <div className="space-y-2">
          {mcpServers.servers.map((server) => {
            const isExpanded = mcpServers.expandedServers.has(server.name)
            const serverTools = mcpServers.getToolsForServer(server.name)

            return (
              <Collapsible
                key={server.name}
                open={isExpanded}
                onOpenChange={() => mcpServers.toggleExpanded(server.name)}
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
                        <CheckCircle2 className="h-4 w-4 text-success" />
                      ) : (
                        <AlertCircle className="h-4 w-4 text-destructive" />
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
                            mcpForm.openEditForm(server, mcpServers.serverConfigs)
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
                            mcpForm.setDeleteConfirmName(server.name)
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
      <Dialog open={mcpForm.isFormOpen} onOpenChange={mcpForm.setIsFormOpen}>
        <DialogContent className="sm:max-w-[500px]">
          <DialogHeader>
            <DialogTitle>
              {mcpForm.editingServerName ? 'Edit MCP Server' : 'Add MCP Server'}
            </DialogTitle>
            <DialogDescription>
              Configure an MCP server connection. Choose transport type and provide the required
              configuration.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            {mcpForm.formError && (
              <div className="flex items-start gap-2 p-2 rounded bg-destructive/10 text-sm">
                <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
                <p className="text-destructive">{mcpForm.formError}</p>
              </div>
            )}

            {/* Name field */}
            <div className="space-y-2">
              <label className="text-xs text-muted-foreground">Server Name</label>
              <Input
                placeholder="my-mcp-server"
                value={mcpForm.formData.name}
                onChange={(e) => mcpForm.setFormData({ ...mcpForm.formData, name: e.target.value })}
                disabled={!!mcpForm.editingServerName}
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
                  variant={mcpForm.formData.transport === 'stdio' ? 'secondary' : 'ghost'}
                  size="sm"
                  className="flex-1"
                  onClick={() => mcpForm.setFormData({ ...mcpForm.formData, transport: 'stdio' })}
                >
                  stdio
                </Button>
                <Button
                  variant={mcpForm.formData.transport === 'http' ? 'secondary' : 'ghost'}
                  size="sm"
                  className="flex-1"
                  onClick={() => mcpForm.setFormData({ ...mcpForm.formData, transport: 'http' })}
                >
                  http
                </Button>
              </div>
            </div>

            {/* stdio fields */}
            {mcpForm.formData.transport === 'stdio' && (
              <>
                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Command</label>
                  <Input
                    placeholder="/usr/local/bin/mcp-server"
                    value={mcpForm.formData.command}
                    onChange={(e) => mcpForm.setFormData({ ...mcpForm.formData, command: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Arguments (comma-separated)</label>
                  <Input
                    placeholder="--port, 8080, --verbose"
                    value={mcpForm.formData.args}
                    onChange={(e) => mcpForm.setFormData({ ...mcpForm.formData, args: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">Environment Variables</label>
                  {mcpForm.envEntries.map((entry, index) => (
                    <div key={entry.id} className="flex gap-2">
                      <Input
                        placeholder="KEY"
                        value={entry.key}
                        onChange={(e) => mcpForm.updateEnvEntry(index, 'key', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Input
                        placeholder="value"
                        value={entry.value}
                        onChange={(e) => mcpForm.updateEnvEntry(index, 'value', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0"
                        onClick={() => mcpForm.removeEnvEntry(index)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ))}
                  <Button variant="outline" size="sm" onClick={mcpForm.addEnvEntry}>
                    <Plus className="h-3 w-3 mr-1" />
                    Add Variable
                  </Button>
                </div>
              </>
            )}

            {/* http fields */}
            {mcpForm.formData.transport === 'http' && (
              <>
                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">URL</label>
                  <Input
                    placeholder="http://localhost:8080/mcp"
                    value={mcpForm.formData.url}
                    onChange={(e) => mcpForm.setFormData({ ...mcpForm.formData, url: e.target.value })}
                    className="h-9 font-mono text-sm"
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-xs text-muted-foreground">
                    Headers (use {'${VAR}'} for env vars)
                  </label>
                  {mcpForm.headerEntries.map((entry, index) => (
                    <div key={entry.id} className="flex gap-2">
                      <Input
                        placeholder="Authorization"
                        value={entry.key}
                        onChange={(e) => mcpForm.updateHeaderEntry(index, 'key', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Input
                        placeholder="Bearer ${API_KEY}"
                        value={entry.value}
                        onChange={(e) => mcpForm.updateHeaderEntry(index, 'value', e.target.value)}
                        className="h-8 font-mono text-xs flex-1"
                      />
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0"
                        onClick={() => mcpForm.removeHeaderEntry(index)}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </div>
                  ))}
                  <Button variant="outline" size="sm" onClick={mcpForm.addHeaderEntry}>
                    <Plus className="h-3 w-3 mr-1" />
                    Add Header
                  </Button>
                </div>
              </>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={mcpForm.closeForm}>
              Cancel
            </Button>
            <Button onClick={handleSaveForm} disabled={mcpServers.isSaving}>
              {mcpServers.isSaving ? 'Saving...' : 'Save'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog
        open={!!mcpForm.deleteConfirmName}
        onOpenChange={(open) => !open && mcpForm.setDeleteConfirmName(null)}
      >
        <DialogContent className="sm:max-w-[400px]">
          <DialogHeader>
            <DialogTitle>Delete Server</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete "{mcpForm.deleteConfirmName}"? This action cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => mcpForm.setDeleteConfirmName(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => mcpForm.deleteConfirmName && handleDeleteConfirm(mcpForm.deleteConfirmName)}
              disabled={mcpServers.isSaving}
            >
              {mcpServers.isSaving ? 'Deleting...' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
