# Аудит слоёв пакета `desktop/`

**Задача:** [issue #42 — «Провести ревью и рефакторинг архитектуры, перенести лишнее из desktop в core или backend»](https://github.com/v0lka/c0wrk/issues/42)
**Дата аудита:** 2026-11-07
**Статус:** аудит завершён, план миграции предлагается к согласованию. Production-код не изменялся.
**Скоуп проверки:** все 39 non-test Go-файлов пакета `desktop/` (8696 строк), а не только пять файлов, перечисленных в issue.

---

## 1. Область и метод

### 1.1. Что проверялось

Полный пакет `desktop/` — все 39 non-test файлов, а также границы с `backend/`, `core/`, `internal/webaccess/` и `frontend/wailsjs/`.

### 1.2. Базовая линия (declared architecture)

Проверка велась не против общих принципов, а против объявленной архитектуры проекта:

| Источник | Утверждение, взятое за эталон |
| --- | --- |
| `specs/architecture/layers.md` | `desktop/` — «Thin layer. The `desktop.App` struct embeds `*backend.FrontendAPI` … Handles `OnStartup` … and `OnShutdown`. Manages pending confirmations (`sync.Map`) between backend and frontend.» |
| `specs/architecture/layers.md` (Import Rules) | `desktop/` может импортировать `backend`, `core`, sp4rk. `core/` не может импортировать `backend`/`desktop` |
| `.golangci.yml:99-105` | depguard реально обеспечивает только правило `core-no-backend`; остальные запреты — соглашением |
| `AGENTS.md` («Layered architecture») | `desktop/` — Wails bindings + app lifecycle (UI callbacks) |
| ADR-008 | `backend` и `desktop` импортируют sp4rk/core напрямую, без alias-слоёв |
| ADR-009 | Прецедент «domain logic out of backend → core» (помечен Superseded by ADR-011) |
| ADR-011 | Прецедент «один перенос пакета = один PR», плюс извлечение c0wrk-специфичных понятий из generic-слоя (~6 000 строк) |

### 1.3. Как велась проверка

1. Инвентаризация: перечень всех non-test файлов с числом строк (раздел 4).
2. Чтение полностью: пять файлов из issue (`csp.go`, `memlimit.go`, `dialog_state.go`, `window_state.go`, `event_handlers.go`) + `event_batcher.go`, `exit_guard.go`, `dialog_save.go`, структура `App` (`app.go:34-310`), ключевые фрагменты `startup_phases.go` и `app.go`.
3. Инвентаризация символов: для каждого файла собраны все top-level `func`/`type`/`const`/`var` с номерами строк — все ссылки `файл:строка` в отчёте взяты из этого перечня и проверяемы через `grep -n`.
4. Проверка направления импортов: фактический список импортов `desktop/` из `github.com/v0lka/c0wrk/*` (16 пакетов, раздел 3) и проверка, что `backend/`/`core/` **не содержат ни одного** импорта Wails.
5. Запуск трёх проверок issue (`make build`, `make lint`, `make test`) плюс `go test ./desktop/... -count=1` (раздел 7).

### 1.4. Критерий классификации (применён к каждому символу)

Символ **остаётся** в `desktop/`, если он попадает хотя бы в одну категорию:

- **W1** — использует типы Wails (`wailsRuntime.*`, `options.*`, `assetserver.Middleware`, `logger.Logger`) или является методом жизненного цикла (`Startup`, `Shutdown`, `DomReady`, `OnBeforeClose`, `OnFileDrop`);
- **W2** — является Wails-биндингом (экспортируемый метод `*App`, видимый генератору `frontend/wailsjs`);
- **W3** — обслуживает HITL-плумбинг между backend и frontend (`sync.Map` незавершённых подтверждений, каналы доставки, `GetPendingActions`), что `specs/architecture/layers.md` прямо закрепляет за `desktop/`;
- **W4** — нативная интеграция с ОС-оболочкой (окно, уведомления, single-instance, power/wake, X11-активация, webview GPU), не являющаяся предметной логикой.

Иначе — **переносится**: в `backend/` (оркестрация, персистентность, конфигурация) или в `core/` (чистая логика). Пограничные случаи, где файл содержит обе категории, помечены как **сплит**: переносится часть, в `desktop/` остаётся тонкий адаптер.

> **Жёсткое ограничение, выявленное в ходе аудита:** `backend/` и `core/` сегодня **не содержат ни одного** импорта `github.com/wailsapp/wails` (`grep -rl 'wailsapp/wails' backend/ core/` — пусто). Любой перенос, затягивающий Wails-тип в нижний слой, запрещён; для таких случаев в отчёте предлагается сплит, а не перенос целиком. Это касается `csp.go`, `notifications*.go`, `window_state.go`, `singleinstance.go`, `webview_gpu_policy.go`.

---

## 2. Фактическая архитектура `desktop/` (как есть)

`desktop/` реализует **четыре** роли одновременно, хотя объявлен как одна:

| Роль | Размер (оценка) | Примеры |
| --- | --- | --- |
| (1) Wails lifecycle + bindings — **легитимная** | ~3.0k строк | `startup.go:147` `Startup`, `startup.go:472` `Shutdown`, `exit_guard.go:64` `ShouldPreventClose`, `wails_logger.go:18`, нативные диалоги `app.go:382-511` |
| (2) Нативная интеграция с ОС-оболочкой — **легитимная** | ~2.0k строк | `notifications_linux.go:174`, `powerstate_darwin.go:215`, `window_activation_linux.go:175`, `singleinstance.go:27` |
| (3) HITL-плумбинг между backend и frontend — **легитимная** | ~1.1k строк | `pending_actions.go:62`, `app.go:186`-блок полей, `event_handlers.go:186-702` |
| (4) Предметная/прикладная логика — **нарушение слоёв** | ~3.2k строк (≈37 %) | `app.go:789-1579` (web-access), `startup_phases.go` (оркестрация подсистем), `memlimit.go`, `event_batcher.go`, `webview_gpu_watchdog.go` |

Ключевое наблюдение: `desktop/` фактически стал **вторым composition root** и одновременно доменным слоем. `startup_phases.go` (1699 строк) собирает базу данных, сторы, терминалы, векторный индекс, embedded-LLM, web-access и фоновые циклы — то есть выполняет работу `backend/`, а `app.go` (1579 строк) держит полноценную подсистему web-access вместе с TLS-логикой.

Второе наблюдение: `desktop/` держит **16 прямых зависимостей** от `backend`/`core`/`internal`. Пока нарушения слоёв не устранены, эта цифра растёт, а не сокращается.

---

## 3. Карта зависимостей и направление импортов

### 3.1. Фактические импорты `desktop/` → `backend`/`core`/`internal`

Замер (`grep -h '"github.com/v0lka/c0wrk/' desktop/*.go`, без тестов), по числу файлов-потребителей:

```
20  backend                  9  core/tools          2  core/updater
18  backend/session          6  backend/crashlog    2  core/toolmanager
18  backend/config           5  internal/webaccess  2  core/terminal
 4  backend/project          3  core/vectorindex    2  core/markitdown
 2  core                     2  backend/logger      1  core/embeddedllm
 1  backend/review
```

### 3.2. Диаграмма (обнаруженное состояние)

```
frontend/ ──(Wails IPC)──► desktop/
                             │  ├── (1) lifecycle + bindings        ✅ по спецификации
                             │  ├── (2) OS-shell integration         ✅ по спецификации
                             │  ├── (3) HITL plumbing                ✅ по спецификации
                             │  └── (4) domain/orchestration logic   ❌ НАРУШЕНИЕ
                             ▼
                          backend/ ──► core/ ──► sp4rk
                             ▲
                             └── ❌ обратного импорта НЕТ (проверено)

Запрещённые направления, которые сегодня НЕ наблюдаются:
  backend → desktop : нет (цикл невозможен — desktop импортирует backend)
  core    → backend : нет (depguard .golangci.yml:99)
  core/backend → wails : нет (0 файлов) — инвариант, который нельзя нарушить переносом
```

**Циклов нет.** Направление импортов формально корректно — нарушение не в направлении, а в **распределении ответственности**: логика, обязанная жить в `backend`/`core`, физически находится в `desktop`. Именно поэтому `depguard` такие нарушения не ловит.

---

## 4. Решения по файлам (все 39 non-test файлов)

Обозначения: **остаётся** — файл остаётся в `desktop/`; **переносится** — файл/символы переезжают; **сплит** — переносится часть, в `desktop/` остаётся адаптер. Риск/трудоёмкость: S (<0,5 дня), M (0,5–2 дня), L (>2 дней).

**Как читать таблицу (правило полноты).** Строка = файл. Колонка «Обоснование (якорь)» перечисляет **все** non-lifecycle символы файла с якорем `файл:строка`; для этих символов колонки «Целевой пакет», «Обоснование», «Риск» и «Труд.» задают решение и оценку **всего перечисленного набора** — символы одного файла переносятся одним PR, что и есть принцип «один перенос = один PR» (там, где набор требует разбиения, это указано явно: P-11 и P-14). Для строк «остаётся» non-lifecycle символов нет: все символы файла попадают в категории W1–W4 раздела 1.4, поэтому целевой пакет и оценка не применимы (`—`); легитимность таких файлов дополнительно обоснована в разделе 5.1. Каждая строка плана в разделе 6.2 несёт собственные риск и трудоёмкость, поэтому оценка существует и на уровне «переноса», и на уровне PR.

| # | Файл | Строк | Решение | Целевой пакет | Обоснование (якорь) | Риск | Труд. |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `app.go` | 1579 | **переносится** (сплит) | `backend/` (+ `core/` для чистых хелперов) | Web-access control surface `app.go:789-1579` (~790 строк) — оркестрация + персистентность + TLS-политика; остаются: структура `App` `app.go:34`, state-accessors `app.go:512-768`, нативные диалоги `app.go:382-511` | Высокий | L |
| 2 | `csp.go` | 41 | **переносится** (сплит) | `core/` (константа политики) | `productionCSP` `csp.go:19` — чистая политика безопасности; `CSPMiddleware` `csp.go:34` остаётся (тип `assetserver.Middleware` — Wails, W1) | Низкий | S |
| 3 | `csp_dev.go` | 19 | **остаётся** | — | build-tag-адаптер `CSPMiddleware` `csp_dev.go:13`, no-op для Vite/HMR (W1) | — | — |
| 4 | `dialog_save.go` | 117 | **переносится** (сплит) | `core/` | Чистые решения по ФС: `pickSaveDialogDir` `dialog_save.go:81`, `ensureMarkdownExtension` `dialog_save.go:93`, `resolveWritePath` `dialog_save.go:106`; биндинг `SaveMessageAsMarkdown` `dialog_save.go:25` остаётся (W2) | Низкий | S |
| 5 | `dialog_state.go` | 63 | **переносится** | `backend/` | Персистентность `~/.c0wrk/dialog_state.json`: `DialogState` `dialog_state.go:15`, `LoadDialogState` `dialog_state.go:23`, `rememberDialogDirectory` `dialog_state.go:45`, `writeDialogState` `dialog_state.go:57`; путь уже принадлежит `backend/config` (`config.DialogStatePath`) | Низкий | S |
| 6 | `doc.go` | 3 | **остаётся** | — | Package doc пакета | — | — |
| 7 | `event_batcher.go` | 303 | **переносится** (сплит) | `backend/` | Транспортно-независимая батчинг/коалесинг-машина: `EventBatcher` `event_batcher.go:39`, `sessionEventCoalesceKey` `event_batcher.go:221`; остаётся клей `sessionEventBatcher` `event_batcher.go:285` (поле `*App`) | Средний | M |
| 8 | `event_handlers.go` | 702 | **переносится** (сплит) | `core/` (чистый разбор) + `backend/` (judge) + `desktop/` (Wails-адаптеры) | Чистый разбор payload: `extractPayload` `event_handlers.go:18`, `parseConfirmDecision` `event_handlers.go:34`, `parseAskUserAnswers` `event_handlers.go:607`, `deliverResponse` `event_handlers.go:175` → `core/`; оркестрация judge `runJudgeEvaluation` `event_handlers.go:297` (LLM-вызов, бюджет 120 s `event_handlers.go:333`) → `backend/`; обработчики-адаптеры `handleToolConfirmResponse` `event_handlers.go:186` … `handleStepLimitResponse` `event_handlers.go:646` остаются (W3) | Средний | M |
| 9 | `exit_guard.go` | 125 | **остаётся** | — | Хук жизненного цикла: `ShouldPreventClose` `exit_guard.go:64`, `ConfirmExit` `exit_guard.go:113` (W1/W2); `lookupActiveSessions` `exit_guard.go:98` — тривиальная делегация в backend | — | — |
| 10 | `memlimit.go` | 164 | **переносится** | `core/` | Чистая политика GC-лимита: `memoryLimitDecision` `memlimit.go:43`, `computeAutoMemoryLimit` `memlimit.go:52`, `resolveMemoryLimit` `memlimit.go:72`, `applyMemorySoftLimit` `memlimit.go:99`, `setupMemorySoftLimit` `memlimit.go:160`; чистая, зависит только от `log/slog` + `runtime/debug`, один вызов `startup.go:250` | Низкий | S |
| 11 | `memlimit_ram_darwin.go` | 15 | **переносится** | `core/` | `physicalMemoryBytes` `memlimit_ram_darwin.go:9` — платформенный зонд RAM, едет вместе с `memlimit.go` | Низкий | S |
| 12 | `memlimit_ram_linux.go` | 34 | **переносится** | `core/` | `physicalMemoryBytes` `memlimit_ram_linux.go:14` — то же | Низкий | S |
| 13 | `memlimit_ram_other.go` | 10 | **переносится** | `core/` | `physicalMemoryBytes` `memlimit_ram_other.go:8` — fallback-звено той же группы build tags | Низкий | S |
| 14 | `memlimit_ram_windows.go` | 15 | **переносится** | `core/` | `physicalMemoryBytes` `memlimit_ram_windows.go:9` — то же | Низкий | S |
| 15 | `notifications.go` | 291 | **остаётся** | — | Wails-поверхность уведомлений: `InitNotifications` `notifications.go:90`, `SendSystemNotification` `notifications.go:164`, callback `notifications.go:60` (W1/W4) | — | — |
| 16 | `notifications_icon.go` | 17 | **остаётся** | — | Встроенная иконка уведомлений `notifications_icon.go:15` (W4) | — | — |
| 17 | `notifications_linux.go` | 590 | **остаётся** | — | D-Bus-транспорт `org.freedesktop.Notifications`: `sendNotificationPlatform` `notifications_linux.go:174`, `platformNotificationState` `notifications_linux.go:133` — нативная интеграция с ОС-оболочкой (W4), предметной логики нет. Кандидат на `internal/` в будущем эпике, не в этом | — | — |
| 18 | `notifications_notlinux.go` | 26 | **остаётся** | — | Fallback на Wails: `sendNotificationPlatform` `notifications_notlinux.go:20`, `cleanupNotificationTransport` `notifications_notlinux.go:26` (W1) | — | — |
| 19 | `pending_actions.go` | 158 | **остаётся** | — | HITL-плумбинг: DTO `pending_actions.go:9-61`, RPC `GetPendingActions` `pending_actions.go:62` — `layers.md` прямо закрепляет это за `desktop/` (W3) | — | — |
| 20 | `powerstate_darwin.go` | 267 | **остаётся** | — | cgo/Objective-C обсерверы power-wake и восстановления webview: `registerPowerWakeObserver` `powerstate_darwin.go:215`, `reloadWebviewNative` `powerstate_darwin.go:264` (W4) | — | — |
| 21 | `powerstate_other.go` | 14 | **остаётся** | — | Заглушки той же группы: `powerstate_other.go:9`, `powerstate_other.go:14` (W4) | — | — |
| 22 | `shutdown_watchdog.go` | 135 | **переносится** (сплит) | `core/` | Чистая политика дедлайна: потолки `shutdown_watchdog.go:26-41`, `EffectiveShutdownDeadline` `shutdown_watchdog.go:48`; сторож `shutdownWatchdog` `shutdown_watchdog.go:63` остаётся (жизненный цикл, W1) | Низкий | S |
| 23 | `singleinstance.go` | 143 | **переносится** (сплит) | `core/` | ОС-примитив блокировки: `InstanceLock` `singleinstance.go:65`, `AcquireSingleInstanceLock` `singleinstance.go:79`, `WaitForInstanceLockRelease` `singleinstance.go:126`; остаются Wails-опции `SingleInstanceOptions` `singleinstance.go:27` и relay `handleSecondInstanceLaunch` `singleinstance.go:46` (W1) | Средний | M |
| 24 | `singleinstance_unix.go` | 25 | **переносится** | `core/` | `tryLockExclusive` `singleinstance_unix.go:15` — платформенная половина блокировки | Средний | M |
| 25 | `singleinstance_windows.go` | 28 | **переносится** | `core/` | `tryLockExclusive` `singleinstance_windows.go:15` — то же | Средний | M |
| 26 | `startup.go` | 747 | **переносится** (сплит) | `core/` + `backend/` | Чистое разрешение путей: `resolveModelPath` `startup.go:696`, `resolveONNXLibPath` `startup.go:723` → `core/`; типы ожиданий `pendingConfirmData` `startup.go:31` … `pendingGoalSuggestionEntry` `startup.go:77` — переезжают к обработчикам `event_handlers.go`; остаются `Startup` `startup.go:147`, `Shutdown` `startup.go:472`, `DomReady` `startup.go:140`, `wireWailsEventListeners` `startup.go:636` (W1) | Средний | M |
| 27 | `startup_phases.go` | 1699 | **переносится** (сплит) | `backend/` | Оркестрация подсистем: `initDatabase` `startup_phases.go:358`, `initStores` `startup_phases.go:397`, `initTerminalManager` `startup_phases.go:372`, `preloadProjectsAndSessions` `startup_phases.go:433`, `initTools` `startup_phases.go:132`, `reconcileToolsWithSplash` `startup_phases.go:213`, `vectorIndexLauncher` `startup_phases.go:1094` + `runVectorIndexInit` `startup_phases.go:1160`, `initEmbeddedLLM` `startup_phases.go:1575`, `initChatGPTAuth` `startup_phases.go:1600`, `startUpdateCheckerBackground` `startup_phases.go:1550`, `startAutoFetchBackground` `startup_phases.go:1565` → `backend/`; остаются Wails-адаптеры `buildUIEmitFunc` `startup_phases.go:482`, `buildConfirmCallback` `startup_phases.go:767`, `buildAskUserCallback` `startup_phases.go:515`, `buildStepLimitCallback` `startup_phases.go:840`, `initLogger` `startup_phases.go:42` (W1/W3) | Высокий | L |
| 28 | `wails_logger.go` | 93 | **остаётся** | — | Мост `logger.Logger` → `slog`: `wailsLogAdapter` `wails_logger.go:18`, `NewWailsLogger` `wails_logger.go:28` (W1) | — | — |
| 29 | `webaccess_extra.go` | 20 | **остаётся** | — | Пином зафиксированная dispatch-поверхность `webExtraSurface` `webaccess_extra.go:12` — не является доменной логикой (W2/W3) | — | — |
| 30 | `webaccess_inbound.go` | 74 | **остаётся** | — | Диспетчер входящих WS-кадров в те же HITL-обработчики: `webWSInboundDispatcher` `webaccess_inbound.go:15`, `dispatchWebInboundFrame` `webaccess_inbound.go:28` (W3). Возможный кандидат на `backend/` в отдельном PR после P-13 | — | — |
| 31 | `webview_gpu_policy.go` | 116 | **переносится** (сплит) | `core/` | Чистое отображение политики: `webviewGpuPolicy` `webview_gpu_policy.go:19`, `webviewGpuPolicyFromCanonical` `webview_gpu_policy.go:42`, `String` `webview_gpu_policy.go:29`; остаётся сборка Wails-опций `WebviewGpuPolicyOptions` `webview_gpu_policy.go:80` (W1) | Низкий | M |
| 32 | `webview_gpu_postmortem.go` | 110 | **переносится** | `backend/` | Посмертный анализ stderr предыдущего запуска + персистентность состояния: `ApplyWebviewGpuCrashFallback` `webview_gpu_postmortem.go:28`, `previousRunDiedOnWaylandProtocolError` `webview_gpu_postmortem.go:68`, `readStderrTail` `webview_gpu_postmortem.go:90` | Средний | M |
| 33 | `webview_gpu_watchdog.go` | 327 | **переносится** | `backend/` | Машина состояний GPU-отказов: `webviewGpuWatchdog` `webview_gpu_watchdog.go:90`, `check` `webview_gpu_watchdog.go:203`, `scanTail` `webview_gpu_watchdog.go:261`, `updateState` `webview_gpu_watchdog.go:271` (пишет `backend.WebviewGpuState`), `GpuRelaunchHandoverBudget` `webview_gpu_watchdog.go:58` | Средний | L |
| 34 | `webview_gpu_watchdog_relaunch_linux.go` | 38 | **переносится** | `backend/` | `relaunchDetachedSelf` `webview_gpu_watchdog_relaunch_linux.go:17` — часть того же переноса | Средний | L |
| 35 | `webview_gpu_watchdog_relaunch_other.go` | 14 | **переносится** | `backend/` | `relaunchDetachedSelf` `webview_gpu_watchdog_relaunch_other.go:12` — заглушка той же группы | Средний | L |
| 36 | `webview_nvidia_explicit_sync.go` | 39 | **переносится** | `core/` | Чистое решение по env: `ApplyNvidiaExplicitSyncWorkaround` `webview_nvidia_explicit_sync.go:17`, `applyNvidiaExplicitSyncWorkaround` `webview_nvidia_explicit_sync.go:21` (все зависимости инъектируемы) | Низкий | S |
| 37 | `window_activation_linux.go` | 351 | **остаётся** | — | X11/cgo-активация окна: `x11ActivateOwnWindow` `window_activation_linux.go:175`, `x11WindowPID` `window_activation_linux.go:282` — нативная ОС-интеграция (W4) | — | — |
| 38 | `window_activation_notlinux.go` | 7 | **остаётся** | — | Заглушка `x11ActivateOwnWindow` `window_activation_notlinux.go:7` (W4) | — | — |
| 39 | `window_state.go` | 177 | **переносится** (сплит) | `backend/` | Персистентность геометрии: `WindowBounds` `window_state.go:19`, константы `window_state.go:29`, `LoadWindowBounds` `window_state.go:40`, `defaultWindowBounds` `window_state.go:61`, правило сохранения maximized `window_state.go:149`, `writeWindowBounds` `window_state.go:171`; остаются Wails-чтения `readLiveWindowGeometry` `window_state.go:104`, `captureWindowGeometry` `window_state.go:125`, биндинг `PersistWindowBounds` `window_state.go:73` (W1/W2) | Средний | M |

### 4.1. Сводка по решениям

| Решение | Файлов | Строк (прибл.) |
| --- | --- | --- |
| **остаётся** целиком | 17 | ~2 100 |
| **переносится** целиком | 12 | ~800 |
| **переносится** (сплит, часть остаётся адаптером) | 10 | ~5 800 файловых, из них переносится ~2 400 |
| Итого | **39** | **8 696** |

Оценка объёма переносимого кода: **≈3 200 из 8 696 строк (≈37 %)**. После завершения эпика `desktop/` сжимается примерно до 5 500 строк, из которых предметной логики не остаётся.

---

## 5. Находки

Формат: `[ID] [SEVERITY] — суть`, затем область, доказательство, следствие, рекомендация.

---

### [A-1] HIGH — Подсистема web-access (оркестрация + персистентность + TLS-политика) целиком живёт в binding-слое

**Область:** structure
**Доказательство:** `desktop/app.go:789-1579` — ~790 строк: `GetWebAccessInfo` `desktop/app.go:881`, `SetWebAccessEnabled` `desktop/app.go:949`, `applyExperimentalWebAccess` `desktop/app.go:1031`, `SetWebAccessConnection` `desktop/app.go:1074`, `RegenerateWebAccessToken` `desktop/app.go:1129`, `SetWebAccessToken` `desktop/app.go:1158`, `SetWebAccessTLS` `desktop/app.go:1207`, `SetWebAccessAllowInsecureLAN` `desktop/app.go:1271`, `GenerateWebAccessCertificate` `desktop/app.go:1311`, `classifyWebAccessCert` `desktop/app.go:1362`, `webAccessCertCoversHost` `desktop/app.go:1397`, `dnsSANCovers` `desktop/app.go:1418`, `restartWebAccessOrRollbackLocked` `desktop/app.go:1441`, `webAccessMountAllowed` `desktop/app.go:1494`, `startWebAccessLocked` `desktop/app.go:1502`, `stopWebAccessLocked` `desktop/app.go:1558`.

Дополнительно: правило «какие хосты покрывает сертификат для данного bind» реализовано **дважды** — генерация в `internal/webaccess/certgen.go:97` (`certSANs`) и валидация в `desktop/app.go:1397-1440` (`webAccessCertCoversHost` + `dnsSANCovers`). Это два независимых кодирования одного и того же правила.

**Почему это важно:** `desktop.App` — ровно та структура, все экспортируемые методы которой Wails публикует в webview и (через `internals/webaccess` reflection-dispatch) в браузерную вкладку. Держать здесь TLS-решения, rollback-персистентность конфига и классификацию сертификатов — значит держать security-релевантную логику в слое, чья задача — маршрутизация вызовов. Логика не покрыта пакетными тестами backend-уровня, а расхождение двух реализаций SAN-правила (генератор vs валидатор) даёт наблюдаемый пользователю дефект: сгенерированный сертификат может быть помечен `cert_status=mismatch`.

**Рекомендация:** вынести в `backend/` (новый `backend/frontend_api_webaccess.go` + сервис жизненного цикла рядом с существующим `backend/config/web_access.go`), методы `*App` оставить тонкими делегатами, чтобы `frontend/wailsjs` не изменился. SAN-правило свести к одной функции, используемой и генератором, и валидатором. Требует сплита на 2–3 PR (см. P-11).

---

### [A-2] HIGH — `startup_phases.go` превращает binding-слой во второй composition root

**Область:** structure
**Доказательство:** `desktop/startup_phases.go` — 1699 строк (самый большой файл пакета). Оркестрация чужих подсистем: открытие БД `desktop/startup_phases.go:358` (`initDatabase`), SQLite-сторы `:397` (`initStores`), PTY-менеджер `:372` (`initTerminalManager`), предзагрузка проектов/сессий `:433`, tool-manager `:132`/`:213`, запуск векторного индекса `:1094`/`:1160` (`vectorIndexLauncher` / `runVectorIndexInit`, ~300 строк), embedded-LLM `:1575`, ChatGPT-auth `:1600`, фоновый update-checker `:1550`, фоновый git-autofetch `:1565`.

**Почему это важно:** `desktop/` держит 16 прямых зависимостей от `backend`/`core` (раздел 3.1) именно из-за этого файла. Каждая новая подсистема добавляет в binding-слой ещё один import и ещё один фоновой цикл с собственными epoch/goroutine-инвариантами (`vectorIndexLauncher` вводит `epoch`+`ready`-канал в `desktop/startup_phases.go:1145`) — то есть конкурентная корректность ядра обслуживается кодом, который не является частью ядра и не может быть протестирован без `App`.

**Рекомендация:** перенести «фазы» в `backend/` как методы `Application`/`FrontendAPILifecycle` (механизм уже есть: `FrontendAPILifecycle` в `backend/frontend_api.go:531`, 17 методов, включая `SetVectorIndexEnabledHook` `backend/frontend_api.go:1003`, которым уже пользуется `desktop/startup.go:392`). В `desktop/` остаются вызовы фаз и Wails-специфичные адаптеры (`buildUIEmitFunc` `desktop/startup_phases.go:482`, `buildConfirmCallback` `:767`). Разбивается на 4–5 PR (P-09, P-12, P-13).

---

### [A-3] HIGH — Оценка judge (LLM-вызов с бюджетом) выполняется внутри обработчика события

**Область:** structure / resilience
**Доказательство:** `desktop/event_handlers.go` (702 строки) смешивают четыре несовместимые ответственности. Оркестрация judge: `runJudgeEvaluation` `desktop/event_handlers.go:297` — `context.WithTimeout(parentCtx, 120*time.Second)` `:333`, инъекция dump-writer, `JudgeContext`-обогащение, `EvaluateJudgeForSession` `:353`, recover от паники `:299`, публикация `tool_judge_response`. Рядом — чистый разбор wire-протокола: `extractPayload` `:18`, `parseConfirmDecision` `:34`, `parseStepLimitResponse` `:77`, `parsePlanApprovalDecision` `:96`, `confirmDecisionString` `:137`, `parseAskUserAnswers` `:607`, `deliverResponse` `:175`, и App-связанные обработчики `:186-702`.

**Почему это важно:** бюджет LLM-вызова (120 s) и корректность выбора session-привязанной модели (ADR-028) определяют поведение задачи, но живут в обработчике события Wails. Тестировать это можно только через `&App{}` с подменёнными `wailsCtx`/`wailsEmit` — отсюда тестовые швы `activeSessionsFn`/`wailsEmit`/`window*Fn` в структуре `App` (`desktop/app.go:34-310`), то есть `App` несёт тестовые поля для чужой логики. Плюс 702-строчный файл как единица навигации: чистая функция и запуск LLM-горутины с `WaitGroup` находятся в одном файле.

**Рекомендация:** `core/` — пакет чистого разбора HITL-wire-протокола (без `App`, без Wails); `backend/` — сервис judge-evaluation, принимающий `(sessionID, toolName, input, taskContext)` и возвращающий `(reasoning, error)`, с собственным таймаутом и без привязки к `wailsCtx`; `desktop/` — тонкие адаптеры событий + доставка в каналы (W3). PR P-08 и P-10.

---

### [A-4] MEDIUM — Политика GC-лимита памяти: чистая логика в `desktop/`

**Область:** structure
**Доказательство:** `desktop/memlimit.go` (164 строки) — `memoryLimitDecision` `desktop/memlimit.go:43`, `computeAutoMemoryLimit` `:52` (clamp(totalRAM/2, 2 GiB, 8 GiB)), `resolveMemoryLimit` `:72` (порядок OFF → config → GOMEMLIMIT → AUTO), `applyMemorySoftLimit` `:99`, `setupMemorySoftLimit` `:160`. Зависимости файла — только `log/slog` и `runtime/debug`; платформенные зонды `physicalMemoryBytes` в `desktop/memlimit_ram_darwin.go:9`, `desktop/memlimit_ram_linux.go:14`, `desktop/memlimit_ram_other.go:8`, `desktop/memlimit_ram_windows.go:9`. Единственная точка вызова — `desktop/startup.go:250`.

**Почему это важно:** политика принадлежит предметной области «снижение памяти векторного индекса» (ADR-049, на который ссылается сам файл), но физически лежит в binding-слое. Следствие ограниченное: рантайм-поведение корректно, страдает только размещение и повторное использование (например, CLI-путь не может применить ту же политику).

**Рекомендация:** перенести всю группу (5 файлов, включая build-tag-близнецы зонда RAM) в `core/` как единый пакет; `desktop/startup.go:250` остаётся единственной точкой вызова. Риск низкий (S), поведение сохраняется побайтно.

---

### [A-5] MEDIUM — Персистентность `dialog_state.json` вне владельца путей

**Область:** structure
**Доказательство:** `desktop/dialog_state.go` (63 строки) — `DialogState` `desktop/dialog_state.go:15`, `LoadDialogState` `:23`, `rememberDialogDirectory` `:45`, `writeDialogState` `:57`. Файл читает/пишет `config.DialogStatePath(agentDir)` и использует `safeio` для atomic-write.

**Почему это важно:** по `specs/architecture/layers.md` единственный владелец файловой раскладки `~/.c0wrk/` — `backend/config/paths.go`; логика работы с состоянием на диске принадлежит `backend/` (персистентность). Здесь она в `desktop/`, из-за чего правила atomic-write/symlink-защиты дублируются в двух слоях (`dialog_state.go:57` и `window_state.go:171` повторяют один и тот же приём).

**Рекомендация:** перенести файл целиком в `backend/` (S). `rememberDialogDirectory` уже принимает логгер — сигнатура переносится без изменений.

---

### [A-6] MEDIUM — Персистентность геометрии окна: перенос без сплита невозможен

**Область:** structure
**Доказательство:** `desktop/window_state.go` (177 строк). Персистентная половина: `WindowBounds` `desktop/window_state.go:19`, константы `:29`, `LoadWindowBounds` `:40`, `defaultWindowBounds` `:61`, `writeResolvedWindowBounds` `:149` (правило сохранения maximized), `writeWindowBounds` `:171`. Wails-половина: `PersistWindowBounds` `:73` (биндинг), `restoreMaximizedWindow` `:83`, `saveWindowBounds` `:94`, `readLiveWindowGeometry` `:104` (`wailsRuntime.WindowGetSize`), `captureWindowGeometry` `:125`, `persistWindowBoundsAtShutdown` `:137`.

**Почему это важно:** файл — типичный пример, где прямая миграция создаст Wails-импорт в `backend/` (чего сегодня нет ни в одном файле `backend/`/`core/`). Это подтверждает, что для части кандидатов из issue нужен именно сплит, а не перенос файла.

**Рекомендация:** в `backend/` переносятся DTO, границы валидации и правило maximized (чистая функция `(live, prev) -> next`); в `desktop/` остаётся чтение геометрии через Wails и биндинг. M.

---

### [A-7] MEDIUM — CSP-политика в binding-слое; middleware отделить от политики

**Область:** structure / security
**Доказательство:** `desktop/csp.go` (41 строка) — `productionCSP` `desktop/csp.go:19` (строгая политика: `default-src 'none'`, `connect-src 'self'`, `img-src` намеренно широкий) и `CSPMiddleware` `:34` (тип `assetserver.Middleware`). Dev-вариант — `desktop/csp_dev.go:13`. Контракт закреплён тестом `desktop/csp_test.go:16`.

**Почему это важно:** политика — чистая константа безопасности, не привязанная к Wails; middleware — наоборот, целиком Wails-типизирован. Пока они в одном файле, изменить политику без правки binding-слоя нельзя. Проверка также подтвердила, что дублирования с `internal/webaccess/webaccess.go:305` (`securityHeaders`) нет — сайдкар выставляет только `nosniff`/`Referrer-Policy`, CSP живёт в одном месте.

**Рекомендация:** константу (и, при желании, сборщик значения заголовка) перенести в `core/` рядом с прочими политиками безопасности; `CSPMiddleware` остаётся в `desktop/` и берёт значение из `core/`. S.

---

### [A-8] MEDIUM — Транспортно-независимый батчер событий в `desktop/`

**Область:** structure
**Доказательство:** `desktop/event_batcher.go` (303 строки) — `EventBatcher` `:39`, константы батча `:14` (16 ms, 256 событий, 256 KiB), `Enqueue` `:124`, `flush` `:166`, `sessionEventCoalesceKey` `:221`, `eventStreamDiscriminator` `:238`, `isImmediateFlushEvent` `:269`. К `App` привязаны только три метода: `sessionEventBatcher` `:285`, `emitBatchedEvent` `:293`, `stopEventBatcher` `:299`.

**Почему это важно:** 300 строк очереди/коалесинга с собственными инвариантами порядка и ограничениями по размеру не содержат ни одного Wails-типа; их место — рядом с остальной транспортной логикой `backend/`. Пока они в `desktop/`, правила доставки (описанные в `specs/contracts/event-catalog.md`) не переиспользуются ни одним другим транспортом, хотя web-access сайдкар получает те же события.

**Рекомендация:** перенести батчер в `backend/` с инъекцией emit-функции (уже инъектируется: `newEventBatcher(log, emit)` `:77`); три метода-обёртки `*App` остаются. M.

---

### [A-9] MEDIUM — Машина состояний GPU-отказов и посмертный анализ в `desktop/`

**Область:** structure / resilience
**Доказательство:** `desktop/webview_gpu_watchdog.go` (327 строк) — `webviewGpuWatchdog` `:90`, `check` `:203`, `scanTail` `:261`, `updateState` `:271` (персистит `backend.WebviewGpuState`), `relaunch` `:278`, `quit` `:292`, `scanStderrTail` `:303`, политика передачи `GpuRelaunchHandoverBudget` `:58` и `WaitForGpuRelaunchHandover` `:72`; плюс `desktop/webview_gpu_postmortem.go` (110 строк) (`ApplyWebviewGpuCrashFallback` `:28`, `previousRunDiedOnWaylandProtocolError` `:68`) и платформенный relaunch `desktop/webview_gpu_watchdog_relaunch_linux.go:17` / `..._other.go:12`.

**Почему это важно:** это персистентная политика отказоустойчивости (сколько раз падал запуск, переключать ли на `never`), уже опирающаяся на `backend/crashlog` и `backend.WebviewGpuState`. Правило «сколько рестартов допускается» — предметная логика, а не UI-обвязка; её место в `backend/` рядом с `crashlog`. Остаётся связанность: `quit()`/`relaunch()` требуют инъекции колбэков приложения (сигнал к переезду — сейчас они напрямую зовут методы `App`).

**Рекомендация:** перенести watchdog+postmortem в `backend/` с инъекцией `quit func()`/`relaunch func() error`; чистое бюджетирование (`GpuRelaunchHandoverBudget` `:58`) можно сразу отдать в `core/`. L, средний риск (фоновые горутины, Linux-специфика).

---

### [A-10] LOW — Рассыпанные чистые хелперы (мелкая, но постоянная утечка логики)

**Область:** structure
**Доказательство:** `webAccessURLHost` `desktop/app.go:845`, `webAccessBrowserURL` `desktop/app.go:858` (чистое построение URL из bind/port/TLS), `attachmentFilterPattern` `desktop/app.go:412`, `resolveModelPath` `desktop/startup.go:696`, `resolveONNXLibPath` `desktop/startup.go:723`, `EffectiveShutdownDeadline` `desktop/shutdown_watchdog.go:48`, `webviewGpuPolicyFromCanonical` `desktop/webview_gpu_policy.go:42`, `applyNvidiaExplicitSyncWorkaround` `desktop/webview_nvidia_explicit_sync.go:21`, `derefFloat`/`derefInt`/`derefParkBudgetBytes`/`derefCorpusResidencyBudgetBytes`/`derefStoreDiskBudgetBytes` `desktop/startup_phases.go:1492-1550`.

**Почему это важно:** каждый по отдельности безобиден, но именно эти функции — причина, по которой `desktop/` нельзя назвать «тонким»: binding-слой содержит чистые вычисления, которые невозможно переиспользовать и которые тянут за собой тесты уровня пакета `desktop`. Функции `deref*` — прямое дублирование логики разыменования конфиг-указателей, место которой в `backend/config`.

**Рекомендация:** сложить в целевые пакеты в рамках ближайших по теме PR (P-01, P-03, P-11, P-12); отдельный PR не нужен.

---

### [A-11] LOW — Связность HITL-плумбинга размазана по четырём файлам

**Область:** structure (cohesion)
**Доказательство:** `sync.Map`-реестры и поля judge — в `desktop/app.go:34-310`; типы ожиданий — `desktop/startup.go:31-83` (`pendingConfirmData` `:31`, `pendingStepLimitEntry` `:44`, `pendingAskUserEntry` `:52`, `pendingPlanApprovalEntry` `:60`, `pendingGoalProposalEntry` `:69`, `pendingGoalSuggestionEntry` `:77`); обработчики — `desktop/event_handlers.go:186-702`; RPC-агрегатор — `desktop/pending_actions.go:62`.

**Почему это важно:** один доменный концепт («ожидающий ответа запрос») обслуживается четырьмя файлами в трёх разных «слоях» одного пакета. Само по себе это **не** нарушение слоёв (`layers.md` закрепляет реестры за `desktop`), но перенос `event_handlers.go` без переезда типов ожиданий оставит `startup.go` владельцем типов, которыми пользуется только HITL-слой.

**Рекомендация:** при выполнении P-08/P-10 перенести типы ожиданий в новый файл `desktop/pending_state.go` рядом с обработчиками — чисто механический шаг внутри соответствующих PR.

---

### [A-12] LOW (governance) — Инвентаризация в issue #42 устарела; аудит-артефакта не существовало

**Область:** governance
**Доказательство:** issue описывает «roughly 8.3k lines across ~38 files» и перечисляет 5 кандидатов с конкретными размерами. Фактически: **39 файлов, 8 696 строк**; размеры разошлись — `event_handlers.go` 499 → **702**, `memlimit.go` 177 → **164**, `dialog_state.go` 77 → **63**, `window_state.go` 145 → **177**, `csp.go` 67 → **41**. Кроме пяти перечисленных, аудит нашёл **ещё девять** файлов с предметной логикой (`app.go:789-1579`, `startup_phases.go`, `event_batcher.go`, `webview_gpu_watchdog.go`, `webview_gpu_postmortem.go`, `singleinstance.go`, `webview_gpu_policy.go`, `webview_nvidia_explicit_sync.go`, `shutdown_watchdog.go`), то есть исходный список покрывал менее половины фактического объёма.

**Почему это важно:** acceptance criterion #1 из issue («An audit document lists the `desktop/` layering violations with a target package for each») до сих пор не был выполнен, поэтому эпик не мог начаться корректно: последовательность переносов, построенная на устаревшем списке, оставила бы ≈2/3 нарушений нетронутыми. Настоящий документ закрывает этот критерий.

**Рекомендация:** при согласовании плана использовать раздел 4 как актуальный реестр; при добавлении нового файла в `desktop/` с предметной логикой реестр обновлять в том же PR (соглашение, а не линтер).

---

### 5.1. Что проверено и нарушением НЕ является (баланс оценки)

Следующие файлы при аудите выглядели кандидатами, но по факту легитимны — это важно, чтобы миграция не начала «переносить всё подряд»:

- `notifications.go` / `notifications_linux.go` / `notifications_notlinux.go` / `notifications_icon.go` — поверхность уведомлений Wails и D-Bus-транспорт: интеграция с ОС-оболочкой (W4). Предметной логики нет, доменных типов нет. 590 строк D-Bus — это объём, а не нарушение слоя; вопрос уместности `internal/` относится к другому эпику.
- `powerstate_darwin.go`, `window_activation_linux.go`, `wails_logger.go`, `exit_guard.go`, `doc.go`, `csp_dev.go` — cgo/Objective-C, X11, `logger.Logger`-мост, quit-хук: чистые ОС/Wails-адаптеры.
- `pending_actions.go`, `webaccess_extra.go`, `webaccess_inbound.go` — HITL-плумбинг и зафиксированная dispatch-поверхность; `layers.md` прямо относит реестры подтверждений к `desktop/`.
- `app.go:768` `resolvePendingMessage` — **не** логика: это однострочная делегация в `FrontendAPI.ResolvePendingMessage`. Хороший пример того, что «метод в `desktop/`» ≠ «логика в `desktop/`»; в отчёте такие символы не считаются нарушениями.
- Направление импортов корректно: `backend/`/`core/` не импортируют `desktop/`, `core/` не импортирует `backend/` (depguard `no-backend`), Wails в нижних слоях отсутствует.

---

## 6. План миграции: один перенос = один PR

### 6.1. Общие правила для **каждого** PR

1. **Поверхность Wails неизменна.** Все 28 экспортируемых методов `*App` (замер: `grep -c '^func (a \*App) [A-Z]' desktop/*.go`) и встроенный `*backend.FrontendAPI` остаются на месте; перенесённый код вызывается из тонкого адаптера. Критерий приёмки PR: `git diff --exit-code frontend/wailsjs/` после `wails build` — **пусто** (сгенерированные биндинги не дрейфуют).
2. **Поведение не меняется.** Перенос + adapter wiring; никаких функциональных изменений, никаких «попутных улучшений». Ошибки/логи сохраняют формулировки, чтобы не ломать контрактные тесты (`specs/contracts/`).
3. **Никакого Wails в `backend/`/`core/`.** Инвариант зафиксирован замером (0 файлов). Если перенос требует Wails-типа — это признак, что нужен **сплит**, а не перенос файла (`csp.go`, `notifications*`, `window_state.go`, `singleinstance.go`, `webview_gpu_policy.go`).
4. **Тесты едут вместе с кодом.** Пакетные `*_test.go` переносятся в целевой пакет; тестовые швы в `App` (`activeSessionsFn`, `wailsEmit`, `window*Fn`, `reloadAppFn`) удаляются по мере исчезновения нужды в них.
5. **Пути — только через централизованный API.** `pathutil` / `backend/config/paths.go` / `core/pathsegments.go`; inline-проверки `HasPrefix`/`filepath.Rel` недопустимы (issue, «Implementation notes»).
6. **Зелёный гейт.** `make build`, `make lint`, `make test` — до и после; отчёт о результате в описании PR.
7. **Один перенос — один PR.** Никаких «заодно перенёс соседний файл».

### 6.2. Последовательность (по возрастанию риска)

| PR | Перенос | Откуда → куда | Риск | Труд. | Зависит от | Обоснование порядка |
| --- | --- | --- | --- | --- | --- | --- |
| P-01 | Политика GC-лимита (5 файлов: `memlimit.go` + `memlimit_ram_*.go`) | `desktop/` → `core/` | Низкий | S | — | Чистая функция, один вызов (`startup.go:250`), нулевые зависимости — идеальный «разогревающий» перенос |
| P-02 | Путевые решения save-диалога (`pickSaveDialogDir`, `ensureMarkdownExtension`, `resolveWritePath` `dialog_save.go:81,93,106`) | `desktop/` → `core/` | Низкий | S | — | Чистые решения по ФС, биндинг остаётся |
| P-03 | CSP-константа (`productionCSP` `csp.go:19`) | `desktop/` → `core/` | Низкий | S | — | Политика отделяется от Wails-middleware; тест `csp_test.go:16` сохраняется на адаптере |
| P-04 | Персистентность `dialog_state.json` (`dialog_state.go`) | `desktop/` → `backend/` | Низкий | S | — | Первый перенос в `backend/` — проверяет, что путь `config.DialogStatePath` доступен из нового места |
| P-05 | Политика дедлайна shutdown (`EffectiveShutdownDeadline` `shutdown_watchdog.go:48`, потолки `:26-41`) | `desktop/` → `core/` | Низкий | S | — | Чистая арифметика; сторож остаётся |
| P-06 | NVIDIA explicit-sync + чистое отображение GPU-политики (`webview_nvidia_explicit_sync.go`, `webviewGpuPolicyFromCanonical` `webview_gpu_policy.go:42`) | `desktop/` → `core/` | Низкий | M | — | Все зависимости инъектируемы; Wails-сборка опций остаётся |
| P-07 | Примитив single-instance-блокировки (`InstanceLock` `singleinstance.go:65`, `AcquireSingleInstanceLock` `:79`, `WaitForInstanceLockRelease` `:126` + `singleinstance_unix.go`/`_windows.go`) | `desktop/` → `core/` | Средний | M | P-01 | Трогает межпроцессную блокировку; после «разогрева», но до крупных переносов |
| P-08 | Чистый разбор HITL-wire-протокола (`extractPayload` `event_handlers.go:18`, `parseConfirmDecision` `:34`, `parseStepLimitResponse` `:77`, `parsePlanApprovalDecision` `:96`, `parseGoalProposalDecision` `:108`, `confirmDecisionString` `:137`, `parseAskUserAnswers` `:607`, `deliverResponse` `:175`) | `desktop/` → `core/` (новый пакет HITL-wire) | Средний | M | — | Первый шаг к разгрузке 702-строчного файла; обработчики пока остаются |
| P-09 | Батчер событий (`event_batcher.go` минус 3 метода `*App`) | `desktop/` → `backend/` | Средний | M | P-08 | Транспортная логика; порядок важен, т.к. обработчики событий остаются в `desktop/` |
| P-10 | Сервис judge-оценки (`runJudgeEvaluation` `event_handlers.go:297`, `beginJudgeEvaluation` `:284`, `finishJudgeEvaluation` `:290`) | `desktop/` → `backend/` | Средний | M | P-08 | Требует выноса бюджета 120 s и dump-writer в сервис; каналы доставки остаются в `desktop/` |
| P-11 | Подсистема web-access — **сплит на 3 PR**: (a) чистая классификация сертификатов и URL-хелперы (`classifyWebAccessCert` `app.go:1362`, `webAccessCertCoversHost` `:1397`, `dnsSANCovers` `:1418`, `webAccessURLHost` `:845`, `webAccessBrowserURL` `:858`); (b) жизненный цикл сайдкара (`startWebAccessLocked` `:1502`, `stopWebAccessLocked` `:1558`, `restartWebAccessOrRollbackLocked` `:1441`, `webAccessMountAllowed` `:1494`); (c) control-RPC `:881-1360` | `desktop/app.go` → `backend/` (+ `core/` для (a)) | Высокий | L | P-06 | Самая крупная и самая security-чувствительная часть; только после появления опыта на P-04/P-09 |
| P-12 | Запуск векторного индекса (`vectorIndexLauncher` `startup_phases.go:1094`, `runVectorIndexInit` `:1160`, `verifyEmbedderGPU` `:1460`, `resolveModelPath` `startup.go:696`, `resolveONNXLibPath` `:723`) | `desktop/` → `backend/` | Высокий | L | P-07 | Конкурентная логика (epoch/ready); `FrontendAPILifecycle.SetVectorIndexEnabledHook` `backend/frontend_api.go:1003` уже даёт нужный шов |
| P-13 | GPU-watchdog + посмертный анализ (`webview_gpu_watchdog.go`, `webview_gpu_postmortem.go`, `webview_gpu_watchdog_relaunch_*.go`) | `desktop/` → `backend/` | Средний | L | P-06 | Требует инъекции `quit`/`relaunch`; персистентность уже в `backend.WebviewGpuState` |
| P-14 | Остальные фазы запуска (`initDatabase` `startup_phases.go:358`, `initStores` `:397`, `initTerminalManager` `:372`, `preloadProjectsAndSessions` `:433`, `initTools` `:132`/`:213`, `initEmbeddedLLM` `:1575`, `initChatGPTAuth` `:1600`, `startUpdateCheckerBackground` `:1550`, `startAutoFetchBackground` `:1565`, `deref*` `:1492-1550`) — **сплит на 4–5 PR по подсистемам** | `desktop/` → `backend/` | Средний-Высокий | L | P-09, P-12 | Финальная фаза: после неё `desktop/` = lifecycle + bindings + OS + HITL, как в `layers.md` |

### 6.3. Целевое состояние после эпика

```
desktop/   ≈5.5k строк: lifecycle (startup/shutdown/dom-ready/close-guard),
           Wails bindings (28 методов *App + promoted FrontendAPI),
           HITL plumbing (pending maps + GetPendingActions + event adapters),
           OS-shell integration (notifications, single-instance options,
           power/wake, X11 activation, native dialogs, wails logger)
backend/   + web-access lifecycle & control RPCs, judge service,
           vector-index launcher, startup phases, dialog/window persistence,
           event batcher, GPU watchdog
core/      + GC memory-limit policy, CSP policy constant, HITL wire protocol,
           save-path decisions, shutdown deadline policy, GPU policy mapping,
           nvidia env decision, single-instance lock primitive
```

### 6.4. Что останется открытым (сознательно)

- `notifications_linux.go` (590 строк D-Bus) и `window_activation_linux.go` (351 строка X11/cgo) останутся в `desktop/` — это ОС-интеграция, а не доменная логика; их возможный переезд в `internal/` — отдельный вопрос вне скоупа issue #42 («Out of scope: refactors unrelated to the desktop/core/backend boundary»).
- `webaccess_inbound.go` (`dispatchWebInboundFrame` `webaccess_inbound.go:28`) — кандидат на `backend/` **после** P-11, когда жизненный цикл сайдкара переедет; сейчас он держит ссылки на HITL-обработчики `App`, поэтому перенос раньше P-10 создал бы обратный шов.

---

## 7. Результаты проверок issue

Все проверки выполнены на чистом дереве (production-код в ходе аудита не изменялся), 2026-11-07, общее время 2 мин 31 с.

| Команда | Результат | Детали |
| --- | --- | --- |
| `make build` | **EXIT=0** | `wails build` (VERSION_LDFLAGS) → `make fetch-onnx` → `make fetch-embedding-model`. ONNX Runtime 1.28.1 уже установлен (`build/bin/c0wrk-desktop.app/Contents/MacOS/libonnxruntime.dylib`); модель на месте, `SHA256 verified` для `jina-v2-small.onnx` и `jina-v2-small-tokenizer.json` |
| `make lint` | **EXIT=0** | `make fmt-check` (gofmt по `main.go internal core backend desktop`) + `golangci-lint run` (depguard `core-no-backend`, errcheck, govet, staticcheck, gocritic, revive, prealloc, perfsprint …) + `cd frontend && npm run lint` |
| `make test` | **EXIT=0** | Go: `go test -count=1 -v -skip '^TestStress' ./...` — 42 пакета `ok`, **0** `FAIL`, 0 `--- FAIL`; frontend: vitest **443 файла / 7049 тестов passed** |
| `go test ./desktop/... -count=1` | **EXIT=0** | `ok github.com/v0lka/c0wrk/desktop 2.912s` |

Дополнительно зафиксировано как факт для будущих PR:

- Импортов `github.com/wailsapp/wails` в `backend/` и `core/` — **0** (`grep -rl 'wailsapp/wails' backend/ core/` — пусто). Это инвариант, который обязаны сохранить все переносы.
- Нарушений объявленного направления импортов нет: `core → backend` запрещён depguard-ом `.golangci.yml:99-105` и фактически отсутствует; `backend/core → desktop` невозможно без цикла.

---

## 8. Не подтверждено / ограничения аудита

1. **Строка `make test-stress` и `make vulncheck` не запускались.** Issue #42 требует только `make build` / `make lint` / `make test`; pre-PR checklist `AGENTS.md` добавляет `make test-stress` и `make vulncheck` — их следует прогонять в каждом PR миграции, но аудит их не измерял.
2. **Windows/Linux-сборки не проверялись.** Аудит выполнен на darwin/arm64. Файлы с build tags (`memlimit_ram_windows.go`, `singleinstance_windows.go`, `notifications_linux.go`, `window_activation_linux.go`, `webview_gpu_watchdog_relaunch_linux.go`, `powerstate_darwin.go`/`_other.go`) прочитаны, но их компиляция на целевых платформах не подтверждена — это должно стать отдельным шагом соответствующих PR.
3. **Оценка объёма переносимого кода (≈3 200 строк, ≈37 %) — приблизительная.** Она получена по границам функций из инвентаризации, а не инструментальным подсчётом AST; допуск ±15 %.
4. **Тестовое покрытие переносимых участков не измерялось** (coverage не снимался). Утверждение «логика не покрыта тестами backend-уровня» в A-1/A-3 относится к *расположению* тестов (они в `desktop/*_test.go` и требуют `App`), а не к проценту покрытия.
5. **`internal/webaccess` как целевой пакет не рассматривался.** Issue ограничивает целевые пакеты `core/` и `backend/`; в отчёте `internal/` упомянут только как вопрос будущего эпика для `notifications_linux.go` / `window_activation_linux.go`.
6. **ADR для будущих переездов не создан.** Согласованный план, вероятно, потребует нового ADR (по образцу ADR-009/011) с фиксацией правила «desktop = lifecycle + bindings + OS + HITL» — это решение уровня governance, вынесено на согласование вместе с планом, а не принято аудитом единолично.

---

## 9. Согласование с эпиком #84 (v2: daemon + клиенты TUI/ACP)

> Добавлено 2026-11-07 при сверке с эпиком [«c0wrk v2 — daemon architecture»](https://github.com/v0lka/c0wrk/issues/84). Полная сверка, инвентаризация устаревших данных эпика и предлагаемые тексты правок: [`docs/development/epic-84-rebaseline.md`](epic-84-rebaseline.md).

Эпик готовит топологию «daemon + клиенты»: gRPC-транспорт, GUI/TUI/ACP как равные клиенты. Его фазы 1a/1b/1c (#74 composition root, #75 HITL+judge, #76 глобальные события) перекрываются с настоящим планом. Направления совпадают (desktop → тонкий хост), конфликтов направлений нет; чтобы выполнение P-01…P-14 делало эпик **легче**, а не наоборот, план получает пять уточнений.

### 9.1. Уточнения к пунктам плана

| Пункт | Уточнение | Зачем |
| --- | --- | --- |
| P-07 | Примитив `InstanceLock` переносится в `core/` как переиспользуемый; desktop-семантика ADR-075 (relay в первый desktop-инстанс) остаётся в `desktop/` | #79 берёт тот же примитив для daemon-lock на agent-dir — эпик прямо требует «absorb rather than re-implement» |
| P-10 | Judge-сервис переносится как транспортно-нейтральный (без `App`/`wailsCtx`), с API, который #75 поглощает в `backend/interaction` без повторного переезда | #75 переносит `runJudgeEvaluation` + `judgeWG` в `backend/interaction`; посадка «в произвольное место backend» заставила бы #75 двигать код дважды |
| P-12, P-14 | Переносимые фазы обязаны быть транспортно-нейтральными: emit/quit-хуки — параметрами (образец — действующий шов `FrontendAPIConfig.EmitEvent`, `backend/frontend_api.go:461`), ни одного Wails-типа; место посадки — пакеты, которые будет вызывать ассемблер #74 | После P-14 фаза 1a (#74) сводится к обёртке уже-backend-фаз в `backend/appbuild` + `HostServices`, а не к повторному переносу |
| P-13 | Политика/машина состояний GPU-watchdog — в `backend/`, но владение и запуск остаются за GUI-хостом; daemon его никогда не стартует (у daemon нет webview) | #80 относит webview-специфику к client-native; «перенос в backend» не должен означать «daemon этим владеет» |
| A-11 (рекомендация из находки) | Внутрипакетный перенос типов ожиданий в `desktop/pending_state.go` **отменяется**; эти типы переедут в `backend/interaction` в рамках #75 | Иначе двойной переезд: сначала внутри `desktop/`, затем в #75 |

### 9.2. Эффект плана для эпика (сводка)

- **P-01…P-06** — нейтрально/в плюс: desktop тоньше, чистые политики переиспользуются (P-05 — graceful shutdown демона #79; P-03 — CSP-политика, тестируемая без Wails).
- **P-07** — плюс: примитив single-instance для #79 (поглощение ADR-075).
- **P-08** — плюс: база translate-and-call-адаптеров #75 и ACP-маппинга HITL в #83.
- **P-09** — плюс: коалесер событий для daemon event-stream (#76/#77).
- **P-10** — плюс при условии уточнения §9.1.
- **P-11** — плюс: daemon (#79) владеет жизненным циклом сайдкара web-access.
- **P-12/P-14** — плюс при условии уточнения §9.1: это и есть большая часть фазы 1a.
- **P-13** — нейтрально при условии уточнения §9.1.

### 9.3. Governance-замечание

Раздел 8, пункт 6 предлагал отдельный ADR с правилом «desktop = lifecycle + bindings + OS + HITL». Поскольку ADR эпика (#73, следующий свободный номер — **116**: максимум сейчас 115, слот 062 занят) меняет судьбу HITL-плумбинга (фаннелы переезжают в `backend/interaction` по #75), правило для ADR переносов надо формулировать с явной оговоркой «до фазы 1b эпика» — либо объединить оба решения в одном ADR-116. Решение за владельцем; план P-01…P-14 от этой оговорки не зависит (он HITL-фаннелы не трогает).
