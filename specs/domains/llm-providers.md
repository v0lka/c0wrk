# LLM Providers

## Purpose

c0wrk does not implement LLM provider abstractions — `Provider`, `Router`, `ModelRegistry`, `TokenCounter`, and retry/backoff are **sp4rk engine** primitives. This spec documents only how c0wrk wires provider configuration into a sp4rk `Router`. The canonical provider/router/model-registry behavior is in [the sp4rk llm-providers spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md) and [the sp4rk llm-providers contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/llm-providers.md).

## Key Files

- `backend/configadapter.go` — `ToBuilderConfig(cfg)` builds `ProviderConfigs` from all known providers via `GetAllProviderConfigs()` (including providers with no models enabled — the router filters to enabled providers downstream in `buildRouter`); sets `DefaultModel` (cross-provider, resolves to owning provider). Single conversion point for all config mapping.
- `backend/config/config.go` — `LLMConfig` provider schema plus the resolution helpers (`allProviderEntries`, `ResolveModelID`, `ResolveDefaultModelProvider`, `GetAllProviderConfigs`); also owns `EmbeddedLLMConfig` and `SyncEmbeddedProvider`, which generate/remove the backend-owned `openai_compatible.embedded` record (see [Backend-Owned Embedded Provider](#backend-owned-embedded-provider))
- `backend/frontend_api_config.go` — `UpdateLLMConfig` applies the UI's provider draft and re-injects the backend-owned `embedded` record before validating the candidate; `GetConfig` reports providers network-free
- `core/builder.go` — `NewOrchestratorBuilder` creates a `github.com/v0lka/sp4rk/llm.Router` with providers (async, in `runAsyncInit()`); passes a `github.com/v0lka/sp4rk/llm.TokenCounter` to the engine
- `core/lmstudio_probe.go` — `probeLMStudioModels` queries the LM Studio-native `GET {base}/api/v0/models` endpoint and returns a per-model context-window map (runtime value when loaded, capacity otherwise); `probeOpenAIModels` is the standard `GET {base}/v1/models` fallback (honors `max_model_len` / `max_context_length` / `context_length`); `probeSelfHostedContextWindow` runs the native leg first, then the OpenAI fallback
- `core/builder.go` — `buildLocalModelProbe` constructs the per-session lazy probe closure (a `LocalModelProbe`); `lookupOpenAIProviderBaseURL` restricts probing to OpenAI-compatible providers
- `core/embeddedllm/transport.go` — `EnsureLoadedTransport` / `NewEnsureLoadedTransport` / `EnsureLoadedClient` / `Loader` / `PortSource`: the ensure-loaded `http.RoundTripper` installed on the embedded provider entry through the same `ProviderEntry.HTTPClient` hook, and the reason that hook's client carries no `Timeout` of its own — the budget moves into the transport so the cold-load wait is not charged to the request. It also redirects each request to the supervisor's live loopback port, because the entry's `base_url` is derived from the PERSISTED port and a load may have moved off it (see [Backend-Owned Embedded Provider](#backend-owned-embedded-provider))
- `frontend/src/lib/llm-providers.ts` — `EMBEDDED_PROVIDER_NAME` / `isBackendOwnedProvider`: the frontend's copy of the backend-owned provider key, used to keep it out of the settings draft and out of the compatible-provider accordions
- `core/builderconfig.go` — `BuilderEmbeddedLLMConfig` (`ProviderName` + `Loader` + `LoadWaitTimeout`): how the backend injects the embedded supervisor into the router build
- `core/orchestrator.go` — holds the `Router` (as `modelSwitcher`) for runtime model switching; wraps the caller in `github.com/v0lka/sp4rk/llm.TrackingCaller` for usage tracking

- `backend/application.go` — `buildAutoRetryIntervals` / `publishAutoRetryIntervals` publish an immutable provider→interval snapshot behind an `atomic.Pointer`; wired into the session manager via `SetAutoRetryResolver` (see Automatic Resend below)
- `backend/session/manager_auto_retry.go` — the backend half of the automatic resend ([ADR-065](../decisions/065-auto-resend-retryable-errors.md)): `isAutoRetryableCause` (the backend-local retryable class: `ErrKind` rate_limit/overloaded, statuses 429/529) and `maybeAutoRetryAt` (classify + resolve interval + stamp the deadline). NO backend timer exists — the UI owns the countdown
- `frontend/src/components/chat/useAutoRetryCountdown.ts` — the UI half: the live-only 1s countdown, the one-shot `resumeTask` fire on zero, the optimistic `stop()` on any manual click, and the failure fallback (strip live keys → plain manual banner)
- `backend/config/config.go` — `OpenAICompatibleConfig` / `AnthropicCompatibleConfig` carry `AutoRetrySeconds` (`auto_retry_seconds`, `omitempty`); `GetAllProviderConfigs` surfaces it per provider entry (fixed providers always 0)
- `backend/frontend_api_config.go` — `resolveAutoRetrySeconds` applies the request/response pointer sentinel for the interval (nil = keep persisted, non-nil — including an explicit 0 — applies verbatim)
Engine files (`github.com/v0lka/sp4rk/llm/router.go`, `modelregistry.go`, `provider_openai.go`, `provider_anthropic.go`, `provider_openai_responses.go`, `tokencount.go`, `message.go`) are documented in [the sp4rk llm-providers spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md).

## Wiring Flow

```
~/.c0wrk/config.yaml (providers + models)
         │
         ▼
backend/configadapter.go: ToBuilderConfig(cfg)
  → ProviderConfigs map (provider → {models, api_key, base_url, ...})
  → DefaultModel (composite "provider/model" ID)
         │
         ▼
core/builder.go: NewOrchestratorBuilder
  ├─ ModelRegistry (5-tier Resolve, with config overrides)
  └─ Router (async): multi-provider routing, composite IDs, retry/backoff,
                    context-window validation
         │
         ▼
per-session Orchestrator
  ├─ Router.Call → satisfies agent.LLMCaller
  ├─ Router.SetModel / ActiveModel — runtime model switch (routing, per-delegation override)
  └─ TrackingCaller wraps usage tracking → persisted via emitter callback
```

## Model Override (c0wrk consumption)

- **Per-task**: `HandleOptions.ModelOverride` (from the frontend model selector) switches the Conductor's model via `Router.SetModel`. The switch also re-binds the session's strict tool judge to the new provider/model (`ApplyRequestOverrides` invokes the `JudgeSync` closure wired by `Build`; see [ADR-028](../decisions/028-session-pinned-judge.md)) — the judge always evaluates on the provider/model the session itself runs on, and a global default-model change elsewhere never re-binds a live session's judge.
- **Per-delegation**: a targeted **Subagent Profile's** `model` frontmatter field (`.agents/agents/<name>/AGENT.md`, [ADR-021](../decisions/021-subagents.md)) overrides the model for a subagent, applied via `agent.NewModelOverrideCaller` during the subagent build (empty = Conductor's active model).
- **Model Profiles sampling override**: when the active model profile's sampling variant is on (master `model_profiles.enabled` AND the profile's `sampling.enabled` — see [model-profiles.md](model-profiles.md)), `core/builder.go` `resolveSamplingFunc` layers the explicitly set Model Profiles sampling values (temperature, top_p, top_k, repetition_penalty, presence_penalty; zero = unset) on top of the per-family `prompt.DefaultSampling` preset for the router's `SamplingFunc` — unset parameters inherit the vendor preset. Sampling the router would inject reaches the wire only when the resolved model capabilities **authoritatively** accept the temperature parameter: catalog-declared no-temperature models (o-series, adaptive-thinking Claude, thinking-locked Kimi) get every sampling field stripped — explicitly-set values included, since the endpoint rejects them with a hard 4xx — while models whose capabilities are the SDK registry's guess (`GuessedCapabilities`, i.e. every model unknown to the catalog and to the user's config: LM Studio, Ollama, vLLM, config entries without a `capabilities:` block) mirror nil capabilities: nothing is injected and explicitly-set request fields are preserved, so the host stays in control of its local models. The builder-level reasoning-effort default is also seeded from the profile (`applyModelProfilesPresets`); per-request overrides (`HandleOptions.ReasoningEffort`) still take precedence. When the variant is off, the router uses the per-family default unchanged.
- Composite model IDs are `"provider/model"` (e.g. `openai/gpt-4o`, `anthropic/claude-3-7-sonnet`).

## Context Window Resolution

The ModelRegistry's context window for a model is resolved with a strict priority order (first match wins):

1. **config.yaml override** — `llm.models.<name>.context_window` (mapped into `BuilderConfig.LLM.Models`). Always wins; the probe never overwrites a model already present in overrides. The embedded local model's override is written by the backend from the resolved RAM tier, not by hand (see [Backend-Owned Embedded Provider](#backend-owned-embedded-provider)).
2. **Lazy local-model probe** — `core/builder.go` `buildLocalModelProbe` constructs a per-session probe closure (`LocalModelProbe`) that, for a given model served by an OpenAI-compatible provider (`ProviderType "openai"`), locates the provider (`lookupOpenAIProviderBaseURL`) and fires an asynchronous `probeSelfHostedContextWindow`. The probe runs once for the session's default model at orchestrator construction and on each model switch (the closure is wired into `OrchestratorDeps.LocalModelProbe`); it writes the discovered window to the registry via `SetRuntimeMetadata` (Resolution tier 1.5 — above the built-in catalog and the lazy cache, below a config.yaml override), so the server's observed runtime window beats the catalog spec and only a config override (tier 1) shadows it. `probeSelfHostedContextWindow` tries the LM Studio-native endpoint `GET {base}/api/v0/models` first — reading the **runtime** window when the model is loaded (top-level `loaded_context_length`, or `loaded_instances[].config.context_length` on older versions), otherwise the advertised **capacity** (`max_context_length`) — then falls back to the standard `GET {base}/v1/models` listing, which self-hosted servers (vLLM/TGI/Ollama) extend with `max_model_len` / `max_context_length` / `context_length` (first non-zero wins). This lets token budgets reflect what LM Studio is actually running (e.g. a model loaded at 16384 instead of its 262144 spec).
3. **Static default** — the sp4rk SDK fallback (`ContextWindow` 128000, `OutputLimit` 32768) when neither config nor the probe provides a value.

The probe is best-effort and non-fatal: only OpenAI-compatible providers are queried (anthropic has no `base_url`). A genuine cloud provider (real OpenAI) whose `/v1/models` listing omits any window field is a silent no-op — its behavior is unchanged — while self-hosted servers (vLLM/TGI/Ollama) supply a window via the OpenAI `/v1/models` fallback. Network/timeout/parse failures and **5xx server errors** (a momentarily-unwell LM Studio) are surfaced as errors and logged at Warn; client errors `< 500` (404, 401, 403) remain a silent no-op. The registry still builds normally regardless.

**Latency & safety.** The probe fires on an internal goroutine with a 3-second per-leg timeout and a detached context, so it never blocks session creation or a model switch even when an LM Studio base URL is unreachable. The discovered metadata written to the runtime tier (tier 1.5) sets `OutputLimit` to `min(32768, window/4)` — mirroring the sp4rk SDK's built-in fallback of 32768 so self-hosted models are not regressed (neither LM Studio nor vLLM expose a per-model output cap), but clamped to at most a quarter of the discovered window so a small-context model cannot disable compaction (an `OutputLimit` larger than the context window drives `EffectiveMax` negative) — and `TokenizerType` to `approximate`.

**Settings-facing resolution.** UI paths that must never block resolve model metadata through the registry's network-free `ModelRegistry.ResolveLocal` (sp4rk): `GetConfig`'s `AllModels` enrichment (`backend/frontend_api_config.go` `collectAllModels`) serves overrides, built-ins, fuzzy matches, and cached entries (including LM Studio probe results written via `SetRuntimeMetadata` to the runtime tier 1.5, which `ResolveLocal` also serves) purely from memory, returning fallback defaults for unknown models. Runtime resolution keeps the full `Resolve` path (network tiers, guarded by a negative cache that suppresses repeat failed probes). The network-free `GetConfig` invariant is specified in [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md).

## Output-Token Reserve

The output-token budget for a model resolves through the same tiering as the context window (first match wins):

1. **Per-model override** — `llm.models.<name>.output_limit` (user config tier).
2. **Per-provider override** — `llm.<provider>.output_token_reserve` (`anthropic`, `chatgpt`, `openai_compatible.<name>`, `anthropic_compatible.<name>`), applied at router construction by `core/builder.go` `applyProviderOutputReserves`: it seeds `ModelMetadata.OutputLimit` into the registry overrides for every model the provider lists. An explicit per-model `output_limit` is never clobbered, and because the seeded value lands in the overrides tier it also shadows the runtime probe cache — an operator-level statement that the gateway's real budget differs from the catalog.
3. **Global** — `executor.output_token_reserve` (default **8192**; modern coding/reasoning models regularly emit multi-thousand-token tool-call replies), carried into `llm.RouterConfig.OutputTokenReserve` as the fallback for models whose metadata carries no `OutputLimit`. The Model Profiles context variant raises this global fallback to 16384 when enabled (see [model-profiles.md](model-profiles.md)); the generation ceiling itself is always set by the per-model/per-provider tiers above.
4. **Discovered/static** — the probe cache (`min(32768, window/4)`) and the sp4rk SDK static fallback (32768).

The budget plays two roles: it is subtracted from the context window during overflow validation, and it caps the executor's per-request `MaxTokens` (the agent loop reads the model's `ContextWindow.OutputLimit()`), so a single provider-level knob adjusts both the validation reserve and the generation ceiling — the right granularity for self-hosted gateways (LM Studio, vLLM) whose effective limits differ from the built-in catalog.

