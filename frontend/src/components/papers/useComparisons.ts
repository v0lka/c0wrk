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
// a partially written set still renders what exists. A `refreshKey` re-run for
// the SAME directory keeps the previously loaded set in place (a library sync
// must not unmount an open matrix); only a directory CHANGE re-enters the
// loading state.

import { useEffect, useRef, useState } from 'react'
import { listDirectory, readFile } from '@/api/workspace'
import { logger } from '@/lib/logger'

/** The research-root subdirectory holding comparison artifacts. Mirrors
 *  backend `config.ComparisonDirName`. */
export const COMPARISONS_DIRNAME = 'comparisons'

/** One loaded comparison artifact. */
export interface ComparisonArtifact {
  /** The artifact's slug (its file name without the `.md` extension). */
  slug: string
  content: string
  /** A read error message when the file exists but could not be read. */
  error: string | null
}

export interface ComparisonsState {
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
 *  unknown). */
function comparisonsDirFor(researchRoot: string): string {
  const root = researchRoot.replace(/[\\/]+$/, '')
  return root === '' ? '' : joinPath(root, COMPARISONS_DIRNAME)
}

/**
 * Load every comparison artifact under `<researchRoot>/comparisons`. A listing
 * failure (typically a not-yet-created directory) is non-fatal: on a FIRST load
 * the state degrades to an empty item list; on a REFRESH the previously rendered
 * set is kept rather than downgraded. `refreshKey` (optional) forces a reload
 * when it changes — pass a value that bumps on library refresh (e.g. the paper
 * store's `lastSyncAt`) so a newly written comparison appears without reopening
 * the tab.
 */
export function useComparisons(researchRoot: string, refreshKey = 0): ComparisonsState {
  const dir = comparisonsDirFor(researchRoot)
  const [state, setState] = useState<ComparisonsState>(() => ({
    loading: dir !== '',
    items: [],
  }))
  // The directory whose set is currently loaded ('' when none). Only a change of
  // directory discards the loaded set.
  const loadedDirRef = useRef<string | null>(null)

  useEffect(() => {
    if (dir === '') {
      loadedDirRef.current = null
      setState({ loading: false, items: [] })
      return
    }
    let cancelled = false
    if (loadedDirRef.current !== dir) {
      // A new directory: nothing to keep, show the loader with an empty set.
      setState({ loading: true, items: [] })
    } else {
      // A refresh of the SAME directory: keep the rendered set (an open matrix
      // must not unmount / flash "Loading…" on every library sync).
      setState((prev) => ({ loading: true, items: prev.items }))
    }
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
              return { slug, content, error: null }
            } catch (err) {
              return { slug, content: '', error: messageOf(err) }
            }
          }),
        )
        if (cancelled) return
        loadedDirRef.current = dir
        setState({ loading: false, items })
      } catch (err) {
        logger.warn('Failed to list the comparisons directory:', err)
        if (cancelled) return
        // Keep what was already rendered when this was a refresh of the same
        // directory; only a first-load failure degrades to the empty state.
        setState((prev) =>
          loadedDirRef.current === dir ? { loading: false, items: prev.items } : { loading: false, items: [] },
        )
      }
    })()
    return () => {
      cancelled = true
    }
  }, [dir, refreshKey])

  return state
}
