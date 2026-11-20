# Agent-quality investigation runbook

A repeatable method for investigating **model-fault / agent-quality signals** in c0wrk
session artifacts. It was written down after the 2026-11-13 → 2026-11-20 weekly review so
the next iteration starts from a documented procedure instead of an ad-hoc one-off scan.

Two artifacts implement it:

- this document — the taxonomy, the content markers, the wrap discriminator and the log-scan method;
- [scripts/agent-quality-scan.py](../../scripts/agent-quality-scan.py) — the scan that reproduces the weekly report.

## Sources of truth

| Source | Location | Record shape |
| --- | --- | --- |
| LLM dumps | `~/.c0wrk/projects/<project>/<session>/dumps/session_<sid>_llm_dump.jsonl[.zst\|.gz]` and per-delegation `dumps/steps/step_*.jsonl[.zst\|.gz]` | JSONL: `{"ts", "direction": "request"\|"response", "data": {...}, "error"?}`. Requests carry `data.messages` (assistant `tool_calls`, tool-role results) and `data.call_purpose`; responses carry `data.model`, `usage`, and optionally a top-level `error` (LLM-level call failure). Timestamps are UTC (`Z`, nanosecond precision). Live/uncleanly-shutdown `.zst` files decode as a valid prefix (`zstd` exits 1 on the missing footer). |
| slog logs | `~/.c0wrk/logs/session-*.log` | JSONL slog records, local timestamps (`+03:00`). The bad-JSON scan keys on `msg == "tool call arguments received"` with fields `provider`, `tool`, `tool_call_id`, `arg_len`, `valid_json`, `repaired`, `dispatch_outcome`. Format-retry nudges surface as `msg` containing `"format nudge"`. |

## Model-fault taxonomy

First-match-wins against the first ~400 bytes of a tool-result's content. `MODEL_*`
categories are faults where the model produced a call the engine refused (or the user
rejected); `TOOL_*` categories are tools that ran and failed on their own terms; the two
`*_SUSPECT` buckets are near-miss triage aids, not classifications.

| Category | Content marker (verbatim) | Engine origin |
| --- | --- | --- |
| `MODEL_PARSE` | `failed to parse input` | executor failed to decode tool-call arguments (sp4rk `agent/executor_run.go`) |
| `MODEL_VALIDATION` | `input for "` **plus** discriminator ` is invalid: ` / ` is invalid at ` | sp4rk `*tools.InputValidationError` render (`tools/inputvalidate.go`) — top-level and nested `at <path>` forms |
| `MODEL_NOT_FOUND` | line starts `tool not found: ` | unknown tool name |
| `MODEL_POLICY` | `blocked by security policy` | `applySecurityPolicies` / group policy refusal |
| `MODEL_USER_DENY` | `Tool execution denied by user.` | HITL denial |
| `MODEL_HITL_ERR` | `HITL handler error` | HITL handler failure |
| `MODEL_BATCH_SUB` | line starts `error executing "` | per-sub-call failure inside an executed batch |
| `batch pre-flight` (renders inside `MODEL_VALIDATION`/`MODEL_BATCH_SUB` content) | `batch rejected:` | batch pre-flight aggregate embedding per-sub-call renders (sp4rk `agent/executor_run.go`) |
| `TOOL_VALIDATION` | line starts `validation error: ` | tool-level runtime validation |
| `TOOL_COHERENCE` | `File conflict: ` + `modified since your last read` | edit coherence guard |
| `TOOL_EDIT_UNIQ` | `old_string is not unique` / `must appear exactly once` | `edit_file` uniqueness rule |
| `TOOL_EDIT_CONFLICT` | `file changed since read` / `matched more than once` / `old_string` … `was not found` | `edit_file` read-state guard |
| `TOOL_READ_FAIL` / `TOOL_WRITE_FAIL` | `failed to read file: ` / `failed to write file: ` | file tools |
| `TOOL_NO_FILE` | `No such file` / `does not exist` / `not a directory` / `is a directory` | filesystem errors |
| `TOOL_REGEX` | `invalid pattern` / `error parsing regex` / `invalid regex` | regex compile failures |
| `OTHER_SUSPECT` | any `Error`/`ERROR`/`error:`-prefixed head that matched nothing above | near-miss bucket — manual triage only; samples are printed by the scan |
| `INFRA_SUSPECT` | regex `timeout\|deadline\|context cancel\|connection refused\|rate limit\|overloaded\|\b5\d\d\b\|EOF\|broken pipe\|temporarily unavailable\|transport` | near-miss bucket — **deliberately over-captures**: e.g. a *successful* `store_fact` whose keywords mention "transport" lands here. Verify manually, never report as faults without inspection. |

