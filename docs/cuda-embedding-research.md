# GPU-инференс эмбеддера: исследование и рабочий PoC

**Дата:** 2026-09-09 (восстановлено 2026-09-10)
**Статус:** PoC работает и собран. В продукт не перенесён.
**Машина:** Zabarch — RTX 5060 Ti 16 ГБ (Blackwell, sm_120) + RTX 2060 6 ГБ, CUDA 13.3, драйвер 610.57.04

---

## TL;DR

Индексация считалась на CPU, хотя рядом с бинарником лежала GPU-сборка `libonnxruntime.so`.
Причин оказалось три, и каждая по отдельности достаточна, чтобы GPU не заработала:

1. **CUDA execution provider никто не включал.** ONNX Runtime не выбирает GPU сам — без явного
   `AppendExecutionProviderCUDA` он исполняет граф на CPU EP, какая бы сборка библиотеки ни была
   подложена.
2. **Приложение грузило другую библиотеку.** Установленный пакет держит CPU-сборку в `/opt/c0wrk`,
   а файлы в `build/bin` при запуске через лаунчер не читаются вообще.
3. **(Обнаружено после включения EP)** CUDA EP заводит `PerThreadContext` — cuBLAS-хендл с
   workspace, ~1 ГБ видеопамяти — на **каждый OS-поток**, который входит в `Session.Run`.
   Go гоняет горутины по потокам, карта заполняется за секунды индексации, и инференс падает с
   `CUBLAS failure 3: the resource allocation failed`.

Всё три починены в PoC. Замер на jina-v2-small, батч 32 × 512 токенов: **1.61 с на CPU → 0.078 с на
GPU, ≈20×**. Потребление видеопамяти после исправления — плоские ~4 ГБ, без роста.

---

## Симптом и как он выглядел

Изначально: индексация грузит CPU, GPU простаивает. Никаких ошибок в логах — всё «работает».

После включения CUDA EP: эмбеддер стартует, сессии создаются, первые ~64 эмбеддинга проходят,
дальше лавина одинаковых ошибок и индекс не строится.

```
{"level":"WARN","msg":"per-text embedding failed; document will be dropped",
 "chunk_offset":32,"total":89,
 "error":"embedding document: running ONNX inference: Error running network:
   .../core/providers/cuda/cuda_call.cc:154 ... CUBLAS failure 3: the resource allocation failed ;
   GPU=0 ; hostname=Zabarch ;
   file=.../core/providers/cuda/cuda_execution_provider.cc ; line=231 ;
   expr=cublasCreate(&cublas_handle_);"}
{"level":"WARN","msg":"incremental indexing failed","error":"adding document batch: ..."}
```

Ключ к разгадке — `cuda_execution_provider.cc:231`, конструктор `PerThreadContext`. Не «модель не
влезла», а «кончились ресурсы при создании ещё одного per-thread контекста».

---

## Причина 1: провайдер не добавляется

ONNX Runtime по умолчанию использует CPU execution provider. GPU включается только явным
добавлением провайдера в `SessionOptions` перед созданием сессии.

```
             БЫЛО                                    НУЖНО
   ┌──────────────────────────────┐        ┌──────────────────────────────┐
   │ SetSharedLibraryPath(...)    │        │ SetSharedLibraryPath(...)    │
   │ InitializeEnvironment()      │        │ InitializeEnvironment()      │
   ├──────────────────────────────┤        ├──────────────────────────────┤
   │ opts = NewSessionOptions()   │        │ opts = NewSessionOptions()   │
   │ opts.SetIntraOpNumThreads(N) │        │ opts.SetIntraOpNumThreads(N) │
   │                              │        │ cuda = NewCUDAProviderOpts() │
   │        ничего                │        │ opts.AppendExecutionProvi... │
   ├──────────────────────────────┤        ├──────────────────────────────┤
   │ NewAdvancedSession(..., opts)│        │ NewAdvancedSession(..., opts)│
   └──────────────┬───────────────┘        └──────────────┬───────────────┘
                  ▼                                       ▼
            CPUExecutionProvider                   CUDAExecutionProvider
```

Строки `AppendExecutionProvider` не было **нигде** ни в c0wrk, ни в sp4rk. Единственное, что
настраивалось, — число intra-op потоков в `sp4rk/embedding/onnx.go:buildSessionOptions`, причём при
`intraOpThreads <= 0` функция возвращала `nil` и сессия создавалась с нулевыми опциями.

Точка создания сессии неэкспортирована (`buildSessionOptions`, `newONNXSession`), поэтому c0wrk до
неё не дотягивается — **правка обязана идти в sp4rk**.

---

