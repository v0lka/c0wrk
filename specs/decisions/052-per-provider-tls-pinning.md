# ADR-052: Per-Provider TLS Verification Override via SPKI Pinning

## Status

Accepted

## Context

Self-hosted LLM endpoints (vLLM, llama.cpp, LM Studio, an internal gateway)
are routinely served over TLS with a self-signed certificate or one issued by
an internal PKI the host OS does not trust. c0wrk could not reach them at all:
every provider dial path used the system CA pool, so the connection failed
with a bare `x509: certificate signed by unknown authority` and the provider
was unusable.

The obvious escape — a per-provider "skip TLS verification" switch — trades a
working connection for no transport security whatsoever, on the exact path
that carries the user's API keys and their entire workspace content. A
mechanism was needed that restores reachability without giving up
authentication of the peer.

Two further forces shaped the design:

- **An HTTP proxy already owns the TLS story on proxied connections.**
  `proxy.tls_cert_dir` exists precisely so a corporate MITM proxy's root CA
  can be trusted. A second, per-provider mechanism answering the same
  question — who verifies the server? — cannot also hold on the same
  transport.
- **The settings dialog saves on a debounce with partial payloads.** Any new
  provider field has to survive a save that did not mention it, or editing
  the model list would silently erase the pin.

## Decision

### 1. One key, and the pin is the switch

Compatible providers (`llm.openai_compatible.<name>`,
`llm.anthropic_compatible.<name>`) gain a single key:

```yaml
llm:
  openai_compatible:
    selfhosted:
      base_url: "https://llm.lan:8443/v1"
      tls_fingerprint: "k3J9vQ1Z…base64(SHA-256(SPKI DER))…"
```

Exactly two states exist:

| `tls_fingerprint` | connection                                  |
| ----------------- | ------------------------------------------- |
| empty / absent    | normal system CA verification; no override  |
| non-empty         | ONLY the pinned SPKI is accepted            |

A non-empty pin activates the override by itself. There is no separate
enable toggle, and — deliberately — **no configuration that accepts an
arbitrary certificate**: a misconfigured provider fails closed with
`llmtls.ErrPinMismatch` rather than silently dropping verification. The only
unverified handshake in the system is the fingerprint probe (§5), which
carries no credentials.

The format is `base64(StdEncoding, SHA-256(SubjectPublicKeyInfo DER))` — the
Chromium CertificatePinList / RFC 7469 construction — so a pin survives
certificate renewal as long as the key pair is reused. Comparison is
whitespace-tolerant so a value pasted with line wraps still matches. A pin
that cannot be base64-decoded to 32 bytes is logged as a Warn (a typo is then
distinguishable from a genuine key rotation) but still flows to the
handshake, where it fails like any other mismatch.

Fixed providers (`anthropic`, `chatgpt`) have no such key: they talk to
vendor endpoints with publicly trusted certificates, where pinning adds
breakage without adding trust.

### 2. Proxy wins, on every dial path

When an effective proxy is configured — `proxy.enabled` is true AND
`proxy.url` is non-empty, the exact rule `proxy.BuildTransport` already
applies — the per-provider pin is **ignored**:

| proxy state               | behavior                                      |
| ------------------------- | --------------------------------------------- |
| effective (enabled + URL) | the plain proxy client dials; pin ignored     |
| not effective             | a non-empty pin yields the pinned direct client |

The proxy is a global network policy chosen by the operator and it predates
the per-provider trust decision. A MITM proxy re-encrypts with its own
certificate, so the origin's key never reaches the client — a pin layered on
top would reject a legitimately configured setup. The proxy also already has
its own trust mechanism (`proxy.tls_cert_dir`).

`proxy.bypass_list` is the documented route for wanting both at once: a
bypassed host dials directly, so its pin applies. The effective-proxy rule
gates the pin's *application*, never its configuration — a pin stays
persisted while a proxy is active and re-arms the moment the proxy is off,
with no re-entry.

### 3. Two resolvers, because the dial paths are not interchangeable

`core/llmtls` exposes `Client` (the pinning primitive) plus two resolvers
that encode the rule above for the two shapes of dial path. They are separate
because returning the proxy client from the wrong one is a live bug:

- `RouterEntryClient(proxyActive, base, pin, logger)` → the chat/inference
  path, materialized as `llm.ProviderEntry.HTTPClient`. Returns **nil**
  whenever a proxy is active or no pin is set. nil makes the SDK fall back to
  `llm.RouterConfig.HTTPClient`, which already carries the proxy transport
  **and** `timeouts.llmRequestTimeout`. Handing the raw proxy client back
  here would shadow that client and cap every inference request at
  `timeouts.webFetchProxyTimeout` (30 s) — long enough to break reasoning
  models. When a pin does apply, the client is cloned from the shared LLM
  client, so it inherits that long timeout.
