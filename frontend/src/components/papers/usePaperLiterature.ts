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
// probe after a successful lookup writes a fresh file.

import { useCallback, useEffect, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'

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
  return err instanceof Error ? err.message : String(err)
}

/**
 * Load `<dir>/literature.json`. A directory-listing failure degrades to
 * "missing" (non-fatal); a read failure on a present file surfaces as
 * `readError`. `reload` re-probes, keyed by an internal nonce.
 */
export function usePaperLiterature(dir: string): {
  artifact: LiteratureArtifact
  reload: () => void
} {
  const [artifact, setArtifact] = useState<LiteratureArtifact>(ABSENT)
  const [nonce, setNonce] = useState(0)

  const reload = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    if (dir === '') {
      setArtifact(ABSENT)
      return
    }
    let cancelled = false
    setArtifact(PENDING)
    void (async () => {
      try {
        const entries = await listDirectory(dir)
        if (cancelled) return
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
  }, [dir, nonce])

  return { artifact, reload }
}
