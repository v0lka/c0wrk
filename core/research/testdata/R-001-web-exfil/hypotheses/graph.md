# Hypothesis Graph — R-001

## Diagram

```mermaid
graph TD
    classDef confirmed fill:#4CAF50,color:#fff
    classDef refuted fill:#F44336,color:#fff
    classDef in_progress fill:#FF9800,color:#fff
    classDef open fill:#2196F3,color:#fff
    classDef cancelled fill:#9E9E9E,color:#fff

    H001["H-001: Static bundle parsing"]:::open
    H002["H-002: Endpoint extraction from bundles"]:::confirmed
    H003["H-003: Runtime fetch/XHR interception"]:::in_progress
    H004["H-004: Service-worker hooking"]:::open
    H005["H-005: WebSocket interception"]:::open
    H006["H-006: Source-map reconstruction"]:::refuted
    H007["H-007: WASM unpacking"]:::cancelled

    H001 --> H002
    H001 --> H003
    H001 --> H006
    H001 --> H007
    H003 --> H004
    H003 --> H005
```

## Hypothesis Catalog

| ID | Hypothesis | Status | Decision | Parent(s) |
|---|---|---|---|---|
| [H-001](H-001.md) | Static bundle parsing | confirmed | continue | — |
| [H-002](H-002.md) | Endpoint extraction from bundles | confirmed | continue | H-001 |
| [H-003](H-003.md) | Runtime fetch/XHR interception | in-progress | continue | H-001 |
| [H-004](H-004.md) | Service-worker hooking | open | — | H-003 |
| [H-005](H-005.md) | WebSocket interception | open | — | H-003 |
| [H-006](H-006.md) | Source-map reconstruction | refuted | kill | H-001 |
| [H-007](H-007.md) | WASM unpacking | cancelled | kill | H-001 |

---

*Each hypothesis card is a separate file in this directory: `H-NNN.md`.*

[Back to Brief](../brief.md)
