import { useState, type KeyboardEvent } from 'react'
import { X, Plus, Lock } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'

/**
 * Toggle switch — master / sub-toggles for the Small LLM profile.
 * Uses the same peer-based switch styling as the security auto-approve toggle,
 * driven entirely by design tokens (no raw hex).
 */
interface ToggleProps {
  checked: boolean
  onChange: (checked: boolean) => void
  label: string
  description?: string
  disabled?: boolean
}

export function Toggle({ checked, onChange, label, description, disabled }: ToggleProps) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-3">
        <label className="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={checked}
            disabled={disabled}
            onChange={(e) => onChange(e.target.checked)}
            className="sr-only peer"
          />
          <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary transition-colors after:content-[''] after:absolute after:top-0.5 after:inset-s-0.5 after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
        </label>
        <span className={`text-sm font-medium ${disabled ? 'text-muted-foreground' : ''}`}>{label}</span>
      </div>
      {description && <p className="text-xs text-muted-foreground pl-12">{description}</p>}
    </div>
  )
}

/**
 * NumberField — integer input for thresholds / counts. Persists on blur to
 * avoid a save storm while typing.
 */
interface NumberFieldProps {
  label: string
  value: number
  onChange: (value: number) => void
  min?: number
  step?: number
  disabled?: boolean
}

export function NumberField({ label, value, onChange, min, step, disabled }: NumberFieldProps) {
  const [draft, setDraft] = useState(String(value))
  const [focused, setFocused] = useState(false)

  const display = focused ? draft : String(value)

  const commit = () => {
    setFocused(false)
    const parsed = Number(draft)
    if (Number.isFinite(parsed) && (min === undefined || parsed >= min)) {
      onChange(parsed)
    } else {
      setDraft(String(value))
    }
  }

  return (
    <div className="flex flex-col gap-1">
      <label className="text-xs text-muted-foreground">{label}</label>
      <Input
        type="number"
        data-field={label}
        value={display}
        disabled={disabled}
        min={min}
        step={step}
        onFocus={() => {
          setFocused(true)
          setDraft(String(value))
        }}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        className="h-8 w-24 text-sm"
      />
    </div>
  )
}

/**
 * TagList — editable list of tool-name strings (e.g. always_present). Add via
 * Enter / button, remove via the per-chip X. Values in `lockedValues` are
 * rendered as non-removable locked tags (no X, a lock glyph).
 */
interface TagListProps {
  label: string
  values: string[]
  onChange: (values: string[]) => void
  placeholder?: string
  disabled?: boolean
  lockedValues?: Set<string>
}

export function TagList({ label, values, onChange, placeholder, disabled, lockedValues }: TagListProps) {
  const [draft, setDraft] = useState('')

  const add = () => {
    const trimmed = draft.trim()
    if (!trimmed || values.includes(trimmed)) {
      setDraft('')
      return
    }
    onChange([...values, trimmed])
    setDraft('')
  }

  const remove = (v: string) => {
    if (lockedValues?.has(v)) return
    onChange(values.filter((x) => x !== v))
  }

  const handleKey = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      add()
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-muted-foreground">{label}</label>
      {values.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {values.map((v) => {
            const locked = lockedValues?.has(v) ?? false
            return (
              <div
                key={v}
                className={`flex items-center gap-1 px-2 py-1 rounded text-xs ${
                  locked ? 'bg-muted/60 border border-border text-muted-foreground' : 'bg-muted'
                }`}
              >
                {locked && <Lock className="h-3 w-3 shrink-0" />}
                <code className="font-mono">{v}</code>
                {!locked && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-4 w-4 p-0 hover:bg-destructive/20"
                    onClick={() => remove(v)}
                    disabled={disabled}
                    aria-label={`Remove ${v}`}
                  >
                    <X className="h-3 w-3" />
                  </Button>
                )}
              </div>
            )
          })}
        </div>
      )}
      <div className="flex gap-2">
        <Input
          placeholder={placeholder}
          value={draft}
          disabled={disabled}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={handleKey}
          className="h-8 text-xs font-mono"
        />
        <Button variant="outline" size="sm" className="h-8" onClick={add} disabled={disabled || !draft.trim()}>
          <Plus className="h-3 w-3" />
        </Button>
      </div>
    </div>
  )
}