These markers are the same strings the live session layer counts:
`backend/session/tool_result_classify.go` (`isInvalidToolCall`, prefix-keyed on
`input for "` and `batch rejected:` with the same discriminator rule) feeds
`invalid_tool_calls`, and `backend/session/agent_metrics.go` maps executor diagnostics to
the `nudges`/`aborts` counters. The offline scan and the runtime metrics agree by
construction; the runtime contract is the [`agent_metrics` row in
`specs/contracts/event-catalog.md`](../../specs/contracts/event-catalog.md#L155)
(`parse_errors`, `invalid_tool_calls`, `nudges{repeat,same_tool,fruitless,parse,validation,truncation}`,
`aborts{...}` — the `validation` family was added during the week this runbook documents).

### The `<untrusted-content>` wrap discriminator

Successful results of **untrusted-source** tools (`bash_exec`, `read_file`, `ripgrep`,
`web_search`, …) are wrapped in `<untrusted-content>…</untrusted-content>` **before**
being stored in conversation history. Genuine tool-authored error results are **not**
wrapped. A tool result whose content starts with `<untrusted-content` is therefore
excluded from the scan outright, no matter what it contains — an error string quoted in
fetched web content, a CLI's stderr echoed through a successful command, or an MCP
server's own error text would otherwise count as model faults. This one rule removes
essentially all code/log-text false positives. The same discriminator is pinned by tests
in `backend/session` (MCP echo, CLI stderr, quoted web content must not count).

## Method

1. **Window filter** — keep records whose `ts` falls in `[since, until)`, parsed to UTC.
   Dumps are UTC; logs are `+03:00`. **Pitfall:** comparing raw ISO strings against a
   `Z`-less boundary silently compares local MSK wall-clock times and shifts the real
   boundary by three hours (this skewed the one-off scan's nudge count: 14 naive vs 9 UTC-correct).
2. **`tool_call_id` dedup** — history replay repeats every tool result in every later
   request, so a single fault would otherwise be counted once per replay. Dedup key:
   `(project, session, tool_call_id, category)`.
3. **Category histogram** — count deduped entries per category, with per-tool breakdown.
4. **Near-miss nudge scan** — two views: (a) `OTHER_SUSPECT` samples from the dumps (errors
   that matched no category, for manual inspection); (b) log records containing
   `format nudge` (oneshot format-retry retries — a model emitting unparseable output that
   *was* recovered, i.e. the misses just below the fault threshold).
5. **Provider attribution** — three angles: log records bucket `tool call arguments
   received` volume, bad-JSON (`valid_json=false`) and repairs per `provider`; dumps
   attribute each deduped fault to the most recent response `model` in its stream; and
   LLM-level response `error` records (provider/infra failures, not model faults) are
   bucketed separately.

## Running the scan

```sh
# Reproduce the weekly report (window frozen at the report's generation moment):
python3 scripts/agent-quality-scan.py --since 2026-11-13T00:00:00Z --until 2026-11-20T08:41:00Z

# Latest state (window open-ended at "now"); defaults: --since 2026-11-13T00:00:00Z
python3 scripts/agent-quality-scan.py

# Machine-readable output for diffs between runs:
python3 scripts/agent-quality-scan.py --json /tmp/agent-quality.json
```

The script is read-only, dependency-free (stdlib; `zstd` binary only for `.zst` dumps),
and exits 0 for any successful scan.

## This week's report (2026-11-13 → 2026-11-20) and its reproduction

Run the frozen-window command above over the current data; expected output:

| Signal | Value |
| --- | --- |
| Deduped fault results (dumps) | **25** — `MODEL_VALIDATION` 15 (`ask_user` 12, `batch` 3), `TOOL_COHERENCE` 6 (`edit_file`), `INFRA_SUSPECT` 4 (`store_fact`, all wrap-discriminator-exempt near-miss false positives: successful facts whose keywords mention "transport") |
| Real sessions affected | 7 |
| Near-miss nudges (logs, in-window) | 9 `format nudge` retries |
| Bad-JSON tool-call args (logs, in-window) | **0** — every in-window `tool call arguments received` was valid JSON |
| Provider attribution (in-window) | `Z-ai` only (`glm-5.3-flash`); 1 LLM-level response error (`HTTP 0, context canceled` — infra, not a model fault) |
| Pre-window context (2026-11-10 → 12) | `DeepSeek` 109 bad-JSON of 3,076 calls (the raw-`\n`-in-JSON-strings era; repaired in sp4rk `repairToolCallInput`), `Qwen-cloud` 2, `Z-ai` 1 |

Two accounting notes versus the original one-off scan, both fixed in the script:

- the one-off script derived project/session from absolute-path offsets, mislabeling
  `steps/` entries (`sid = "dumps"`, 8-char truncation) and thereby reporting a phantom
  8th session; the script reads `<project>/<session>` from the path relative to the root;
- the one-off script's nudge count used a naive string-compare window (see the pitfall above).

### Where the findings went

- `ask_user` options-contract rejections (the dominant `MODEL_VALIDATION` family): tool
  description restructured so the exactly-two-keys `{label, value}` contract leads
  (`core/tools/askuser.go`); both validation render families are now counted at runtime
  (`validation` counters, see the [`agent_metrics` event-catalog row](../../specs/contracts/event-catalog.md#L155)).
- batch pre-flight rejections: each rejection line now carries a verbatim excerpt of the
  model's own wrong args (sp4rk `truncateBatchInputExcerpt`), and raw control characters
  inside JSON string tokens are repaired before dispatch (`repairToolCallInput` quirk-3).
- `TOOL_COHERENCE` write conflicts: `formatWriteConflict` gained a self-change hint
  (a shell command the session ran earlier is its own change, not another agent's).

## Maintenance notes

- **Marker drift.** The render strings live in sp4rk (`agent/executor_run.go`,
  `tools/inputvalidate.go`) and reach c0wrk as an external dependency. During dual-repo
  development cycles c0wrk builds against a local sp4rk checkout via the repo-root
  `go.work` ([ADR-031](../../specs/decisions/031-gowork-repo-root.md)); after a sp4rk
  bump, re-verify the taxonomy markers verbatim before trusting a diff between weekly runs.
- **Classifier sync.** If you add a category here, mirror it in
  `backend/session/tool_result_classify.go` and the [`agent_metrics`
  row](../../specs/contracts/event-catalog.md#L155); if you change the runtime
  classifier, mirror it here. The `<untrusted-content>` exclusion and the `input for "`
  discriminator are the two invariants both sides must keep.
- **Near-miss buckets are noise-prone by design** (see `INFRA_SUSPECT`). Report them as
  triage queues, never as fault counts.
