# LLM Latency & Prompt-Cache Measurement Procedure

Evidence harness for latency and cache-efficiency acceptance work: every claim
like "cache stability improved per-call latency" or "c0wrk matches/exceeds
OpenCode on the same model" is backed by numbers recorded with this procedure.
It documents the observability c0wrk emits (`session_tokens`, `agent_metrics`,
DEBUG session artifacts), a fixed comparative task, the per-run recording
checklist, and the c0wrk-vs-OpenCode comparison protocol.

## Instrumentation inventory

| Signal | Where it lands | Persistence |
| ------ | -------------- | ----------- |
| `session_tokens` event — `session_input_tokens`, `session_output_tokens`, `median_output_tok_s`, `tok_s_samples`, `median_call_ms`, `mean_call_ms`, `max_call_ms`, `call_count`, `cached_input_tokens_total`, `cache_write_tokens_total`, `cache_hit_percent` | Frontend event stream (status-bar data); the emitter also logs the aggregates at Debug into the session log | Transient (not persisted); the Debug session-log line is the durable trace |
| `agent_metrics` event — `llm_call_count`, `cached_input_tokens_total`, `cache_write_tokens_total`, `cache_hit_percent`, `median_call_ms`, `mean_call_ms`, `max_call_ms` alongside the existing per-run quality counters | Emitted once per task finish/abort; persisted by the event pipeline as a `status` message row (metadata JSON round-trips through session reload) | Durable per run |
| Per-call LLM dump — JSONL entries `{ts, direction: "request"|"response", data, error?}` with RFC3339Nano timestamps and the full `ChatRequest`/`ChatResponse` (usage included: `input_tokens`, `output_tokens`, `cached_input_tokens`, `cache_write_tokens`); request entries additionally carry `call_purpose` (e.g. `executor`, `routing`, `summarization`); pre-`1df9c68` builds leave the request `model` empty — read the model from response entries | `~/.c0wrk/projects/<projectID>/<sessionID>/dumps/session_<sessionID>_llm_dump.jsonl[.gz|.zst]` (session-wide) and `.../dumps/steps/step_<stepID>.jsonl[.gz|.zst]` (per plan step); codec follows `session_artifacts.compression` | Durable, DEBUG builds only |
| Session log — executor + emitter Debug lines (LLM calls, session-token aggregate updates) | `~/.c0wrk/projects/<projectID>/<sessionID>/logs/session_<sessionID>.log[.gz|.zst]` | Durable, DEBUG level required |

Window semantics (`core/session_throughput.go`): `median_output_tok_s` and
`median_call_ms` are medians over a sliding window of the last 64 tracked LLM
calls and are omitted below 3 samples (warming up); `mean_call_ms`,
`max_call_ms`, `call_count` and the cache totals/hit-rate are
session-cumulative over every timed call. `cached_input_tokens_total` /
`cache_write_tokens_total` accumulate the providers' cache detail
(`llm.TokenUsage.CachedInputTokens` / `CacheWriteTokens`) — details already
included in each call's `input_tokens`, never additions to it; providers that
report no cache detail (and Anthropic's cache-write counter is
Anthropic-family only) leave the fields at zero. `cache_hit_percent` is
cumulative cached input over cumulative observed per-call input tokens × 100.

## Preparation (every measurement session)

1. Build the app (`make build`) at the commit under test; record the commit.
2. Set `log_level: "DEBUG"` in `~/.c0wrk/config.yaml` — step dumps and the
   session LLM dump exist only in DEBUG builds/sessions.
3. Configure exactly one provider/model; disable model switching mid-run.
   Record: provider, model id, sampling settings, `reasoning_effort`,
   context window, enabled MCP servers/skills (all part of the prompt prefix
   and therefore of cacheability and latency).
4. Create a FRESH project + session per run (never reuse sessions — history
   changes the prompt prefix and the cache state).
5. Optional warm-up: run the task once and discard the result — providers
   differ in cold-start cache behavior (Anthropic writes the cache on first
   use; OpenAI-family caches activate above a per-model token floor).

