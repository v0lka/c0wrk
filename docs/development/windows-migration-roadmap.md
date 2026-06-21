# c0wrk: План миграции на Windows

## Текущее состояние

Проект имеет ограниченную поддержку Windows. Go-код содержит build tags для исключения Unix-специфичных компонентов (`terminal`, `bash_exec`), но **отсутствуют Windows-заглушки для bash_exec, что вызывает ошибку компиляции**. Рантайм-детекция ОС (`runtime.GOOS`) в `sdk/tools/envinfo.go` и `desktop/startup.go` корректно обрабатывает Windows (определение `COMSPEC`, имя ONNX DLL).

Инфраструктура сборки завязана на POSIX-окружение (`make`, shell-команды), недоступное на Windows без MSYS2. `APP_BUNDLE_DIR` и `APP_MODELS_DIR` в Makefile уже имеют платформенно-зависимые определения (Darwin → `.app/Contents/…`, Linux → `build/bin`, else → `build/bin`). `resolveModelPath()` в `desktop/startup.go` поддерживает flat-структуру `exeDir/models/` (работает на Windows и Linux).

| Компонент             | Статус на Windows  | Проблема                                                                                                       |
| --------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------- |
| Go-компиляция         | **Сломана**        | `bash.go` исключён build tag, но `builtin_registration.go` вызывает `NewBashExecToolWithTimeouts()` безусловно |
| `make build`          | Недоступен         | Нет `make` и POSIX shell без MSYS2                                                                             |
| PTY-терминал          | Заглушка (ошибка)  | `manager_stub.go` возвращает «not supported»                                                                   |
| `bash_exec` tool      | **Отсутствует**    | Нет Windows-заглушки; shell-альтернатива не реализована                                                        |
| ONNX Runtime          | Частично           | Архив (.zip) определяется верно, `APP_BUNDLE_DIR` платформенно-зависим                                        |
| Embedding model       | Частично           | `resolveModelPath()` поддерживает flat-структуру `exeDir/models/`                                             |
| WebView2 Runtime      | Требуется          | Evergreen Runtime (предустановлен в Win 11)                                                                    |
| Системные зависимости | Не документированы | Go 1.21+, CGO (MinGW-w64), WebView2                                                                            |
| CI/CD                 | Отсутствует        | Нет GitHub Actions для Windows                                                                                 |
| PowerShell build      | Отсутствует        | Нет `scripts/build.ps1`                                                                                        |

---

## Фаза 1: Исправление компиляции (blocking)

Цель: `go build` должен проходить под Windows без ошибок.

### 1.1 Создание Windows-заглушки для `bash_exec`

> **Статус**: отсутствует. `sdk/tools/builtins/builtin_registration.go` вызывает `builtins.NewBashExecToolWithTimeouts()`, но `bash.go` исключён build tag `!windows`. **Компиляция падает с undefined symbol.**

**Новый файл**: `sdk/tools/builtins/bash_stub.go`

**Реализация**: создать файл с `//go:build windows`, содержащий no-op реализацию:

```go
//go:build windows

package builtins

import (
    "context"
    "errors"

    "github.com/v0lka/c0wrk/sdk/agent"
)

// BashExecTool is a no-op stub on Windows.
type BashExecTool struct{}

// NewBashExecTool creates a no-op tool on Windows.
func NewBashExecTool(blacklist []string) (*BashExecTool, error) {
    return NewBashExecToolWithTimeouts(blacklist, DefaultBashTimeouts())
}

// NewBashExecToolWithTimeouts creates a no-op tool on Windows.
func NewBashExecToolWithTimeouts(_ []string, _ BashTimeouts) (*BashExecTool, error) {
    return &BashExecTool{}, nil
}

// ToolName returns the tool identifier.
func (t *BashExecTool) ToolName() string { return "bash_exec" }

// ToolDescription returns the tool description (with platform note).
func (t *BashExecTool) ToolDescription() string {
    return "Execute shell commands. On Windows, bash is not available natively. " +
        "Install Git Bash (MSYS2) or WSL to enable shell command execution, " +
        "or use PowerShell/cmd commands through the terminal."
}

// ParameterSchema returns the JSON Schema for tool parameters.
func (t *BashExecTool) ParameterSchema() json.RawMessage { ... }

// Execute returns a clear error message.
func (t *BashExecTool) Execute(_ context.Context, _ json.RawMessage, _ agent.ToolContext) (string, error) {
    return "", errors.New(
        "bash_exec is not supported on Windows. Use the terminal for PowerShell/cmd commands, " +
        "or install Git Bash / WSL for bash support.",
    )
}
```

