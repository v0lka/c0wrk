import { useState, useEffect, useCallback } from "react";
import { Loader2, AlertTriangle, Info, RotateCw, FolderGit2, ShieldBan } from "lucide-react";
import { getSecuritySettings, updateSecuritySettings } from "@/api/config";
import { getToolList } from "@/api/mcp";
import { logger } from "@/lib/logger";
import { Button } from "@/components/ui/button";
import { SecurityGroupCard } from "./SecurityGroupCard";
import { ShellExecutionCard } from "./ShellExecutionCard";
import { TrustedReposDialog } from "./TrustedReposDialog";
import { HardenReposDialog } from "./HardenReposDialog";
import { SilentModeCard } from "./SilentModeCard";
import {
  DEFAULT_GROUP_POLICY,
  EXECUTE_GROUP,
  GROUP_ORDER,
} from "@/lib/securityGroups";
import { DEFAULT_SILENT_POLICIES, useAutonomyStore } from "@/stores/autonomyStore";
import { AUTONOMY_MODES, autonomyModeMeta, normalizeAutonomyMode } from "@/lib/autonomyModes";
import type { SilentSubPolicyKey } from "@/lib/silentMode";
import type {
  AutonomyMode,
  GroupPolicy,
  SecurityGroupPolicy,
  SecuritySettingsResponse,
  SilentModeSettings,
  ToolInfo,
} from "@/types/models";

interface LocalSettings {
  groups: Record<string, SecurityGroupPolicy>;
  auto_approve_workspace_writes: boolean;
  autonomy_mode: AutonomyMode;
  silent_mode: SilentModeSettings;
}

const initialSettings: LocalSettings = {
  groups: {},
  auto_approve_workspace_writes: false,
  autonomy_mode: "standard",
  silent_mode: DEFAULT_SILENT_POLICIES,
};

/**
 * Fill a possibly-partial silent_mode response with the documented defaults, so
 * local state (and the app-wide store) always carry a complete posture and a
 * save echoes back every sub-policy instead of resetting the omitted ones.
 */
function normalizeSilentMode(sm: SilentModeSettings | undefined): SilentModeSettings {
  return {
    tool_confirm: sm?.tool_confirm ?? DEFAULT_SILENT_POLICIES.tool_confirm,
    step_limit: sm?.step_limit ?? DEFAULT_SILENT_POLICIES.step_limit,
    ask_user: sm?.ask_user ?? DEFAULT_SILENT_POLICIES.ask_user,
    review_prompt: sm?.review_prompt ?? DEFAULT_SILENT_POLICIES.review_prompt,
  };
}

/**
 * Security settings tab, group-based (security.groups): the seven configurable
 * tool groups each have a policy dropdown; the execute group additionally has
 * a command-blocklist editor. The reserved "system" group is not configurable
 * and never rendered. Tool policies are group-level — the tool list inside
 * each card is display-only (data from GetToolList).
 */
