# ADR-116: Kimi for Coding subscription authentication and multi-provider OAuth wiring

## Status

Accepted. The refusal of a live model catalog in D6 is superseded by [ADR-117](117-kimi-subscription-model-catalog.md).

## Context

Kimi is a first-class fixed provider whose API-key mode targets the Moonshot OpenAI-compatible platform. Kimi for Coding subscriptions instead use browser-issued OAuth credentials accepted by a separate coding endpoint. The public Kimi Code CLI profile fixes the issuer, public client identity, Device Authorization Grant, and protocol headers; those values are part of the credential's trust boundary rather than user-tunable provider configuration.

[ADR-074](074-chatgpt-subscription-auth.md) established secure browser OAuth, minimal OS-keychain persistence, proactive refresh, and a builder token-source seam for ChatGPT. Its original single-provider source shape cannot represent ChatGPT and Kimi signed in concurrently, and a blanket sign-out or streaming rule for every OAuth provider would break account independence or impose the Codex wire protocol on Kimi.

The decision must pin subscription endpoints against credential redirection, preserve API-key behavior, keep explicit OAuth modes fail-closed without static-key fallback, reuse the existing credential lifecycle, and extend the router seam without changing ChatGPT's contract. The initial implementation incorrectly assumed an Authorization Code + loopback profile; the live `auth.kimi.com/oauth/authorize` endpoint returns 404. The authoritative Kimi CLI sources (`src/kimi_cli/auth/oauth.py`, KLIP-14) instead define Device Authorization Grant.

## Decision

**D1 — One fixed Kimi provider with two closed auth modes and two closed regions.** `llm.kimi` carries `api_key`, `models`, `output_token_reserve`, `auth.mode`, and `auth.region`. `auth.mode` is `api_key` (the default) or `oauth`, validated identically at config load and at `UpdateLLMConfig`. API-key mode uses `https://api.moonshot.ai/v1` and the static key. OAuth mode marks the existing OpenAI entry `SubscriptionAuth`; it does not create a second provider or a user-editable OAuth endpoint. `auth.region` is `global` (default) or `mainland_cn`, validated at the same boundaries: global pins `https://api.kimi.ai/coding/v1`, mainland_cn pins `https://api.kimi.com/coding/v1`.

**D2 — The public Kimi Code CLI device profiles are compile-time data.** `providerauth.Kimi()` defaults to global and `providerauth.KimiForRegion` selects global or mainland_cn. Both use public client id `17e5f671-d194-4dfb-9706-5516cb48c098`; issuer is `https://auth.kimi.ai` for global and `https://auth.kimi.com` for mainland_cn. Sign-in starts with `POST /api/oauth/device_authorization` containing only `client_id`; the user opens the returned `verification_uri_complete`. Device token polling and refresh both use `POST /api/oauth/token`: polling uses `grant_type=urn:ietf:params:oauth:grant-type:device_code`, refresh uses `grant_type=refresh_token`. `authorization_pending` and `slow_down` are non-terminal, `expired_token` and `access_denied` are actionable failures, and cancellation aborts polling. Token requests carry genuine `X-Msh-Platform`, `X-Msh-Version`, `X-Msh-Device-Name`, `X-Msh-Device-Model`, `X-Msh-Os-Version`, and a stable UUID `X-Msh-Device-Id` stored outside config.yaml in `<agentDir>/device_id` with mode 0600. Coding API requests retain `User-Agent: c0wrk/<core/version.Version> (dev until release ldflags inject the tag)` and `anthropic-version: 2023-06-01`. Endpoint and client changes require a reviewed code change; configuration cannot redirect subscription credentials or migrate them between regions. An effective region change cancels any in-flight sign-in, signs out the old manager, reconstructs it with the selected profile and requires fresh authorization; the frontend flushes the pending config save before starting OAuth so the browser flow cannot use the previous region. The persisted credential records its region. Startup rejects and clears a mismatch before any refresh; a legacy record without a region is accepted only as global. A non-secret durable rejection marker is written before deletion, so even a failed keychain delete cannot let the old credential resurrect by changing the config back; fresh sign-in or successful sign-out clears the marker.

**D3 — Kimi uses the shared secure credential lifecycle.** The TokenManager, `KeyringSecretStore`, minimal refresh-token-plus-identity persistence, proactive single-flight refresh, rotation handling, and reauthentication errors apply to Kimi through profile data rather than a copied implementation. Kimi's keychain record is `providerauth:kimi`. Startup restores it without network I/O through `InitKimiAuth`; token operations use the proxy-aware outbound HTTP client and follow proxy-setting updates. Access tokens, refresh tokens, device codes, and ID tokens stay out of config, logs, errors, events, and RPC results. ChatGPT retains Authorization Code + PKCE; Kimi does not create a loopback listener.

**D4 — The builder seam becomes provider-keyed and preserves legacy sign-out semantics.** `BuilderSubscriptionAuthConfig.TokenSources` maps provider entry names to independent live sources while `ProviderName`/`TokenSource` remains the compatibility form. An explicit per-build source or map wins over the builder default; snapshots copy maps before router construction or request-time lookup. `SetSubscriptionTokenSource` updates only its named provider. The historical zero value withdraws ChatGPT only, while a named nil source withdraws that provider only; a supplied map replaces the complete default map. This lets ChatGPT and Kimi restore, refresh, sign in, and sign out independently across app-level and per-session routers.

