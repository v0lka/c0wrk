# Отчёт о верификации полноты реализации Git Panel

**Источник роадмапа:** [`docs/development/git-panel-roadmap.md`](git-panel-roadmap.md)
**Дата верификации:** 2026-07-03
**Объект проверки:** соответствие кодовой базы (`backend/frontend_api_git.go`, фронтенд `frontend/src/components/GitPanel/`, тесты, спецификации) всем 6 фазам роадмапа.

---

## 1. Резюме (Executive Summary)

Роадмап Git Panel определяет **6 фаз** в двух вехах (**MVP**: Фазы 1–3; **v2**: Фазы 4–6), **43 подзадачи** суммарно, все заявлены как полностью реализованные. В ходе верификации (шаги 1–4) проверены: бэкенд Go-реализация (все 27 RPC-методов + 2 дополнительных), фронтенд React/TS-компоненты (12 компонентов роадмапа + ряд сверх-роадмапных файлов), а также тесты (Go + vitest) и спецификации (`specs/`).

### Общая оценка

Реализация **существенно полно** и в большинстве областей **превосходит** роадмап (дополнительные извлечённые подкомпоненты, типобезопасный API-слой с runtime-проверками, выделенные библиотеки парсинга diff и layout-а графа). **Все 27 RPC-методов роадмапа присутствуют** (пропущенных методов нет), **все 12 компонентов фронтенда существуют и реализованы**. Все существующие тесты (124 функции Go, 182 теста vitest) **проходят успешно**.

### Сводка по расхождениям

**Всего задокументировано 19 отклонений** от роадмапа (17 расхождений + 2 наблюдения-расширения). Из них:

| Серьёзность | Количество | Категории |
|-------------|------------|-----------|
| **Средняя** | 4 | B1, B2, D1, D2 |
| **Ниже средней** | 3 | B5, D3, D4 |
| **Низкая** | 8 | B3, Набл-7, D5, D6, D7, D8, Тесты, Спецификации |
| **Очень низкая** | 3 | B4, D9, D10 |
| **Незначительная** | 1 | Набл-6 |
| **ИТОГО** | **19** | |

### Ключевые выводы

- **2 функциональных пробела среднего уровня:** `Commit()` не возвращает SHA коммита (B1); DiffStat-индикаторы `+N/-M` никогда не отображаются — код рендеринга мёртв, wiring отсутствует (D1).
- **1 пробел ниже среднего на бэкенде:** `GetGitGraph` без пагинации — «lazy-load» графа не обеспечен (B5).
- **2 частичных пробела ниже среднего на фронтенде:** нет UI для инициации merge/rebase (D2, доступны только кнопки Abort); `stashList` не используется UI (D3).
- **1 частичный пробел ниже среднего:** специальное выделение конфликтных маркеров `<<<<<<< ======= >>>>>>>` в FileViewerPanel отсутствует (D4).
- **Фаза 4 (AI и управление ветками) реализована полностью** — расхождений не выявлено.
- Ряд низких/очень низких отклонений — это **архитектурные отклонения и расширения**, функционально эквивалентные или превосходящие роадмап.
- Тесты и спецификации: роадмап **не требует** тестов/спецификаций явно, поэтому пробелы тестового покрытия и устаревание контракта — это **наблюдения о качестве/документации**, а не строгие расхождения.

### Условные обозначения серьёзности

- **Средняя** — функциональный пробел: заявленная возможность частично не работает или данные недоступны.
- **Ниже средней** — частичный пробел: основная функция есть, но заявленная часть UI/данных отсутствует.
- **Низкая** — отклонение сигнатуры/пути/архитектуры, функционально эквивалентное или незначительное.
- **Очень низкая** — косметическое/типизационное несоответствие без влияния на поведение.
- **Незначительная** — улучшение, отклоняющееся от буквальной формулировки роадмапа в лучшую сторону.

---

## 2. Методология

Верификация выполнена в 4 шага:
1. **Шаг 1** — извлечение требований и фаз из роадмапа (261 строка, полный разбор + ripgrep по ключевым словам test/spec/acceptance → совпадений нет).
2. **Шаг 2** — верификация бэкенд Go-реализации (`backend/frontend_api_git.go`, 1297 строк; `core/workspace/git.go`; `core/workspace/types.go`; Wails-биндинги `frontend/wailsjs/go/desktop/App.d.ts`).
3. **Шаг 3** — верификация фронтенд React/TS-компонентов (12 компонентов роадмапа + сверх-роадмапные файлы; проверка функций против требований по фазам).
4. **Шаг 4** — верификация тестов и спецификаций (Go-тесты `go test`, vitest-тесты `vitest run`, спецификации `specs/`).

Идентификаторы расхождений: **B#** — бэкенд (шаг 2), **D#** — фронтенд (шаг 3), **Набл-#** — наблюдения-расширения бэкенда, **Тесты/Спецификации** — шаг 4.

---

## 3. Фаза 1 — Бэкенд RPC-инфраструктура (MVP)

### Требования роадмапа (10 подзадач)