## Причина 2: грузилась не та библиотека

`resolveONNXLibPath()` в `desktop/startup.go` ищет `libonnxruntime.so` рядом с исполняемым файлом.
Обёртка `/usr/bin/c0wrk-desktop` делает `exec /opt/c0wrk/c0wrk-desktop`, так что `os.Executable()`
указывает в `/opt/c0wrk` — а там CPU-сборка из пакета `c0wrk-bin-0.7.3` (24.3 МБ против 27.6 МБ у
GPU-сборки).

**Для PoC запускать только `build/bin/c0wrk-desktop` напрямую**, не `c0wrk` из меню. Чтобы GPU
работала через лаунчер, в `/opt/c0wrk` нужно положить и новый бинарник, и GPU-библиотеки
(`libonnxruntime.so` + `libonnxruntime_providers_cuda.so` + `libonnxruntime_providers_shared.so`).

---

## Причина 3: per-thread CUDA-контексты (главная и неочевидная)

CUDA EP в ONNX Runtime держит отдельное состояние на каждый OS-поток, который вызывает
`Session.Run`: свой cuBLAS-хендл со своим workspace. Go-планировщик свободно перемещает горутины
между потоками, а `EmbedDocuments` вызывается из разных горутин индексатора. Мьютекс `Embedder.mu`
сериализует вызовы, но **не привязывает их к потоку** — вызовы строго последовательны и при этом
приходят со всё новых потоков.

Замер: 60 инференсов batch=32, меняется только то, с какого потока приходит вызов.

```
        GPU used (MiB), RTX 5060 Ti 16 ГБ
16000 ┤                    ╭──────────────────  новый поток на вызов
      │              ╭─────╯                    (потолок → CUBLAS failure 3)
12000 ┤         ╭────╯
      │     ╭───╯
 8000 ┤  ╭──╯
      │╭─╯        ╭──────────────────────────   один поток: плато 5969
 4000 ┤╯   ╭──────╯
      │────────────────────────────────────────  закреплённый поток: ровно 3792
    0 ┼────┬────┬────┬────┬────┬────┬────┬────
      0    10   20   30   40   50   60  прогонов
```

| сценарий | GPU used: старт → плато | вывод |
|---|---|---|
| один OS-поток | 1755 → **5969** MiB | арена ORT растёт и стабилизируется |
| новый поток на каждый вызов | 1755 → **15839** MiB за ~20 прогонов | ~1 ГБ на поток, карта кончается |
| закреплённый поток, вызовы с 60 разных | 1854 → **3792** MiB, плоско | лечение |

**Лечение:** все обращения к ONNX Runtime исполняются на одном закреплённом
`runtime.LockOSThread()`-потоке.

---

## Дохлые ветки (не тратьте на них время)

- **`arena_extend_strategy: kSameAsRequested`** — проверено, не помогает. Рост тот же: 11573 MiB к
  10-му прогону. Значит растёт не арена аллокатора, а именно контексты.
- **`gpu_mem_limit`** — не пробовал и не советую как решение: он ограничит арену, но не количество
  cuBLAS-хендлов, и просто превратит переполнение в отказ аллокации раньше.
- **Установка cuDNN** — **не нужна**. Проверено: cuDNN в системе нет вообще, CUDA EP грузит
  `libcudnn.so` лениво через `dlopen`, а в jina-v2 (BERT-like: MatMul/Gemm/LayerNorm/Softmax) нет ни
  одного cuDNN-ядра. Сессия создаётся и считает без него. `ldd` на
  `libonnxruntime_providers_cuda.so` подтверждает: cuDNN нет среди `NEEDED`.
- **Переменная окружения ORT для выбора провайдера** — такой нет. Только API.

---

## Проверенные факты об окружении

- `libonnxruntime_providers_cuda.so` слинкована с `libcudart.so.13`, `libcublas.so.13`,
  `libcublasLt.so.13`, `libcurand.so.10` — это **CUDA 13** сборка, совпадает с установленным
  `/opt/cuda` 13.3. cu12-сборка бы не поднялась.
- ORT 1.28.1, `libonnxruntime_providers_shared.so` лежит рядом — рантайм находит провайдеры
  `dlopen`'ом из каталога основной библиотеки.
- RTX 5060 Ti (Blackwell, sm_120) поддерживается: сессия создаётся и считает корректно.
- sha256 рабочей GPU-сборки: `4680895afc920629c16fd4aea9d04e1b40cf6d66cbb1495dead4953de9377e6c`
  (27 668 560 байт). Полезно для сверки после `make`.

### ⚠️ Ловушка со стампом версии

