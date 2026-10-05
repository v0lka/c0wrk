# Local-change code review: c0wrk and sp4rk

## Summary and scope

**Primary review complete, including the upgrade-collision and interface-contract continuations: 0 MUST FIX, 6 SHOULD FIX, 3 CONSIDER findings.** Independent follow-up verification is a separate goal-loop step; this report does not claim that it has already passed. No fixes were applied.

Reviewed all staged, unstaged, and untracked local changes in both repositories, using current full-file contents, diffs against `HEAD`, affected callers, cross-repository contracts, and tests. Git inventories showed **24 modified tracked files and 4 untracked source/test files in c0wrk**, and **5 modified tracked files in sp4rk**. Neither repository had staged changes. `c0wrk/code-review.md` is the sole additional report created by this task and the only intentionally modified artifact.

## Finding index

| # | Severity | Summary |
|---|---|---|
| 1 | SHOULD FIX | Background profile refresh consumes the one-shot deletion notice |
| 2 | SHOULD FIX | Automatic prompt-cache sizing omits active checkpoint headroom |
| 3 | SHOULD FIX | Documented YAML cache value `-1` is rejected by validation |
| 4 | CONSIDER | SDK regression does not exercise a complete routed tool turn |
| 5 | SHOULD FIX | Authoritative YAML example describes obsolete unset-cache behavior |
| 6 | CONSIDER | Legacy launch descriptions omit explicit checkpoint policy |
| 7 | SHOULD FIX | New predefined Bonsai ID collides with valid pre-upgrade custom profiles |
| 8 | SHOULD FIX | YAML-only exception conflicts with RPC/reset/status completeness claims |
| 9 | CONSIDER | Banner recommends replacing an equivalent generic preset |

## Findings

### 1. SHOULD FIX — Background profile refresh consumes and discards the active-profile deletion notice

