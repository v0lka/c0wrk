import { useState, useCallback } from "react";
import { GitBranch, ChevronDown, FolderTree, List } from "lucide-react";
import { useGitPanelStore } from "@/stores/gitPanelStore";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuCheckboxItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { MenuSearchInput } from "@/components/ui/MenuSearchInput";
import { Button } from "@/components/ui/button";
import { CollapsibleSectionHeader } from "@/components/ui/CollapsibleSectionHeader";
import { InlineRenameInput } from "@/components/ui/InlineRenameInput";
import { useBranchActions } from "@/hooks/useBranchActions";
import type { BranchActionKind } from "@/hooks/useBranchActions";
import { useSessionWorktreePin } from "@/hooks/useSessionWorktreePin";
import { useCollapsibleSections } from "@/hooks/useCollapsibleSections";
import { useWorktreeActions } from "@/hooks/useWorktreeActions";
import { focusWorktree } from "@/lib/gitFocus";
import { BranchList } from "./BranchList";
import { branchRegionId } from "./branchRegionId";
import { BranchDeleteConfirmDialog } from "./BranchDeleteConfirmDialog";
import { WorktreeDeleteConfirmDialog } from "./WorktreeDeleteConfirmDialog";
import { WorktreesSection } from "./WorktreesSection";

/**
 * Inline branch dropdown — the git panel's branch field.
 *
 * Modeled on {@link SessionSelector}: a `DropdownMenu` anchored to the current
 * branch indicator. Clicking the field opens the list; the search-input line
 * carries a tree/flat display-mode toggle (modeled on
 * {@link SessionSelectorToolbar} — a registered `DropdownMenuCheckboxItem`
 * whose `onSelect` preventDefaults, so flipping the mode never closes the
 * menu while `aria-checked` announces the current mode; the
 * icon shows the mode you switch TO and renders even below the 5-branch
 * threshold, where the search box itself is hidden). Branches are grouped into
 * **Local** (rows
 * expose hover mini-icons for push/merge/rebase/rename/delete) and **Remote**
 * (click checks the branch out as a new local tracking branch; hover exposes
 * delete-remote). A **Worktrees** section switches the panel's FOCUS between
 * every worktree of the project (local checkout, managed session trees,
 * external trees) — a focus switch, never a checkout — and exposes a hover
 * delete on the linked trees (managed + external; never the checkout) that
 * runs through the mandatory {@link WorktreeDeleteConfirmDialog}. The ACTIVE
 * managed session's pinned branch and its own tree are the exception: they
 * render a lock instead of the focus/current checkmark and offer no delete
 * (the immutable session binding, ADR-080). The same ADR gates the rest of
 * the branch list while the panel's FOCUS sits on a managed session tree:
 * off-pin checkout rows dim with the explaining reason instead of failing
 * server-side (ErrPinnedWorktreeBranch). All three
 * section headers are collapsible (ephemeral state; an active search
 * force-expands them). The Local/Remote list rendering is the shared
 * {@link BranchList} (menu variant) — the toggle above drives its `tree`
 * prop; this component keeps the trigger, the search box and the Worktrees
 * section (always flat). Branch
 * creation/management lives in the `BranchPicker` modal, opened from the git
 * panel's toolbar button — the dropdown does not duplicate those entries.
 * Branch data comes from `gitPanelStore.branches`.
 */