`Makefile` короткозамыкает установку ORT, сравнивая `build/bin/.onnxruntime-version` с
`ONNX_VERSION` (`Makefile:212`). Сейчас в стампе лежит `1.28.1-gpu`, а `ONNX_VERSION` = `1.28.1` —
**значения не совпадают, и `make build` молча скачает CPU-архив поверх GPU-библиотеки.**

Пока это не решено, собирать в обход `fetch-onnx`:

```bash
wails build -tags webkit2_41 -ldflags "-X github.com/v0lka/c0wrk/core/version.Version=$(git describe --tags --dirty) -X github.com/v0lka/c0wrk/core/version.GitCommit=$(git rev-parse --short HEAD) -X github.com/v0lka/c0wrk/core/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

Либо привести стамп к `1.28.1` (тогда защита снова работает, но пропадает пометка, что это
GPU-вариант), либо — правильнее — научить `Makefile` варианту сборки с GPU, чтобы стамп и
ожидаемое значение сходились. Это часть задачи упаковки ниже.

---

## Что изменено

### `/home/zab/Git/sp4rk` (не закоммичено, ветка от `5e2a034`)

**`embedding/onnx.go`**
- Константы `ExecutionProviderCPU = "cpu"`, `ExecutionProviderCUDA = "cuda"`.
- Новая сигнатура: `buildSessionOptions(provider string, deviceID, intraOpThreads int)`.
  Ранний возврат `nil, nil` теперь срабатывает **только** для CPU — для CUDA опции нужны всегда,
  независимо от числа потоков.
- Функция `appendCUDAProvider(opts, deviceID)`: `NewCUDAProviderOptions` → `Update{device_id}` →
  `AppendExecutionProviderCUDA` → `Destroy`. Опции провайдера уничтожаются сразу после append —
  владение не передаётся сессии.
- Неизвестное значение провайдера отвергается ошибкой, а не деградирует до CPU.

**`embedding/runner.go`** (новый)
- `ortRunner`: горутина с `runtime.LockOSThread()` (никогда не разблокируется), небуферизованный
  канал `chan func()`, метод `do(fn)` синхронно исполняет задание на этом потоке, `stop()` закрывает
  канал.
- Паника внутри задания перехватывается на потоке исполнителя и перебрасывается вызывающему. Если
  дать ей улететь, горутина исполнителя умрёт и все последующие `do` зависнут навсегда на канале,
  который никто не читает. c0wrk рассчитывает на свой `recover` в горутине векторного индекса.

**`embedding/embedder.go`**
- Поля `ExecutionProvider string` и `DeviceID int` в `EmbedderConfig`.
- Поле `runner *ortRunner` в `Embedder`; создаётся в `NewEmbedder` **только** при GPU-провайдере,
  при CPU остаётся `nil`.
- Хелперы `runOnORTThread(r, fn)` (свободная функция, нужна до создания `Embedder`) и
  `(*Embedder).onORTThread(fn)`. При `nil` исполнителе вызывают `fn()` напрямую — CPU-путь не
  меняется.
- Через исполнитель проходят **все** касания ORT: `initONNXRuntime`, `buildSessionOptions`,
  создание fast-path сессии, ленивое создание batch-сессии (`ensureBatchSession`), оба пути
  инференса (`e.sess.run` для одиночного текста и `e.batchSess.runBatch` для батча), и весь
  teardown в `Close` одним заданием — после чего исполнитель останавливается.
- Провайдер и device id попали в лог `embedder initialized`.

**`embedding/runner_test.go`** (новый) — 4 теста: единственный поток при вызовах с 20 разных,
отсутствие параллелизма, выживание исполнителя после паники с пробросом её вызывающему, инлайн-путь
при `nil`. Проходят под `-race -count=2`.

**`embedding/embedder_test.go`** — существующие `TestBuildSessionOptions_*` переписаны под новую
сигнатуру, добавлен `TestBuildSessionOptions_UnknownProvider`.

### `/home/zab/Git/c0wrk` (не закоммичено, ветка `embedding-gpu-support`)

**`desktop/startup_phases.go`** — в `startVectorIndexBackground` читаются `C0WRK_ONNX_EP` и
`C0WRK_ONNX_DEVICE` (через `strconv.Atoi`, при ошибке 0) и прокидываются в `EmbedderConfig`. Лог
ошибки создания эмбеддера расширен: провайдер, device id, путь к библиотеке.

**`go.work`** (новый, в `.gitignore`) — по ADR-031 лежит в корне c0wrk:

```
go 1.27.1

