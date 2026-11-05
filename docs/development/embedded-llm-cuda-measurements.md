# Embedded LLM (Ternary Bonsai 2 27B) — CUDA-замеры распределения памяти

Дата: 2026-11-03. GPU: NVIDIA GeForce RTX 5060 Ti 16 ГБ (Blackwell, arch 1200), runtime `prism-b10735-842b188`, сборка **cuda-13.3**. Модель: `Ternary-Bonsai-2-27B-PQ2_0.gguf` (GGUF V3, 851 тензор), 65/65 слоёв на GPU, packing PQ2_0.

## Зачем

Исходные данные для memory-plan (`core/embeddedllm/plan.go`, `core/embeddedllm/memory.go`) на Blackwell: сколько занимают weights, spill (CPU-mapped-часть весов), recurrent state и compute-буферы (device/host) при разных `-c` и `-ctk`/`-ctv`. Числа сверяются с ожиданиями планировщика (`reserve_compute_meta`-проверки `~llama_context: ... matches expectation` в логах) и служат fixture-данными для калибровки плана памяти под 16-ГБ карту.

## Версии и окружение

| Компонент | Значение | Источник |
|---|---|---|
| Runtime pin | `prism-b10735-842b188` (лог: `build 10735 (842b18804) with GNU 11.4.0 for Linux x86_64`) | шапка лога `common_param` |
| Сборка | cuda-13.3 (`CUDA : ARCHS = 750,800,860,890,900,1200,1210`, `USE_GRAPHS = 1`, `BLACKWELL_NATIVE_FP4 = 1`) | `system_info` лога |
| GPU | NVIDIA GeForce RTX 5060 Ti, 15938 MiB (14453 free до загрузки) — arch 1200/1210, Blackwell | `device_info` лога |
| Прочий хост | RTX 2060 5.7 ГБ (CUDA1, в замерах не задействована), i7-11700KF, 32 ГБ RAM, Linux x86_64 | `device_info` лога |
| Модель | `~/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PQ2_0.gguf` + mmproj (vision-tower) | `load_model` лога |
| Дата замера | 2026-11-03 | — |

## Методика

### Команда запуска llama-server

Форма — канонический argv из `LaunchSpec.Args()` (`core/embeddedllm/server.go`): fit включён и выбирает только `-ngl` (все слои), контекст задаётся явно код-фиксом A (см. [embedded-llm-perf-diagnosis.md](embedded-llm-perf-diagnosis.md)), KV-точность одна для обеих половин кэша:

```
llama-server \
  -m ~/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PQ2_0.gguf \
  --host 127.0.0.1 --port <свободный> \
  -fit on \
  -ngl 99 -fa on \
  -c <32768|65536|131072> -ctk <f16|q8_0> -ctv <f16|q8_0> \
  -np 1 --ctx-checkpoints 32 --no-cache-idle-slots \
  --temp 1.0 --top-p 0.95 --top-k 20 --jinja --no-ui \
  --mmproj <mmproj> --image-max-tokens 1024
```

Каждый прогон — отдельный процесс на своём порту (в замерах 45996–45999). Готовность — строки `model loaded` / `listening on http://127.0.0.1:<порт>`; выгрузка всех слоёв — `load_tensors: offloaded 65/65 layers to GPU`; checkpoint'ы спекулятивного декодирования — `context checkpoints enabled, max = 32, min spacing = 8192`.

### Парсинг breakdown-строк loaded-pass при shutdown

Процесс останавливается (SIGTERM → `operator(): cleaning up before exit...`), и форк печатает **loaded-pass breakdown** памяти через `common_memory_breakdown_print` — по строке на устройство, где `self` раскладывается на `model + context + compute`:

```
| memory breakdown [MiB]  | total   free    self   model   context   compute    unaccounted |
|   - CUDA0 (RTX 5060 Ti) | 15938 = 4581 + (8907 =  6543 +    2197 +     166) +        2449 |
|   - Host                |                  374 =   322 +       0 +      52                |
```

Дополнительно из load-паса берутся точные значения буферов:

| Строка лога | Смысл |
|---|---|
| `load_tensors:        CUDA0 model buffer size` | weights на GPU (6539.67 MiB) |
| `load_tensors:   CPU_Mapped model buffer size` | spill — CPU-mapped-часть весов на host (322.07 MiB) |
| `llama_memory_recurrent:      CUDA0 RS buffer size` | recurrent state гибридных SSM-слоёв, R+S f32 (149.62 MiB) |
| `sched_reserve:      CUDA0 compute buffer size` | compute-буфер на GPU |
| `sched_reserve:  CUDA_Host compute buffer size` | compute-буфер на CPU |

Готовые команды:

