// Source-section actions: fetch the paper's original arXiv HTML rendition
// (FetchPaperOriginal → <paper-dir>/paper.html + assets/) and open the arXiv
// abs page in the system browser.
//
// The fetch outcome is explicit data (offline / no_arxiv / not_found / error
// are conditions, not crashes) and renders as one distinct plain message per
// status — never an empty silent failure. The fetch action and its status
// line are mutually exclusive while work is in flight: clicking the button
// REPLACES it with the status (running → outcome), so a loading status never
// stacks on top of the action. Failure statuses are terminal, so the action
// returns alongside the message as the retry; the success message retires on
// its own once the fetched artifact is actually in the view (the watcher's
// papers:changed refetch lands it), at which point the action returns as
// "Reload HTML" — the next distinct action. `ok` writes paper.html inside the
// WATCHED library, so the file watcher's refetch (paperStore.lastSyncAt →
// usePaperArtifacts' refresh key) IS the artifact refresh; nothing reloads
// manually here. The browser action derives the abs URL from the card's
// identifiers (lib/paperArxivUrl) and hides itself when the card carries no
// usable arXiv id.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Globe, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { fetchPaperOriginal, type PaperOriginalStatus, type PaperRecord } from '@/api/papers'
import { openExternalURL } from '@/api/runtime'
import { arxivAbsUrl } from '@/lib/paperArxivUrl'
import { cn } from '@/lib/utils'
import { usePaperStore } from '@/stores/paperStore'

/** One plain message per status — distinct, honest, never empty. */
const FETCH_MESSAGES: Record<PaperOriginalStatus, string> = {
  ok: 'HTML rendition fetched to paper.html — the view refreshes when the library watcher reports it.',
  offline: 'arXiv could not be reached — check the network and try again.',
  no_arxiv: 'The paper card carries no arXiv identifier, so there is no HTML rendition to fetch.',
  not_found: 'No HTML rendition exists on arXiv for this paper.',
  error: 'Fetching the HTML rendition failed.',
}

/** The running label shown in place of the button while a fetch is in flight. */
const FETCH_RUNNING_MESSAGE = 'Fetching the HTML rendition…'

interface PaperSourceActionsProps {
  paper: PaperRecord
  /** paper.html is already on disk — the fetch action re-fetches. */
  hasHtml: boolean
  /** Called once a fetch resolves ok (the caller reveals the HTML sub-view). */
  onFetched: () => void
}

export function PaperSourceActions({ paper, hasHtml, onFetched }: PaperSourceActionsProps) {
  const projectId = usePaperStore((s) => s.projectId)
  const [running, setRunning] = useState(false)
  const [status, setStatus] = useState<PaperOriginalStatus | null>(null)
  /** Extra context for the message line (the resolved URL on ok, the thrown
   *  error's text on a transport failure); '' when none. */
  const [detail, setDetail] = useState('')
  // Identity token of the paper an in-flight fetch belongs to. Bumped on
  // paper/project change so a late resolution never lands on the next paper.
  const identityRef = useRef(0)
  useEffect(() => {
    identityRef.current += 1
    setRunning(false)
    setStatus(null)
    setDetail('')
  }, [paper.id, projectId])

  // The success message's job ends when the fetched document is actually in
  // the view (hasHtml flips once the watcher's refresh lands paper.html): the
  // line clears itself and the fetch action returns as "Reload HTML".
  useEffect(() => {
    if (status === 'ok' && hasHtml) {
      setStatus(null)
      setDetail('')
    }
  }, [status, hasHtml])

  const absUrl = useMemo(() => arxivAbsUrl(paper.identifiers), [paper.identifiers])

  const onFetch = useCallback(() => {
    if (projectId === null) {
      setStatus('error')
      setDetail('No active project.')
      return
    }
    const identity = identityRef.current
    const isCurrent = (): boolean => identityRef.current === identity
    setRunning(true)
    void (async () => {
      try {
        const result = await fetchPaperOriginal(projectId, paper.id)
        if (!isCurrent()) return
        setStatus(result.status)
        setDetail(result.status === 'ok' ? result.url : '')
        if (result.status === 'ok') onFetched()
      } catch (err) {
        if (!isCurrent()) return
        setStatus('error')
        setDetail(err instanceof Error ? err.message : String(err))
      } finally {
        if (isCurrent()) setRunning(false)
      }
    })()
  }, [projectId, paper.id, onFetched])

  // The action and its status never share the row while work is in flight:
  // running and the not-yet-landed success replace the button outright, so
  // the status renders in the button's place, never on top of it. A failure
  // is terminal — the action returns immediately next to its message as the
  // retry.
  const showFetchButton = !running && status !== 'ok'
  const statusValue: PaperOriginalStatus | 'running' | null = running ? 'running' : status

  const message =
    status === null
      ? ''
      : status === 'error' && detail !== ''
        ? `${FETCH_MESSAGES.error} — ${detail}`
        : FETCH_MESSAGES[status]

  return (
    <div
      data-testid="paper-source-actions"
      className="flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1"
    >
      {showFetchButton && (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 shrink-0 gap-1 px-2 text-[11px]"
          onClick={onFetch}
          data-testid="paper-source-fetch"
        >
          <RefreshCw className="size-3" />
          {hasHtml ? 'Reload HTML' : 'Load HTML original'}
        </Button>
      )}
      {statusValue !== null && (
        <span
          data-testid="paper-source-fetch-status"
          data-status={statusValue}
          title={detail !== '' ? detail : undefined}
          className={cn(
            'inline-flex min-w-0 flex-1 items-center gap-1 truncate text-[10px]',
            statusValue === 'ok'
              ? 'text-success'
              : statusValue === 'running'
                ? 'text-muted-foreground'
                : 'text-destructive',
          )}
        >
          {statusValue === 'running' && (
            <RefreshCw className="size-3 shrink-0 animate-spin" aria-hidden="true" />
          )}
          {statusValue === 'running' ? FETCH_RUNNING_MESSAGE : message}
        </span>
      )}
      {absUrl !== '' && (
        <Button
          variant="ghost"
          size="sm"
          className="ml-auto h-6 shrink-0 gap-1 px-2 text-[11px]"
          onClick={() => openExternalURL(absUrl)}
          data-testid="paper-source-open"
        >
          <Globe className="size-3" />
          Open in browser
        </Button>
      )}
    </div>
  )
}