## Per-Provider TLS Verification Override

Compatible providers (`openai_compatible.<name>`, `anthropic_compatible.<name>`) may replace system CA verification with an SPKI pin, so a self-signed or internal-PKI endpoint is reachable without giving up peer authentication ([ADR-054](../decisions/054-per-provider-tls-pinning.md)):

```yaml
llm:
  openai_compatible:
    selfhosted:
      base_url: "https://llm.lan:8443/v1"
      tls_fingerprint: "k3J9vQ1Z…base64(SHA-256(SPKI DER))…"
```

**The pin is the only switch** — exactly two states exist:

| `tls_fingerprint` | connection                                 |
| ----------------- | ------------------------------------------ |
| empty / absent    | normal system CA verification; no override |
| non-empty         | ONLY the pinned SPKI; mismatch = bare error |

A non-empty pin activates the override by itself; there is no separate toggle and **no configured state that accepts an arbitrary certificate**. The only deliberate unverified handshake is the Get-fingerprint probe, which exchanges no credentials. Fixed providers (`anthropic`, `chatgpt`) carry no such key: they reach vendor endpoints with publicly trusted certificates.

The value is `base64(SHA-256(SubjectPublicKeyInfo DER))` — Chromium CertificatePinList / RFC 7469 style — so a pin survives certificate renewal that reuses the key pair. Comparison is whitespace-tolerant; a pin that is not base64 of 32 bytes is logged as a Warn and still fails closed at the handshake.

