# Ребейзлинг эпика #84 (v2 daemon) и согласование с планом P-01…P-14

**Эпик:** [«c0wrk v2 — daemon architecture: TUI and headless/ACP clients»](https://github.com/v0lka/c0wrk/issues/84) — 9 саб-issue, все OPEN (0/9 выполнено).
**Сопоставляемый план:** [desktop-layering-audit.md](desktop-layering-audit.md) (аудит issue #42, 2026-11-07), миграция P-01…P-14.
**Дата сверки:** 2026-11-07. Production-код не изменялся; изменён только аудит-документ (добавлен §9) и создан настоящий отчёт.
**Статус:** предлагается к согласованию. Правки GitHub (эпик, саб-issue) **не применялись** — готовые тексты в §3–§5.

---

## 1. Задача и метод

1. Получены и прочитаны: тело эпика #84, его единственный комментарий (ребейзлинг от 2026-11-03), тела всех 9 саб-issue (#73, #74, #75, #76, #77, #79, #80, #81, #83).
2. Каждое фактическое утверждение эпика проверено против текущего дерева (`wc`, `ls`, `grep`, `gh issue view`), не против памяти.
3. Каждый пункт плана P-01…P-14 спроецирован на фазы эпика (0/1a/1b/1c/2/3/4/5/6): облегчает / нейтрален / конфликтует; для конфликтующих и рискованных пунктов предложены уточнения (они же внесены как §9 аудит-документа).

---

## 2. Инвентаризация устаревших и неточных данных

| # | Где | Утверждение | Факт на 2026-11-07 | Доказательство |
| --- | --- | --- | --- | --- |
| D-1 | #84 «Why», #73 «Context» | «a second and third host would duplicate **~600 lines of assembly**» | Composition root в `desktop/` — `startup_phases.go` **1699 строк** (69 452 B ≈ 68 КБ) + `startup.go` **747 строк**; всего предметной логики в `desktop/` ≈**3,2k строк** (37 % из 8696, 39 файлов) | `wc -l`; аудит #42 §2, §4.1 |
| D-2 | #84 «References», #77, #81 | «**ADR-062** (authored by #73)» | Слот 062 занят (`specs/decisions/062-hide-legacy-review-prompt-rows.md`); текущий максимум — **115** (`115-sidebar-pack-gates-reconcile-and-serialization.md`); следующий свободный номер — **116** | `ls specs/decisions/` |
| D-3 | комментарий #84 (2026-11-03) | «the ADR max is now 103 … would now be **104**» | Максимум **115** → следующий свободный **116** | `ls specs/decisions/` |
| D-4 | #84 «Why», #73 «Context» | «**only** `desktop` emits global (non-session) events» | Частично устарело: шов `FrontendAPIConfig.EmitEvent` уже в backend (`backend/frontend_api.go:461`), каталог событий несёт колонку Emitter (`specs/contracts/event-catalog.md:13`), часть глобальных событий эмитится из `backend/frontend_api_*.go`. Из `desktop/` всё ещё эмитятся `emitBackendReady` (`desktop/startup_phases.go:1008`), `app:exit_requested` (`desktop/exit_guard.go:19`), `files:dropped` и др. | `grep`; #76 «Context» подтверждает |
| D-5 | #79, work item | «Add `vector_index.enabled` (default `true`) so the daemon can skip ONNX load» | **Уже реализовано** ADR-103: `vector_index.enabled` — pointer-bool, default true, применяется live; секция конфига `VectorIndexConfig` (`backend/config/config.go:72`) | `specs/decisions/103-vector-index-master-switch.md` п.1–2 |
| D-6 | #74 «Context» | «`desktop/startup_phases.go` (~**58 KB**)» | **1699 строк / 69 452 байта (≈68 КБ)** | `wc -l`, `ls -l` |
| D-7 | #74 «Context» | Список функций сборки: `buildVectorCallbacks`, `startVectorIndexBackground` | **Не существуют** (переименованы): векторный запуск — `vectorIndexLauncher` (`startup_phases.go:1094`) и `runVectorIndexInit` (`:1160`). Остальные 17 имён из списка #74 актуальны (`initConfigAndDeps :105`, `buildUIEmitFunc :482`, `startUpdateCheckerBackground :1550` и т.д.) | `grep -n "func.*<имя>" desktop/startup_phases.go` |
| D-8 | #81 «Context» | «the **four** HITL funnels» | Их **пять**: `pendingConfirmations`, `pendingAskUser`, `pendingStepLimit`, `pendingPlanApprovals`, `pendingGoalProposals` | `desktop/app.go:83-87` |
| D-9 | #84 фазовая карта | 0/9 саб-issue выполнено | Верно, не устарело: все 9 OPEN | `gh issue view` |
| D-10 | комментарий #84 | «No daemon/gRPC/ACP code exists: no `.proto`, grpc absent, no `cmd/`, no TUI deps» | Верно, не устарело: `cmd/` нет, `*.proto` нет, `google.golang.org/grpc` в `go.mod` отсутствует (`protobuf v1.36.6 // indirect` — как и пишет #77), цели `proto` в Makefile нет | `ls`, `find`, `grep`, `go.mod:122` |
| D-11 | пробел (не устаревание) | Эпик не знает об аудите #42 | Существует `docs/development/desktop-layering-audit.md` (2026-11-07) с планом P-01…P-14, напрямую перекрывающим фазы 1a/1b/1c | настоящий документ |

---

## 3. Предлагаемое обновлённое тело эпика #84 (готово к вставке)

Полная замена тела; изменения против текущего: уточнены цифры D-1; снято «only desktop» (D-4); добавлена секция **Pre-work alignment**; References переведены на ADR-116 (D-2). Фазовая карта, цель, non-goals, граф последовательности и ограничения — без изменений (актуальны).

````markdown
# c0wrk v2 — daemon architecture: TUI and headless/ACP clients

## Why

The near-term plans for c0wrk include a **TUI** and a **headless mode** for IDE and cross-agent integration over the **ACP** protocol. Today c0wrk is a single Wails desktop application: the entire composition root lives in `desktop/` (`desktop/startup_phases.go`, 1 699 lines, plus `desktop/startup.go`, 747 lines of orchestration), all HITL (human-in-the-loop) routing lives in `desktop/`, and most global (non-session) events still originate in `desktop` (the `FrontendAPIConfig.EmitEvent` seam and the event-catalog Emitter column, landed as #76 groundwork, are the exception). A second and third host would duplicate the whole assembly — the #42 audit measures ≈3.2k lines of domain logic currently in `desktop/` — and re-implement five confirmation funnels.

The v2 direction:

- **Topology:** daemon + clients. The GUI migrates onto the same topology as the TUI and the ACP server.
- **Transport:** gRPC over a Unix domain socket (macOS/Linux) / named pipe (Windows), protobuf schema.
- **Clients:** GUI (thin Wails shell proxying to the daemon), TUI, and an ACP server (JSON-RPC over stdio → daemon gRPC).
- **Headless policy:** a permission-policy ladder with a **fail-closed default** (no client ⇒ deny).

## Goal

Turn the engine into a daemon that owns the engine, the SQLite DB, PTYs and HITL state, and make GUI/TUI/ACP equal clients of one transport. This epic is the **preparatory work only** — the TUI and the ACP server themselves are follow-up work built on top.

## Non-goals

- Implementing the TUI.
- Implementing the ACP server / mapping runtime.
- A sandbox execution provider.

## Pre-work alignment (2026-11-07)

The #42 layering audit (`docs/development/desktop-layering-audit.md`) defines a 14-PR migration (P-01…P-14) moving ≈3.2k lines of domain logic out of `desktop/` into `backend/`/`core/` with zero behavior change. It is executed **before** this epic's phases and is aligned with them (amendment log: audit §9):

- **P-12 + P-14 ≈ the bulk of phase 1a:** startup phases land in `backend/` as transport-neutral code (emit/quit hooks passed as parameters, following the existing `FrontendAPIConfig.EmitEvent` seam). Phase 1a (#74) then shrinks to wrapping them into `backend/appbuild` + `HostServices`.
- **P-08 + P-10 pre-implement the parsing/judging halves of phase 1b** (#75): HITL wire parsing goes to `core/`, the judge becomes a transport-neutral service shaped for absorption by `backend/interaction`. The five funnels themselves still move in #75.
- **P-09** (event batcher → `backend/`) is the reusable event-stream coalescer for the daemon transport (groundwork for 1c/2).
- **P-07** (single-instance lock primitive → `core/`) is the absorption point for the desktop-scoped ADR-075 lock that #79 needs.
- **P-11** (web-access subsystem → `backend/`) lets the daemon own the sidecar lifecycle (#79) instead of the GUI binding layer.

## Phase map

- [ ] **Phase 0 — ADR** #73: daemon topology, gRPC transport, `HostServices`, policy ladder, API split.
- [ ] **Phase 1a — Assembler** #74: extract the composition root into a transport-neutral assembler.
- [ ] **Phase 1b — Interaction** #75: HITL funnels + judge into `backend/interaction`.
- [ ] **Phase 1c — Global events** #76: emit global events from their source.
- [ ] **Phase 2 — Proto** #77: gRPC contract + codegen pipeline.
- [ ] **Phase 3 — Daemon** #79: `c0wrk serve` headless host, single-instance, lifecycle.
- [ ] **Phase 4 — GUI migration** #80: Wails shell as a gRPC client.
- [ ] **Phase 5 — Policy engine** #81: permission-policy ladder, fail-closed default.
- [ ] **Phase 6 — Contract versioning** #83: versioned event contract + ACP/TUI mapping tables.

## Sequencing / dependencies

```
0 ─▶ 1a ─▶ { 1b , 1c , 2 } ─▶ 3 ─▶ { 4 , 5 }
0 ─▶ 6  (parallel)
```

- 1a (assembler) unblocks 1b/1c/2.
- 3 (daemon) needs 1a + 1b + 1c + 2.
- 4 (GUI migration) and 5 (policy) both need 3.
- 6 (contract versioning) needs 0 and 2; can run parallel to 3.
- Pre-work: P-01…P-14 (issue #42) run before 1a; ADR-116 (#73) may be authored in parallel with them, but its `HostServices` design must reference the post-P shape (phases already in `backend/`, emit hooks as parameters).

## Cross-cutting constraints

- No behavior change to the desktop app at any step; migration is incremental and desktop tests move with the code.
- `make build && make lint && make test && make vulncheck` green after every phase.
- All new code lives in `backend/` (+ sub-packages) and `desktop/`; `core/` must not gain desktop/transport notions (`specs/architecture/layers.md`).
- All path construction/containment goes through `backend/config/paths.go` / `sp4rk/pathutil` — never inline.
- Phase work must not regress the layering gains of #42's migration: `desktop/` stays lifecycle + bindings + OS-shell + (until 1b) HITL plumbing, and no Wails import may leak into `backend/`/`core/` (0 today — keep it 0).

## References

- ADR-116 — to be authored by #73 (the 062 slot assumed in early drafts is taken by `specs/decisions/062-hide-legacy-review-prompt-rows.md`; the current ADR max is 115).
- Pre-work: `docs/development/desktop-layering-audit.md` (issue #42) + its §9 alignment amendments.
- `specs/architecture/layers.md`, `specs/contracts/event-catalog.md`, `SECURITY.md`.
````

---

## 4. Предлагаемый ребейзлинг-комментарий к #84 (кратко, EN)

> **Re-baseline v2 (2026-11-07, verified against current `main`):** still **0/9** sub-issues complete; no daemon/gRPC/ACP code exists (no `cmd/`, no `.proto`, `grpc` absent from `go.mod`, no `make proto` target). Updates to the previous re-baseline:
>
> 1. **ADR number**: the free slot moved again — current ADR max is **115**, so the daemon-topology ADR is now **#116** (not 104).
> 2. **Issue #42 audit landed** (`docs/development/desktop-layering-audit.md`): a 14-PR migration P-01…P-14 moves ≈3.2k lines of domain logic out of `desktop/`. It is sequenced **before** phase 1a and aligned with this epic via audit §9 (judge service shaped for `backend/interaction`, startup phases land transport-neutral as a `HostServices` precursor, GPU watchdog stays GUI-host-owned, intra-desktop pending-types move cancelled in favor of #75). Phases 1a/1b/1c get measurably smaller after it.
> 3. **#79's `vector_index.enabled` work item is already shipped** by ADR-103 (pointer-bool, default `true`, applies live) — mark absorbed.
> 4. Minor data drift in sub-issues: #74's function list references `buildVectorCallbacks`/`startVectorIndexBackground`, which no longer exist (now `vectorIndexLauncher`/`runVectorIndexInit`; file is 1 699 lines ≈ 68 KB, not ~58 KB); #81 says "four" HITL funnels — there are five (`desktop/app.go:83-87`); the "~600 lines of assembly" figure in the old Why is now ≈3.2k lines of domain logic per the audit.

---

## 5. Точечные правки саб-issue

| Issue | Правка | Обоснование |
| --- | --- | --- |
| #73 | «Context»: заменить «~600 lines of assembly» на актуальные цифры; снять «only `desktop` emits global events» (см. D-1, D-4); ADR-номер в контексте — 116 | D-1, D-2, D-4 |
| #74 | «Context»: `startup_phases.go` — 1699 строк (~68 КБ), не ~58 КБ; из списка функций убрать `buildVectorCallbacks` и `startVectorIndexBackground`, добавить `vectorIndexLauncher` (`:1094`) / `runVectorIndexInit` (`:1160`); в «Work items» добавить оговорку: фазы, уже перенесённые P-12/P-14, не переносятся повторно — #74 только оборачивает их в `backend/appbuild` + `HostServices` | D-6, D-7 + §6 |
| #77 | «References»: `ADR-062` → `ADR-116` | D-2 |
| #79 | Work item «Add `vector_index.enabled`…» — отметить `[x]` с пометкой «absorbed by ADR-103 (config key shipped; live apply)» | D-5 |
| #81 | «Context»: «the four HITL funnels» → «the five HITL funnels» | D-8 |
| #75, #76, #80, #83 | Правки не требуются: якоря и формулировки актуальны (проверено) | §1 |

---

## 6. Матрица сверки P-01…P-14 × фазы эпика

Оценка: как выполнение PR из плана #42 влияет на соответствующую фазу эпика. «Требует уточнения» — поправка уже внесена в аудит §9.

| PR (план #42) | Затрагиваемая фаза эпика | Эффект | Комментарий |
| --- | --- | --- | --- |
| P-01 GC-лимит → `core/` | — (ортогонально; политика переиспользуется #79) | ➕ нейтрально/плюс | Тоньше desktop; daemon применяет ту же политику памяти |
| P-02 save-path решения → `core/` | #80 (диалоги остаются client-native) | нейтрально+ | Биндинг остаётся в shell — граница #80 уже прорезана |
| P-03 CSP-константа → `core/` | #80 | плюс | Политика тестируема без Wails; middleware остаётся в shell |
| P-04 dialog_state → `backend/` | #80 | нейтрально+ | Персистентность уходит из shell |
| P-05 дедлайн shutdown → `core/` | #79 (graceful shutdown демона) | плюс | Daemon переиспользует политику дедлайнов |
| P-06 nvidia-sync + GPU-policy → `core/` | #80 | нейтрально+ | Все зависимости инъектируемы |
| P-07 `InstanceLock` → `core/` | #79 (single-instance daemon) | **плюс** | Поглощение ADR-075, которого эпик прямо требует; desktop-семантика relay остаётся в desktop (§9.1) |
| P-08 разбор HITL-wire → `core/` | #75 (translate-and-call адаптеры), #83 (ACP-маппинг HITL) | **плюс** | Чистый пакет без backend-зависимостей — переиспользуется и #75, и ACP-маппингом |
| P-09 батчер событий → `backend/` | #76/#77 (event-stream демона) | **плюс** | Коалесер с инъекцией emit — готовый строительный блок транспортировки событий |
| P-10 judge-сервис → `backend/` | #75 (`backend/interaction`) | плюс **с уточнением** | §9.1: API должен поглощаться #75 без повторного переезда (транспортно-нейтральный, без `App`/`wailsCtx`) |
| P-11 web-access → `backend/` | #79 (daemon владеет сайдкаром) | **плюс** | Плюс устранение дубля SAN-правила (A-1) — security-фикс, независимый от эпика |
| P-12 запуск вектора → `backend/` | #74 (фоновые службы), ADR-103 | плюс **с уточнением** | §9.1: транспортная нейтральность (emit-хуки параметрами) |
| P-13 GPU-watchdog → `backend/` | #80 (client-native) | нейтрально **с уточнением** | §9.1: политика в backend, но владение/запуск — GUI-хост; daemon не стартует |
| P-14 остальные фазы запуска → `backend/` | #74 — **это и есть большая часть 1a** | **плюс** с уточнением | §9.1: фазы без Wails-типов, посадка в пакеты, которые вызовет ассемблер; после P-14 фаза 1a сжимается до обёртки |
| A-11 (перенос pending-типов внутри desktop) | #75 (фаннелы → `backend/interaction`) | конфликт → **отменено** | §9.1: иначе двойной переезд; типы едут в #75 |

**Итог сверки:** направленных конфликтов нет — план и эпик двигают код в одну сторону (desktop → тонкий хост). Две точки «движения навстречу» (P-10 vs #75; A-11 vs #75) и два риска misplaced ownership (P-12/P-14 без шва; P-13 под владением daemon) закрыты уточнениями §9 аудит-документа. После P-01…P-14 объём фаз 1a/1b/1c сокращается, ни одна фаза не усложняется.

---

## 7. Рекомендуемый порядок работ

1. **Согласовать и выполнить P-01…P-14** (issue #42) с уточнениями §9. Каждый PR — «один перенос = один PR», зелёный гейт `make build/lint/test` (+ `test-stress`, `vulncheck` по pre-PR чек-листу).
2. **Параллельно** — ADR-116 (#73, фаза 0): писать можно уже во время миграции, но дизайн `HostServices` обязан опираться на пост-P форму (фазы уже в `backend/`, emit/quit — параметрами). Решить governance-вопрос §9.3 аудита: объединить ADR переносов («desktop = lifecycle + bindings + OS + HITL до фазы 1b») с ADR-116 или писать отдельным ADR с явной оговоркой.
3. **После P-14** — фазы эпика по его собственному графу `0 → 1a → {1b, 1c, 2} → 3 → {4, 5}`, `6` параллельно; граф не меняется, меняются объёмы: 1a ≈ обёртка готовых backend-фаз; 1b стартует с готовых P-08/P-10; 1c — с готового P-09; 3 использует P-07 + уже закрытый ADR-103 пункт; 4 получает заранее утончённый shell.

---

## 8. Верификация и ограничения

**Проверено инструментально (2026-11-07):** размеры файлов (`wc -l`, `ls -l` — `startup_phases.go` 1699/69 452 B, `startup.go` 747, `app.go` 1579, `event_handlers.go` 702, `singleinstance.go` 143, итого 8696 в 39 non-test файлах); занятость ADR-слотов (`ls specs/decisions/` — 062 занят, максимум 115); наличие пяти фаннелов (`desktop/app.go:83-87`); несуществование `buildVectorCallbacks`/`startVectorIndexBackground` и существование 17 остальных имён из #74 (`grep -n "func.*…" desktop/startup_phases.go`); шов `EmitEvent` (`backend/frontend_api.go:461`); ключ `vector_index.enabled` (ADR-103, `backend/config/config.go:72`); отсутствие `cmd/`, `*.proto`, `grpc` в `go.mod`, цели `proto` в Makefile; статусы всех 9 саб-issue (OPEN).

**Ограничения:**

1. Правки GitHub не применялись — тексты §3–§5 готовы к применению после согласования.
2. Проверялись тела issue и один комментарий #84; комментарии в саб-issue не вычитывались (их нет по данным `gh issue view --json comments` для эпика; для саб-issue не запрашивались — при применении правок стоит проверить ленту каждого).
3. Количественная оценка «фазы 1a/1b/1c станут меньше» опирается на состав функций P-12/P-14 против work items #74/#75; точную экономию в строках даст только исполнение.
4. Сравнение «~600 lines» с текущим состоянием — это фиксация дрейфа, а не поиск исходного замера: где возникла цифра 600, не устанавливалось.
