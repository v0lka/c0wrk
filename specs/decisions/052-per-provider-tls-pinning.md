# ADR-052: Per-Provider TLS Verification Override via SPKI Pinning

## Status

Accepted — amended by [ADR-053](./053-proxy-wins-over-tls-pin.md), which
settles the pin's interaction with the configured HTTP proxy (proxy wins;
the pin applies only on direct connections). All other decisions remain in
force.

## Context

Users run OpenAI- and Anthropic-compatible LLM servers inside private
networks (homelab vLLM/LM Studio boxes, corporate gateways, Tailscale
endpoints) whose TLS certificates are self-signed or issued by an internal
CA. Go's default verification rejects them:

```
tls: failed to verify certificate: x509: certificate signed by unknown authority
```

Until now the only workarounds were global and blunt:

- `SSL_CERT_FILE` / system CA store — machine-global, affects every Go
  program and child process, requires shell-level setup, and cannot be
  scoped to one provider in `~/.c0wrk/config.yaml`.
- The proxy section's `tls_cert_dir` — only takes effect when an HTTP proxy
  is *enabled*, so it is useless for direct connections.

Neither is per-provider, and neither is reachable from the Settings UI.

A structural constraint shaped the design: the sp4rk router built ONE
`http.Client` for ALL providers (`RouterConfig.HTTPClient` threaded into
every `createProviderFromConfig` call), so a per-provider TLS policy could
not be expressed at all without an SDK change. Beyond the chat path, three
more call sites dial the provider endpoint and each would fail
independently: the settings "Fetch Models" listing
(`listOpenAIModels` — which built its own SDK client, ignoring even the
proxy), the lazy context-window probe (`lookupOpenAIProviderBaseURL` →
`probeSelfHostedContextWindow`), and the draft-provider path that fetches
models before the provider is first saved
(`applyListProviderModelsOverrides`).

### Forces

- **The user's server, the user's call.** A self-signed endpoint on a LAN
  the user controls is the canonical opt-out case; every comparable tool
  (`curl -k`, `kubectl --insecure-skip-tls-verify`) ships an escape hatch.
- **An accept-any bypass is a MITM window.** Disabling verification
  entirely exposes the API key and conversation traffic to any on-path
  attacker — acceptable for a lab, poor as a *supported steady state*.
  The override must therefore never express "trust any certificate".
