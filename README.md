# c0wrk

Локальный desktop‑оркестратор AI‑агентов на Go + Wails.

c0wrk помогает запускать агентные сценарии на локальной машине: с настраиваемыми LLM‑провайдерами, управляемыми политиками инструментов и предсказуемым циклом сборки/разработки.

## Что это даёт

- **Локальный desktop runtime** на Wails (Go backend + frontend UI).
- **Единая YAML‑конфигурация** для LLM, MCP, execution‑лимитов, security и search.
- **Поддержка нескольких LLM‑провайдеров** с переключением через `llm.active_provider`.
- **Повторяемая сборка через Makefile** (frontend deps, build, test, lint, runtime‑артефакты).
- **Кэширование runtime‑зависимостей** (`.cache`, `.cache/models`) для более быстрых повторных сборок.

---

## Быстрый старт

```bash
# 1) Клонирование и переход в каталог
cd /path/to/c0wrk

# 2) Создать рабочий конфиг
cp config.example.yaml config.yaml

# 3) Установить frontend-зависимости
make frontend-deps

# 4) Собрать desktop-приложение
make build
```

Что делает `make build`:
1. запускает `wails build`;
2. вызывает `make fetch-onnx`;
3. вызывает `make fetch-embedding-model`.

После сборки артефакты находятся в `build/bin`.

---

## Первый запуск (walkthrough)

1. Откройте `config.yaml` и проверьте минимум:
   - `llm.active_provider`;
   - настройки выбранного провайдера (`api_key`, `model`, при необходимости `base_url`).
2. При использовании web search заполните `search.provider` и `search.api_key`.
3. Выполните `make build`.
4. Запустите собранное desktop‑приложение из `build/bin`.
5. Для запуска с инспектором Wails установите переменную:

```bash
C0WRK_DEBUG=1
```

(`main.go`: `OpenInspectorOnStartup` включается, если `C0WRK_DEBUG` не пустой.)

---

## Требования

Подтверждённые проектом зависимости:

- **Go** (используется для backend и `go test ./...`);
- **Node.js + npm** (frontend: `npm install`, `npm run dev`, `npm run lint`, `npm test`);
- **Wails CLI** (используется в `make build` через `wails build`);
- **Интернет‑доступ для первой полной сборки** (скачивание ONNX Runtime и embedding‑модели).

---

## Конфигурация

Источник истины: `config.example.yaml`.

### 1) LLM-провайдеры

Поддерживаемые значения `llm.active_provider`:

- `anthropic`
- `gemini`
- `lmstudio`
- `openai_compatible`
- `chatgpt`

Пример:

```yaml
llm:
  active_provider: "anthropic"

  anthropic:
    api_key: "${ANTHROPIC_API_KEY}"
    model: "claude-sonnet-4-20250514"
```

Пример для локального провайдера:

```yaml
llm:
  active_provider: "lmstudio"
  lmstudio:
    api_key: ""
    base_url: "http://localhost:1234"
    model: ""
```

### 2) Web Search

```yaml
search:
  provider: "tavily"
  api_key: "${TAVILY_API_KEY}"
```

### 3) MCP-серверы

```yaml
mcp:
  servers:
    example-server:
      transport: "stdio"
      command: "npx"
      args: ["-y", "some-mcp"]
```

### 4) Политики безопасности инструментов

```yaml
security:
  default_policy: "user_confirm"
  tool_policies:
    bash_exec:
      policy: "user_confirm"
    web_search:
      policy: "always_allow"
```

### 5) Лимиты исполнения агента

```yaml
executor:
  max_react_steps: 50
  max_retries: 2
  output_token_reserve: 4096
```

---

## Архитектура (высокий уровень)

- **`main.go`**
  - создаёт `desktop.NewApp()`;
  - запускает `wails.Run(...)`;
  - встраивает фронтенд через `//go:embed all:frontend/dist`.

- **`desktop/`**
  - слой desktop‑приложения и bridge между UI и backend.

- **`backend/`**
  - конфигурация и backend‑сервисы.

- **`core/`**
  - оркестрация, планирование и исполнение.

- **`sdk/`**
  - интеграции с LLM/инструментами и сопутствующая инфраструктура.

- **`frontend/`**
  - UI и dev/prod frontend‑сборка.

---

## Разработка

Команды из `Makefile`:

```bash
# frontend dependencies
make frontend-deps

# frontend dev server
make dev-desktop

# линтинг (Go + frontend)
make lint

# тесты (Go + frontend)
make test
```

Расшифровка:
- `make dev-desktop` → `cd frontend && npm run dev`
- `make lint` → `golangci-lint run` + `cd frontend && npm run lint`
- `make test` → `go test ./...` + `cd frontend && npm test`

---

## Сборка и артефакты

### Основная сборка

```bash
make build
```

### Вспомогательные цели

```bash
make fetch-onnx
make fetch-embedding-model
make clean-onnx
make clean
```

### Куда попадают артефакты

- `build/bin` — результаты сборки Wails;
- `build/bin/c0wrk-desktop.app/Contents/MacOS` — ONNX Runtime library в bundle;
- `build/bin/c0wrk-desktop.app/Contents/Resources/models` — embedding‑модель и tokenizer;
- `.cache` — кэш ONNX Runtime;
- `.cache/models` — кэш embedding‑модели и tokenizer;
- `frontend/dist` — production frontend‑ассеты (встраиваются в бинарь через `go:embed`).

---

## Диагностика и FAQ

### `wails: command not found`

`make build` вызывает `wails build`. Установите Wails CLI и повторите сборку.

### Сборка падает при скачивании ONNX/model

Проверьте сетевой доступ к:
- GitHub Releases (ONNX Runtime);
- Hugging Face (`jinaai/jina-embeddings-v2-small-en`).

Затем повторите `make build` — повторный запуск использует кэш из `.cache` и `.cache/models`.

### Приложение запускается без инспектора

Инспектор открывается только если установлена переменная `C0WRK_DEBUG`:

```bash
C0WRK_DEBUG=1
```

### Ошибки авторизации провайдера

Проверьте, что в `config.yaml` заполнены поля `api_key` у активного LLM‑провайдера и, при необходимости, `search.api_key`.

---

## Практические сценарии

### Сценарий 1: облачный LLM + web search

```yaml
llm:
  active_provider: "anthropic"
  anthropic:
    api_key: "${ANTHROPIC_API_KEY}"
    model: "claude-sonnet-4-20250514"

search:
  provider: "tavily"
  api_key: "${TAVILY_API_KEY}"
```

### Сценарий 2: локальный LLM (LM Studio)

```yaml
llm:
  active_provider: "lmstudio"
  lmstudio:
    base_url: "http://localhost:1234"
    api_key: ""
    model: ""
```

### Сценарий 3: безопасный старт с подтверждением действий

```yaml
security:
  default_policy: "user_confirm"
  tool_policies:
    bash_exec:
      policy: "user_confirm"
    write_file:
      policy: "user_confirm"
    web_search:
      policy: "always_allow"
```
