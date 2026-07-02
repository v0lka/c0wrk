import { useChatStore } from "@/stores/chatStore";
import { useSessionStore } from "@/stores/sessionStore";
import { usePlanStore, type SessionStats } from "@/stores/planStore";
import { useProjectStore } from "@/stores/projectStore";
import { useUIStore } from "@/stores/uiStore";
import { useFileViewerStore } from "@/stores/fileViewerStore";
import { domainLabels } from "@/constants/routingLabels";
import { formatTokenCount } from "@/lib/formatters";
import { cn } from "@/lib/utils";
import { Separator } from "@/components/ui/separator";
import { Badge } from "@/components/ui/badge";
import { IndexingStatus } from "./IndexingStatus";
import { Loader2 } from "lucide-react";

function Sep() {
  return <Separator orientation="vertical" className="mx-1 h-4" />;
}

export function StatusBar() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  const activityStatus = useChatStore((s) => s.activityStatus);
  const sessionTokens = useChatStore((s) => s.sessionTokens);
  const sessionStats = usePlanStore((s) => s.sessionStats);
  const isNoProject = useProjectStore((s) => {
    const active = s.projects?.find((p) => p.id === s.activeProjectId);
    return active?.is_no_project === true;
  });
  const sidebarCollapsed = useUIStore((s) => s.sidebarCollapsed);
  const viewerCollapsed = useFileViewerStore((s) => s.collapsed);

  const tokens = activeSessionId ? sessionTokens[activeSessionId] : undefined;
  const stats: SessionStats | undefined = activeSessionId ? sessionStats[activeSessionId] : undefined;

  const domainLabel = stats?.routingDomain ? (domainLabels[stats.routingDomain] ?? stats.routingDomain) : undefined;

  return (
    <div
      className={cn(
        "flex h-8 shrink-0 items-center gap-0.5 overflow-hidden border-t border-border bg-background px-3 text-xs text-muted-foreground",
        sidebarCollapsed && "ml-1",
        viewerCollapsed && "mr-1",
      )}
    >
      {/* Thinking indicator */}
      {activityStatus && (
        <>
          <Loader2 className="size-3 shrink-0 animate-spin text-info" />
          <span className="min-w-0 truncate" title={activityStatus}>
            {activityStatus}
          </span>
          <Sep />
        </>
      )}

      {/* Routing domain badge */}
      {domainLabel && (
        <>
          <Badge
            variant="secondary"
            className="h-5 min-w-0 shrink max-w-full px-1.5 text-[10px] truncate"
            title={domainLabel}
          >
            {domainLabel}
          </Badge>
          <Sep />
        </>
      )}

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

      {/* Spacer */}
      <div className="flex-1" />

      {/* Vector index status (hidden for No Project) */}
      {!isNoProject && <IndexingStatus />}
    </div>
  );
}
