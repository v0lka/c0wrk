// Paper-artifact loading for the paper workspace tab.
//
// A paper directory (`<research-root>/papers/<slug>/`) holds the per-paper
// artifacts the study-paper skill writes (see specs/domains/papers.md § Artifact
// Layout): the identity card (`paper.md`, parsed into the library record, not
// read here), the evidence note (`note.md`), the appraisal sheet
// (`appraisal.md`), the flashcard deck (`flashcards.md`, Teach mode), the
// extracted source text (`source.md`), the literature-context note
// (`literature.md`), a per-paper comparison note (`comparison.md`, the older
// single-paper form), and the literature-neighbourhood graph (`literature.json`,
// read by `usePaperLiterature`). Multi-paper comparisons are NOT per-paper files
// — they live at `<research-root>/comparisons/<slug>.md` and are loaded by
// `useComparisons`. The workspace reads the per-paper artifacts straight from
// disk through the workspace RPCs; nothing here is persisted in a store (the
// tab is virtual).
//
// The directory is listed ONCE to learn which candidate files exist, then only
// the present ones are read — so an absent artifact is a clean empty state
// rather than a read error, and a partially studied paper still renders. A
// LISTING failure, by contrast, is surfaced as an error state (never as "no
// artifacts"), so an unreadable paper directory is distinguishable from a paper
// that simply has no artifacts.

import { useEffect, useRef, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'

export type PaperSectionId =
  | 'note'
  | 'appraisal'
  | 'compare'
  | 'flashcards'
  | 'source'
  | 'literature'
  | 'html'

/** Candidate artifact file names per section, in preference order. Every section
 *  has a candidate the study-paper skill writes; the `source` / `literature` /
 *  per-paper `compare` files are OPTIONAL (a partially studied paper simply has
 *  none), so an absent candidate degrades cleanly to the section's empty state. */
export const PAPER_SECTION_FILES: Record<PaperSectionId, readonly string[]> = {
  note: ['note.md'],
  appraisal: ['appraisal.md'],
  // A per-paper comparison note (the older single-paper form). Multi-paper
  // comparisons are research-root-level (`<research-root>/comparisons/<slug>.md`)
  // and loaded by `useComparisons`.
  compare: ['comparison.md', 'comparison-matrix.md'],
  flashcards: ['flashcards.md'],
  // The extracted source text and the literature-context note — optional
  // per-paper artifacts written by the study-paper skill.
  source: ['source.md'],
  literature: ['literature.md', 'literature-context.md'],
  // The fetched original HTML (LaTeXML-shaped export, see core/papers'
  // FetchOriginalHTML) with its localized `assets/` — the rendered-paper
  // sub-view of the Source section. Not a workspace section of its own.
  html: ['paper.html'],
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
  /** A read error message when the file exists but could not be read, or a
   *  directory-listing error (returned on every section when the listing
   *  failed). */
  error: string | null
}

export type PaperArtifacts = Record<PaperSectionId, PaperArtifact>

function absent(): PaperArtifact {
  return { fileName: '', content: '', loading: false, missing: true, error: null }
}

function pending(): PaperArtifact {
  return { fileName: '', content: '', loading: true, missing: false, error: null }
}

function errored(message: string): PaperArtifact {
  return { fileName: '', content: '', loading: false, missing: false, error: message }
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
 * loading / missing / error).
 *
 * `refreshKey` re-runs the load when it changes — the paper workspace passes the
 * paper store's last-sync stamp, so a `papers:changed` refetch (e.g. after the
 * study-paper skill appends sections on a deepen) rebuilds the section set. A
 * re-run for the SAME directory keeps the previously loaded content in place
 * until the fresh read resolves (only the first load for a directory shows the
 * loading placeholders), so the workspace never flashes "Loading…" and resolved
 * anchors stay clickable mid-refresh.
 */
export function usePaperArtifacts(dir: string, refreshKey: number = 0): PaperArtifacts {
  const [artifacts, setArtifacts] = useState<PaperArtifacts>(() => allOf(absent))
  // The directory whose artifacts are currently loaded ('' when none). Only a
  // change of directory re-enters the loading state.
  const loadedDirRef = useRef<string | null>(null)

  useEffect(() => {
    if (dir === '') {
      loadedDirRef.current = null
      setArtifacts(allOf(absent))
      return
    }
    let cancelled = false
    if (loadedDirRef.current !== dir) setArtifacts(allOf(pending))
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
        if (cancelled) return
        loadedDirRef.current = dir
        setArtifacts(next)
      } catch (err) {
        logger.warn('Failed to list the paper directory:', err)
        if (cancelled) return
        // A directory-listing failure is a FAILURE, not "no artifacts": surface
        // it as a per-section error (missing stays false) so the workspace
        // renders it instead of a blank paper that looks like an empty one.
        const message = messageOf(err)
        setArtifacts(allOf(() => errored(message)))
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dir, refreshKey])

  return artifacts
}
