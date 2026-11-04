#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SP4RK_ROOT="${SP4RK_ROOT:-$(cd "$ROOT/../sp4rk" && pwd)}"
BENCHTIME="${BENCHTIME:-1x}"
COUNT="${COUNT:-1}"
GRID_WORKERS="${GRID_WORKERS:-1 2}"
GRID_THREADS="${GRID_THREADS:-0 1 2 4}"
GRID_BATCHES="${GRID_BATCHES:-8 32}"
GRID_SEQUENCES="${GRID_SEQUENCES:-64 512}"
GRID_MEMORY_CAP_MIB="${GRID_MEMORY_CAP_MIB:-4096}"

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
(
  cd "$WORK_DIR"
  GOWORK=off go work init "$ROOT" "$SP4RK_ROOT"
)
WORK_FILE="$WORK_DIR/go.work"

MODEL="${EMBEDDING_TEST_MODEL_PATH:-$ROOT/.cache/models/jina-v2-small.onnx}"
TOKENIZER="${EMBEDDING_TEST_TOKENIZER_PATH:-$ROOT/.cache/models/jina-v2-small-tokenizer.json}"
case "$(uname -s)" in
  Darwin) default_library="$ROOT/.cache/libonnxruntime.dylib" ;;
  Linux) default_library="$ROOT/.cache/libonnxruntime.so" ;;
  *) default_library="$ROOT/.cache/onnxruntime.dll" ;;
esac
LIBRARY="${EMBEDDING_TEST_LIBRARY_PATH:-$default_library}"

printf '## vector-index pipeline (deterministic embedder)\n'
(
  cd "$ROOT"
  GOWORK="$WORK_FILE" go test -run '^$' -bench '^BenchmarkIndexingPipeline$' \
    -benchtime="$BENCHTIME" -count="$COUNT" -benchmem ./core/vectorindex
)

printf '\n## embedding/persistence pipelining gate (real ONNX, isolated process per case)\n'
if [[ ! -f "$MODEL" || ! -f "$TOKENIZER" || ! -f "$LIBRARY" ]]; then
  printf 'SKIP: ONNX assets unavailable.\n'
else
  (
    cd "$ROOT"
    GOWORK="$WORK_FILE" EMBEDDING_TEST_MODEL_PATH="$MODEL" \
    EMBEDDING_TEST_TOKENIZER_PATH="$TOKENIZER" \
    EMBEDDING_TEST_LIBRARY_PATH="$LIBRARY" \
      go test -run '^$' -bench '^BenchmarkRealONNXIndexingPersistence$' \
        -benchtime=1x -count=5 -benchmem ./core/vectorindex
  )
fi

printf '\n## ADR-104 store protocol (deterministic embedder, isolated process per case)\n'
# BENCHMARKS.md "Multi-branch / multi-root store protocol": every case runs in
# its own process (peak-RSS discipline). PROTOCOL_CORPUS=large switches the
# corpus; the branch/root grids below cover the budget-table anchors.
STORE_PROTOCOL_SCENARIOS="${STORE_PROTOCOL_SCENARIOS:-cold_full cold_full_legacy warm_attach branch_fork_reuse branch_switch incremental migration eviction concurrent_roots}"
for scenario in $STORE_PROTOCOL_SCENARIOS; do
  case "$scenario" in
    warm_attach)
      # The warm-attach budget is anchored at 12 branches; 1 and 4 document
      # the attach-cost slope across historical branch counts.
      for branches in ${STORE_PROTOCOL_BRANCHES:-1 4 12}; do
        printf '\n### scenario=%s branches=%s\n' "$scenario" "$branches"
        (
          cd "$ROOT"
          GOWORK="$WORK_FILE" PROTOCOL_BRANCHES="$branches" \
            go test -run '^$' -bench "^BenchmarkStoreProtocol$/^sub=${scenario}$" \
              -benchtime="$BENCHTIME" -count="$COUNT" -benchmem ./core/vectorindex
        )
      done
      ;;
    concurrent_roots)
      # The scenario is "searchers on R-1 warm roots while one root
      # cold-builds"; the harness rejects R<2 (one cold indexer + one
      # searcher). A roots=1 cell degenerates to the unloaded cold_full
      # case the grid already measures, so it is skipped loudly here
      # instead of failing the grid.
      for roots in ${STORE_PROTOCOL_ROOTS:-1 2 4}; do
        if [[ "$roots" -lt 2 ]]; then
          printf '\n### scenario=%s roots=%s SKIP\n' "$scenario" "$roots"
          printf 'concurrent_roots needs at least 2 roots (one cold indexer + one searcher); roots=1 degenerates to the unloaded cold_full case this grid already measures.\n'
          continue
        fi
        printf '\n### scenario=%s roots=%s\n' "$scenario" "$roots"
        (
          cd "$ROOT"
          GOWORK="$WORK_FILE" PROTOCOL_ROOTS="$roots" \
            go test -run '^$' -bench "^BenchmarkStoreProtocol$/^sub=${scenario}$" \
              -benchtime="$BENCHTIME" -count="$COUNT" -benchmem ./core/vectorindex
        )
      done
      ;;
    *)
      printf '\n### scenario=%s\n' "$scenario"
      (
        cd "$ROOT"
        GOWORK="$WORK_FILE" \
          go test -run '^$' -bench "^BenchmarkStoreProtocol$/^sub=${scenario}$" \
            -benchtime="$BENCHTIME" -count="$COUNT" -benchmem ./core/vectorindex
      )
      ;;
  esac
