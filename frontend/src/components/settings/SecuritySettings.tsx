import { useState, useEffect, type KeyboardEvent } from 'react'
import { Info, Plus, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { GetSecuritySettings, UpdateSecuritySettings, GetToolList } from '../../../wailsjs/go/desktop/App'
import { backend } from '../../../wailsjs/go/models'
import { logger } from '@/lib/logger'

type ToolPolicy = 'always_allow' | 'always_deny' | 'user_confirm'

interface ToolPolicyData {
  policy: ToolPolicy
  blacklist?: string[]
}

interface SecuritySettings {
  default_policy: ToolPolicy
  tool_policies: Record<string, ToolPolicyData>
}

interface PolicyOption {
  value: ToolPolicy
  label: string
}

interface ToolInfo {
  name: string
  description: string
  source: string
  policy: string
}

interface GroupedTools {
  [source: string]: ToolInfo[]
}

const policyOptions: PolicyOption[] = [
  { value: 'always_allow', label: 'Always Allow' },
  { value: 'always_deny', label: 'Always Deny' },
  { value: 'user_confirm', label: 'User Confirm' },
]

// Tools that support blacklist functionality
const BLACKLIST_ENABLED_TOOLS = ['bash_exec']

// Internal system tools that are always allowed and should not appear in UI
const INTERNAL_TOOLS = new Set([
  'ask_user',
  'finish',
  'list_step_outputs',
  'read_step_output',
])

function getGroupLabel(source: string): string {
  if (source === 'core') {
    return 'Built-in Tools'
  }
  return `MCP: ${source}`
}

export function SecuritySettings() {
  const [settings, setSettings] = useState<SecuritySettings>({
    default_policy: 'user_confirm',
    tool_policies: {},
  })
  const [tools, setTools] = useState<ToolInfo[]>([])
  const [newBlacklistPattern, setNewBlacklistPattern] = useState('')
  const [isLoading, setIsLoading] = useState(true)
  const [isToolsLoading, setIsToolsLoading] = useState(true)

  useEffect(() => {
    const loadSettings = async () => {
      try {
        const result = await GetSecuritySettings()
        if (!result || typeof result !== 'object') {
          throw new Error('Invalid security settings response')
        }
        setSettings(result as SecuritySettings)
      } catch (error) {
        logger.error('Failed to load security settings:', error)
      } finally {
        setIsLoading(false)
      }
    }
    loadSettings()
  }, [])

  useEffect(() => {
    const loadTools = async () => {
      try {
        const result = await GetToolList()
        if (!result || !Array.isArray(result)) {
          throw new Error('Invalid tool list response')
        }
        // Filter out internal tools that should not appear in UI
        const filteredTools = (result as ToolInfo[]).filter(
          (tool) => !INTERNAL_TOOLS.has(tool.name)
        )
        setTools(filteredTools)
      } catch (error) {
        logger.error('Failed to load tools:', error)
      } finally {
        setIsToolsLoading(false)
      }
    }
    loadTools()
  }, [])

  const updateSettings = async (newSettings: SecuritySettings) => {
    setSettings(newSettings)
    try {
      const toolPolicies: Record<string, backend.ToolPolicyResponse> = {}
      for (const [name, data] of Object.entries(newSettings.tool_policies)) {
        toolPolicies[name] = new backend.ToolPolicyResponse({
          policy: data.policy,
          blacklist: data.blacklist,
        })
      }
      const request = new backend.SecuritySettingsResponse({
        default_policy: newSettings.default_policy,
        tool_policies: toolPolicies,
      })
      await UpdateSecuritySettings(request)
    } catch (error) {
      logger.error('Failed to update security settings:', error)
    }
  }

  const handlePolicyChange = (toolId: string, policy: ToolPolicy) => {
    const newSettings: SecuritySettings = {
      ...settings,
      tool_policies: {
        ...settings.tool_policies,
        [toolId]: {
          ...settings.tool_policies[toolId],
          policy,
        },
      },
    }
    updateSettings(newSettings)
  }

  const handleAddBlacklistPattern = () => {
    if (!newBlacklistPattern.trim()) return

    const currentBlacklist = settings.tool_policies['bash_exec']?.blacklist || []
    if (currentBlacklist.includes(newBlacklistPattern.trim())) {
      setNewBlacklistPattern('')
      return
    }

    const newSettings: SecuritySettings = {
      ...settings,
      tool_policies: {
        ...settings.tool_policies,
        bash_exec: {
          ...settings.tool_policies['bash_exec'],
          policy: settings.tool_policies['bash_exec']?.policy || 'user_confirm',
          blacklist: [...currentBlacklist, newBlacklistPattern.trim()],
        },
      },
    }
    updateSettings(newSettings)
    setNewBlacklistPattern('')
  }

  const handleRemoveBlacklistPattern = (pattern: string) => {
    const currentBlacklist = settings.tool_policies['bash_exec']?.blacklist || []
    const newSettings: SecuritySettings = {
      ...settings,
      tool_policies: {
        ...settings.tool_policies,
        bash_exec: {
          ...settings.tool_policies['bash_exec'],
          policy: settings.tool_policies['bash_exec']?.policy || 'user_confirm',
          blacklist: currentBlacklist.filter((p) => p !== pattern),
        },
      },
    }
    updateSettings(newSettings)
  }

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleAddBlacklistPattern()
    }
  }

  // Group tools by source
  const groupedTools: GroupedTools = tools.reduce((acc, tool) => {
    const source = tool.source || 'core'
    if (!acc[source]) {
      acc[source] = []
    }
    acc[source].push(tool)
    return acc
  }, {} as GroupedTools)

  // Sort sources: core first, then alphabetically
  const sortedSources = Object.keys(groupedTools).sort((a, b) => {
    if (a === 'core') return -1
    if (b === 'core') return 1
    return a.localeCompare(b)
  })

  // Sort tools within each source by name
  sortedSources.forEach((source) => {
    if (groupedTools[source]) {
      groupedTools[source].sort((a, b) => a.name.localeCompare(b.name))
    }
  })

  if (isLoading || isToolsLoading) {
    return (
      <div className="flex items-center justify-center py-8 gap-2">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading security settings...</span>
      </div>
    )
  }

  if (tools.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-8 gap-2">
        <span className="text-sm text-muted-foreground">No tools available.</span>
        <span className="text-xs text-muted-foreground">Tools will appear here once registered.</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {sortedSources.map((source) => (
        <div key={source} className="flex flex-col gap-4">
          <h3 className="text-sm font-semibold text-muted-foreground border-b border-border pb-1">
            {getGroupLabel(source)}
          </h3>
          {(groupedTools[source] || []).map((tool) => {
            const currentPolicy = settings.tool_policies[tool.name]?.policy || 'user_confirm'
            const blacklist = settings.tool_policies[tool.name]?.blacklist || []
            const hasBlacklist = BLACKLIST_ENABLED_TOOLS.includes(tool.name)

            return (
              <div key={tool.name} className="flex flex-col gap-3">
                <div className="flex flex-col gap-1">
                  <span className="text-sm font-medium font-mono">{tool.name}</span>
                  <span className="text-xs text-muted-foreground">{tool.description}</span>
                </div>
                <div className="flex gap-1 p-1 bg-muted rounded-lg flex-wrap">
                  {policyOptions.map((option) => (
                    <Button
                      key={option.value}
                      variant={currentPolicy === option.value ? 'secondary' : 'ghost'}
                      size="sm"
                      className={`flex-1 gap-2 justify-center transition-all duration-200 ${
                        currentPolicy === option.value
                          ? 'bg-background shadow-sm text-foreground'
                          : 'text-muted-foreground hover:text-foreground'
                      }`}
                      onClick={() => handlePolicyChange(tool.name, option.value)}
                    >
                      <span className="text-xs">{option.label}</span>
                    </Button>
                  ))}
                </div>

                {/* Blacklist for bash_exec */}
                {hasBlacklist && (
                  <div className="mt-2 space-y-2">
                    <p className="text-xs text-muted-foreground">Blacklist patterns (regex):</p>
                    {blacklist.length > 0 && (
                      <div className="flex flex-wrap gap-2">
                        {blacklist.map((pattern) => (
                          <div
                            key={pattern}
                            className="flex items-center gap-1 px-2 py-1 bg-muted rounded text-xs"
                          >
                            <code className="font-mono">{pattern}</code>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-4 w-4 p-0 hover:bg-destructive/20"
                              onClick={() => handleRemoveBlacklistPattern(pattern)}
                            >
                              <X className="h-3 w-3" />
                            </Button>
                          </div>
                        ))}
                      </div>
                    )}
                    <div className="flex gap-2">
                      <Input
                        placeholder="e.g., rm\\s+-rf"
                        value={newBlacklistPattern}
                        onChange={(e) => setNewBlacklistPattern(e.target.value)}
                        onKeyDown={handleKeyDown}
                        className="h-8 text-xs font-mono"
                      />
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-8"
                        onClick={handleAddBlacklistPattern}
                        disabled={!newBlacklistPattern.trim()}
                      >
                        <Plus className="h-3 w-3" />
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      ))}

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          <strong>User Confirm</strong> mode requires manual approval. Use the "Ask agent" button to get an AI safety assessment before deciding.
          <strong> Always Allow</strong> disables all confirmations (use with caution).
        </p>
      </div>
    </div>
  )
}
