import { create } from 'zustand'

/**
 * Effective HTTP-proxy state shared between the settings tabs.
 *
 * The per-provider TLS pin is inert while the proxy dials for a provider's
 * host — the proxy is effective AND the host is not on the bypass list
 * (ADR-054) — so the LLM tab has to disable its pin controls the moment the
 * proxy is toggled in the General tab. It cannot learn that by re-reading
 * the backend config for two reasons:
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
 * The bypass list rides along because the gate is per host: a host on
 * proxy.bypass_list dials directly, so its pin (and the Get probe) applies
 * even while the proxy is on ("bypass re-arms the pin", ADR-054). The list
 * uses the same matching semantics as the Go proxy.BypassMatcher: exact
 * host match, case-insensitive, plus "*.domain.com" wildcard suffixes.
 *
 * `active: null` means "not known yet" — nothing has read the proxy config in
 * this dialog session.
 */
interface ProxyDraftState {
  /** Effective proxy state, or null when it has not been established yet. */
  active: boolean | null
  /** Draft proxy.bypass_list, empty until the General tab publishes it. */
  bypassList: string[]
}

interface ProxyDraftActions {
  /**
   * Authoritative write from the General tab: the user just changed the proxy
   * settings, or ProxySettings loaded them from the backend. Always applies.
   */
  setActive: (active: boolean) => void
  /**
   * Authoritative write of the draft bypass list from the General tab
   * (on load and on every bypass edit, before the debounce).
   */
  setBypassList: (list: string[]) => void
  /**
   * Best-effort seed from a component that merely happens to have the proxy
   * section of a config payload (the LLM tab's own getConfig). It is a NO-OP
   * once a value is known, because the known value may be a fresher draft
   * than anything the backend has persisted — seeding over it would
   * re-enable controls the user just disabled.
   */
  seedActive: (active: boolean) => void
  /**
   * Best-effort seed of the bypass list: same no-op-once-known rule as
   * seedActive, so a stale config payload never overwrites the draft.
   */
  seedBypassList: (list: string[]) => void
}

export const useProxyDraftStore = create<ProxyDraftState & ProxyDraftActions>((set, get) => ({
  active: null,
  bypassList: [],
  setActive: (active) => set({ active }),
  setBypassList: (bypassList) => set({ bypassList }),
  seedActive: (active) => {
    if (get().active !== null) return
    set({ active })
  },
  seedBypassList: (bypassList) => {
    // The General tab always publishes; a seed only fills the unknown case.
    if (get().active !== null) return
    set({ bypassList })
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

/**
 * Computes the per-provider TLS-pin gate for one base URL (ADR-054): true
 * when the proxy DIALS for that host — the proxy is effective and the host
 * is NOT on the bypass list. `active === null` (unknown) reads as "no
 * proxy": the permissive side, guarded by the backend RPC either way.
 * Call in a component via useMemo over selectProxyActive + the draft list,
 * never inside a Zustand selector (a fresh string[] must not be allocated
 * per render).
 */
export function pinGatedByProxy(
  active: boolean | null,
  bypassList: string[],
  baseUrl: string | undefined,
): boolean {
  if (active !== true) return false
  const host = hostOf(baseUrl)
  if (host === '') return true
  return !isBypassed(host, bypassList)
}

/** Hostname (no port, no scheme) of a URL, or '' when unparseable/absent. */
function hostOf(rawUrl: string | undefined): string {
  if (!rawUrl) return ''
  try {
    // A bare "llm.lan:8443" has no scheme and fails URL parsing; that shape
    // never reaches the gate (the form's Get button already requires a
    // parseable base URL), so '' → gate closed is the safe default.
    return new URL(rawUrl).hostname.toLowerCase()
  } catch {
    return ''
  }
}

/**
 * Same matching semantics as the Go proxy.BypassMatcher: exact host match
 * (case-insensitive) or "*.domain.com" wildcard suffix.
 */
export function isBypassed(host: string, bypassList: string[]): boolean {
  const lower = host.toLowerCase()
  return bypassList.some((entry) => {
    const e = entry.trim().toLowerCase()
    if (e === '') return false
    if (e === lower) return true
    if (e.startsWith('*.')) return lower.endsWith(e.slice(1)) // ".domain.com"
    return false
  })
}

/** Effective proxy state from a config payload's proxy section. */
export function isProxyEffective(proxy: { enabled?: boolean; url?: string } | null | undefined): boolean {
  return proxy?.enabled === true && !!proxy.url
}
