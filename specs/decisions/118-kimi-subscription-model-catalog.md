# ADR-118: Kimi subscription model catalog

## Status

Accepted. Supersedes the live-catalog refusal in [ADR-117](117-kimi-subscription-auth.md) D6.

## Context

ADR-117 pinned Kimi for Coding OAuth to a compile-time checklist of four model ids. The coding endpoint's model set is part of the subscription: a Max account is served models, including `k3.1-pro`, that the pinned list does not name. The Kimi Code CLI already reads that set with `GET {coding base}/models` after sign-in. A static list cannot follow the account.

Separately, the sp4rk built-in registry metadata for the well-known Kimi ids lagged the actual endpoint capabilities (for example, `kimi-for-coding` still described the older K2.7 Code 256K variant, and the `k3.1-pro` ids carried stale context/output figures). c0wrk therefore maintains a small overlay of corrected factory defaults for the ids it knows the subscription advertises.

## Decision

`FetchKimiModels` loads the signed-in checklist from `GET {regional coding base}/models` using the live bearer and the profile's model-API headers. The server's order is the checklist order. Metadata for each id resolves in this priority:

1. User per-model overrides (`llm.models.<name>`) — always win.
2. c0wrk Kimi defaults overlay (`backend/config/kimi_models.go`) — applies to `k3.1-pro`, `k3.1-pro-highspeed`, and `kimi-for-coding` with corrected K3.1 Pro / K2.8 Preview metadata (1M context, 131072 output, `kimi` family, `chat_completions` protocol, attachment/reasoning/tool_call capabilities).
3. sp4rk built-in registry metadata for any other registry-known id.
4. Endpoint fields (`context_length`, `supports_reasoning`) for ids the registry does not know.

Redirects are not followed. A signed-out call fails before transport.

`GetKimiModelPreset` remains the network-free fallback (`k3.1-pro`, `k3.1-pro-highspeed`, `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed`). An empty or failed catalog leaves that fallback in place, including when a previous fetch had already succeeded. Sign-out and account-region changes also restore the fallback instead of retaining another account's catalog. API-key mode continues to list Moonshot platform models through `ListProviderModels`.

## Consequences

- The Settings checklist shows the models the signed-in subscription serves, including ids the app has not hard-coded.
- The bearer stays on the pinned coding host.
- The c0wrk overlay gives the Settings model configuration dialog correct factory defaults for `k3.1-pro`, `k3.1-pro-highspeed`, and `kimi-for-coding` without waiting for a sp4rk catalog update. User overrides still take precedence; an all-defaults save drops the override and lets the overlay apply again.
- An unknown id is selectable. Its family and sampling stay on the registry's unknown-model path until the registry learns the id.
- The offline preset can still lag the endpoint until the next app update; a successful fetch replaces it.

## Alternatives Considered

- **Keep the four-id preset as the only list.** Rejected: it hides models the subscription already serves.
- **Replace the preset with the live list and show nothing when the fetch fails.** Rejected: a signed-in checklist would go blank on a transient catalog error.
- **Rely solely on the sp4rk built-in registry for known-id metadata.** Rejected: the built-in catalog carried stale figures for ids the subscription actively advertises, so the Settings dialog would misreport context window, output limit, and capabilities.
