import { useRef, useEffect, useCallback } from "react";
import { Loader2, Sparkles } from "lucide-react";
import { useGitPanelStore, EMPTY_COMMIT_DRAFT, selectSkipCommitSuppress } from "@/stores/gitPanelStore";
import { useProjectStore } from "@/stores/projectStore";
import { commit, generateCommitMessage, type CommitResult } from "@/api/git";
import { runGitOperation } from "@/lib/gitOperation";
import { useCommitSuppressedFlow } from "./useCommitSuppressedFlow";
import { CommitSuppressedDialog } from "./CommitSuppressedDialog";
import { cn } from "@/lib/utils";

/** Header label for a commit record in the git-operation console. */
function commitLabel(sha?: string): string {
  return sha ? `Committed ${sha.slice(0, 7)}` : "Committed";
}

/**
 * Record a commit that actually ran in the git-operation console and clear
 * the committed project's draft. A withheld (suppressed) result never reaches
 * here — it is a decision request, not a commit, so it must not land in the
 * console. The commit already ran, so the recorder thunk replays its result
 * and the shared wrapper owns the console write (and the error logging).
 */
async function recordCommit(projectId: string, result: CommitResult): Promise<void> {
  await runGitOperation({
    projectId,
    kind: "commit",
    label: commitLabel(result.sha),
    fn: async () => result,
    extractOutput: (r) => r.output ?? "",
  });
  useGitPanelStore.getState().setCommitMessage(projectId, "");
}

/**
 * Record a failed commit in the git-operation console. Never throws: the
 * recorder captures the rejection (and logs it) so the caller's control flow
 * is preserved.
 */
async function recordCommitFailure(projectId: string, err: unknown): Promise<void> {
  await runGitOperation({
    projectId,
    kind: "commit",
    label: "Commit failed",
    fn: async () => {
      throw err;
    },
    extractOutput: () => "",
  });
}