- `DirectDialClient(proxyClient, pin, logger)` → paths with no router-level
  fallback (the Fetch Models listing, the lazy context-window probe), each of
  which bounds its own requests with a context deadline. Returns the proxy
  client **verbatim** when one exists; nil would mean "dial directly" and
  quietly bypass the operator's routing policy.

The pinned `*http.Transport` is derived **once**, at client construction:
`http.Transport.Clone()` does not carry over the idle-connection pool, so
cloning per request would drop every keep-alive connection and force a fresh
TCP+TLS handshake each time. The result is a concrete `*http.Transport`, not
a wrapper, so `http.Client.CloseIdleConnections` still reaches the pool. A
base transport that is not an `*http.Transport` cannot hold a `tls.Config`;
it is replaced by a default-transport clone with a Warn, so a retry or
observability wrapper never disappears silently.

Mismatch errors carry the fact of the mismatch and nothing else — no
certificate bytes, no subject, no DNS names.

### 4. All provider dial paths honor the rule

`core/builder.go` applies it at every point that opens a connection to a
provider endpoint:

- **Chat / inference** — `providerEntryFromConfig` → `RouterEntryClient` →
  `llm.ProviderEntry.HTTPClient`.
- **Fetch Models** — `fetchProviderModels` → `DirectDialClient` →
  `listOpenAIModels` / `listAnthropicModels`. Includes the unsaved-draft path
  (`applyListProviderModelsOverrides`), so a pin works before the provider is
  ever persisted.
- **Lazy context-window probe** — `lookupOpenAIProviderBaseURL` carries the
  pin out with the base URL and key; `buildLocalModelProbe` resolves the
  client before dispatching the detached probe.

`proxyActive` is derived as `b.proxyClient != nil`, which is exactly
`enabled && url != ""` because `proxy.BuildClient` returns a nil client in
every other case. The proxy client is snapshotted once per call under
`b.mu.RLock`; no lock is held across a network call.

`ModelRegistry.SetHTTPClient` is deliberately untouched: it fetches
HuggingFace metadata, not provider endpoints.

### 5. The Get button is unconditional

`FrontendAPI.GetProviderTLSCertificate` performs a TLS handshake against the
provider endpoint and returns the leaf certificate's SPKI pin. No HTTP
request is sent and no API key is involved; verification is skipped because
the fingerprint IS what is being fetched.

The call takes **no fingerprint argument** and reads no persisted pin. Its
answer is always "what is this endpoint serving right now?", identical
whether the provider is unpinned, correctly pinned, or pinned to something
that does not match at all — and it overwrites the field. Deciding what to do
with the result is the user's business, not the probe's. The signature is
guarded by a compile-time assertion in the package tests so a pin parameter
cannot be added back by accident.

The RPC is rejected with an actionable error while an effective proxy is
configured: the probe dials directly, so a pin fetched then would be inert
the moment it is saved. The rejection happens before any network access and
names both remedies (disable the proxy, or use the bypass list).

### 6. The pointer sentinel lives only at the API boundary

`ProviderConfigRequest.TLSFingerprint` and
`ListProviderModelsRequest.TLSFingerprint` are `*string`:

- `nil` = keep the persisted pin. This is what a debounced partial save
  sends when only credentials or the model list changed; without the
  sentinel every such save would clear the pin.
- non-nil = apply verbatim, so an explicit `""` is the deliberate "clear the
  pin" signal.

`resolveTLSFingerprint` is the single place that applies this. Persisted
config (`backend/config`) and the builder layer carry plain strings — the
sentinel does not leak inward.

### 7. No lock is held across a network call

`GetProviderTLSCertificate` snapshots the effective proxy state and the
persisted base URL under `configMu.RLock` and **releases the lock** before
the handshake (bounded by `llmtls.FetchFingerprintTimeout`, 10 s).

This rule is enforced by a test that contends with a **writer**, not a
reader: the RPC takes a read lock, and two readers never block each other, so
a concurrent `GetConfig` cannot detect the regression. A parked reader plus
one queued writer is the chain that matters, because Go's `sync.RWMutex`
stops admitting readers once a writer is waiting.

The same reasoning fixed a pre-existing instance of the hazard on the path
this feature makes users walk. `UpdateProxySettings` held the configMu
**write** lock across `RebuildProxy`, which restarts the MCP gateway and
rebuilds the router and judge, each waiting on builder readiness with its own
30-second budget. Toggling the proxy and switching to the LLM tab therefore
froze the dialog for the duration of the rebuild. It now follows the
`UpdateLLMConfig` pattern: `saveMu` serializes save sequences, configMu
covers only the field mutation and the (bounded, local, atomic) disk write,
and the propagation runs outside the lock.

### 8. The UI has no toggle, and reads proxy state from a draft store

The settings form renders the fingerprint field and the Get button
unconditionally for a compatible provider. There is no "Custom TLS
fingerprint" checkbox: it would duplicate the state the pin already encodes,
and — because Get lives inside that section — it would hide the button behind
a checkbox the user has no reason to tick before they have a fingerprint to
paste. Clearing the field switches the override off. The only local state in
the form is the Get button's in-flight and error state.