Цель: расширить Go-бэкенд Wails RPC-методами для staging/commit/веток; обновить `GitStatusEntry` для staged+unstaged; добавить событийную модель. Все в `backend/frontend_api_git.go`:
1. `StageFile(path) error` — `git add`; `emitGitStatusChanged()`.
2. `UnstageFile(path) error` — `git reset HEAD <path>`; событие.
3. `StageAll() error` — `git add -A`; событие.
4. `UnstageAll() error` — `git reset HEAD`; событие.
5. `Commit(message) (string, error)` — `git commit` с экранированием `--message=` (анти-инъекция); **возвращает SHA**; событие.
6. `GetBranches() ([]string, error)` — `git branch --list`; убрать префикс `* `.
7. `GetCurrentBranch() (string, error)` — `git branch --show-current`; обработать detached HEAD.
8. `GetDiffStat(path) (*DiffStat, error)` — `git diff --numstat`; `DiffStat{Added, Deleted}`.
9. Обновить `GitStatusEntry` в `GitStatus()` — статус index (1-й XY-символ)→`Staged:true`, worktree (2-й)→`Staged:false`; **один файл может дать до 2 записей**.
10. Событие `git:status_changed` — `emitGitStatusChanged()` → `EventsEmit(ctx, "git:status_changed", nil)`; во всех мутирующих RPC.

### Результаты верификации (бэкенд — шаг 2)

✅ Все 9 RPC-методов Фазы 1 реализованы: `StageFile`/`UnstageFile`/`StageAll`/`UnstageAll` (git add/reset, эмитят событие), `GetDiffStat` (git diff --numstat → `*DiffStat{Added,Deleted}`), `GetBranches`, `GetCurrentBranch` (→ `BranchInfo`; расширение Фазы 5 upstream/ahead-behind уже встроено), `Commit` (git commit -m), `GetGitStatus`/`GitStatus` (делегирует в `core/workspace`), `emitGitStatusChanged`. Событие `git:status_changed` эмитируется всеми мутирующими методами (подтверждено тестами: эмитится при успехе, НЕ эмитится при ошибке).

### Расхождения

