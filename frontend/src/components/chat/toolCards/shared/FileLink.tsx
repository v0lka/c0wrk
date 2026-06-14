import { useCallback } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'

interface FileLinkProps {
  path: string
  line?: number
  label?: string
  className?: string
}

export function FileLink({ path, line, label, className }: FileLinkProps) {
  const displayName = label ?? path.split('/').pop() ?? path

  const handleClick = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    const store = useFileViewerStore.getState()
    if (line !== undefined) {
      store.openFileAtLine(path, line)
    } else {
      store.openFile(path)
    }
  }, [path, line])

  return (
    <span
      role="button"
      tabIndex={0}
      title={path}
      className={`text-info hover:underline cursor-pointer font-mono text-xs ${className ?? ''}`}
      onClick={handleClick}
      onKeyDown={(e) => { if (e.key === 'Enter') handleClick(e as unknown as React.MouseEvent) }}
    >
      {displayName}
    </span>
  )
}