use (
	.
	../sp4rk
)
```

---

## Как собрать и запустить

Сборка — см. ловушку со стампом выше. Запуск:

```bash
C0WRK_ONNX_EP=cuda ./build/bin/c0wrk-desktop
```

- `C0WRK_ONNX_EP` — `cuda` или `cpu`. Не задана = CPU, поведение прежнее.
- `C0WRK_ONNX_DEVICE` — индекс GPU, по умолчанию `0` (RTX 5060 Ti). `1` — RTX 2060.
- Фоллбэка на CPU нет намеренно: при сбое CUDA векторный поиск отключается и в логе появляется
  `vector search unavailable` с текстом ошибки ONNX Runtime.

**Признаки успеха:** в логе `embedder initialized` с `executionProvider=cuda`; нет
`per-text embedding failed` и `vector indexing failed`; в `nvidia-smi` потребление
`c0wrk-desktop` выходит на плато ~2–4 ГБ и **не растёт** дальше.

---

## Что проверено, а что нет

**Проверено:**
- CUDA EP поднимается на этой машине, инференс корректен (осмысленные значения на выходе).
- Замер CPU vs GPU: 1.61 с vs 0.078 с на батч 32×512, ≈20×.
- Исправление держит память плоской: 20 раундов × 89 документов (ровно та форма нагрузки, что
  падала в логе) с 20 разных OS-потоков — стабильные ~4030 MiB, ни одного отказа.
- CPU-путь не задет: тот же прогон без `C0WRK_ONNX_EP` не трогает GPU.
- `gofmt`, `go vet ./...`, `go test ./...` — чисто в обоих репозиториях.

**Не проверено:**
- `make lint` — `golangci-lint` в системе не установлен. Прогнать перед переносом в продукт.
- Живая индексация большого проекта из UI после исправления — проверялось на изолированном
  прогоне эмбеддера, не на полном приложении.
- Поведение на машине без GPU/драйвера с `C0WRK_ONNX_EP=cuda` — ожидается громкая ошибка и
  отключённый векторный поиск, но не проверялось.
- Windows и macOS не затрагивались.

---

## Что нужно для переноса в продукт

### Обязательное

1. **Закрепление потока — не оптимизация, а условие работоспособности.** Без него CUDA выглядит
   рабочей ровно до первой индексации. Это первое, что нужно защитить тестом при рефакторинге.

2. **Фоллбэк на CPU.** Сейчас любая ошибка `NewEmbedder` отключает векторный поиск целиком. В
   продукте нужно: попробовали CUDA → не вышло → залогировали причину → пересоздали эмбеддер на CPU.
   Осторожно с `sync.Once` в `initONNXRuntime`: **окружение ORT инициализируется один раз за процесс
   и переинициализации не поддаётся**, первый `libraryPath` окончателен. Значит фоллбэк должен
   пересоздавать только `SessionOptions` и сессии, не окружение.

3. **Ручка в конфиге вместо env.** Напрашивается `vector_index.execution_provider` и
   `vector_index.device_id` рядом с существующими `embedding_threads` / `embedding_batch_size`
   (`backend/config/config.go`). Потребует: валидацию, дефолты в `ApplyDefaults`,
   `config.example.yaml`, тесты конфига, обновление спеки домена.

4. **Упаковка.** `Makefile` тянет `onnxruntime-linux-x64-$(ONNX_VERSION).tgz` (CPU) с пиненным
   sha256. GPU-вариант — `onnxruntime-linux-x64-gpu-1.28.1.tgz`, только
   `libonnxruntime_providers_cuda.so` весит 279 МБ. Вшивать всем — вряд ли разумно. Варианты:
   отдельный пакет, докачка по требованию, или «если GPU-библиотеки лежат рядом — используем».
   Сюда же — согласование стампа версии (см. ловушку выше).

### Открытые вопросы

- **Кроссплатформенность.** Если поле называть `execution_provider`, надо сразу решить: это
  `cpu|cuda` или `auto|cpu|cuda|coreml|directml`. На macOS аналог — CoreML EP (`onnxruntime_go` его
  поддерживает), на Windows — DirectML/CUDA. От ответа зависит форма конфига и объём упаковки.
- **Размер батча.** На GPU инференс батча 32 занимает ~78 мс, и узкое место, скорее всего, переедет
  в подготовку данных (`prep_workers: 2`, чтение/хеширование/чанкинг). Возможно, `embedding_batch_size`
  захочется поднять — но только после замеров на реальной индексации, не умозрительно.
  Учтите: комментарий в sp4rk (`DefaultBatchSize`) фиксирует, что на **CPU** пропускная способность
  выходит на плато ~42 док/с уже при 32, и большие батчи только увеличивают латентность. На GPU эта
  кривая наверняка другая — её надо перемерить, а не наследовать.
- **Вторая GPU.** Стоит ли давать выбор устройства пользователю или хватит device 0.
- **Плато ~4 ГБ** — многовато для фона на 16 ГБ карте, а на 6 ГБ RTX 2060 может и не влезть
  вместе с десктопом. Если понадобится ужать — `gpu_mem_limit` и меньший батч в резерве.

---

## Дуал-репо: не наступите

Правка живёт в **двух** репозиториях, и связывает их `go.work`, который **не коммитится**
(ADR-015/025/031).

- `GOWORK=off go build` соберёт опубликованный пин sp4rk (`5e2a034`, без правок) и **молча** даст
  CPU-сборку без единой ошибки. Если GPU «перестала работать» — первым делом проверьте, что
  `go.work` на месте.
- `replace` в `go.mod` добавлять нельзя (ADR-015/001).
- `go.work` кладётся в корень c0wrk, **не** в родительский каталог — ADR-031 переносил его именно
  оттуда, потому что он протекал во все соседние проекты.
- Шапка `go.mod` про «mid-cycle state (ADR-025)» ссылается на устаревший ADR — ADR-031 её уже
  заменил. Мелочь, но при следующей правке `go.mod` стоит поправить.
- Порядок публикации: коммит+пуш sp4rk → `GOWORK=off go get github.com/v0lka/sp4rk@main && go mod tidy`
  → `GOWORK=off go build ./...` → `make build` / `make lint` / `make test` → коммит+пуш c0wrk.
  CI на `main` зелёный только в точках сдвига пина.

---

## Скрипты воспроизведения

### Замер CPU vs CUDA напрямую через `onnxruntime_go`

Модуль с `require github.com/yalue/onnxruntime_go v1.27.0`. Ключевое:

```go
ort.SetSharedLibraryPath("/home/zab/Git/c0wrk/build/bin/libonnxruntime.so")
ort.InitializeEnvironment()
opts, _ := ort.NewSessionOptions()
cuda, _ := ort.NewCUDAProviderOptions()
cuda.Update(map[string]string{"device_id": "0"})
opts.AppendExecutionProviderCUDA(cuda)
cuda.Destroy()

