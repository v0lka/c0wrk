import { useEffect, useState } from 'react'
import { Plus, X, AlertTriangle, Info } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Combobox } from '@/components/ui/combobox'
import { getShellExecSettings, updateShellExecSettings } from '@/api/config'
import type { ShellExecSettingsResponse, ShellExecToolSettings, ShellExecShellKind } from '@/types/models'
import { logger } from '@/lib/logger'

/** The closed shell set the backend accepts (mirrors config.ShellExecToolConfig). */
const SHELL_KINDS: { value: ShellExecShellKind; label: string }[] = [
  { value: 'bash', label: 'bash' },
  { value: 'sh', label: 'sh' },
  { value: 'zsh', label: 'zsh' },
  { value: 'ksh', label: 'ksh' },
  { value: 'dash', label: 'dash' },
  { value: 'powershell', label: 'powershell' },
  { value: 'pwsh', label: 'pwsh' },
]

/** The argv element replaced by the agent's command (mirrors backend). */
const COMMAND_PLACEHOLDER = '{command}'

interface ToolOverride {
  binary: string
  args: string[]
  shell: ShellExecShellKind | ''
}

const EMPTY_TOOL: ToolOverride = { binary: '', args: [], shell: '' }

interface ShellExecutionCardProps {
  /** Called after a successful save so the parent can react (currently unused). */
  onSaved?: () => void
}

/**
 * Shell-execution launch-shape override (the shell_exec config section): how
 * the platform shell tool (bash_exec on Linux/macOS, posh_exec on Windows)
 * launches the agent's command, plus the declared shell the command text is
 * written in. The deterministic command-safety analysis runs with the
 * declared shell's dialect, so the shell list is closed — shells the analysis
 * cannot parse are rejected by the backend at load and at save.
 */