export function SecuritySettings() {
  const [settings, setSettings] = useState<LocalSettings>(initialSettings);
  const [tools, setTools] = useState<ToolInfo[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [judgeAvailable, setJudgeAvailable] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  // Trusted-repositories dialog (git-config intake warnings dismissed with
  // "Trust this repo") — see TrustedReposDialog.
  const [trustedOpen, setTrustedOpen] = useState(false);
  // Hardened-repositories dialog (git-config intake warnings answered with
  // "Harden") — see HardenReposDialog.
  const [hardenOpen, setHardenOpen] = useState(false);

  // Fail-closed load: save() below normalizes the FULL seven-group payload
  // from local state. If the initial load failed, local state is empty and a
  // save would replace every live policy (and strip the execute blocklist)
  // with defaults — the exact fail-open path the backend rejects partial
  // payloads for. So the editable surface only renders after a successful
  // load; a failed load shows an error state with a retry.
  const load = useCallback(async () => {
    setIsLoading(true);
    setLoadError(null);
    setSaveError(null);
    try {
      const [r, toolList] = await Promise.all([getSecuritySettings(), getToolList()]);
      const silentMode = normalizeSilentMode(r.silent_mode);
      const autonomyMode = normalizeAutonomyMode(r.autonomy_mode);
      setSettings({
        groups: r.groups || {},
        auto_approve_workspace_writes: r.auto_approve_workspace_writes || false,
        autonomy_mode: autonomyMode,
        silent_mode: silentMode,
      });
      // Keep the app-wide posture (the review-prompt gate) in sync with the
      // authoritative read.
      useAutonomyStore.getState().setAutonomy({ autonomy_mode: autonomyMode, ...silentMode });
      setJudgeAvailable(r.judge_available ?? false);
      setTools(toolList || []);
    } catch (err) {
      logger.error("Failed to load security settings:", err);
      setLoadError(err instanceof Error ? err.message : String(err));
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Saves the FULL group set — exactly the seven configurable groups from
  // GROUP_ORDER — plus the two flags: the group schema only, never a per-tool
  // policy map. Normalizing here guarantees the payload shape even if the
  // backend response ever drifted. On failure the backend message is surfaced
  // and the displayed state is re-synced with the enforced one, so the UI
  // never shows policies/blocklist that were not persisted or applied.
  const save = async (next: LocalSettings) => {
    setSettings(next);
    const groups: Record<string, SecurityGroupPolicy> = {};
    for (const group of GROUP_ORDER) {
      groups[group] = next.groups[group] ?? { policy: DEFAULT_GROUP_POLICY };
    }
    try {
      await updateSecuritySettings({
        groups,
        auto_approve_workspace_writes: next.auto_approve_workspace_writes,
        autonomy_mode: next.autonomy_mode,
        silent_mode: next.silent_mode,
      } as SecuritySettingsResponse);
      setSaveError(null);
      // The save persisted the posture: reflect it in the app-wide store
      // immediately (the backend's config:updated re-fetch would too).
      useAutonomyStore.getState().setAutonomy({
        autonomy_mode: next.autonomy_mode,
        ...next.silent_mode,
      });
    } catch (err) {
      logger.error("Failed to update security settings:", err);
      // Wails rejects RPC failures with the backend error text as a plain
      // string (not an Error), so fall back to String(err) to keep the
      // backend's validation message — the canonical extraction pattern.
      setSaveError(err instanceof Error ? err.message : String(err));
      try {
        const r = await getSecuritySettings();
        const silentMode = normalizeSilentMode(r.silent_mode);
        const autonomyMode = normalizeAutonomyMode(r.autonomy_mode);
        setSettings({
          groups: r.groups || {},
          auto_approve_workspace_writes: r.auto_approve_workspace_writes || false,
          autonomy_mode: autonomyMode,
          silent_mode: silentMode,
        });
        useAutonomyStore.getState().setAutonomy({ autonomy_mode: autonomyMode, ...silentMode });
      } catch (reloadErr) {
        // The rollback re-fetch failed too: the displayed state is neither
        // persisted nor verified. Fail closed exactly like a failed initial
        // load — drop to the non-editable error screen instead of leaving
        // the optimistic (never-persisted) state editable.
        logger.error("Failed to re-read security settings after a failed save:", reloadErr);
        setLoadError(
          reloadErr instanceof Error ? reloadErr.message : String(reloadErr),
        );
      }
    }
  };

  const handlePolicy = (group: string, policy: GroupPolicy) => {
    const prev = settings.groups[group] ?? { policy: DEFAULT_GROUP_POLICY };
    save({
      ...settings,
      groups: { ...settings.groups, [group]: { ...prev, policy } },
    });
  };

  const handleBlocklist = (group: string, blocklist: string[]) => {
    const prev = settings.groups[group] ?? { policy: DEFAULT_GROUP_POLICY };
    save({
      ...settings,
      groups: { ...settings.groups, [group]: { ...prev, blocklist } },
    });
  };

  const handleAutoApprove = (checked: boolean) => {
    save({ ...settings, auto_approve_workspace_writes: checked });
  };

  const handleAutonomyMode = (mode: AutonomyMode) => {
    save({ ...settings, autonomy_mode: mode });
  };

  const handleSilentMode = (key: SilentSubPolicyKey, mode: string) => {
    save({
      ...settings,
      silent_mode: { ...settings.silent_mode, [key]: { mode } },
    });
  };

  // Tools grouped by their backend-reported security group.
  const toolsByGroup = tools.reduce<Record<string, ToolInfo[]>>((acc, t) => {
    const g = t.group || "";
    (acc[g] ??= []).push(t);
    return acc;
  }, {});

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8 gap-2">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading security settings...</span>
      </div>
    );
  }

  // Fail-closed: with no successfully-loaded state there is nothing safe to
  // save (see load()), so render an error state instead of editable controls.
  if (loadError) {
    return (
      <div className="flex flex-col items-start gap-3 p-4 rounded-lg border border-destructive/20 bg-destructive/5">
        <div className="flex items-start gap-2 text-sm">
          <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <span className="text-destructive">
            Failed to load security settings: {loadError}
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          Editing is disabled until the current policies load — saving without them could
          overwrite the enforced configuration with defaults.
        </p>
        <button
          type="button"
          onClick={() => void load()}
          className="flex items-center gap-2 px-3 py-1.5 rounded-md border border-border text-sm hover:bg-accent transition-colors"
        >
          <RotateCw className="h-3.5 w-3.5" />
          Retry
        </button>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      {saveError && (
        <div className="flex items-start gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm">
          <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <span className="text-destructive">{saveError}</span>
        </div>
      )}
      {/* Trusted repositories (dismissed git-config warnings) */}
      <div className="flex items-center justify-between gap-3 p-4 rounded-lg border border-border bg-card/50">
        <div className="min-w-0">
          <p className="text-sm font-medium">Trusted repositories</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Repositories whose &ldquo;untrusted git configuration&rdquo; warning you dismissed with
            &ldquo;Trust this repo&rdquo;. Removing an entry re-enables the warning.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => setTrustedOpen(true)}
          data-testid="trusted-repos-open"
        >
          <FolderGit2 className="h-3.5 w-3.5" />
          Trusted repos
        </Button>
      </div>

      {/* Hardened repositories (git-config warnings answered with "Harden") */}
      <div className="flex items-center justify-between gap-3 p-4 rounded-lg border border-border bg-card/50">
        <div className="min-w-0">
          <p className="text-sm font-medium">Hardened repositories</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Repositories you marked &ldquo;Harden&rdquo; on the &ldquo;untrusted git
            configuration&rdquo; warning. They are always neutralized and can never become raw-git
            eligible. Removing an entry returns the repository to the default intake path.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => setHardenOpen(true)}
          data-testid="harden-repos-open"
        >
          <ShieldBan className="h-3.5 w-3.5" />
          Hardened repos
        </Button>
      </div>

      {/* Workspace auto-approve toggle */}
      <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
        <div className="flex items-center gap-3">
          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={settings.auto_approve_workspace_writes}
              onChange={(e) => handleAutoApprove(e.target.checked)}
              className="sr-only peer"
            />
            <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary transition-colors after:content-[''] after:absolute after:top-0.5 after:inset-s-0.5 after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
          </label>
          <span className="text-sm font-medium">Auto-approve writes in workspace</span>
        </div>
        <p className="text-xs text-muted-foreground pl-12">
          When enabled, file write tools (write_file, edit_file, delete_file, delete_directory, create_directory)
          execute without confirmation when all paths are within the session roots — the workspace, the session temp
          directory, and any additional working directories (equal peers). Symlink traversals that resolve out of
          session roots are still forced to confirmation.
        </p>
      </div>

      {/* Autonomy mode — the single 3-state control that replaced the Smart
          Approve checkbox and the silent-mode master toggle. Each mode's
          description states the terminal difference honestly (which gated
          decisions end at a human card vs resolve automatically); the
          silent-mode sub-policy card renders only in Silent. */}
      <div
        className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50"
        data-testid="autonomy-mode-card"
      >
        <span className="text-sm font-medium">Autonomy mode</span>
        <div
          role="radiogroup"
          aria-label="Autonomy mode"
          data-testid="autonomy-mode-control"
          className="flex gap-1 p-1 rounded-lg bg-muted/50 w-fit"
        >
          {AUTONOMY_MODES.map((m) => (
            <label
              key={m.value}
              data-testid={`autonomy-mode-${m.value}`}
              className="cursor-pointer"
            >
              <input
                type="radio"
                name="autonomy-mode"
                value={m.value}
                checked={settings.autonomy_mode === m.value}
                onChange={() => handleAutonomyMode(m.value)}
                className="sr-only peer"
              />
              <span className="flex items-center px-3 py-1.5 rounded-md text-xs font-medium text-muted-foreground transition-colors peer-checked:bg-primary peer-checked:text-primary-foreground">
                {m.label}
              </span>
            </label>
          ))}
        </div>
        <p data-testid="autonomy-mode-description" className="text-xs text-muted-foreground">
          {autonomyModeMeta(settings.autonomy_mode).description}
        </p>
        {settings.autonomy_mode === "assisted" && !judgeAvailable && (
          <div
            role="note"
            data-testid="autonomy-judge-warning"
            className="flex items-start gap-2 text-xs text-warning"
          >
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
            <span>
              <strong>Judge unavailable.</strong> No LLM model is configured for the strict judge —
              every judged call degrades to a confirmation card, so Assisted currently behaves like
              Standard. Enable at least one LLM provider in settings to arm the judge.
            </span>
          </div>
        )}
      </div>

      {/* Silent-mode sub-policies — rendered only while the autonomy mode is
          Silent (the mode owns liveness; the card has no master switch).
          judgeAvailable drives a non-blocking warning for the selected
          judge-dependent sub-policies (see SilentModeCard). */}
      {settings.autonomy_mode === "silent" && (
        <SilentModeCard
          mode={settings.autonomy_mode}
          value={settings.silent_mode}
          judgeAvailable={judgeAvailable}
          onModeChange={handleSilentMode}
        />
      )}

      {/* The seven configurable tool groups */}
      {GROUP_ORDER.map((group) => (
        <SecurityGroupCard
          key={group}
          group={group}
          policy={settings.groups[group]?.policy ?? DEFAULT_GROUP_POLICY}
          blocklist={group === EXECUTE_GROUP ? settings.groups[group]?.blocklist ?? [] : []}
          tools={toolsByGroup[group] ?? []}
          onPolicyChange={handlePolicy}
          onBlocklistChange={handleBlocklist}
        />
      ))}

      {/* Shell-execution launch-shape override (the shell_exec config
          section): how the execute tool launches commands and which shell the
          command text is written in. Self-contained: loads and saves its own
          section via the shell-exec settings RPCs. */}
      <ShellExecutionCard />

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 shrink-0 mt-0.5" />
        <p>
          Policies apply to entire groups, not individual tools. <strong>User Confirm</strong> requires manual
          approval per call; <strong>Allow</strong> executes without confirmations (use with caution);{" "}
          <strong>Deny</strong> blocks execution. Internal orchestration tools are always allowed and not listed.
        </p>
      </div>

      <TrustedReposDialog open={trustedOpen} onOpenChange={setTrustedOpen} />
      <HardenReposDialog open={hardenOpen} onOpenChange={setHardenOpen} />
    </div>
  );
}
