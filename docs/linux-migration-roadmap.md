# c0wrk: План миграции на Linux

## Текущее состояние

c0wrk имеет частичную поддержку Linux на уровне Go-кода: все Unix-специфичные компоненты — PTY-терминал (`creack/pty`), инструмент `bash_exec`, `fsnotify`, `modernc.org/sqlite` — работают на Linux без изменений. Рантайм-детекция ОС (`runtime.GOOS`) в `sdk/tools/envinfo.go`, `desktop/startup.go` и `backend/config/shell_env.go` корректно обрабатывает Linux.

Однако инфраструктура сборки жёстко завязана на структуру macOS `.app` bundle, что делает команду `make build` неработоспособной на Linux. Модели и ONNX-библиотека не попадают в ожидаемые пути.

| Компонент | Статус на Linux | Проблема |
|---|---|---|
| Go-компиляция (`wails build`) | Готово | Требует системных зависимостей (GTK, WebKit) |
| PTY-терминал | Готово | `creack/pty` — кроссплатформенный для Unix |
| `bash_exec` tool | Готово | `/bin/bash` доступен на Linux |
| ONNX Runtime | Частично | Архив скачивается верный, но копируется в `.app/Contents/MacOS/` |
| Embedding model | Частично | Модель качается, но копируется в `.app/Contents/Resources/` |
| `resolveModelPath()` | Частично | Не ищет модель рядом с flat binary |
| Системные зависимости | Не документированы | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` и др. |
| Shell environment | Готово | `shell_env.go` пропускает Linux (наследует нормально) |
| CI/CD | Отсутствует | Нет GitHub Actions для Linux |

---

## Фаза 1: Инфраструктура сборки

Цель: `make build` на Linux должен производить готовый к запуску бинарник с ONNX и embedding-моделью.

### 1.1 Платформенно-зависимый `APP_BUNDLE_DIR` и `APP_MODELS_DIR`

> **Статус**: отсутствует. Сейчас жёстко `build/bin/c0wrk-desktop.app/Contents/MacOS`.

**Файл**: `Makefile` (строки 39-44)

**Реализация**:
- Добавить блок `ifeq ($(UNAME_S),Linux)` после строки 33 (после Windows-else), определяющий:
  ```makefile
  APP_BUNDLE_DIR := build/bin
  APP_MODELS_DIR := build/bin/models
  ```
- Убедиться, что ветка `Darwin` осталась без изменений, ветка `Linux` использует `build/bin`, а `else` (Windows) тоже `build/bin`.
- Для Linux-ветки убрать вызов `install_name_tool` (macOS-only) в цели `fetch-onnx` (строка 78, 89-91). Вызов уже обёрнут в `if [ "$(UNAME_S)" = "Darwin" ]`, поэтому автоматически пропустится.

### 1.2 Расширение `resolveModelPath()` для flat-структуры

> **Статус**: отсутствует. Ищет только `exeDir/../Resources/models/` (macOS `.app` bundle) и `~/.c0wrk/models/`.

**Файл**: `desktop/startup.go` (строки 908-928)

**Реализация**:
- Добавить после проверки `bundlePath` (строка 918) проверку `exeDir/models/<filename>`:
  ```go
  // Linux/Windows flat layout: models/ next to binary
  flatPath := filepath.Join(exeDir, "models", filename)
  if _, statErr := os.Stat(flatPath); statErr == nil {
      return flatPath
  }
  ```
- Порядок проверок: `.app` bundle → `exeDir/models/` → `~/.c0wrk/models/`.

**Примечание**: для Linux это не критично, т.к. fallback в `~/.c0wrk/models/` работает. Но с пунктом 1.1 модель будет класться в `build/bin/models/`, и этот путь нужно уметь читать.

### 1.3 Документирование системных зависимостей Linux

> **Статус**: отсутствует.

**Файл**: `README.md`

**Реализация**: добавить секцию «Linux Build Dependencies»:

```markdown
### Linux build dependencies

Wails v2 requires native libraries for the WebKit GTK backend:

**Ubuntu/Debian 24.04+:**
```bash
sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev build-essential pkg-config
```