export function ShellExecutionCard({ onSaved }: ShellExecutionCardProps) {
  const [bash, setBash] = useState<ToolOverride>(EMPTY_TOOL)
  const [posh, setPosh] = useState<ToolOverride>(EMPTY_TOOL)
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [savedTick, setSavedTick] = useState(0)

  useEffect(() => {
    let cancelled = false
    getShellExecSettings()
      .then((settings: ShellExecSettingsResponse) => {
        if (cancelled) return
        setBash(toToolState(settings.bash_exec))
        setPosh(toToolState(settings.posh_exec))
        setLoaded(true)
      })
      .catch((err) => {
        if (cancelled) return
        logger.error('Failed to load shell exec settings:', err)
        setError('Failed to load the current shell command settings.')
      })
    return () => {
      cancelled = true
    }
  }, [])

  const bashValid = validateTool(bash)
  const poshValid = validateTool(posh)
  const canSave = loaded && !saving && bashValid === null && poshValid === null

  const save = async () => {
    if (!canSave) return
    setSaving(true)
    setError(null)
    try {
      await updateShellExecSettings({
        bash_exec: toPayload(bash),
        posh_exec: toPayload(posh),
      })
      setSavedTick((t) => t + 1)
      onSaved?.()
    } catch (err) {
      logger.error('Failed to save shell exec settings:', err)
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50" data-testid="shell-exec-card">
      <div className="flex flex-col gap-0.5">
        <span className="text-sm font-medium">Shell execution</span>
        <span className="text-xs text-muted-foreground">
          Override how the platform shell tool launches commands and declare which shell the
          command text is written in. The safety analysis and the agent prompts follow the declared
          shell. Leave empty to keep the built-in launch shape (bash -c / powershell -Command).
        </span>
      </div>

      <ToolEditor
        testIdPrefix="shell-exec-bash"
        toolName="bash_exec"
        appliesTo="Linux / macOS"
        state={bash}
        onChange={setBash}
      />
      <ToolEditor
        testIdPrefix="shell-exec-posh"
        toolName="posh_exec"
        appliesTo="Windows"
        state={posh}
        onChange={setPosh}
      />

      {error && (
        <div role="alert" data-testid="shell-exec-error" className="flex items-start gap-2 text-xs text-destructive">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}

      <div className="flex items-center gap-3">
        <Button
          variant="outline"
          size="sm"
          onClick={save}
          disabled={!canSave}
          data-testid="shell-exec-save"
        >
          {saving ? 'Saving…' : 'Save shell command'}
        </Button>
        {savedTick > 0 && !error && (
          <span data-testid="shell-exec-saved" className="text-xs text-muted-foreground">
            Saved — applies to new and running sessions.
          </span>
        )}
      </div>

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 shrink-0 mt-0.5" />
        <p>
          The <code className="font-mono">{COMMAND_PLACEHOLDER}</code> element is replaced by the
          agent&apos;s command as a single argument — no extra quoting. Security gates (policy,
          blocklist, command analysis) are unaffected. verify-on-edit commands also run under this
          shell.
        </p>
      </div>
    </div>
  )
}

interface ToolEditorProps {
  testIdPrefix: string
  toolName: string
  appliesTo: string
  state: ToolOverride
  onChange: (next: ToolOverride) => void
}

/** One tool's override editor: binary, one-argument-per-row template, shell select. */
function ToolEditor({ testIdPrefix, toolName, appliesTo, state, onChange }: ToolEditorProps) {
  const [newArg, setNewArg] = useState('')
  const problem = validateTool(state)
  const preview = problem ?? previewCommand(state)

  const addArg = () => {
    const arg = newArg
    if (!arg) return
    onChange({ ...state, args: [...state.args, arg] })
    setNewArg('')
  }

  return (
    <div className="flex flex-col gap-2 p-3 rounded-md border border-border/60" data-testid={`${testIdPrefix}-editor`}>
      <div className="flex items-baseline gap-2">
        <span className="text-xs font-medium font-mono">{toolName}</span>
        <span className="text-[10px] text-muted-foreground">{appliesTo}</span>
      </div>

      <div className="flex items-center gap-2">
        <label className="text-xs text-muted-foreground w-14 shrink-0" htmlFor={`${testIdPrefix}-binary`}>
          Binary
        </label>
        <Input
          id={`${testIdPrefix}-binary`}
          value={state.binary}
          onChange={(e) => onChange({ ...state, binary: e.target.value })}
          placeholder="e.g. /opt/homebrew/bin/zsh"
          className="h-8 text-xs font-mono flex-1"
          data-testid={`${testIdPrefix}-binary`}
        />
        <div className="w-[130px] shrink-0" data-testid={`${testIdPrefix}-shell`}>
          <Combobox
            ariaLabel={`${toolName} declared shell`}
            value={state.shell}
            onChange={(v) => onChange({ ...state, shell: v as ShellExecShellKind })}
            className="h-8 text-xs"
            options={SHELL_KINDS.map((k) => ({ value: k.value, label: k.label }))}
          />
        </div>
      </div>

      <div className="flex flex-col gap-1.5">
        <label className="text-xs text-muted-foreground">Arguments — one per row</label>
        {state.args.map((arg, i) => (
          <div key={`${arg}-${i}`} className="flex items-center gap-1.5">
            <span
              className={
                'text-xs font-mono truncate flex-1 px-2 py-1 rounded bg-muted/50 ' +
                (arg === COMMAND_PLACEHOLDER ? 'text-primary' : '')
              }
              data-testid={`${testIdPrefix}-arg-${i}`}
            >
              {arg}
            </span>
            <Button
              variant="ghost"
              size="icon"
              className="h-6 w-6"
              aria-label={`Remove argument ${arg}`}
              onClick={() => onChange({ ...state, args: state.args.filter((_, j) => j !== i) })}
              data-testid={`${testIdPrefix}-arg-remove-${i}`}
            >
              <X className="h-3 w-3" />
            </Button>
          </div>
        ))}
        <div className="flex items-center gap-1.5">
          <Input
            value={newArg}
            onChange={(e) => setNewArg(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                addArg()
              }
            }}
            placeholder={`add argument (use ${COMMAND_PLACEHOLDER} for the command)`}
            className="h-8 text-xs font-mono flex-1"
            data-testid={`${testIdPrefix}-new-arg`}
          />
          <Button variant="ghost" size="icon" className="h-6 w-6" aria-label="Add argument" onClick={addArg} data-testid={`${testIdPrefix}-arg-add`}>
            <Plus className="h-3 w-3" />
          </Button>
        </div>
      </div>

      <p
        data-testid={`${testIdPrefix}-preview`}
        className={'text-xs font-mono break-all ' + (problem ? 'text-destructive' : 'text-muted-foreground')}
      >
        {problem ? `⚠ ${problem}` : preview}
      </p>
    </div>
  )
}

function toToolState(settings: ShellExecToolSettings): ToolOverride {
  if (settings.command.length === 0) return EMPTY_TOOL
  return {
    binary: settings.command[0] ?? '',
    args: settings.command.slice(1),
    shell: (settings.shell || '') as ToolOverride['shell'],
  }
}

function toPayload(state: ToolOverride): ShellExecToolSettings {
  if (!state.binary) return { command: [], shell: '' }
  return { command: [state.binary, ...state.args], shell: state.shell }
}

/** Client-side mirror of the backend validation (the backend stays authoritative). */
function validateTool(state: ToolOverride): string | null {
  if (!state.binary && state.args.length === 0 && !state.shell) return null // no override
  if (!state.binary.trim()) return 'Binary is required when an override is set.'
  if (state.binary === COMMAND_PLACEHOLDER) return 'The binary must not be the {command} placeholder.'
  if (state.args.length === 0) return `Add at least one ${COMMAND_PLACEHOLDER} argument.`
  const placeholders = state.args.filter((a) => a === COMMAND_PLACEHOLDER).length
  if (placeholders === 0) return `Add exactly one ${COMMAND_PLACEHOLDER} argument.`
  if (placeholders > 1) return `Only one ${COMMAND_PLACEHOLDER} argument is allowed.`
  if (state.args.some((a) => a !== COMMAND_PLACEHOLDER && a.includes(COMMAND_PLACEHOLDER))) {
    return `${COMMAND_PLACEHOLDER} must be a standalone argument, not embedded in another.`
  }
  if (!state.shell) return 'Declare the shell the command text is written in.'
  return null
}

/** Renders the effective launch command with the placeholder shown as &lt;command&gt;. */
function previewCommand(state: ToolOverride): string {
  const rendered = state.args.map((a) => (a === COMMAND_PLACEHOLDER ? '<command>' : a))
  const quote = (s: string) => (s.includes(' ') ? `"${s}"` : s)
  return [quote(state.binary), ...rendered].join(' ')
}
