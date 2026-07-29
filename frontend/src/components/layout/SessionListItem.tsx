// Shared session row primitives used by both session list surfaces:
//
// - SessionSelector (dropdown, CODE mode) — renders rows as DropdownMenuItem.
// - SessionList     (flat, CHAT mode)      — renders rows as plain buttons.
//
// Both share the identical row layout (status dot, active check, pin icon,
// name, relative time, hover action overlay). The `variant` prop selects the
// outer element; the inner content is identical so visuals stay consistent.

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { formatRelativeTime } from '@/lib/formatters'
import { useSessionStatusIndicator } from '@/hooks/useSessionStatusIndicator'
import type { SessionIndicatorStatus } from '@/hooks/useSessionStatusIndicator'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import {
  Check,
  Pencil,
  Archive,
  ArchiveRestore,
  Trash2,
  GitFork,
  Pin,
  PinOff,
} from 'lucide-react'

/** Minimal session shape consumed by a list item. */
export interface SessionItemSummary {
  id: string
  name: string
  archived: boolean
  pinned: boolean
  last_active_at: string
  has_unfinished_task: boolean
}

export interface SessionItemCallbacks {
  onSelect: () => void
  onRename: () => void
  onArchive: () => void
  onPin: () => void
  onFork: () => void
  onDelete: () => void
}

// --- Action button ---

interface SessionActionProps {
  label: string
  onClick: () => void
  disabled?: boolean
  children: ReactNode
}

export function SessionAction({ label, onClick, disabled, children }: SessionActionProps) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={onClick}
          disabled={disabled}
          className={cn(
            'rounded p-0.5',
            disabled && 'cursor-not-allowed opacity-30 hover:bg-transparent',
          )}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent side="left">{label}</TooltipContent>
    </Tooltip>
  )
}

// --- Row content (shared between variants) ---

interface SessionRowContentProps extends SessionItemCallbacks {
  session: SessionItemSummary
  isActive: boolean
  status: SessionIndicatorStatus
}

function SessionRowContent({ session, isActive, status, onPin, onFork, onRename, onArchive, onDelete }: SessionRowContentProps) {
  const forkDisabled = status === 'active' || session.has_unfinished_task
  const forkDisabledReason = status === 'active'
    ? 'Cannot fork while a task is running'
    : 'Cannot fork a session with an unfinished task'

  return (
    <>
      <div className="flex min-w-0 flex-1 items-center gap-1.5">
        {status === 'pending' && (
          <span className="size-1.5 shrink-0 rounded-full bg-warning" title="Awaiting your response" />
        )}
        {status === 'active' && <span className="size-1.5 shrink-0 rounded-full bg-success" title="Task running" />}
        {isActive && <Check className="size-3.5 shrink-0" />}
        {session.pinned && <Pin className="size-3 shrink-0 text-primary" />}
        <span className={cn('min-w-0 flex-1 truncate', isActive && 'font-medium')}>{session.name}</span>
      </div>
      <span className="text-[10px] text-muted-foreground">{formatRelativeTime(session.last_active_at)}</span>

      {/* Action overlay — absolutely positioned over the right portion of the
          item. Appears on hover/focus, with a gradient background so the
          underlying time text stays readable underneath the buttons. */}
      <span
        className="absolute inset-y-0 right-0 flex items-center gap-0.5 pl-7 pr-1 opacity-0 transition-opacity bg-gradient-to-l from-popover via-popover to-popover/0 group-hover/item:opacity-100 group-focus-within/item:opacity-100"
        onPointerDown={(e) => e.stopPropagation()}
        onPointerUp={(e) => e.stopPropagation()}
        onClick={(e) => e.stopPropagation()}
      >
        <SessionAction label={session.pinned ? 'Unpin' : 'Pin'} onClick={onPin}>
          {session.pinned ? <PinOff className="size-3 text-primary" /> : <Pin className="size-3 text-primary" />}
        </SessionAction>
        <SessionAction label={forkDisabled ? forkDisabledReason : 'Fork session'} onClick={onFork} disabled={forkDisabled}>
          <GitFork className="size-3 text-primary" />
        </SessionAction>
        <SessionAction label="Rename" onClick={onRename}>
          <Pencil className="size-3 text-info" />
        </SessionAction>
        <SessionAction label={session.archived ? 'Unarchive' : 'Archive'} onClick={onArchive}>
          {session.archived ? (
            <ArchiveRestore className="size-3 text-warning" />
          ) : (
            <Archive className="size-3 text-warning" />
          )}
        </SessionAction>
        <SessionAction label="Delete" onClick={onDelete}>
          <Trash2 className="size-3 text-destructive" />
        </SessionAction>
      </span>
    </>
  )
}

export interface SessionItemProps extends SessionItemCallbacks {
  session: SessionItemSummary
  isActive: boolean
  /** Outer element. 'dropdown' (DropdownMenuItem) or 'flat' (button). */
  variant?: 'dropdown' | 'flat'
}

export function SessionItem({
  session,
  isActive,
  variant = 'dropdown',
  onSelect,
  onRename,
  onArchive,
  onPin,
  onFork,
  onDelete,
}: SessionItemProps) {
  const status = useSessionStatusIndicator(session.id)
  const callbacks: SessionItemCallbacks = { onSelect, onRename, onArchive, onPin, onFork, onDelete }

  if (variant === 'flat') {
    return (
      <div
        role="button"
        tabIndex={0}
        className="group/item relative flex w-full items-center gap-2 rounded-sm px-2 py-1 text-left text-sm hover:bg-muted/50 focus:bg-muted/50 focus:outline-none cursor-pointer"
        onClick={onSelect}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onSelect()
          }
        }}
        aria-current={isActive ? 'true' : undefined}
      >
        <SessionRowContent session={session} isActive={isActive} status={status} {...callbacks} />
      </div>
    )
  }

  return (
    <DropdownMenuItem className="group/item gap-2" onSelect={onSelect}>
      <SessionRowContent session={session} isActive={isActive} status={status} {...callbacks} />
    </DropdownMenuItem>
  )
}
