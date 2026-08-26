import { useChatStore } from "@/stores/chatStore";
import { useSessionStore } from "@/stores/sessionStore";
import { useProjectStore } from "@/stores/projectStore";
import { useUIStore } from "@/stores/uiStore";
import { useFileViewerStore } from "@/stores/fileViewerStore";
import { formatTokenCount } from "@/lib/formatters";
import { cn } from "@/lib/utils";
import { Separator } from "@/components/ui/separator";
import { Badge } from "@/components/ui/badge";
import { IndexingStatus } from "./IndexingStatus";
import { ContextFillStatus } from "./ContextFillStatus";
import { CompactContextButton } from "./CompactContextButton";
import { GoalStatusIndicator } from "./GoalStatusIndicator";

function Sep() {
  return <Separator orientation="vertical" className="mx-1 h-4" />;
}

export function StatusBar() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  const tokens = useChatStore((s) => activeSessionId ? s.sessionTokens[activeSessionId] : undefined);
  const isNoProject = useProjectStore((s) => {
    const active = s.projects?.find((p) => p.id === s.activeProjectId);
    return active?.is_no_project === true;
  });
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed);
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed);

  return (
    <div
      className={cn(
        "flex h-8 shrink-0 items-center gap-0.5 overflow-hidden border-t border-border bg-background px-3 text-xs text-muted-foreground",
        sidebarCollapsed && "ml-1",
        viewerCollapsed && "mr-1",
      )}
    >
      {/* Context badge: model, family, input/output token counts */}
      {tokens && (
        <>
          <span className="flex min-w-0 items-center gap-1">
            {tokens.model && (
              <Badge
                variant="outline"
                className="h-5 min-w-0 shrink max-w-full px-1.5 text-[10px] truncate"
                title={tokens.model}
              >
                {tokens.model}
              </Badge>
            )}
            {tokens.family && (
              <span className="min-w-0 truncate text-[10px] text-muted-foreground/70" title={tokens.family}>
                {tokens.family}
              </span>
            )}
            <span className="shrink-0 tabular-nums">
              {formatTokenCount(tokens.total_input_tokens)}↑ {formatTokenCount(tokens.total_output_tokens)}↓
            </span>
          </span>
          <Sep />
        </>
      )}

      {/* Goal status badge with Pause/Resume/Clear (only when goal active/paused) */}
      <GoalStatusIndicator />

      {/* Spacer */}
      <div className="flex-1" />

      {/* Context fill (conductor's context window) with the manual compaction
          control immediately to its left */}
      {tokens && (
        <>
          <CompactContextButton />
          <ContextFillStatus percent={tokens.fill_percent} usedTokens={tokens.used_tokens} maxTokens={tokens.max_tokens} />
        </>
      )}

      {/* Vector index status (hidden for No Project) */}
      {!isNoProject && (
        <>
          <Sep />
          <IndexingStatus />
        </>
      )}
    </div>
  );
}