**Примечание**: типы `BashTimeouts` и `DefaultBashTimeouts()` находятся в `sdk/tools/builtins/limits.go` (без build tag) — они доступны на всех платформах.

### 1.2 Верификация компиляции

> **Статус**: после 1.1.

**Команда**: `GOOS=windows GOARCH=amd64 go build ./...`

**Ожидаемый результат**: все пакеты компилируются. Тесты с build tag `!windows` пропускаются. Тесты без build tag проходят.

**Уточнение**: `CGO_ENABLED=1` не требуется для компиляции Go-кода проекта (`modernc.org/sqlite` — CGO-free). Но для `wails build` CGO понадобится (линковка с Windows API).

---

## Фаза 2: Инфраструктура сборки

Цель: полный цикл `wails build` + копирование ONNX и моделей на Windows.

### 2.1 Windows build script (альтернатива Makefile)

> **Статус**: отсутствует. На Windows нет `make` без MSYS2. `APP_BUNDLE_DIR` и `APP_MODELS_DIR` в Makefile уже платформенно-зависимы, но сам `make` недоступен.

**Новый файл**: `scripts/build.ps1` (PowerShell)

**Содержание**:

```powershell
param([switch]$SkipWails, [switch]$SkipOnnx, [switch]$SkipModels)

$ErrorActionPreference = "Stop"
$ONNX_VERSION = "1.24.1"
$BUILD_DIR = "build\bin"

# 1. Frontend deps
Write-Host "Installing frontend dependencies..."
Push-Location frontend
npm install
npm run build
Pop-Location

# 2. Wails build
if (-not $SkipWails) {
    Write-Host "Building with Wails..."
    wails build
}

# 3. ONNX Runtime
if (-not $SkipOnnx) {
    $onnxDll = Join-Path $BUILD_DIR "onnxruntime.dll"
    if (-not (Test-Path $onnxDll)) {
        Write-Host "Downloading ONNX Runtime $ONNX_VERSION..."
        $zip = "onnxruntime-win-x64-$ONNX_VERSION.zip"
        $url = "https://github.com/microsoft/onnxruntime/releases/download/v$ONNX_VERSION/$zip"
        Invoke-WebRequest -Uri $url -OutFile "$env:TEMP\$zip"
        Expand-Archive -Path "$env:TEMP\$zip" -DestinationPath "$env:TEMP\onnx" -Force
        $src = Get-ChildItem "$env:TEMP\onnx\*\lib\onnxruntime.dll" -Recurse | Select-Object -First 1
        Copy-Item $src.FullName $onnxDll
        Remove-Item "$env:TEMP\$zip", "$env:TEMP\onnx" -Recurse -Force
        Write-Host "ONNX Runtime installed to $onnxDll"
    }
}

# 4. Embedding model
if (-not $SkipModels) {
    $modelsDir = Join-Path $BUILD_DIR "models"
    New-Item -ItemType Directory -Force -Path $modelsDir | Out-Null
    $modelFile = Join-Path $modelsDir "jina-v2-small.onnx"
    $tokFile = Join-Path $modelsDir "jina-v2-small-tokenizer.json"
    if (-not (Test-Path $modelFile)) {
        Write-Host "Downloading embedding model..."
        Invoke-WebRequest -Uri "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx" -OutFile $modelFile
    }
    if (-not (Test-Path $tokFile)) {
        Write-Host "Downloading tokenizer..."
        Invoke-WebRequest -Uri "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json" -OutFile $tokFile
    }
}

Write-Host "Build complete. Binary: $BUILD_DIR\c0wrk-desktop.exe"
```

**Примечание**: PowerShell-скрипт также можно написать на Go (один бинарник для всех платформ). Вариант: добавить `cmd/c0wrk-tools/` с командой `download-assets` для скачивания ONNX и моделей.

---

## Фаза 3: Функциональные заглушки и альтернативы

Цель: на Windows агент должен либо работать с альтернативными инструментами, либо получать внятные сообщения об ошибках вместо падений.

### 3.1 Терминал: улучшить сообщение об ошибке