**Repository / changed location:** c0wrk, [frontend/src/hooks/useModelProfilesGate.ts:90–97](frontend/src/hooks/useModelProfilesGate.ts#L90-L97).

The new background catalog fetch keeps only `active_id` and `suggested_profile_id`, ignoring warnings. However, `GetModelProfiles` is a consuming read: [backend/frontend_api_config.go:1181–1184](backend/frontend_api_config.go#L1181-L1184) appends pending one-shot notices to its response and clears them. The change introduces a competing consumer that can silently discard a notice intended for Settings.

**Trigger and impact:** With the chat toolbar mounted, delete the active custom profile from Settings. [DeleteModelProfile](backend/frontend_api_config.go#L1627-L1632) records the “deleted; switched to generic” notice, then [applyModelProfilesChange](backend/frontend_api_config.go#L1453-L1456) emits `config:updated`. The hook starts a background refresh. Settings also reloads after deletion ([ModelProfilesSettings.tsx:214–218](frontend/src/components/settings/ModelProfilesSettings.tsx#L214-L218)). If the background getter acquires the backend lock first, it drains the notice without displaying it. Settings receives no explanation of the automatic fallback. The switch succeeds, but its explanatory UI becomes RPC-order dependent.

**Evidence:** Current source establishes both consumers and the notice drain. [frontend_api_config_test.go:2879–2898](backend/frontend_api_config_test.go#L2879-L2898) explicitly asserts that the first getter receives the notice and the second does not; the focused regression passed. The mocked Settings test does not reproduce the competing consumer.

**Suggested fix:**
a) Give the background hook a non-consuming identity read: include active/suggested IDs in `GetConfig`, or expose a dedicated metadata getter. Leave notice consumption with Settings.
b) Centralize catalog synchronization and retain the full response, including notices, until the UI displays or acknowledges them. Add a regression in which background metadata refresh completes before the post-delete Settings reload.

### 2. SHOULD FIX — Automatic prompt-cache sizing spends headroom needed by active checkpoints

**Repository / changed location:** c0wrk, [core/embeddedllm/plan.go:1567–1579](core/embeddedllm/plan.go#L1567-L1579), called for unset `CacheRAMMiB` at [plan.go:1512–1514](core/embeddedllm/plan.go#L1512-L1514).

`planPromptCacheCeiling` assigns all remaining measured host-budget capacity to `--cache-ram`: host budget minus expected host footprint, additionally subtracting expected device footprint on unified memory. The expected footprint includes weights, active context, compute and projector reserves, but no separate allowance for the active slots' checkpoint storage. Thus the automatic ceiling can consume headroom still needed outside the RAM prompt cache.

**Trigger and impact:** On a measured machine with more than 8192 MiB of calculated spare budget, leave `cache_ram_mib` unset and checkpoints enabled. Retain several distinct session/project prompt states until the cache is near its raised ceiling, then run another checkpoint-producing completion. Cached states and active checkpoint vectors coexist. If active checkpoint bytes exceed the remaining cache slack, their combined footprint exceeds the planner's budget and consumes the configured OS/app/vector-index reserve. The previous fixed default could leave substantial spare capacity on such machines; the new automatic calculation spends it.

This is a budget-accounting defect, **not a demonstrated unconditional crash or OOM**. Checkpoints use `PARTIAL_ONLY`; they must not be described as 32 full KV-prefix copies. No live cache-saturation/RSS reproduction was performed.

**Evidence:** The direct footprint chain ([plan.go:1436–1439](core/embeddedllm/plan.go#L1436-L1439), [1476–1479](core/embeddedllm/plan.go#L1476-L1479), [2052–2070](core/embeddedllm/plan.go#L2052-L2070)) does not pass checkpoint storage/count to the footprint estimator. Its underlying base terms are in [memory.go:577–635](core/embeddedllm/memory.go#L577-L635), with relocation/projector terms in [plan.go:2011–2047](core/embeddedllm/plan.go#L2011-L2047).

Pinned-runtime source was inspected at `PrismML-Eng/llama.cpp` tag `prism-b10735-842b188`, verified to resolve to commit `842b1880415d6f508f03b789e5ce70194def7bfd`:

- Each active slot owns a `server_prompt` ([server-context.cpp:249–251](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-context.cpp#L249-L251)), with its own checkpoint list ([server-task.h:566–585](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-task.h#L566-L585)). Creation/retention uses the configured count and `LLAMA_STATE_SEQ_FLAGS_PARTIAL_ONLY` ([server-context.cpp:2228–2248](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-context.cpp#L2228-L2248)). Saved target state allocates host vector bytes ([common.cpp:2296–2312](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/common/common.cpp#L2296-L2312)).
- Cached checkpoint copies **are** included in cached-entry size ([server-task.h:597–609](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-task.h#L597-L609)). Cache size sums cached `states`, not active slots ([server-task.cpp:1691–1699](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-task.cpp#L1691-L1699)). Eviction makes room before cache allocation ([server-task.cpp:1722–1766](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-task.cpp#L1722-L1766)); the problem is not omitted cached copies, but active storage outside that limit.
- Loading a cached prompt removes only the selected entry, not all cached states ([server-task.cpp:1862–1867](https://github.com/PrismML-Eng/llama.cpp/blob/842b1880415d6f508f03b789e5ce70194def7bfd/tools/server/server-task.cpp#L1862-L1867)). Other entries can remain while active checkpoints are retained.

**Suggested fix:**
a) Reserve a validated bound for active checkpoint storage, using resolved checkpoint count and parallel-slot count, before deriving the automatic cache ceiling. Add a regression asserting base footprint + active-checkpoint allowance + automatic cache capacity stays within the host/unified budget.
b) Until a reliable checkpoint-storage bound is available, retain the runtime's default ceiling rather than automatically consuming all headroom; leave larger ceilings as explicit operator choices.

### 3. SHOULD FIX — Newly documented YAML `cache_ram_mib: -1` is rejected by config validation

**Repository / changed location:** c0wrk, [specs/domains/embedded-llm.md:2907](specs/domains/embedded-llm.md#L2907).

The changed configuration table advertises `-1` as a valid YAML value meaning “no limit” and says every explicit value passes through verbatim. In contrast, [backend/config/config.go:1239–1243](backend/config/config.go#L1239-L1243) validates this key with a floor of **0**. [embedded_llm_tuning_test.go:339–342](backend/config/embedded_llm_tuning_test.go#L339-L342) explicitly expects `-1` to be rejected. The prior specification advertised nonnegative values; the contradiction is introduced by this documentation change.

**Trigger and impact:** An operator follows the new table and writes `embedded_llm.tuning.cache_ram_mib: -1`. Config validation fails instead of accepting the documented setting. Core/runtime support for `-1` does not make it reachable through YAML translation.

**Suggested fix:**
a) Restore the documented YAML range to `0..MaxTuningMiB`, clearly distinguishing it from the wider core/runtime vocabulary.
b) If YAML no-limit support is intended, deliberately extend validation to accept exactly `-1` and add config-load/translation tests; update the example consistently.

### 4. CONSIDER — SDK wire regression does not exercise a complete routed tool turn

**Repository / locations:** sp4rk, [llm/router_test.go:1193–1208](../sp4rk/llm/router_test.go#L1193-L1208), [1254–1256](../sp4rk/llm/router_test.go#L1254-L1256), [1276–1278](../sp4rk/llm/router_test.go#L1276-L1278).

The new regression constructs a router but invokes its extracted provider directly. Its assistant history declares `call-1`, then proceeds to another user message without the corresponding tool result. It proves constructor plumbing and outbound field omission, but not a complete multi-turn routed tool exchange.

**Why it matters:** When diagnosing embedded failures after a tool call, a passing narrow wire test does not establish tool-result correlation through `Router.Call`. This is optional test improvement, not a demonstrated production defect.

**Suggested fix:**
a) Invoke the router through `Router.Call`; add a tool-role message with `ToolCallID: "call-1"` before the next user turn and assert content, tool ID/name/arguments, and correlation alongside reasoning-field omission.
b) Keep the narrow test and add a builder → router → HTTP regression for complete tool turns, synchronous and streaming, with empty/non-empty assistant reasoning.

### 5. SHOULD FIX — The authoritative YAML example still describes the old unset-cache behavior

**Repository / locations:** c0wrk, [config.example.yaml:378–379](config.example.yaml#L378-L379) and [512–515](config.example.yaml#L512-L515), in the changed example file.

Both passages say an unset prompt-cache cap keeps the runtime's own default. The changed measured-plan path can instead raise it to spare host/unified memory ([plan.go:1504–1515](core/embeddedllm/plan.go#L1504-L1515), [1567–1582](core/embeddedllm/plan.go#L1567-L1582)); the updated config comment ([config.go:990–995](backend/config/config.go#L990-L995)) and domain table describe that behavior.

**Trigger and impact:** On a measured topology with more than 8192 MiB spare budget, an operator relying on the example would not expect a larger generated `--cache-ram` ceiling. This is documentation drift, distinct from the accounting defect in finding 2.

**Suggested fix:**
a) Update both example passages to explain that unset retains the runtime default unless the measured planner raises it, distinguishing the RAM-only fallback that still omits the flag. Keep the description aligned with whichever policy is chosen to address finding 2.

### 6. CONSIDER — Legacy launch descriptions omit the now-explicit checkpoint policy

**Repository / locations:** c0wrk, [specs/domains/embedded-llm.md:2013–2028](specs/domains/embedded-llm.md#L2013-L2028) and [2985](specs/domains/embedded-llm.md#L2985).

The new policy paragraph says every launch explicitly emits checkpoint count and idle-slot caching policy, but the load command example omits those switches. Both legacy descriptions still broadly say pre-plan launches leave “the cache knobs” to runtime defaults.

**Trigger and impact:** An operator diagnosing a legacy manifest or inherited `LLAMA_ARG_*` settings could expect the runtime/environment to choose these policies. Current production code supplies 32/true for a plan-less launch ([server.go:1686–1687](core/embeddedllm/server.go#L1686-L1687)); argv unconditionally includes checkpoint count and exactly one idle-slot switch ([server.go:712–720](core/embeddedllm/server.go#L712-L720)). This is documentation drift directly affected by the change, not an additional production defect.

**Suggested fix:**
a) Add `--ctx-checkpoints N` and the resolved idle-slot switch to the command example. In both legacy descriptions, distinguish the omitted cache-size/KV/device/split settings from the explicitly pinned checkpoint and idle-slot policy.

### 7. SHOULD FIX — New predefined Bonsai ID invalidates previously valid custom profiles on upgrade

**Repository / changed location:** c0wrk, [backend/config/model_profiles.go:377–400](backend/config/model_profiles.go#L377-L400), the new predefined `bonsai-2-27b` entry.

The new preset claims an ID in the existing custom-profile namespace without migrating previously saved records. In `HEAD`, creating a custom profile named **Bonsai 2 27B** derives the ID `bonsai-2-27b`: the unchanged creation path checks only the then-current predefined IDs/names, and the slug generator lowercases the name and replaces separators ([model_profiles_store.go:222–250](backend/config/model_profiles_store.go#L222-L250), [329–340](backend/config/model_profiles_store.go#L329-L340)). The five-entry `HEAD` catalog did not reserve that ID. This is a valid pre-upgrade state, not an operator-authored malformed store.

**Trigger and impact:** Upgrade with that custom profile saved in `model-profiles.yaml`. Loading pre-seeds the ID namespace with the new predefined catalog, rejects the formerly valid custom entry, and skips it with a warning ([model_profiles_store.go:91–105](backend/config/model_profiles_store.go#L91-L105), [306–307](backend/config/model_profiles_store.go#L306-L307)). The custom profile disappears from the loaded catalog. If `model_profiles.active_profile` is `bonsai-2-27b`, the same ID now resolves to the new predefined values rather than the operator's custom tuning ([config.go:2372–2396](backend/config/config.go#L2372-L2396), [2404–2407](backend/config/config.go#L2404-L2407)); with the master toggle enabled, those different values take effect. The load warning does not prevent that identity substitution.

The on-disk bytes are preserved, but normal custom-profile writes are then blocked: `ensureStoreWritable` revalidates the existing file against the enlarged catalog and rejects the collided record before any write ([model_profiles_store.go:125–161](backend/config/model_profiles_store.go#L125-L161), [172–175](backend/config/model_profiles_store.go#L172-L175)). Creating another custom profile or editing a remaining custom profile reaches that guard ([frontend_api_config.go:1509–1518](backend/frontend_api_config.go#L1509-L1518), [1573–1579](backend/frontend_api_config.go#L1573-L1579)). Editing the collided ID itself is rejected as predefined/read-only ([frontend_api_config.go:1540–1541](backend/frontend_api_config.go#L1540-L1541)). The user must repair the file externally to recover ordinary CRUD in the absence of a migration. This is a compatibility/tuning-preservation defect, not a claim of on-disk data loss.

**Evidence:** Current full store source, catalog diff against `HEAD`, pre-change creation/slug source, active-profile resolution and CRUD callers were inspected. The existing collision regression explicitly expects colliding records to be dropped ([model_profiles_store_test.go:205–238](backend/config/model_profiles_store_test.go#L205-L238)); it does not distinguish an invalid original record from a formerly valid record made invalid by catalog growth. The inspected load/save/resolution path contains no upgrade migration. No new reproduction test or source fix was written during this report-only continuation.

**Suggested fix:**
a) Before merging the new catalog, migrate collided legacy custom IDs to unused IDs and update any persisted active-profile reference together, preserving names and all tuning values. Back up the old store and make partial migration failures recoverable. Add an upgrade fixture with `bonsai-2-27b` active, asserting preserved tuning and successful create/update/delete afterward.
b) Introduce a distinct predefined-ID namespace that the existing custom slug generator cannot produce, use it for the new preset and its banner/suggestion callers, and retain legacy custom IDs unchanged. Validate both identity resolution and store writability against a pre-upgrade Bonsai custom-profile fixture; do not weaken the existing fail-closed writer to silently prune the old record.

### 8. SHOULD FIX — New YAML-only tuning exception leaves RPC and status completeness contracts inaccurate

**Repository / changed location:** c0wrk, [specs/domains/embedded-llm.md:1651](specs/domains/embedded-llm.md#L1651), the added checkpoint/idle-slot policy and explicit frontend exception; affected status contract at [2989](specs/domains/embedded-llm.md#L2989).

The new paragraph intentionally says `ctx_checkpoints` and `cache_idle_slots` do not add frontend tuning DTOs or controls. That exclusion is not reflected in the existing completeness claims: [EmbeddedLLMTuningDTO:477–484](backend/frontend_api_embedded_dto.go#L477-L484) says it mirrors persisted overrides “field for field,” and [embeddedTuningDTO:682–708](backend/frontend_api_embedded_dto.go#L682-L708) repeats that claim while copying neither key. The reset contract describes the YAML key of “every knob,” but its accepted twelve-key list omits both ([frontend_api_embedded_tuning.go:39–74](backend/frontend_api_embedded_tuning.go#L39-L74)). Separately, the domain spec says `EmbeddedLLMStatus.Plan` receives “every flag-bearing value,” whereas [embeddedPlanDTO:623–680](backend/frontend_api_embedded_dto.go#L623-L680) copies neither resolved value. The new exception names **tuning DTOs and controls**, not the separate resolved-plan/status DTO.

**Trigger and impact:** A caller or maintainer follows the documented full-snapshot/reset contract after configuring either new YAML key. The tuning snapshot omits the stored value, a reset naming that key is refused as unknown, and the structured plan cannot show its effective flag value despite the status promise. This is a change-induced interface/documentation inconsistency and maintenance hazard, not a demonstrated runtime failure. It does **not** establish that new UI controls are required; YAML-only tuning is expressly intended. It is distinct from finding 6's legacy argv-example drift.

**Evidence:** Direct current-source reads confirmed the getter/request/mapper surfaces, reset vocabulary and unknown-key rejection, the explicit exception, and the status completeness claim. `git diff HEAD -- specs/domains/embedded-llm.md` confirms the exception is newly added. The independently recovered `fresh_docs_completion` result identifies the same discrepancy and explicitly limits its conclusion to the tuning exception rather than inferring a status exception.

**Suggested fix:**
a) Qualify getter/mapper/reset contracts as covering **UI-exposed tuning knobs**, explicitly name both YAML-only exceptions, and document whether resolved checkpoint/idle-slot values are intentionally absent from status. If so, replace “every flag-bearing value” with the actual status DTO scope.
b) Keep tuning edits YAML-only but expose the two resolved values as read-only plan/status fields if complete launch-plan observability is intended; align the spec and mapping tests. This requires a deliberate contract choice, not automatically adding editing controls.

### 9. CONSIDER — Banner recommends replacing an equivalent generic preset

**Repository / changed locations:** c0wrk, [BonsaiProfileBanner.tsx:19–20](frontend/src/components/chat/BonsaiProfileBanner.tsx#L19-L20) and [30](frontend/src/components/chat/BonsaiProfileBanner.tsx#L30).

The banner excludes only the named Bonsai profile, then tells users that Bonsai “works best with the Bonsai profile.” The new Bonsai configuration and the generic configuration use identical values and shared helper expressions ([model_profiles.go:377–426](backend/config/model_profiles.go#L377-L426)).

**Trigger and impact:** Enable Model Profiles, keep `generic` active, and select embedded Bonsai. The warning recommends switching profiles even though this changes no tuning values. This is redundant/misleading advice, not a demonstrated execution defect.

**Evidence:** The latest independent pass identified this optional observation; direct current-source reads of the complete banner and both catalog definitions corroborated it.

**Suggested fix:**
a) Suppress the warning for configurations equivalent to the Bonsai preset, including the current generic preset.
b) Rephrase it as a named-preset suggestion without implying an effective performance difference between identical configurations.

## Review coverage

Full current contents of all changed source, test, configuration and specification files were read across the primary-review fronts; diffs alone were not used as sufficient context. Previously incomplete large-file reads were explicitly completed by continuation reviewers. The four untracked source/test files were read in full.

| Front | Files covered |
|---|---|
| Embedded planning and compatibility | `core/embeddedllm/{limits.go,plan.go,plan_test.go,plan_json.go,plan_json_test.go}` |
| Embedded launch/config | `core/embeddedllm/{resolve.go,server.go,server_test.go}`, `backend/config/{config.go,embedded_llm_tuning_test.go}`, `config.example.yaml`, `specs/domains/embedded-llm.md` |
| Model profiles/backend | `backend/config/{model_profiles.go,model_profiles_test.go}`, `backend/{frontend_api_config.go,frontend_api_config_test.go}`, `backend/session/agent_metrics_test.go`, `specs/domains/model-profiles.md` |
| Frontend profiles/banner | `frontend/src/components/layout/{AppLayout.tsx,AppLayout.test.tsx}`, `frontend/src/components/chat/{BonsaiProfileBanner.tsx,BonsaiProfileBanner.test.tsx}`, `frontend/src/hooks/{useModelProfilesGate.ts,useModelProfilesGate.test.ts}`, `frontend/src/stores/modelProfilesGateStore.ts`, `specs/domains/frontend/stores.md` |
| Cross-repository provider contract | c0wrk `core/{builder.go,builder_embedded_transport_test.go}`; sp4rk `llm/{provider_openai.go,provider_openai_test.go,router.go,router_test.go}`, `specs/domains/llm-providers.md` |

Affected integration paths traced included install planning/manifest persistence; load-time replanning and recorded-plan/legacy fallback; tuning translation, launch validation and argv; post-ready context/plan persistence; profile suggestion/selection/gating/metrics and Settings deletion; and builder → router entry → OpenAI provider request encoding. Outbound reasoning omission is guarded by the supervised embedded-provider seam; response-side reasoning capture remains unchanged. Missing legacy checkpoint fields default to 32/true, while explicit zero/false survive. The new checkpoint YAML knobs intentionally have no frontend tuning DTOs/controls; this is not a reported defect.

Changed-file graph metadata was stale. Coverage was checked and current source was treated as authoritative. Relevant embedded-runtime security boundaries and typed/bounded launch requirements were considered; no introduced weakening was substantiated in the inspected launch changes.

## Checks observed during the primary review

These are primary-review checks, including delegated results, **not independent goal verification**:

- Embedded/config focused tests: 179 embeddedllm and 76 config test/subtest executions passed with no failures/skips. The planner continuation separately passed 179 focused executions; its only non-runner output was an informational KV-escalation test message.
- Broader embedded/config tests: `go test -count=1 -json ./core/embeddedllm ./backend/config` reported 1,947 passing test/subtest executions, no failures, and 9 explicit platform/helper skips. Non-runner output contained informational messages and skip reasons, not warnings/errors.
- Backend embedded integration: `go test -count=1 -json ./backend -run 'Test.*Embedded'` reported 203 passing executions, no failures/skips or non-runner diagnostics.
- Profile/UI: 3 focused frontend files, 30 tests passed without reported warnings/errors; focused uncached Go profile/catalog/suggestion/preservation/metrics and deletion-notice regressions passed. The remaining-context continuation passed 74 focused Go executions across backend/config/session with no failures/skips or warnings/errors.
- SDK: focused reasoning/conversion/router plumbing tests ran uncached with race detection and passed; six c0wrk embedded provider-entry tests passed.
- Scoped formatting, vet and diff whitespace checks passed. Vet scopes included sp4rk `./llm` and `./...`, and c0wrk core/backend/config/session/embeddedllm across review fronts.
- Installed runtime `--version` reported build 10735, commit `842b18804`; `--help` confirmed an 8192 MiB cache default, 32 checkpoints, and idle-slot caching enabled. Relevant pinned source was inspected for finding 2.

**Limits:** No full repository suite, full build/lint/stress, vulnerability scan, standalone release build, or live multi-gigabyte generation/cache-saturation/RSS validation was completed. Focused passing checks do not establish absence of all defects. The checkpoint finding is grounded in allocation/accounting source, not a measured OOM. Independent follow-up must compare the same change scope against this report; it has not been self-certified by the primary reviewer.

## Reviewed-file integrity

All review operations were read-only except creating/updating this report. Inventories retained the original modified/untracked source files, with no staged changes or Git state-changing commands. The complete c0wrk tracked diff against `HEAD` retained SHA-256 `5645eae7c0e4bf9290720fbb7df59a8c6849bed65818598659867ac5d4d3e5d2` across the continuation. The observed sp4rk tracked-diff SHA-256 was `df159ceacda3af5a2c870d4278ffa2896892b413226284c3928486c8d40d7eae`. No reviewed source, test, config or specification was fixed by this task.

## Fresh additional review after the completeness rejection

A fresh, disjoint read-only review covered all **33 changed source/test/config/specification files** (28 c0wrk, 5 sp4rk), including all four untracked source/test files. Its findings and subsequent substantiated continuations are consolidated in the numbered findings above; no separate unreported mandatory finding is asserted. The optional legacy-launch documentation observation is finding 6. This is additional review evidence, not a substitute for the independent goal-loop verifier's verdict.

The initial launch partition explicitly reported incomplete reads. Three separate continuation reviewers then closed those exact gaps; the initial incomplete result alone is not credited as exhaustive. Saved review outputs `fresh_planner`, `fresh_launch`, `fresh_profiles`, `fresh_provider`, `fresh_server_tests_tail`, `fresh_config_tail`, and `fresh_spec_tail` contain the contiguous pagination/truncation-recovery and affected-caller ledgers. All report no file modifications or Git mutations.

### Complete current-source reading ledger

Every interval below reaches the current file's last line; pagination and truncated-tail recovery are documented in the saved outputs.

| Repository / file | Complete line coverage | Fresh review output |
|---|---:|---|
| c0wrk `core/embeddedllm/limits.go` | 1–67 | `fresh_planner` |
| c0wrk `core/embeddedllm/plan.go` | 1–2149 | `fresh_planner` |
| c0wrk `core/embeddedllm/plan_test.go` | 1–1832 | `fresh_planner` |
| c0wrk `core/embeddedllm/plan_json.go` | 1–21 | `fresh_planner` |
| c0wrk `core/embeddedllm/plan_json_test.go` | 1–68 | `fresh_planner` |
| c0wrk `core/embeddedllm/resolve.go` | 1–927 | `fresh_launch` |
| c0wrk `core/embeddedllm/server.go` | 1–2893 | `fresh_launch` |
| c0wrk `core/embeddedllm/server_test.go` | 1–5361 | `fresh_launch` + `fresh_server_tests_tail` |
| c0wrk `backend/config/config.go` | 1–3285 | `fresh_launch` + `fresh_config_tail` |
| c0wrk `backend/config/embedded_llm_tuning_test.go` | 1–944 | `fresh_launch` |
| c0wrk `config.example.yaml` | 1–1794 | `fresh_launch` |
| c0wrk `specs/domains/embedded-llm.md` | 1–3013 | `fresh_launch` + `fresh_spec_tail` |
| c0wrk `backend/config/model_profiles.go` | 1–447 | `fresh_profiles` |
| c0wrk `backend/config/model_profiles_test.go` | 1–554 | `fresh_profiles` |
| c0wrk `backend/frontend_api_config.go` | 1–2388 | `fresh_profiles` |
| c0wrk `backend/frontend_api_config_test.go` | 1–5517 | `fresh_profiles` |
| c0wrk `backend/session/agent_metrics_test.go` | 1–366 | `fresh_profiles` |
| c0wrk `frontend/src/components/layout/AppLayout.tsx` | 1–213 | `fresh_profiles` |
| c0wrk `frontend/src/components/layout/AppLayout.test.tsx` | 1–108 | `fresh_profiles` |
| c0wrk `frontend/src/components/chat/BonsaiProfileBanner.tsx` | 1–33 | `fresh_profiles` |
| c0wrk `frontend/src/components/chat/BonsaiProfileBanner.test.tsx` | 1–118 | `fresh_profiles` |
| c0wrk `frontend/src/hooks/useModelProfilesGate.ts` | 1–135 | `fresh_profiles` |
| c0wrk `frontend/src/hooks/useModelProfilesGate.test.ts` | 1–423 | `fresh_profiles` |
| c0wrk `frontend/src/stores/modelProfilesGateStore.ts` | 1–51 | `fresh_profiles` |
| c0wrk `specs/domains/frontend/stores.md` | 1–184 | `fresh_profiles` |
| c0wrk `specs/domains/model-profiles.md` | 1–330 | `fresh_profiles` |
| c0wrk `core/builder.go` | 1–3317 | `fresh_provider` |
| c0wrk `core/builder_embedded_transport_test.go` | 1–307 | `fresh_provider` |
| sp4rk `llm/provider_openai.go` | 1–1144 | `fresh_provider` |
| sp4rk `llm/provider_openai_test.go` | 1–2572 | `fresh_provider` |
| sp4rk `llm/router.go` | 1–728 | `fresh_provider` |
| sp4rk `llm/router_test.go` | 1–1287 | `fresh_provider` |
| sp4rk `specs/domains/llm-providers.md` | 1–296 | `fresh_provider` |

The server-test continuation read 181–1689 and 2940–4489; the launch reviewer read all other intervals. The config continuation read 181–900 and 1361–3285; the launch reviewer read 1–180 and 901–1360. The specification continuation read 1–2889 and 2920–3013, recovering all truncated prose; the launch reviewer fully observed 2890–2919. These unions leave no changed-file whole-reading gap.

### Fresh affected-context and check evidence

Reviewers traced measured install/manifest persistence; load-time replanning, recorded-plan and legacy fallback; launch validation/argv and post-ready persistence; config translation/validation/preservation; profile Settings and toolbar competing getters, cache invalidation, config events, session metrics and locking; and c0wrk builder/seam → SDK router → synchronous/streaming provider encoding → executor history/response reasoning. Supporting callers were read in relevant ranges, not exhaustively in unrelated code. Their exact ranges are in the saved outputs.

- Planner: 105 focused planner/JSON/footprint/resolve/bridge/load test/subtest passes, 76 tuning passes, and 4 load-fallback passes; no failures/skips, with one informational KV-escalation test message. Scoped vet/format/whitespace checks passed.
- Launch: 126 focused embedded/config passes, no failures/skips; scoped vet/format/whitespace checks passed.
- Profiles: 73 focused Go test/subtest passes plus three package passes, and 58 frontend tests across four files; no failures/skips or warnings/errors. Scoped vet/format/whitespace checks passed.
- Provider: uncached race-enabled SDK reasoning/entry/streaming tests and c0wrk embedded entry/gate/precedence tests passed; scoped SDK/core vet, formatting and whitespace checks passed.
- Continuations: source-reading completion and relevant contract checks; no new tests or live model/cache/RSS experiments. No full suite/build/lint/stress/vulnerability scan was performed.

### Integrity evidence and its limits

Fresh initial inventories and tracked-diff fingerprints matched those already recorded above. All reviewers used read-only operations and returned explicit no-modification statements. Only this report was edited by the conductor. Current untracked-source SHA-256 values are:

| c0wrk file | SHA-256 |
|---|---|
| `core/embeddedllm/plan_json.go` | `f865ca4ddddab9dbd4e4f8f6f47bd6ed9d7790727f2cc18f4f444d4ae59f5ee8` |
| `core/embeddedllm/plan_json_test.go` | `d3e824abbc223727e4ce1ef29b14b134698e69e001f9f6348dac4609be489b6b` |
| `frontend/src/components/chat/BonsaiProfileBanner.tsx` | `7d430ec2e0c678f838d72b3c9be9a90e7e0a2529e9a503c1908adba00179179d` |
| `frontend/src/components/chat/BonsaiProfileBanner.test.tsx` | `50b1f1bc3ff3b1306ae855e7730f7dc99889411a76a12c84880bb1b7a5340bc0` |

A separately recorded pre-task byte-hash baseline was not available. Historical **full-content reads from the original review** have now been recovered, as detailed below; these establish byte identity from those early reads onward. They are not represented as hashes captured before the task began.

### Recovered historical execution and untracked-content evidence

The preservation evidence gap raised by independent verification was investigated without changing reviewed files. The session's persisted execution artifacts are available locally under:

`/Users/vkochetkov/.c0wrk/projects/3908f983-dfcf-469f-9151-ab5e8f00ee9e/cfd54ac4-d644-4be2-913f-f18ad09986a3/`

- `logs/session_cfd54ac4-d644-4be2-913f-f18ad09986a3.log` records executed tool calls from the initial review onward. Parsing the JSON records whose `msg` is `executor: tool call` found **two `write_file` and three `edit_file` executions**, and no other write/edit/delete/create/push tool executions in the audited log.
- `dumps/session_cfd54ac4-d644-4be2-913f-f18ad09986a3_llm_dump.jsonl` retains their arguments in request-history `data.messages[].tool_calls[]` (`name`, `input`, `id`). The two writes occurred at **2026-10-04 19:17:53 and 20:09:35 UTC**; the three edits at **21:47:15, 21:47:33 and 21:48:40 UTC**. Every target was `/Users/vkochetkov/Repositories/c0wrk/code-review.md`.
- The main dump and **25 per-step dumps** under `dumps/steps/` were scanned for tool calls, including nested batch/parallel inputs. The five distinct file-mutation calls above were the only such calls found. Recovered shell calls comprise review reads/diff/status/hash/coverage calculations, non-writing `gofmt -d/-l`, vet/tests, runtime version/help probes, public pinned-source reads and verifier waits; no source-writing shell or Git state-changing command was found. Tests can create runtime/temp/cache artifacts; this statement concerns preservation of the reviewed repository files, not a claim that tests perform no filesystem writes anywhere.

The earliest full `read_file` results for all four untracked source/test files predate the first report write. They survive in the original delegated request histories, not merely in current hashes or reviewers' declarations:

| File | Original artifact | Captured at (UTC, 2026-10-04) | Full content size | Comparison with current file |
|---|---|---|---:|---|
| `core/embeddedllm/plan_json.go` | `dumps/steps/step_embedded_review.jsonl` | 19:01:18.442261 | 759 bytes / 21 lines | Exact byte match; SHA-256 matches the table above |
| `core/embeddedllm/plan_json_test.go` | `dumps/steps/step_embedded_review.jsonl` | 19:02:30.523224 | 2163 bytes / 68 lines | Exact byte match; SHA-256 matches the table above |
| `frontend/src/components/chat/BonsaiProfileBanner.tsx` | `dumps/steps/step_profile_review.jsonl` | 19:01:02.469038 | 1725 bytes / 33 lines | Exact byte match; SHA-256 matches the table above |
| `frontend/src/components/chat/BonsaiProfileBanner.test.tsx` | `dumps/steps/step_profile_review.jsonl` | 19:01:48.346646 | 4970 bytes / 118 lines | Exact byte match; SHA-256 matches the table above |

**Reproduction:** Parse each JSONL record, map request-history `tool_calls[].id` to its `name`/`input`, then correlate messages with `role: tool` by `tool_call_id`. Select the earliest `read_file` result for each path. Its content has a `[File: … | Lines 1-N of N | B bytes]` header. Take the next **B UTF-8 bytes**, excluding the wrapper/cache footer, and compare with the current file's raw bytes; hash those extracted bytes independently. The observed comparisons all returned true. The initial inventories in the same histories already list these four files as untracked, before the report existed.

This historical content evidence, the log/dump agreement on report-only mutations, and unchanged tracked-diff fingerprints provide inspectable support for source preservation. The early captures are minutes after task start, so the execution audit—not an invented pre-start snapshot—covers that preceding interval. No new code-review defect is claimed here, and this evidence-recovery pass does not substitute for independent verification.

## Independent follow-up status

**Additional full-source review coverage is complete across all 33 changed files; the independent goal-loop verdict remains separate.** The recovered whole-scope independent pass (`step_fresh_independent_review.jsonl`) supported all six SHOULD FIX findings and identified the optional equivalent-preset observation now recorded as finding 9. It reported 12 completely read changed files and 21 incomplete files. Separate read-only continuations completed all five SDK files (`sdk_followup_complete`), four c0wrk profile-test/specification files (`profile_small_followup`), two resolution/tuning-test files (`resolve_tuning_followup`), config (`config_whole_followup`), builder (`builder_whole_followup`), both frontend configuration API files (`frontend_config_whole_followup`), and the final six planner/server/documentation files detailed below. None established a new substantiated MUST FIX or SHOULD FIX finding absent from this report. These are additional primary-review artifacts, not an independently issued whole-goal approval.

The 12 completed files in the whole-scope pass were the model-profile catalog implementation, builder embedded transport test, embedded limits and both JSON files, both banner files, both layout files, both profile-hook files, and profile gate store. The SDK follow-up independently read `llm/provider_openai.go` 1–1144, `llm/provider_openai_test.go` 1–2572, `llm/router.go` 1–728, `llm/router_test.go` 1–1287, and `specs/domains/llm-providers.md` 1–296: 6,027 lines in contiguous chunks of at most 180 lines, recovering truncated specification text. It inspected the complete SDK HEAD diff and empty index, and returned **no additional substantiated MUST FIX or SHOULD FIX findings**. Finding 4's existing router-test limitation remains documented; it was not elevated to a demonstrated production defect.

The SDK follow-up also checked c0wrk builder ranges 1460–1505, 1790–1969 and 2341–2520, builderconfig 765–845, and the complete embedded transport test; SDK executor-run ranges 121–280, 806–925, 970–1080 and 1160–1245, types 1–70, complete executor streaming tests, complete memory steps, memory context 600–735 and context tests 1–130. Focused uncached race-enabled SDK provider/reasoning/conversion, executor streaming and memory-reasoning tests passed; six c0wrk embedded provider-entry tests passed. SDK changed-file formatting, `go vet ./llm` and HEAD whitespace checks were clean. One existing informational SDK test log appeared; no test warning, error or skip was reported. No files were modified by that reviewer, and the SDK tracked-diff fingerprint remained unchanged.

The separate `profile_small_followup` reviewer completely read `backend/config/model_profiles_test.go` 1–554, `backend/session/agent_metrics_test.go` 1–366, `specs/domains/model-profiles.md` 1–330, and `specs/domains/frontend/stores.md` 1–184, using contiguous chunks of at most 180 lines and recovering truncated specification text. It inspected complete scoped HEAD diffs and the empty index, returning **no new substantiated MUST FIX or SHOULD FIX finding**. It corroborated existing findings 1, 7 and 9 without escalating the optional preset observation. Affected context included catalog 255–447, profile resolution 2360–2420, store collision guards 60–175, config adapter 1–80, frontend config RPCs 1145–1320 and 1450–1635, deletion-notice tests 2850–2900, complete session metrics implementation 1–172, manager 500–575, builder 3044–3185, complete gate hook/store/banner and hook tests, settings 60–245, and relevant security rules. No tests or other automated code checks were run by this partition; no files were modified. Its own observations—not prior primary ledgers—support the four-file coverage completion.

The separate `resolve_tuning_followup` reviewer completely read `core/embeddedllm/resolve.go` 1–927 and `backend/config/embedded_llm_tuning_test.go` 1–944 in contiguous chunks of at most 150 lines, with no unrecovered truncation. It inspected complete scoped HEAD diffs and empty staged diffs, returning **no new evidence-backed introduced MUST FIX or SHOULD FIX findings**. Own caller/contract reads included config translation/validation 1121–1250, measured planner/checkpoint helper 1401–1550, server argv/plan mapping 701–795 and launch selection 1646–1695, plus relevant security rules. It confirmed default/explicit-zero/explicit-false propagation in the inspected chain without claiming full-file readings of those callers or independently re-proving all existing findings. Scoped HEAD whitespace checks passed, status was unchanged, and no files were modified. No tests/vet/build/live inference were run. This is additional bounded review research, not whole-goal verification.

The separate `config_whole_followup` reviewer completely read `backend/config/config.go` 1–3285 in 22 contiguous chunks of at most 150 lines, and re-read `backend/config/embedded_llm_tuning_test.go` 1–944 in seven such chunks, with no truncation. It inspected complete HEAD diffs for config, tuning tests, model-profile catalog/tests and frontend config RPCs; scoped staged diffs were empty. It returned **no new substantiated introduced MUST FIX or SHOULD FIX findings**. Its affected-context reads included the complete tuning RPC implementation 1–425 and config adapter 1–371, live tuning caller 680–729, store collision guards 60–175 and creation/slug paths 220–340, catalog/clone helpers 350–447, profile getter/suggestion/gate 1171–1320 and selection 1635–1720, relevant profile tests, planner 1401–1550, and server argv/plan mapping 701–795 and legacy launch 1646–1695. It corroborated pointer cloning, YAML-only-field preservation in the partial-update fold, bounded checkpoint validation, and explicit zero/false propagation. `go test -count=1 -v ./backend/config -run '^TestEmbeddedLLMTuning'` passed with no observed warning, error or skip; `go vet ./backend/config`, scoped `gofmt -d` and HEAD whitespace checks produced no diagnostics. No files were edited. Stale graph metadata was checked against current source; caller ranges do not claim whole-file coverage. No full-suite/build/lint/stress/vulnerability scan or live inference/RSS experiment was performed. This is additional bounded review research, not whole-goal verification.

The separate `builder_whole_followup` reviewer completely read `core/builder.go` 1–3317 in 23 contiguous chunks of at most 150 lines, with no truncation. It inspected the complete builder/transport-test HEAD diff and empty index, returning **no new substantiated introduced MUST FIX or SHOULD FIX findings**. Own complete supporting reads included embedded transport tests 1–307, embedded gate tests 1–210, builder budget tests 1–625, transport implementation 1–590 and prompt RPC 1–54. Targeted caller/contract ranges covered builderconfig 765–845, application construction/session factory 90–220 and 250–340, embedded entry/readiness/service gates 2000–2210 and 2220–2247, title and commit service gates, and SDK router/provider constructor, shared request builder and history encoding. It verified supervised-entry omission plumbing, sibling/inert zero-value policy, preserved tool-call fields and readiness-before-request-budget ordering; finding 4 remains optional. Uncached focused `go test ./core` passed 18 top-level tests plus four seam subtests without observed warning/error/skip; `go vet ./core`, scoped formatting and whitespace checks were silent. Source/index inventories, both tracked-diff fingerprints and all four untracked-source hashes were unchanged; no files were modified. Caller ranges are targeted, not whole-file coverage; no full suite/build/lint/stress/vulnerability scan or live inference/RSS experiment was performed. This is additional bounded primary-review research, not independent whole-goal verification.

The separate `frontend_config_whole_followup` reviewer completely read `backend/frontend_api_config.go` 1–2388 in 16 contiguous chunks of at most 150 lines and `backend/frontend_api_config_test.go` 1–5517 in 37 such chunks, without source truncation. It read the complete report with cached prose recovery and inspected complete scoped HEAD and empty staged diffs, returning **no new substantiated introduced MUST FIX or SHOULD FIX findings**. The changed embedded-model alias resolves to the existing Bonsai preset; target-existence/non-generic guards and hint-only getter behavior remain intact. Own supporting reads covered catalog 350–447, Settings 60–410, complete gate hook/store/banner, frontend API/response-guard ranges, SDK model IDs 1–51 and registry 2210–2258, and relevant security rules. Existing findings 1, 7 and 9 already cover the related known issues. Uncached verbose focused backend tests passed seven top-level tests and seven subtests with no observed warning, error or skip; `go vet ./backend`, scoped `gofmt -d` and whitespace checks were silent. Initial/final hashes of both source files and the report matched; the complete c0wrk tracked HEAD-diff hash remained unchanged. No files were modified by the reviewer. Supporting caller ranges are targeted, not whole-file coverage; no full suite/build/lint/stress/vulnerability scan, race run or live inference/RSS experiment was performed. This is bounded additional primary review, not independent whole-goal verification.

The final six-file reading queue is closed by the following separate read-only primary-review partitions. Each checked current scoped local diffs and an empty index, read the existing findings, and reported **no new substantiated introduced MUST FIX or SHOULD FIX issue absent from the report**. Earlier incomplete attempts are not credited as complete.

- **`server_whole_followup`:** Personally read `core/embeddedllm/server.go` 1–2893 in 20 contiguous chunks and `server_test.go` 1–5361 in 36 chunks, each at most 150 lines; neither source read was truncated. Supporting diff/report/security truncations were recovered. Own context included complete limits and JSON implementation/tests; planner 119–144 and 1401–1550; resolver 451–580; planner tests 472–653; installer 110–290; config 961–1040 and 1121–1250; tuning tests 551–680; embedded API 401–550 and 680–729; tuning RPC 231–298; and specification policy ranges. It checked typed bounded argv, explicit 32/true legacy defaults, plan mapping and missing-versus-explicit JSON policy. Focused uncached embedded tests passed 107 test/subtest executions plus the package; two config translation/cloning tests passed, with no observed warning/error/skip. Scoped formatting, vet and whitespace checks were silent. Initial/final server, test and report hashes matched; no edits were performed.
- **`embedded_docs_whole_followup`:** Personally read `config.example.yaml` 1–1794 and `specs/domains/embedded-llm.md` 1–3013 in contiguous windows of at most 150 lines. Every truncated specification range and dense lines 24/48 were recovered; no document gap remains. Own affected-source reads covered config 1121–1250; planner 1401–1600; resolver 451–600; server validation/argv/legacy/readback ranges; complete limits/JSON files; planner tests 472–820; server tests 2583–2834; config tuning cases; DTO/reset/desktop-frontend contracts; builder/seam tests; catalog/suggestion mapping; and SDK provider/router encoding/test ranges. It rechecked the pinned runtime tag and installed version/help, not a live generation or binary-integrity audit. Focused uncached embedded/config tests passed 136 test/subtest executions plus two packages, with no observed warning/error/skip; scoped vet, formatting and document whitespace checks were silent. No files were edited.
- **Planner implementation:** `planner_whole_followup` personally read `plan.go` 1–150 and complete scoped local diffs but explicitly reported its remaining work incomplete. The separate **`planner_impl_tail_followup`** personally read 151–2149 in fourteen contiguous requests of at most 150 lines with no truncation. Together those own observations cover the complete current 2149-line file; the incomplete initial attempt is credited only for 1–150. The tail reviewer read the complete report, current implementation/test diffs with cached recovery, resolver 386–570, server 701–800 and 1646–1700, config 1121–1255, complete JSON implementation/tests, planner tests 482–631 and 669–818, and server bridge tests 2730–2868. Seven focused top-level tests plus thirteen subtests passed without observed warning/error/skip; scoped vet, formatting and whitespace checks were silent. No files were edited.
- **`planner_tests_whole_followup`:** Personally read `plan_test.go` 1–1832 in thirteen contiguous requests of at most 150 lines, all untruncated, and the complete report/local test diff with prose/diff recovery. Own context included planner relaxation/budget/cache ranges 1132–1280, 1401–1600 and 1657–1730; complete limits/JSON files; resolver 401–550; server 580–610, 701–795 and 1646–1900; server bridge tests 2730–2868; config 1121–1250; tuning tests 550–680; and relevant security rules. Six focused planner/JSON top-level tests plus thirteen subtests, one bridge test, and two config tests passed without observed warning/error/skip. Scoped formatting, vet and whitespace checks were silent. No files were edited.

All final reviewers checked stale MCP coverage against current source rather than treating the graph as exhaustive. Their caller reads are targeted ranges, not claims to have read unrelated files fully. None ran a full suite/build/lint/stress/vulnerability scan, live cache-saturation/RSS experiment or race-enabled run. Exact pagination, caller ranges, commands, results and limits remain inspectable in the named saved outputs. The completed union is **33/33 changed files**; an independent verifier must issue its own verdict rather than crediting these primary-review ledgers as its own observations.

Subsequent bounded primary continuations found no further substantiated MUST FIX or SHOULD FIX issues. They examined upgrade collisions; tuning/reset/status contracts; request/history/streaming callers; install/manifest defaulting and post-ready persistence; SDK documentation; and complete caller files `core/builderconfig.go` (1–845), `core/embeddedllm/transport.go` (1–590), `core/builder_embedded_gate_test.go` (1–210), `backend/frontend_api_embedded_gate_test.go` (1–402), and `backend/application.go` (1–659). Inspectable detailed outputs include `request_history_contract`, `install_persistence_contract`, `manifest_readback_tail`, and `boundary_full_callers`. These are supporting primary-review artifacts, not independent approval.

### Latest scope and preservation check

The latest read-only inventory still shows 24 tracked modifications plus four untracked source/test files in c0wrk and five tracked modifications in sp4rk, with no staged changes. Both tracked-diff SHA-256 values and all four untracked-source hashes match the tables above. Unstaged and staged whitespace checks were silent in both repositories. This report is the only review-authored repository artifact; no source fixes or Git state-changing operations were performed.

The report retains findings, scope, complete changed-file reading coverage, checks, preservation evidence and the independent-verdict requirement rather than repeated turn-by-turn handoffs. Current totals remain **0 MUST FIX, 6 SHOULD FIX, 3 CONSIDER**. The final read-only partitions ran the focused checks recorded above; no full repository validation was performed. No new mandatory finding was established by those continuations. The deliverable and evidence are ready for independent verification; the primary reviewer does not self-certify the verifier's outcome.