```bash
grep -E "model buffer size|RS buffer size|compute buffer size" llama-measure-cuda-*.log
grep -A2 "memory breakdown \[MiB\]" llama-measure-cuda-*.log
```

## Таблица breakdown

Все значения в MiB. Колонка «context» в shutdown-строке = KV-кэш + recurrent (например, 32768/f16: 2048.00 + 149.62 ≈ 2197).

| Лог | `-c` | KV | weights (CUDA0) | spill (Host) | recurrent (RS) | compute device | compute host | shutdown CUDA0: self = model+ctx+compute (free) | shutdown Host: self |
|---|---|---|---|---|---|---|---|---|---|
| `llama-measure-cuda-32768.log` | 32768 | f16 | 6539.67 | 322.07 | 149.62 | 166.02 | 52.02 | 8907 = 6543 + 2197 + 166 (free 4581) | 374 = 322 + 0 + 52 |
| `llama-measure-cuda-65536.log` | 65536 | f16 | 6539.67 | 322.07 | 149.62 | 198.02 | 84.02 | 10987 = 6543 + 4245 + 198 (free 2479) | 406 = 322 + 0 + 84 |
| `llama-measure-cuda-65536-q8.log` | 65536 | q8_0 | 6539.67 | 322.07 | 149.62 | 410.28 | 84.28 | 9279 = 6543 + 2325 + 410 (free 4209) | 406 = 322 + 0 + 84 |
| `llama-measure-cuda-131072-q8.log` | 131072 | q8_0 | 6539.67 | 322.07 | 149.62 | 730.28 | 148.28 | 11775 = 6543 + 4501 + 730 (free 1702) | 470 = 322 + 0 + 148 |

KV-кэш из load-паса (16 attention-слоёв): 2048.00 (32768/f16), 4096.00 (65536/f16), 2176.00 (65536/q8_0), 4352.00 (131072/q8_0); recurrent: R (f32) 5.62 + S (f32) 144.00 = 149.62 во всех прогонах.

## Наблюдения

- **Weights/spill/recurrent постоянны** — модель одна: 6539.67 MiB весов на GPU + 322.07 MiB CPU-mapped spill + 149.62 MiB recurrent. Сумма CUDA0 `model` в shutdown-breakdown (6543) — это weights с округлением/overhead лоадера.
- **Compute-буфер растёт с контекстом и квантованием KV**: 166 → 198 (f16, ×2 контекст), 410 → 730 (q8_0, 64k → 131k). q8_0 при 64k держит compute-device вдвое больше f16 (410 против 198), но экономит вдвое на KV (2176 против 4096) — суммарно 9279 против 10987 MiB self.
- **131072/q8_0 — худший замеренный профиль: 11775 MiB self, free 1702** — на 16-ГБ карте влезает, но впритык; 131072/f16 (≈ +2.2 ГБ KV к 64k-профилю) не замерялся и по экстраполяции ушёл бы в своп/otkat fit'а.
- **~2.4–2.5 ГБ unaccounted** на CUDA0 во всех прогонах — CUDA-контекст, workspace cuBLAS/graph-пулов и mmproj; константа замера, не масштабируется с `-c`.
- **Host-compute растёт с KV-квантованием/контекстом**: 52 (32k) → 84 (64k) → 148 (131k/q8_0) — дегазация dequant-KV на CPU-стороне графа.
- Все аллокации легли на CUDA0 (строка CUDA1 в breakdown отсутствует) — вторая карта хоста в замерах не участвовала.

## Файлы и целостность

Логи — **локальные артефакты замера** в `core/embeddedllm/testdata/cuda-measured/`
(полные stdout/stderr llama-server, verbosity max). В репозиторий они **не
входят**: `*.log` исключён `.gitignore` намеренно. Durable-запись чисел в репо —
транскрипция выше и replay-тест `TestCUDAMeasuredProfileReplay`
(`core/embeddedllm/memory_test.go`), проверяющий каждую строку. Таблица ниже —
манифест целостности локальных файлов: держатель оригиналов может сверить с
ними транскрипцию.

| Файл | Порт | SHA256 |
|---|---|---|
| `llama-measure-cuda-32768.log` | 45998 | `714e3e9cda8ff574faa478eb1d93198e7844df7113c7396462426aec8cdc89bb` |
| `llama-measure-cuda-65536.log` | 45999 | `2e83f1a4430cd50d8c2d256437d6abb82095b8ce9f76b5d7b8462847762eb146` |
| `llama-measure-cuda-65536-q8.log` | 45996 | `2b69731f89cb1dc20dfa7287636bd6f17b1a2e9733eb2b45b19d86abf28ff2c8` |
| `llama-measure-cuda-131072-q8.log` | 45997 | `08bffb384045da6aaef86d362c8079418bb1b14a191cdc75ec37134816d51a42` |
