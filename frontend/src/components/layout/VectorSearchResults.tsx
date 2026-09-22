import { useCallback } from "react";
import { Loader2, FileCode, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { useVectorIndexStore } from "@/stores/vectorIndexStore";
import { useFileViewerStore } from "@/stores/fileViewerStore";
import type { VectorIndexStatus, VectorStoreEntry } from "@/types/models";

const MAX_PREVIEW_LINES = 4;
const MAX_PREVIEW_CHARS = 300;

interface VectorSearchResultsProps {
  isSearchMode: boolean;
}

/**
 * Placeholder copy for an index that is not `ready` yet.
 *
 * `loading` (ADR-064) is the branch-scoped chromem DB open: no indexing pass
 * exists yet, so it gets its own wording instead of borrowing the
 * "Indexing in progress" claim — the same honesty rule the status-bar pill
 * follows (see `IndexingStatus`).
 */
function notReadyMessage(state: VectorIndexStatus["state"], isSearchMode: boolean): string {
  if (state === "unavailable") return "Vector index unavailable";
  if (state === "loading") {
    return isSearchMode
      ? "Preparing index — results will appear automatically when ready"
      : "Preparing index";
  }
  return isSearchMode
    ? "Indexing in progress — results will appear automatically when ready"
    : "Indexing in progress";
}

/**
 * VectorSearchResults renders the list of vector-search hits, plus the
 * empty / loading / idle placeholder states. The store provides entries;
 * the component is purely presentational. (W-30 split)
 */
export function VectorSearchResults({ isSearchMode }: VectorSearchResultsProps) {
  const status = useVectorIndexStore((s) => s.status);
  const entries = useVectorIndexStore((s) => s.entries);
  const isLoading = useVectorIndexStore((s) => s.isLoading);

  if (status.state !== "ready" && status.state !== "idle") {
    // Index opening, building, or unavailable. The auto-search effect in
    // useVectorSearch re-runs the active query once the index reports ready
    // (incl. the vector_index:status → ready subscription), so a seeded query
    // is not lost — surface that here instead of a blank area. Without an
    // active query we stay terse: the IndexingStatus pill above already
    // conveys what is happening.
    const message = notReadyMessage(status.state, isSearchMode);
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-xs text-muted-foreground text-center px-4">{message}</p>
      </div>
    );
  }
  if (status.state === "idle") {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-xs text-muted-foreground">Select a project to search</p>
      </div>
    );
  }
  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (!isSearchMode) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Search className="size-12 text-muted-foreground/30" />
      </div>
    );
  }
  if (entries.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-xs text-muted-foreground">No results found</p>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-auto custom-scrollbar">
      {entries.map((entry) => (
        <VectorStoreEntryItem
          key={`${entry.file_path}:${entry.start_line}:${entry.end_line}`}
          entry={entry}
          showScore={isSearchMode}
        />
      ))}
    </div>
  );
}

function VectorStoreEntryItem({ entry, showScore }: { entry: VectorStoreEntry; showScore: boolean }) {
  const openFileAtLine = useFileViewerStore((s) => s.openFileAtLine);

  const handleClick = useCallback(() => {
    openFileAtLine(entry.file_path, entry.start_line);
  }, [openFileAtLine, entry.file_path, entry.start_line]);

  // Build content preview
  const previewLines = entry.content.split("\n").slice(0, MAX_PREVIEW_LINES);
  let preview = previewLines.join("\n");
  if (preview.length > MAX_PREVIEW_CHARS) {
    preview = preview.slice(0, MAX_PREVIEW_CHARS) + "...";
  }
  if (entry.content.split("\n").length > MAX_PREVIEW_LINES) {
    preview += "\n...";
  }

  // Extract relative path segment for display
  const fileName = entry.file_name || entry.file_path.split("/").pop() || "";
  const dirPath = entry.file_path.substring(0, entry.file_path.length - fileName.length);

  return (
    <button
      type="button"
      onClick={handleClick}
      className="w-full text-left px-1.5 py-1.5 rounded hover:bg-muted/50 transition-colors cursor-pointer"
    >
      {/* Header line */}
      <div className="flex items-center gap-1.5 text-xs">
        <FileCode className="size-3 shrink-0 text-muted-foreground" />
        <span className="truncate font-medium text-foreground select-text">{fileName}</span>
        {entry.start_line > 0 && (
          <span className="shrink-0 text-muted-foreground">
            L{entry.start_line}
            {entry.end_line > entry.start_line ? `-${entry.end_line}` : ""}
          </span>
        )}
        {entry.language && (
          <Badge variant="outline" className="h-4 px-1 text-[10px] leading-none">
            {entry.language}
          </Badge>
        )}
        {showScore && entry.vector_rank !== undefined && entry.vector_rank > 0 && (
          <Badge
            variant="outline"
            className="h-4 px-1 text-[10px] leading-none text-info"
            title={`Vector rank ${entry.vector_rank}`}
          >
            V#{entry.vector_rank}
          </Badge>
        )}
        {showScore && entry.lexical_rank !== undefined && entry.lexical_rank > 0 && (
          <Badge
            variant="outline"
            className="h-4 px-1 text-[10px] leading-none text-warning"
            title={`Lexical rank ${entry.lexical_rank}`}
          >
            L#{entry.lexical_rank}
          </Badge>
        )}
        {showScore && (
          <span className="ml-auto shrink-0 text-muted-foreground tabular-nums">{entry.score.toFixed(2)}</span>
        )}
      </div>

      {/* Directory path */}
      {dirPath && <div className="truncate text-[10px] text-muted-foreground mt-0.5 pl-4.5 select-text">{dirPath}</div>}

      {/* Content preview */}
      <pre className="mt-1 pl-4.5 text-[10px] leading-4 text-muted-foreground whitespace-pre-wrap break-all line-clamp-4 select-text">
        {preview}
      </pre>
    </button>
  );
}
