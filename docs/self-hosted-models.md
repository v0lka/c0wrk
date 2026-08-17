# Self-hosted models: server configuration for reliable tool calling

c0wrk drives local models through the OpenAI-compatible `chat/completions` API and relies on
**native tool calling** (structured `tool_calls` in the response). When a local server is
misconfigured, the model still *tries* to call tools — but emits the call as plain text
(`<tool_call>...</tool_call>`, ```json fenced blocks, etc.), which c0wrk cannot parse reliably.
This is a class of problems the application cannot fix on its own; it has to be fixed in the
server configuration.

This guide covers the four servers c0wrk is commonly used with — **vLLM**, **llama.cpp
(`llama-server`)**, **LM Studio**, and **Ollama** — with concrete flags, a model-family
lookup table, and a diagnostics checklist. Every recommendation links to the vendor's
primary documentation.

> How to register a server in c0wrk: `llm.openai_compatible.<name>` in `~/.c0wrk/config.yaml`
> (`base_url`, `api_key`, `models`). See `config.example.yaml` for the annotated reference.
> Related specs: [specs/domains/llm-providers.md](../specs/domains/llm-providers.md)
> (provider wiring) and [specs/domains/small-llm.md](../specs/domains/small-llm.md)
> (small-model profile: tool-set narrowing, low-temperature sampling, loop hardening).

## Why local servers break tool calling

Hosted APIs (OpenAI, Anthropic) ship a serving stack matched to the model: the chat template
renders tool definitions correctly and a server-side parser converts the model's raw output
back into structured `tool_calls`. With self-hosted servers you own both halves:

1. **Chat template** — must know how to render `tools` and `tool` role messages for the
   model family (Hermes-style JSON `<tool_call>`, Llama 3.1's `{"type": "function", ...}`
   JSON, Mistral's `[TOOL_CALLS]`, Qwen3-Coder's XML `<tool_call>` ...).
2. **Tool-call parser** — must recognize the format the model actually emits and re-emit it
   as `tool_calls` in the API response instead of leaving it in `content`.

If either half is missing or mismatched, the call leaks into the text channel. Symptom in
c0wrk: the assistant prints something that *looks like* a tool call, or replies with a
stub answer and no tool runs.

Two c0wrk-side facts shape the advice below:

- c0wrk sends the sampling parameters defined by the per-family vendor preset
  (`temperature`, plus `top_p`/`top_k`/`repetition_penalty` where the preset — or an explicit
  `small_llm.sampling` override — sets them) and `reasoning_effort`. Parameters that stay
  unset keep the server-side defaults (vLLM `--generation-config`, `llama-server` flags,
  LM Studio preset, Ollama `options`/environment), so server-side configuration remains the
  right place for anything c0wrk does not set explicitly.
- c0wrk probes model context windows at registration time: LM Studio's
  `GET /api/v0/models` (`loaded_context_length`/`max_context_length`) and the OpenAI-style
  `GET /v1/models` (`max_model_len`, `max_context_length`). Values found there override the
  `llm.models.<name>.context_window` capability in config.

## Cheat sheet: model family → tool parser / template + sampling

| Family | Tool-call format | vLLM `--tool-call-parser` | llama.cpp llama-server | LM Studio | Ollama | Suggested sampling |
| --- | --- | --- | --- | --- | --- | --- |
| Hermes 2/3, NousResearch | JSON `<tool_call>` | `hermes` | native handler (Hermes/Qwen) | works | works | temp 0.1–0.3 |
| Qwen2.5 / Qwen2.5-Coder / QwQ | JSON `<tool_call>` | `hermes` | native handler | works | works | temp 0.0–0.3, `top_p` 0.8–0.9 (Qwen2.5 card) |
| Qwen3 (Instruct/Thinking) | JSON `<tool_call>` | `hermes` (per vLLM docs; verify on your build) | native handler | works | works | temp 0.6–0.7 + `top_p` 0.95 for non-thinking (Qwen3 card); temp 0.1–0.3 for tool-heavy agent loops |
| Qwen3-Coder | **XML** `<tool_call>` | `qwen3_xml` | needs Qwen3-Coder template; see quirks below | works (0.3.x templates) | works | temp 0.7 + `top_p` 0.8 (Qwen3-Coder card); temp 0.1–0.3 for agent loops |
| Llama 3.1 / 3.2 / 3.3 | `{"type":"function",...}` JSON | `llama3_json` (+ template fix, see below) | native handler | works | works | temp 0.1–0.3 |
| Llama 4 | pythonic tool syntax | `llama4_pythonic` | native handler | — | — | temp 0.1–0.3 |
| Mistral (incl. Nemo) | `[TOOL_CALLS]` | `mistral` (+ `bos_token` fix, see below) | native handler (Nemo) | works (0.3.5+) | works | temp 0.1–0.3 |
| DeepSeek-V3 / V3.1 | JSON tool calls | `deepseek_v3` / `deepseek_v31` | generic handler | works | works | temp 0.0–0.3 (DeepSeek-V3 card recommends 0.0) |
| DeepSeek-R1-0528 | JSON tool calls | `drill` | generic handler | — | — | temp 0.5–0.7 (recommended for R1 reasoning) |
| GLM-4.5 | JSON tool calls | `glm45` | generic handler | — | — | temp 0.6 (GLM-4.5 card) |
| Kimi K2 | JSON tool calls | `kimi_k2` | generic handler | — | — | temp 0.6 (Kimi K2 card) |
| Command-R7B | XML-style | `minerator` | native handler | — | works | temp 0.1–0.3 |
| Granite 4.0 | XML-style | `granite4` | generic handler | — | — | temp 0.1–0.3 |
| Phi-4-mini | JSON | `phi4_mini` | generic handler | — | — | temp 0.1–0.3 |

Notes on the table:

- "works" for LM Studio/Ollama means the model's bundled GGUF template handles tool calls
  via their OpenAI-compatible APIs without extra server flags.
- The vLLM parser names are from the
  [vLLM tool-calling docs table](https://docs.vllm.ai/en/stable/features/tool_calling/#supported-models);
  llama.cpp handler names from
  [llama.cpp function-calling docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md).
- Vendor model cards: [Qwen3-Coder](https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct#3-recommended-settings-for-serving),
  [Qwen3-Coder-Flash](https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct#3-recommended-settings-for-serving),
  [DeepSeek-V3](https://huggingface.co/deepseek-ai/DeepSeek-V3#usage-recommendations),
  [Kimi K2](https://huggingface.co/moonshotai/Kimi-K2-Instruct#%E4%BD%BF%E7%94%A8usage),
  [GLM-4.5](https://huggingface.co/zai-org/GLM-4.5#recommended-settings).
- Sampling that is good for chat is often bad for agent tool loops: slightly higher
  temperature improves prose but degrades JSON validity. For agentic use prefer the low end
  of every range; c0wrk's small-LLM profile pins `temperature: 0.1`
  ([small-llm spec](../specs/domains/small-llm.md)).

## vLLM

Primary sources: [Tool calling](https://docs.vllm.ai/en/stable/features/tool_calling/),
[Structured outputs](https://docs.vllm.ai/en/latest/features/structured_outputs/).

Tool calling is **off by default**. The OpenAI-compatible server must be started with an
auto tool-choice parser:

```bash
vllm serve Qwen/Qwen3-Coder-30B-A3B-Instruct \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_xml \
  --max-model-len 131072
```

- `--enable-auto-tool-choice` + `--tool-call-parser <name>` — pick the parser from the
  table above; the full supported list with template caveats is in the
  [vLLM docs](https://docs.vllm.ai/en/stable/features/tool_calling/#supported-models).
- **Llama 3.1/3.2**: the stock HF chat template had a tool-call bug; vLLM documents the
  required `data.tool_call_resolver` template fix — see the
  [Llama 3.1/3.2 section](https://docs.vllm.ai/en/stable/features/tool_calling/#llama-31-and-32-models)
  before serving.
- **Mistral**: same idea — the `bos_token` template quirk is documented in the
  [Mistral section](https://docs.vllm.ai/en/stable/features/tool_calling/#mistral-models).
- **Structured output** (constrained decoding) is available in the OpenAI-compatible server
  and forces tool arguments / JSON into a valid schema. The backend is selected with
  `--structured-outputs-config.backend {auto,guidance,outlines,xgrammar}`; `auto` picks the
  best available, `xgrammar` is the fast multi-backend choice for CPU-constrained decoding —
  [structured outputs docs](https://docs.vllm.ai/en/latest/features/structured_outputs/).
- **Sampling defaults**: c0wrk only sends `temperature`. Pin the rest server-side with
  `--generation-config auto`, which applies the model repo's `generation_config.json`
  (`top_p`, `top_k`, `repeat_penalty` and friends) as request defaults —
  [vLLM tool-calling docs](https://docs.vllm.ai/en/stable/features/tool_calling/) (see
  the *Simulating the hidden reasoning* / sampling notes) and
  [`--generation-config` in server args](https://docs.vllm.ai/en/stable/serving/openai_compatible_server/#server-command-line-arguments).
  To override rather than inherit, ship a custom `generation_config.json` in the model dir.
- Set `--max-model-len` explicitly — the model default may exceed your VRAM or, conversely,
  truncate agent contexts. c0wrk reads `max_model_len` from `/v1/models` when present.

## llama.cpp (`llama-server`)

Primary source: [docs/function-calling.md](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)
in the llama.cpp repository; server flags in
[tools/server/README.md](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).

Tool calling over the OpenAI-compatible endpoint requires the built-in chat templates —
always start with **`--jinja`**:

```bash
llama-server -m qwen3-coder-30b.gguf \
  --jinja \
  --host 127.0.0.1 --port 8080 \
  -c 131072 \
  --top-k 20 --repeat-penalty 1.0
```

- Without `--jinja`, `/v1/chat/completions` tool use falls back to a legacy prompt path
  that most modern models were not trained for — symptom: tool calls appear as text
  ([function-calling docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)).
- **Native handlers** exist for Llama 3.x, Functionary, Hermes 2/3 (incl. Qwen2.5 and
  Qwen2.5-Coder), Mistral Nemo, Firefunction v2, Command-R7B, DeepSeek R1; other models use
  the generic handler — check whether your family is listed in the
  [docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md) before
  debugging.
- **Custom template**: override with `--chat-template-file <template.jinja>`; ~100 community
  templates ship in the llama.cpp repo —
  [server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).
- `parallel_tool_calls=false` in the request reduces malformed multi-call output on small
  models (supported per the
  [function-calling docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)).
- **Grammar mode**: when a model just cannot produce reliable tool-call syntax, GBNF
  grammars can constrain output shape. Grammars and usage are documented in
  [grammars/README.md](https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md)
  (server accepts a default grammar via `--grammar` and per-request `grammar`). This is a
  last resort for tool calling — prefer a correct chat template first.

### Qwen3-Coder quirks in llama.cpp

Qwen3-Coder is the most tool-call-sensitive family in practice:

- **XML, not JSON**: Qwen3-Coder switched tool calls from Qwen3-Instruct's JSON
  `<tool_call>` format to an XML `<tool_call>` syntax. Templates/parsers written for
  Qwen3-Instruct silently break — tracked in
  [ggml-org/llama.cpp#15012](https://github.com/ggml-org/llama.cpp/issues/15012).
- **Lazy-trigger parsing failures**: the parser waits for the model to "decide" before
  switching into tool-call mode; when that trigger misfires the whole call leaks into
  `content` as text — see
  [ggml-org/llama.cpp#26987](https://github.com/ggml-org/llama.cpp/issues/26987).
  Upgrading llama.cpp helps: dedicated `qwen3_coder` common-chat support landed in
  [PR #26252](https://github.com/ggml-org/llama.cpp/pull/26252).
- Practical advice: use a recent llama.cpp build, verify the model card's
  [recommended serving settings](https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct#3-recommended-settings-for-serving),
  and prefer `Qwen3-Coder-Flash` variants / bigger quants on small machines.

## LM Studio

Primary sources: [Tool use via OpenAI-compatible API](https://lmstudio.ai/docs/developer/openai-compat/tools),
[Config presets](https://lmstudio.ai/docs/app/presets),
[changelog 0.3.5](https://lmstudio.ai/changelog/lmstudio/lmstudio-v0.3.5).

LM Studio's server (`Developer` tab or `lms server start`, default `http://localhost:1234/v1`)
supports tool use through the OpenAI-compatible endpoint with **no extra flags** — the
model's bundled template does the work:

```bash
lms server start   # then register in c0wrk: base_url http://localhost:1234/v1
lms load           # load the model (or load it in the Chat/Developer tab)
```

- Model choice matters: LM Studio's own tool-use rollout names **Qwen, Mistral, and
  Llama 3.1/3.2** as working well ([0.3.5 changelog](https://lmstudio.ai/changelog/lmstudio/lmstudio-v0.3.5)).
- **Sampling presets**: set per-model inference defaults (temperature, top_p, top_k) in the
  app and save them as config presets — since c0wrk only sends `temperature`, the preset's
  other values apply ([presets docs](https://lmstudio.ai/docs/app/presets)). For agent
  loops: temp ≤ 0.3, modest `top_p`, preset saved as the model's default config.
- **Structured output**: LM Studio supports `json_schema` response constraints on the
  OpenAI-compatible endpoint — see the
  [developer docs hub](https://lmstudio.ai/docs/developer/openai-compat) for the current
  endpoint surface.
- Load-time settings (context length, GPU offload) live on the model's config in
  **My Models**, not in presets ([presets docs](https://lmstudio.ai/docs/app/presets)) —
  set the context length there; c0wrk picks it up automatically from
  `/api/v0/models` (`loaded_context_length`).

## Ollama

Primary source: [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility).

Ollama (default `http://localhost:11434/v1`, any `api_key` — e.g. `ollama` — is accepted)
applies each model's built-in tool template automatically; the
[compatibility page](https://docs.ollama.com/api/openai-compatibility) confirms `tools`,
`tool_choice`, `temperature`, `top_p`, and `reasoning_effort` are supported on
`/v1/chat/completions`.

```yaml
# ~/.c0wrk/config.yaml
llm:
  openai_compatible:
    ollama:
      base_url: http://localhost:11434/v1
      api_key: ollama
      models:
        - qwen3:14b
```

- **Context window**: Ollama defaults to a **4096-token** context window — far too small
  for c0wrk sessions (system prompt + tools alone approach it). Raise it with the
  `OLLAMA_CONTEXT_LENGTH` environment variable, e.g.
  `OLLAMA_CONTEXT_LENGTH=32768 ollama serve`
  ([Ollama FAQ](https://docs.ollama.com/faq)). Symptom of a too-small window: the model
  "forgets" tools or mid-task instructions.
- No per-request `top_k`/`repeat_penalty` pass-through on the OpenAI layer — c0wrk's
  `temperature` is honored; for more, use a custom Modelfile with `PARAMETER` entries
  ([Modelfile docs](https://docs.ollama.com/modelfile)).
- Thinking models: `reasoning_effort` (`none`/`low`/`medium`/`high`) is supported through
  the OpenAI layer and maps to c0wrk's reasoning-effort capability
  ([compatibility page](https://docs.ollama.com/api/openai-compatibility)).

## Registering the server in c0wrk

Minimal annotated example (full reference in [`config.example.yaml`](../config.example.yaml)):

```yaml
llm:
  openai_compatible:
    vllm-local:
      base_url: http://127.0.0.1:8000/v1   # vLLM OpenAI server
      api_key: dummy                       # vLLM ignores it unless --api-key is set
      models:
        - Qwen/Qwen3-Coder-30B-A3B-Instruct
  models:
    Qwen/Qwen3-Coder-30B-A3B-Instruct:
      protocol: chat_completions
      capabilities:
        tool_call: true       # feature flag: server emits native tool_calls
        temperature: true     # server accepts temperature
        reasoning: false
      context_window: 131072  # only needed if auto-probe cannot detect it
      output_limit: 32768
```

Notes:

- `llm.models.<name>` keys match the model name the server reports on `/v1/models`.
- `capabilities.tool_call` documents that the server produces native `tool_calls`; it does
  not parse text for you — a server that leaks tool calls as text is misconfigured, see the
  checklist below.
- For small/local models also review the **small-LLM profile**
  (`small_llm.*` in config; [spec](../specs/domains/small-llm.md)): it narrows the tool set
  to essentials, pins low temperature, and tightens loop breakers — orthogonal to, and
  combinable with, correct server configuration.

## Diagnostics checklist: "the reply looks like a text tool-call"

Work top to bottom; each step links the check to its likely fix.

1. **Confirm the symptom.** The assistant message contains literal `<tool_call>` /
   `[TOOL_CALLS]` / ```json {"name": ...}``` text, or ends with a stub answer and no tool
   execution. → The server is not converting the model's tool-call output into structured
   `tool_calls`.
2. **vLLM: are the tool flags set?** `ps | grep vllm` or check the launch command:
   `--enable-auto-tool-choice` and `--tool-call-parser <family>` must be present and the
   parser must match the model family (table above). → Restart with the right parser
   ([docs](https://docs.vllm.ai/en/stable/features/tool_calling/)).
3. **llama-server: is `--jinja` present?** No `--jinja` → templates inactive → tool calls
   as text. → Add `--jinja`, and for Qwen3-Coder see the quirks section above
   ([docs](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)).
4. **Parser/template mismatch after a model swap.** Switching Qwen3-Coder ↔ Qwen3-Instruct
   (XML ↔ JSON `<tool_call>`) or Hermes ↔ Llama keeps the old template. → Match
   parser/template to the *current* model
   ([vLLM table](https://docs.vllm.ai/en/stable/features/tool_calling/#supported-models),
   [llama.cpp handlers](https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md)).
5. **Context window too small.** Ollama's 4096 default silently drops the tool definitions
   from the prompt; the model then improvises a textual call. → Raise
   `OLLAMA_CONTEXT_LENGTH` ([FAQ](https://docs.ollama.com/faq)); for vLLM check
   `--max-model-len`, for llama-server `-c`, for LM Studio the load config.
6. **Sampling too hot.** High temperature/top_p corrupt JSON arguments. → Server-side
   defaults: temp ≤ 0.3, `top_p` ≤ 0.9, `repeat_penalty` 1.0 (vLLM `--generation-config`,
   llama-server `--top-k/--repeat-penalty`, LM Studio preset, Ollama Modelfile).
7. **Model cannot do native tool calls at all.** Base/instruct-lite GGUFs without tool
   training will never emit reliable calls. → Use a tool-trained family from the table
   ([LM Studio 0.3.5 changelog](https://lmstudio.ai/changelog/lmstudio/lmstudio-v0.3.5)
   lists known-good families); as a last resort constrain output with grammars
   ([llama.cpp grammars](https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md))
   or structured output
   ([vLLM](https://docs.vllm.ai/en/latest/features/structured_outputs/)).
8. **Check the raw server response** to see which side is at fault:
   `curl -s $BASE_URL/v1/chat/completions -d '{"model":"...","messages":[{"role":"user","content":"list files in /tmp using the provided tool"}],"tools":[...]}' | jq '.choices[0].message'`
   — if `tool_calls` is absent but the call text sits in `content`, the server/parser is at
   fault; if `tool_calls` is present, the problem is on the c0wrk side (file an issue).
9. **Still stuck?** Enable c0wrk's small-LLM profile (`small_llm.enabled`) to reduce the
   tool-set size and harden the loop while you tune the server — but remember: it narrows
   *which* tools are offered, it cannot make a broken parser emit structured calls.

## Reference index

- vLLM — tool calling: <https://docs.vllm.ai/en/stable/features/tool_calling/>
- vLLM — structured outputs: <https://docs.vllm.ai/en/latest/features/structured_outputs/>
- llama.cpp — function calling: <https://github.com/ggml-org/llama.cpp/blob/master/docs/function-calling.md>
- llama.cpp — server README (templates, flags): <https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md>
- llama.cpp — grammars (GBNF): <https://github.com/ggml-org/llama.cpp/blob/master/grammars/README.md>
- LM Studio — tool use: <https://lmstudio.ai/docs/developer/openai-compat/tools>
- LM Studio — presets: <https://lmstudio.ai/docs/app/presets>
- LM Studio — 0.3.5 changelog (tool-use models): <https://lmstudio.ai/changelog/lmstudio/lmstudio-v0.3.5>
- Ollama — OpenAI compatibility: <https://docs.ollama.com/api/openai-compatibility>
- Ollama — FAQ (context window): <https://docs.ollama.com/faq>
- Ollama — Modelfile: <https://docs.ollama.com/modelfile>
- Qwen3-Coder — recommended serving settings: <https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct>
