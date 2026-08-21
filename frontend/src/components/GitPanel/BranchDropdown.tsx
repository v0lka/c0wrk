import { useState, useMemo, useCallback } from "react";
import { GitBranch, ChevronDown, Plus, Settings } from "lucide-react";
import { useGitPanelStore } from "@/stores/gitPanelStore";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { useBranchActions } from "@/hooks/useBranchActions";
import type { BranchActionKind } from "@/hooks/useBranchActions";
import { LocalBranchRow } from "./LocalBranchRow";
import { RemoteBranchRow } from "./RemoteBranchRow";
import { BranchDeleteConfirmDialog } from "./BranchDeleteConfirmDialog";

/**
 * Inline branch dropdown — the git panel's branch field.
 *
 * Modeled on {@link SessionSelector}: a `DropdownMenu` anchored to the current
 * branch indicator. Clicking the field opens the list; a search box appears
 * once there are >= 5 branches. Branches are grouped into **Local** (rows
 * expose hover mini-icons for push/merge/rebase/rename/delete) and **Remote**
 * (click checks the branch out as a new local tracking branch; hover exposes
 * delete-remote). The footer offers **New branch…** and **Manage branches…**,
 * both of which open the existing `BranchPicker` modal (the full switch/create
 * surface). Branch data comes from `gitPanelStore.branches`.
 */
export function BranchDropdown() {
  const branches = useGitPanelStore((s) => s.branches);
  const branch = useGitPanelStore((s) => s.branch);
  const openBranchPicker = useGitPanelStore((s) => s.openBranchPicker);

  const actions = useBranchActions();

  const [search, setSearch] = useState("");
  const [open, setOpen] = useState(false);

  const localBranches = useMemo(
    () => (branches ?? []).filter((b) => b.kind === "local"),
    [branches],
  );
  const remoteBranches = useMemo(
    () => (branches ?? []).filter((b) => b.kind === "remote"),
    [branches],
  );

  const showSearch = branches.length >= 5;

  const filterFn = useCallback(
    (name: string) => {
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return name.toLowerCase().includes(q);
    },
    [search],
  );

  const visibleLocal = localBranches.filter((b) => filterFn(b.name));
  const visibleRemote = remoteBranches.filter((b) => filterFn(b.name));

  // While renaming, the inline input replaces the whole field (mirrors
  // SessionSelector). The dropdown is closed so it doesn't pop back open
  // after the rename commits.
  if (actions.renamingBranch) {
    return (
      <div className="px-2 py-1">
        <Input
          ref={actions.renameRef}
          value={actions.renameValue}
          onChange={(e) => actions.setRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") actions.commitRename();
            if (e.key === "Escape") actions.cancelRename();
          }}
          onBlur={actions.commitRename}
          className="h-7 text-sm"
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
      {actions.error && (
        <div className="mb-1 rounded px-2 py-1 text-[11px] text-destructive bg-destructive/10">
          {actions.error}
        </div>
      )}
      {actions.output && (
        <div className="mb-1 rounded px-2 py-1 text-[11px] text-success bg-success/10">
          {actions.output}
        </div>
      )}

      <DropdownMenu
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) setSearch("");
          if (o) {
            actions.clearError();
            actions.clearOutput();
          }
        }}
      >
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            className="h-7 w-full min-w-0 justify-start gap-1.5 px-2 text-sm text-muted-foreground"
          >
            <GitBranch className="size-3.5 shrink-0" />
            <span className="truncate font-mono text-[11px]">
              {branch.name || <span className="italic opacity-50">no branch</span>}
            </span>
            {(branch.ahead > 0 || branch.behind > 0) && (
              <span className="shrink-0 font-mono text-[10px] opacity-70">
                ↑{branch.ahead} ↓{branch.behind}
              </span>
            )}
            <ChevronDown className="ml-auto size-3 shrink-0 opacity-60" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-80">
          {showSearch && (
            <>
              <div className="px-2 py-1">
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="Search branches..."
                  className="h-7 text-sm"
                  autoFocus
                />
              </div>
              <DropdownMenuSeparator />
            </>
          )}

          {visibleLocal.length > 0 && (
            <>
              <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                Local
              </div>
              {visibleLocal.map((b) => (
                <LocalBranchRow
                  key={b.name}
                  branch={b}
                  inFlight={inFlightFor(b.name)}
                  disabled={disabledFor(b.name)}
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
                  onDelete={actions.requestDeleteLocal}
                />
              ))}
            </>
          )}

          {visibleRemote.length > 0 && (
            <>
              <DropdownMenuSeparator />
              <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                Remote
              </div>
              {visibleRemote.map((b) => (
                <RemoteBranchRow
                  key={b.name}
                  branch={b}
                  inFlight={inFlightFor(b.name)}
                  disabled={disabledFor(b.name)}
                  onCheckoutRemote={(remoteBranch) => {
                    actions.checkoutRemote(remoteBranch);
                    close();
                  }}
                  onDeleteRemote={actions.requestDeleteRemote}
                />
              ))}
            </>
          )}

          {visibleLocal.length === 0 && visibleRemote.length === 0 && (
            <div className="px-2 py-3 text-center text-xs text-muted-foreground">
              {branches.length === 0 ? "No branches" : "No matching branches"}
            </div>
          )}

          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => openBranchPicker()}>
            <Plus className="size-3.5" />
            New branch...
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => openBranchPicker()}>
            <Settings className="size-3.5" />
            Manage branches...
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <BranchDeleteConfirmDialog
        pending={actions.pendingDelete}
        onConfirm={actions.confirmDelete}
        onCancel={actions.cancelDelete}
      />
    </div>
  );
}
