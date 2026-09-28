# Embedded LLM (Ternary Bonsai 2 27B) — диагностика медленного инференса

Дата: 2026-09-27. Хост: Apple M4 Max, 128 ГБ, backend Metal, runtime `prism-b10735-842b188`, packing PQ2_0.

## Жалоба

«Встроенная Bonsai отвечает 5–7 минут на запрос, тогда как qwen3.8-27b Q4_K_XL на этом же железе даёт 25–30 tok/s». Подозревались: незадействованный GPU, неиспользуемая VRAM, неверные параметры.

## Вердикт (кратко)

GPU задействован, decode на коротком контексте — 31 tok/s (паритет с эталоном). Проблема НЕ в «медленной модели как таковой», а в комбинации:

1. **Prefill этой модели в llama.cpp/Metal принципиально медленный** (~80–220 tok/s, деградирует с позицией) — это подтверждает и сам вендор («prompt processing is the practical bottleneck on very long inputs»).
2. **c0wrk скармливает серверу промпты по 40–50K токенов** — из-за отравленного окна контекста 262144 (см. ниже) один prefill = 8–10 минут.
3. **Промпт-кэш регулярно сбрасывается** мелкими служебными запросами на тот же односотовый сервер → каждый шаг агента оплачивает prefill заново.

## Подтверждённые факты (измерения на живом сервере, port 54386)

### Командная строка запуска (из ps и лога)

```
llama-server -m .../Ternary-Bonsai-2-27B-PQ2_0.gguf --host 127.0.0.1 --port 54386 \
  -fit on -fitc 65536 -fa on -c 0 -ctk f16 -ctv f16 -np 1 \
  --temp 1.0 --top-p 0.95 --top-k 20 --jinja --no-ui --mmproj ... --image-max-tokens 1024
```

Результат: `n_ctx_slot = 262144` (максимум модели), n_threads=12, KV f16.

### Скорости

| Метрика | Значение | Источник |
|---|---|---|
| Decode, контекст ~0 | **31.1 tok/s** (task 0: 30 ток / 0.93 с) | print_timing |
| Decode, контекст 41.7K | **15.2 tok/s** (task 59) | print_timing |
| Prefill 806 токенов | 220 tok/s | замер /completion |
| Prefill 4014 | 169 tok/s | замер /completion |
| Prefill 7222 | 142 tok/s | замер /completion |
| Prefill 41 705 токенов | **76 tok/s = 546 с (9.1 мин)** | task 3, print_timing |
| Prefill 41 700 | 82 tok/s = 507 с | task 59 |
| Prefill 47 104 | ~80 tok/s = 590+ с | task 135 |

Аппроксимация стоимости prefill: **~4.2 мс/токен + 0.4 мкс × позиция** (база в 4–8 раз хуже плотной 27B Q4 на Metal; рост — внимание по KV). Маржинальная скорость к концу 41K-промпта падает до 37 tok/s.

### GPU работает (гипотеза «не задействуется GPU» опровергнута)

31 tok/s decode на 27B недостижим на CPU (было бы 2–5 tok/s). Метал-сборка, полная выгрузка слоёв.

### Причина №1: контекст 262144 вместо tiered ≤131072

- All-Auto Tuning → `fit on, -c 0`. У форка `-fitc` — это **МИНИМУМ**, а не максимум (`-fitc, --fit-ctx N  minimum ctx size that can be set by --fit`). На 128-ГБ хосте fit раздул контекст до модельного максимума 262144.
- Вендор прямо предупреждает: «Bare `-c 0` uses the model's full training context (262K) regardless of available memory», их скрипт использует RAM-tiered `-c` (максимум **131072** для 27B при >71 ГБ). Собственная tier-лестница c0wrk (`contextSizeFor`) тоже даёт 131072 — но all-auto путь её обходит.
- `persistEmbeddedContext` (backend/frontend_api_embedded.go:720+) записывает живое окно из `/props` в tier-1 override `llm.models."Bonsai 2 27B".context_window` — в живом конфиге сейчас **`context_window: 262144`**, хотя config.example.yaml заявляет «RAM-tiered; never the fork's 262144 maximum». Два замысла противоречат друг другу; read-back побеждает.
- Последствия каскадом: компакция срабатывает на 85% окна ≈ **222K токенов** (пороги 85/92/98%), OutputLimit = min(32768, 262144/4) = **32768** на запрос, промпты агента растут до 40–50K+ без ограничений. KV f16 = 64 КБ/токен → 16.4 ГБ только кэша.

### Причина №2: сброс промпт-кэша служебными запросами (один слот)

Сервер запущен с `-np 1` (один слот). Любой мелкий запрос с другим промптом стирает кэш слота, и следующий большой запрос обрабатывается с нуля:

- task 3 (41 705 ток, cache 0, 546 с) → task 58 (**4 токена**, 30 в ответ — служебный вызов) → task 59 (41 700 ток, **cache 0**, 507 с заново).
- Контроль: task 2365 (51 774 ток) сразу после другого большого — **cache 51575** (кэш работает, когда между ними никто не влез).
- c0wrk реально шлёт служебные вызовы на embedded: в логе `llm: request provider: embedded, 110 in / 30 out` (генерация заголовка, builder.go:986 MaxTokens:30).

### Причина №3: запросы без стриминга + таймаут

