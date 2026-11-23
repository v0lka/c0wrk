# Networking

## Purpose

Defines the outbound-network policy every c0wrk HTTP client obeys: proxy routing and bypass semantics, operator-supplied CA trust, per-provider TLS certificate pinning for self-signed LLM endpoints, and HTTPS-only hardening for security-relevant downloads. The domain owns transport composition, not protocol clients — LLM, MCP, search, update and tool-download traffic each build their own client on these rules.

## Key Files

- `core/proxy/proxy.go` — `Config`, `BuildTransport`/`BuildClient`, `BypassMatcher` (the single authority on bypass semantics), `SetEnvVars`/`ClearEnvVars`, and `MaskURL` for safe display
- `core/llmtls/llmtls.go` — per-provider pinning: `DialPolicy`, `Client`/`RouterEntryClient`/`DirectDialClient`, `SPKIFingerprint`, `FetchFingerprint`
- `internal/securehttp/securehttp.go` — the HTTPS-only download policy shared by the update and tool-manager download surfaces (`RequireHTTPS`, `SecureRedirectCheck`, `SecureHTTPClient`, `ErrNonHTTPS`, `MaxRedirects`)
- `core/builder.go` — builds the proxy client once per builder (`proxy.BuildClient` with `timeouts.webFetchProxyTimeout`) and threads it into the router, the built-in tool HTTP client, and the model registry; applies the pin resolvers on every provider dial path
- `core/builder_mcp.go` — hands the proxy client to the MCP gateway (`mcpCfg.HTTPClient`)
- `backend/configadapter.go` — `config.ProxyConfig` → `proxy.Config`, plus the `BuilderConfig.ExpandEnvVars` seam the proxy values and every consumer use
- `core/updater/checker.go`, `core/updater/downloader.go` — proxy-aware release check/download, both hardened with `securehttp`
- `core/toolmanager/download.go` — archive downloads behind `securehttp.SecureHTTPClient`
- `backend/frontend_api_provider_auth.go`, `backend/frontend_api_updater.go` — backend-owned clients (OAuth token endpoints, update check/download) built from the live config

## Core Types

```go
// Proxy settings for every outbound connection (core/proxy.Config).
type Config struct {
    Enabled      bool
    URL          string   // scheme://user:password@host:port (scheme defaults to http)
    BypassList   []string // hostnames; "*.example.com" matches subdomains
    TLSCertDir   string   // directory of operator CA bundles (.pem/.crt)
    SetGlobalEnv bool     // export the proxy into child-process env
}

// The single authority on bypass semantics, shared by the proxy transport and
// the TLS resolvers so the two can never diverge. The zero value matches
// nothing. See specs/decisions/054-per-provider-tls-pinning.md.
type BypassMatcher struct{ /* exact hosts + "*.suffix" entries */ }

// Whether one provider dial goes through the proxy and whether that host
// bypasses it — the gate deciding whether a pin applies.
type DialPolicy struct {
    ProxyActive    bool
    TargetBypassed bool
}
```

## Flow

```
config.yaml (proxy.*, llm.<provider>.tls_fingerprint)
        │
        ├─ configadapter.ToBuilderConfig → proxy.Config (+ ExpandEnvVars seam)
        │        │
        │        ▼
        │   core/builder: proxy.BuildClient(cfg, webFetchProxyTimeout)
        │        ├─ transport: ProxyFunc = bypass ? direct : proxyURL
        │        │              TLSClientConfig.RootCAs = system ∪ TLSCertDir
        │        ├─ RouterConfig.HTTPClient        (LLM inference)
        │        ├─ builtin tools HTTPClient       (web fetch/search)
        │        ├─ ModelRegistry.SetHTTPClient    (model metadata)
        │        └─ mcpCfg.HTTPClient              (MCP transports)
        │
        └─ per provider dial: llmtls.DialPolicy + fingerprint
                 ├─ proxy dials  → no pin (proxy wins)
                 ├─ bypassed host→ direct dial, pin applies
                 └─ no proxy     → direct dial, pin applies

download surfaces (updater, tool manager)
        └─ securehttp.SecureHTTPClient(client): every URL and every redirect
           hop must be https (loopback http excepted), ≤ MaxRedirects hops
```

