# Git Panel Roadmap

Описание: Поэтапный план реализации Git-панели в левом сайдбаре c0wrk (замена текущего плейсхолдера «Git integration coming soon»). Опирается на дизайн-эксплорейшн [Zed git_ui](https://github.com/zed-industries/zed/tree/main/crates/git_ui/src) и результаты исследования архитектуры c0wrk.

**Текущее состояние**: Backend (Go) предоставляет `GitStatus()` через `git status --porcelain`, `GetFileDiff()` через `git diff`, `IsGitRepo()` с 30-секундным кешем. `GitStatusEntry` содержит одно состояние на путь (`staged XOR unstaged`). `FileTreePanel` раскрашивает имена файлов по git-статусу. Фронтенд-API: `getGitStatus(path)`, `getFileDiff(path)`, `watchDirectory()`/`unwatchDirectory()`. Отсутствуют staging-операции, commit, работа с ветками и remote-операции. Вкладка «git» в `WorkspacePanel` содержит плейсхолдер.

---

## MVP

### Phase 1: Backend RPC Infrastructure

> **Цель**: Расширить Go-бэкенд новыми Wails RPC-методами для staging, commit и работы с ветками. Обновить модель данных `GitStatusEntry` для поддержки staged+unstaged состояний. Добавить событийную модель для реактивных обновлений UI.

- **What:** Реализовать RPC-метод `StageFile(path string) error` для staging отдельного файла.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git add <path>` через `exec.Command` с контекстом рабочей директории репозитория. После успешного выполнения вызвать `emitGitStatusChanged()` для оповещения фронтенда об изменении staging area.

- **What:** Реализовать RPC-метод `UnstageFile(path string) error` для снятия файла со staging.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git reset HEAD <path>`. После успешного выполнения вызвать `emitGitStatusChanged()`.

- **What:** Реализовать RPC-метод `StageAll() error` для staging всех изменений.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git add -A`. Вызвать `emitGitStatusChanged()` для обновления UI.

- **What:** Реализовать RPC-метод `UnstageAll() error` для снятия всех файлов со staging.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git reset HEAD`. Вызвать `emitGitStatusChanged()`.

- **What:** Реализовать RPC-метод `Commit(message string) (string, error)` для создания коммита.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git commit -m <message>`, экранируя сообщение через `--message=` во избежание инъекций. Вернуть SHA нового коммита (stdout). Вызвать `emitGitStatusChanged()`.

- **What:** Реализовать RPC-метод `GetBranches() ([]string, error)` для получения списка локальных веток.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git branch --list`, распарсить вывод (удалить префикс `* ` у текущей ветки). Вернуть слайс имён веток.

- **What:** Реализовать RPC-метод `GetCurrentBranch() (string, error)` для определения текущей ветки.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git branch --show-current`, вернуть stdout как строку. Обработать случай detached HEAD (пустой вывод).

- **What:** Реализовать RPC-метод `GetDiffStat(path string) (*DiffStat, error)` для получения статистики изменений файла.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git diff --numstat <path>`, распарсить два числа (added/deleted строки). Альтернативно — парсинг `git diff --stat`. Вернуть структуру `DiffStat` с полями `Added`, `Deleted`.

- **What:** Обновить модель `GitStatusEntry` в `GitStatus()` для поддержки одновременного staged и unstaged статуса одного файла.
  **Where:** `backend/frontend_api_git.go`
  **How:** Изменить парсинг `git status --porcelain`: индексный статус (первый символ XY-кода) → `GitStatusEntry{Staged: true}`, статус рабочей директории (второй символ) → `GitStatusEntry{Staged: false}`. Один физический файл может порождать до двух записей в слайсе, если изменён и в staging, и в рабочей директории.

- **What:** Реализовать событийную модель `git:status_changed` для реактивных обновлений UI.
  **Where:** `backend/frontend_api_git.go`
  **How:** Создать хелпер `emitGitStatusChanged()`, вызывающий `runtime.EventsEmit(ctx, "git:status_changed", nil)` через Wails Events API. Вызывать этот хелпер во всех мутирующих RPC-методах (Stage*, Unstage*, Commit, CheckoutBranch и т.д.).

### Phase 2: Frontend Core Components

> **Цель**: Создать Zustand-стор `gitPanelStore` и основные React-компоненты Git-панели: список файлов с чекбоксами, секция коммита, переключение flat/tree view.

- **What:** Создать Zustand-стор `gitPanelStore` для управления состоянием Git-панели.
  **Where:** `frontend/src/stores/gitPanelStore.ts`
  **How:** Использовать Zustand с `persist`-мидлварью (по аналогии с `uiStore`). Определить состояние: `viewMode` (flat/tree), `entries` (массив `GitPanelEntry` с полями: path, status, staged, diffStat), `commitMessage`, `branch`, `expandedDirs` (для tree mode). Реализовать экшены: `setViewMode`, `setCommitMessage`, `loadEntries`, `toggleStage`, `commit`, `refreshStatus`. Настроить `persist` с ключом `git-panel-settings` для сохранения view-настроек в localStorage.

- **What:** Создать корневой компонент `GitPanel`, заменяющий текущий плейсхолдер.
  **Where:** `frontend/src/components/GitPanel/index.tsx`
  **How:** Реализовать React-компонент, проверяющий `isGitRepo` через `getGitStatus`. Если репозиторий не найден — показывать «Not a git repository». При успехе — рендерить дочерние компоненты: `GitPanelToolbar`, `ChangesList`, `CommitSection`. В `useEffect` подписаться на событие `git:status_changed` через Wails runtime, при получении вызывать `refreshStatus()`.

- **What:** Создать верхнюю панель инструментов `GitPanelToolbar`.
  **Where:** `frontend/src/components/GitPanel/GitPanelToolbar.tsx`
  **How:** Реализовать компонент с: отображением текущей ветки (из `GetCurrentBranch`), кнопками «Stage All» / «Unstage All» (вызывают `StageAll()`/`UnstageAll()` RPC), переключателем flat/tree view (иконка списка/дерева), кнопкой обновления статуса. При монтировании вызывать `GetCurrentBranch()` и отображать имя ветки с git-иконкой.

- **What:** Создать скроллируемый список изменённых файлов `ChangesList`.
  **Where:** `frontend/src/components/GitPanel/ChangesList.tsx`
  **How:** Реализовать компонент, группирующий файлы по секциям: «Staged Changes», «Changes» (unstaged), «Untracked Files». Каждая секция имеет заголовок с индикатором количества записей. Поддерживать flat-режим (плоский список) и tree-режим (вложенные папки с раскрытием через `expandedDirs`). Рендерить `GitFileEntry` для каждого файла.

- **What:** Создать строку файла `GitFileEntry` с чекбоксом, статусом и diff-статистикой.
  **Where:** `frontend/src/components/GitPanel/GitFileEntry.tsx`
  **How:** Реализовать компонент с: чекбоксом (checked для staged, unchecked для unstaged), иконкой файла, именем файла, бейджем статуса (M/A/R/C/U), diff-статистикой (added/deleted строки через `GetDiffStat`). Клик по чекбоксу вызывает `StageFile(path)`/`UnstageFile(path)`. Двойной клик по имени файла открывает diff в `FileViewerPanel`. Правый клик — контекстное меню (v2, Phase 6).

- **What:** Создать секцию коммита `CommitSection` с textarea и кнопкой Commit.
  **Where:** `frontend/src/components/GitPanel/CommitSection.tsx`
  **How:** Реализовать компонент с: `<textarea>` для сообщения коммита (auto-height через ref, placeholder «Describe your changes»), кнопкой «Commit» (вызывает `Commit(message)` RPC). Кнопка disabled если нет staged-файлов или пустое сообщение. Лейбл кнопки динамический: «Commit» / «Commit N files». Показывать спиннер во время выполнения `Commit()`.

- **What:** Создать React-хук `useGitStatusEvents` для подписки на события обновления git-статуса.
  **Where:** `frontend/src/hooks/useGitStatusEvents.ts`
  **How:** Реализовать хук, подписывающийся на событие `git:status_changed` через `EventsOn` (Wails runtime). При получении события вызывать `getGitStatus()` и обновлять `gitPanelStore.entries`. Добавить дебаунс 50ms через `setTimeout`/`clearTimeout` для предотвращения множественных запросов при быстрых изменениях. Автоматически отписываться в cleanup-функции `useEffect`.

### Phase 3: Integration & Polish (MVP)

> **Цель**: Интегрировать GitPanel в существующую архитектуру, подключить к бэкенду, реализовать навигацию к diff-вьюверу и персистентность настроек.

- **What:** Заменить плейсхолдер «Git integration coming soon» на компонент `GitPanel` во вкладке «git».
  **Where:** `frontend/src/components/Sidebar/WorkspacePanel.tsx`
  **How:** Найти `<TabsContent value="git">` в `WorkspacePanel`, заменить текстовый плейсхолдер на рендер `<GitPanel />`. Убедиться, что `GitPanel` рендерится только в CODE-режиме сайдбара (не в CHAT), добавив условный рендеринг по значению режима.

- **What:** Подключить событие `git:status_changed` к `gitPanelStore` для автоматического обновления.
  **Where:** `frontend/src/components/GitPanel/index.tsx`
  **How:** В `useEffect` корневого компонента `GitPanel` подписаться на событие `git:status_changed` через `EventsOn` (Wails runtime). При получении события вызывать `gitPanelStore.refreshStatus()`, который выполняет параллельные вызовы `getGitStatus()` и `GetCurrentBranch()` с бэкенда и обновляет стор.

- **What:** Реализовать навигацию к diff-вьюверу при клике на файл в Git-панели.
  **Where:** `frontend/src/components/GitPanel/GitFileEntry.tsx`
  **How:** При клике на имя файла (не на чекбокс) вызывать `getFileDiff(path)` и передавать результат в `FileViewerPanel` (правая панель). Расширить `uiStore` или создать контекст для передачи выбранного diff-содержимого: добавить поле `selectedDiff: { path: string; content: string } | null` и экшен `openDiff`.

- **What:** Отображать текущую ветку в тулбаре Git-панели.
  **Where:** `frontend/src/components/GitPanel/GitPanelToolbar.tsx`
  **How:** При монтировании компонента вызывать `GetCurrentBranch()` RPC. Отображать имя ветки с git-иконкой в левой части тулбара. Обновлять имя ветки при каждом событии `git:status_changed` через подписку в `useEffect`.

- **What:** Реализовать персистентность view-настроек Git-панели.
  **Where:** `frontend/src/stores/gitPanelStore.ts`
  **How:** Настроить Zustand `persist`-мидлварю для сохранения `viewMode`, `sortBy`, `groupBy` и `expandedDirs` в localStorage (ключ: `git-panel-settings`). При инициализации стора восстанавливать сохранённые настройки из localStorage. Использовать `partialize` для выбора только персистируемых полей.

- **What:** Добавить индикаторы загрузки и обработку ошибок во все компоненты Git-панели.
  **Where:** `frontend/src/components/GitPanel/` (все компоненты)
  **How:** Добавить состояния `loading` и `error` в `gitPanelStore`. В каждом компоненте проверять `loading` — отображать спиннер при загрузке статуса. При `error` — показывать inline-сообщение об ошибке или toast-уведомление. Кнопка Commit должна показывать спиннер и блокироваться во время выполнения `Commit()`. Использовать try/catch во всех асинхронных операциях.

---

## v2

### Phase 4: AI & Branch Management

> **Цель**: Добавить AI-генерацию commit-сообщений на основе диффов (используя существующую LLM-конфигурацию c0wrk). Реализовать picker веток с возможностью переключения и простого создания.

- **What:** Реализовать RPC-метод `GenerateCommitMessage(diff string) (string, error)` для AI-генерации сообщения коммита.
  **Where:** `backend/frontend_api_git.go`
  **How:** Принять вывод `git diff --staged` как строку. Вызвать настроенный LLM-провайдер c0wrk (через существующий `llm` пакет) с промптом, следующим формату conventional commits (feat:, fix:, chore: и т.д.). Вернуть сгенерированное сообщение. Добавить таймаут 15 секунд на LLM-запрос.

- **What:** Добавить кнопку AI-генерации сообщения коммита в `CommitSection`.
  **Where:** `frontend/src/components/GitPanel/CommitSection.tsx`
  **How:** Добавить кнопку «✨ Generate» рядом с textarea коммита. По клику: собрать staged diff через `GetFileDiff` по всем staged-файлам (из `gitPanelStore.entries`), вызвать `GenerateCommitMessage(diff)` RPC, вставить результат в textarea. Показывать спиннер на кнопке во время генерации. Обрабатывать ошибки LLM (показывать toast).

- **What:** Реализовать RPC-метод `CheckoutBranch(name string) error` для переключения веток.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git checkout <name>`. После успешного переключения вызвать `emitGitStatusChanged()`. Обработать ошибку «local changes would be overwritten» и вернуть понятное сообщение.

- **What:** Реализовать RPC-метод `CreateBranch(name string) error` для создания новой ветки.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git checkout -b <name>`. После успешного создания и переключения вызвать `emitGitStatusChanged()`. Обработать ошибку «branch already exists».

- **What:** Создать модальный picker веток `BranchPicker`.
  **Where:** `frontend/src/components/GitPanel/BranchPicker.tsx`
  **How:** Реализовать модальное окно/попап, открывающееся по клику на имя ветки в `GitPanelToolbar`. Содержит: список локальных веток (из `GetBranches()`), подсветку текущей ветки, строку поиска/фильтрации по имени, кнопку «New Branch» с полем ввода имени (вызывает `CreateBranch`). Клик по ветке вызывает `CheckoutBranch(name)` и закрывает модал.

### Phase 5: Remote Operations & History

> **Цель**: Добавить базовые remote-операции (Pull/Push/Fetch) с использованием системных git-credentials. Реализовать вкладку истории коммитов. Добавить поддержку stash и индикаторы merge-конфликтов.

- **What:** Реализовать RPC-метод `Pull(remote string) error` для получения изменений с remote.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git pull <remote>`. Использовать системные git-credentials (без собственного менеджмента, полагаться на `git` CLI). Возвращать вывод команды (stdout+stderr) для отображения в UI. Блокировать параллельные remote-операции через мьютекс (паттерн `pending_remote_operation` из Zed).

- **What:** Реализовать RPC-метод `Push(remote string) error` для отправки изменений на remote.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git push <remote>`. Использовать тот же мьютекс для блокировки параллельных remote-операций. Возвращать вывод команды. После успешного push вызвать `emitGitStatusChanged()`.

- **What:** Реализовать RPC-метод `Fetch(remote string) error` для загрузки данных с remote без мержа.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git fetch <remote>`. После успешного выполнения вызвать `emitGitStatusChanged()` для обновления ahead/behind индикаторов на фронтенде.

- **What:** Расширить `GetCurrentBranch` для возврата информации об upstream (ahead/behind).
  **Where:** `backend/frontend_api_git.go`
  **How:** Дополнить метод: выполнить `git rev-list --count --left-right @{upstream}...HEAD` для получения ahead/behind счётчиков. Вернуть структуру с полями `Name`, `Upstream`, `Ahead`, `Behind`. Отображать в `GitPanelToolbar` в формате «↑N ↓M».

- **What:** Создать вторую вкладку «History» в `GitPanel` с логом коммитов.
  **Where:** `frontend/src/components/GitPanel/GitHistoryTab.tsx`
  **How:** Добавить переключатель вкладок (Changes | History) в `GitPanel`. Вкладка History отображает список коммитов (sha, автор, дата, сообщение) через новый RPC `GetCommitLog(limit int) ([]CommitInfo, error)`. Реализовать пагинацию/«Load more» для подгрузки истории. Клик по коммиту показывает список изменённых файлов в этом коммите.

- **What:** Реализовать RPC-методы для работы со stash: `StashCreate`, `StashPop`, `StashList`.
  **Where:** `backend/frontend_api_git.go`
  **How:** `StashCreate(message string) error` — выполнить `git stash push -m <message>`. `StashPop(index int) error` — выполнить `git stash pop stash@{<index>}`. `StashList() ([]StashEntry, error)` — выполнить `git stash list` и распарсить вывод. После каждого мутирующего метода вызывать `emitGitStatusChanged()`. Добавить кнопки stash в `GitPanelToolbar` (v2-секция).

- **What:** Добавить индикацию merge-конфликтов в список файлов.
  **Where:** `frontend/src/components/GitPanel/GitFileEntry.tsx`
  **How:** Определять файлы в состоянии конфликта: статус «U» (unmerged) с обеих сторон в `git status --porcelain` (код «UU», «AA», «DD»). Отображать специальную иконку (warning) и красную/оранжевую окраску строки. При клике на конфликтный файл показывать конфликтующие регионы в `FileViewerPanel` с маркерами `<<<<<<<` / `=======` / `>>>>>>>`.

- **What:** Создать нижнюю панель `GitPanelFooter` с кнопками remote-операций.
  **Where:** `frontend/src/components/GitPanel/GitPanelFooter.tsx`
  **How:** Реализовать компонент с кнопками: Pull, Push, Fetch. Каждая кнопка показывает спиннер во время выполнения операции. Блокировать параллельные remote-операции через флаг в `gitPanelStore` (`remoteOperationInProgress`). Отображать результат операции (вывод команды) в разворачиваемой секции под кнопками.

- **What:** Реализовать RPC-метод `GetCommitLog(limit int) ([]CommitInfo, error)` для получения истории коммитов.
  **Where:** `backend/frontend_api_git.go`
  **How:** Выполнить `git log --oneline -n <limit> --format=%H|%an|%ae|%ad|%s`. Распарсить вывод по разделителю `|`. Структура `CommitInfo` содержит: `SHA`, `Author`, `Email`, `Date`, `Message`. Поддерживать параметр `skip` для пагинации.

### Phase 6: Polish & Advanced Workflows

> **Цель**: Контекстные меню для файлов, частичное staging (hunk-level), rebase/merge workflow, визуализация графа коммитов.

- **What:** Добавить контекстное меню для строки файла `GitFileEntry`.
  **Where:** `frontend/src/components/GitPanel/GitFileEntry.tsx`
  **How:** Реализовать правый клик по файлу с опциями: «Discard Changes» (вызывает новый RPC `DiscardChanges(path)` с confirm-диалогом), «Add to .gitignore» (вызывает `AppendToGitignore(path)`), «Open in Editor» (открывает файл в `FileViewerPanel` в обычном режиме, не diff). Использовать нативный `onContextMenu` или библиотеку контекстного меню.

- **What:** Реализовать RPC-метод `DiscardChanges(path string) error` для отката изменений файла.
  **Where:** `backend/frontend_api_git.go`
  **How:** Для unstaged-файлов: выполнить `git checkout -- <path>`. Для staged: выполнить `git reset HEAD <path> && git checkout -- <path>`. Требовать подтверждения от пользователя на фронтенде (confirm-диалог). После выполнения вызвать `emitGitStatusChanged()`.

- **What:** Реализовать RPC-метод `AppendToGitignore(pattern string) error` для добавления файла в `.gitignore`.
  **Where:** `backend/frontend_api_git.go`
  **How:** Проверить существование `.gitignore` в корне репозитория. Если отсутствует — создать. Добавить строку `<pattern>` в конец файла (с переводом строки). Избегать дублирования паттернов — проверить, нет ли уже такого паттерна в файле.

- **What:** Реализовать частичное staging на уровне hunk'ов.
  **Where:** `backend/frontend_api_git.go`
  **How:** Добавить RPC-метод `StageHunks(path string, hunks []HunkRange) error`. `HunkRange` содержит `StartLine`, `EndLine`. Использовать подход: создать временный патч с выбранными hunk'ами через манипуляцию выводом `git diff`, применить через `git apply --cached`. Альтернативно — использовать `GIT_EDITOR` для программного `git add -p`. На фронтенде: при открытии diff в `FileViewerPanel` показывать кнопки «Stage Hunk» для каждого hunk'а.

- **What:** Реализовать rebase/merge workflow с RPC-методами и UI-элементами.
  **Where:** `backend/frontend_api_git.go`
  **How:** Добавить RPC-методы: `Merge(branch string) error` (`git merge <branch>`), `Rebase(branch string) error` (`git rebase <branch>`), `AbortMerge() error` (`git merge --abort`), `AbortRebase() error` (`git rebase --abort`). Определять активное merge/rebase состояние через `git status` (наличие `.git/MERGE_HEAD` или `.git/rebase-apply`). Добавить UI-элементы в `GitPanelToolbar`: кнопки «Abort Merge» / «Abort Rebase», появляющиеся только при активном конфликтном состоянии.

- **What:** Реализовать визуализацию графа коммитов на canvas/SVG.
  **Where:** `frontend/src/components/GitPanel/GitGraph.tsx`
  **How:** Создать компонент, отображающий граф коммитов: вертикальные линии веток, точки коммитов, merge-точки, метки веток и тегов. Данные получать через новый RPC `GetGitGraph() ([]GraphCommit, error)`, который парсит `git log --graph --format=%H|%P|%s|%d`. Визуализация в отдельной вкладке «Graph» панели Git. Использовать HTML Canvas или SVG для рисования (без тяжёлых библиотек). Реализовать скроллинг и lazy-загрузку коммитов.

---

## Сводка RPC-методов

| Метод | Фаза | Файл |
|-------|------|------|
| `GitStatus()` (обновлённый) | Phase 1 | `backend/frontend_api_git.go` |
| `StageFile(path)` | Phase 1 | `backend/frontend_api_git.go` |
| `UnstageFile(path)` | Phase 1 | `backend/frontend_api_git.go` |
| `StageAll()` | Phase 1 | `backend/frontend_api_git.go` |
| `UnstageAll()` | Phase 1 | `backend/frontend_api_git.go` |
| `Commit(message)` | Phase 1 | `backend/frontend_api_git.go` |
| `GetBranches()` | Phase 1 | `backend/frontend_api_git.go` |
| `GetCurrentBranch()` | Phase 1 | `backend/frontend_api_git.go` |
| `GetDiffStat(path)` | Phase 1 | `backend/frontend_api_git.go` |
| `GenerateCommitMessage(diff)` | Phase 4 | `backend/frontend_api_git.go` |
| `CheckoutBranch(name)` | Phase 4 | `backend/frontend_api_git.go` |
| `CreateBranch(name)` | Phase 4 | `backend/frontend_api_git.go` |
| `Pull(remote)` | Phase 5 | `backend/frontend_api_git.go` |
| `Push(remote)` | Phase 5 | `backend/frontend_api_git.go` |
| `Fetch(remote)` | Phase 5 | `backend/frontend_api_git.go` |
| `GetCommitLog(limit)` | Phase 5 | `backend/frontend_api_git.go` |
| `StashCreate(message)` | Phase 5 | `backend/frontend_api_git.go` |
| `StashPop(index)` | Phase 5 | `backend/frontend_api_git.go` |
| `StashList()` | Phase 5 | `backend/frontend_api_git.go` |
| `DiscardChanges(path)` | Phase 6 | `backend/frontend_api_git.go` |
| `AppendToGitignore(pattern)` | Phase 6 | `backend/frontend_api_git.go` |
| `StageHunks(path, hunks)` | Phase 6 | `backend/frontend_api_git.go` |
| `Merge(branch)` | Phase 6 | `backend/frontend_api_git.go` |
| `Rebase(branch)` | Phase 6 | `backend/frontend_api_git.go` |
| `AbortMerge()` | Phase 6 | `backend/frontend_api_git.go` |
| `AbortRebase()` | Phase 6 | `backend/frontend_api_git.go` |
| `GetGitGraph()` | Phase 6 | `backend/frontend_api_git.go` |

## Сводка Frontend-компонентов

| Компонент | Фаза | Файл |
|-----------|------|------|
| `gitPanelStore` (Zustand) | Phase 2 | `frontend/src/stores/gitPanelStore.ts` |
| `GitPanel` (корневой) | Phase 2 | `frontend/src/components/GitPanel/index.tsx` |
| `GitPanelToolbar` | Phase 2 | `frontend/src/components/GitPanel/GitPanelToolbar.tsx` |
| `ChangesList` | Phase 2 | `frontend/src/components/GitPanel/ChangesList.tsx` |
| `GitFileEntry` | Phase 2 | `frontend/src/components/GitPanel/GitFileEntry.tsx` |
| `CommitSection` | Phase 2 | `frontend/src/components/GitPanel/CommitSection.tsx` |
| `useGitStatusEvents` (hook) | Phase 2 | `frontend/src/hooks/useGitStatusEvents.ts` |
| Интеграция в `WorkspacePanel` | Phase 3 | `frontend/src/components/Sidebar/WorkspacePanel.tsx` |
| `BranchPicker` (модал) | Phase 4 | `frontend/src/components/GitPanel/BranchPicker.tsx` |
| `GitHistoryTab` | Phase 5 | `frontend/src/components/GitPanel/GitHistoryTab.tsx` |
| `GitPanelFooter` | Phase 5 | `frontend/src/components/GitPanel/GitPanelFooter.tsx` |
| `GitGraph` (визуализация) | Phase 6 | `frontend/src/components/GitPanel/GitGraph.tsx` |