### Proxy wins

When an effective proxy is configured — `proxy.enabled` AND a non-empty `proxy.url`, the `proxy.BuildTransport` rule — the pin is **ignored on proxied dials** (a MITM proxy re-encrypts with its own certificate, so the origin key never reaches the client and a layered pin would reject a correctly configured setup; the proxy carries its own trust mechanism, `proxy.tls_cert_dir`). The exception is `proxy.bypass_list`: a bypassed host dials directly, so its pin applies even while the proxy serves everyone else — implemented in every resolver (`llmtls.DialPolicy.TargetBypassed`), not just documented. The rule gates the pin's *application*, never its configuration — a pin stays persisted while the proxy dials and re-arms on its own when the proxy stops dialing for that host.

### Mechanics

`core/llmtls` holds the TLS policy; sp4rk only transports the client it is handed (`llm.ProviderEntry.HTTPClient`). `Client` clones the base HTTP client and attaches a `VerifyPeerCertificate` that compares the leaf's SPKI hash; the pinned `*http.Transport` is derived ONCE at construction, so the derived client keeps its idle-connection pool across requests, and it stays a concrete `*http.Transport` so `CloseIdleConnections` reaches that pool. A base transport that is not an `*http.Transport` cannot hold a `tls.Config` and is replaced by a default-transport clone with a Warn. Mismatch errors carry no key material by design.