## Proxy Routing

`proxy.BuildTransport` returns `(nil, nil)` when the section is disabled or the URL is empty, and every consumer treats a nil transport as "dial directly". A configured URL is normalized (a missing scheme becomes `http`), restricted to `http`, `https`, or `socks5`, and required to carry a host. Parse failures surface only the underlying reason, never the raw URL, so a proxy password cannot leak into logs or RPC errors.

Bypass matching is centralized in `BypassMatcher`: entries are trimmed and lowercased; matching is on the hostname without a port; an exact entry matches that host, and a `*.` entry matches every subdomain. The proxy transport's `Proxy` function and the TLS resolvers share this matcher — a bypassed host both dials direct and re-arms its pin.

When `tls_cert_dir` is set, the transport trusts the system pool **plus** every `.pem`/`.crt` file in that directory, with `MinVersion` TLS 1.2. Unreadable or non-PEM files are warned and skipped; a directory with no usable certificate leaves the system pool in force rather than failing the dial.

`set_global_env` (default true when the proxy is enabled) exports the proxy to child processes: `HTTP_PROXY`/`HTTPS_PROXY` (upper- and lowercase) carry the URL with the userinfo **stripped**, `NO_PROXY`/`no_proxy` carry the bypass list when non-empty, and `SSL_CERT_DIR` carries the CA directory. `ClearEnvVars` removes the whole set when the proxy is disabled, so a stopped proxy does not keep routing child processes.

## TLS Pinning

The pin is the switch: an empty `tls_fingerprint` means ordinary system verification, and there is no accept-any state. A set pin accepts only the certificate whose base64 SPKI SHA-256 matches; a mismatch wraps `llmtls.ErrPinMismatch` and carries no certificate material.

Pinning composes with the proxy by the **proxy-wins** rule: while a host's dial goes through the proxy, its pin is not applied (a MITM proxy re-encrypts with its own certificate); a bypassed host dials direct, so its pin applies even while the proxy serves everyone else. The rule gates application only — the pin stays persisted and re-arms when the proxy stops dialing for that host.

Resolver choice is load-bearing (`llm-providers.md` owns the full table): `RouterEntryClient` returns nil for proxied/unpinned entries so the SDK falls back to the router-level client, which carries the request budget; `DirectDialClient` resolves the listing, probe, and direct-dial paths and strips proxy routing from a cloned transport on a bypassed host.

## HTTPS-Only Download Hardening

`internal/securehttp` is one implementation shared by the update and tool-manager surfaces because the invariant is security-critical: SHA256 pins bytes, not authorship, so the transport itself must be end-to-end encrypted.

- `RequireHTTPS` rejects any non-`https` URL, with a single exception for loopback `http` (`localhost`, IPv4/IPv6 loopback), which never traverses a network.
- `SecureRedirectCheck` applies the same rule to **every** redirect hop and re-implements the chain cap (`MaxRedirects` = 10) that installing a custom `CheckRedirect` replaces.
- `SecureHTTPClient` returns a shallow copy with the policy installed — the caller's client is never mutated, because one proxy-aware client may be shared across subsystems that each layer their own policy on a copy.
- Callers must run `RequireHTTPS` on the initial URL themselves: `CheckRedirect` sees hops only.

## Consumers

| Consumer | Client composition | Proxy | TLS pin | HTTPS-only |
| -------- | ------------------ | ----- | ------- | ---------- |
| LLM router / main loop | builder proxy client + per-provider `llmtls` client | yes | yes (proxy-wins) | — |
| MCP gateway | `mcpCfg.HTTPClient = b.proxyClient` | yes | — | — |
| Built-in HTTP tools (web fetch/search) | tools built with `b.proxyClient` | yes | — | — |
| Model registry metadata | `ModelRegistry.SetHTTPClient(b.proxyClient)` | yes | — | — |
| Provider OAuth token endpoints | `proxy.BuildClient(...)` per rebuild | yes | — | — |
| Update check | `NewCheckerWithProxy` → `securehttp.SecureHTTPClient` | yes | — | yes |
| Update download | `proxy.BuildClient` → `NewDownloader` → `securehttp` | yes | — | yes |
| Tool-manager archive downloads | `NewManager` default 5-minute client → `securehttp.SecureHTTPClient` | not routed | — | yes |
| Embedded-LLM artifact downloads | default transfer client | not routed | — | — |

