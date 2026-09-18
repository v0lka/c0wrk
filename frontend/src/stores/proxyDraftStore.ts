import { create } from 'zustand'

/**
 * Effective HTTP-proxy state shared between the settings tabs.
 *
 * The per-provider TLS pin is inert while a proxy is active (proxy wins,
 * ADR-052), so the LLM tab has to disable its pin controls the moment the
 * proxy is toggled in the General tab. It cannot learn that by re-reading the
 * backend config for two reasons:
 *
 *  - ProxySettings persists on an 800 ms debounce, so a re-read right after
 *    the toggle returns the STALE persisted value.
 *  - UpdateProxySettings rebuilds the proxy, MCP gateway, router and judge;
 *    a config read racing that rebuild is exactly the contention that freezes
 *    the dialog.
 *
 * So this store holds the DRAFT state — what the user currently sees in the
 * General tab — written synchronously on every edit, before the debounce.
 * "Effective" mirrors proxy.BuildTransport on the Go side: enabled AND a
 * non-empty URL. An enabled-but-empty proxy dials directly, so the pin stays
 * meaningful there and this stays false.
 *
 * `active: null` means "not known yet" — nothing has read the proxy config in
 * this dialog session.
 */
interface ProxyDraftState {
  /** Effective proxy state, or null when it has not been established yet. */
  active: boolean | null
}

interface ProxyDraftActions {
  /**
   * Authoritative write from the General tab: the user just changed the proxy
   * settings, or ProxySettings loaded them from the backend. Always applies.
   */
  setActive: (active: boolean) => void
  /**
   * Best-effort seed from a component that merely happens to have the proxy
   * section of a config payload (the LLM tab's own getConfig). It is a NO-OP
   * once a value is known, because the known value may be a fresher draft
   * than anything the backend has persisted — seeding over it would
   * re-enable controls the user just disabled.
   */
  seedActive: (active: boolean) => void
}

export const useProxyDraftStore = create<ProxyDraftState & ProxyDraftActions>((set, get) => ({
  active: null,
  setActive: (active) => set({ active }),
  seedActive: (active) => {
    if (get().active !== null) return
    set({ active })
  },
}))

/**
 * Selector for consumers that need a plain boolean: an unknown state is
 * treated as "no proxy", which is the permissive reading — the pin controls
 * stay usable, and the backend still guards the fingerprint RPC.
 *
 * Returns a scalar, so it is safe to call directly inside a component
 * (a selector returning a fresh object/array would loop under React 19's
 * useSyncExternalStore reference comparison).
 */
export const selectProxyActive = (s: ProxyDraftState): boolean => s.active === true

/** Effective proxy state from a config payload's proxy section. */
export function isProxyEffective(proxy: { enabled?: boolean; url?: string } | null | undefined): boolean {
  return proxy?.enabled === true && !!proxy.url
}