- sp4rk никогда не стримит (поля `stream` нет в провайдере; `/slots` показывает `"stream": false`) → пользователь слепо ждёт весь prefill+decode.
- `timeouts.llmRequestTimeout: 600` → задачи отменяются по таймауту: в логе задачи с промптами 19234/20480 токенов и `processed=0/decoded=0` (клиент отключился), работа потеряна, ретраи снова платят prefill.

### Причина №4 (фундаментальная): prefill форка на этой архитектуре медленный

Модель — гибрид qwen35: 64 блока, из них только 16 полн attention (interval=4), 48 SSM/GDN-слоёв, плюс рантайм Walsh–Hadamard-преобразование активаций. База ~4.2 мс/токен на prefill — уровень вендорского форка на Metal, c0wrk конфигом это не лечится. Вендор сам отмечает bottleneck и советует MLX на Mac (вне текущего дизайна пина llama.cpp). Таблицы вендора: M4 Pro ≈ 18 tok/s, M5 Max (MLX) ≈ 47 tok/s decode — наши 31 tok/s на M4 Max в норме.

## Рекомендации (переносимо на все ОС)

1. **Немедленно на этом хосте (без кода)**: Settings → Embedded LLM → Tuning → context: exact, 65536 (или 131072), перезагрузить модель. Fit выключится для контекста, `-c` зафиксируется, read-back запишет корректное окно, компакция начнёт работать в разумном диапазоне (85% от 65536 ≈ 55K).
2. **Код-фикс A (launch)**: для машин с избытком памяти не отдавать контекст fit'у — использовать собственную лестницу `contextSizeFor` (max 131072), как делает вендорский скрипт; fit оставить подбирать только `-ngl`.
3. **Код-фикс B (read-back)**: `persistEmbeddedContext` должен клампить окно до tier-значения (`min(live, tier)`), выполняя заявленный инвариант конфига; иначе компакция/OutputLimit продолжат масштабироваться от 262144.
4. **Код-фикс C (кэш)**: не пускать служебные вызовы (title/commit/optimize) на embedded-сервер во время активной сессии — отложить до простоя агента; это устраняет повторные 8-минутные prefill.
5. **Код-фикс D (бюджет вывода)**: cap на max_tokens для embedded (например 8192 по умолчанию) — 32768 токенов при ≤31 tok/s это до 17–36 минут худшего случая.
6. **Опционально**: kv_cache_type q8_0 для больших контекстов (вдвое меньше KV-трафика/памяти), стриминг для embedded-провайдера (UX + устойчивость к таймаутам), слежение за новыми релизами форка (upstream PR #27779 — hadamard в mainline; улучшения Metal-kernels возможны только там).

## Реализованные исправления (2026-09-27)

1. **Код-фикс A (launch)** — `core/embeddedllm/plan.go`: fit-путь больше не оставляет контекст фиту. `planTargetContext` возвращает RAM-tier (как explicit-путь) с полом `-fitc`, а `planShape` всегда записывает конкретный `plan.ContextSize` — команда запуска получает явный `-c` (max 131072), и `--fit` выбирает только `-ngl`. Пиновка `TestPlanDefaultsAreTheAllAutoShape` / `TestPlanFitExclusivity` / source-guard `TestPackageSourcesNeverEmitBannedContext` обновлены под контракт «план всегда рендерит `-c`».
2. **Код-фикс B (read-back)** — `core/embeddedllm/server.go` `recordEffectiveContext`: кламп reported-окна к запущенному `-c` (`launch.Spec.ContextSize`) ДО записи в оба хранилища (манифест + `llm.models.context_window` через `persistEmbeddedContext`). Отравленный read-back (сквоттированный порт, устаревший fit-сервер) больше не раздувает компакцию и OutputLimit. Тест `TestLoadClampsTheReadbackToTheLaunchedContext`; E2E-тесты read-back снабжены планом, чтобы reported-фигура была детерминированной.
3. **Код-фикс C (кэш)** — `backend/frontend_api_embedded.go`: `serviceEmbeddedGate` = ожидание простоя агента (`waitForEmbeddedAgentIdle` по seam `activeSessionCount`, опрос `Manager.ActiveSessions`) + прежняя готовность модели. Интерактивные RPC (commit message, prompt optimization) ждут 20 с (`embeddedServiceIdleWaitInteractive`) и отказывают с действенным сообщением; фоновая генерация заголовка ждёт 10 мин (`embeddedServiceIdleWaitBackground`) и пропускает переименование при неудаче (уже существующее поведение gate-фейла). Провайдер не embedded — gate инертен.
4. **Код-фикс D (бюджет вывода)** — `backend/config`: `EmbeddedLLMOutputLimit = 8192`, ставится sync'ом (`setEmbeddedOutputLimit`) как `llm.models."Bonsai 2 27B".output_limit` с клампом под window/4; пользовательский `output_limit` не перетирается (`TestSyncEmbeddedProviderSeedsTheOutputLimit`).
5. **KV q8_0 для длинных контекстов** — `core/embeddedllm`: `longContextKVThreshold = DefaultFitMinContext` (65536); выше порога авто-лестница точностей стартует с q8_0 вместо f16 (вдвое меньше KV-трафика/памяти при вендорских ~1% потерь; 131072×f16 ≈ 8 ГБ → 4 ГБ).

Не выполнено (осознанно): стриминг для embedded-провайдера и слежение за релизами форка — по явному указанию пользователя из п.5 берётся только kv_cache_type q8_0.