**Fedora 39+:**
```bash
sudo dnf install gtk3-devel webkit2gtk4.1-devel gcc pkg-config
```

**Arch Linux:**
```bash
sudo pacman -S gtk3 webkit2gtk-4.1 base-devel
```

**Runtime only** (для конечных пользователей):
```bash
# Ubuntu/Debian
sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0
```
```

---

## Фаза 2: Верификация рантайма

Цель: подтвердить, что все компоненты работают под Linux без ошибок.

### 2.1 Тестирование PTY-терминала на Linux

> **Статус**: код общий с macOS через `//go:build !windows`. Теоретически работает.

**Файлы**: `backend/terminal/manager.go`, `backend/terminal/manager_test.go`

**Что проверить**:
- Запуск `make test` на Linux — тесты `manager_test.go` должны проходить.
- Ручное тестирование: открыть терминал в UI, выполнить команды.
- Размеры окна, ресайз, цвета (xterm-256color).
- Кодировка вывода (base64).

### 2.2 Тестирование bash_exec на Linux

> **Статус**: код общий с macOS через `//go:build !windows`.

**Файлы**: `sdk/tools/builtins/bash.go`, `sdk/tools/builtins/bash_test.go`

**Что проверить**:
- `go test ./sdk/tools/builtins/...` на Linux.
- Команды с `sudo`, `rm -rf /` — блокировка через blacklist.
- Таймауты (120s default).
- Process group kill (`Setpgid`).

### 2.3 Тестирование ONNX embedding на Linux

> **Статус**: код кроссплатформенный, но не тестировался на Linux.

**Файлы**: `sdk/embedding/onnx.go`, `desktop/startup.go` (resolveONNXLibPath, строки 930-955)

**Что проверить**:
- Библиотека `libonnxruntime.so` загружается (путь `build/bin/libonnxruntime.so`).
- Embedding-модель `jina-v2-small.onnx` загружается.
- Инференс работает, эмбеддинги совпадают с macOS.

### 2.4 Тестирование Git-интеграции на Linux

> **Статус**: `git` вызывается через `os/exec`, не платформенно-специфичен.

**Файлы**: `backend/vectorindex/git.go`, `backend/frontend_api_workspace.go`

**Что проверить**:
- `git status`, `git diff`, `git branch` — все работают.
- Git monitor (`backend/vectorindex/git.go`) определяет смену ветки.

---

## Фаза 3: CI/CD

Цель: автоматическая проверка сборки и тестов на Linux.

### 3.1 GitHub Actions workflow для Linux

> **Статус**: отсутствует (нет `.github/workflows/`).

**Новый файл**: `.github/workflows/ci.yml` (или `build-linux.yml`)

**Содержание**:
```yaml
name: Linux Build & Test
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.1'
      - uses: actions/setup-node@v4
        with:
          node-version: '22'
      - name: Install system dependencies
        run: |
          sudo apt-get update
          sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev build-essential pkg-config
      - name: Install Wails CLI
        run: go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
      - name: Install golangci-lint
        run: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
      - name: Build
        run: make build
      - name: Lint
        run: make lint
      - name: Test
        run: make test
```

**Примечание**: `make build` на CI запустит `wails build`, которому нужен дисплей (X11/Wayland) для линковки GTK. На GitHub Actions нужно либо использовать `xvfb-run`, либо установить `xvfb`:
```yaml
      - name: Build
        run: xvfb-run make build
```
Перед этим добавить: `sudo apt-get install -y xvfb`.

---

## Сводка изменений

| Пункт | Файл(ы) | Тип изменения |
|---|---|---|
| 1.1 | `Makefile` | Добавить Linux-ветку для `APP_BUNDLE_DIR`/`APP_MODELS_DIR` |
| 1.2 | `desktop/startup.go:908-928` | Добавить `exeDir/models/` в `resolveModelPath` |
| 1.3 | `README.md` | Добавить секцию «Linux build dependencies» |
| 3.1 | `.github/workflows/ci.yml` | Новый файл — CI для Linux |

**Общий объём**: ~30 строк Makefile, ~5 строк Go, ~15 строк README, ~50 строк CI YAML.