- **Pinning by public key survives certificate renewal.** Servers rotate
  leaf certificates far more often than they rotate keys; an SPKI pin
  (as in Chromium's CertificatePinList and RFC 7469) outlives cert renewal
  while still rejecting any substituted key.
- **TLS policy belongs in c0wrk, not sp4rk.** The SDK is a transport; the
  decision *whether* to trust an endpoint is application (and user) policy.

## Decision

1. **Per-provider `HTTPClient` in sp4rk.** `llm.ProviderEntry` gains an
   optional `HTTPClient *http.Client`; the router prefers it per entry and
   falls back to `RouterConfig.HTTPClient`. The SDK stays a dumb transport —
   it never learns about TLS policy.

2. **One config key on each compatible provider** (both
   `llm.openai_compatible.<name>` and `llm.anthropic_compatible.<name>`):

   ```yaml
   tls_fingerprint: "base64(SHA-256(SPKI DER))"
   ```

   **The pin is the only switch** — exactly two states exist:

   | `tls_fingerprint` | connection                                        |
   | ----------------- | ------------------------------------------------- |
   | empty             | normal system CA verification                     |
   | non-empty         | ONLY the pinned SPKI; mismatch = bare error       |

   A non-empty pin **by itself** activates the override. There is no
   separate toggle and no configured state that accepts ANY certificate:
   "accept any" exists only inside the Get-fingerprint handshake, which
   exists precisely to produce a pin and sends no credentials. One trust
   decision, one knob — no 2×2 state machine where the dangerous corner is
   reachable by leaving a second field empty.

3. **`core/llmtls` owns the mechanics.** `llmtls.Client(base, pin)` clones
   (never mutates) the shared client and wraps its transport with
   `tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: …}`
   comparing `base64(SHA-256(RawSubjectPublicKeyInfo))` (whitespace-
   tolerant). A wrong pin fails with a bare `ErrPinMismatch` — the error
   deliberately does NOT echo the server's actual fingerprint, so a MITM
   cannot social-engineer the user into pinning the attacker's key via the
   chat error text. An empty pin returns the base client unchanged.
   `llmtls.FetchFingerprint` performs a handshake-only `tls.Dial` (no HTTP
   request, no API key) to power the Settings UI.

4. **All four dial paths wrap consistently** (`core/builder.go`):
   `providerEntryFromConfig` attaches the pinned client to router entries;
   `fetchProviderModels` threads it into `listOpenAIModels` (whose
   signature gained an `httpClient` parameter — as a side effect the model
   listing now honors the proxy client it previously ignored) and
   `listAnthropicModels`; the lazy probe resolves the provider's TLS fields
   through the extended `lookupOpenAIProviderBaseURL`; the unsaved-draft
   path (`applyListProviderModelsOverrides`) sends the draft pin verbatim.

5. **Settings UI.** Each compatible provider's form gains a "Custom TLS
   fingerprint" checkbox — its checked state is derived from the persisted
   pin — with a fingerprint input and a **Get** button underneath. Get
   calls the new `GetProviderTLSCertificate` RPC, which handshakes to the
   (draft or persisted) base URL and returns the pin the server currently
   presents — the user pins what the server actually serves, no openssl
   archaeology. Unchecking emits an explicit empty pin, which clears the
   override on save (back to system verification). Fetch Models sends the
   draft pin so a first-run unsaved provider lists models over the
   self-signed connection too.

6. **Debounce-safe round-trip.** `UpdateLLMConfig` rebuilds provider maps
   from every request; a field it does not carry is silently lost. The pin
   therefore travels as a pointer in `ProviderConfigRequest`
   (`tls_fingerprint *string`): nil = keep the persisted value (partial
   debounced saves cannot drop the pin), non-nil = apply verbatim (an
   explicit empty string clears it). The read side (`ConfigProviderFull`)
   returns plain values. The nil-vs-`""` sentinel exists only at this API
   boundary — a transport concern; below it everything stays plain and
   non-pointer (`config.OpenAICompatibleConfig.TLSFingerprint string`,
   `BuilderProviderConfig.TLSFingerprint string`), where the empty string
   is a *data value* meaning "no override", not a tri-state.

## Consequences

- A self-signed provider connects after one Get-button press and one save;
  the pinned connection keeps MITM resistance comparable to normal TLS
  (the pin substitutes for the CA chain on that connection).
- **The accept-any-certificate data path is structurally closed.** No
  configuration, UI action, or wire payload can produce a connection that
  skips verification without a pin. The only deliberate
  `InsecureSkipVerify` is the Get-fingerprint handshake, which exchanges
  no credentials — it reads the certificate the user is about to trust.
- Users who genuinely want `curl -k` semantics have no supported path: the
  cost is one Get-button press per server (and per server key rotation) —
  a deliberate, visible trust re-decision.
- The pin survives certificate renewal with the same key; rotating the
  server key requires re-fetching the pin (Get button). The mismatch error
  names no key material — diagnosing a rotation means pressing Get in
  Settings, which is the intended workflow (RFC 7469 social-engineering
  posture).
- sp4rk carries one additive, backward-compatible field; existing
  `ProviderEntry` users (zero-value field) are byte-for-byte unaffected.

## Rejected Alternatives

- **Global `SSL_CERT_FILE` guidance only** — machine-global, no UI, not
  per-provider, and undocumented for app-store launches. Kept as a footnote
  workaround, not the feature.
- **Reuse the proxy `tls_cert_dir` CA-pool machinery per provider** — right
  primitive, wrong ergonomics for the stated problem ("my server has a
  self-signed cert"): the user must fetch the PEM off the server and manage
  a directory. The fingerprint button does that in one click. A CA-file
  option remains possible later on top of the same `llmtls` plumbing.
- **An accept-any-certificate bypass (`curl -k` style) as the override** —
  the escape hatch every comparable tool ships is also a first-class MITM
  window: any on-path attacker interposes and receives the API key and the
  full conversation traffic. A recommendation to pin is advisory; the
  config surface must not offer a no-questions-asked bypass as a steady
  state. Pinning gives the same "my server, my call" autonomy without the
  window.
- **`InsecureSkipVerify` flag inside sp4rk's `ProviderEntry`** — leaks TLS
  policy into the SDK and cannot express pinning. The per-entry client is
  strictly more general.
- **Pin layered on top of normal CA verification (double verification)** —
  surprising failure mode ("valid cert rejected"): the pin would reject
  CA-valid certificates from a key the user never pinned. The pin must be
  the override it replaces, not a second interacting layer.
- **`*string` for `tls_fingerprint` in the persisted config schema** — a
  tri-state at rest. The nil-vs-`""` distinction exists only for the
  debounce round-trip (a transport concern); config loading reads whole
  files and has no partial-save concept, so no consumer below the API
  boundary can ever set the third state, while every reader pays nil-check
  tax. The sentinel stays where the partial saves happen — the API
  boundary — and the persisted schema keeps plain values.
- **Auto-fetch a pin at config load** — a network side effect at load,
  trust-on-first-use without consent, and the wrong layer (load must stay
  pure). The Get button is the consentful equivalent.
- **Echoing the actual fingerprint on mismatch** — convenient, but it is
  exactly the social-engineering vector RFC 7469 warns about; the Get
  button provides the legitimate discovery path instead.
