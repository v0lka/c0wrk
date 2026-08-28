# Small-LLM Profile: обоснование значений по умолчанию

**Дата:** 2026-08-28
**Целевая модель:** Qwen3.8-27B-MTP (dense 27B, гибридный thinking, MTP — decode-side speculative decoding)
**Метод:** каждое значение профиля сверено с внешними источниками (официальная документация Qwen/vLLM, карточка модели, независимые замеры, практики индустриальных агентных фреймворков) и с кодом (`backend/config/defaults.go`, `core/builderconfig.go`, sp4rk `llm/reasoning.go`, `llm/provider_openai.go`, `prompt/sampling.go`).
**Статус верификации:** все URL из раздела «Источники» получены и проверены в ходе исследования (fetch/search), ни один не приведён по памяти.

---

## 1. Сводная таблица вердиктов

| # | Параметр | Дефолт | Базлайн executor | Вердикт | Уверенность |
|---|----------|--------|------------------|---------|-------------|
| 1 | `small_llm.enabled` | `false` | — | ✅ подтверждён | High |
| 2 | `essential_tools.enabled` | `false` | — | ✅ подтверждён | High |
| 3 | `essential_tools.always_present` | 12 инструментов | — | ✅ подтверждён (13 с protected ≤ safe zone) | High |
| 4 | `essential_tools.max_tools` | `16` | — | ✅ подтверждён (<20) | High |
| 5 | `essential_tools.compact_descriptions` | `false` | — | ✅ подтверждён (консервативно) | Medium-High |
| 6 | `system_prompt.lite` | `false` | — | ✅ подтверждён | Medium-High |
| 7 | `system_prompt.few_shot` | `false` | — | ✅ подтверждён (кандидат на A/B) | Medium |
| 8 | `system_prompt.reasoning_scaffold` | `false` | — | ✅ подтверждён | Medium |
| 9 | `sampling.enabled` | `false` | — | ✅ подтверждён | High |
| 10 | `sampling.temperature` | `0` = наследовать (qwen-пресет **0.6**) | — | ❌ пресет устарел → **1.0** | High |
| 11 | `sampling.top_p` | `0` = наследовать (0.95) | — | ✅ подтверждён (thinking 0.95; instruct 0.80) | High |
| 12 | `sampling.top_k` | `0` = наследовать (20) | — | ✅ подтверждён (оба режима 20) | High |
| 13 | `sampling.repetition_penalty` | `0` = наследовать (сервер 1.0) | — | ✅ подтверждён (не поднимать) | High |
| 14 | `sampling.reasoning_effort` | `""` = наследовать (→ **xhigh**) | — | ❌ → **`"medium"`** (после починки plumbing) | High |
| 15 | `sampling.presence_penalty` | **поля нет** | — | ⚠️ пробел → добавить (инструкт-режим) | Medium |
| 16 | `loop_hardening.enabled` | `false` | — | ✅ подтверждён | High |
| 17 | `repeat_nudge_threshold` | `2` | 3 | ✅ подтверждён (агрессивнее эталона 3) | Medium |
| 18 | `parse_error_abort_threshold` | `3` | 3 | ✅ подтверждён (= базлайн) | Medium |
| 19 | `fruitless_nudge_threshold` | `3` | 4 | ✅ подтверждён | Medium |
| 20 | `fruitless_abort_threshold` | `5` | 6 | ✅ подтверждён | Medium |
| 21 | `same_tool_repeat_nudge_threshold` | `4` | 6 | ✅ подтверждён | Medium |
| 22 | `context.enabled` | `false` | — | ✅ подтверждён | High |
| 23 | `compaction.keep_last` | `6` | 10 | ✅ направление подтверждено | Medium |
| 24 | `compaction.block_size` | `5` | 7 | ✅ направление подтверждено | Medium |
| 25 | `compaction.trigger_percent` | `80` | 85 | ✅ подтверждён | Medium-High |
| 26 | `tool_output_keep_last_n` | `2` | 3 | ✅ подтверждён | Medium-High |
| 27 | `context.output_token_reserve` | `8192` | 8192 | ❌ → **16384** | High |

