import { useState, useMemo, useCallback, useRef } from "react";
import { cn } from "@/lib/utils";
import { useSessionStore } from "@/stores/sessionStore";
import { createSession, renameSession, archiveSession, deleteSession } from "@/api/sessions";
import { formatRelativeTime } from "@/lib/formatters";
import { logger } from "@/lib/logger";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuLabel,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ChevronDown, Check, Plus, Pencil, Archive, Trash2 } from "lucide-react";
import { useSessionStatusIndicator } from "@/hooks/useSessionStatusIndicator";

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

            {activeSessionsList
              .filter((s) => filterFn(s.name))
              .map((session) => (
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
                  onDelete={() => handleDelete(session.id)}
                />
              ))}

            {archivedList.filter((s) => filterFn(s.name)).length > 0 && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuLabel className="text-xs text-muted-foreground">Archived</DropdownMenuLabel>
                {archivedList
                  .filter((s) => filterFn(s.name))
                  .map((session) => (
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
                      onDelete={() => handleDelete(session.id)}
                    />
                  ))}
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
  session: { id: string; name: string; archived: boolean; last_active_at: string };
  isActive: boolean;
  onSelect: () => void;
  onRename: () => void;
  onArchive: () => void;
  onDelete: () => void;
}
function SessionItem({ session, isActive, onSelect, onRename, onArchive, onDelete }: SessionItemProps) {
  const status = useSessionStatusIndicator(session.id);
  return (
    <DropdownMenuItem className="group/item gap-2" onSelect={onSelect}>
      <div className="flex min-w-0 flex-1 items-center gap-1.5">
        {status === "pending" && (
          <span className="size-1.5 shrink-0 rounded-full bg-warning" title="Awaiting your response" />
        )}
        {status === "active" && <span className="size-1.5 shrink-0 rounded-full bg-success" title="Task running" />}
        {isActive && <Check className="size-3.5 shrink-0" />}
        <span className={cn("min-w-0 flex-1 truncate", isActive && "font-medium")}>{session.name}</span>
      </div>
      <span className="ml-auto text-[10px] text-muted-foreground">{formatRelativeTime(session.last_active_at)}</span>
      <span
        className="flex shrink-0 gap-0.5 opacity-0 transition-opacity group-hover/item:opacity-100 group-focus-within/item:opacity-100"
        onPointerDown={(e) => e.stopPropagation()}
        onPointerUp={(e) => e.stopPropagation()}
        onClick={(e) => e.stopPropagation()}
      >
        <button type="button" className="rounded p-0.5 hover:bg-info/15" onClick={onRename}>
          <Pencil className="size-3 text-info" />
        </button>
        <button type="button" className="rounded p-0.5 hover:bg-warning/15" onClick={onArchive}>
          <Archive className="size-3 text-warning" />
        </button>
        <button type="button" className="rounded p-0.5 hover:bg-destructive/20" onClick={onDelete}>
          <Trash2 className="size-3 text-destructive" />
        </button>
      </span>
    </DropdownMenuItem>
  );
}
