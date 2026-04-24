import { cn } from '@/lib/utils'

const DEFAULT_FILE_ICON = '\uf15b'

interface FileIconProps {
  isDir?: boolean
  icon?: string
  iconColor?: string
  className?: string
}

export function FileIcon({ isDir = false, icon, iconColor, className }: FileIconProps) {
  if (!icon && isDir) {
    return <span className={cn('inline-block w-4', className)} aria-hidden="true" />
  }

  const resolvedIcon = icon || DEFAULT_FILE_ICON
  const resolvedColor = icon ? iconColor : undefined

  return (
    <span
      className={cn('nerd-font-icon inline-block w-4 text-center text-sm leading-none', className)}
      style={resolvedColor ? { color: resolvedColor } : undefined}
      aria-hidden="true"
    >
      {resolvedIcon}
    </span>
  )
}
