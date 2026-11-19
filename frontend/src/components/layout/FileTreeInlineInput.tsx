import { useEffect, useMemo, useRef, useState } from 'react'
import { FilePlus, FolderPlus, Pencil } from 'lucide-react'
import { validateEntryName } from '@/lib/fileTreeNames'

export type InlineEditMode = 'file' | 'folder' | 'rename'

interface FileTreeInlineInputProps {
  mode: InlineEditMode
  /** Initial value: empty for creation, the current name for a rename. */
  initialName: string
  /** Tree depth — the input aligns with the rows around it. */
  depth: number
  /** True while the commit RPC is in flight (locks the input, ignores blur). */
  busy: boolean
  /** Server-side error surfaced by the panel after a failed commit. */
  error: string | null
  /** Sibling names in the target directory, for the collision check. */
  siblings: readonly string[]
  onCommit: (name: string) => void
  onCancel: () => void
}

const modeIcon = {
  file: FilePlus,
  folder: FolderPlus,
  rename: Pencil,
} as const

/**
 * The in-tree inline name input for create/rename (issue #60): Enter commits,
 * Escape cancels, blur cancels (unless a commit is in flight). Validation
 * runs client-side for a clear message; the backend stays authoritative and
 * its errors surface through `error`.
 */
export function FileTreeInlineInput({
  mode,
  initialName,
  depth,
  busy,
  error,
  siblings,
  onCommit,
  onCancel,
}: FileTreeInlineInputProps) {
  const [value, setValue] = useState(initialName)
  const inputRef = useRef<HTMLInputElement>(null)

  // Autofocus with the name pre-selected: typing replaces it, Enter without
  // typing commits the current name unchanged (a no-op rename is rejected by
  // the backend).
  useEffect(() => {
    const el = inputRef.current
    if (!el) return
    el.focus()
    el.select()
  }, [])

  // Validation runs only once the value is TOUCHED — an untouched input (or
  // one showing a server-side error) must not flash "empty name" before the
  // user types; the server error stays visible through `error` until then.
  const touched = value !== initialName
  const localError = useMemo(
    () => (touched ? validateEntryName(value, siblings) : null),
    [touched, value, siblings],
  )
  const shownError = localError ?? error
  const Icon = modeIcon[mode]

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onCancel()
      return
    }
    // An empty/whitespace name is inert on Enter — nothing to commit.
    if (e.key === 'Enter' && !busy && !localError && value.trim().length > 0) {
      e.preventDefault()
      onCommit(value)
    }
  }

  const handleBlur = () => {
    // A blur during the commit RPC must not tear the input down — the panel
    // clears it when the RPC settles.
    if (!busy) onCancel()
  }

  return (
    <div
      className="flex flex-col gap-0.5 py-0.5 pr-4"
      style={{ paddingLeft: `${depth * 16 + 4}px` }}
      data-testid="file-tree-inline-input"
      onContextMenu={(e) => e.stopPropagation()}
    >
      <div className="flex items-center gap-1">
        <Icon className="size-3.5 shrink-0 text-muted-foreground" />
        <input
          ref={inputRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={handleKeyDown}
          onBlur={handleBlur}
          disabled={busy}
          spellCheck={false}
          aria-label={mode === 'rename' ? 'New name' : 'New entry name'}
          className="h-6 min-w-0 flex-1 rounded border bg-background px-1 text-sm outline-none focus:border-primary"
        />
      </div>
      {shownError && (
        <p className="truncate pl-5 text-xs text-destructive" role="alert">
          {shownError}
        </p>
      )}
    </div>
  )
}
