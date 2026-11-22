# Context-window caps for the ChatGPT subscription (OAuth)

When the `chatgpt` provider runs in OAuth mode (`llm.chatgpt.auth.mode: oauth`),
requests go to the ChatGPT Codex backend under your subscription credentials.
The context window c0wrk plans prompts against does **not** come from that
backend: it comes from c0wrk's built-in model registry, whose values describe
the **public OpenAI API** tier. For subscription models the two disagree, and a
manual tier-1 override aligns them.

## Why the built-in value is wrong for subscriptions

The registry entry for a Codex model (e.g. `gpt-5.3-codex`, 400 000) mirrors
what the public API advertises. What a ChatGPT subscription actually serves is
different:

| baseline | context window |
| -------- | -------------- |
| legacy Codex baseline | 272 000 tokens |
| current Codex flagship | 500 000 tokens |

(Exact values are whatever your subscription serves — **Settings → LLM →
chatgpt → Fetch models** while signed in shows your account's catalog.)

Normally a self-hosted model's window is corrected at runtime by the lazy
metadata probe, but that probe deliberately skips subscription (OAuth) entries
— probing them would leak your stored credentials to the vendor's keyless probe
paths (ADR-074). Nothing therefore corrects the registry value for a
subscription model, and the tier-1 config override is the only mechanism that
pins the served window.

## Why the cap matters

- **Compaction timing.** Compaction thresholds (85 / 92 / 98 %) are percentages
  of the resolved window. Overstated (400 000 registry vs the 272 000 legacy
  baseline), compaction fires around 340 000 tokens — past what the backend
  accepts, so the first symptom is a mid-task backend rejection instead of a
  graceful compaction. Understated (400 000 registry vs the 500 000 current
  flagship), compaction fires ~120 000 tokens early and throws away
  subscription capacity you are paying for.
- **Cache-miss cost.** A larger calibrated window invites longer prompts; on a
  prompt-cache miss the whole prompt is reprocessed at once — slower first
  tokens and faster consumption of the subscription's usage budget. Keeping the
  calibrated window at what the backend actually serves bounds both.
- **Parity with OpenCode.** OpenCode — the other major open-source coding agent
  with a ChatGPT OAuth integration — ships exactly these caps for the same
  backend. Pinning the same values makes c0wrk's compaction timing and prompt
  budgets directly comparable when you evaluate the two side by side.

## How to set the cap

Override the model's context window under `llm.models.<slug>` — the tier-1
value that beats every other source (the catalog, the registry, the fallback).
Values 0/unset inherit the built-in default; any explicit value must be within
1–16 777 216. Edit `~/.c0wrk/config.yaml` while the app is not running:

```yaml
llm:
  chatgpt:
    auth:
      mode: oauth
  models:
    "gpt-5.3-codex":
      # Current flagship served by the ChatGPT subscription.
      context_window: 500000
    # A model still served at the legacy Codex baseline:
    # "codex-mini-latest":
    #   context_window: 272000
```

The same entry is editable through the UI: **Settings → LLM** → the model's
row → **Configure** → context window (it persists into the same
`llm.models.<name>` key, and saving a value equal to the built-in default
removes the entry again).

Pick the value your subscription actually serves: the legacy 272 000 baseline
for models still served at the old cap, 500 000 for the current flagship. If
you are unsure, compare against what the Codex CLI reports for the same model —
the two speak to the same backend.

No backend "subscription cap" logic exists or is needed: c0wrk never
hard-codes per-provider ceilings, and this override is the ordinary documented
mechanism — see [llm-providers.md](../specs/domains/llm-providers.md)
§ Context Window Resolution.
