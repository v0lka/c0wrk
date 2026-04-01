import { useState, useEffect, type KeyboardEvent } from 'react'
import { Info, Plus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

type ToolPolicy = 'always_allow' | 'always_deny' | 'user_confirm' | 'auto'

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

const policyOptions: PolicyOption[] = [
  { value: 'always_allow', label: 'Always Allow' },
  { value: 'always_deny', label: 'Always Deny' },
  { value: 'user_confirm', label: 'User Confirm' },
  { value: 'auto', label: 'Auto (Heuristics + LLM Judge)' },
]

const toolConfigs = [
  { id: 'bash_exec', name: 'Bash Execution', hasBlacklist: true },
  { id: 'file_ops', name: 'File Operations', hasBlacklist: false },
  { id: 'web_search', name: 'Web Search', hasBlacklist: false },
  { id: 'web_fetch', name: 'Web Fetch', hasBlacklist: false },
]

export function SecuritySettings() {
  const [settings, setSettings] = useState<SecuritySettings>({
    default_policy: 'auto',
    tool_policies: {},
  })
  const [newBlacklistPattern, setNewBlacklistPattern] = useState('')
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    const loadSettings = async () => {
      try {
        const getSecuritySettings = window.go?.main?.App?.GetSecuritySettings
        if (getSecuritySettings) {
          const result = (await getSecuritySettings()) as unknown as SecuritySettings
          setSettings(result)
        }
      } catch {
        // Keep defaults if fetch fails
      } finally {
        setIsLoading(false)
      }
    }
    loadSettings()
  }, [])

  const updateSettings = async (newSettings: SecuritySettings) => {
    setSettings(newSettings)
    try {
      const updateSecuritySettings = window.go?.main?.App?.UpdateSecuritySettings
      if (updateSecuritySettings) {
        await updateSecuritySettings(newSettings as unknown as Record<string, unknown>)
      }
    } catch {
      // Handle error silently
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
          policy: settings.tool_policies['bash_exec']?.policy || 'auto',
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
          policy: settings.tool_policies['bash_exec']?.policy || 'auto',
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

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading security settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {toolConfigs.map((tool) => {
        const currentPolicy = settings.tool_policies[tool.id]?.policy || 'auto'
        const blacklist = settings.tool_policies[tool.id]?.blacklist || []

        return (
          <div key={tool.id} className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">{tool.name}</span>
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
                  onClick={() => handlePolicyChange(tool.id, option.value)}
                >
                  <span className="text-xs">{option.label}</span>
                </Button>
              ))}
            </div>

            {/* Blacklist for bash_exec */}
            {tool.hasBlacklist && (
              <div className="mt-2 space-y-2">
                <p className="text-xs text-muted-foreground">Blacklist patterns (regex):</p>
                {blacklist.length > 0 && (
                  <div className="flex flex-wrap gap-2">
                    {blacklist.map((pattern, index) => (
                      <div
                        key={index}
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

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          <strong>Auto</strong> mode uses heuristics and LLM Judge to determine if confirmation is needed.
          <strong> Always Allow</strong> disables all confirmations (use with caution).
        </p>
      </div>
    </div>
  )
}