Two resolvers encode the proxy-wins rule, because the dial paths differ in whether a fallback client exists behind them:

| resolver | used by | proxy active | no pin | pin set |
| --- | --- | --- | --- | --- |
| `RouterEntryClient` | chat / inference (`ProviderEntry.HTTPClient`) | `nil` | `nil` | pinned clone of the shared LLM client |
| `DirectDialClient` | Fetch Models, lazy probe | the proxy client verbatim | `nil` | fresh pinned client |

`RouterEntryClient` returning nil is load-bearing: the SDK then falls back to `RouterConfig.HTTPClient`, which already carries the proxy transport **and** `timeouts.llmRequestTimeout`. Attaching the raw proxy client to the entry would shadow it and cap inference at `timeouts.webFetchProxyTimeout` (30 s). When a pin does apply, the client is cloned from the shared LLM client so the long timeout is inherited.

`core/builder.go` applies the rule on every path that opens a provider connection: router entries (`providerEntryFromConfig`), the Fetch Models listing (`fetchProviderModels` → `listOpenAIModels` / `listAnthropicModels`, including the unsaved-draft path `applyListProviderModelsOverrides`), and the lazy context-window probe (`lookupOpenAIProviderBaseURL` → `buildLocalModelProbe`). `proxyActive` is `b.proxyClient != nil`, which is exactly `enabled && url != ""`. `ModelRegistry.SetHTTPClient` is out of scope — it fetches HuggingFace metadata, not provider endpoints.

`providerEntryFromConfig` is the one place where a SECOND resolver shares this hook: for the embedded provider entry the pin resolver's answer is decorated by `embeddedllm.EnsureLoadedClient` (see [Backend-Owned Embedded Provider](#backend-owned-embedded-provider)). The order is fixed — pin first, ensure-loaded second — because `llmtls` needs a concrete `*http.Transport` to hold a `tls.Config`, so wrapping first would make it discard the wrapper for a default-transport clone. Both resolvers clone from the shared LLM client, so whichever applies, `timeouts.llmRequestTimeout` survives.

### Settings UI and the Get button

The provider form shows the fingerprint field and a **Get** button for every compatible provider, always — the pin is the switch, so an empty field already means standard verification and there is no toggle to tick. `GetProviderTLSCertificate` performs only the TLS handshake (no HTTP request, no API key; `${VAR}` base URLs are expanded like every other dial path) and returns what the server presents right now. It is **unconditional with respect to any configured pin**: the request carries no fingerprint, nothing reads the persisted one, and the result overwrites the field whether the provider was unpinned, correctly pinned, or mismatched. The RPC rejects with an actionable error while an effective proxy is configured, because the direct-dial probe's pin would be inert once saved; the form disables the field, the button and their help text in the same situation, while keeping the persisted pin visible.

The UI gate reads the draft store `frontend/src/stores/proxyDraftStore.ts`, which the General tab writes synchronously on every proxy edit — ahead of its own 800 ms debounce — so an already-mounted LLM tab reacts with no config re-read. The LLM tab only *seeds* that store from its own `getConfig`, a no-op once a value is known.

Fetch Models sends the draft pin verbatim (an explicit draft `""` wins over the persisted value), and the pin is deliberately NOT part of `useModelFetch`'s `credentialKey`, so typing a fingerprint does not discard an already-fetched model list.

### Automatic Resend (UI-owned countdown)

sp4rk's router already retries **inside** a single LLM call (backoff against `*llm.Error`), but when the whole request loop dies — rate-limited to the point the task fails — the session ends in the `task_failed_resumable` state and the user must click Resume manually. For self-hosted gateways (LM Studio, vLLM, a proxy) that sit behind aggressive rate limits, c0wrk adds an **automatic re-send of the failed task** ([ADR-065](../decisions/065-auto-resend-retryable-errors.md)). Ownership is split deliberately: the **backend only classifies the terminal failure and stamps a deadline into the banner payload** (`backend/session/manager_auto_retry.go`, no timers, no state); the **UI owns the countdown and performs the resume** (`frontend/src/components/chat/useAutoRetryCountdown.ts`, firing the ordinary guarded `resumeTask` RPC on zero). The knob is never mapped into `ToBuilderConfig` — the engine's in-flight retry behavior is untouched.

```yaml
llm:
  openai_compatible:
    selfhosted:
      base_url: "http://localhost:1234/v1"
      auto_retry_seconds: 30   # 0 / omitted = disabled (default)
```

**The key exists ONLY on compatible providers** (`openai_compatible.<name>`, `anthropic_compatible.<name>`). The fixed providers (`anthropic`, `chatgpt`) have no `auto_retry_seconds` field: vendor endpoints are not the user's own, and their 429 semantics are already handled by the engine's in-flight retry + backoff. A fixed provider always resolves to 0 — no deadline, only the manual resume banner.

**No bool, 0 = off.** The interval is a plain int; there is no separate enable switch. `auto_retry_seconds: 0` (or omitted — `omitempty`) means "never auto-resend; surface the failure and let the user decide". Because the frontend edit path persists the key only when a value was ever set, a provider that never had an interval simply omits the key.

