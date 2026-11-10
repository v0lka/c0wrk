# sp4rk patch: upstream Kimi model catalog entries

## Goal

Upstream the model metadata currently carried as a c0wrk overlay in
[`backend/config/kimi_models.go`](../../backend/config/kimi_models.go).
Once the next `github.com/v0lka/sp4rk` release contains equivalent entries,
c0wrk can delete the overlay file and the merge logic that applies it.

## Current c0wrk overlay

`backend/config/kimi_models.go` defines tier-2 defaults for three Kimi model
IDs that are missing or outdated in the current sp4rk built-in catalog:

| Model ID | Context window | Output limit | Family | Protocol | Capabilities |
|---|---|---|---|---|---|
| `k3.1-pro` | 1,000,000 | 131,072 | `kimi` | `chat_completions` | Attachment, Reasoning, ToolCall |
| `k3.1-pro-highspeed` | 1,000,000 | 131,072 | `kimi` | `chat_completions` | Attachment, Reasoning, ToolCall |
| `kimi-for-coding` | 1,000,000 | 131,072 | `kimi` | `chat_completions` | Attachment, Reasoning, ToolCall |

The overlay is merged in:

- [`backend/configadapter.go`](../../backend/configadapter.go) — seeded above the
  sp4rk catalog but below user `llm.models` overrides.
- [`backend/frontend_api_config.go`](../../backend/frontend_api_config.go) —
  consulted by `GetModelConfig`/`SetModelConfig` before falling back to
  `llm.ResolveBuiltInModel`.

## Equivalent upstream changes

Apply these changes to `github.com/v0lka/sp4rk/llm/modelregistry.go`, inside
`getBuiltInRegistry()`, in the `── Kimi models (Moonshot AI) ──` section.

### 1. Add the new K3.1 Pro entries

Insert the two new short IDs near the other Kimi Code endpoint aliases.
The `k3.1-pro` / `k3.1-pro-highspeed` names carry no "kimi" token, so the
explicit `Family: "kimi"` is load-bearing (the same reason the existing
`k3` alias sets it).

```go
"k3.1-pro": {
    ContextWindow: 1_000_000,
    OutputLimit:   131_072,
    TokenizerType: "approximate",
    Family:        "kimi",
    Capabilities:  &ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
},
"k3.1-pro-highspeed": {
    ContextWindow: 1_000_000,
    OutputLimit:   131_072,
    TokenizerType: "approximate",
    Family:        "kimi",
    Capabilities:  &ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
},
```

### 2. Update the `kimi-for-coding` entry to K2.8 Preview

The current sp4rk entry still describes the older K2.7 Code variant
(256K context, 65K output). c0wrk overlays it as the K2.8 Preview model
with the same 1M context and 131K output as `k3.1-pro`.

Change the existing entry from:

```go
"kimi-for-coding": {
    ContextWindow: 262144,
    OutputLimit:   65536,
    TokenizerType: "approximate",
    Family:        "kimi",
    Capabilities:  &ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
},
```

to:

```go
"kimi-for-coding": {
    ContextWindow: 1_000_000,
    OutputLimit:   131_072,
    TokenizerType: "approximate",
    Family:        "kimi",
    Capabilities:  &ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
},
```

### 3. (Recommended) Keep `kimi-for-coding-highspeed` consistent

The highspeed counterpart is not currently overridden by c0wrk, but updating
it to the same K2.8 Preview values avoids a confusing 256K/65K sibling entry:

```go
"kimi-for-coding-highspeed": {
    ContextWindow: 1_000_000,
    OutputLimit:   131_072,
    TokenizerType: "approximate",
    Family:        "kimi",
    Capabilities:  &ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
},
```

## Notes on field choices

- `Protocol` is intentionally omitted from the proposed built-in entries.
  `finalizeMeta` calls `resolveProtocol`, which falls back to
  `DetectProtocol`. These IDs contain no `claude`, `gemini`, `gemma`, `gpt-5`,
  `gpt-6`, or `codex` token, so they resolve to `ProtocolChatCompletions`
  automatically — matching the overlay's explicit protocol.
- `Family` must stay explicit: `DetectFamily` would not infer `kimi` from the
  short IDs `k3.1-pro` / `kimi-for-coding`.
- `Temperature` is intentionally omitted from the overlay. Existing sp4rk
  K3/K2.7 Code entries deliberately omit `Temperature` because the Kimi Code
  endpoint rejects non-1 temperature values. Preserve that choice when
  upstreaming so that the router does not inject a `temperature` parameter
  for these thinking-locked models.

## Cleanup after the sp4rk bump

After updating `github.com/v0lka/sp4rk` in `go.mod`:

1. Delete `backend/config/kimi_models.go`.
2. Delete `backend/config/kimi_models_test.go`.
3. Remove the `config.KimiModelDefaults()` / `config.KimiModelDefault()`
   merge calls from `backend/configadapter.go` and
   `backend/frontend_api_config.go`.
4. Update any c0wrk tests that asserted the overlay values to use
   `llm.ResolveBuiltInModel` as the source of truth instead.

## Verification

After the patch lands in sp4rk and c0wrk bumps to that version:

```go
for _, id := range []string{"k3.1-pro", "k3.1-pro-highspeed", "kimi-for-coding"} {
    meta, ok := llm.ResolveBuiltInModel(id)
    if !ok {
        t.Fatalf("missing built-in entry for %q", id)
    }
    if meta.ContextWindow != 1_000_000 {
        t.Errorf("%s context window = %d, want 1000000", id, meta.ContextWindow)
    }
    if meta.OutputLimit != 131_072 {
        t.Errorf("%s output limit = %d, want 131072", id, meta.OutputLimit)
    }
    if meta.Family != "kimi" {
        t.Errorf("%s family = %q, want kimi", id, meta.Family)
    }
    if meta.Capabilities == nil || !meta.Capabilities.Attachment || !meta.Capabilities.Reasoning ||
        !meta.Capabilities.ToolCall {
        t.Errorf("%s capabilities = %+v, want attachment/reasoning/tool_call", id, *meta.Capabilities)
    }
}
```

A passing run confirms the overlay is no longer needed.
