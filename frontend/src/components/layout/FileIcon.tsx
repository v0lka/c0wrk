import { useMemo } from 'react'

// Nerd Font icon codes and seti color palette
// We avoid importing @m234/nerd-fonts/fs-collections/seti because it depends on node:path
const colors = {
  blue: '#519ABA',
  grey: '#4D5A5E',
  greyLight: '#6D8086',
  green: '#8DC149',
  orange: '#E37933',
  pink: '#F55385',
  purple: '#A074C4',
  red: '#CC3E44',
  white: '#D4D7D6',
  yellow: '#CBCB41',
  ignore: '#41535B',
} as const

interface IconDef {
  glyph: string
  color: string
}

// Folder icons
const FOLDER_CLOSED = '\ue5ff'
const FOLDER_OPEN = '\ue5fe'
const FOLDER_COLOR = '#6D8086'

// Default file icon
const DEFAULT_ICON: IconDef = { glyph: '\ue612', color: colors.white }

// Extension to icon mapping (seti-based nerd font codepoints)
const extensionMap: Record<string, IconDef> = {
  // Go
  '.go': { glyph: '\ue627', color: colors.blue },
  // JavaScript / TypeScript
  '.js': { glyph: '\ue74e', color: colors.yellow },
  '.mjs': { glyph: '\ue74e', color: colors.yellow },
  '.cjs': { glyph: '\ue74e', color: colors.yellow },
  '.jsx': { glyph: '\ue7ba', color: colors.blue },
  '.ts': { glyph: '\ue628', color: colors.blue },
  '.tsx': { glyph: '\ue7ba', color: colors.blue },
  // Web
  '.html': { glyph: '\ue736', color: colors.orange },
  '.htm': { glyph: '\ue736', color: colors.orange },
  '.css': { glyph: '\ue749', color: colors.blue },
  '.scss': { glyph: '\ue749', color: colors.pink },
  '.less': { glyph: '\ue749', color: colors.blue },
  '.svg': { glyph: '\ue698', color: colors.purple },
  // Data
  '.json': { glyph: '\ue60b', color: colors.yellow },
  '.yaml': { glyph: '\ue60b', color: colors.purple },
  '.yml': { glyph: '\ue60b', color: colors.purple },
  '.toml': { glyph: '\ue60b', color: colors.greyLight },
  '.xml': { glyph: '\ue619', color: colors.orange },
  '.csv': { glyph: '\ue60b', color: colors.green },
  // Markdown / Text
  '.md': { glyph: '\ue73e', color: colors.blue },
  '.mdx': { glyph: '\ue73e', color: colors.blue },
  '.txt': { glyph: '\ue612', color: colors.white },
  '.rst': { glyph: '\ue612', color: colors.white },
  // Python
  '.py': { glyph: '\ue73c', color: colors.blue },
  '.pyx': { glyph: '\ue73c', color: colors.blue },
  // Rust
  '.rs': { glyph: '\ue7a8', color: colors.greyLight },
  // Ruby
  '.rb': { glyph: '\ue739', color: colors.red },
  // Shell
  '.sh': { glyph: '\ue795', color: colors.orange },
  '.bash': { glyph: '\ue795', color: colors.orange },
  '.zsh': { glyph: '\ue795', color: colors.orange },
  '.fish': { glyph: '\ue795', color: colors.green },
  // Config
  '.env': { glyph: '\ue60b', color: colors.yellow },
  '.ini': { glyph: '\ue60b', color: colors.greyLight },
  '.cfg': { glyph: '\ue60b', color: colors.greyLight },
  '.conf': { glyph: '\ue60b', color: colors.greyLight },
  // Docker
  '.dockerfile': { glyph: '\ue7b0', color: colors.blue },
  // Images
  '.png': { glyph: '\ue60d', color: colors.purple },
  '.jpg': { glyph: '\ue60d', color: colors.purple },
  '.jpeg': { glyph: '\ue60d', color: colors.purple },
  '.gif': { glyph: '\ue60d', color: colors.purple },
  '.ico': { glyph: '\ue60d', color: colors.purple },
  '.webp': { glyph: '\ue60d', color: colors.purple },
  // Lock / Sum
  '.sum': { glyph: '\ue60b', color: colors.ignore },
  '.lock': { glyph: '\ue60b', color: colors.ignore },
  // Misc
  '.mod': { glyph: '\ue627', color: colors.blue },
  '.sql': { glyph: '\ue706', color: colors.pink },
  '.graphql': { glyph: '\ue662', color: colors.pink },
  '.proto': { glyph: '\ue60b', color: colors.red },
  '.wasm': { glyph: '\ue6a1', color: colors.purple },
  '.out': { glyph: '\ue612', color: colors.ignore },
}

// Exact filename matches
const filenameMap: Record<string, IconDef> = {
  '.gitignore': { glyph: '\ue702', color: colors.red },
  '.gitmodules': { glyph: '\ue702', color: colors.red },
  '.gitattributes': { glyph: '\ue702', color: colors.red },
  '.editorconfig': { glyph: '\ue60b', color: colors.white },
  'Makefile': { glyph: '\ue60b', color: colors.greyLight },
  'makefile': { glyph: '\ue60b', color: colors.greyLight },
  'Dockerfile': { glyph: '\ue7b0', color: colors.blue },
  'dockerfile': { glyph: '\ue7b0', color: colors.blue },
  'docker-compose.yml': { glyph: '\ue7b0', color: colors.blue },
  'docker-compose.yaml': { glyph: '\ue7b0', color: colors.blue },
  'package.json': { glyph: '\ue71e', color: colors.green },
  'package-lock.json': { glyph: '\ue71e', color: colors.ignore },
  'tsconfig.json': { glyph: '\ue628', color: colors.blue },
  'go.mod': { glyph: '\ue627', color: colors.blue },
  'go.sum': { glyph: '\ue627', color: colors.ignore },
  'LICENSE': { glyph: '\ue60a', color: colors.yellow },
  'README.md': { glyph: '\ue73e', color: colors.blue },
  'TODO.md': { glyph: '\ue612', color: colors.blue },
}

function getFileIconDef(name: string): IconDef {
  // Check exact filename match first
  const byName = filenameMap[name]
  if (byName) return byName

  // Try extension matching (longest match first)
  const dotIdx = name.indexOf('.')
  if (dotIdx < 0) return DEFAULT_ICON

  let ext = name.slice(dotIdx)
  while (ext) {
    const found = extensionMap[ext.toLowerCase()]
    if (found) return found
    const nextDot = ext.indexOf('.', 1)
    if (nextDot === -1) break
    ext = ext.slice(nextDot)
  }

  return DEFAULT_ICON
}

interface FileIconProps {
  name: string
  isDir: boolean
  isOpen?: boolean
}

export function FileIcon({ name, isDir, isOpen }: FileIconProps) {
  const { glyph, color } = useMemo(() => {
    if (isDir) {
      return {
        glyph: isOpen ? FOLDER_OPEN : FOLDER_CLOSED,
        color: FOLDER_COLOR,
      }
    }
    return getFileIconDef(name)
  }, [name, isDir, isOpen])

  return (
    <span
      className="nerd-font-icon inline-block w-5 text-center text-base leading-none flex-shrink-0"
      style={{ color }}
      aria-hidden
    >
      {glyph}
    </span>
  )
}
