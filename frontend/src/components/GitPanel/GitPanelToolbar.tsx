import { Settings } from 'lucide-react'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { BranchDropdown } from './BranchDropdown'

/**
 * Top toolbar of the Git panel — spans the full panel width and stays visible
 * across all tabs (changes / history).
 *
 * - Left: the {@link BranchDropdown} field — clicking it opens the inline
 *   branch list (not the modal).
 * - Right: a manage icon-button that opens the full `BranchPicker` modal
 *   (switch / create / manage branches).
 */
export function GitPanelToolbar() {
  const openBranchPicker = useGitPanelStore((s) => s.openBranchPicker)

  return (
    <div className="flex items-center min-h-[32px] shrink-0 border-b border-border bg-secondary/30">
      <div className="min-w-0 flex-1">
        <BranchDropdown />
      </div>
      <button
        type="button"
        onClick={openBranchPicker}
        title="Manage branches"
        aria-label="Manage branches"
        className="mr-1 flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
      >
        <Settings className="size-3.5" />
      </button>
    </div>
  )
}