## Invariants

- One bypass matcher decides routing: the proxy transport and the TLS resolvers always agree on whether a host is bypassed.
- A proxy password never leaves the process: display masking keeps the username and substitutes `***`; the environment export strips the userinfo entirely.
- TLS verification is never disabled. Trust is widened only by the operator's CA directory, and narrowed only by an explicit per-provider pin.
- An empty pin always means system verification; there is no configuration that accepts any certificate.
- A pin never applies to a dial that traverses the proxy, and always applies to a direct (unproxied or bypassed) dial.
- Every security-relevant download fetch starts on `https` and stays on `https` across every redirect hop, except loopback.
- `securehttp` never mutates a caller's `*http.Client`; each hardened client is a copy.
- The proxy client is built with `timeouts.webFetchProxyTimeout`, never with the LLM request budget; an entry that needs a budget receives a clone carrying it rather than the raw proxy client.

## Configuration

| Key | Default | Meaning |
| --- | ------- | ------- |
| `proxy.enabled` | `false` | Master switch; with `false` every consumer dials directly and the env export is cleared |
| `proxy.url` | `""` | `scheme://user:password@host:port`; scheme optional (defaults to `http`), schemes `http`/`https`/`socks5` |
| `proxy.bypass_list` | `["localhost", "127.0.0.1"]` | Hostnames that skip the proxy; `*.example.com` matches subdomains (seeded by `ApplyDefaults` when the key is absent) |
| `proxy.tls_cert_dir` | `""` | Directory of `.pem`/`.crt` CA bundles appended to the system pool |
| `proxy.set_global_env` | `true` when enabled (`*bool`) | Export the proxy (credentials stripped) and `SSL_CERT_DIR` to child processes |
| `llm.<provider>.tls_fingerprint` | `""` | Base64 SPKI SHA-256 pin for a compatible provider ([llm-providers.md](llm-providers.md)) |
| `timeouts.webFetchProxyTimeout` | `30` s | Timeout of the shared proxy client (web fetch); deliberately not an inference budget |

Values support `${VAR}` expansion, resolved at consumption through the adapter's `ExpandEnvVars` seam.

## Extension Points

A new outbound consumer joins the policy by composing an existing client instead of building a bare one:

1. Accept an `*http.Client` (or build one from `proxy.Config` via `proxy.BuildClient`) instead of `http.DefaultClient`.
2. For download surfaces, wrap it with `securehttp.SecureHTTPClient` **and** call `RequireHTTPS` on the initial URL.
3. For an LLM-endpoint dial, resolve the client through `llmtls` with the provider's `DialPolicy` and pin rather than attaching a raw client to the entry.
4. Never mutate a shared client; clone it and layer the policy.
5. Add the consumer to the table above and cross-reference it from the owning domain.

## Related Specs

- [llm-providers.md](llm-providers.md) — per-provider TLS pinning semantics, the resolver table, and the request-budget interaction that makes a raw proxy client unsafe on an inference entry
- [configuration.md](configuration.md) — the load pipeline and save discipline behind `proxy.*`
- [tool-manager.md](tool-manager.md) — the download surface hardened here
- [updates.md](updates.md) — the update check/download surface hardened here
- [web-access.md](web-access.md) — the inbound-facing sidecar, which is a separate trust boundary
- [../decisions/054-per-provider-tls-pinning.md](../decisions/054-per-provider-tls-pinning.md) — the pinning decision, including the proxy-wins rule
- [../contracts/core-sp4rk.md](../contracts/core-sp4rk.md) — the exported `core/proxy` interface table
- [../architecture/security-model.md](../architecture/security-model.md) — the threat model this policy serves
