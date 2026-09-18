// Literature-graph artifact loading for the paper workspace.
//
// The study-paper literature helper writes `<paper-dir>/literature.json` (run
// through the managed Python interpreter — see backend RunPaperLiterature). This
// hook reads that file from disk, distinguishing "absent" (the helper never ran,
// or ran offline and wrote nothing) from "unreadable" (the file exists but the
// read failed), so the workspace can degrade honestly. Nothing here is persisted
// — the paper tab is virtual, and the file is the single source of truth.
//
// The directory is listed ONCE to learn whether the file exists, then only the
// present file is read (mirrors usePaperArtifacts), and `reload()` re-runs the
// probe after a successful lookup writes a fresh file. The probe is ALSO keyed
// to the paper store's library sync (its `lastSyncAt`), so a `literature.json`
// written by the helper in a chat session (which emits `papers:changed`) refreshes
// the graph without the user reopening the tab — the same refresh contract the
// sibling `usePaperArtifacts`/`useComparisons` loaders honour.

import { useCallback, useEffect, useRef, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'
import { selectPapersSyncAt, usePaperStore } from '@/stores/paperStore'

/** The literature helper's output file name inside a paper directory. */
export const LITERATURE_JSON_FILE = 'literature.json'

export interface LiteratureArtifact {
  /** The file's raw JSON content ('' while loading or when absent). */
  raw: string
  loading: boolean
  /** True when the file does not exist on disk. */
  missing: boolean
  /** A read error message when the file exists but could not be read. */
  readError: string | null
}

const ABSENT: LiteratureArtifact = { raw: '', loading: false, missing: true, readError: null }
const PENDING: LiteratureArtifact = { raw: '', loading: true, missing: false, readError: null }

function joinPath(dir: string, name: string): string {
  return `${dir.replace(/[\\/]+$/, '')}/${name}`
}

function messageOf(err: unknown): string {
  return String(err instanceof Error ? err.message : err)
}

/**
 * Load `<dir>/literature.json`. A directory-listing failure degrades to
 * "missing" (non-fatal); a read failure on a present file surfaces as
 * `readError`. `reload` re-probes, keyed by an internal nonce.
 *
 * `refreshKey` re-probes when it changes. When omitted, the hook falls back to
 * the paper store's library-sync stamp, so a skill-written `literature.json`
 * refreshes the graph automatically (the paper workspace need not thread one).
 */
export function usePaperLiterature(
  dir: string,
  refreshKey?: number,
): {
  artifact: LiteratureArtifact
  reload: () => void
} {
  const [artifact, setArtifact] = useState<LiteratureArtifact>(ABSENT)
  const [nonce, setNonce] = useState(0)
  const syncAt = usePaperStore(selectPapersSyncAt)
  const key = refreshKey ?? syncAt
  // The directory whose artifact is currently loaded ('' when none). Only a
  // change of directory re-enters the PENDING state; a re-probe of the SAME
  // directory (a library-sync key bump or an explicit reload()) keeps the
  // previously loaded artifact mounted until the fresh read resolves — the
  // sibling usePaperArtifacts/useComparisons refresh contract.
  const loadedDirRef = useRef<string | null>(null)

  const reload = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    if (dir === '') {
      loadedDirRef.current = null
      setArtifact(ABSENT)
      return
    }
    let cancelled = false
    if (loadedDirRef.current !== dir) setArtifact(PENDING)
    void (async () => {
      try {
        const entries = await listDirectory(dir)
        if (cancelled) return
        loadedDirRef.current = dir
        const present = entries.some((entry) => !entry.is_dir && entry.name === LITERATURE_JSON_FILE)
        if (!present) {
          setArtifact(ABSENT)
          return
        }
        try {
          const raw = await readFile(joinPath(dir, LITERATURE_JSON_FILE))
          if (!cancelled) setArtifact({ raw, loading: false, missing: false, readError: null })
        } catch (err) {
          if (!cancelled) {
            setArtifact({ raw: '', loading: false, missing: false, readError: messageOf(err) })
          }
        }
      } catch (err) {
        logger.warn('Failed to list the paper directory for the literature graph:', err)
        if (!cancelled) setArtifact(ABSENT)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dir, nonce, key])

  return { artifact, reload }
}