## Fixed comparative task

The task must be deterministic, small enough to finish in a few minutes, and
require several file reads plus one edit so the run exercises repeated LLM
calls against a growing context (where prompt caching shows).

Fixture (identical bytes every run, committed once and re-checked-out per
run):

```
latency-fixture/
├── README.md            # ~30 lines describing a tiny CLI
├── src/cli.ts           # ~60 lines, entry point
├── src/config.ts        # ~40 lines
├── src/render.ts        # ~80 lines
├── src/store.ts         # ~70 lines
└── test/store.test.ts   # ~50 lines
```

Prompt (verbatim, one message, no attachments):

> Rename the function `saveEntry` to `persistEntry` in `src/store.ts` and
> update every call site in the repository. Run the test suite and report the
> result. Do not change anything else.

Run constraints: fresh session per repetition; no config edits mid-run; same
machine load profile (no parallel heavy tasks); record the run order.

## Per-run recording checklist

For EACH run, record one row in the measurement log:

- [ ] Commit under test, provider, model, sampling/reasoning settings, date/time
- [ ] Wall-clock end-to-end: send → `task_complete` (stopwatch or the session
      log's first/last LLM timestamps for the run)
- [ ] `agent_metrics` payload after the run (from the UI metrics card or the
      persisted `status` row metadata): `llm_call_count`,
      `cached_input_tokens_total`, `cache_write_tokens_total`,
      `cache_hit_percent`, `median_call_ms`, `mean_call_ms`, `max_call_ms`,
      `output_tokens`, `steps`, `finish`
- [ ] Final `session_tokens`-derived totals: `session_input_tokens`,
      `session_output_tokens` (Debug session log's last
      "emitter: session tokens update" line carries them)
- [ ] TTFT proxy: send → first `assistant_chunk` visible in the UI (or the
      session log's first assistant output). CAVEAT: c0wrk's caller path
      returns complete responses (no API-level streaming), so this proxy is
      really "time to first visible output" and includes router/executor
      overhead — use it for end-to-end comparisons only, never as a provider
      TTFT figure
- [ ] Per-call table from the step/session dumps: for each request/response
      pair — duration (response.ts − request.ts), `input_tokens`,
      `output_tokens`, `cached_input_tokens`, `cache_write_tokens`,
      request prefix size (bytes of the serialized request) and, for
      diagnosis, the first ~5 tool names in the request
- [ ] Anomalies: compaction events (`context_compaction`), retries, judge
      interventions, tool failures — each perturbs the prompt prefix and must
      be noted or the run discarded

Cross-check rule: the per-call table's summed `cached_input_tokens` must match
`agent_metrics.cached_input_tokens_total`; the derived median/mean/max of the
per-call durations must match the emitted aggregates within rounding. A
mismatch means the run mixed builds or the dumps lost entries — fix the
harness before quoting any number.

### Dump extraction one-liners

```sh
# Plain JSONL dump (codec none)
jq -s '[.[] | select(.direction == "response") | .data.usage]' \
  ~/.c0wrk/projects/<projectID>/<sessionID>/dumps/session_<id>_llm_dump.jsonl

# Compressed dump (zstd example)
zstdcat ~/.c0wrk/.../session_<id>_llm_dump.jsonl.zst | jq -c \
  'select(.direction == "response") | {ts: .timestamp, usage: .data.usage}'

# Per-call durations from request/response pairs (entries are ordered; the
# timestamp key is `ts`)
zstdcat dump.jsonl.zst | jq -s '
  [group_by(.data.id // input_line_number)] | map(.[0:2]) | map({
    req: .[0].ts, resp: .[1].ts,
    usage: .[1].data.usage
  })'
```

(When a one-liner does not fit a dump's exact shape, extract interactively
with `jq` first and paste the working filter into the measurement log — the
log must contain the filter that produced every quoted number.)

## c0wrk-vs-OpenCode comparison protocol

Goal: same model, same task, same machine — compare agent overhead (prompt
assembly, history management, loop policy) and cache utilization, not model
speed.

1. Pin the backend: both agents pointed at the identical provider endpoint,
   model id, temperature/top-p, max tokens, and reasoning/thinking setting.
2. Same fixture bytes and prompt (above); fresh session/workspace per run;
   check the fixture out anew each run.
3. N ≥ 5 accepted runs per side, alternating c0wrk/OpenCode order to spread
   time-of-day drift; one discarded warm-up per side before the first
   recorded run.
4. Record the per-run checklist for BOTH sides. For OpenCode use its own
   usage/statistics output for tokens; per-call wall-clock from its logs.
   c0wrk's cache fields come from the dump/agent_metrics; OpenCode's from
   whatever its provider reports (map the same wire fields; if OpenCode does
   not surface a field, record "not exposed" — never impute).
5. Compare per metric: median and interquartile range across runs —
   wall-clock, median per-call duration, total input tokens (prompt-size
   proxy), cached-input total, cache-hit %, TTFT proxy, output tokens.
6. Report as one table per metric with per-run rows and the median/IQR
   summary; quote the commit hashes and settings block verbatim.

Confounds checklist (verify before comparing, from the dumps' request
records): serialized system prompt size, tool-schema count and size, history
shape per call (c0wrk compacts, OpenCode may truncate), reasoning/thinking
payload differences, and cache TTL interaction with run pacing (Anthropic's
~5-minute cache window: keep consecutive runs within it or reset the warm-up).

## Using this harness for acceptance

A step's acceptance claims MUST cite: the commit, the number of accepted
runs, and the metric table(s) produced above. A claim without a recorded
table is not evidence. The harness itself is versioned with the code — when
the payload fields or dump format change, update this document in the same
change.

## Recorded evidence — prompt-cache prefix-stability + retry cycle (2026-11-22)

Before/after measurement pass for the sp4rk `1df9c68` cycle (steps: cache
token fields, K-step history-mutation cohorts, Anthropic cache breakpoints;
c0wrk: observability seam, `cohortSize` knob, ADR-134 retry schedule,
continuation routing fast-path). Recorded per the checklist above; every
number below was extracted from on-disk session dumps with the filter
defined in § Extraction filter.

### Environment limits (honest statement)

- **No after-era run exists yet.** The changes are uncommitted c0wrk work on
  top of `e6d1f597`; the running desktop instance predates them. Verified:
  all 178 session dumps on disk carry usage keys
  `{input_tokens, output_tokens}` only, and `session_messages` contains no
  persisted `agent_metrics` row with the step_5 mirror fields (every
  `LIKE` hit is text inside tool results / plan arguments).
- **The fixed-task protocol could not be driven from this environment:**
  the CLI supports only help/version/self-update — every other launch starts
  the Wails UI, and the single-instance gate routes a second launch to the
  running (pre-change) instance. The after table therefore requires a
  rebuild (`make build` with the new pin), an app restart, and harness runs
  per the preparation checklist.
- **Cache-hit % is structurally unrecordable in the before era:** the old
  build never parsed or persisted cache detail — that instrumentation is
  itself part of the change under test. The before table gives latency and
  prefix-size evidence only.
- Provider caveat for the after era: Z.ai's GLM endpoint may not report
  cache detail; if it does not, the cache fields stay empty (`omitempty`)
  and cache-hit evidence for this profile remains absent. Providers that
  verifiably report cache detail (Anthropic cache-read/creation,
  DeepSeek `prompt_cache_hit_tokens`) are the immediate cache-hit targets
  after the rebuild.

### Commit under test

- sp4rk: `1df9c68` `feat(llm,memory,agent): prompt-cache stability,
  Anthropic caching + thinking, usage cache detail` — published; pinned in
  c0wrk `go.mod` as `v0.17.4-0.20261122031032-1df9c6800045`.
- c0wrk: worktree at `e6d1f597` + the cycle's uncommitted changes
  (observability seam, `executor.historyMutation.cohortSize`, ADR-134
  schedule, continuation fast-path, picker/docs). Verification state:
  `make lint` green (gofmt clean, golangci-lint 0 issues, eslint clean);
  `go test -count=1 -skip '^TestStress' ./...` green (all packages `ok`,
  zero FAIL/panic/race in the verbose streams); frontend vitest 491 files /
  7838 tests pass.

### Before baseline — complaining-user profile (`Z-ai/glm-5.3-flash`), real usage

Observational data from real sessions (not the fixed fixture task — see the
environment limits above). Corpus: 178 session dumps, 2026-10-05 →
2026-11-21, all from the pre-change build. Definition of a call: a paired
request/response entry; duration = `response.ts − request.ts` (includes
in-flight engine retries); model read from response entries (older builds
leave the request `model` empty); purpose from the request's `call_purpose`.

| Metric | Full corpus (glm-5.3-flash) | Sessions starting ≥ 2026-11-15 |
| ------ | --------------------------- | ------------------------------ |
| Sessions with ≥1 paired call | 114 | 48 |
| Paired LLM calls | 18,418 | 4,697 |
| Total input tokens | 1,758,851,486 | 441,408,710 |
| Mean input tokens per call | 95,496 | 93,977 |
| Pooled per-call duration — median | 10,626 ms | 8,283 ms |
| Pooled per-call duration — p25 / p75 / p90 | 6,108 / 21,838 / 44,935 ms | — |
| Pooled per-call duration — mean | 21,817 ms | 18,748 ms |
| Median of session medians / mean of session means | 11,172 ms / 23,418 ms | 8,060 ms / 20,483 ms |
| `cached_input_tokens` recorded | **none** (fields absent) | **none** (fields absent) |
| `cache_hit_percent` | **unrecordable** (pre-instrumentation build) | **unrecordable** |

Read of the baseline: with no cache detail ever recorded and ~95k input
tokens re-presented per call on average, every call in the before era paid
full prefix processing per the provider's own accounting; the cohort-mutation
and cache-breakpoint changes target exactly this. The call-purpose mix shows
`executor` 17,408 (94.5%), unspecified 620 (3.4%), **routing 277 (1.5%)**,
`summarization` (titles) 113 (0.6%) — 1.5% is the upper bound of call
volume the continuation routing fast-path can skip; observed first-call
durations in recent sessions range 1,220–6,355 ms, so each skipped routing
call saves a call of roughly that size (plus its tokens). The recent-week
median drop (8.3 s vs 10.6 s) reflects task-mix drift in observational
data — it is NOT evidence of improvement; the changes were not running.

TTFT proxy note: per this document's caveat, dump durations measure
request→response, not provider TTFT; "time to first visible output" remains
a UI-level quantity.

### Extraction filter (produced every number above)

Single pass over `~/.c0wrk/projects/*/*/dumps/*_llm_dump.jsonl[.zst]`
(`zstdcat` for `.zst`): keep entries with `direction` `request`/`response`;
pair by order (`min(len(requests), len(responses))`); parse `ts`
(RFC3339Nano, truncate to µs); session counts a call only when both
timestamps parsed and the session's first non-empty response `model`
equals `glm-5.3-flash`; duration `(resp.ts − req.ts)` in ms; purpose
`request.call_purpose or "(none)"`; era = after iff any response usage
object contains `cached_input_tokens`/`cache_write_tokens`. Zero dumps
satisfied the after-era condition.

### After-side checklist (to close this note)

1. Commit/merge the cycle, rebuild (`make build`), restart the app with
   `log_level: "DEBUG"`.
2. Run the fixed fixture task ≥5× fresh-session against ONE provider;
   record the per-run checklist. Prefer a provider that reports cache
   detail (Anthropic/DeepSeek) for the cache-hit columns; repeat on
   `glm-5.3-flash` for this profile's latency comparison.
3. Cross-check `agent_metrics` totals against the per-call dump table
   (the rule above); only then quote an after table here.
4. With that evidence, decide the deferred `prompt_cache_key`
   (OpenAI-style cache-routing key) question and record the decision.
