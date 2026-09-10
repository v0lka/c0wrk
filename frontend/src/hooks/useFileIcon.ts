import { useEffect, useState } from 'react'
import { getFileIcon } from '@/api/workspace'
import { useFileViewerStore } from '@/stores/fileViewerStore'

type IconEntry = { icon: string; icon_color: string }

/** Prefix of synthetic pseudo-paths reserved for c0wrk virtual tabs (e.g.
 *  `c0wrk:research`, `c0wrk:review`, `c0wrk:commit:<sha>` — see
 *  RESEARCH_TAB_PATH / REVIEW_TAB_PATH). These are not filesystem paths:
 *  the hook must never fire the getFileIcon RPC for them nor persist a
 *  `fileIcons` cache entry keyed by them. Guarded on the PREFIX, not any
 *  single constant, so future pseudo-tabs are covered automatically. */
const PSEUDO_PATH_PREFIX = 'c0wrk:'

export function useFileIcon(filePath: string) {
  const cached = useFileViewerStore((s) => s.fileIcons[filePath])
  const setFileIcon = useFileViewerStore((s) => s.setFileIcon)

  const [fetched, setFetched] = useState<IconEntry | null>(null)

  useEffect(() => {
    // Pseudo-paths (`c0wrk:…`) have no on-disk file to resolve an icon for —
    // bail before the RPC so neither a request nor a junk cache entry is
    // made. (Consumers render a dedicated icon for known pseudo-tabs.)
    if (cached || filePath.startsWith(PSEUDO_PATH_PREFIX)) return

    let cancelled = false
    getFileIcon(filePath)
      .then((res) => {
        if (cancelled) return
        setFileIcon(filePath, res.icon, res.icon_color)
        setFetched(res)
      })
      .catch(() => {
        // ignore
      })

    return () => {
      cancelled = true
    }
  }, [filePath, cached, setFileIcon])

  // Derive synchronously from cache or from async fetch result
  return cached
    ? { icon: cached.icon, icon_color: cached.iconColor }
    : fetched
}