Контекст кода: `backend/config/defaults.go:391–450` (сидирование), `core/builderconfig.go` (модель билдера), `config.example.yaml:713+` (документация профиля).

---

## 2. Детальное обоснование по блокам

### 2.1 Мастер-переключатель

**`enabled: false` — подтверждён [High].**
Профиль — manual-only по дизайну: оптимизации для «малых» моделей не должны молча менять поведение. Для Qwen3.8-27B это тем более верно: модель показывает Terminal-Bench 2.1 = 73.0 (против 63.4 у Qwen3.6-27B) и DeepSWE 1.1 = 42.2 (против 13.3) [4] — это агентный уровень, не «малый». Ни один внешний источник не рекомендует принудительно сужать тулсет/промпт для моделей такого класса по умолчанию; все практики «tool narrowing» описаны как опциональная мера под конкретный workload [13][14].

### 2.2 Essential Tools

**`always_present` = 12 инструментов — подтверждён [High].**
Список (`read_file, write_file, edit_file, list_directory, glob, ripgrep, bash_exec, semantic_search, store_fact, search_facts, ask_user, finish`) закрывает полный цикл задачи: навигация → чтение → правка → исполнение → поиск по смыслу → память → взаимодействие → завершение. Вместе с protected-инструментами (5, из них 4 пересекаются со списком) гарантированный набор = 13 ≤ 20 — ниже порога «до 20 инструментов можно обойтись без retrieval-слоя» [14]. Точность выбора инструментов деградирует с ростом набора: без мер по селективности точность падает до ~13% [13]; академическое исследование числа видимых инструментов подтверждает эффект [15]. 13 — уверенно в safe zone.

**`max_tools: 16` — подтверждён [High].**
Индустриальный ориентир: <20 инструментов — без tool-RAG [14]; «safe zone» 10–20 с идеалом 5–15 [13]. 16 оставляет 3 свободных слота для router-matched инструментов поверх гарантированных 13. Риск зафиксирован в п. 4.

**`compact_descriptions: false` — подтверждён (консервативно) [Medium-High].**
Описание инструмента — это промпт для селекции: качество описаний напрямую влияет на точность выбора [13]. Экономия ~150–400 токенов на инструмент (≈2–6K на 13–16 инструментов) не оправдывает риск путаницы инструментов с пересекающейся семантикой (`glob` vs `ripgrep` vs `semantic_search` в always-present одновременно). Для модели агентного уровня (см. 2.1) консервативный дефолт корректен.

### 2.3 System Prompt (`lite/few_shot/reasoning_scaffold: false`)

**Все три — подтверждены [Medium-High].**
- Qwen3.8-27B пост-тренирована под популярные harness'ы: собственные бенчмарки Qwen выполнены в Claude Code harness (QwenSWEBench: Claude Code harness, max_tokens=32768, temp=1.0 [3]; DeepSWE 1.1 — Claude Code harness [3]); независимо модель прогоняла реальный агентный цикл в репозитории с дефолтным поведением [4]. Значит, полные (harness-подобные) системные промпты для неё — «родная» среда, а Lite-свап скорее навредит.
- Few-shot примеры значительно улучшают tool-calling у слабых/средних моделей (LangChain: 16% → 52% на сложной селекции, прошлый этап исследования), но для harness-пост-тренированной 27B ожидаемый эффект маргинален, а токен-стоимость постоянна. Дефолт `false` корректен; включение — кандидат на A/B-тест, не на дефолт.
- Reasoning scaffold (шаблон трёхшагового мышления) конфликтует с нативным thinking-режимом Qwen3.8 (thinking on by default [1][4]) — модель уже имеет встроенный CoT-механизм; внешний scaffold — дублирование. `false` корректен.

### 2.4 Sampling

**Дизайн «0 = наследовать vendor-пресет» — подтверждён [High].**
Наследование вместо сидирования защищает от повторения задокументированной внутренней регрессии («27-30B»: принудительные 0.1/0.9 ломали семейства с иными тюнингами). Это соответствует официальной доктрине Qwen: для каждого режима модели рекомендованы свои значения, и «чужие» значения вредят [7]. Сам механизм корректен; **устарело содержимое пресета** (см. ниже).

