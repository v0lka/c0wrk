import { useState, useMemo, useCallback } from "react";
import { useSessionStore } from "@/stores/sessionStore";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  ChevronDown,
  ChevronRight,
  Plus,
} from "lucide-react";
import { useSessionActions } from "@/hooks/useSessionActions";
import { SessionItem, type SessionItemSummary } from "./SessionListItem";
import { SessionActionConfirmDialog } from "./SessionActionConfirmDialog";

/**
 * Dropdown session selector — used in CODE mode (a real project is active).
 *
 * In CHAT (No Project) mode the sidebar renders the flat {@link SessionList}
 * instead, so this component only mounts when `isChatMode` is false.
 */
export function SessionSelector() {
  const sessions = useSessionStore((s) => s.sessions);
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  const setActiveSessionId = useSessionStore((s) => s.setActiveSessionId);

  const {
    createError,
    clearCreateError,
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
  const [dropdownOpen, setDropdownOpen] = useState(false);
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

  const activeSession = sessions?.find((s) => s.id === activeSessionId);

  if (renamingId) {
    return (
      <div className="px-2 py-1">
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

  const renderSessionItem = (session: SessionItemSummary) => (
    <SessionItem
      key={session.id}
      session={session}
      isActive={session.id === activeSessionId}
      onSelect={() => {
        setActiveSessionId(session.id);
        setDropdownOpen(false);
      }}
      onRename={() => startRename(session.id, session.name)}
      onArchive={() => handleArchive(session.id, session.archived)}
      onPin={() => handlePin(session.id, session.pinned)}
      onFork={() => handleFork(session.id)}
      onDelete={() => handleDelete(session.id)}
    />
  );

  const visibleActive = activeSessionsList.filter((s) => filterFn(s.name));
  const visibleArchived = archivedList.filter((s) => filterFn(s.name));

  return (
    <div className="border-b border-border px-2 py-1">
      {createError && (
        <div className="mb-1 rounded px-2 py-1 text-[11px] text-destructive bg-destructive/10">{createError}</div>
      )}
      <div className="flex items-center gap-1">
        <DropdownMenu
          open={dropdownOpen}
          onOpenChange={(o) => {
            setDropdownOpen(o);
            if (!o) setSearch("");
            if (o) clearCreateError();
          }}
        >
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" className="h-7 flex-1 min-w-0 justify-between gap-1 px-2 text-sm">
              <span className="truncate text-muted-foreground">{activeSession?.name ?? "Select session"}</span>
              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-96">
            {showSearch && (
              <>
                <div className="px-2 py-1">
                  <Input
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder="Search sessions..."
                    className="h-7 text-sm"
                    autoFocus
                  />
                </div>
                <DropdownMenuSeparator />
              </>
            )}

            {visibleActive.map(renderSessionItem)}

            {visibleArchived.length > 0 && (
              <>
                <DropdownMenuSeparator />
                <button
                  type="button"
                  className="flex w-full items-center gap-1 rounded-sm px-2 py-1 text-xs text-muted-foreground hover:bg-muted/50"
                  onClick={() => setArchivedCollapsed((c) => !c)}
                >
                  {archivedCollapsed ? (
                    <ChevronRight className="size-3" />
                  ) : (
                    <ChevronDown className="size-3" />
                  )}
                  Archived ({visibleArchived.length})
                </button>
                {!archivedCollapsed && visibleArchived.map(renderSessionItem)}
              </>
            )}

            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={handleNewSession}>
              <Plus className="size-3.5" />
              New Session...
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <button type="button" className="rounded p-0.5 hover:bg-muted/50 active:bg-muted/30" onClick={handleNewSession}>
          <Plus className="size-3.5" />
        </button>
      </div>
      <SessionActionConfirmDialog pending={pendingAction} onConfirm={confirmPendingAction} onCancel={cancelPendingAction} />
    </div>
  );
}
