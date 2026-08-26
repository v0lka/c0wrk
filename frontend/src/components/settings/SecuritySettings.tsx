import { useState, useEffect, useCallback } from "react";
import { Loader2, AlertTriangle, Info, RotateCw } from "lucide-react";
import { getSecuritySettings, updateSecuritySettings } from "@/api/config";
import { getToolList } from "@/api/mcp";
import { logger } from "@/lib/logger";
import { SecurityGroupCard } from "./SecurityGroupCard";
import {
  DEFAULT_GROUP_POLICY,
  EXECUTE_GROUP,
  GROUP_ORDER,
} from "@/lib/securityGroups";
import type {
  GroupPolicy,
  SecurityGroupPolicy,
  SecuritySettingsResponse,
  ToolInfo,
} from "@/types/models";

interface LocalSettings {
  groups: Record<string, SecurityGroupPolicy>;
  auto_approve_workspace_writes: boolean;
  smart_approve: boolean;
}

const initialSettings: LocalSettings = {
  groups: {},
  auto_approve_workspace_writes: false,
  smart_approve: false,
};

/**
 * Security settings tab, group-based (security.groups): the seven configurable
 * tool groups each have a policy dropdown; the execute group additionally has
 * a command-blacklist editor. The reserved "system" group is not configurable
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
  // Shipped execute-blacklist patterns (read-only, from the backend): the
  // blacklist editor offers them as a one-click restore.
  const [executeDefaults, setExecuteDefaults] = useState<string[]>([]);

  // Fail-closed load: save() below normalizes the FULL seven-group payload
  // from local state. If the initial load failed, local state is empty and a
  // save would replace every live policy (and strip the execute blacklist)
  // with defaults — the exact fail-open path the backend rejects partial
  // payloads for. So the editable surface only renders after a successful
  // load; a failed load shows an error state with a retry.
  const load = useCallback(async () => {
    setIsLoading(true);
    setLoadError(null);
    setSaveError(null);
    try {
      const [r, toolList] = await Promise.all([getSecuritySettings(), getToolList()]);
      setSettings({
        groups: r.groups || {},
        auto_approve_workspace_writes: r.auto_approve_workspace_writes || false,
        smart_approve: r.smart_approve || false,
      });
      setJudgeAvailable(r.judge_available ?? false);
      setExecuteDefaults(r.execute_blacklist_defaults ?? []);
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
  // never shows policies/blacklist that were not persisted or applied.
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
        smart_approve: next.smart_approve,
      } as SecuritySettingsResponse);
      setSaveError(null);
    } catch (err) {
      logger.error("Failed to update security settings:", err);
      // Wails rejects RPC failures with the backend error text as a plain
      // string (not an Error), so fall back to String(err) to keep the
      // backend's validation message — the canonical extraction pattern.
      setSaveError(err instanceof Error ? err.message : String(err));
      try {
        const r = await getSecuritySettings();
        setSettings({
          groups: r.groups || {},
          auto_approve_workspace_writes: r.auto_approve_workspace_writes || false,
          smart_approve: r.smart_approve || false,
        });
        setExecuteDefaults(r.execute_blacklist_defaults ?? []);
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

  const handleBlacklist = (group: string, blacklist: string[]) => {
    const prev = settings.groups[group] ?? { policy: DEFAULT_GROUP_POLICY };
    save({
      ...settings,
      groups: { ...settings.groups, [group]: { ...prev, blacklist } },
    });
  };

  const handleAutoApprove = (checked: boolean) => {
    save({ ...settings, auto_approve_workspace_writes: checked });
  };

  const handleSmartApprove = (checked: boolean) => {
    save({ ...settings, smart_approve: checked });
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

      {/* Smart Approve toggle */}
      <div className="flex flex-col gap-3 p-4 rounded-lg border border-border bg-card/50">
        <div className="flex items-center gap-3">
          <label className={`relative inline-flex items-center ${judgeAvailable ? "cursor-pointer" : "cursor-not-allowed opacity-50"}`}>
            <input
              type="checkbox"
              checked={settings.smart_approve}
              onChange={(e) => handleSmartApprove(e.target.checked)}
              disabled={!judgeAvailable}
              className="sr-only peer"
            />
            <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary transition-colors after:content-[''] after:absolute after:top-0.5 after:inset-s-0.5 after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
          </label>
          <span className="text-sm font-medium">Smart Approve</span>
        </div>
        {!judgeAvailable && (
          <div className="flex items-start gap-2 text-xs text-warning pl-12">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 mt-0.5" />
            <span>
              No LLM model configured for the strict judge. Enable at least one LLM provider
              in settings to use Smart Approve.
            </span>
          </div>
        )}
        <p className="text-xs text-muted-foreground pl-12">
          When enabled, a strict OWASP ASI (ASI01–ASI10) judge automatically evaluates calls that require
          confirmation. A safe verdict executes without UI; any risk, error, or ambiguity falls back to manual
          confirmation. Only affects the effective allow and user_confirm policies — deny and symlink-forced
          confirmations are unchanged.
        </p>
      </div>

      {/* The seven configurable tool groups */}
      {GROUP_ORDER.map((group) => (
        <SecurityGroupCard
          key={group}
          group={group}
          policy={settings.groups[group]?.policy ?? DEFAULT_GROUP_POLICY}
          blacklist={group === EXECUTE_GROUP ? settings.groups[group]?.blacklist ?? [] : []}
          blacklistDefaults={group === EXECUTE_GROUP ? executeDefaults : undefined}
          tools={toolsByGroup[group] ?? []}
          onPolicyChange={handlePolicy}
          onBlacklistChange={handleBlacklist}
        />
      ))}

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 shrink-0 mt-0.5" />
        <p>
          Policies apply to entire groups, not individual tools. <strong>User Confirm</strong> requires manual
          approval per call; <strong>Allow</strong> executes without confirmations (use with caution);{" "}
          <strong>Deny</strong> blocks execution. Internal orchestration tools are always allowed and not listed.
        </p>
      </div>
    </div>
  );
}