**`temperature` (наследует qwen-пресет sp4rk = 0.6) — ОПРОВЕРГНУТО → пресет 1.0 [High].**
- Пресет 0.6 в sp4rk (`prompt/sampling.go:53`) ссылается на рекомендацию qwen.readthedocs.io эры Qwen3 (гибрид 2504: «thinking mode: temperature=0.6»).
- Для Qwen3.8 официальная рекомендация thinking-режима: **temperature=1.0**, top_p=0.95, top_k=20, min_p=0, presence_penalty=0.0, repetition_penalty=1.0 [8 — карточка Unsloth-GGUF/ModelScope; 2 — Unsloth docs: все run-команды с `--temp 1.0`].
- Собственные агентные бенчмарки Qwen выполнены при temp=1.0 [3].
- Для инструктивного (non-thinking) режима рекомендация иная: 0.7 / top_p 0.8 / top_k 20 [7] — но у Qwen3.8 thinking включён по умолчанию [1][4], поэтому пресет семейства должен соответствовать thinking-режиму.
- Вердикт: обновить qwen-пресет в sp4rk до 1.0 (с комментарием-источником), либо сделать пресет era-aware.

**`top_p` (наследует 0.95) — подтверждён [High].** Thinking-режим: 0.95 [2][8]; бенчмарки Qwen: top_p=0.95 [3]. Instruct-режим использует 0.80 [7] — отличается, но дефолт режима модели = thinking.

**`top_k` (наследует 20) — подтверждён [High].** 20 рекомендуется для обоих режимов [2][7][8].

**`repetition_penalty` (наследует; серверный дефолт 1.0) — подтверждён [High].** Официально: repetition_penalty=1.0 в thinking-режиме [8]; Qwen прямо рекомендует против повторов использовать presence_penalty (0–2), а не repetition_penalty, т.к. высокие значения ведут к language mixing и деградации [7].

**`reasoning_effort: ""` (наследовать) — ОПРОВЕРГНУТО → `"medium"` [High].**
- Наследование для Qwen3.8 означает дефолт шаблона = **xhigh** — самый дорогой из трёх уровней (xhigh/medium/low) [4]; «если вы бенчмаркали Qwen3.8, не задав reasoning_effort, вы измерили самый дорогой режим» [4].
- Замеры: SVG-пеликан — 22 276 reasoning-токенов за 21 минуту (против 3 715 output-токенов за 137 секунд с выключенным thinking); кодинг-задача — 17 576 reasoning-токенов против 1 021 у Muse Glimmer 30B [4].
- `medium` — нейтральная точка: шаблон не инъектирует дополнительных инструкций, ошибок не возвращает [4]; в обсуждении модели переход на medium улучшал результаты, устраняя «thinking loops» [9].
- Unsloth включает `--reasoning-effort medium` в рекомендованную команду запуска [2].
- Контраргумент (зафиксирован): Willison советует начинать с low/off [4]; низкий effort в мульти-turn агентных задачах может увеличить суммарные токены из-за ретраев (прошлый этап). `medium` — консенсусный дефолт; low/off остаются пользовательскими опциями.
- **Блокер:** в текущем sp4rk значение доходит неправильно (см. п. 3, R1) — сначала plumbing, потом дефолт.

**`presence_penalty` — поля нет в профиле — пробел → добавить [Medium].**
Официальный анти-повторный рычаг Qwen: «adjust presence_penalty between 0 and 2 to reduce repetitions» (с оговоркой о language mixing при высоких значениях) [7]. Нужен прежде всего для instruct-режима (thinking-режим рекомендует 0.0 [8]) и как альтернатива опасному repetition_penalty. Поле `PresencePenalty` в `llm.ChatRequest` sp4rk уже существует и отправляется (официальное поле OpenAI-схемы) — недостаёт только конфиг-поля в профиле c0wrk. Дефолт 0 (= наследовать).

### 2.5 Loop Hardening (`2 / 3 / 3 / 5 / 4`)

