import { useState, useMemo, useCallback, useRef, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { useSessionStore } from "@/stores/sessionStore";
import { createSession, renameSession, archiveSession, deleteSession, forkSession, pinSession } from "@/api/sessions";
import { formatRelativeTime } from "@/lib/formatters";
import { logger } from "@/lib/logger";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip";
import {
  ChevronDown,
  ChevronRight,
  Check,
  Plus,
  Pencil,
  Archive,
  ArchiveRestore,
  Trash2,
  GitFork,
  Pin,
  PinOff,
} from "lucide-react";
import { useSessionStatusIndicator } from "@/hooks/useSessionStatusIndicator";

/** Minimal session shape consumed by a list item. */
interface SessionItemSummary {
  id: string;
  name: string;
  archived: boolean;
  pinned: boolean;
  last_active_at: string;
  has_unfinished_task: boolean;
}

export function SessionSelector() {
  const sessions = useSessionStore((s) => s.sessions);
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  const setActiveSessionId = useSessionStore((s) => s.setActiveSessionId);
  const addSession = useSessionStore((s) => s.addSession);
  const removeSession = useSessionStore((s) => s.removeSession);
  const updateSession = useSessionStore((s) => s.updateSession);

  const [search, setSearch] = useState("");
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [dropdownOpen, setDropdownOpen] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  // Archived sessions are collapsed by default; the header shows the count and
  // expands on click.
  const [archivedCollapsed, setArchivedCollapsed] = useState(true);
  const renameRef = useRef<HTMLInputElement>(null);

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

  const handleNewSession = useCallback(async () => {
    setCreateError(null);
    try {
      const session = await createSession();
      addSession(session);
      setActiveSessionId(session.id);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.error("Failed to create session:", error);
      setCreateError(message);
    }
  }, [addSession, setActiveSessionId]);

  const handleDelete = useCallback(
    async (id: string) => {
      try {
        await deleteSession(id);
        removeSession(id);
      } catch (error) {
        logger.error("Failed to delete session:", error);
      }
    },
    [removeSession],
  );

  const handleArchive = useCallback(
    async (id: string, isArchived: boolean) => {
      try {
        await archiveSession(id);
        updateSession(id, { archived: !isArchived });
      } catch (error) {
        logger.error("Failed to archive session:", error);
      }
    },
    [updateSession],
  );

  const handlePin = useCallback(
    async (id: string, isPinned: boolean) => {
      try {
        await pinSession(id);
        updateSession(id, { pinned: !isPinned });
      } catch (error) {
        logger.error("Failed to pin session:", error);
      }
    },
    [updateSession],
  );

  const handleFork = useCallback(
    async (id: string) => {
      try {
        const forked = await forkSession(id);
        addSession(forked);
        setActiveSessionId(forked.id);
        setDropdownOpen(false);
      } catch (error) {
        logger.error("Failed to fork session:", error);
      }
    },
    [addSession, setActiveSessionId],
  );

  const startRename = useCallback((id: string, currentName: string) => {
    setRenamingId(id);
    setRenameValue(currentName);
    setTimeout(() => renameRef.current?.focus(), 50);
  }, []);

  const commitRename = useCallback(async () => {
    if (!renamingId || !renameValue.trim()) {
      setRenamingId(null);
      return;
    }
    try {
      await renameSession(renamingId, renameValue.trim());
      updateSession(renamingId, { name: renameValue.trim() });
    } catch (error) {
      logger.error("Failed to rename session:", error);
    }
    setRenamingId(null);
  }, [renamingId, renameValue, updateSession]);

  if (renamingId) {
    return (
      <div className="px-2 py-1">
        <Input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") commitRename();
            if (e.key === "Escape") setRenamingId(null);
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
            if (o) setCreateError(null);
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
    </div>
  );
}

// --- Session Item ---
interface SessionItemProps {
  session: SessionItemSummary;
  isActive: boolean;
  onSelect: () => void;
  onRename: () => void;
  onArchive: () => void;
  onPin: () => void;
  onFork: () => void;
  onDelete: () => void;
}

function SessionAction({
  label,
  onClick,
  disabled,
  children,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  children: ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={onClick}
          disabled={disabled}
          className={cn(
            "rounded p-0.5",
            disabled && "cursor-not-allowed opacity-30 hover:bg-transparent",
          )}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent side="left">{label}</TooltipContent>
    </Tooltip>
  );
}

function SessionItem({ session, isActive, onSelect, onRename, onArchive, onPin, onFork, onDelete }: SessionItemProps) {
  const status = useSessionStatusIndicator(session.id);
  // Disable forking both while a task is actively running and when the session
  // has an unfinished (in-progress or failed) task — the backend rejects both.
  const forkDisabled = status === "active" || session.has_unfinished_task;
  const forkDisabledReason = status === "active"
    ? "Cannot fork while a task is running"
    : "Cannot fork a session with an unfinished task";

  return (
    <DropdownMenuItem className="group/item gap-2" onSelect={onSelect}>
      <div className="flex min-w-0 flex-1 items-center gap-1.5">
        {status === "pending" && (
          <span className="size-1.5 shrink-0 rounded-full bg-warning" title="Awaiting your response" />
        )}
        {status === "active" && <span className="size-1.5 shrink-0 rounded-full bg-success" title="Task running" />}
        {isActive && <Check className="size-3.5 shrink-0" />}
        {session.pinned && <Pin className="size-3 shrink-0 text-primary" />}
        <span className={cn("min-w-0 flex-1 truncate", isActive && "font-medium")}>{session.name}</span>
      </div>
      <span className="text-[10px] text-muted-foreground">{formatRelativeTime(session.last_active_at)}</span>

      {/* Action overlay — absolutely positioned over the right portion of the
          item. Appears on hover/focus, with a gradient background so the
          underlying time text stays readable underneath the buttons. */}
      <span
        className="absolute inset-y-0 right-0 flex items-center gap-0.5 pl-7 pr-1 opacity-0 transition-opacity bg-gradient-to-l from-popover via-popover to-popover/0 group-hover/item:opacity-100 group-focus-within/item:opacity-100"
        onPointerDown={(e) => e.stopPropagation()}
        onPointerUp={(e) => e.stopPropagation()}
        onClick={(e) => e.stopPropagation()}
      >
        <SessionAction label={session.pinned ? "Unpin" : "Pin"} onClick={onPin}>
          {session.pinned ? <PinOff className="size-3 text-primary" /> : <Pin className="size-3 text-primary" />}
        </SessionAction>
        <SessionAction label={forkDisabled ? forkDisabledReason : "Fork session"} onClick={onFork} disabled={forkDisabled}>
          <GitFork className="size-3 text-primary" />
        </SessionAction>
        <SessionAction label="Rename" onClick={onRename}>
          <Pencil className="size-3 text-info" />
        </SessionAction>
        <SessionAction label={session.archived ? "Unarchive" : "Archive"} onClick={onArchive}>
          {session.archived ? (
            <ArchiveRestore className="size-3 text-warning" />
          ) : (
            <Archive className="size-3 text-warning" />
          )}
        </SessionAction>
        <SessionAction label="Delete" onClick={onDelete}>
          <Trash2 className="size-3 text-destructive" />
        </SessionAction>
      </span>
    </DropdownMenuItem>
  );
}
