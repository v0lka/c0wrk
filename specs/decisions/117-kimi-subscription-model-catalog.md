# ADR-117: Kimi subscription model catalog

## Status

Accepted. Supersedes the live-catalog refusal in [ADR-116](116-kimi-subscription-auth.md) D6.

## Context

ADR-116 pinned Kimi for Coding OAuth to a compile-time checklist of four model ids. The coding endpoint's model set is part of the subscription: a Max account is served models, including `k3.1-pro`, that the pinned list does not name. The Kimi Code CLI already reads that set with `GET {coding base}/models` after sign-in. A static list cannot follow the account.

## Decision

`FetchKimiModels` loads the signed-in checklist from `GET {regional coding base}/models` using the live bearer and the profile's model-API headers. The server's order is the checklist order. Registry-known ids keep registry metadata; other ids keep `context_length` and `supports_reasoning`. Redirects are not followed. A signed-out call fails before transport.

`GetKimiModelPreset` remains the network-free fallback (`k3.1-pro`, `k3.1-pro-highspeed`, `k3`, `k3-256k`, `kimi-for-coding`, `kimi-for-coding-highspeed`). An empty or failed catalog leaves that fallback in place, including when a previous fetch had already succeeded. Sign-out and account-region changes also restore the fallback instead of retaining another account's catalog. API-key mode continues to list Moonshot platform models through `ListProviderModels`.

## Consequences

- The Settings checklist shows the models the signed-in subscription serves, including ids the app has not hard-coded.
- The bearer stays on the pinned coding host.
- An unknown id is selectable. Its family and sampling stay on the registry's unknown-model path until the registry learns the id.
- The offline preset can still lag the endpoint until the next app update; a successful fetch replaces it.

## Alternatives Considered

- **Keep the four-id preset as the only list.** Rejected: it hides models the subscription already serves.
- **Replace the preset with the live list and show nothing when the fetch fails.** Rejected: a signed-in checklist would go blank on a transient catalog error.
