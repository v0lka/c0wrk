import { X, Lock } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Markdown } from '@/lib/markdownConfig'
import type { PickerOption } from '@/lib/modelProfilesTools'

/**
 * Toggle switch — master / sub-toggles for the Model Profiles profile.
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
 * TagList — an editable list of tool-name chips (e.g. always_present). A tool
 * is added by picking an entry from a combobox restricted to `options` (the
 * caller pre-filters it to the still-selectable built-ins and clusters); an
 * entry may be a single tool or a workflow cluster, in which case picking it
 * pins the whole cluster at once. The per-chip X removes one. Values in
 * `lockedValues` render as non-removable locked tags (no X, a lock glyph).
 */
interface TagListProps {
  label: string
  values: string[]
  onChange: (values: string[]) => void
  /** Picker entries (tools and clusters) offered, in display order. */
  options: readonly PickerOption[]
  disabled?: boolean
  lockedValues?: Set<string>
}

export function TagList({ label, values, onChange, options, disabled, lockedValues }: TagListProps) {
  const add = (value: string) => {
    const option = options.find((o) => o.value === value)
    if (!option) return
    // A cluster entry (members set) pins its whole cluster at once, so the
    // picker never adds a workflow only in part (individual chips remain
    // removable afterwards). A tool entry pins itself. Already-present names
    // are filtered defensively.
    const toAdd = (option.members ?? [option.value]).filter((n) => !values.includes(n))
    if (toAdd.length === 0) return
    onChange([...values, ...toAdd])
  }

  const remove = (v: string) => {
    if (lockedValues?.has(v)) return
    onChange(values.filter((x) => x !== v))
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
      {options.length === 0 ? (
        <p className="text-xs text-muted-foreground italic">All built-in tools are already included.</p>
      ) : (
        <Combobox
          ariaLabel={`Add ${label}`}
          value=""
          options={options.map((o) => ({
            value: o.value,
            label: o.label,
            tooltip: o.tooltip ? (
              <Markdown content={o.tooltip} compact className="prose-tooltip" />
            ) : undefined,
          }))}
          onChange={add}
          placeholder="Add tool or group…"
          disabled={disabled}
          className="h-8 w-full text-xs font-mono"
        />
      )}
    </div>
  )
}
