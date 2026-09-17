# ADR-053: Proxy Wins Over the Per-Provider TLS Pin

## Status

Accepted — amends [ADR-052](./052-per-provider-tls-pinning.md) (the pin remains
in force; its interaction with the HTTP proxy is settled here). All ADR-052
decisions remain in force except where this ADR narrows them.

## Context

ADR-052 added the per-provider `tls_fingerprint` SPKI pin so self-signed LLM
endpoints connect without disabling verification wholesale. Its four dial
paths derive the pinned client from the builder's **proxy client** when one
is configured (`llmtls.Client(proxyClient, pin, …)`), layering the pin on
top of the proxy transport.

That composition is wrong in practice:

- **The proxy owns the TLS story on proxied connections.** Corporate proxies
  (explicit or transparent) routinely re-encrypt traffic with their own
  certificate — a MITM by design, sanctioned by the operator. The proxy
  section already carries its own trust mechanism for exactly this case:
  `proxy.tls_cert_dir` adds the corporate root CA to the transport's CA
  pool. A pin applied on top of a MITM proxy rejects the proxy's
  certificate (the origin's key is never seen), making a legitimately
  configured setup unconnectable.
- **A pinned direct dial behind a proxy is a policy violation.** With
  `Proxy: http.ProxyURL(...)` set, layering `InsecureSkipVerify` + SPKI
  verify on the same transport does not pin the ORIGIN — the CONNECT tunnel
  forwards TLS end-to-end, so the pin would apply to the origin cert, but
  the operator's explicit choice to route through the proxy (visibility,
  auth, DLP) is what governs. Both mechanisms claim the same knob: who
  verifies the server.
- **Two verification mechanisms on one transport are fragile.** llmtls
  replaces `VerifyPeerCertificate` on a clone of the proxy transport; any
  custom RoundTripper (retry wrapper, observability) is silently swapped
  for a default-transport clone — proxy settings ride along only by
  accident of transport type.

The user-facing symptom: with a proxy configured AND a pin set, connections
fail with `fingerprint mismatch` even though the user configured both
features correctly per their own documentation.

### Forces

- **The proxy is a global network policy; the pin is a per-provider trust
  decision.** When both are configured, the broader policy scope wins —
  routing every provider through the proxy is the operator's explicit
  choice, and it predates the pin.
- **No silent mode changes.** A user who configures both must be able to
  see why the pin is inert, or the combination reads as a bug.
- **Bypass list already exists.** `proxy.bypass_list` is the sanctioned
  escape for endpoints that must NOT go through the proxy — putting the
  self-signed server on the bypass list restores the pin's direct-dial
  semantics without a new mechanism.

## Decision

1. **Proxy wins.** When an effective proxy is configured — `proxy.enabled`
   is true AND `proxy.url` is non-empty, the exact rule
   `proxy.BuildTransport` already applies — the per-provider TLS pin is
   ignored on every dial path:

   | proxy state                     | client used                                     |
   | ------------------------------- | ----------------------------------------------- |
   | effective (enabled + URL)       | the plain proxy client, unchanged (pin ignored) |
   | not effective                   | pre-ADR-053 behavior (ADR-052 pin applies)      |

   With no proxy, nothing changes: a non-empty pin still yields the pinned
   direct client (ADR-052).

2. **One resolver, one rule.** `llmtls.ResolveProviderClient(proxyClient,
   pin, logger)` encodes the table above and is the single source of truth
   for the Fetch Models listing, the lazy context-window probe, and any
   future dial path. The router-entry path encodes the same rule at
   construction (`providerEntryFromConfig` takes `proxyActive bool`; the
   entry's `HTTPClient` stays nil while a proxy is active, so the router's
   shared proxy client dials exactly as before the pin existed).

3. **The Get-fingerprint button is guarded, not silently broken.** The
   `GetProviderTLSCertificate` RPC rejects with an actionable error while
   an effective proxy is configured ("disable the proxy to pin this
   server's certificate") — the probe dials directly, so a fetched pin
   would be inert the moment it is saved. The guard mirrors the effective
   rule (enabled AND URL): an enabled-but-empty proxy URL dials directly
   and keeps the button functional.

4. **The Settings UI disables the whole TLS section, not just the button.**
   While the LLM tab loads an effective proxy state from the same
   `getConfig` payload (`proxy.enabled && proxy.url`), the "Custom TLS
   fingerprint" checkbox, the fingerprint input, and the Get button are
   disabled, and an explanatory comment is shown: the pin does not apply
   to proxied connections; disable the proxy (or put the endpoint on the
   proxy bypass list) to use certificate pinning. The persisted pin is
   preserved — disabling the proxy later re-arms it without re-entry.

5. **The bypass list is the documented route for both at once.** A user
   who wants a proxy AND a pinned self-signed server adds the server's
   host to `proxy.bypass_list` (Settings → General → HTTP Proxy → Bypass
   List): the proxy client bypasses the host (direct dial) and the pin
   applies, because the effective-proxy rule only gates the pin's
   **application**, not its configuration.

## Consequences

- The failing combination (proxy + pin) now connects: the proxy client
  dials with the proxy's own TLS config (`tls_cert_dir` CA pool, or system
  verification).
- A configured pin is preserved but inert while the proxy is active; it
  re-arms automatically when the proxy is disabled. Users who need both
  simultaneously use the bypass list.
- The UI never shows an interactive control that silently does nothing.
- `llmtls.Client` itself is unchanged — it remains the pure pinning
  primitive for direct connections; `ResolveProviderClient` composes it
  with the proxy policy. Existing `llmtls.Client` callers outside the
  provider dial paths are unaffected.
- The fingerprint RPC gains a rejection branch; the direct probe remains
  available whenever the proxy is off.

## Alternatives Considered

- **Pin the origin through the proxy (status quo ex ante).** Rejected: a
  MITM proxy re-encrypts, the origin key never reaches the client, the pin
  rejects the proxy cert — legitimately configured setups become
  unconnectable. The two mechanisms answer the same question ("who
  verifies the server?") and cannot both hold on one transport.
- **Pin the proxy's own certificate instead.** Rejected: the user pinned
  the provider endpoint, not the proxy; pinning a different host's key
  than the one configured is surprising, and the proxy already has a
  trust mechanism (`tls_cert_dir`).
- **Auto-disable the proxy for pinned providers (per-provider proxy
  opt-out).** Rejected: routing policy is global and operator-owned;
  carving per-provider holes undermines it and multiplies states. The
  bypass list already expresses "this host skips the proxy" cleanly.
- **Keep the pin applied and surface the mismatch error.** Rejected: the
  error is precisely the confusing failure this ADR exists to remove; the
  RFC 7469-style bare error deliberately carries no remediation hint.
- **Ignore the proxy for the Get-fingerprint probe (probe always direct)
  and save the fetched pin regardless.** Rejected as the default: a saved
  pin that is inert (proxy active) invites "I pressed Get and nothing
  works" reports. The guard turns that into an actionable message. (The
  probe itself remains a direct dial by nature — it reads the endpoint's
  certificate, which is exactly what the user is about to trust.)
