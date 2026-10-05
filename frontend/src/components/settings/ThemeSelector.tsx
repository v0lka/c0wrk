// Theme picker for the settings Appearance tab: a Radix dropdown combobox
// (the ui/combobox pattern — mandatory inside the modal settings Dialog)
// listing the two builtin themes plus every imported custom theme, with a
// native picker import button beside it. Custom rows expose the shared
// one-click hover-delete overlay (ItemAction pattern from session/project
// lists).

import { useCallback, useState } from 'react'
import { ChevronDown, Plus } from 'lucide-react'

import { deleteTheme, pickAndImportThemes } from '@/api/themes'
import { emit } from '@/api/runtime'
import { ThemeMenuItem, ThemeTypeIcon, type ThemeRow } from './ThemeMenuItem'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { logger } from '@/lib/logger'
import { cn } from '@/lib/utils'
import {
  BUILTIN_THEMES,
  themeTypeOf,
  useThemeStore,
} from '@/stores/themeStore'

/** Surfaces a failure via the app-wide runtime_error toast. */
function toastError(message: string): void {
  emit('runtime_error', { id: crypto.randomUUID(), message })
}

export function ThemeSelector() {
  const themeId = useThemeStore((s) => s.themeId)
  const customThemes = useThemeStore((s) => s.customThemes)
  const setTheme = useThemeStore((s) => s.setTheme)
  const loadThemes = useThemeStore((s) => s.loadThemes)
  const [open, setOpen] = useState(false)

  // The descriptor that resolves the trigger label/icon; falls back to
  // Default Dark when the active id is not (yet) in either list.
  const active: ThemeRow = (
    [...BUILTIN_THEMES, ...customThemes] as readonly ThemeRow[]
  ).find((t) => t.id === themeId) ?? { id: 'default-dark', name: 'Default Dark', type: 'dark' }

  const handleSelect = useCallback(
    (id: string) => {
      // Builtins carry no CSS; a custom theme activates with its body from
      // the catalog (listThemes entries carry the CSS; a re-selection of the
      // same theme keeps its cached CSS inside themeStore.setTheme).
      if (id === 'default-dark' || id === 'default-light') setTheme(id)
      else {
        const entry = useThemeStore.getState().customThemes.find((t) => t.id === id)
        setTheme(id, entry?.css ?? '', entry ? themeTypeOf(entry) : undefined)
      }
    },
    [setTheme],
  )

  const handleImport = useCallback(async () => {
    try {
      const outcomes = await pickAndImportThemes()
      if (!outcomes) return // user cancelled the picker — nothing changes
      const imported = outcomes.filter((o) => o.theme)
      if (imported.length > 0) {
        await loadThemes()
        // Activate the last successful import — with several files picked at
        // once it is the most recent pick, and single-file imports (the common
        // case) keep the exact "activate what you imported" behavior.
        const last = imported[imported.length - 1]?.theme
        if (last) setTheme(last.id, last.css ?? '', themeTypeOf(last))
      }
      const failed = outcomes.length - imported.length
      if (failed > 0) {
        // Some files were skipped (invalid CSS, unreadable, reserved id) —
        // the rest of the batch still installed; tell the user what happened.
        if (imported.length > 0) {
          toastError(
            `Imported ${imported.length} theme${imported.length === 1 ? '' : 's'}, ` +
              `${failed} file${failed === 1 ? ' was' : 's were'} skipped (invalid theme)`,
          )
        } else {
          toastError(
            `No themes imported: ${failed} file${failed === 1 ? ' was' : 's were'} skipped (invalid theme)`,
          )
        }
      }
    } catch (err) {
      logger.error('Failed to import theme:', err)
      toastError('Failed to import theme')
    }
  }, [loadThemes, setTheme])

  const handleDelete = useCallback(
    async (id: string) => {
      try {
        await deleteTheme(id)
        // applyThemes (inside loadThemes) rolls the active theme back to
        // Default Dark when the deleted one disappears from the catalog.
        await loadThemes()
      } catch (err) {
        logger.error('Failed to delete theme:', err)
        toastError('Failed to delete theme')
      }
    },
    [loadThemes],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">Theme</span>
      </div>
      <div className="flex items-center gap-1">
        <DropdownMenu open={open} onOpenChange={setOpen}>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label="Theme"
              title="Switch theme"
              className={cn(
                'flex h-9 flex-1 items-center gap-2 rounded-md border border-input bg-background px-3',
                'text-sm text-foreground hover:bg-muted/50 transition-colors truncate',
                'focus-visible:outline-none',
              )}
            >
              <ThemeTypeIcon type={active.type} className="text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate text-left">{active.name}</span>
              <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-56">
            {BUILTIN_THEMES.map((t) => (
              <ThemeMenuItem
                key={t.id}
                theme={t}
                active={t.id === themeId}
                onSelect={handleSelect}
              />
            ))}
            {customThemes.length > 0 && <DropdownMenuSeparator />}
            {customThemes.map((t) => (
              <ThemeMenuItem
                key={t.id}
                theme={t}
                active={t.id === themeId}
                onSelect={handleSelect}
                onDelete={handleDelete}
              />
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        <button
          type="button"
          aria-label="Import theme"
          title="Import theme"
          className="rounded p-1.5 hover:bg-muted/50 active:bg-muted/30"
          onClick={() => void handleImport()}
        >
          <Plus className="size-4" />
        </button>
      </div>
    </div>
  )
}
