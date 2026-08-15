import { useState, type KeyboardEvent } from 'react'
import { Plus, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { GroupPolicy, ToolInfo } from '@/types/models'
import { EXECUTE_GROUP, GROUP_META, POLICY_OPTIONS } from '@/lib/securityGroups'

interface SecurityGroupCardProps {
  group: string
  policy: GroupPolicy
  blacklist: string[]
  tools: ToolInfo[]
  onPolicyChange: (group: string, policy: GroupPolicy) => void
  onBlacklistChange: (group: string, blacklist: string[]) => void
}

/**
 * One configurable security group: policy dropdown, the read-only tool list
 * mapped into the group (policies are group-level — tools are display-only),
 * and, for the execute group, the command-blacklist editor.
 */
export function SecurityGroupCard({
  group,
  policy,
  blacklist,
  tools,
  onPolicyChange,
  onBlacklistChange,
}: SecurityGroupCardProps) {
  const meta = GROUP_META[group] ?? { title: group, description: '' }
  const sorted = [...tools].sort((a, b) => a.name.localeCompare(b.name))

  return (
    <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex flex-col gap-0.5 min-w-0">
          <span className="text-sm font-medium">
            {meta.title} <span className="font-mono text-xs text-muted-foreground">({group})</span>
          </span>
          <span className="text-xs text-muted-foreground">{meta.description}</span>
        </div>
        <select
          aria-label={`${meta.title} policy`}
          value={policy}
          onChange={(e) => onPolicyChange(group, e.target.value as GroupPolicy)}
          className="c0-input h-8 px-2 rounded-md border border-input text-xs focus:outline-none min-w-[130px]"
        >
          {POLICY_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>{opt.label}</option>
          ))}
        </select>
      </div>

      {sorted.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          {sorted.map((tool) => (
            <div key={tool.name} className="flex items-baseline gap-2 min-w-0">
              <span className="text-xs font-medium font-mono shrink-0">{tool.name}</span>
              {tool.source && tool.source !== 'core' && (
                <span className="text-[10px] text-muted-foreground/70 shrink-0">{tool.source}</span>
              )}
              <span className="text-xs text-muted-foreground truncate">{tool.description}</span>
            </div>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground italic">No tools currently map to this group.</p>
      )}

      {group === EXECUTE_GROUP && (
        <BlacklistEditor blacklist={blacklist} onChange={(bl) => onBlacklistChange(group, bl)} />
      )}
    </div>
  )
}

interface BlacklistEditorProps {
  blacklist: string[]
  onChange: (blacklist: string[]) => void
}

/** Regex-pattern editor for the execute group's command blacklist. */
function BlacklistEditor({ blacklist, onChange }: BlacklistEditorProps) {
  const [newPattern, setNewPattern] = useState('')

  const addPattern = () => {
    const pattern = newPattern.trim()
    if (!pattern || blacklist.includes(pattern)) {
      setNewPattern('')
      return
    }
    onChange([...blacklist, pattern])
    setNewPattern('')
  }

  const removePattern = (pattern: string) => onChange(blacklist.filter((p) => p !== pattern))

  const handleKey = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      addPattern()
    }
  }

  return (
    <div className="mt-1 flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">
        Blacklist patterns (regex) — a matching shell command is forced to user confirmation:
      </p>
      {blacklist.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {blacklist.map((p) => (
            <div key={p} className="flex items-center gap-1 px-2 py-1 bg-muted rounded text-xs">
              <code className="font-mono">{p}</code>
              <Button
                variant="ghost"
                size="sm"
                aria-label={`Remove pattern ${p}`}
                className="h-4 w-4 p-0 hover:bg-destructive/20"
                onClick={() => removePattern(p)}
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
          value={newPattern}
          onChange={(e) => setNewPattern(e.target.value)}
          onKeyDown={handleKey}
          className="h-8 text-xs font-mono"
        />
        <Button
          variant="outline"
          size="sm"
          className="h-8"
          onClick={addPattern}
          disabled={!newPattern.trim()}
          aria-label="Add blacklist pattern"
        >
          <Plus className="h-3 w-3" />
        </Button>
      </div>
    </div>
  )
}