**Resolver.** `backend/application.go` wires `Manager.SetAutoRetryResolver` to the immutable provider→interval **snapshot** published on `Application` behind an `atomic.Pointer` (`publishAutoRetryIntervals`): the session manager invokes the resolver without `configMu`, and Settings saves replace the live config maps under that lock — an unsynchronized map read there would be a fatal concurrent-map-access crash. The snapshot is seeded at construction and republished by `UpdateLLMConfig` from every committed candidate (a rejected or rolled-back update leaves the previous snapshot), so an interval change applies to the NEXT failure that surfaces a deadline (no restart, no rebuild). The provider name is the logical config key carried by the classified `*llm.Error.Provider` (sp4rk `llm/errors.go`); unknown names and a missing snapshot resolve to 0.

**Retryable class.** A failure surfaces a deadline only when the terminal `*llm.Error` qualifies under `isAutoRetryableCause` (`backend/session/manager_auto_retry.go`): `ErrKind == rate_limit` OR `ErrKind == overloaded` — or the equivalent HTTP statuses 429/529 on status-carrying transports (the anthropic_compatible SDK parses the JSON error body into `APIError`, which carries no status; sp4rk derives ErrKind from the provider's own `rate_limit_error`/`overloaded_error` type fields). The class is a backend-local, unexported constant; it deliberately does NOT inherit the engine's broader in-flight `Retryable` set (500/502/503/504 are left to the engine's own backoff — a whole-task auto-resend is for the two classes a wait-and-retry actually cures). Network errors (`StatusCode 0`) and non-matching statuses never surface a deadline.

**Countdown lifecycle (UI).** The live `task_failed_resumable` handler (`useActionEvents`) copies the deadline into the banner message metadata together with `auto_retry_live: true` — the ONLY discriminator a countdown may run on (the persister stores the raw payload, so restored rows never carry the flag). `useAutoRetryCountdown` arms a 1s ticker only for a live-marked, future-at-mount deadline; the Resume button renders `Resume (Ns)`. On zero it fires `resumeTask(sessionId)` exactly once (one-shot guard) and the button yields to a disabled `Auto-resend…` until `task_resumed` resolves the banner. **Any manual click — Resume or Cancel — stops the countdown optimistically** (`stop()`, before the RPC round-trip): the manual flow proceeds alone and the auto fire never happens. If the auto fire's resumeTask rejects (busy/archived/refused), the hook strips the live keys from the banner metadata → plain manual banner; a REJECTED MANUAL RESUME degrades identically — its revert-to-original-metadata strips the live keys too, because the optimistic `resumed` marking unmounted the banner (destroying the instance-scoped one-shot disarm), so a raw revert would remount the countdown and fire at the unchanged deadline. A manual compaction (`compaction_started`) also strips the live keys (`stripAutoRetryFromBanner` in `useContextEvents`) — an auto fire inside the compaction window would only hit its guard. A restored banner (app restart, session switch) always renders the plain manual banner: after a restart there is no watcher — the deadline served the open session. Auto-resends are **unlimited** while the banner stays mounted (a task that keeps failing on 429/529 re-surfaces a fresh deadline after every failure; the stop is the interval, a manual click, or closing the view).

**Settings UI.** The provider form (`frontend/src/components/settings/ProviderConfigForm.tsx`) renders the "Auto-retry interval" field behind the SAME gate as Base URL (`showBaseUrl` — compatible providers only), so the fixed Anthropic/ChatGPT forms never show it. It is an `EditableCombobox` with presets `0, 5, 10, 30, 60, 120, 300` seconds, filtered at render time against the SERVER-published inclusive bound `llm.auto_retry_max_seconds` (`AUTO_RETRY_PRESETS.filter(p => p <= autoRetryMaxSeconds)` — the bound the form's `max` clamps typed input to, so the dropdown can never offer a value the save RPC would reject) and clamped to `0–<bound>`; the caption reads "0 = auto-resend off (retry only inside the engine)". The bound is REQUIRED — there is no compiled-in fallback: frontend and backend ship in one Wails binary, so a payload without a positive `auto_retry_max_seconds` fails the LLM-settings load loudly (logged error, provider forms stay gated behind a retry notice) instead of silently clamping against a stale constant. A manual `0` is saved as an explicit `0` (disables), while an untouched provider omits the key from the save payload entirely — the backend pointer sentinel (`ProviderConfigRequest.AutoRetrySeconds *int`: nil = keep persisted, non-nil applies verbatim) keeps debounce-safe partial saves from silently resetting the interval. The save path (`useLLMConfigSave`) attaches the key only to compatible entries.

## Invariants

- `auto_retry_seconds` exists only on compatible provider configs; the fixed providers have no such key, and their resolver answer is always 0 (no deadline, manual resume only). The knob never reaches the router/SDK layer — `ToBuilderConfig` does not map it.
- There is NO backend timer: the backend's entire contribution is a pure classification + deadline stamp in the event payload (`maybeAutoRetryAt`). The countdown, the stop-on-manual-click, and the resume fire live in the UI (`useAutoRetryCountdown`), exclusively behind the `auto_retry_live` in-memory flag — restored banners never count down.
- The retryable class is a backend-local constant (rate_limit / overloaded, statuses 429/529); no config or frontend surface can widen it today.
- No lock is held across a network call on any of these paths. `GetProviderTLSCertificate` snapshots proxy state and the base URL under `configMu.RLock` and releases it before the handshake; `fetchProviderModels` snapshots `b.proxyClient` under `b.mu.RLock` once per call.
- The debounce-safe round-trip keeps its pointer sentinel at the API boundary only (`ProviderConfigRequest.TLSFingerprint *string` / `ListProviderModelsRequest.TLSFingerprint *string`: nil = keep the persisted pin, non-nil `""` = clear it; `ProviderConfigRequest.AutoRetrySeconds *int`: nil = keep the persisted interval). Persisted config and the builder layer carry plain values.
- A pinned inference client always carries `timeouts.llmRequestTimeout`, never the proxy timeout.
- So does the embedded entry's ensure-loaded client: `embeddedllm.EnsureLoadedClient` clones the pinned client when the pin resolver produced one and the shared LLM client otherwise, and it never takes the raw proxy client. A client attached to `ProviderEntry.HTTPClient` shadows `RouterConfig.HTTPClient`, so an entry client without the long timeout caps inference at 30 s no matter who built it. The budget is then MOVED rather than kept on the client: `http.Client.Timeout` would also cover the cold-load wait, so the clone's `Timeout` is zeroed and the transport arms the same budget on the request only once the model is resident.

## Backend-Owned Embedded Provider

The `openai_compatible.embedded` record is **not user-authored**. It is generated from the authoritative `embedded_llm:` section (`backend/config/config.go` `EmbeddedLLMConfig`, reconciled by `Config.SyncEmbeddedLLMProvider` → `LLMConfig.SyncEmbeddedProvider`) and exists exactly while `embedded_llm.installed` is true:

```yaml
llm:
  openai_compatible:
    embedded: # GENERATED — saving LLM settings cannot delete or redirect it
      base_url: "http://127.0.0.1:4321/v1" # derived from embedded_llm.port, never stored separately
      api_key: "" # a loopback server takes no key
      models: ["Bonsai 2 27B"]
```

No `tls_fingerprint` is ever set: the endpoint is plain HTTP on loopback, so the ADR-054 pin has nothing to pin. `output_token_reserve` is the one operator field with no `embedded_llm` counterpart, so it survives regeneration; every other field is rewritten verbatim from the authoritative state.

**Why the backend re-injects it.** `UpdateLLMConfig` treats a non-nil `req.OpenAICompatible` as a WHOLE-MAP replacement — that is how the UI deletes a provider. A settings draft that never saw the generated record (a dialog opened before the install finished, a provider deletion, a hand-built request) would therefore silently delete the local model on every save. The sync runs after the candidate is built and before it is validated, so:

- an absent record is regenerated from `embedded_llm.port`;
- a draft that claims the `embedded` key cannot redirect `base_url` or swap the model list — backend-owned fields are overwritten from the authoritative state;
- an uninstalled model drops the record even if the draft still carries it.

The second sync point is the config load path (`LoadWithResult`, after `ApplyDefaults` and before `validate`), which makes a hand-deleted entry self-heal and a record with no install behind it disappear. Both are pure in-memory work: no hardware probe, no network, no disk. `SyncEmbeddedProvider` copies the provider and override maps before mutating them, because a candidate built as a struct copy shares its map headers with the live config — a sync performed for a candidate that is later REJECTED must not leak into the observable state.

The third is a pre-spawn port move: `embedded_llm.port` is a preference that is re-checked before every spawn, and a load that walks to the next free port persists the new one and regenerates the record from it, so `base_url` never describes a socket the server is not bound to (see [embedded-llm.md](embedded-llm.md#port-allocation)).

**The settings UI renders no editor for it.** `useLLMConfig` loads the record into `providerConfigs` — the default-model picker is built from that map and the composite default must validate — but does NOT add `embedded` to `openaiCompatibleProviderNames`, the set that drives the compatible-provider accordions, and `useLLMConfigSave` omits backend-owned keys from the draft it sends (`frontend/src/lib/llm-providers.ts` `isBackendOwnedProvider`). An accordion for it would offer edits the next sync silently discards. The name is also reserved STATICALLY in `LLMSettings` (`RESERVED_NAMES`), not derived from the loaded providers: while the model is not installed there is no `embedded` record to collide with, so a uniqueness check alone would let a user create one — and the next sync would delete it.

**Resolution needs no special case.** `embedded` is an ordinary `openai_compatible` key, so `allProviderEntries` feeds it to `ResolveModelID`, `ResolveDefaultModelProvider`, `AllModelIDs` and `GetAllProviderConfigs` unchanged: the composite id `embedded/Bonsai 2 27B` resolves with `ProviderType "openai"`, and the router, the lazy probe and both pickers treat it like any self-hosted server.

**The display layer has exactly two special cases**, both in the one picker implementation the two model lists share (`frontend/src/components/ui/ModelPickerMenu.tsx`), so the surfaces cannot disagree: `providerLabel` humanizes the internal key to **Embedded**, and `groupByProvider` hoists that group to the FRONT of every picker. The hoist is necessary because neither feed order puts it there — the settings list iterates a provider map whose JSON keys Go alphabetizes, and `all_models` arrives as (anthropic, chatgpt, sorted `openai_compatible`, sorted `anthropic_compatible`) — so without it the local model reads as a mid-list custom endpoint. It moves the group only: other providers keep their input order, models keep theirs, and no group is invented while the model is not installed. See [embedded-llm.md](embedded-llm.md#settings-block-ui-states).

**The chat picker's list needs an explicit cache drop.** The chat toolbar reads its models from `useConfigData`'s module-level cache, and the settings dialog does not — it re-reads config on every open. An install or removal that generates/deletes the provider record therefore changes `all_models` without the chat picker noticing, so `refreshEmbeddedLLMStatus` calls `invalidateConfigCache()` exactly when `installed` flips between two applied snapshots (never on load/unload: an unloaded model stays selectable, since the first request loads it). See [embedded-llm.md](embedded-llm.md#invariants).

**Context window.** The resolved RAM tier is written as the tier-1 `llm.models."Bonsai 2 27B".context_window` override deterministically by the path that knows it (install / load), never by querying the server — the process is stopped more often than not, and `GetConfig` must stay network-free. The tier-1.5 lazy probe may still refine the value at runtime, but a config override always shadows it. A `contextWindow` argument of `0` means "leave the existing override alone", which is what the load and settings-save paths pass.

**Validation.** `validate()` rejects an out-of-range `embedded_llm.port` (legal: 1024–65535, or `0` = allocate at install time), `installed: true` with `port: 0` (a completed install always has one, and the base URL could not be derived), and a non-positive `auto_unload.minutes` — each with a message naming the key and the fix. `auto_unload.minutes` is validated even while the timer is disabled, so re-enabling it can never activate a dead budget.

**Uninstall ordering.** Because the record disappears with `installed: false`, a `default_model` still pointing at `embedded/Bonsai 2 27B` becomes dangling: `validate()` fails such a config at load and `UpdateLLMConfig` rejects the save. The Remove flow must therefore migrate `default_model` off the embedded composite as part of clearing the section.

**Ensure-loaded transport.** This is the one provider whose endpoint may not be listening: with `embedded_llm.auto_unload` armed the process is stopped more often than not, so a first request would fail with a connection error instead of loading the model. `providerEntryFromConfig` therefore installs `embeddedllm.EnsureLoadedClient` on this entry's `ProviderEntry.HTTPClient` — the same hook the pin resolver uses, decorated in that order — guarded by `BuilderConfig.EmbeddedLLM.ProviderName`. The seam is `BuilderEmbeddedLLMConfig` (`ProviderName` + `Loader` + `LoadWaitTimeout`), where `Loader` is `embeddedllm.Loader` (`Load(ctx) error` + `MarkActivity()`) and `*embeddedllm.Server` satisfies it unchanged; `guards(name)` requires BOTH halves, so a name with no loader installs nothing and the entry behaves exactly as before.

> **Injection is lock-free and precedes the supervisor.** `BuilderConfig.EmbeddedLLM` is filled on the backend side by `FrontendAPI.applyEmbeddedLoader`, reached through `FrontendAPI.toBuilderConfigLocked` (`ToBuilderConfig` + the seam) — the single wrapper every production conversion goes through, so `backend/configadapter.go` `ToBuilderConfig` stays a pure function of `*config.Config` and never learns about the supervisor. Two constraints shaped it. Every call site runs with `configMu` held while the backend's lock order is one-directional (`(st.mu | st.infoMu) → configMu`), so the injection must not take `st.mu`; and the router is first built inside `NewApplication`, before this subsystem exists, so it must not require a supervisor either. The loader is therefore `embeddedLoaderRef` — a value type that resolves the supervisor at CALL time from `embeddedLLMState.loader`, an `atomic.Pointer[embeddedllm.Server]` published by `embeddedBuild` — and a request that somehow arrives before any lifecycle hook built it constructs the subsystem on the spot (pure local work, no `configMu` held on that path). The gate is the persisted `embedded_llm.installed`, true from the config load onwards, which is also what leaves a user's own unrelated provider named `embedded` untouched while the local model is not installed. `LoadWaitTimeout` stays zero so core applies `DefaultLoadWaitTimeout`; deriving it from config would need `embeddedAutoUnloadPolicy`, which takes `configMu.RLock` and would self-deadlock under the caller's lock. Finally, `initEmbeddedLLM` re-attaches the seam to the LIVE router during the startup restore — before `backend:ready`, so before any session can issue a request — gated on an install so a machine without the model pays no rebuild.
>
> Per-config injection alone cannot cover every router: a per-session orchestrator is built from a `BuilderConfig` converted inside the session factory in `backend/application.go`, which was closed over in `NewApplication` and has no path to the supervisor. `OrchestratorBuilder.SetEmbeddedLLM` therefore also holds a builder-level DEFAULT seam, resolved by `embeddedSeam` inside `buildRouter` — the one place every router is constructed — whenever the config carries no `Loader` of its own. `FrontendAPI.syncEmbeddedBuilderSeam` keeps it in step with `embedded_llm.installed` from `rebuildAfterEmbeddedConfigChange`, the single path the startup restore, a completed install and a removal all funnel through, so a removal withdraws it and a user's own provider reclaimed under the name `embedded` is never hijacked.

What the wrapper adds on top of an ordinary OpenAI-compatible dial:

- **A cold request loads the model.** `Server.Load` runs before the request is sent, so the first inference after an idle unload succeeds instead of failing. A load that fails is reported as itself and never cached — and the request is not sent to a socket nothing is listening on.
- **Concurrent cold requests coalesce.** The transport keeps ONE in-flight load and every waiter joins it, so a burst of first requests is one weight load, not N queue entries on the supervisor's single-instance gate.
- **The wait is bounded, and longer than the load's own budget.** `DefaultLoadWaitTimeout` is `DefaultReadyTimeout + 2m` (17 min vs 15 min) on purpose: the supervisor's ready budget is the authority on "this load is wedged", so a wedged model is diagnosed by `Load` — with the server's own log tail — rather than reported as a transport timeout. An expired transport budget is the distinct sentinel `ErrLoadWaitTimeout`.
- **The load outlives the request that triggered it.** `timeouts.llmRequestTimeout` (default 600 s) is SHORTER than the ready budget, so the load runs on a context derived from nothing the caller owns: a request that runs out of patience stops waiting and returns its own context error, while the load runs to completion and the next request joins it. Binding the load to the request context would cancel a legitimate cold load at ten minutes, discard the half-loaded weights, and leave the model permanently unreachable through the client timeout.
- **Activity is stamped on completion.** `MarkActivity` fires when the response body is read to EOF or closed — not when the headers arrive — so a streamed generation restarts the idle budget instead of being charged to it. The wrapper also forwards `CloseIdleConnections`, so installing it does not strand the entry's connection pool.
- **The load is not charged to the request.** The request budget is armed AFTER the gate, on a context derived from the request's own, and released when the response body closes rather than when `RoundTrip` returns (a streamed body is read long after the headers). With the default 600 s budget against a 15 min ready allowance, a client-level timeout would otherwise let a legitimately slow load consume the generation's entire budget before a single token is produced.

**Short-budget callers gate before they arm.** The transport gates inside `http.Client.Do`, which is too late for a caller that has already created its deadline: the one-shot service requests arm `timeouts.serviceLLMRequestTimeout` (default 120 s) first and issue the request second, so a cold load measured in minutes would be charged to it and the call would fail instead of waiting. `OptimizePrompt`, `GenerateCommitMessage` and the session manager's title generation therefore call `FrontendAPI.ensureEmbeddedReadyForLLMRequest` — the last through `Manager.SetServiceLLMGate` — BEFORE creating that context, and skip the request entirely when the gate fails. The gate is keyed on `activeModelIsEmbedded` (installed AND the default model resolving to this provider), runs the load under the app context bounded by `DefaultLoadWaitTimeout`, and returns at once for every other provider.

Only the inference path is wrapped. The Fetch Models listing and the lazy context-window probe keep dialing this endpoint through `DirectDialClient`: neither may start a multi-gigabyte weight load as a side effect of opening a settings dialog or a session, and both are already best-effort — a cold probe just leaves the tier-1 `llm.models."Bonsai 2 27B".context_window` override (written by the install/load path) in charge until the server runs.

**Reasoning wire.** The same `embedded.guards(name)` branch is the ONLY place that sets `llm.ProviderEntry.ReasoningWire`: this entry carries `ReasoningWireChatTemplateKwargs`, every other entry keeps the zero value `ReasoningWireVendorDefault` (top-level `enable_thinking` / `reasoning_effort`, which is what vLLM, LM Studio, SGLang, Ollama and DashScope read). The pinned llama.cpp fork parses the payload differently — `oaicompat_chat_params_parse` never reads a top-level `enable_thinking`, only the one inside `chat_template_kwargs`, whose values it JSON-dumps and compares against the strings `true`/`false` (so the flag must be a JSON boolean, and a JSON *string* makes the server throw). Under the vendor spelling `Off` would therefore be a silent no-op on this endpoint. The wire is an explicit per-entry switch and never a loopback heuristic: guessing from `base_url` would mislabel every non-llama.cpp local server and silently change the body of providers that work. See [embedded-llm.md](embedded-llm.md#reasoning-effort-and-the-family-resolution) and [ADR-066](../decisions/066-embedded-llm-runtime.md) D7.

See [embedded-llm.md](embedded-llm.md) for the install/supervision side and [ADR-066](../decisions/066-embedded-llm-runtime.md) for the decision record.

## Configuration

Provider configuration lives in `config.yaml` under each provider block (api key, base URL, model list, defaults). Compatible providers additionally carry `tls_fingerprint` (SPKI pin, ADR-054) and `auto_retry_seconds` (automatic re-send of retryable failures — 0/omitted = disabled; see [Automatic Resend](#automatic-resend-ui-owned-countdown) above). Main-loop calls use `timeouts.llmRequestTimeout` (default 600 seconds); one-shot service calls for session titles, commit messages, and prompt optimization use the independent `timeouts.serviceLLMRequestTimeout` (default 120 seconds), so a stuck auxiliary request cannot inherit the ten-minute chat-loop budget. The authoritative reference for every tunable is `config.example.yaml`. Env vars are expanded as `${VAR}`; on macOS `config.LoadShellEnvironment()` runs before any other init so Finder-launched apps inherit shell env. For self-hosted servers (vLLM, llama.cpp, LM Studio, Ollama), reliable tool calling additionally requires **server-side** configuration — tool-call parser/chat-template selection per model family, sampling defaults, context-window sizing.

One provider block is app-written rather than operator-authored: `llm.openai_compatible.embedded` is generated from the `embedded_llm:` section and regenerated on every config load and every LLM settings save, so hand-editing it has no lasting effect — edit `embedded_llm:` (or use the Embedded LLM settings UI) instead. See [Backend-Owned Embedded Provider](#backend-owned-embedded-provider) and [embedded-llm.md](embedded-llm.md).

## Related Specs

- [sp4rk llm-providers](https://github.com/v0lka/sp4rk/blob/main/specs/domains/llm-providers.md) — canonical `Router`, `ModelRegistry` (5-tier Resolve), token counting, `TrackingCaller`, retry/backoff
- [sp4rk llm-providers contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/llm-providers.md) — `Provider`/`Router`/`Message`/`ChatRequest`/`ChatResponse` interface definitions
- [orchestration/router.md](orchestration/router.md) — routing uses the router for classification
- [../contracts/core-sp4rk.md](../contracts/core-sp4rk.md) — LLM interfaces at the core↔sp4rk boundary
- [embedded-llm.md](embedded-llm.md) — the local inference server behind the backend-owned `embedded` provider (install, supervision, port allocation, RAM-tiered context)
- [ADR-066: Embedded LLM Runtime (Bonsai 2 27B)](../decisions/066-embedded-llm-runtime.md) — why the local runtime is a hybrid provider on the existing OpenAI-compatible path
