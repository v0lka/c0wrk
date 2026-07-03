import { useState, useRef, useEffect, useCallback } from 'react'
import { Loader2, Sparkles, Check } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { commit, generateCommitMessage } from '@/api/git'
import { getFileDiff } from '@/api/workspace'
import { cn } from '@/lib/utils'

export function CommitSection() {
  const entries = useGitPanelStore((s) => s.entries)
  const commitMessage = useGitPanelStore((s) => s.commitMessage)
  const setCommitMessage = useGitPanelStore((s) => s.setCommitMessage)

  const [isCommitting, setIsCommitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [successSha, setSuccessSha] = useState<string | null>(null)
  const successTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const stagedCount = entries.filter((e) => e.staged).length
  const isEmpty = commitMessage.trim().length === 0
  const isDisabled = stagedCount === 0 || isEmpty || isCommitting
  const isGenerating = useGitPanelStore((s) => s.isGeneratingCommit)
  const setGenerating = useGitPanelStore((s) => s.setGeneratingCommit)
  const isGenerateDisabled = stagedCount === 0 || isGenerating || isCommitting

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

  // Clear the success-banner timer on unmount to avoid leaking.
  useEffect(() => {
    return () => {
      if (successTimerRef.current !== null) {
        clearTimeout(successTimerRef.current)
        successTimerRef.current = null
      }
    }
  }, [])

  const handleCommit = async () => {
    if (isDisabled) return
    setIsCommitting(true)
    setError(null)
    if (successTimerRef.current !== null) {
      clearTimeout(successTimerRef.current)
      successTimerRef.current = null
    }
    try {
      // commit() now returns the new commit's full SHA (FE-1 / B1).
      const sha = await commit(commitMessage)
      setCommitMessage('')
      setSuccessSha(sha)
      // Auto-clear the success banner after a few seconds.
      successTimerRef.current = setTimeout(() => setSuccessSha(null), 4000)
      // Status refresh is handled by the git:status_changed event emitted
      // by the backend after a successful commit (picked up by useGitStatusEvents)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Commit failed')
    } finally {
      setIsCommitting(false)
    }
  }

  const handleGenerate = async () => {
    const stagedEntries = entries.filter((e) => e.staged)
    if (stagedEntries.length === 0 || isGenerating) return

    setGenerating(true)
    setError(null)
    try {
      // Collect the diff for each staged file. getFileDiff returns the
      // staged + unstaged diff for a path; failures for individual files
      // are swallowed so one bad file does not abort the whole request.
      const diffs = await Promise.all(
        stagedEntries.map((e) => getFileDiff(e.path).catch(() => '')),
      )
      const diff = diffs.filter((d) => d.trim().length > 0).join('\n')
      if (!diff.trim()) {
        setError('No staged changes to generate a commit message from')
        return
      }
      const message = await generateCommitMessage(diff)
      setCommitMessage(message)
    } catch (err) {
      setError(
        err instanceof Error ? err.message : 'Failed to generate commit message',
      )
    } finally {
      setGenerating(false)
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

      {error ? (
        <div className="mt-1.5 text-xs text-destructive">{error}</div>
      ) : successSha ? (
        <div className="mt-1.5 flex items-center gap-1.5 text-xs text-success">
          <Check className="size-3 shrink-0" />
          <span>
            Committed{' '}
            <span className="font-mono">{successSha.slice(0, 7)}</span>
          </span>
        </div>
      ) : null}

      <div className="mt-2 flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {stagedCount > 0
            ? `${stagedCount} staged file${stagedCount !== 1 ? 's' : ''}`
            : 'No staged changes'}
        </span>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={handleGenerate}
            disabled={isGenerateDisabled}
            title="Generate commit message with AI"
            aria-label="Generate commit message with AI"
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors',
              'focus:outline-none focus:ring-1 focus:ring-ring',
              isGenerateDisabled
                ? 'bg-muted text-muted-foreground cursor-not-allowed'
                : 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
            )}
          >
            {isGenerating ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <Sparkles className="h-3 w-3" />
            )}
            Generate
          </button>
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
    </div>
  )
}