#### B1 — `Commit()` не возвращает SHA коммита — Серьёзность: Средняя
- **Ожидание роадмапа:** (Фаза 1 #5) `Commit(message string) (string, error)` — возвращает новый SHA (stdout) коммита.
- **Фактическое состояние:** Реализация `func (f *FrontendAPI) Commit(message string) error` возвращает **только error**, без SHA. Wails-биндинг подтверждает: `Commit(arg1:string):Promise<void>`. Фронтенд не может получить SHA только что созданного коммита. Защита от инъекции выполнена иным механизмом (передача message отдельным argv-элементом через `exec.Command`, без shell) — соответствует интенту, но сигнатура не совпадает.
- **Оценка:** Средняя — заявленное возвращаемое значение отсутствует; влияет на возможность отображения/использования SHA на фронтенде.

#### B2 — `GitStatus` создаёт ОДНУ запись на файл, а не «до 2» — Серьёзность: Средняя
- **Ожидание роадмапа:** (Фаза 1 #9) один файл может дать до 2 записей: отдельная `GitStatusEntry{Staged:true}` для index и `{Staged:false}` для worktree.
- **Фактическое состояние:** `core/workspace/git.go` `GitStatus()` возвращает `map[string]GitStatusEntry` с ключом — абсолютный путь; карта не может содержать 2 записи на один ключ. Вместо этого одна запись несёт **оба** поля `IndexStatus` + `WorkTreeStatus` (+ legacy `Staged` со стороны index). Разделение staged/unstaged выражается через поля в одной записи, а не через отдельные записи. Фронтендный `ChangesList` должен выводить staged/unstaged из `IndexStatus`/`WorkTreeStatus`, а не из отдельных `Staged`-флагованных записей.
- **Оценка:** Средняя — модель данных отличается от описанной в роадмапе; фронтенд адаптирован (см. D-проверки Фазы 2), функционально работает, но структура данных не соответствует спецификации.

#### B3 — `GetBranches()` возвращает `[]Branch`, не `[]string` — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 1 #6) `GetBranches() ([]string, error)` — `git branch --list`; убрать префикс `* ` у текущей.
- **Фактическое состояние:** Возвращает `[]Branch{Name, IsCurrent}` через `git for-each-ref`. Флаг текущей ветки — `IsCurrent bool`, а не префикс `* `. Это супермножество/расширение, функционально полное.
- **Оценка:** Низкая — отклонение сигнатуры в сторону расширения; фронтенд адаптирован (`BranchPicker` использует `IsCurrent`).

#### B4 — `emitGitStatusChanged` передаёт payload=repoPath, а не nil — Серьёзность: Очень низкая
- **Ожидание роадмапа:** (Фаза 1 #10) `EventsEmit(ctx, "git:status_changed", nil)`.
- **Фактическое состояние:** Эмитит `repoPath` как payload (фронтенд знает, какой проект затронут). Расширение; обработчик фронтенда игнорирует payload.
- **Оценка:** Очень низкая — безвредное расширение.

### Наблюдения

#### Набл-6 — Разделители control-char вместо `|` в парсинге лога/графа — Серьёзность: Незначительная
- **Ожидание роадмапа:** (Фаза 5 #9 / Фаза 6 #6) парсинг `git log --format=%H|%an|%ae|%ad|%s` и `--format=%H|%P|%s|%d` по разделителю `|`.
- **Фактическое состояние:** `GetCommitLog` и `GetGitGraph` используют control-char разделители (`%x1f`/`%x1e`) вместо `|` — надёжнее (корректно обрабатывает сообщения, содержащие `|`). `GetGitGraph` также опускает ASCII `--graph` (полосы графа выводятся из `Parents`) — соответствует интенту `GraphCommit.Parents`.
- **Оценка:** Незначительная — улучшение, полностью соответствующее интенту.

### Итог по Фазе 1
Реализована **существенно полно** (все методы присутствуют и работают). 2 средних расхождения (B1, B2) затрагивают сигнатуру/модель данных; B3, B4 — расширения; Набл-6 — улучшение.

---

## 4. Фаза 2 — Фронтенд: базовые компоненты (MVP)

### Требования роадмапа (7 подзадач)

Цель: Zustand-стор `gitPanelStore` + основные React-компоненты (список файлов с чекбоксами, секция коммита, переключатель flat/tree).
1. `gitPanelStore` — Zustand + persist (ключ `git-panel-settings`); состояние: `viewMode`/`entries`/`commitMessage`/`branch`/`expandedDirs`; действия: `setViewMode`, `setCommitMessage`, `loadEntries`, `toggleStage`, `commit`, `refreshStatus`.
2. `GitPanel` (root) — проверка `isGitRepo`; рендер Toolbar/ChangesList/CommitSection; `useEffect`→`git:status_changed`→`refreshStatus()`.
3. `GitPanelToolbar` — текущая ветка, Stage All/Unstage All, переключатель flat/tree, refresh.
4. `ChangesList` — секции Staged/Changes/Untracked со счётчиками; flat и tree режимы.
5. `GitFileEntry` — чекбокс (checked=staged), бейдж статуса (M/A/R/C/U), diff stat; двойной клик→diff; контекстное меню (отложено в Фазу 6).
6. `CommitSection` — textarea (auto-height), кнопка Commit (disabled-логика, «Commit»/«Commit N files», спиннер).
7. `useGitStatusEvents` — подписка `git:status_changed` через `EventsOn`; `getGitStatus`→entries; debounce 50ms; cleanup.

### Результаты верификации (фронтенд — шаг 3)

✅ Все 7 подзадач реализованы (в большинстве — с превышением):
- `gitPanelStore` ✅ — Zustand+persist `git-panel-settings`, partialize; состояние Фаз 2/3/5/6.
- `GitPanel/index.tsx` ✅ — проверка isGitRepo, переключатель вкладок, inline-ошибки, все подкомпоненты.
- `GitPanelToolbar.tsx` ✅ — ветка + ↑N↓M, Stage/Unstage All, flat/tree, refresh, stash, кнопки Abort.
- `ChangesList.tsx` (+ директория `ChangesList/`) ✅ — 3 секции со счётчиками, flat+tree, loading/empty.
- `GitFileEntry.tsx` ✅ — чекбокс, бейдж статуса, обнаружение конфликтов, контекстное меню.
- `CommitSection.tsx` ✅ — textarea auto-height, «Commit N files», AI Generate, Ctrl+Enter.
- `useGitStatusEvents.ts` ✅ — debounce 50ms, cleanup, параллельная загрузка status+branch+mergeState.

✅ Проверенные функции (без расхождений): отображение статуса git (M/A/R/C/D/U + секции staged/unstaged/untracked со счётчиками и сворачиванием); file-level staging (чекбокс + Stage All/Unstage All); коммит (textarea + кнопка + спиннер + динамическая подпись); обработка событий (глобальный `git:status_changed` + debounce 50ms + cleanup); индикаторы загрузки и обработка ошибок во всех компонентах.

### Расхождения

#### D1 — DiffStat никогда не заполняется (мёртвый код рендеринга) — Серьёзность: Средняя
- **Ожидание роадмапа:** (Фаза 2 #5) `GitFileEntry` отображает diff stat (`GetDiffStat`) — per-file индикаторы `+added/-deleted`.
- **Фактическое состояние:** API-обёртка `getDiffStat` существует и покрыта тестами; в `GitFileEntry.tsx` присутствует код рендеринга `+N/-M` (`entry.diffStat && ...`). Однако `toEntries()` (`lib/gitStatus.ts`) **всегда устанавливает `diffStat: null`**, а `getDiffStat` **никогда не вызывается** ни одним production-компонентом (только в `git.test.ts`). Per-file индикаторы `+added/-deleted` **никогда не появляются в UI** — код рендеринга фактически мёртв. Бэкенд RPC + API + UI-код существуют, но wiring (связывание) отсутствует.
- **Оценка:** Средняя — заявленная функция (diff-stat индикаторы) не отображается; функциональный пробел.

#### D7 — Стор не содержит действий `commit`/`refreshStatus` — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 2 #1) `gitPanelStore` содержит действия `commit` и `refreshStatus`.
- **Фактическое состояние:** Стор `gitPanelStore` **не содержит** этих действий. Логика коммита — в `CommitSection.tsx`; обновление статуса — в `index.tsx` + `useGitStatusEvents`. Функционально эквивалентно.
- **Оценка:** Низкая — архитектурное отклонение, функционально эквивалентное.

#### D10 — `useGitStatusEvents` использует низкоуровневый `subscribe()` вместо типизированного `onGlobalEvent()` — Серьёзность: Очень низкая
- **Ожидание роадмапа:** (Фаза 2 #7) подписка на `git:status_changed` (без указания конкретного механизма).
- **Фактическое состояние:** `git:status_changed` присутствует в типизированной `GlobalEventMap`, но подписка выполнена через нетипизированный `subscribe('git:status_changed', ...)`, тогда как другие события используют типизированные хелперы. Функционально корректно; минимальное несоответствие консистентности.
- **Оценка:** Очень низкая — безвредное несоответствие стиля.

### Итог по Фазе 2
Реализована **существенно полно**. 1 средний функциональный пробел (D1 — diff-stat не отображается); D7, D10 — незначительные архитектурные/стилевые отклонения.

---

## 5. Фаза 3 — Интеграция и полировка (MVP)

### Требования роадмапа (6 подзадач)

Цель: интегрировать GitPanel в архитектуру, подключить бэкенд, навигацию diff-viewer, сохранение настроек.
1. Заменить плейсхолдер «Git integration coming soon» на `<GitPanel/>` в `frontend/src/components/Sidebar/WorkspacePanel.tsx` (`<TabsContent value="git">`); только в CODE-режиме.
2. Подключить `git:status_changed`→`gitPanelStore.refreshStatus()` (параллельно getGitStatus + GetCurrentBranch) в `GitPanel/index.tsx`.
3. Навигация diff-viewer в `GitFileEntry.tsx` — клик по имени→`getFileDiff(path)`→FileViewerPanel; расширить `uiStore`: `selectedDiff{path,content}|null` + `openDiff`.
4. Отображение текущей ветки в `GitPanelToolbar.tsx` — GetCurrentBranch на mount + на `git:status_changed`.
5. Сохранение настроек в `gitPanelStore.ts` — persist `viewMode`/`sortBy`/`groupBy`/`expandedDirs` (ключ `git-panel-settings`); `partialize`.
6. Индикаторы загрузки и обработка ошибок во ВСЕХ компонентах GitPanel — состояния `loading`/`error` в сторе; спиннер; inline/toast ошибка; Commit спиннер+блокировка; try/catch.

### Результаты верификации (фронтенд — шаг 3)

✅ Все 6 подзадач реализованы:
- `WorkspacePanel.tsx` ✅ — плейсхолдер заменён, рендер только в CODE-режиме (путь перемещён — см. D5).
- Подключение события ✅ — `useGitStatusEvents` + `index.tsx` (параллельная загрузка).
- Навигация diff ✅ — `CodeMirrorFileViewer` принимает `diff`, парсит через `parseUnifiedDiff`/`buildDisplayLines`, применяет декорации + подсветку синтаксиса.
- Ветка в toolbar ✅ — GetCurrentBranch + ↑N↓M.
- Persist ✅ — Zustand persist `git-panel-settings` + partialize.
- Loading/error ✅ — все компоненты: спиннеры, try/catch, inline-ошибки, ошибка в сторе.

### Расхождения

#### D5 — Путь `WorkspacePanel` перемещён — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 3 #1) `frontend/src/components/Sidebar/WorkspacePanel.tsx`.
- **Фактическое состояние:** Фактический путь — `frontend/src/components/layout/WorkspacePanel.tsx` (директория `Sidebar/` упразднена). Функционально корректно.
- **Оценка:** Низкая — отклонение пути без влияния на функциональность.

#### D6 — Состояние diff в `fileViewerStore`, а не в `uiStore` — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 3 #3) расширить `uiStore` состоянием `selectedDiff{path,content}|null` + действием `openDiff`.
- **Фактическое состояние:** Используется выделенный `fileViewerStore` (`openFile`/`setFileDiff`/`setFileError`) вместо `uiStore`. Роадмап допускал альтернативу «or context» — функционально эквивалентно.
- **Оценка:** Низкая — архитектурное отклонение в рамках допустимых роадмапом альтернатив.

#### D8 — `sortBy`/`groupBy` не реализованы — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 3 #5) partialize должен персистить `viewMode`/`sortBy`/`groupBy`/`expandedDirs`.
- **Фактическое состояние:** Стор **не содержит** состояния `sortBy`/`groupBy`; partialize персистит только `viewMode` + `expandedDirs`. Сортировка/группировка отсутствуют.
- **Оценка:** Низкая — минорный пробел; функциональность сортировки/группировки не реализована, но роадмап упоминает её только в контексте persist.

### Итог по Фазе 3
Реализована **полно**, все подзадачи выполнены. 3 низких отклонения (D5 — путь; D6 — архитектура стора; D8 — отсутствующие sortBy/groupBy).

---

## 6. Фаза 4 — AI и управление ветками (v2)

### Требования роадмапа (5 подзадач)

Цель: AI-генерация commit-сообщений из diff (через существующий LLM-конфиг c0wrk); picker веток с переключением + простым созданием.
1. `GenerateCommitMessage(diff) (string, error)` — `git diff --staged` → существующий `llm`-пакет с conventional-commits промптом (feat/fix/chore); таймаут 15с.
2. AI-кнопка «✨ Generate» в `CommitSection.tsx` — сбор staged diff через GetFileDiff → `GenerateCommitMessage` → textarea; спиннер; ошибки→toast.
3. `CheckoutBranch(name) error` — `git checkout <name>`; событие; обработка «local changes would be overwritten».
4. `CreateBranch(name) error` — `git checkout -b <name>`; событие; обработка «branch already exists».
5. `BranchPicker` модал — список/поиск/«New Branch»; `CheckoutBranch`/`CreateBranch`.

### Результаты верификации

**Бэкенд (шаг 2):** ✅ `GenerateCommitMessage` (таймаут 15с → `builder.GenerateCommitMessage` → conventional-commits промпт feat/fix/chore/docs/style/refactor/perf/test/build/ci/revert); `CheckoutBranch` (обрабатывает «local changes would be overwritten»); `CreateBranch` (обрабатывает «already exists»). Все эмитят `git:status_changed`.

**Фронтенд (шаг 3):** ✅ AI commit message — кнопка «Generate» (Sparkles) → сбор staged diffs → `generateCommitMessage`; Branch operations — `BranchPicker` модал (список/фильтр/New Branch/checkout); отображение ветки + ↑N↓M ahead/behind.

**Тесты (шаг 4):** ✅ `BranchPicker.test.tsx` (10 тестов — список, подсветка текущей, checkout, создание, поиск/фильтр) и `CommitSection.test.tsx` (6 тестов — секция коммита + AI-кнопка). Бэкенд: `GenerateCommitMessage` (делегирование/распространение ошибки/пустой diff/no-builder), `CheckoutBranch`, `CreateBranch` покрыты.

### Расхождения

**Не выявлено.**

### Итог по Фазе 4
**✅ Фаза реализована ПОЛНОСТЬЮ.** Все 5 подзадач выполнены на бэкенде, фронтенде и покрыты тестами. Расхождений не найдено.

---

## 7. Фаза 5 — Удалённые операции и история (v2)

### Требования роадмапа (9 подзадач)

Цель: базовые удалённые операции (Pull/Push/Fetch) через system git-credentials; вкладка истории коммитов; поддержка stash; индикаторы merge-конфликтов.
1. `Pull(remote) error` — `git pull`; system git-credentials; возврат stdout+stderr; mutex (pending_remote_operation).
2. `Push(remote) error` — `git push`; mutex; событие после успеха.
3. `Fetch(remote) error` — `git fetch`; событие (обновление ahead/behind).
4. Расширить `GetCurrentBranch` — `git rev-list --count --left-right @{upstream}...HEAD`; `{Name, Upstream, Ahead, Behind}`; отображение «↑N ↓M».
5. `GitHistoryTab` — вкладки Changes|History; `GetCommitLog`; пагинация/«Load more»; клик→изменённые файлы.
6. Stash RPC: `StashCreate(message)`, `StashPop(index)`, `StashList()` ([]StashEntry); событие; кнопки stash в toolbar.
7. Индикация merge-конфликтов в `GitFileEntry.tsx` — UU/AA/DD → warning-иконка + красная/оранжевая строка; клик→конфликтные регионы (<<<<<<< ======= >>>>>>>) в FileViewerPanel.
8. `GitPanelFooter` — кнопки Pull/Push/Fetch; флаг `remoteOperationInProgress`; разворачиваемая секция вывода.
9. `GetCommitLog(limit) ([]CommitInfo, error)` — `git log --format=%H|%an|%ae|%ad|%s`; `CommitInfo{SHA,Author,Email,Date,Message}`; `skip` для пагинации.

### Результаты верификации

**Бэкенд (шаг 2):** ✅ `Pull`/`Push`/`Fetch` (`runRemoteOp`, сериализованы через `remoteOpMu`, таймаут 2 мин, возврат combined output); `GetCommitLog(limit, skip)` (пагинация через skip, default 50); `StashCreate`/`StashPop`/`StashList` (валидация index≥0, regex-парсинг); расширение `GetCurrentBranch` (upstream/ahead-behind). Все мутирующие эмитят событие.

**Фронтенд (шаг 3):** ✅ Remote ops (`GitPanelFooter` — Pull/Push/Fetch, mutex, разворачиваемый вывод); history (`GitHistoryTab` + `GitCommitRow` — пагинированный лог, клик→изменённые файлы); отображение ветки + ↑N↓M; обнаружение конфликтов в списке файлов (UU/AA/DD/AU/UD/UA/DU, warning-иконка, красная строка); кнопки stash (`GitStashButtons`).

**Тесты (шаг 4):** ✅ Бэкенд: `ParseCommitLog`/`ParseCommitFiles`/`ParseStashList`, `GetCommitLog`, `GetCommitFiles`, `StashCreateListPop`/`StashPop` (отрицательный index), `GetCurrentBranch` ahead/behind, `Push` + `FetchAndPull`, no-project-гарды. Фронтенд: API-обёртки покрыты в `git.test.ts`.

### Расхождения

#### D3 — `stashList` API не используется UI — Серьёзность: Ниже средней
- **Ожидание роадмапа:** (Фаза 5 #6) `StashList()` ([]StashEntry) + кнопки stash в toolbar; подразумевается просмотр/выбор/извлечение конкретного stash по индексу.
- **Фактическое состояние:** API `stashList()` + type guards + тесты существуют, но **ни один UI не использует** его. `GitStashButtons` выполняет только stash-create + pop-latest (`stash@{0}`); нет представления списка stash, нет возможности просмотреть/выбрать/извлечь конкретный stash по индексу.
- **Оценка:** Ниже средней — частичный пробел: RPC и API готовы, но UI для списка stash отсутствует.

#### D4 — Специальное выделение конфликтных маркеров отсутствует — Серьёзность: Ниже средней
- **Ожидание роадмапа:** (Фаза 5 #7) клик по файлу-конфликту → показ конфликтных регионов в FileViewerPanel с маркерами `<<<<<<< / ======= / >>>>>>>`.
- **Фактическое состояние:** Обнаружение конфликтов в списке файлов **выполнено** (комбинации UU/AA/DD/AU/UD/UA/DU, warning-иконка, красная строка). Однако **специального рендеринга конфликтных регионов** в FileViewerPanel нет — клик по файлу-конфликту показывает маркеры как обычный текст без выделения/навигации по `<<<<<<< ======= >>>>>>>`.
- **Оценка:** Ниже средней — частичный пробел: обнаружение есть, специальное выделение/навигация по конфликтам отсутствует.

#### Набл-6 (продолжение) — Разделители control-char в `GetCommitLog` — Серьёзность: Незначительная
- См. Фазу 1 (Набл-6): `GetCommitLog` использует `%x1f`/`%x1e` вместо роадмаповского `|` — надёжнее. Улучшение.

### Итог по Фазе 5
Реализована **существенно полно**. 2 частичных пробела ниже среднего (D3 — список stash в UI; D4 — выделение конфликтных маркеров). Набл-6 — улучшение парсинга.

---

## 8. Фаза 6 — Полировка и продвинутые сценарии (v2)

### Требования роадмапа (6 подзадач)

Цель: контекстные меню файлов, partial staging (на уровне hunk), rebase/merge workflow, визуализация commit-графа.
1. Контекстное меню `GitFileEntry` — «Discard Changes» (DiscardChanges + confirm), «Add to .gitignore» (AppendToGitignore), «Open in Editor».
2. `DiscardChanges(path) error` — unstaged: `git checkout -- <path>`; staged: `git reset HEAD <path> && git checkout -- <path>`; confirm-диалог на фронтенде; событие.
3. `AppendToGitignore(pattern) error` — проверить/создать `.gitignore`; дописать pattern; избежать дублей.
4. `StageHunks(path, hunks []HunkRange) error` — `HunkRange{StartLine, EndLine}`; temp-patch через git diff → `git apply --cached`; фронтенд: кнопки «Stage Hunk» по каждому hunk.
5. Rebase/merge workflow — `Merge(branch)`, `Rebase(branch)`, `AbortMerge()`, `AbortRebase()`; детекция через `.git/MERGE_HEAD`/`.git/rebase-apply`; кнопки Abort в toolbar (только в состоянии конфликта).
6. `GitGraph` (canvas/SVG) — `GetGitGraph()` ([]GraphCommit), парсинг `git log --graph --format=%H|%P|%s|%d`; отдельная вкладка «Graph»; scrolling + lazy-load.

### Результаты верификации

**Бэкенд (шаг 2):** ✅ `DiscardChanges` (untracked→`git clean -f`, tracked→reset+checkout); `AppendToGitignore` (дедуп через `patternAlreadyIgnored`); `StageHunks(path, []HunkRange)` (buildHunkPatch по old-file диапазонам → `git apply --cached` через temp-файл); `Merge`/`Rebase`/`AbortMerge`/`AbortRebase`; `GetGitGraph` (git log -n 100 → `[]GraphCommit{SHA, Parents, Message, Refs}`). Дополнительно: `GetCommitFiles(sha)`, `GetRebaseMergeState()`.

**Фронтенд (шаг 3):** ✅ Контекстное меню (`GitFileContextMenu` — Discard/gitignore/Open + confirm); hunk-level staging (`DiffHunkStageBar` рендерит per-hunk кнопки → `stageHunks(filePath, [hunkToRange(hunk)])`, **подключён** в `FileViewerContent`); abort merge/rebase (кнопки в toolbar); граф (`GitGraph` + `gitGraphLayout.ts` — SVG, полосы/узлы/refs, lazy-load).

**Тесты (шаг 4):** ✅ Бэкенд (`frontend_api_git_phase6_test.go`, 45 функций): `ParseGitGraph`/`ParseGitRefs`, `HunkInRange`, `BuildHunkPatch`, `PatternAlreadyIgnored`, `IsRebaseActive`, `DiscardChanges`, `AppendToGitignore`, `Merge`, `Rebase`, `AbortMerge`/`AbortRebase`, `GetRebaseMergeState`, `GetGitGraph`, `StageHunks`. Фронтенд: `gitGraphLayout.test.ts` (алгоритм layout) + `DiffHunkStageBar.test.ts` (hunkToRange).

### Расхождения

#### B5 — `GetGitGraph` без пагинации — Серьёзность: Ниже средней
- **Ожидание роадмапа:** (Фаза 6 #6, фронтенд) «scrolling + lazy-load commits».
- **Фактическое состояние:** Бэкенд `GetGitGraph()` **без параметров** возвращает capped 100 коммитов (`defaultGitGraphLimit=100`). В отличие от `GetCommitLog(limit, skip)`, способа догрузить дополнительные страницы для графа нет. Сигнатура бэкенда соответствует роадмапу (без параметров), но «lazy-load» **не обеспечивается бэкендом** для репозиториев >100 коммитов.
- **Оценка:** Ниже средней — заявленный lazy-load не поддержан пагинацией на бэкенде.

#### D2 — Нет UI для инициации merge/rebase — Серьёзность: Средняя
- **Ожидание роадмапа:** (Фаза 6 #5) rebase/merge workflow: кнопки Abort Merge/Abort Rebase в toolbar (только в состоянии конфликта). Явное фронтенд-требование роадмапа — только кнопки Abort.
- **Фактическое состояние:** API-обёртки `merge()`/`rebase()` существуют и покрыты тестами, но **никогда не вызываются из UI**. Toolbar имеет кнопки Abort Merge/Abort Rebase (`abortMerge`/`abortRebase`) — **выполнено**. Однако **нет UI для инициации** merge/rebase (`merge(branch)`/`rebase(branch)` недостижимы из UI). Пользователь может только прервать уже идущую операцию, но не начать новую.
- **Оценка:** Средняя — хотя явное фронтенд-требование роадмапа (кнопки Abort) выполнено, RPC `Merge`/`Rebase` недоступны из UI, что делает workflow неполным.

#### Набл-7 — `DiscardChanges` удаляет untracked через `git clean -f` — Серьёзность: Низкая
- **Ожидание роадмапа:** (Фаза 6 #2) unstaged: `git checkout -- <path>`; staged: `git reset HEAD <path> && git checkout -- <path>`.
- **Фактическое состояние:** Для untracked-файлов `DiscardChanges` использует `git clean -f` — **сильнее** подхода роадмапа (только checkout/reset), деструктивнее для untracked. Роадмап требует confirm-диалог на фронтенде (выполнено — см. контекстное меню).
- **Оценка:** Низкая — расширение с более острым краем; confirm-диалог присутствует.

### Итог по Фазе 6
Реализована **существенно полно**. 1 средний частичный пробел (D2 — нет UI инициации merge/rebase); 1 пробел ниже среднего (B5 — граф без пагинации); Набл-7 — расширение с более острым краем (git clean -f).

---

## 9. Тесты и спецификации (сквозная верификация — шаг 4)

### Контекст

Роадмап **не содержит** секций тестов/спецификаций (подтверждено полным чтением + ripgrep по ключевым словам test/spec/acceptance → совпадений нет). Поэтому пробелы тестового покрытия и устаревание спецификаций — это **наблюдения о качестве/документации**, а не строгие расхождения с роадмапом. Тем не менее они задокументированы по запросу.

### Тесты — общее состояние

**Все тесты проходят успешно.**
- **Бэкенд Go:** 2 файла, 124 функции — `frontend_api_git_test.go` (1624 строки, 79 функций, Фазы 1–5) + `frontend_api_git_phase6_test.go` (45 функций, Фаза 6). `go test` → `ok github.com/v0lka/c0wrk/backend`. Все 27 RPC-методов + хелперы покрыты.
- **Фронтенд vitest:** 6 файлов, 182 теста — `git.test.ts` (113 тестов — все 27 API-обёрток + type guards), `gitPanelStore.test.ts` (36 тестов — стор), `gitGraphLayout.test.ts` (layout-алгоритм), `BranchPicker.test.tsx` (10), `CommitSection.test.tsx` (6), `DiffHunkStageBar.test.ts` (hunkToRange). `vitest run` → 6 files, 182 passed.

### Расхождения

#### Тесты — 11 компонентов GitPanel без компонентных тестов — Серьёзность: Низкая
- **Ожидание роадмапа:** роадмап не требует тестов явно.
- **Фактическое состояние:** Только **2 из 13** файлов компонентов GitPanel имеют выделенные тесты (`BranchPicker.test.tsx`, `CommitSection.test.tsx`). Остальные **11 компонентов/hooks** не имеют компонентных тестов (косвенно проверяются через store/API-тесты):
  - `GitPanel/index.tsx` (Фаза 2/3) — root-рендер, «Not a git repository», подписка на события.
  - `GitPanelToolbar.tsx` (Фаза 2/3/5/6) — ветка, Stage All/Unstage All, кнопки abort, кнопки stash.
  - `ChangesList.tsx` + 5 подкомпонентов (Фаза 2) — flat/tree группировка, заголовки секций.
  - `GitFileEntry.tsx` (Фаза 2/5/6) — чекбокс, бейдж статуса, diff stat, контекстное меню, индикация конфликтов.
  - `GitFileContextMenu.tsx` (Фаза 6) — действия discard/ignore/open.
  - `GitHistoryTab.tsx` (Фаза 5) — список истории, пагинация.
  - `GitPanelFooter.tsx` (Фаза 5) — кнопки Pull/Push/Fetch, вывод.
  - `GitGraph.tsx` (Фаза 6) — рендеринг графа (только алгоритм layout протестирован, не компонент).
  - `GitCommitRow.tsx` (Фаза 5) — строка коммита.
  - `GitStashButtons.tsx` (Фаза 5) — кнопки stash create/pop/list.
  - `useGitStatusEvents.ts` (Фаза 2) — подписка + debounce.
- **Оценка:** Низкая — заметный пробел тестового покрытия для заявленных-реализованными компонентов; не строгое расхождение (роадмап не требует тестов).

#### Спецификации — контракт устарел относительно Git Panel — Серьёзность: Низкая
- **Ожидание роадмапа:** роадмап не требует обновления спецификаций явно.
- **Фактическое состояние:** **Отсутствует** dedicated spec-файл git-panel в `specs/`. Существующие git-упоминания предшествуют роадмапу:
  - `specs/contracts/desktop-frontend.md` документирует **только** старые git RPC (`GetFileDiff`, `GetGitStatus`) в секции *Workspace*, относя их к `frontend_api_workspace.go`. **Ни один** из 27 Git Panel RPC (Stage/Unstage/Commit/Branches/Checkout/Create/DiffStat/GenerateCommitMessage/Pull/Push/Fetch/GetCommitLog/GetCommitFiles/Stash*/DiscardChanges/AppendToGitignore/StageHunks/Merge/Rebase/Abort*/GetGitGraph/GetRebaseMergeState) не задокументирован. **Секции для `backend/frontend_api_git.go` в контракте нет**.
  - `specs/contracts/backend-core.md` и `specs/domains/workspace.md` описывают низкоуровневые CLI-обёртки `core/workspace/git.go` (IsGitRepo, IsGitTracked, GitStatus, GetFileDiffInRepo, GitIgnoredPaths) — не Git Panel RPC.
  - ADR-004 / ADR-010 / ADR-009 документируют решение о git как внешней бинарной зависимости и извлечении git-логики в `core/`.
- **Оценка:** Низкая — дрейф документации: контракт фронтенда не отражает реализованный API-surface; не строгое расхождение (роадмап не требует обновления спецификаций).

### Итог по тестам/спецификациям
Тесты существуют и проходят для всей поверхности бэкенд-RPC (все 27 методов) и фронтенд-слоя API/store/layout + 2 компонентов. Пробелы: (а) 11 компонентов без выделенных тестов; (б) отсутствие git-panel spec и устаревший контракт. Оба — наблюдения о качестве/документации, т.к. роадмап не требует тестов/спецификаций формально.

---

## 10. Сводная таблица всех расхождений

| ID | Фаза | Описание | Серьёзность | Тип |
|----|------|----------|-------------|-----|
| B1 | 1 | `Commit()` не возвращает SHA | Средняя | Функциональный пробел |
| B2 | 1 | `GitStatus` — 1 запись на файл, не «до 2» | Средняя | Расхождение модели данных |
| B3 | 1 | `GetBranches()` → `[]Branch`, не `[]string` | Низкая | Расширение |
| B4 | 1 | payload события = repoPath, не nil | Очень низкая | Расширение |
| Набл-6 | 1/5/6 | Control-char разделители вместо `\|` | Незначительная | Улучшение |
| D1 | 2 | DiffStat никогда не заполняется (мёртвый код) | Средняя | Функциональный пробел |
| D7 | 2 | Стор без `commit`/`refreshStatus` | Низкая | Арх. отклонение |
| D10 | 2 | `subscribe()` вместо `onGlobalEvent()` | Очень низкая | Стилевое отклонение |
| D5 | 3 | Путь WorkspacePanel перемещён | Низкая | Отклонение пути |
| D6 | 3 | Diff-состояние в `fileViewerStore`, не `uiStore` | Низкая | Арх. отклонение |
| D8 | 3 | `sortBy`/`groupBy` не реализованы | Низкая | Минорный пробел |
| — | 4 | **Расхождений нет — фаза реализована полностью** | — | — |
| D3 | 5 | `stashList` не используется UI | Ниже средней | Частичный пробел |
| D4 | 5 | Выделение конфликтных маркеров отсутствует | Ниже средней | Частичный пробел |
| B5 | 6 | `GetGitGraph` без пагинации (lazy-load) | Ниже средней | Частичный пробел |
| D2 | 6 | Нет UI для инициации merge/rebase | Средняя | Частичный пробел |
| Набл-7 | 6 | `DiscardChanges` через `git clean -f` | Низкая | Расширение (острый край) |
| Тесты | * | 11 компонентов без компонентных тестов | Низкая | Пробел покрытия (наблюдение) |
| Спецификации | * | Контракт устарел, нет git-panel spec | Низкая | Дрейф документации (наблюдение) |

**Итого: 19 задокументированных отклонений** (4 средних, 3 ниже средней, 8 низких, 3 очень низких, 1 незначительное).

---

## 11. Заключение

Верификация всех 6 фаз роадмапа Git Panel показала, что реализация **существенно полна и в большинстве областей превосходит** требования. **Все 27 RPC-методов бэкенда и все 12 компонентов фронтенда присутствуют и реализованы**; пропущенных методов или компонентов нет. Все существующие тесты (Go + vitest) проходят успешно.

Из **19 задокументированных отклонений**:
- **4 средних** функциональных пробела требуют внимания: `Commit()` без возврата SHA (B1); модель данных `GitStatus` «1 запись на файл» вместо «до 2» (B2); DiffStat-индикаторы не отображаются из-за отсутствия wiring (D1); нет UI для инициации merge/rebase (D2).
- **3 пробела ниже среднего** — частичные: граф без пагинации (B5); список stash в UI (D3); выделение конфликтных маркеров (D4).
- Остальные — низкие/очень низкие архитектурные отклонения, расширения и улучшения, а также наблюдения о тестовом покрытии и дрейфе документации (роадмап не требует тестов/спецификаций формально).

**Единственная фаза без расхождений — Фаза 4 (AI и управление ветками)** — реализована полностью и покрыта тестами.

Для закрытия наиболее значимых пробелов рекомендуется: (1) вернуть SHA из `Commit()` и отобразить его на фронтенде; (2) реализовать wiring DiffStat — вызывать `getDiffStat` и заполнять `entry.diffStat` в `toEntries()`; (3) добавить UI для инициации merge/rebase; (4) добавить пагинацию к `GetGitGraph` для поддержки lazy-load; (5) реализовать представление списка stash и специальное выделение конфликтных маркеров.
