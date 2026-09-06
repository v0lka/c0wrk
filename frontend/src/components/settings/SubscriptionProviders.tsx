import { ChevronDown, ChevronRight } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { invalidateConfigCache } from "@/hooks/useConfigData";
import { getConfig, updateLLMConfig } from "@/api/config";
import { Button } from "@/components/ui/button";
import { openExternalURL } from "@/api/runtime";
import {
  cancelSubscriptionLogin,
  connectSubscription,
  getSubscriptionStatuses,
  logoutSubscription,
  type SubscriptionStatus,
} from "@/api/subscriptions";

const POLL_INTERVAL_MS = 1_000;

export function SubscriptionProviders({
  isExpanded,
  onToggle,
  onConnected,
}: {
  isExpanded: boolean;
  onToggle: () => void;
  onConnected?: () => Promise<void>;
}) {
  const [statuses, setStatuses] = useState<SubscriptionStatus[]>([]);
  const [catalog, setCatalog] = useState<string[]>([]);
  const [enabledModels, setEnabledModels] = useState<string[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const pollRef = useRef<number | undefined>(undefined);

  const refreshModels = useCallback(async () => {
    const config = await getConfig();
    const subscription = config.llm.subscriptions?.find(
      (item) => item.provider === "chatgpt_subscription",
    );
    setCatalog(subscription?.models ?? []);
    setEnabledModels(subscription?.enabled ?? []);
  }, []);

  const refresh = useCallback(async () => {
    try {
      await Promise.all([
        getSubscriptionStatuses().then(setStatuses),
        refreshModels(),
      ]);
    } catch {
      setStatuses([]);
      setCatalog([]);
      setEnabledModels([]);
    }
  }, [refreshModels]);

  const stopPolling = useCallback(() => {
    if (pollRef.current !== undefined) window.clearInterval(pollRef.current);
    pollRef.current = undefined;
  }, []);

  const startPolling = useCallback(
    (provider: string) => {
      stopPolling();
      pollRef.current = window.setInterval(() => {
        void getSubscriptionStatuses().then((next) => {
          setStatuses(next);
          const status = next.find((item) => item.provider === provider);
          if (status?.connected || !status?.connecting) {
            if (status?.connected) {
              invalidateConfigCache();
              void onConnected?.();
            }
            stopPolling();
            setBusy("");
          }
        });
      }, POLL_INTERVAL_MS);
    },
    [onConnected, stopPolling],
  );

  useEffect(() => {
    void refresh();
    return stopPolling;
  }, [refresh, stopPolling]);

  const toggleModel = async (model: string) => {
    const next = enabledModels.includes(model)
      ? enabledModels.filter((item) => item !== model)
      : [...enabledModels, model];
    setEnabledModels(next);
    try {
      await updateLLMConfig({
        subscription_models: { chatgpt_subscription: next },
      });
      invalidateConfigCache();
      await onConnected?.();
    } catch {
      await refreshModels();
      setError("Could not update visible subscription models. Try again.");
    }
  };

  const connect = async (provider: string) => {
    setBusy(provider);
    setError("");
    try {
      const login = await connectSubscription(provider);
      openExternalURL(login.authorization_url);
      startPolling(provider);
    } catch {
      setBusy("");
      setError("Could not start browser sign-in. Try again.");
      await refresh();
    }
  };

  const cancel = async (provider: string) => {
    try {
      await cancelSubscriptionLogin(provider);
    } finally {
      stopPolling();
      setBusy("");
      await refresh();
    }
  };

  const logout = async (provider: string) => {
    setBusy(provider);
    setError("");
    try {
      await logoutSubscription(provider);
    } catch {
      setError("Could not log out. Try again.");
    } finally {
      setBusy("");
      await refresh();
    }
  };

  return (
    <section
      className="rounded-lg border"
      aria-label="Managed provider sign-in"
    >
      <button
        type="button"
        className="flex w-full items-center justify-between px-4 py-3 text-sm font-medium hover:bg-muted/50"
        onClick={onToggle}
        aria-expanded={isExpanded}
      >
        <span className="flex items-center gap-2">
          {isExpanded ? (
            <ChevronDown className="h-4 w-4" />
          ) : (
            <ChevronRight className="h-4 w-4" />
          )}
          ChatGPT subscription
        </span>
        <span className="text-xs text-muted-foreground">
          {enabledModels.length} model{enabledModels.length !== 1 ? "s" : ""}{" "}
          enabled
        </span>
      </button>

      {isExpanded && (
        <div className="flex flex-col gap-3 border-t px-4 py-4">
          <div>
            <p className="text-xs text-muted-foreground">
              Sign in with ChatGPT in your browser. Credentials are kept in the
              operating-system secure store and are never added to config files.
            </p>
          </div>
          <p className="rounded-md bg-muted p-2 text-xs text-muted-foreground">
            <strong>ChatGPT personal-use compatibility (experimental):</strong>{" "}
            c0wrk never imports browser cookies, scrapes sessions, or exposes
            tokens.
          </p>
          {error && (
            <p className="text-xs text-destructive" role="alert">
              {error}
            </p>
          )}
          {statuses.map((status) => (
            <div
              key={status.provider}
              className="flex items-center justify-between gap-3 text-sm"
            >
              <span>
                <strong>ChatGPT / Codex</strong>
                <span className="ml-2 text-xs text-muted-foreground">
                  {status.connected
                    ? "Connected"
                    : status.connecting
                      ? "Waiting for browser sign-in…"
                      : status.message || "Disconnected"}
                </span>
              </span>
              {status.available &&
                (status.connected ? (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy === status.provider}
                    onClick={() => void logout(status.provider)}
                  >
                    Log out
                  </Button>
                ) : status.connecting || busy === status.provider ? (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void cancel(status.provider)}
                  >
                    Cancel
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    onClick={() => void connect(status.provider)}
                  >
                    Connect
                  </Button>
                ))}
            </div>
          ))}
          {catalog.length > 0 && (
            <div className="flex flex-col gap-2 border-t pt-3">
              <div>
                <h5 className="text-sm font-medium">Subscription models</h5>
                <p className="text-xs text-muted-foreground">
                  Choose the models shown in chat and available as the default
                  model.
                </p>
              </div>
              <div className="grid gap-1">
                {catalog.map((model) => (
                  <label
                    key={model}
                    className="flex cursor-pointer items-center gap-2 text-sm"
                  >
                    <input
                      type="checkbox"
                      checked={enabledModels.includes(model)}
                      disabled={busy !== ""}
                      onChange={() => void toggleModel(model)}
                    />
                    {model}
                  </label>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
