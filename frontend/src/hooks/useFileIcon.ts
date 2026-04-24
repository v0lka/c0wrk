import { useEffect, useState } from 'react'
import { getFileIcon } from '@/api/workspace'
import { useFileViewerStore } from '@/stores/fileViewerStore'

type IconEntry = { icon: string; icon_color: string }

export function useFileIcon(filePath: string) {
  const cached = useFileViewerStore((s) => s.fileIcons[filePath])
  const setFileIcon = useFileViewerStore((s) => s.setFileIcon)

  const [result, setResult] = useState<IconEntry | null>(
    cached ? { icon: cached.icon, icon_color: cached.iconColor } : null,
  )

  useEffect(() => {
    if (cached) {
      setResult({ icon: cached.icon, icon_color: cached.iconColor })
      return
    }

    let cancelled = false
    getFileIcon(filePath)
      .then((res) => {
        if (cancelled) return
        setFileIcon(filePath, res.icon, res.icon_color)
        setResult(res)
      })
      .catch(() => {
        // ignore
      })

    return () => {
      cancelled = true
    }
  }, [filePath, cached, setFileIcon])

  return result
}
