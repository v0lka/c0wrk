# c0wrk

## Overview

`c0wrk` — desktop-first AI workspace на Go + Wails с локальной памятью, планированием шагов и интеграцией инструментов для выполнения задач через агентный цикл.

Проект объединяет:
- **desktop shell** (Wails) для запуска и UI-интеграции,
- **frontend** (Vite + React + TypeScript) для интерфейса,
- **backend/core** на Go для планирования, исполнения шагов, памяти и инструментов.

## Features / Capabilities

Подтверждённые по коду/структуре возможности:

- Агентный workflow с планированием и пошаговым исполнением (`core/planner.go`, `core/reflector.go`, `main.go`).
- Инструментальный runtime в backend (`backend/`), включая память и тесты памяти (`backend/memory/*`).
- Desktop-приложение на **Wails v2** (наличие `wails.json`, `main.go`, `desktop/`).
- Отдельный frontend-проект (`frontend/`) для dev-режима UI.
- Makefile-цели для сборки, тестов, линтинга, dev-режимов и подготовки runtime-артефактов.
- Конфигурация через YAML-файл на основе `config.example.yaml`.

## Requirements

Минимальные требования, выведенные из репозитория:

- **Go 1.26+** (по `go.mod` и локальной среде).
- **Node.js** и npm для frontend/Wails asset pipeline.
- **Make** для запуска стандартных команд разработки.
- **Wails CLI** для desktop dev/build (если используется `make dev-desktop`/desktop-сборка).

Опционально (если используете локальные embedding/ONNX-фичи, см. Makefile):
- ONNX Runtime (через соответствующие make targets).
- Файлы embedding-моделей (через make targets загрузки/подготовки).

## Installation

### 1) Клонирование

```bash
git clone <repo-url>
cd c0wrk
```

### 2) Установка зависимостей

Go-зависимости подтягиваются автоматически при сборке/тестах, но можно прогреть кэш:

```bash
go mod download
```

Frontend-зависимости:

```bash
cd frontend
npm install
cd ..
```

Если используете desktop через Wails, убедитесь, что установлен `wails` CLI.

## Quick Start

### 1) Подготовить конфиг

Скопируйте пример:

```bash
cp config.example.yaml config.yaml
```

Откройте `config.yaml` и заполните обязательные значения (API-ключи/провайдеры/модели) в тех полях, которые реально используются вашим сценарием запуска.

### 2) Базовая сборка и запуск

```bash
make build
```

Для desktop dev-режима:

```bash
make dev-desktop
```

> `dev-desktop` в текущем `Makefile` запускает frontend dev server (`cd frontend && npm run dev`). Для полноценной desktop-сборки используйте `make build` (внутри вызывает `wails build`).

## Configuration

Основной источник настроек: `config.example.yaml`.

Рекомендуемый процесс:

1. `cp config.example.yaml config.yaml`
2. Заполнить секции провайдеров LLM/API ключи.
3. Настроить модельные/embedding-параметры.
4. При необходимости включить/настроить локальные runtime-компоненты.

Типичные категории полей в `config.example.yaml`:
- параметры модели/провайдера,
- ключи доступа (API keys),
- параметры памяти/индексации,
- пути к локальным артефактам (если используются).

> Не коммитьте `config.yaml` с секретами. Держите ключи в локальном файле или переменных окружения, если это поддержано вашими полями конфигурации.

## Development Commands

Ниже — команды, которые должны соответствовать целям Makefile:

```bash
make build
make test
make lint
make dev-desktop
```

Дополнительно (если цели присутствуют в вашем Makefile):

```bash
make dev-frontend
make download-onnx
make download-embeddings
```

Рекомендуется посмотреть полный список:

```bash
make help
```

## Platform-specific notes

Если в Makefile есть platform-specific цели для runtime-артефактов:

- Загрузка/настройка **ONNX Runtime** может отличаться для macOS/Linux.
- Пути к динамическим библиотекам и моделям должны совпадать с конфигом.
- На Apple Silicon проверьте совместимость бинарников/моделей с arm64.

Используйте соответствующие make targets из вашего Makefile для автоматизации этих шагов.

## Project Structure

Высокоуровневая структура репозитория:

```text
.
├── main.go                 # entrypoint desktop/backend app
├── Makefile                # build/dev/test/lint и utility targets
├── config.example.yaml     # пример конфигурации
├── go.mod                  # Go module и зависимости
├── backend/                # backend runtime, tools, memory
├── core/                   # planner, reflector, orchestration
├── frontend/               # Vite/React UI
├── desktop/                # desktop-specific assets/integration
├── sdk/                    # SDK/вспомогательные пакеты
└── build/                  # сборочные артефакты/настройки
```

## Troubleshooting

### Ошибка из-за отсутствующего `config.yaml`

- Убедитесь, что файл создан из `config.example.yaml`.
- Проверьте обязательные поля и формат YAML.

### Не запускается desktop dev

- Проверьте установку Wails CLI и Node.js.
- Убедитесь, что frontend-зависимости установлены (`frontend/node_modules`).

### Проблемы с ONNX/embeddings

- Запустите соответствующие make targets для загрузки runtime и моделей.
- Проверьте пути к файлам в `config.yaml`.
- Сверьте архитектуру платформы (arm64/x86_64) и совместимость бинарников.

### Линт/тесты падают локально

```bash
make test
make lint
```

Если проблемы повторяются — проверьте версии Go/Node и локальные зависимости.

## Roadmap / Notes

Судя по `TODO.md`, в проекте есть планируемые улучшения workflow (в т.ч. ReAct-поведение по умолчанию и связанные инженерные задачи).

Это рабочие заметки, а не стабильная публичная дорожная карта. Перед началом крупных изменений сверяйтесь с актуальным `TODO.md` и кодом.

## Contributing

1. Создайте ветку от актуального main.
2. Внесите изменения небольшими атомарными коммитами.
3. Перед PR прогоните:

```bash
make test
make lint
```

4. В описании PR укажите, какие модули затронуты (`core`, `backend`, `frontend`, `desktop`) и как воспроизвести изменения.