**Направление (жёстче базлайна) — подтверждено; точные числа — внутренняя калибровка [Medium].**
- Паттерн «nudge → abort» (сначала мягкое вмешательство, потом жёсткий стоп) — индустриальный эталон: Pydantic Deep Agents — stuck-loop detection **on by default**, порог `max_repeated=3`, действие `warn` (ModelRetry — модель самокорректируется), три паттерна: идентичные повторы, A-B-A-B, no-op [18].
- OpenClaw: no-progress circuit breaker включён по умолчанию после инцидента с бесконечным циклом неизвестного инструмента [16].
- `repeat_nudge=2` (базлайн 3): на шаг агрессивнее эталона Pydantic (3). Контрточка: слишком строгие настройки блокируют легитимные повторы вызовов [16 — ClawCentral: «guard disabled by default… strict settings can block legitimate repeated calls»]. Не опускаться ниже 2.
- `parse_error_abort=3` (= базлайн): правильно не ужат — parse-ошибки это capability-провал, а не зацикливание; для малой модели порог равен базлайну достаточно.
- `fruitless_nudge=3` (базлайн 4), `fruitless_abort=5` (базлайн 6): ~на 25% раньше ловят no-op циклы — соответствует no-op паттерну [18]; цена задокументированных runaway-циклов высока ($4 000 счетов [17]).
- `same_tool_repeat_nudge=4` (базлайн 6): раньше ловит A-B-A-B/идентичные повторы (у Pydantic идентичные — с 3 [18]).
- Внешнего стандарта на точные числа нет; единственная честная калибровка — телеметрия c0wrk (доля срабатываний, спасших цикл, vs ложных срабатываний).

### 2.6 Context

**`trigger_percent: 80` (базлайн 85) — подтверждён [Medium-High].**
- Пороговая компакция — механизм `contextTokens > contextWindow − reserveTokens`; «ранний триггер — это то направление, которое вам всё равно нужно» [5].
- Исследования context rot: frontier-модели пропускают целевое действие в 2–30 раз чаще после 800K токенов benign-активности; одиночный семантический дистрактор уже снижает качество (Chroma, 18 моделей) [5 — обзор]. Чем меньше эффективное окно у «малой» модели, тем раньше стоит компактировать — 80% умеренно агрессивнее базлайна 85%, обосновано.
- Claude Code компактирует при ~90% с агрессивным пользовательским диапазоном 40–70% (прошлый этап) — 80% внутри коридора.

**`keep_last: 6` (базлайн 10), `block_size: 5` (базлайн 7) — направление подтверждено [Medium].**
Хвост — единственная дословная память после компакции: «всё остальное модель знает из summary, системного промпта и перечитывания репозитория» [5]. Меньший хвост экономит окно, но чреват потерей свежих правок (WorkOS рекомендует УВЕЛИЧИВАТЬ keep-бюджет при симптоме «после компакции модель потеряла правку прошлого хода» [5]) — 6 сообщений против базлайна 10: умеренное сжатие; следить за телеметрией. BlockSize 5 — меньше токенов на summary-блок, дешевле вызов компакции (сам компакшн-вызов платит output-токенами [1]).

**`tool_output_keep_last_n: 2` (базлайн 3) — подтверждён [Medium-High].**
Принцип «hot tail»: свежие tool-выводы — дословно, старые — сжимаются/обрезаются; pi при сериализации на summarization обрезает каждый tool result до 2000 символов [5] — старые tool-выводы считаются безопасно сжимаемыми первыми. 2 — агрессивнее базлайна, но консистентно с принципом; чтение файла дёшево повторить (`read_file` в always-present).

