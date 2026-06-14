import { useEffect, useState } from 'react'
import { getFileIcon } from '@/api/workspace'
import { useFileViewerStore } from '@/stores/fileViewerStore'

type IconEntry = { icon: string; icon_color: string }

export function useFileIcon(filePath: string) {
  const cached = useFileViewerStore((s) => s.fileIcons[filePath])
  const setFileIcon = useFileViewerStore((s) => s.setFileIcon)

  const [fetched, setFetched] = useState<IconEntry | null>(null)

  useEffect(() => {
    if (cached) return

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
