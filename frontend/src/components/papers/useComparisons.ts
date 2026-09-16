// Comparison-artifact loading for the paper Compare section.
//
// Multi-paper comparisons live at `<research-root>/comparisons/<slug>.md` — a
// global subdirectory of the research root, beside the paper library (a
// comparison spans papers from across the library, so it belongs to no single
// paper directory). The workspace reads every comparison straight from disk
// through the workspace RPCs; nothing here is persisted in a store (the Compare
// section is rendered inside the virtual paper tab).
//
// The directory is listed ONCE and every present `*.md` file is read, so an
// absent comparisons directory is a clean empty state rather than an error, and
// a partially written set still renders what exists.

import { useEffect, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'

/** The research-root subdirectory holding comparison artifacts. Mirrors
 *  backend `config.ComparisonDirName`. */
export const COMPARISONS_DIRNAME = 'comparisons'

/** One loaded comparison artifact. */
export interface ComparisonArtifact {
  /** The artifact's slug (its file name without the `.md` extension). */
  slug: string
  fileName: string
  content: string
  error: string | null
}

export interface ComparisonsState {
  /** The absolute comparisons directory ('' when no research root is known). */
  dir: string
  loading: boolean
  items: ComparisonArtifact[]
}

function joinPath(dir: string, name: string): string {
  return `${dir.replace(/[\\/]+$/, '')}/${name}`
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** Join a research root with the comparisons subdirectory ('' when the root is
 *  unknown). Exported so the Compare section can render the path it looked at. */
export function comparisonsDirFor(researchRoot: string): string {
  const root = researchRoot.replace(/[\\/]+$/, '')
  return root === '' ? '' : joinPath(root, COMPARISONS_DIRNAME)
}

/**
 * Load every comparison artifact under `<researchRoot>/comparisons`. A listing
 * failure (typically a not-yet-created directory) is non-fatal: the state
 * degrades to an empty item list so the Compare section renders its empty
 * state. `refreshKey` (optional) forces a reload when it changes — pass a
 * value that bumps on library refresh (e.g. the paper store's `lastSyncAt`) so
 * a newly written comparison appears without reopening the tab.
 */
export function useComparisons(researchRoot: string, refreshKey = 0): ComparisonsState {
  const dir = comparisonsDirFor(researchRoot)
  const [state, setState] = useState<ComparisonsState>(() => ({
    dir,
    loading: dir !== '',
    items: [],
  }))

  useEffect(() => {
    if (dir === '') {
      setState({ dir: '', loading: false, items: [] })
      return
    }
    let cancelled = false
    setState({ dir, loading: true, items: [] })
    void (async () => {
      try {
        const entries = await listDirectory(dir)
        if (cancelled) return
        const files = entries
          .filter((entry) => !entry.is_dir && entry.name.toLowerCase().endsWith('.md'))
          .sort((a, b) => a.name.localeCompare(b.name))
        const items = await Promise.all(
          files.map(async (file): Promise<ComparisonArtifact> => {
            const slug = file.name.replace(/\.md$/i, '')
            try {
              const content = await readFile(joinPath(dir, file.name))
              return { slug, fileName: file.name, content, error: null }
            } catch (err) {
              return { slug, fileName: file.name, content: '', error: messageOf(err) }
            }
          }),
        )
        if (!cancelled) setState({ dir, loading: false, items })
      } catch (err) {
        logger.warn('Failed to list the comparisons directory:', err)
        if (!cancelled) setState({ dir, loading: false, items: [] })
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dir, refreshKey])

  return state
}