> **Статус**: частично — `core/terminal/manager_stub.go` возвращает «terminal not supported on Windows». Сообщение базовое, без указания альтернатив.

**Файл**: `core/terminal/manager_stub.go`

**Реализация**: улучшить сообщение об ошибке, чтобы пользователь понимал альтернативы:

```go
func (*Manager) Start(_, _ string) error {
    return errors.New("terminal not supported on Windows. Use an external terminal (PowerShell, CMD, Windows Terminal) instead.")
}
```

**Перспектива (не в MVP)**: реализовать `manager_windows.go` через Windows ConPTY API (`golang.org/x/term` + Win32 Pseudo Console). Сложность: высокая (~200-400 строк, требует CGO, работу с `CreatePseudoConsole`, `STARTUPINFOEX`).

### 3.2 Shell-альтернатива для `bash_exec`

> **Статус**: после Фазы 1 инструмент `bash_exec` будет доступен как no-op. Агент не сможет выполнять shell-команды.

**Варианты реализации** (выбрать один):

**Вариант A — PowerShell fallback**:

- В `bash_stub.go` (или новом `bash_windows.go`) реализовать `Execute()` через `powershell -Command "..."`.
- Чёрный список адаптировать под PowerShell (`rm -rf /` → `Remove-Item -Recurse -Force C:\`).
- **Плюсы**: PowerShell предустановлен на всех Windows 10+.
- **Минусы**: другой синтаксис, скрипты macOS/Linux несовместимы.

**Вариант B — Git Bash fallback**:

- Искать `C:\Program Files\Git\bin\bash.exe` при старте.
- Если найден — использовать как замену `/bin/bash`.
- **Плюсы**: совместимость с существующими скриптами.
- **Минусы**: требует установки Git for Windows.

**Вариант C — WSL fallback**:

- Искать `wsl.exe`, выполнять команды через `wsl bash -c "..."`.
- **Плюсы**: полноценный Linux.
- **Минусы**: требует установки WSL; медленный запуск.

**Рекомендация**: для MVP — Вариант A (PowerShell) с автоматическим детектированием Git Bash как приоритетного, если он установлен. Для этого нужна абстракция `ShellProvider`:

```go
type ShellProvider interface {
    Execute(ctx context.Context, command string, workDir string, timeout time.Duration) (string, error)
    Name() string
}

type PowerShellProvider struct { ... } // powershell -Command
type GitBashProvider struct { ... }     // bash -c
type WSLProvider struct { ... }         // wsl bash -c
```

Файл: `sdk/tools/builtins/bash_windows.go`

### 3.3 Frontend: скрыть или заблокировать терминал на Windows

> **Статус**: отсутствует — UI показывает вкладку терминала, но backend возвращает ошибку.

**Решение**: фронтенд может проверять платформу через `window.runtime.Environment().Platform` (Wails API) и скрывать кнопку терминала на Windows, либо показывать информационное сообщение вместо панели терминала.

**Приоритет**: низкий (MVP — оставить как есть, ошибка «not supported» информативна).

---

## Фаза 4: Документация и CI/CD

### 4.1 Документирование системных зависимостей Windows

> **Статус**: отсутствует. README.md только упоминает «Windows (x64, via zip runtime artifact path)» без секции build-зависимостей.

**Файл**: `README.md`

**Реализация**: добавить секцию «Windows Build Dependencies»:

````markdown
### Windows build dependencies

**Required:**

- Go 1.26.1+
- Node.js 22+ with npm
- Wails v2 CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0`)
- **WebView2 Evergreen Runtime** — preinstalled on Windows 11; on Windows 10, download from [Microsoft](https://developer.microsoft.com/microsoft-edge/webview2/)

**C toolchain (for CGO):**

- Install [MSYS2](https://www.msys2.org/)
- Run: `pacman -S mingw-w64-x86_64-gcc`

Or use [TDM-GCC](https://jmeubank.github.io/tdm-gcc/)

**Runtime only:**

- WebView2 Evergreen Runtime
- (Optional) Git for Windows — for `git` integration
- (Optional) ripgrep (`rg`) — for content search tool (`choco install ripgrep` or `scoop install ripgrep`)

**Build commands (PowerShell):**

```powershell
# Option 1: Using PowerShell build script
.\scripts\build.ps1

# Option 2: Using Wails directly (ONNX and models must be placed manually)
cd frontend; npm install; npm run build; cd ..
wails build
# Then place onnxruntime.dll and models/ next to c0wrk-desktop.exe in build\bin\
```
````

### 4.2 GitHub Actions workflow для Windows

> **Статус**: отсутствует. `.github/workflows/ci.yml` содержит только `linux` и `macos` jobs.

**Новый файл**: добавить Windows job в `.github/workflows/ci.yml`

```yaml
  build-windows:
    runs-on: windows-2022
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - uses: actions/setup-node@v4
        with:
          node-version: '22'
      - name: Install Wails CLI
        run: go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
      - name: Install golangci-lint
        run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
      - name: Build
        run: |
          cd frontend; npm install; npm run build; cd ..
          wails build
      - name: Test
        run: go test ./...
      - name: Frontend test
        run: cd frontend && npm test
```

**Примечание**: на Windows CI нет `make` и `xvfb-run`. Используем прямые команды.

### 4.3 Обновление `.gitignore` для Windows-артефактов

> **Статус**: частично — `.gitignore` уже содержит общие Windows-паттерны (`Thumbs.db`, `Desktop.ini`, `*.exe`, `*.dll`). Не хватает специфичных путей.

**Файл**: `.gitignore`

**Добавить** (специфичные для проекта пути):

```
# Windows build artifacts (project-specific)
build/bin/c0wrk-desktop.exe
build/bin/onnxruntime.dll
```

---

## Сводка изменений

| Пункт | Файл(ы)                              | Тип изменения                                                | Фаза | Статус     |
| ----- | ------------------------------------ | ------------------------------------------------------------ | ---- | ---------- |
| 1.1   | `sdk/tools/builtins/bash_stub.go`    | **Новый** — Windows no-op заглушка                           | 1    | Отсутствует |
| 1.2   | —                                    | Верификация компиляции                                       | 1    | Блокировано 1.1 |
| 2.1   | `scripts/build.ps1`                  | **Новый** — PowerShell build-скрипт                          | 2    | Отсутствует |
| 3.1   | `core/terminal/manager_stub.go`      | Улучшить сообщение об ошибке                                 | 3    | Частично   |
| 3.2   | `sdk/tools/builtins/bash_windows.go` | **Новый** — Shell-альтернатива (PowerShell/Git Bash/WSL)     | 3    | Отсутствует |
| 3.3   | `frontend/...`                       | Скрыть/заблокировать терминал на Windows                     | 3    | Отсутствует |
| 4.1   | `README.md`                          | Добавить секцию «Windows build dependencies»                 | 4    | Отсутствует |
| 4.2   | `.github/workflows/ci.yml`           | Добавить Windows job                                         | 4    | Отсутствует |
| 4.3   | `.gitignore`                         | Добавить специфичные Windows-пути                            | 4    | Частично   |

**Уже реализовано (исключено из плана)**:

| Пункт | Файл(ы)                | Что сделано                                                        |
| ----- | ---------------------- | ------------------------------------------------------------------ |
| —     | `Makefile`             | `APP_BUNDLE_DIR`/`APP_MODELS_DIR` платформенно-зависимы (Darwin/Linux/Windows) |
| —     | `desktop/startup.go`   | `resolveModelPath()` поддерживает flat-структуру `exeDir/models/` |

**Общий объём оставшихся работ**: ~80 строк Go (заглушки), ~80 строк PowerShell, ~10 строк Go (терминал), ~30 строк README, ~30 строк CI YAML, ~5 строк .gitignore.

---

## Диаграмма зависимостей между задачами

```
Фаза 1 (компиляция) ── blocking ──► Фаза 2 (сборка)
                                         │
                                         ▼
                                    Фаза 3 (runtime) ──► Фаза 4 (CI/docs)
```

- **Фаза 1** — строго блокирующая: без неё код не компилируется под Windows.
- **Фаза 2** — необходима для получения рабочего `.exe` с ONNX.
- **Фаза 3** — улучшает UX; MVP можно выпускать с заглушками.
- **Фаза 4** — документирует процесс и добавляет автоматическую проверку.

**Минимальный viable продукт для Windows**: Фаза 1 + Фаза 2 + частично Фаза 4 (README). Это даёт компилируемый `.exe` с ONNX и документацию. Терминал и shell-команды будут недоступны, но UI и все остальные инструменты (read/write/edit/list/search/fetch) будут работать.
# c0wrk: План миграции на Windows

## Текущее состояние

Проект имеет ограниченную поддержку Windows. Go-код содержит build tags для исключения Unix-специфичных компонентов (`terminal`, `bash_exec`), но **отсутствуют Windows-заглушки для bash_exec, что вызывает ошибку компиляции**. Рантайм-детекция ОС (`runtime.GOOS`) в `sdk/tools/envinfo.go` и `desktop/startup.go` корректно обрабатывает Windows (определение `COMSPEC`, имя ONNX DLL).

Инфраструктура сборки завязана на POSIX-окружение (`make`, shell-команды), недоступное на Windows без MSYS2.

| Компонент             | Статус на Windows  | Проблема                                                                                                       |
| --------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------- |
| Go-компиляция         | **Сломана**        | `bash.go` исключён build tag, но `builtin_registration.go` вызывает `NewBashExecToolWithTimeouts()` безусловно |
| `make build`          | Недоступен         | Нет `make` и POSIX shell без MSYS2                                                                             |
| PTY-терминал          | Заглушка (ошибка)  | `manager_stub.go` возвращает «not supported»                                                                   |
| `bash_exec` tool      | **Отсутствует**    | Нет Windows-заглушки; shell-альтернатива не реализована                                                        |
| ONNX Runtime          | Частично           | Архив (.zip) определяется верно, но копируется в `.app/Contents/MacOS/`                                        |
| Embedding model       | Частично           | Модель качается, но копируется в `.app/Contents/Resources/`                                                    |
| `resolveModelPath()`  | Частично           | Не ищет модель рядом с `.exe`                                                                                  |
| WebView2 Runtime      | Требуется          | Evergreen Runtime (предустановлен в Win 11)                                                                    |
| Системные зависимости | Не документированы | Go 1.21+, CGO (MinGW-w64), WebView2                                                                            |
| CI/CD                 | Отсутствует        | Нет GitHub Actions для Windows                                                                                 |

---

## Фаза 1: Исправление компиляции (blocking)

Цель: `go build` должен проходить под Windows без ошибок.

### 1.1 Создание Windows-заглушки для `bash_exec`

> **Статус**: отсутствует. `sdk/tools/builtins/builtin_registration.go` вызывает `builtins.NewBashExecToolWithTimeouts()`, но `bash.go` исключён build tag `!windows`. **Компиляция падает с undefined symbol.**

**Новый файл**: `sdk/tools/builtins/bash_stub.go`

**Реализация**: создать файл с `//go:build windows`, содержащий no-op реализацию:

```go
//go:build windows

package builtins

import (
    "context"
    "errors"

    "github.com/v0lka/c0wrk/sdk/agent"
)

// BashExecTool is a no-op stub on Windows.
type BashExecTool struct{}

// NewBashExecTool creates a no-op tool on Windows.
func NewBashExecTool(blacklist []string) (*BashExecTool, error) {
    return NewBashExecToolWithTimeouts(blacklist, DefaultBashTimeouts())
}

// NewBashExecToolWithTimeouts creates a no-op tool on Windows.
func NewBashExecToolWithTimeouts(_ []string, _ BashTimeouts) (*BashExecTool, error) {
    return &BashExecTool{}, nil
}

// ToolName returns the tool identifier.
func (t *BashExecTool) ToolName() string { return "bash_exec" }

// ToolDescription returns the tool description (with platform note).
func (t *BashExecTool) ToolDescription() string {
    return "Execute shell commands. On Windows, bash is not available natively. " +
        "Install Git Bash (MSYS2) or WSL to enable shell command execution, " +
        "or use PowerShell/cmd commands through the terminal."
}

// ParameterSchema returns the JSON Schema for tool parameters.
func (t *BashExecTool) ParameterSchema() json.RawMessage { ... }

// Execute returns a clear error message.
func (t *BashExecTool) Execute(_ context.Context, _ json.RawMessage, _ agent.ToolContext) (string, error) {
    return "", errors.New(
        "bash_exec is not supported on Windows. Use the terminal for PowerShell/cmd commands, " +
        "or install Git Bash / WSL for bash support.",
    )
}
```

**Примечание**: типы `BashTimeouts` и `DefaultBashTimeouts()` находятся в `sdk/tools/builtins/limits.go` (без build tag) — они доступны на всех платформах.

### 1.2 Верификация компиляции

> **Статус**: после 1.1.

**Команда**: `GOOS=windows GOARCH=amd64 go build ./...`

**Ожидаемый результат**: все пакеты компилируются. Тесты с build tag `!windows` пропускаются. Тесты без build tag проходят.

**Уточнение**: `CGO_ENABLED=1` не требуется для компиляции Go-кода проекта (`modernc.org/sqlite` — CGO-free). Но для `wails build` CGO понадобится (линковка с Windows API).

---

## Фаза 2: Инфраструктура сборки

Цель: полный цикл `wails build` + копирование ONNX и моделей на Windows.

### 2.1 Платформенно-зависимый `APP_BUNDLE_DIR` и `APP_MODELS_DIR` в Makefile

> **Статус**: Makefile уже определяет `ONNX_ARCHIVE := onnxruntime-win-x64-1.24.1.zip` для Windows-ветки (строка 30), но `APP_BUNDLE_DIR` жёстко задан как `.app/Contents/MacOS`.

**Файл**: `Makefile` (строки 39-44)

**Реализация**: переместить определение `APP_BUNDLE_DIR` и `APP_MODELS_DIR` внутрь платформенных `ifeq`-блоков (строки 11-33), добавив в ветку `else` (Windows):

```makefile
APP_BUNDLE_DIR := build/bin
APP_MODELS_DIR := build/bin\models
```

Для совместимости с Windows использовать прямой слеш `/` (Go-under-MSYS2 понимает оба варианта) или добавить `$(shell cygpath -w ...)` при использовании cmd/PowerShell.

### 2.2 Windows build script (альтернатива Makefile)

> **Статус**: отсутствует. На Windows нет `make` без MSYS2.

**Новый файл**: `scripts/build.ps1` (PowerShell)

**Содержание**:

```powershell
param([switch]$SkipWails, [switch]$SkipOnnx, [switch]$SkipModels)

$ErrorActionPreference = "Stop"
$ONNX_VERSION = "1.24.1"
$BUILD_DIR = "build\bin"

# 1. Frontend deps
Write-Host "Installing frontend dependencies..."
Push-Location frontend
npm install
npm run build
Pop-Location

# 2. Wails build
if (-not $SkipWails) {
    Write-Host "Building with Wails..."
    wails build
}

# 3. ONNX Runtime
if (-not $SkipOnnx) {
    $onnxDll = Join-Path $BUILD_DIR "onnxruntime.dll"
    if (-not (Test-Path $onnxDll)) {
        Write-Host "Downloading ONNX Runtime $ONNX_VERSION..."
        $zip = "onnxruntime-win-x64-$ONNX_VERSION.zip"
        $url = "https://github.com/microsoft/onnxruntime/releases/download/v$ONNX_VERSION/$zip"
        Invoke-WebRequest -Uri $url -OutFile "$env:TEMP\$zip"
        Expand-Archive -Path "$env:TEMP\$zip" -DestinationPath "$env:TEMP\onnx" -Force
        $src = Get-ChildItem "$env:TEMP\onnx\*\lib\onnxruntime.dll" -Recurse | Select-Object -First 1
        Copy-Item $src.FullName $onnxDll
        Remove-Item "$env:TEMP\$zip", "$env:TEMP\onnx" -Recurse -Force
        Write-Host "ONNX Runtime installed to $onnxDll"
    }
}

# 4. Embedding model
if (-not $SkipModels) {
    $modelsDir = Join-Path $BUILD_DIR "models"
    New-Item -ItemType Directory -Force -Path $modelsDir | Out-Null
    $modelFile = Join-Path $modelsDir "jina-v2-small.onnx"
    $tokFile = Join-Path $modelsDir "jina-v2-small-tokenizer.json"
    if (-not (Test-Path $modelFile)) {
        Write-Host "Downloading embedding model..."
        Invoke-WebRequest -Uri "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx" -OutFile $modelFile
    }
    if (-not (Test-Path $tokFile)) {
        Write-Host "Downloading tokenizer..."
        Invoke-WebRequest -Uri "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json" -OutFile $tokFile
    }
}

Write-Host "Build complete. Binary: $BUILD_DIR\c0wrk-desktop.exe"
```

**Примечание**: PowerShell-скрипт также можно написать на Go (один бинарник для всех платформ). Вариант: добавить `cmd/c0wrk-tools/` с командой `download-assets` для скачивания ONNX и моделей.

### 2.3 Расширение `resolveModelPath()` для Windows flat-структуры

> **Статус**: аналогично Linux (см. `docs/linux-migration-roadmap.md` пункт 1.2).

**Файл**: `desktop/startup.go` (строки 908-928)

**Реализация**:

- Добавить проверку `exeDir/models/<filename>` (работает и на Windows через `filepath.Join`).
- Для Windows бандл (.app) не существует, поэтому проверка `exeDir/../Resources/models/` (строка 915) безопасно вернёт `os.Stat` error и перейдёт к flat-пути.

---

## Фаза 3: Функциональные заглушки и альтернативы

Цель: на Windows агент должен либо работать с альтернативными инструментами, либо получать внятные сообщения об ошибках вместо падений.

### 3.1 Терминал: улучшить сообщение об ошибке

> **Статус**: `backend/terminal/manager_stub.go` возвращает «terminal not supported on Windows». Этого достаточно для MVP.

**Файл**: `backend/terminal/manager_stub.go`

**Реализация**: улучшить сообщение об ошибке, чтобы пользователь понимал альтернативы:

```go
func (*Manager) Start(_, _ string) error {
    return errors.New("terminal not supported on Windows. Use an external terminal (PowerShell, CMD, Windows Terminal) instead.")
}
```

**Перспектива (не в MVP)**: реализовать `manager_windows.go` через Windows ConPTY API (`golang.org/x/term` + Win32 Pseudo Console). Сложность: высокая (~200-400 строк, требует CGO, работу с `CreatePseudoConsole`, `STARTUPINFOEX`).

### 3.2 Shell-альтернатива для `bash_exec`

> **Статус**: после Фазы 1 инструмент `bash_exec` будет доступен как no-op. Агент не сможет выполнять shell-команды.

**Варианты реализации** (выбрать один):

**Вариант A — PowerShell fallback**:

- В `bash_stub.go` (или новом `bash_windows.go`) реализовать `Execute()` через `powershell -Command "..."`.
- Чёрный список адаптировать под PowerShell (`rm -rf /` → `Remove-Item -Recurse -Force C:\`).
- **Плюсы**: PowerShell предустановлен на всех Windows 10+.
- **Минусы**: другой синтаксис, скрипты macOS/Linux несовместимы.

**Вариант B — Git Bash fallback**:

- Искать `C:\Program Files\Git\bin\bash.exe` при старте.
- Если найден — использовать как замену `/bin/bash`.
- **Плюсы**: совместимость с существующими скриптами.
- **Минусы**: требует установки Git for Windows.

**Вариант C — WSL fallback**:

- Искать `wsl.exe`, выполнять команды через `wsl bash -c "..."`.
- **Плюсы**: полноценный Linux.
- **Минусы**: требует установки WSL; медленный запуск.

**Рекомендация**: для MVP — Вариант A (PowerShell) с автоматическим детектированием Git Bash как приоритетного, если он установлен. Для этого нужна абстракция `ShellProvider`:

```go
type ShellProvider interface {
    Execute(ctx context.Context, command string, workDir string, timeout time.Duration) (string, error)
    Name() string
}

type PowerShellProvider struct { ... } // powershell -Command
type GitBashProvider struct { ... }     // bash -c
type WSLProvider struct { ... }         // wsl bash -c
```

Файл: `sdk/tools/builtins/bash_windows.go`

### 3.3 Frontend: скрыть или заблокировать терминал на Windows

> **Статус**: UI показывает вкладку терминала, но backend возвращает ошибку.

**Решение**: фронтенд может проверять платформу через `window.runtime.Environment().Platform` (Wails API) и скрывать кнопку терминала на Windows, либо показывать информационное сообщение вместо панели терминала.

**Приоритет**: низкий (MVP — оставить как есть, ошибка «not supported» информативна).

---

## Фаза 4: Документация и CI/CD

### 4.1 Документирование системных зависимостей Windows

> **Статус**: отсутствует.

**Файл**: `README.md`

**Реализация**: добавить секцию «Windows Build Dependencies»:

````markdown
### Windows build dependencies

**Required:**

- Go 1.26.1+
- Node.js 22+ with npm
- Wails v2 CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0`)
- **WebView2 Evergreen Runtime** — preinstalled on Windows 11; on Windows 10, download from [Microsoft](https://developer.microsoft.com/microsoft-edge/webview2/)

**C toolchain (for CGO):**

- Install [MSYS2](https://www.msys2.org/)
- Run: `pacman -S mingw-w64-x86_64-gcc`

Or use [TDM-GCC](https://jmeubank.github.io/tdm-gcc/)

**Runtime only:**

- WebView2 Evergreen Runtime
- (Optional) Git for Windows — for `git` integration
- (Optional) ripgrep (`rg`) — for content search tool (`choco install ripgrep` or `scoop install ripgrep`)

**Build commands (PowerShell):**

```powershell
# Option 1: Using PowerShell build script
.\scripts\build.ps1

# Option 2: Using Wails directly (ONNX and models must be placed manually)
cd frontend; npm install; npm run build; cd ..
wails build
# Then place onnxruntime.dll and models/ next to c0wrk-desktop.exe in build\bin\
```
````

````

### 4.2 GitHub Actions workflow для Windows

> **Статус**: отсутствует.

**Новый файл**: добавить Windows job в `.github/workflows/ci.yml`

```yaml
  build-windows:
    runs-on: windows-2022
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - uses: actions/setup-node@v4
        with:
          node-version: '22'
      - name: Install Wails CLI
        run: go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
      - name: Install golangci-lint
        run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
      - name: Build
        run: |
          cd frontend; npm install; npm run build; cd ..
          wails build
      - name: Test
        run: go test ./...
      - name: Frontend test
        run: cd frontend && npm test
````

**Примечание**: на Windows CI нет `make` и `xvfb-run`. Используем прямые команды.

### 4.3 Обновление `.gitignore` для Windows-артефактов

> **Статус**: отсутствуют правила для Windows.

**Файл**: `.gitignore`

**Добавить**:

```
# Windows build artifacts
*.exe
*.dll
build/bin/c0wrk-desktop.exe
build/bin/onnxruntime.dll
```

---

## Сводка изменений

| Пункт | Файл(ы)                              | Тип изменения                                                | Фаза |
| ----- | ------------------------------------ | ------------------------------------------------------------ | ---- |
| 1.1   | `sdk/tools/builtins/bash_stub.go`    | **Новый** — Windows no-op заглушка                           | 1    |
| 2.1   | `Makefile`                           | Добавить Windows-ветку для `APP_BUNDLE_DIR`/`APP_MODELS_DIR` | 2    |
| 2.2   | `scripts/build.ps1`                  | **Новый** — PowerShell build-скрипт                          | 2    |
| 2.3   | `desktop/startup.go`.                | Добавить `exeDir/models/` в `resolveModelPath`               | 2    |
| 3.1   | `backend/terminal/manager_stub.go`   | Улучшить сообщение об ошибке                                 | 3    |
| 3.2   | `sdk/tools/builtins/bash_windows.go` | **Новый** — Shell-альтернатива (PowerShell/Git Bash/WSL)     | 3    |
| 3.3   | `frontend/...`                       | Скрыть/заблокировать терминал на Windows                     | 3    |
| 4.1   | `README.md`                          | Добавить секцию «Windows build dependencies»                 | 4    |
| 4.2   | `.github/workflows/ci.yml`           | Добавить Windows job                                         | 4    |
| 4.3   | `.gitignore`                         | Добавить Windows-паттерны                                    | 4    |

**Общий объём**: ~80 строк Go (заглушки), ~60 строк Makefile, ~80 строк PowerShell, ~10 строк Go (resolveModelPath), ~30 строк README, ~30 строк CI YAML, ~5 строк .gitignore.

---

## Диаграмма зависимостей между задачами

```
Фаза 1 (компиляция) ── blocking ──► Фаза 2 (сборка)
                                         │
                                         ▼
                                    Фаза 3 (runtime) ──► Фаза 4 (CI/docs)
```

- **Фаза 1** — строго блокирующая: без неё код не компилируется под Windows.
- **Фаза 2** — необходима для получения рабочего `.exe` с ONNX.
- **Фаза 3** — улучшает UX; MVP можно выпускать с заглушками.
- **Фаза 4** — документирует процесс и добавляет автоматическую проверку.

**Минимальный viable продукт для Windows**: Фаза 1 + Фаза 2 + частично Фаза 4 (README). Это даёт компилируемый `.exe` с ONNX и документацию. Терминал и shell-команды будут недоступны, но UI и все остальные инструменты (read/write/edit/list/search/fetch) будут работать.