**`output_token_reserve: 8192` (= базлайн) — ОПРОВЕРГНУТО → 16384 [High].**
Резерв — это не «запас на ответ вообще», а **потолок генерации хода** (у c0wrk он же является потолком MaxTokens, `core/builderconfig.go`):
- «`reserveTokens: 16384` — слабое звено… При пороге резерв — это то, что остаётся от окна на ответ модели… ~12K потолка генерации для high-reasoning хода на сложном баге — мало. Возможные симптомы: обрезанный reasoning, length stop, недописанная реализация» [5 — WorkOS о pi; дефолт pi 16384 и тот назван ограничительным для reasoning-моделей].
- `max_tokens` ограничивает reasoning + видимый ответ **суммарно**; если reasoning съедает бюджет — видимый ответ обрезается или пуст [10 — Meta AI docs].
- Задокументированный отказ-режим: «thinking-токены делят тот же max_tokens бюджет… весь бюджет съедается reasoning_content → 0 контент-токенов → пустой ответ» (в т.ч. для Qwen3-thinking) [11 — goose #11142].
- Практическая эвристика: max_tokens ≈ 4× ожидаемого видимого вывода (thinking обычно 1–2× видимого) [12].
- Официальный harness Qwen: **max_tokens=32 768**, temp=1.0 [3]; thinking-токены часто >60% output-токенов [1]; замеры reasoning Qwen3.8: 2.7K–22.3K токенов [4].
- 8192 при thinking-режиме — ниже наблюдаемых reasoning-трейсов → гарантированные пустые/обрезанные ходы. **16384** — минимально достаточный (дефолт pi, «ограничительный, но рабочий»); 32768 — ориентир Qwen для агентных harness (для больших окон/API).
- Tradeoff: на локальных сетапах с малым контекстом резерв ест входной бюджет (эффективное окно = окно − резерв [5]) — взаимосвязь задокументировать.

---

## 3. Рекомендуемые изменения (все подтверждены внешними источниками)

| # | Изменение | Где | Подтверждение | Приоритет |
|---|-----------|-----|---------------|-----------|
| R1 | Для qwen передавать нативные уровни `reasoning_effort` (`low/medium/xhigh`) + `enable_thinking`, вместо бинарного `enable_thinking=(effort=="On")`. Сейчас c0wrk шлёт `"low"/"medium"`, а sp4rk для qwen понимает только `"On"/"Off"` (`llm/reasoning.go:28`) → `"medium"` молча **выключает** thinking | sp4rk `llm/provider_openai.go`, `llm/reasoning.go` | QwenCloud: `extra_body={"enable_thinking": true, "reasoning_effort": "medium"}` — официальный API [1]; vLLM: `reasoning_effort` автоматически включает thinking, явный `chat_template_kwargs.enable_thinking` приоритетнее [6]; llama.cpp: флаг `--reasoning-effort` [2]; шаблон Qwen3.8 предлагает ровно три уровня xhigh/medium/low [4]; прецедент в самом sp4rk: GLM 5.2+ уже получил нативный `reasoning_effort` (`applyGLMReasoning`) | P0 |
| R2 | Обновить qwen-пресет температуры 0.6 → 1.0 (thinking-режим; thinking у Qwen3.8 on by default) | sp4rk `prompt/sampling.go` | [2] (все run-команды temp 1.0), [3] (harness temp=1.0), [8] (thinking: temp=1.0, rep_penalty=1.0, presence=0.0); 0.6 — рекомендация эры Qwen3 [7] | P0 |
| R3 | Дефолт `sampling.reasoning_effort` → `"medium"` (при включённом sampling-варианте); расширить допустимые значения (`xhigh`) | c0wrk `backend/config/defaults.go`, валидатор | xhigh-дефолт — самый дорогой режим [4][9]; medium — нейтральная точка [4], улучшает результаты [9], рекомендован Unsloth [2]. **Зависит от R1** | P1 |
| R4 | `context.output_token_reserve` 8192 → **16384** | c0wrk `backend/config/defaults.go` (+ config.example.yaml, spec) | [5] (резерв = потолок генерации; 16384 у pi и тот «тонкий»), [10] (max_tokens = reasoning+content), [11] (пустые ответы), [12] (4× эвристика), [3] (Qwen harness 32768), [1] (>60% reasoning-доля) | P1 |
| R5 | Добавить `sampling.presence_penalty` (0 = наследовать; диапазон [0, 2]) | c0wrk config → adapter → builder → SamplingFunc | Официальный анти-повторный рычаг Qwen 0–2 [7]; поле уже поддерживается sp4rk (`ChatRequest.PresencePenalty`, официальное поле схемы); thinking-режим — 0.0 [8] | P2 |
| R6 | Зафиксировать в документации warning: гарантированный набор (always_present ∪ protected ∪ **все MCP**) не режется `max_tools` — несколько MCP-серверов легко выводят за safe zone 10–20 → деградация селекции | c0wrk docs/spec | [13] (~13% точность без селекции), [14] (<20 порог), [15] (исследование) | P2 |

---

## 4. Риски и ограничения

- **R3 требует R1**: пока sp4rk маппит qwen-effort бинарно, дефолт `"medium"` эквивалентен выключенному thinking — менять дефолт c0wrk можно только после маппинга в sp4rk (кросс-репо цикл по ADR-025).
- **R4 на малых окнах**: резерв ест входной бюджет (эффективное окно = окно − резерв [5]). Для сетапов с контекстом <32K может потребоваться пользовательская подстройка — отразить в config.example.yaml.
- **Точные пороги loop-hardening (2/3/3/5/4) и окон компакции (6/5/80/2)** не имеют внешних стандартов — подтверждено только направление; калибровка по телеметрии c0wrk (ложные срабатывания nudge vs спасённые циклы).
- **Sycophancy Qwen3.8** (соглашается с неверными баг-репортами) и чувствительность к квантам <Q6 — свойства модели вне профиля (прошлый этап), но влияют на выбор verify-механик.

## 5. Метрики для валидации изменений

1. Доля ходов с `finish_reason=length` / пустым контентом при thinking ON — до/после R4.
2. Медиана и p95 reasoning-токенов на ход — до/после R3 (ожидание: 22K→~3K на тривиальных, ~2× ускорение хода [4]).
3. Точность выбора инструментов (правильный инструмент на шаг) при подключённых MCP — мониторинг R6.
4. Частота срабатываний loop-breaker: nudge-вмешательства, за которыми последовал прогресс (истинные) vs ложные.

---

## Источники (все верифицированы в ходе исследования)

- [1] QwenCloud Docs — «Thinking»: enable_thinking / thinking_budget (1–32768) / reasoning_effort (low, medium, xhigh; default xhigh) через extra_body; reasoning-токены биллятся как output; >60% output-токенов может быть reasoning; preserve_thinking. https://docs.qwencloud.com/developer-guides/text-generation/thinking — официальная документация. Доверие: высокое.
- [2] Unsloth Documentation — «Qwen3.8 — How to Run Locally»: рекомендованные флаги `--temp 1.0 --top-p 0.95 --top-k 20 --min-p 0.0 --reasoning-effort medium`; vLLM MTP-конфиг. https://unsloth.ai/docs/models/qwen3.8 — официальный док мейнтейнера квантов. Доверие: высокое.
- [3] Qwen/Qwen3.8-27B — карточка модели HF: бенчмарки в Claude Code harness, temp=1.0, top_p=0.95, max_tokens=32 768, контекст 262 144. https://huggingface.co/Qwen/Qwen3.8-27B — первоисточник вендора. Доверие: высокое.
- [4] implicator.ai — «Alibaba Ships Qwen 3.8 27B With Maximum Reasoning Effort Turned On by Default»: xhigh-дефолт; 22 276 reasoning-токенов / 21 мин (пеликан-SVG) vs 3 715 токенов / 137 с без thinking; 17 576 vs 1 021 reasoning на кодинг-задаче; medium — «нейтральная точка, без инъекций»; Terminal-Bench 73.0, DeepSWE 42.2; ссылка на Simon Willison. https://www.implicator.ai/qwen-3-8-27b-xhigh-reasoning-default/ — независимая журналистика с проверяемыми цитатами. Доверие: средне-высокое.
- [5] WorkOS Blog — «Stop giving your coding agent a million-token context window»: механика порога `window − reserve`; дефолт pi reserveTokens=16384 назван «слабым звеном» для reasoning-моделей; «резерв = то, что остаётся на ответ»; keep-хвост — единственная дословная память; обзор context-rot исследований (arXiv 2605.12366; Chroma). https://workos.com/blog/coding-agent-context-window-compaction-settings — инженерный разбор исходников pi. Доверие: высокое.
- [6] vLLM Docs — «Reasoning Outputs» + GitHub main: `reasoning_effort` автоматически управляет enable_thinking («you no longer need to manually pass chat_template_kwargs… when using reasoning_effort»); явный chat_template_kwargs приоритетнее. https://docs.vllm.ai/en/stable/features/reasoning_outputs/ — официальная документация сервера. Доверие: высокое.
- [7] Qwen Docs (readthedocs) — Quickstart: «temperature=0.7, top_p=0.8, top_k=20, min_p=0 for Qwen3-Instruct-2507»; «adjust presence_penalty between 0 and 2 to reduce repetitions… higher value may occasionally result in language mixing»; историческая рекомендация thinking=0.6 (эра Qwen3). https://qwen.readthedocs.io/en/latest/getting_started/quickstart.html — официальная документация. Доверие: высокое.
- [8] Unsloth Qwen3.8-27B-GGUF (ModelScope) — карточка кванта: «Thinking Mode: temperature=1.0, top_p=0.95, top_k=20, min_p=0.0, presence_penalty=0.0, repetition_penalty=1.0». https://www.modelscope.cn/models/unsloth/Qwen3.8-27B-GGUF/summary — карточка мейнтейнера (страница рендерится JS; цитата из поискового индекса). Доверие: средне-высокое.
- [9] GitHub QwenLM/Qwen3.8 — issue #216 / discussion #113: xhigh-дефолт отслежен до chat-шаблона; переход на medium улучшал результаты, устраняя thinking loops. https://github.com/QwenLM/Qwen3.8/issues/216 — официальный трекер модели. Доверие: средне-высокое.
- [10] Meta AI Developer Docs — «Model API / Reasoning»: «max_tokens… cap reasoning tokens plus visible output tokens combined. If the model spends most of the budget on reasoning, the visible response may be truncated». https://ai.developer.meta.com/docs/reasoning — документация вендора. Доверие: высокое.
- [11] GitHub aaif-goose/goose #11142 — «Reasoning models produce empty content: max_tokens shared budget»: весь бюджет съедается reasoning_content → пустой ответ (Qwen3-thinking в т.ч.). https://github.com/aaif-goose/goose/issues/11142 — баг-трекер агентного фреймворка. Доверие: среднее.
- [12] tokenmix.ai — «Thinking Tokens Trap»: эвристика max_tokens ≈ 4× ожидаемого видимого вывода; thinking обычно 1–2× видимого. https://tokenmix.ai/blog/thinking-tokens-billing-trap-2026 — практический гайд. Доверие: среднее.
- [13] tianpan.co — «The Over-Tooled Agent Problem»: точность селекции ~13% на больших тулсетах; safe zone 10–20; 150–400 токенов на схему инструмента; описание инструмента = промпт селекции. https://tianpan.co/blog/2026-04-19-over-tooled-agent-problem — синтез индустрии. Доверие: средне-высокое.
- [14] webscraft.org — «Tool RAG»: «If you have fewer than 20 tools — stop at section 7». https://webscraft.org/blog/tool-rag-scho-robiti-koli-u-agenta-zabagato-instrumentiv?lang=en — практический порог. Доверие: среднее.
- [15] arXiv 2605.24660 — «How Many Tools Should an LLM Agent See?»: число инструментов как объект оценки селекции. https://arxiv.org/html/2605.24660 — препринт. Доверие: средне-высокое (не рецензирован).
- [16] OpenClaw / ClawCentral docs — tool-loop detection: no-progress breaker включён по умолчанию (после runaway-инцидента); контрточка ClawCentral: «guard disabled by default… strict settings can block legitimate repeated calls». https://open-claw.bot/docs/tools/loop-detection/ , https://docs.clawcentral.io/tools/loop-detection/ — документация фреймворков. Доверие: среднее.
- [17] SupraWall — «AI Agent Infinite Loop Detection & Circuit Breakers»: кейсы runaway-стоимости. https://www.supra-wall.com/learn/ai-agent-infinite-loop-detection — вендорский образовательный материал. Доверие: среднее.
- [18] Pydantic Deep Agents — «Stuck-loop detection»: on by default; max_repeated=3; action=warn (ModelRetry); паттерны identical / A-B-A-B / no-op. https://vstorm-co.github.io/pydantic-deepagents/advanced/stuck-loop-detection/ — документация фреймворка. Доверие: высокое.

*Источники прошлого этапа исследования (vLLM Recipes Qwen3.8, codersera guide, network-tocoder setup guide, LangChain few-shot tool-calling, bswen Claude Code autocompact, Anthropic compaction docs) дополнительно подкрепляют выводы, но в основной список включены только перепроверенные в этот раз.*