export function CommitSection() {
  const activeProjectId = useProjectStore((s) => s.activeProjectId);
  const entries = useGitPanelStore((s) => s.entries);
  const setCommitMessage = useGitPanelStore((s) => s.setCommitMessage);
  const setGenerating = useGitPanelStore((s) => s.setGeneratingCommit);
  const setCommitting = useGitPanelStore((s) => s.setCommitting);
  const setCommitError = useGitPanelStore((s) => s.setCommitError);
  const setSkipCommitSuppress = useGitPanelStore((s) => s.setSkipCommitSuppress);

  // The Trust/continue flow for suppressed commits: owns the withheld
  // commit, the trust→re-commit sequence, and the force-commit path. Its
  // outcomes are recorded in the git-operation console (never inline).
  const suppressedFlow = useCommitSuppressedFlow({
    recordCommit,
    recordCommitFailure,
    setSkipCommitSuppress,
  });

  // Per-project commit-box slice. The selector returns the stored slice (a
  // stable reference) or undefined when the project has no state yet — never
  // a fresh object — so the stable-selector rule holds. Defaults come from a
  // module constant, keeping the draft/generating/committing/error state alive
  // across project switches and GitPanel unmounts (CHAT mode).
  const storedDraft = useGitPanelStore((s) =>
    activeProjectId === null ? undefined : s.commitByProject[activeProjectId],
  );
  const draft = storedDraft ?? EMPTY_COMMIT_DRAFT;
  const { message: commitMessage, isGenerating, isCommitting, error } = draft;
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const stagedCount = entries.filter((e) => e.staged).length;
  const isEmpty = commitMessage.trim().length === 0;
  const isDisabled = stagedCount === 0 || isEmpty || isCommitting;
  const isGenerateDisabled = stagedCount === 0 || isGenerating || isCommitting;

  // Auto-height: expand up to ~6 lines
  const adjustHeight = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    const lineHeight = parseFloat(getComputedStyle(el).lineHeight) || 20;
    // 6 lines + vertical padding
    const maxHeight = lineHeight * 6 + 16;
    el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`;
    // Show scrollbar when content exceeds max height
    el.style.overflowY = el.scrollHeight > maxHeight ? "auto" : "hidden";
  }, []);

  useEffect(() => {
    adjustHeight();
  }, [commitMessage, adjustHeight]);

  const handleCommit = async () => {
    // Capture the project at click time: every write below (console record,
    // draft clear) must land in the project whose draft is being committed,
    // even if the user switches projects mid-flight.
    const projectId = activeProjectId;
    if (projectId === null || isDisabled) return;
    const message = commitMessage;
    setCommitting(projectId, true);
    setCommitError(projectId, null);
    suppressedFlow.setSuppressed(null);
    try {
      // A project with "don't ask again" set commits hardened directly —
      // the suppression dialog never opens for it.
      const force = selectSkipCommitSuppress(useGitPanelStore.getState(), projectId);
      // A withheld commit (result.suppressed) is a non-error decision
      // request, not a commit: it opens the Trust/continue dialog and must
      // never reach the console. Probe first; only a commit that actually
      // ran is recorded (success or failure).
      const result = await commit(message, force);
      if (result.suppressed) {
        suppressedFlow.setSuppressed({ suppression: result.suppressed, message, projectId });
        return;
      }
      // Records the result in the git-operation console and clears the draft.
      await recordCommit(projectId, result);
      // Status refresh is handled by the git:status_changed event emitted
      // by the backend after a successful commit (picked up by useGitStatusEvents)
    } catch (err) {
      await recordCommitFailure(projectId, err);
    } finally {
      setCommitting(projectId, false);
    }
  };

  const handleGenerate = async () => {
    // Capture the project at click time so the result (or error) is written
    // into the project that requested generation — never into whichever
    // project happens to be active when the promise settles.
    const projectId = activeProjectId;
    if (projectId === null) return;
    const stagedEntries = entries.filter((e) => e.staged);
    if (stagedEntries.length === 0 || isGenerating) return;

    setGenerating(projectId, true);
    setCommitError(projectId, null);
    try {
      // The backend obtains the staged diff itself via a single
      // `git diff --staged` invocation, so no diff is collected here.
      // An empty staged diff (e.g. stale entries) is reported by the
      // backend as an error and surfaced below.
      const message = await generateCommitMessage();
      setCommitMessage(projectId, message);
    } catch (err) {
      setCommitError(projectId, err instanceof Error ? err.message : "Failed to generate commit message");
    } finally {
      setGenerating(projectId, false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Ctrl+Enter / Cmd+Enter to commit
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void handleCommit();
    }
  };

  return (
    <div className="border-t border-border bg-muted/20 p-3">
      <textarea
        ref={textareaRef}
        value={commitMessage}
        onChange={(e) => {
          if (activeProjectId === null) return;
          setCommitMessage(activeProjectId, e.target.value);
          if (error) setCommitError(activeProjectId, null);
          suppressedFlow.setSuppressed(null);
        }}
        onKeyDown={handleKeyDown}
        placeholder="Describe your changes..."
        rows={2}
        aria-invalid={error ? true : undefined}
        className={cn(
          // Mirror the shared `c0-input` styling used by the search rows in
          // the file panel (FileTreePanel) and the search panel
          // (VectorSearchFilters) so the commit message window, its
          // placeholder and its content share the same look.
          "c0-input w-full resize-none rounded-md border border-input px-3 py-2 text-sm shadow-xs custom-scrollbar",
          "transition-[color,box-shadow] outline-none",
          "aria-invalid:border-destructive",
        )}
      />

      {/* Inline error is reserved for DRAFT/GENERATE/VALIDATION failures only.
          Commit outcomes (success or failure) go to the git-operation console. */}
      {error ? <div className="mt-1.5 text-xs text-destructive">{error}</div> : null}

      <div className="mt-2 flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {stagedCount > 0 ? `${stagedCount} staged file${stagedCount !== 1 ? "s" : ""}` : "No staged changes"}
        </span>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={handleGenerate}
            disabled={isGenerateDisabled}
            title="Generate commit message with AI"
            aria-label="Generate commit message with AI"
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors",
              "focus:outline-none focus:ring-1 focus:ring-ring",
              isGenerateDisabled
                ? "bg-muted text-muted-foreground cursor-not-allowed"
                : "bg-secondary text-secondary-foreground hover:bg-secondary/80",
            )}
          >
            {isGenerating ? <Loader2 className="h-3 w-3 animate-spin" /> : <Sparkles className="h-3 w-3" />}
            Generate
          </button>
          <button
            type="button"
            onClick={() => void handleCommit()}
            disabled={isDisabled}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
              "focus:outline-none focus:ring-1 focus:ring-ring",
              isDisabled
                ? "bg-muted text-muted-foreground cursor-not-allowed"
                : "bg-primary text-primary-foreground hover:bg-foreground/80",
            )}
          >
            {isCommitting && <Loader2 className="h-3 w-3 animate-spin" />}
            Commit
          </button>
        </div>
      </div>

      {/* The Trust/continue decision for a withheld commit. */}
      <CommitSuppressedDialog
        suppressed={suppressedFlow.pendingSuppressed?.suppression ?? null}
        repoPath={suppressedFlow.pendingWorkspacePath}
        isSubmitting={suppressedFlow.isDialogSubmitting}
        onTrust={() => void suppressedFlow.handleTrustAndCommit()}
        onForceCommit={(skip) => void suppressedFlow.handleForceCommit(skip)}
        onCancel={suppressedFlow.handleCancel}
      />
    </div>
  );
}
