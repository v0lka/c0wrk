import { useState, useMemo, useCallback } from "react";
import { cn } from "@/lib/utils";
import { useSessionStore } from "@/stores/sessionStore";
import { Input } from "@/components/ui/input";
import { ChevronDown, ChevronRight, Plus, Search } from "lucide-react";
import { useSessionActions } from "@/hooks/useSessionActions";
import { SessionItem, type SessionItemSummary } from "./SessionListItem";
import { SessionActionConfirmDialog } from "./SessionActionConfirmDialog";

/**
 * Flat, scrollable session list — used in CHAT (No Project) mode.
 *
 * Layout (top → bottom):
 *   1. Header row: title + "New Session" button.
 *   2. Inline create error (when present).
 *   3. Search box (appears once total sessions ≥ 5).
 *   4. Scrollable region: active sessions, then the collapsible Archived group.
 *
 * Rename is rendered inline: the row swaps to an input for the session being
 * renamed. The outer scroll region keeps the list within the half-height slot
 * allocated by the sidebar's vertical split.
 */
export function SessionList() {
  const sessions = useSessionStore((s) => s.sessions);
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  const selectSession = useSessionStore((s) => s.selectSession);

  const {
    createError,
    renamingId,
    renameValue,
    setRenameValue,
    renameRef,
    startRename,
    commitRename,
    cancelRename,
    handleNewSession,
    handleDelete,
    handleArchive,
    handlePin,
    handleFork,
    pendingAction,
    confirmPendingAction,
    cancelPendingAction,
  } = useSessionActions();

  const [search, setSearch] = useState("");
  // Archived sessions are collapsed by default; the header shows the count and
  // expands on click.
  const [archivedCollapsed, setArchivedCollapsed] = useState(true);

  const activeSessionsList = useMemo(() => (sessions ?? []).filter((s) => !s.archived), [sessions]);
  const archivedList = useMemo(() => (sessions ?? []).filter((s) => s.archived), [sessions]);

  const totalCount = activeSessionsList.length + archivedList.length;
  const showSearch = totalCount >= 5;

  const filterFn = useCallback(
    (name: string) => {
      if (!search) return true;
      return name.toLowerCase().includes(search.toLowerCase());
    },
    [search],
  );

  const visibleActive = activeSessionsList.filter((s) => filterFn(s.name));
  const visibleArchived = archivedList.filter((s) => filterFn(s.name));

  const renderItem = (session: SessionItemSummary) => {
    // Inline rename: the row is replaced by an input for the targeted session.
    if (renamingId === session.id) {
      return (
        <div key={session.id} className="px-1 py-0.5">
          <Input
            ref={renameRef}
            value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") commitRename();
              if (e.key === "Escape") cancelRename();
            }}
            onBlur={commitRename}
            className="h-7 text-sm"
          />
        </div>
      );
    }
    return (
      <SessionItem
        key={session.id}
        variant="flat"
        session={session}
        isActive={session.id === activeSessionId}
        onSelect={() => selectSession(session.id, session.project_id)}
        onRename={() => startRename(session.id, session.name)}
        onArchive={() => handleArchive(session.id, session.archived)}
        onPin={() => handlePin(session.id, session.pinned)}
        onFork={() => handleFork(session.id)}
        onDelete={() => handleDelete(session.id)}
      />
    );
  };

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Header */}
      <div className="flex shrink-0 items-center justify-between gap-1 border-b border-border px-2 py-1">
        <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Sessions
        </span>
        <button
          type="button"
          className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium text-foreground/70 hover:bg-muted/50 hover:text-foreground"
          onClick={handleNewSession}
          title="New session"
        >
          <Plus className="size-3" />
          New
        </button>
      </div>

      {createError && (
        <div className="shrink-0 mx-2 mt-1 rounded px-2 py-1 text-[11px] text-destructive bg-destructive/10">
          {createError}
        </div>
      )}

      {showSearch && (
        <div className="shrink-0 px-2 py-1">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search sessions..."
              className="h-7 pl-6 text-sm"
            />
          </div>
        </div>
      )}

      {/* Scrollable list */}
      <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto px-1 py-1">
        {visibleActive.length > 0 ? (
          visibleActive.map(renderItem)
        ) : (
          <p className="px-2 py-4 text-center text-xs text-muted-foreground/70">
            {search ? "No matching sessions" : "No sessions yet"}
          </p>
        )}

        {visibleArchived.length > 0 && (
          <div className="mt-1">
            <button
              type="button"
              className={cn(
                "flex w-full items-center gap-1 rounded-sm px-2 py-1 text-xs text-muted-foreground hover:bg-muted/50",
              )}
              onClick={() => setArchivedCollapsed((c) => !c)}
            >
              {archivedCollapsed ? <ChevronRight className="size-3" /> : <ChevronDown className="size-3" />}
              Archived ({visibleArchived.length})
            </button>
            {!archivedCollapsed && visibleArchived.map(renderItem)}
          </div>
        )}
      </div>
      <SessionActionConfirmDialog pending={pendingAction} onConfirm={confirmPendingAction} onCancel={cancelPendingAction} />
    </div>
  );
}