done

printf '\n## ONNX embedding matrix (isolated process per case)\n'
if [[ ! -f "$MODEL" || ! -f "$TOKENIZER" || ! -f "$LIBRARY" ]]; then
  printf 'SKIP: ONNX assets unavailable. Set EMBEDDING_TEST_MODEL_PATH, EMBEDDING_TEST_TOKENIZER_PATH, and EMBEDDING_TEST_LIBRARY_PATH.\n'
  exit 0
fi

for mode in fixed buckets; do
  for corpus in short mixed; do
    for batch in 8 16 32 64; do
      regex="^BenchmarkEmbedderBatch$/^mode=${mode}$/^corpus=${corpus}$/^batch=${batch}$"
      (
        cd "$SP4RK_ROOT"
        GOWORK="$WORK_FILE" EMBEDDING_TEST_MODEL_PATH="$MODEL" \
        EMBEDDING_TEST_TOKENIZER_PATH="$TOKENIZER" \
        EMBEDDING_TEST_LIBRARY_PATH="$LIBRARY" \
          go test -run '^$' -bench "$regex" -benchtime="$BENCHTIME" \
            -count="$COUNT" -benchmem ./embedding
      )
    done
  done
done

printf '\n## ONNX dynamic-shape evaluation (isolated process per corpus)\n'
for corpus in short mixed; do
  regex="^BenchmarkEmbedderDynamicShape$/^corpus=${corpus}$"
  (
    cd "$SP4RK_ROOT"
    GOWORK="$WORK_FILE" EMBEDDING_TEST_MODEL_PATH="$MODEL" \
    EMBEDDING_TEST_TOKENIZER_PATH="$TOKENIZER" \
    EMBEDDING_TEST_LIBRARY_PATH="$LIBRARY" \
      go test -run '^$' -bench "$regex" -benchtime="$BENCHTIME" \
        -count="$COUNT" -benchmem ./embedding
  )
done

printf '\n## ONNX multi-session grid (isolated process per case)\n'
for workers in $GRID_WORKERS; do
  for threads in $GRID_THREADS; do
    for batch in $GRID_BATCHES; do
      for sequence in $GRID_SEQUENCES; do
        printf '\n### workers=%s threads=%s batch=%s sequence=%s\n' "$workers" "$threads" "$batch" "$sequence"
        (
          cd "$SP4RK_ROOT"
          GOWORK="$WORK_FILE" EMBEDDING_TEST_MODEL_PATH="$MODEL" \
          EMBEDDING_TEST_TOKENIZER_PATH="$TOKENIZER" \
          EMBEDDING_TEST_LIBRARY_PATH="$LIBRARY" \
          EMBEDDING_GRID_WORKERS="$workers" EMBEDDING_GRID_THREADS="$threads" \
          EMBEDDING_GRID_BATCH="$batch" EMBEDDING_GRID_SEQUENCE="$sequence" \
          EMBEDDING_GRID_MEMORY_CAP_MIB="$GRID_MEMORY_CAP_MIB" \
            go test -run '^$' -bench '^BenchmarkEmbedderMultiSessionGrid$' \
              -benchtime="$BENCHTIME" -count="$COUNT" -benchmem ./embedding
        )
      done
    done
  done
done
