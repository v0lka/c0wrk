import { useCallback } from 'react'
import { useFileViewerStore } from '@/stores/fileViewerStore'

interface FileLinkProps {
  path: string
  line?: number
  label?: string
  className?: string
  /**
   * Render the native `title` (full path) hint. Defaults to `true`. Set to
   * `false` when the link sits inside a tooltip-providing parent (e.g. the tool
   * card title) to avoid a duplicate hint.
   */
  nativeTitle?: boolean
}

export function FileLink({ path, line, label, className, nativeTitle = true }: FileLinkProps) {
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
      title={nativeTitle ? path : undefined}
      className={`text-info hover:underline cursor-pointer font-mono text-xs ${className ?? ''}`}
      onClick={handleClick}
      onKeyDown={(e) => { if (e.key === 'Enter') handleClick(e as unknown as React.MouseEvent) }}
    >
      {displayName}
    </span>
  )
}
