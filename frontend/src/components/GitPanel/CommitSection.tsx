import { useState, useRef, useEffect, useCallback } from 'react'
import { Loader2 } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { commit } from '@/api/git'
import { cn } from '@/lib/utils'

export function CommitSection() {
  const entries = useGitPanelStore((s) => s.entries)
  const commitMessage = useGitPanelStore((s) => s.commitMessage)
  const setCommitMessage = useGitPanelStore((s) => s.setCommitMessage)

  const [isCommitting, setIsCommitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const stagedCount = entries.filter((e) => e.staged).length
  const isEmpty = commitMessage.trim().length === 0
  const isDisabled = stagedCount === 0 || isEmpty || isCommitting

  // Auto-height: expand up to ~6 lines
  const adjustHeight = useCallback(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    const lineHeight = parseFloat(getComputedStyle(el).lineHeight) || 20
    // 6 lines + vertical padding
    const maxHeight = lineHeight * 6 + 16
    el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`
    // Show scrollbar when content exceeds max height
    el.style.overflowY = el.scrollHeight > maxHeight ? 'auto' : 'hidden'
  }, [])

  useEffect(() => {
    adjustHeight()
  }, [commitMessage, adjustHeight])

  const handleCommit = async () => {
    if (isDisabled) return
    setIsCommitting(true)
    setError(null)
    try {
      await commit(commitMessage)
      setCommitMessage('')
      // Status refresh is handled by the git:status_changed event emitted
      // by the backend after a successful commit (picked up by useGitStatusEvents)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Commit failed')
    } finally {
      setIsCommitting(false)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Ctrl+Enter / Cmd+Enter to commit
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      handleCommit()
    }
  }

  const buttonLabel =
    stagedCount > 0
      ? `Commit ${stagedCount} file${stagedCount !== 1 ? 's' : ''}`
      : 'Commit'

  return (
    <div className="border-t border-border bg-muted/20 p-3">
      <textarea
        ref={textareaRef}
        value={commitMessage}
        onChange={(e) => {
          setCommitMessage(e.target.value)
          if (error) setError(null)
        }}
        onKeyDown={handleKeyDown}
        placeholder="Describe your changes..."
        rows={2}
        className={cn(
          'w-full resize-none rounded-md border bg-input px-3 py-2 text-sm',
          'text-foreground placeholder:text-muted-foreground',
          'focus:outline-none focus:ring-1 focus:ring-ring',
          'transition-colors',
          error ? 'border-destructive' : 'border-border',
        )}
      />

      {error && (
        <div className="mt-1.5 text-xs text-destructive">{error}</div>
      )}

      <div className="mt-2 flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {stagedCount > 0
            ? `${stagedCount} staged file${stagedCount !== 1 ? 's' : ''}`
            : 'No staged changes'}
        </span>
        <button
          type="button"
          onClick={handleCommit}
          disabled={isDisabled}
          className={cn(
            'inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
            'focus:outline-none focus:ring-1 focus:ring-ring',
            isDisabled
              ? 'bg-muted text-muted-foreground cursor-not-allowed'
              : 'bg-primary text-primary-foreground hover:bg-foreground/80',
          )}
        >
          {isCommitting && <Loader2 className="h-3 w-3 animate-spin" />}
          {buttonLabel}
        </button>
      </div>
    </div>
  )
}