// batch=32, seq=512, hidden=512; входы input_ids/attention_mask/token_type_ids (int64),
// выход last_hidden_state (float32)
sess, _ := ort.NewAdvancedSession(modelPath, inputNames, outputNames, inputs, outputs, opts)
for i := 0; i < 5; i++ { t := time.Now(); sess.Run(); fmt.Println(time.Since(t)) }
```

Чтобы воспроизвести исчерпание памяти — вызывать `sess.Run()` из свежей горутины с
`runtime.LockOSThread()` на каждой итерации и печатать
`nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits -i 0`.

### Проверка исправления на настоящем эмбеддере

Модуль с `replace github.com/v0lka/sp4rk => /home/zab/Git/sp4rk`, собирать с `GOWORK=off`:

```go
emb, err := embedding.NewEmbedder(embedding.EmbedderConfig{
    ModelPath:         bin + "/models/jina-v2-small.onnx",
    TokenizerPath:     bin + "/models/jina-v2-small-tokenizer.json",
    LibraryPath:       bin + "/libonnxruntime.so",
    MaxSeqLength:      512, HiddenDim: 512, BatchSize: 32,
    ExecutionProvider: "cuda",
})
// 20 раундов × 89 документов, каждый раунд — новая горутина с runtime.LockOSThread(),
// вызовы сериализованы мьютексом. Печатать GPU used после каждого раунда.
```

Ожидание: плоские ~4000 MiB. Рост означает, что какая-то точка касания ORT осталась незакреплённой.

---

## Полезные координаты в коде

| что | где |
|---|---|
| создание эмбеддера, env-ручка | `desktop/startup_phases.go`, `startVectorIndexBackground` |
| поиск библиотеки рядом с бинарником | `desktop/startup.go`, `resolveONNXLibPath` |
| конфиг векторного индекса | `backend/config/config.go`, поля `VectorIndex.*` |
| скачивание/установка ORT, стамп версии | `Makefile`, цель `fetch-onnx`, `ONNX_STAMP` (строка 212) |
| опции сессии и провайдер | `../sp4rk/embedding/onnx.go`, `buildSessionOptions` |
| закреплённый поток | `../sp4rk/embedding/runner.go` |
| жизненный цикл эмбеддера | `../sp4rk/embedding/embedder.go`, `NewEmbedder` / `Close` |
| ADR про дуал-репо | `specs/decisions/031-gowork-repo-root.md` (и 015, 025) |
