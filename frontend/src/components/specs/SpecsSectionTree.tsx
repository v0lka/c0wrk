// The specs tree grouped by top-level section (architecture/, domains/,
// contracts/, decisions/ — plus the root files INDEX.md / META.md). Every
// file row opens in the file viewer through the panel's onOpen callback
// (specs-root-relative; the panel resolves it against the absolute root).

import { FileText, FolderOpen } from 'lucide-react'
import type { SpecFile } from '@/types/models'
import { groupFilesBySection } from './specsPaths'

function fileLabel(file: SpecFile): string {
  return file.title !== undefined && file.title !== '' ? file.title : file.path
}

/** One file row: the title (or path when the file carries no heading) with
 *  the relative path on the tooltip, plus a Key Files count chip when the
 *  spec lists any. */
function FileRow({
  file,
  onOpen,
}: {
  file: SpecFile
  onOpen: (relPath: string) => void
}) {
  const keyFiles = file.key_files ?? []
  return (
    <button
      type="button"
      data-testid="specs-file"
      data-path={file.path}
      title={file.path}
      onClick={() => onOpen(file.path)}
      className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left text-xs text-foreground transition-colors hover:bg-muted"
    >
      <FileText className="size-3 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">{fileLabel(file)}</span>
      {keyFiles.length > 0 && (
        <span
          className="shrink-0 rounded bg-secondary px-1 text-xs text-muted-foreground"
          title={`${keyFiles.length} key files`}
        >
          {keyFiles.length}
        </span>
      )}
    </button>
  )
}

/** One section: the directory header (the root files group is labeled
 *  "root") and its file rows in parse order. */
function SectionGroup({
  section,
  files,
  onOpen,
}: {
  section: string
  files: SpecFile[]
  onOpen: (relPath: string) => void
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1 px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        <FolderOpen className="size-3" />
        {section === '' ? 'root' : section}
        <span className="text-muted-foreground/60">({files.length})</span>
      </div>
      {files.map((file) => (
        <FileRow key={file.path} file={file} onOpen={onOpen} />
      ))}
    </div>
  )
}

/** The tree section: files grouped by section via the pure helper (stable
 *  first-appearance order), every row clickable. */
export function SpecsSectionTree({
  files,
  onOpen,
}: {
  files: SpecFile[]
  onOpen: (relPath: string) => void
}) {
  const groups = groupFilesBySection(files)
  return (
    <section data-testid="specs-tree" className="flex flex-col gap-1.5">
      {groups.map((group) => (
        <SectionGroup
          key={group.section}
          section={group.section}
          files={group.files}
          onOpen={onOpen}
        />
      ))}
    </section>
  )
}
