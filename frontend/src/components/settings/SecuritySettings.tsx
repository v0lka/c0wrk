import { useState, useEffect, type KeyboardEvent } from 'react'
import { Info, Plus, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getSecuritySettings, updateSecuritySettings } from '@/api/config'
import { getToolList } from '@/api/mcp'
import { logger } from '@/lib/logger'
import type { ToolInfo, SecuritySettingsResponse, ToolPolicyResponse } from '@/types/models'

type ToolPolicy = 'always_allow' | 'always_deny' | 'user_confirm'

interface LocalSettings {
  default_policy: ToolPolicy
  tool_policies: Record<string, { policy: ToolPolicy; blacklist?: string[] }>
  auto_approve_workspace_writes: boolean
}

const policyOptions: { value: ToolPolicy; label: string }[] = [
  { value: 'always_allow', label: 'Always Allow' },
  { value: 'always_deny', label: 'Always Deny' },
  { value: 'user_confirm', label: 'User Confirm' },
]

const BLACKLIST_TOOLS = ['bash_exec']
const INTERNAL_TOOLS = new Set(['ask_user', 'finish', 'list_step_outputs', 'read_step_output'])

export function SecuritySettings() {
  const [settings, setSettings] = useState<LocalSettings>({ default_policy: 'user_confirm', tool_policies: {}, auto_approve_workspace_writes: false })
  const [tools, setTools] = useState<ToolInfo[]>([])
  const [newPattern, setNewPattern] = useState('')
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    Promise.all([
      getSecuritySettings().then((r) => {
        const tp: Record<string, { policy: ToolPolicy; blacklist?: string[] }> = {}
        for (const [name, data] of Object.entries(r.tool_policies || {})) {
          tp[name] = { policy: (data.policy as ToolPolicy) || 'user_confirm', blacklist: data.blacklist }
        }
        setSettings({ default_policy: (r.default_policy as ToolPolicy) || 'user_confirm', tool_policies: tp, auto_approve_workspace_writes: r.auto_approve_workspace_writes || false })
      }),
      getToolList().then((r) => setTools((r || []).filter((t) => !INTERNAL_TOOLS.has(t.name)))),
    ]).catch((err) => logger.error('Failed to load security settings:', err))
      .finally(() => setIsLoading(false))
  }, [])

  const save = async (next: LocalSettings) => {
    setSettings(next)
    try {
      const tp: Record<string, ToolPolicyResponse> = {}
      for (const [name, data] of Object.entries(next.tool_policies)) {
        tp[name] = { policy: data.policy, blacklist: data.blacklist }
      }
      await updateSecuritySettings({ default_policy: next.default_policy, tool_policies: tp, auto_approve_workspace_writes: next.auto_approve_workspace_writes } as SecuritySettingsResponse)
    } catch (err) { logger.error('Failed to update security settings:', err) }
  }

  const handlePolicy = (tool: string, policy: ToolPolicy) => {
    save({ ...settings, tool_policies: { ...settings.tool_policies, [tool]: { ...settings.tool_policies[tool], policy } } })
  }

  const addBlacklist = () => {
    if (!newPattern.trim()) return
    const bl = settings.tool_policies['bash_exec']?.blacklist || []
    if (bl.includes(newPattern.trim())) { setNewPattern(''); return }
    save({
      ...settings,
      tool_policies: { ...settings.tool_policies, bash_exec: { ...settings.tool_policies['bash_exec'], policy: settings.tool_policies['bash_exec']?.policy || 'user_confirm', blacklist: [...bl, newPattern.trim()] } },
    })
    setNewPattern('')
  }

  const removeBlacklist = (pattern: string) => {
    const bl = (settings.tool_policies['bash_exec']?.blacklist || []).filter((p) => p !== pattern)
    save({
      ...settings,
      tool_policies: { ...settings.tool_policies, bash_exec: { ...settings.tool_policies['bash_exec'], policy: settings.tool_policies['bash_exec']?.policy || 'user_confirm', blacklist: bl } },
    })
  }

  const handleKey = (e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); addBlacklist() } }

  const handleAutoApprove = (checked: boolean) => {
    save({ ...settings, auto_approve_workspace_writes: checked })
  }

  // Group tools by source, core first
  const grouped = tools.reduce<Record<string, ToolInfo[]>>((acc, t) => {
    const src = t.source || 'core'
    ;(acc[src] ??= []).push(t)
    return acc
  }, {})
  const sources = Object.keys(grouped).sort((a, b) => a === 'core' ? -1 : b === 'core' ? 1 : a.localeCompare(b))

  if (isLoading) {
    return <div className="flex items-center justify-center py-8 gap-2"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /><span className="text-sm text-muted-foreground">Loading security settings...</span></div>
  }

  if (tools.length === 0) {
    return <div className="flex flex-col items-center justify-center py-8 gap-2"><span className="text-sm text-muted-foreground">No tools available.</span></div>
  }

  return (
    <div className="flex flex-col gap-6">
      {/* Workspace auto-approve toggle */}
      <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
        <div className="flex items-center gap-3">
          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={settings.auto_approve_workspace_writes}
              onChange={(e) => handleAutoApprove(e.target.checked)}
              className="sr-only peer"
            />
            <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary transition-colors after:content-[''] after:absolute after:top-0.5 after:start-[2px] after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
          </label>
          <span className="text-sm font-medium">Auto-approve writes in workspace</span>
        </div>
        <p className="text-xs text-muted-foreground pl-12">
          When enabled, file write tools (write_file, edit_file, delete_file,
          delete_directory, create_directory) execute without confirmation
          when all paths are within the session workspace. Symlink traversals
          are still forced to confirmation.
        </p>
      </div>

      {sources.map((source) => (
        <div key={source} className="flex flex-col gap-4">
          <h3 className="text-sm font-semibold text-muted-foreground border-b border-border pb-1">
            {source === 'core' ? 'Built-in Tools' : `MCP: ${source}`}
          </h3>
          {(grouped[source] || []).sort((a, b) => a.name.localeCompare(b.name)).map((tool) => {
            const policy = (settings.tool_policies[tool.name]?.policy || 'user_confirm') as ToolPolicy
            const bl = settings.tool_policies[tool.name]?.blacklist || []
            const hasBl = BLACKLIST_TOOLS.includes(tool.name)
            return (
              <div key={tool.name} className="flex flex-col gap-3">
                <div className="flex flex-col gap-1">
                  <span className="text-sm font-medium font-mono">{tool.name}</span>
                  <span className="text-xs text-muted-foreground">{tool.description}</span>
                </div>
                <div className="flex gap-1 p-1 bg-muted rounded-lg flex-wrap">
                  {policyOptions.map((opt) => (
                    <Button
                      key={opt.value}
                      variant={policy === opt.value ? 'secondary' : 'ghost'}
                      size="sm"
                      className={`flex-1 gap-2 justify-center transition-all duration-200 ${policy === opt.value ? 'bg-background shadow-sm text-foreground' : 'text-muted-foreground hover:text-foreground'}`}
                      onClick={() => handlePolicy(tool.name, opt.value)}
                    >
                      <span className="text-xs">{opt.label}</span>
                    </Button>
                  ))}
                </div>
                {hasBl && (
                  <div className="mt-2 space-y-2">
                    <p className="text-xs text-muted-foreground">Blacklist patterns (regex):</p>
                    {bl.length > 0 && (
                      <div className="flex flex-wrap gap-2">
                        {bl.map((p) => (
                          <div key={p} className="flex items-center gap-1 px-2 py-1 bg-muted rounded text-xs">
                            <code className="font-mono">{p}</code>
                            <Button variant="ghost" size="sm" className="h-4 w-4 p-0 hover:bg-destructive/20" onClick={() => removeBlacklist(p)}><X className="h-3 w-3" /></Button>
                          </div>
                        ))}
                      </div>
                    )}
                    <div className="flex gap-2">
                      <Input placeholder="e.g., rm\\s+-rf" value={newPattern} onChange={(e) => setNewPattern(e.target.value)} onKeyDown={handleKey} className="h-8 text-xs font-mono" />
                      <Button variant="outline" size="sm" className="h-8" onClick={addBlacklist} disabled={!newPattern.trim()}><Plus className="h-3 w-3" /></Button>
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
        <p><strong>User Confirm</strong> requires manual approval. <strong>Always Allow</strong> disables confirmations (use with caution).</p>
      </div>
    </div>
  )
}
