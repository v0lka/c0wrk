// Paper-artifact loading for the paper workspace tab.
//
// A paper directory (`<research-root>/papers/<slug>/`) holds the identity card
// (paper.md), the evidence note (note.md) and the appraisal sheet (appraisal.md)
// written by the backend (see core/papers), plus optional artifacts the
// study-paper skill may add — the extracted source text (source.md) and the
// teach/compare/literature companions. The workspace reads them straight from
// disk through the workspace RPCs; nothing here is persisted in a store (the
// tab is virtual).
//
// The directory is listed ONCE to learn which candidate files exist, then only
// the present ones are read — so an absent artifact is a clean empty state
// rather than a read error, and a partially studied paper still renders.

import { useEffect, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'

export type PaperSectionId =
  | 'note'
  | 'appraisal'
  | 'compare'
  | 'flashcards'
  | 'source'
  | 'literature'

/** Candidate artifact file names per section, in preference order. */
export const PAPER_SECTION_FILES: Record<PaperSectionId, readonly string[]> = {
  note: ['note.md'],
  appraisal: ['appraisal.md'],
  compare: ['comparison.md', 'comparison-matrix.md'],
  flashcards: ['flashcards.md'],
  source: ['source.md'],
  literature: ['literature.md', 'literature-context.md'],
}

const SECTION_IDS = Object.keys(PAPER_SECTION_FILES) as PaperSectionId[]

export interface PaperArtifact {
  /** The candidate file name that was found ('' when none exists). */
  fileName: string
  /** The file's markdown content ('' while loading or when absent). */
  content: string
  loading: boolean
  /** True when none of the section's candidate files exist on disk. */
  missing: boolean
  /** A read error message when the file exists but could not be read. */
  error: string | null
}

export type PaperArtifacts = Record<PaperSectionId, PaperArtifact>

function absent(): PaperArtifact {
  return { fileName: '', content: '', loading: false, missing: true, error: null }
}

function pending(): PaperArtifact {
  return { fileName: '', content: '', loading: true, missing: false, error: null }
}

function allOf(make: () => PaperArtifact): PaperArtifacts {
  const out = {} as PaperArtifacts
  for (const id of SECTION_IDS) out[id] = make()
  return out
}

/** Join a directory and a child name with exactly one forward slash. */
function joinPath(dir: string, name: string): string {
  return `${dir.replace(/[\\/]+$/, '')}/${name}`
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * Load the paper directory's artifacts. Returns a per-section state (content /
 * loading / missing / error). A directory listing failure is non-fatal: every
 * section degrades to "missing" (the workspace renders empty states).
 *
 * `refreshKey` re-runs the load when it changes — the paper workspace passes the
 * paper store's last-sync stamp, so a `papers:changed` refetch (e.g. after the
 * study-paper skill appends sections on a deepen) rebuilds the section set. An
 * unchanged key (the default 0) keeps the single per-directory load.
 */
export function usePaperArtifacts(dir: string, refreshKey: number = 0): PaperArtifacts {
  const [artifacts, setArtifacts] = useState<PaperArtifacts>(() => allOf(absent))

  useEffect(() => {
    if (dir === '') {
      setArtifacts(allOf(absent))
      return
    }
    let cancelled = false
    setArtifacts(allOf(pending))
    void (async () => {
      try {
        const entries = await listDirectory(dir)
        if (cancelled) return
        const names = new Set(entries.filter((entry) => !entry.is_dir).map((entry) => entry.name))
        const next = allOf(absent)
        await Promise.all(
          SECTION_IDS.map(async (section) => {
            const fileName = PAPER_SECTION_FILES[section].find((candidate) => names.has(candidate))
            if (fileName === undefined) return
            try {
              const content = await readFile(joinPath(dir, fileName))
              next[section] = { fileName, content, loading: false, missing: false, error: null }
            } catch (err) {
              next[section] = {
                fileName,
                content: '',
                loading: false,
                missing: false,
                error: messageOf(err),
              }
            }
          }),
        )
        if (!cancelled) setArtifacts(next)
      } catch (err) {
        logger.warn('Failed to list the paper directory:', err)
        if (!cancelled) setArtifacts(allOf(absent))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dir, refreshKey])

  return artifacts
}