export function BranchDropdown() {
  const branches = useGitPanelStore((s) => s.branches);
  const branch = useGitPanelStore((s) => s.branch);
  const worktrees = useGitPanelStore((s) => s.worktrees);

  const actions = useBranchActions();
  const worktreeActions = useWorktreeActions();
  // The active managed session's pin (ADR-080): its branch row renders a
  // lock instead of the current-branch check and offers no delete; its tree
  // row does the same in the Worktrees section. The dropdown is a
  // manage-mode surface — no draft selection ever flows through it.
  // `branchSwitchBlockedReason` is the managed-FOCUS gate: while the focus
  // sits on a managed session tree, off-pin checkout rows dim up front
  // instead of failing server-side.
  const { pinnedBranch, isPinnedWorktree, branchSwitchBlockedReason } = useSessionWorktreePin();

  const [search, setSearch] = useState("");
  const [open, setOpen] = useState(false);
  // Local/Remote display mode: `/`-slash tree vs flat list. Store-backed,
  // persisted (`branchListTree`) and SHARED with the BranchPicker modal, so
  // both branch-list surfaces always render the same mode; the Worktrees
  // section is unaffected (it has no directory structure of its own).
  const tree = useGitPanelStore((s) => s.branchListTree);
  const setBranchListTree = useGitPanelStore((s) => s.setBranchListTree);
  // Collapsible state for the Worktrees section — Local/Remote (and any tree
  // dirs) collapse inside BranchList, which owns its own hook instance.
  const { isSectionExpanded, toggleSection } = useCollapsibleSections();

  const showSearch = branches.length >= 5;

  const filterFn = useCallback(
    (name: string) => {
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return name.toLowerCase().includes(q);
    },
    [search],
  );

  /**
   * Worktrees section header — the exact styling the session selector's
   * Managed/Local/Archived sections mirror. An active search force-expands
   * the section so matches inside a collapsed one stay reachable.
   */
  const searchActive = search.trim() !== "";
  const renderWorktreesHeader = () => (
    <CollapsibleSectionHeader
      label="Worktrees"
      noun="branches"
      expanded={isSectionExpanded("worktrees", searchActive)}
      onToggle={() => toggleSection("worktrees")}
      controlsId={branchRegionId("worktrees")}
    />
  );

  // While renaming, the inline input replaces the whole field (mirrors
  // SessionSelector). The dropdown is closed so it doesn't pop back open
  // after the rename commits.
  if (actions.renamingBranch) {
    return (
      <div className="px-2 py-1">
        <InlineRenameInput
          inputRef={actions.renameRef}
          value={actions.renameValue}
          onChange={actions.setRenameValue}
          onCommit={actions.commitRename}
          onCancel={actions.cancelRename}
          autoFocus
        />
      </div>
    );
  }

  const close = () => setOpen(false);

  // The busy row shows its own spinner (via `inFlight`); every other row is
  // dimmed and blocked while an operation runs.
  const inFlightFor = (name: string): BranchActionKind | null =>
    actions.busyBranch === name ? actions.busyAction : null;
  const disabledFor = (name: string): boolean =>
    actions.isBusy && actions.busyBranch !== name;

  return (
    <div className="px-2 py-1">
      <DropdownMenu
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) setSearch("");
        }}
      >
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            className="h-7 w-full min-w-0 justify-start gap-1.5 px-2 text-sm text-muted-foreground"
            title="Switch branch"
          >
            <GitBranch className="size-4 shrink-0" />
            <span className="truncate font-medium">
              {branch.name || <span className="italic opacity-50">no branch</span>}
            </span>
            {(branch.ahead > 0 || branch.behind > 0) && (
              <span className="shrink-0 font-mono text-xs opacity-70">
                ↑{branch.ahead} ↓{branch.behind}
              </span>
            )}
            <ChevronDown className="ml-auto size-3.5 shrink-0 opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-80">
          {/* The search-input line, modeled on SessionSelectorToolbar: the
              Local/Remote tree-mode toggle sits BEFORE the input, renders
              even when the input is hidden (below the 5-branch threshold)
              and its onSelect preventDefaults — flipping the display mode
              never dismisses the menu. The icon shows the mode you switch
              TO. The input is a MenuSearchInput so Radix's menu typeahead
              cannot steal focus from it now that the toggle makes the menu
              hold a registered item. */}
          <div className="flex items-center gap-1 px-2 py-1">
            <DropdownMenuCheckboxItem
              checked={tree}
              aria-label={tree ? "Show flat list" : "Show branch tree"}
              title={tree ? "Show flat list" : "Show branch tree"}
              // The wrapper's check indicator is suppressed: the icon-only
              // button's glyph already shows the mode you switch TO.
              className="h-7 w-7 shrink-0 justify-center rounded p-0 text-muted-foreground hover:bg-muted/50 active:bg-muted/30 [&>span]:hidden"
              onSelect={(event) => {
                // Keep the menu open — only the display mode flips.
                event.preventDefault();
                setBranchListTree(!tree);
              }}
            >
              {tree ? <List className="size-4" /> : <FolderTree className="size-4" />}
            </DropdownMenuCheckboxItem>
            {showSearch && (
              <MenuSearchInput
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search branches..."
                className="h-7 min-w-0 flex-1 text-sm"
                autoFocus
              />
            )}
          </div>
          {showSearch && <DropdownMenuSeparator />}

          <BranchList
            variant="menu"
            tree={tree}
            branches={branches}
            filter={filterFn}
            searchActive={searchActive}
            inFlightFor={inFlightFor}
            disabledFor={disabledFor}
            pinnedFor={(name) => name === pinnedBranch}
            checkoutBlockedReasonFor={branchSwitchBlockedReason}
            onCheckout={(name) => {
              actions.checkout(name);
              close();
            }}
            onRename={(name) => {
              close();
              actions.startRename(name);
            }}
            onMerge={actions.mergeBranch}
            onRebase={actions.rebaseBranch}
            onPush={actions.push}
            onDeleteLocal={actions.requestDeleteLocal}
            onCheckoutRemote={(remoteBranch) => {
              actions.checkoutRemote(remoteBranch);
              close();
            }}
            onDeleteRemote={actions.requestDeleteRemote}
            hideEmptyState={worktrees.length > 0}
          />

          {worktrees.length > 0 && (
            <>
              <DropdownMenuSeparator />
              {renderWorktreesHeader()}
              {isSectionExpanded("worktrees", searchActive) && (
                <div id={branchRegionId("worktrees")} role="presentation">
                  <WorktreesSection
                    variant="menu"
                    worktrees={worktrees}
                    isPinnedWorktree={isPinnedWorktree}
                    onSelect={(w) => {
                      void focusWorktree(w.path);
                      close();
                    }}
                    onDelete={worktreeActions.requestDelete}
                    deleteBusyPath={worktreeActions.busyPath}
                  />
                </div>
              )}
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <BranchDeleteConfirmDialog
        pending={actions.pendingDelete}
        onConfirm={actions.confirmDelete}
        onCancel={actions.cancelDelete}
      />

      <WorktreeDeleteConfirmDialog
        pending={worktreeActions.pendingDelete}
        error={worktreeActions.deleteError}
        onConfirm={worktreeActions.confirmDelete}
        onCancel={worktreeActions.cancelDelete}
      />
    </div>
  );
}