**D5 — OAuth stays fail-closed, while streaming remains protocol-specific.** A marked entry with no live source uses `dynamicSubscriptionSource`, which re-reads only its provider's current source per request. Signed-out requests fail before transport with provider-specific sign-in guidance; a lingering static key is preserved but never acts as an OAuth fallback. A later sign-in or startup restore serves cached routers through the same dynamic source. `RequireStreaming` is set only for ChatGPT's Codex endpoint. Kimi OAuth retains normal OpenAI streaming behavior and calls `chat/completions`; its `anthropic-version` header does not imply Anthropic Messages transport.

**D6 — Kimi owns a parallel RPC/event surface and deterministic presets.** `StartKimiSignIn`, `CancelKimiSignIn`, `GetKimiAuthStatus`, `SignOutKimi`, and `GetKimiModelPreset` expose the shared lifecycle without ChatGPT account fields. `kimi_auth:state` carries only `pending`/`success`/`error`/`cancelled` transitions and secret-free URL, error, email, and expiry fields. Sign-out and region rebinding complete synchronously and emit `cancelled` so account-scoped status and live-catalog state are invalidated. Successful sign-in mirrors the source and rebuilds the judge and router before the success event; startup restore rebuilds when oauth mode is selected. `GetKimiModelPreset` is network-free and always returns the Kimi for Coding subscription preset: `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed`. API-key mode lists Moonshot platform models through `ListProviderModels`; the preset RPC never depends on a stale persisted mode. Registry metadata enrichment is fail-soft. [ADR-117](117-kimi-subscription-model-catalog.md) adds the live model catalog and expands the fallback.

## Consequences

**Positive:**

- Kimi for Coding subscriptions work through the same device flow as the official CLI, without placing long-lived credentials in plaintext configuration, and every OAuth request stays on the pinned issuer and coding API.
- No loopback port is required, and sign-in works through any browser that can open `verification_uri_complete`.
- The shared providerauth implementation keeps refresh, persistence, cancellation, and error behavior consistent across providers.
- Independent provider-keyed sources support simultaneous ChatGPT and Kimi accounts while preserving the legacy zero-value ChatGPT sign-out behavior.
- Kimi retains the ordinary OpenAI provider path and existing streaming choice; ChatGPT retains its streaming-only Codex behavior and ADR-074 contract unchanged.
- Startup remains network-free for restoration, proxy changes reach token operations, and model selection remains deterministic without a live catalog dependency.

**Negative:**

- Rotation or revocation of the pinned public OAuth client, endpoints, or required headers requires a reviewed application update.
- Device sign-in requires a browser and user confirmation; access-token renewal depends on the persisted refresh token.
- The builder configuration now carries both a compatibility pair and a provider map, which requires the documented precedence and snapshot rules to remain consistent.
- Static presets require an application update when the provider's subscription model offerings change.

**Neutral:**

- A configured Kimi static API key survives a switch to oauth mode but is excluded from OAuth wire authentication.
- Kimi auth status intentionally omits ChatGPT's `account_id` field because the Kimi API profile does not use ChatGPT account identity headers.

## Alternatives Considered

- **Keep the Authorization Code + loopback profile.** Rejected: the assumed authorization endpoint returns 404 and does not match the official Kimi CLI Device Authorization Grant.
- **Expose the Kimi OAuth base URL as ordinary provider configuration.** Rejected: a user-supplied endpoint could receive subscription bearer tokens; the coding endpoint and public client profile are protocol trust anchors.
- **Keep one global subscription token source.** Rejected: ChatGPT and Kimi could not remain signed in independently, and one provider's sign-out could withdraw or replace the other's credentials.
- **Clear the entire token-source map on the legacy zero value.** Rejected: it would silently sign ChatGPT semantics out of other providers and break concurrent account independence; the zero value now explicitly means ChatGPT only.
- **Copy the ChatGPT auth lifecycle into a Kimi-specific manager.** Rejected: duplicated keychain, refresh, cancellation, and persistence logic would drift; provider-specific behavior belongs in the profile and thin RPC descriptor.
- **Apply `RequireStreaming` to every OAuth entry.** Rejected: streaming-only is a Codex endpoint constraint, not an OAuth property, and Kimi uses ordinary OpenAI `chat/completions`.
- **Use an Anthropic provider transport because Kimi requires `anthropic-version`.** Rejected: the implemented subscription API contract is OpenAI chat/completions with additional headers, not Anthropic Messages.
- **Fall back to `llm.kimi.api_key` while OAuth is signed out or refresh fails.** Rejected: explicit oauth would silently bill or authenticate through a different credential kind; the provider-specific error makes the required user action visible.
- **Fetch a live Kimi model catalog.** Rejected: it adds a network-dependent settings path and another endpoint contract without improving the required static subscription preset.
- **Store OAuth tokens in `config.yaml` or a sibling plaintext file.** Rejected for the same credential-exposure reasons as ADR-074; the OS keychain remains the durable secret boundary.