While a proxy is effective the field, the button, and their help text are
disabled with an explanation naming Settings → General → HTTP Proxy and the
bypass list. The persisted pin stays visible and intact.

The gate reads `frontend/src/stores/proxyDraftStore.ts`, not the backend
config. `ProxySettings` publishes the effective state synchronously on every
edit, ahead of its own 800 ms debounce, so an already-mounted LLM tab reacts
immediately. A config re-read could not serve this: it would return the stale
persisted value inside the debounce window, and it would contend with the
proxy rebuild described in §7. The LLM tab only *seeds* the store from its
own `getConfig` — a no-op once a value is known, so it never overwrites a
fresher draft.

## Consequences

- Self-signed LLM endpoints are reachable on every dial path — chat, model
  listing, and the context-window probe — with the peer still authenticated
  by its public key.
- No configuration accepts an arbitrary certificate. The failure mode of a
  wrong pin is a refused connection, not a silent downgrade.
- A pin survives certificate renewal that reuses the key pair, and survives
  debounced partial saves.
- A pin is inert but preserved while a proxy is active, and re-arms by
  itself. Users who need both at once use `proxy.bypass_list`.
- The pinned inference client inherits the long LLM request timeout; the two
  resolvers exist to keep that true.
- TLS policy stays in c0wrk. sp4rk only transports the client it is handed
  (`llm.ProviderEntry.HTTPClient`), and its fallback to the router-level
  client is what makes the nil return meaningful.
- Two pre-existing defects were fixed in passing, both on paths this feature
  touches: the `chatgpt` and `openai` branches of `fetchProviderModels`
  ignored the configured proxy entirely, and `b.proxyClient` was read there
  without `b.mu` (a race against `RebuildProxy`).
- Mismatch errors are deliberately uninformative. Diagnosing one means
  re-running Get and comparing, which is the intended workflow.
- The Get button silently overwrites an existing pin. That is the specified
  behavior; the value lands in the form field rather than straight in the
  config, so the change is visible and reversible by clearing the field
  before the debounce settles.

## Alternatives Considered

- **A per-provider "skip TLS verification" flag.** Rejected: it removes peer
  authentication from the path that carries API keys and workspace content,
  and it is indistinguishable in config from a deliberate, scoped trust
  decision. Pinning restores reachability without giving up authentication.
- **A separate `tls_override_enabled` boolean beside the pin.** Rejected: it
  creates a reachable "override on, pin empty" state whose only sensible
  meaning is accept-anything, which §1 exists to forbid. The pin's emptiness
  is already a perfectly good switch.
- **A "Custom TLS fingerprint" checkbox gating the field in the UI.**
  Rejected for the same reason plus a concrete usability defect: the Get
  button sits in the gated section, so a user with no pin yet — exactly the
  person who needs Get — would find it hidden.
- **Certificate-fingerprint pinning (SHA-256 of the whole DER) instead of
  SPKI.** Rejected: it breaks on every certificate renewal even when the key
  pair is unchanged, which for a self-signed internal endpoint means a
  support call per renewal.
- **Pinning the origin through the proxy.** Rejected: a MITM proxy
  re-encrypts, so the origin key never reaches the client and the pin rejects
  the proxy's certificate — a correctly configured setup becomes
  unconnectable. The two mechanisms answer the same question and cannot both
  hold on one transport.
- **Pinning the proxy's own certificate instead.** Rejected: the user pinned
  the provider endpoint, not the proxy; substituting a different host's key
  is surprising, and the proxy already has `proxy.tls_cert_dir`.
- **A per-provider proxy opt-out for pinned providers.** Rejected: routing
  policy is global and operator-owned. `proxy.bypass_list` already expresses
  "this host skips the proxy" without multiplying states.
- **One unified resolver for all dial paths.** Rejected: the paths differ in
  whether a fallback client exists behind them. A single resolver returning
  the proxy client would shadow the router-level client on the inference path
  and cap requests at the 30-second proxy timeout; one returning nil would
  bypass the proxy on the listing and probe paths. See §3.
- **A plain `string` pin at the API boundary.** Rejected: indistinguishable
  from "field not sent", so every debounced partial save would clear the pin.
- **Re-reading the backend config to gate the pin UI on proxy state.**
  Rejected: inside the 800 ms proxy-save debounce the read returns the stale
  value, and it contends with the proxy rebuild (§7). A draft store written
  synchronously by the General tab is both fresher and cheaper.
- **Offering the pin for fixed providers too.** Rejected: `anthropic` and
  `chatgpt` reach vendor endpoints with publicly trusted certificates, where
  a pin can only cause an outage at the vendor's next key rotation.

## Related Specs

- [llm-providers](../domains/llm-providers.md) — the provider domain,
  including the configuration reference for this key
- `SECURITY.md` — the in-transit trust statement
- `config.example.yaml` — the authoritative tunable reference
