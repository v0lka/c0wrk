import { useState, useEffect } from "react";
import { Loader2, AlertTriangle, Info } from "lucide-react";
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
  const [judgeAvailable, setJudgeAvailable] = useState(false);

  useEffect(() => {
    Promise.all([
      getSecuritySettings().then((r) => {
        setSettings({
          groups: r.groups || {},
          auto_approve_workspace_writes: r.auto_approve_workspace_writes || false,
          smart_approve: r.smart_approve || false,
        });
        setJudgeAvailable(r.judge_available ?? false);
      }),
      getToolList().then((r) => setTools(r || [])),
    ])
      .catch((err) => logger.error("Failed to load security settings:", err))
      .finally(() => setIsLoading(false));
  }, []);

  // Saves the FULL group set — exactly the seven configurable groups from
  // GROUP_ORDER — plus the two flags: the group schema only, never a per-tool
  // policy map. Normalizing here guarantees the payload shape even if the
  // backend response ever drifted.
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
    } catch (err) {
      logger.error("Failed to update security settings:", err);
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

  return (
    <div className="flex flex-col gap-6">
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
          execute without confirmation when all paths are within the session workspace and temp. Symlink traversals are
          still forced to confirmation.
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
          confirmation. Only affects the effective user_confirm policy — allow, deny, and symlink-forced
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
